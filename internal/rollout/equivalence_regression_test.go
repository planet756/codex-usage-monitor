package rollout

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func auditRecords() []testRecord {
	var records []testRecord
	for i := 0; i < 5; i++ {
		batch := fixtureTurn(fmt.Sprint("turn-", i), 1000+int64(i)*5000, 1000+int64(i)*200, 100+int64(i)*25)
		records = append(records, batch...)
	}
	records = append(records, testRecord{"response_item", map[string]any{"note": "分段与缓存🧭测试"}, 1})
	return records
}

func TestAuditEquivalentSegmentCuts(t *testing.T) {
	records := auditRecords()
	home := t.TempDir()
	fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	expected := readFixture(t, resolveFixture(t, home, locateChat)).State.Snapshot()
	for cut := 0; cut <= len(records); cut++ {
		t.Run(fmt.Sprint(cut), func(t *testing.T) {
			d := t.TempDir()
			root := fixtureHistory(t, d, locateChat, locateChat, 0, nil, records...)
			base := root.ref(cut + 1)
			fixtureHistory(t, d, locateChat, locateOther, *base.Ordinal, base, records[cut:]...)
			got := readFixture(t, resolveFixture(t, d, locateChat)).State.Snapshot()
			if !got.Equal(expected) {
				t.Fatalf("segmentation changed metrics: cut=%d got=%+v want=%+v", cut, got, expected)
			}
		})
	}
}

