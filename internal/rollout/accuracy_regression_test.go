package rollout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditPreservedTimestampMiddleRewrite(t *testing.T) {
	home := t.TempDir()
	filler := testRecord{"response_item", map[string]any{"padding": strings.Repeat("x", 900)}, 1}
	records := append([]testRecord{filler}, fixtureTurn("a", 1000, 1000, 100)...)
	records = append(records, filler)
	source := fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	history := resolveFixture(t, home, locateChat)
	r := readFixture(t, history)
	original, err := os.Stat(source.path)
	if err != nil {
		t.Fatal(err)
	}
	changed := strings.Replace(string(source.data), `"input_tokens":1000`, `"input_tokens":9000`, 1)
	if len(changed) != len(source.data) || changed == string(source.data) {
		t.Fatal("bad fixture")
	}
	if err := os.WriteFile(source.path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(source.path, original.ModTime(), original.ModTime()); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	cacheErr := r.SaveCheckpoint(filepath.Join(t.TempDir(), "cache"))
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	fresh := readFixture(t, history)
	if r.State.Snapshot().Total != fresh.State.Snapshot().Total {
		t.Fatalf("cached_total=%d replay_total=%d complete=%t cache_error=%v; live reader stayed stale even after integrity failure", r.State.Snapshot().Total, fresh.State.Snapshot().Total, r.State.Snapshot().HistoryComplete, cacheErr)
	}
}

func TestAuditConflictingDuplicateIsNotCertifiedComplete(t *testing.T) {
	home := t.TempDir()
	records := fixtureTurn("a", 1000, 1000, 100)
	p := map[string]any{"turn_id": "a", "response_id": "response-a", "usage": map[string]any{"input_tokens": 100, "output_tokens": 900}, "thread_token_usage": map[string]any{"input_tokens": 1000, "output_tokens": 900}}
	records = append(records, testRecord{"token_usage_record", p, 2500})
	fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	r := NewHistory(resolveFixture(t, home, locateChat))
	defer r.Close()
	if _, err := r.Update(context.Background()); errors.Is(err, ErrAccountingConflict) {
		return
	} else if err != nil {
		t.Fatal(err)
	}
	s := r.State.Snapshot()
	if s.HistoryComplete {
		t.Fatalf("same response has output 100 and 900; conflict silently ignored: total=%d steps=%d complete=%t", s.Total, s.Steps, s.HistoryComplete)
	}
}

func TestAuditUnreferencedEmbeddedIdentityCannotContribute(t *testing.T) {
	home := t.TempDir()
	records := []testRecord{{"session_meta", map[string]any{"id": locateOther}, 1000}}
	foreign := fixtureTurn("foreign", 2000, 1000, 900)
	for i := range foreign {
		foreign[i].payload["thread_id"] = locateOther
	}
	records = append(records, foreign...)
	records = append(records, testRecord{"session_meta", map[string]any{"id": locateChat}, 5000})
	records = append(records, fixtureTurn("mine", 10000, 1000, 100)...)
	fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	r := NewHistory(resolveFixture(t, home, locateChat))
	defer r.Close()
	_, err := r.Update(context.Background())
	s := r.State.Snapshot()
	if err == nil && s.HistoryComplete && s.Turns != 1 {
		t.Fatalf("unreferenced foreign metadata was accepted: turns=%d steps=%d complete=%t correct_final_identity=%t", s.Turns, s.Steps, s.HistoryComplete, s.ThreadID == locateChat)
	}
}