func TestAuditEquivalentAppendAndResumeAtEveryByte(t *testing.T) {
	home := t.TempDir()
	source := fixtureHistory(t, home, locateChat, locateChat, 0, nil, auditRecords()...)
	h := resolveFixture(t, home, locateChat)
	expected := readFixture(t, h).State.Snapshot()
	initial := int(source.offsets[1])
	for trial := 0; trial < 24; trial++ {
		t.Run(fmt.Sprint(trial), func(t *testing.T) {
			if err := os.WriteFile(source.path, source.data[:initial], 0600); err != nil {
				t.Fatal(err)
			}
			r := NewHistory(h)
			defer func() { r.Close() }()
			cache := filepath.Join(t.TempDir(), "cache")
			for cursor, round := initial, 0; cursor < len(source.data); round++ {
				width := 1 + (round*53+trial*97)%521
				end := min(len(source.data), cursor+width)
				f, err := os.OpenFile(source.path, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.Write(source.data[cursor:end])
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
				if _, err := r.Update(context.Background()); err != nil {
					t.Fatal(err)
				}
				if round%7 == trial%7 {
					if err := r.SaveCheckpoint(cache); err != nil {
						t.Fatal(err)
					}
					next := NewHistory(h)
					if !next.RestoreCheckpoint(cache) {
						next.Close()
						t.Fatal("resume rejected")
					}
					r.Close()
					r = next
				}
				cursor = end
			}
			if _, err := r.Update(context.Background()); err != nil {
				t.Fatal(err)
			}
			if !r.State.Snapshot().Equal(expected) {
				t.Fatalf("append/checkpoint schedule changed metrics: trial=%d got=%+v", trial, r.State.Snapshot())
			}
		})
	}
}

type auditMutatingContext struct {
	context.Context
	reader *Reader
	fired  bool
	mutate func()
}

func (c *auditMutatingContext) Err() error {
	if !c.fired && c.reader.State.StepCount > 0 {
		c.fired = true
		c.mutate()
	}
	return c.Context.Err()
}

func TestAuditConcurrentRewriteCannotPublishUnverifiedSnapshot(t *testing.T) {
	home := t.TempDir()
	records := fixtureTurn("a", 1000, 1000, 100)
	records = append(records, testRecord{"response_item", map[string]any{"padding": strings.Repeat("x", 200000)}, 1})
	source := fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	h := resolveFixture(t, home, locateChat)
	r := NewHistory(h)
	defer r.Close()
	updated := strings.ReplaceAll(string(source.data), `"output_tokens":100`, `"output_tokens":900`)
	if len(updated) != len(source.data) || updated == string(source.data) {
		t.Fatal("bad fixture")
	}
	ctx := &auditMutatingContext{Context: context.Background(), reader: r, mutate: func() {
		if err := os.WriteFile(source.path, []byte(updated), 0600); err != nil {
			t.Fatal(err)
		}
		stamp := time.Now().Add(time.Second)
		if err := os.Chtimes(source.path, stamp, stamp); err != nil {
			t.Fatal(err)
		}
	}}
	_, err := r.Update(ctx)
	if !ctx.fired {
		t.Fatal("rewrite was not exercised")
	}
	fresh := readFixture(t, h)
	if err == nil && r.State.Snapshot().HistoryComplete && !r.State.Snapshot().Equal(fresh.State.Snapshot()) {
		t.Fatalf("Update accepted mixed-time source without post-read verification: published_total=%d current_source_total=%d complete=%t", r.State.Snapshot().Total, fresh.State.Snapshot().Total, r.State.Snapshot().HistoryComplete)
	}
}

func TestAuditSettledCountBeyondInMemoryDetailBudget(t *testing.T) {
	home := t.TempDir()
	const steps = 9000
	records := []testRecord{{"event_msg", map[string]any{"type": "task_started", "turn_id": "long"}, 1000}}
	for i := 0; i < steps; i++ {
		records = append(records, testRecord{"token_usage_record", map[string]any{
			"turn_id": "long", "response_id": fmt.Sprint("unique-", i),
			"usage":              map[string]any{"input_tokens": 100, "output_tokens": 10},
			"thread_token_usage": map[string]any{"input_tokens": int64(i+1) * 100, "output_tokens": int64(i+1) * 10},
		}, 2000 + int64(i)})
	}
	fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	r := readFixture(t, resolveFixture(t, home, locateChat))
	s := r.State.Snapshot()
	t.Logf("valid_unique_responses=%d counted_steps=%d counts_complete=%t total_tokens=%d", steps, s.Steps, s.CountsComplete, s.Total)
	if s.Steps != steps || s.Turns != 1 || !s.CountsComplete {
		t.Fatalf("valid settled counts stop at detail retention budget: got=%d expected=%d qualified_as_lower_bound=%t", s.Steps, steps, !s.CountsComplete)
	}
}

func TestExactCountsSurviveTimingDetailBudget(t *testing.T) {
	home := t.TempDir()
	const steps = 9000
	records := []testRecord{{"event_msg", map[string]any{"type": "task_started", "turn_id": "long"}, 1000}}
	for i := 0; i < steps; i++ {
		start := int64(2000 + i*10)
		records = append(records, testRecord{"event_msg", map[string]any{"type": "item_completed", "turn_id": "long", "item": map[string]any{"type": "Reasoning"}, "started_at_ms": start, "completed_at_ms": start + 1}, start + 1})
		records = append(records, testRecord{"token_usage_record", map[string]any{"turn_id": "long", "response_id": fmt.Sprint("r", i), "usage": map[string]any{"input_tokens": 100, "output_tokens": 10}, "thread_token_usage": map[string]any{"input_tokens": (i + 1) * 100, "output_tokens": (i + 1) * 10}}, start + 2})
	}
	fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	r := readFixture(t, resolveFixture(t, home, locateChat))
	x := r.State.Snapshot()
	if x.Steps != steps || x.Turns != 1 || !x.CountsComplete || !x.HistoryComplete || x.TimingComplete || !x.TimingLimited || x.DecodeSamples+x.DecodeSkipped != steps {
		t.Fatalf("timing budget changed exact counts or coverage: %+v", x)
	}
	cache := filepath.Join(t.TempDir(), "checkpoint.json")
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	next := NewHistory(*r.history)
	defer next.Close()
	if !next.RestoreCheckpoint(cache) || !next.State.Snapshot().Equal(x) {
		t.Fatal("bounded timing checkpoint lost exact counts")
	}
}
