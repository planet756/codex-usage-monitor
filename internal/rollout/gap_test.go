package rollout

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"codex-usage-monitor/internal/metrics"
)

func withoutOrdinals(t *testing.T, data []byte, removed ...int64) []byte {
	t.Helper()
	var out []byte
	for _, line := range bytes.SplitAfter(data, []byte("\n")) {
		if len(line) == 0 {
			continue
		}
		var r struct{ Ordinal int64 }
		if err := json.Unmarshal(line, &r); err != nil {
			t.Fatal(err)
		}
		keep := true
		for _, ordinal := range removed {
			keep = keep && r.Ordinal != ordinal
		}
		if keep {
			out = append(out, line...)
		}
	}
	return out
}

func requireHistoryGap(t *testing.T, s metrics.Snapshot, missing int64) {
	t.Helper()
	if s.MissingRecords != missing || s.HistoryComplete || s.CountsComplete || s.TimingComplete || !s.HasCounts {
		t.Fatalf("gap was concealed or lost: %+v", s)
	}
}

func TestHistoryGapCountsUsageAndTPS(t *testing.T) {
	for _, tc := range []struct {
		name    string
		removed int64
		count   int64
		skipped int64
	}{
		{"response", 10, 2, 0},
		{"timing", 8, 3, 1},
		{"turn_start", 7, 3, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			records := append(fixtureTurn("a", 1000, 1000, 100), fixtureTurn("b", 10000, 1000, 900)...)
			records = append(records, fixtureTurn("c", 20000, 2000, 200)...)
			// A repeated response after the gap must still be counted exactly once.
			records = append(records, fixtureTurn("a", 1000, 1000, 100)[3])
			f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
			data := withoutOrdinals(t, f.data, tc.removed)
			if err := os.WriteFile(f.path, data, 0600); err != nil {
				t.Fatal(err)
			}
			r := readFixture(t, resolveFixture(t, home, locateChat))
			s := r.State.Snapshot()
			requireHistoryGap(t, s, 1)
			if s.Turns != tc.count || s.Steps != tc.count || !s.HasUsage || s.UsageStale || s.Total != 20200 ||
				s.DecodeSamples != 2 || s.DecodeSkipped != tc.skipped || s.DecodeTokens != 300 || s.DecodeMS != 3000 || s.TPS == nil || *s.TPS != 100 {
				t.Fatalf("gap changed verified counts, latest cumulative usage, or TPS: %+v", s)
			}
			after, err := os.ReadFile(f.path)
			if err != nil || !bytes.Equal(after, data) {
				t.Fatal("source log changed during replay", err)
			}
		})
	}
}

func TestHistoryGapAppendCheckpointAndRepair(t *testing.T) {
	home := t.TempDir()
	records := append(fixtureTurn("a", 1000, 1000, 100), testRecord{"response_item", map[string]any{"type": "removed_item"}, 3000}, testRecord{"response_item", map[string]any{"type": "retained_item"}, 4000})
	records = append(records, fixtureTurn("b", 10000, 2000, 300)...)
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
	if err := os.WriteFile(f.path, f.data[:f.offsets[7]], 0600); err != nil {
		t.Fatal(err)
	}
	h := resolveFixture(t, home, locateChat)
	r := readFixture(t, h)
	cache := filepath.Join(t.TempDir(), "state.json")
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	next := NewHistory(h)
	t.Cleanup(next.Close)
	if !next.RestoreCheckpoint(cache) {
		t.Fatal("initial checkpoint failed")
	}
	r.Close()
	// Observe the gap before any fresh accounting arrives.
	appendFile(t, f.path, string(f.data[f.offsets[8]:f.offsets[9]]))
	if _, err := next.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := next.State.Snapshot()
	requireHistoryGap(t, s, 1)
	if s.HasUsage || !s.UsageStale || s.TPS == nil || *s.TPS != 100 {
		t.Fatal("gap exposed stale usage or erased verified TPS", s)
	}
	if err := next.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	resumed := NewHistory(h)
	t.Cleanup(resumed.Close)
	if !resumed.RestoreCheckpoint(cache) || !resumed.State.Snapshot().Equal(s) {
		t.Fatal("checkpoint lost incomplete state")
	}
	next.Close()
	appendFile(t, f.path, string(f.data[f.offsets[9]:]))
	if _, err := resumed.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	s = resumed.State.Snapshot()
	requireHistoryGap(t, s, 1)
	if !s.HasUsage || s.UsageStale || s.Total != 10300 || s.Turns != 2 || s.Steps != 2 || s.DecodeTokens != 400 || s.DecodeMS != 3000 {
		t.Fatal("fresh response did not restore cumulative usage", s)
	}
	cold := readFixture(t, h)
	if !cold.State.Snapshot().Equal(s) {
		t.Fatal("cold and resumed sparse history differ")
	}
	// A restored source is replayed; incomplete state must not survive repair.
	if err := os.WriteFile(f.path, f.data, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := resumed.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	if s = resumed.State.Snapshot(); s.MissingRecords != 0 || !s.HistoryComplete || !s.CountsComplete || !s.TimingComplete {
		t.Fatal("source repair retained stale gap state", s)
	}
	other := fixtureHistory(t, t.TempDir(), locateOther, locateOther, 0, nil, fixtureTurn("other", 30000, 1000, 10)...)
	fresh := New(other.path)
	defer fresh.Close()
	if _, err := fresh.Update(context.Background()); err != nil || fresh.State.MissingRecords != 0 || !fresh.State.Snapshot().CountsComplete {
		t.Fatal("another chat inherited missing history", err)
	}
}

func TestHistoryGapAncestorBoundaryAndCheckpoint(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		t.Run(map[bool]string{false: "plain", true: "compressed"}[compressed], func(t *testing.T) {
			home := t.TempDir()
			records := append(fixtureTurn("a", 1000, 1000, 100), fixtureTurn("b", 10000, 1000, 900)...)
			records = append(records, fixtureTurn("discarded", 15000, 1000, 9000)...)
			root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, records...)
			ref := root.ref(13)
			*ref.Bytes = 1 // Stale byte offset; only the exact ordinal cut may recover it.
			head := fixtureHistory(t, home, locateChat, locateOther, *ref.Ordinal, ref, fixtureTurn("c", 20000, 2000, 200)...)
			if err := os.WriteFile(root.path, withoutOrdinals(t, root.data, 8), 0600); err != nil {
				t.Fatal(err)
			}
			if compressed {
				compressFixture(t, root.path, true)
			}
			h, err := (Locator{Home: home, ActivePath: head.path}).Resolve(context.Background(), locateChat)
			if err != nil {
				t.Fatal(err)
			}
			r := NewHistory(h)
			defer r.Close()
			prefixCache := t.TempDir()
			for {
				if _, err := r.update(context.Background(), 1); err != nil {
					t.Fatal(err)
				}
				if r.Boundary() > 0 {
					if err := r.SaveBoundary(context.Background(), prefixCache); err != nil {
						t.Fatal(err)
					}
				}
				if !r.Loading() {
					break
				}
			}
			s := r.State.Snapshot()
			requireHistoryGap(t, s, 1)
			if s.Turns != 3 || s.Steps != 3 || s.Total != 20200 || s.DecodeTokens != 300 || s.DecodeMS != 3000 || s.DecodeSkipped != 1 || r.RecoveredBoundaries() != 1 {
				t.Fatalf("sparse prefix included reverted work or lost statistics: %+v", s)
			}
			prefix := NewHistory(h)
			defer prefix.Close()
			if mode, ok := prefix.RestoreBest(context.Background(), prefixCache); !ok || mode != "prefix" {
				t.Fatal("sparse ancestor prefix did not restore", mode)
			}
			if _, err := prefix.Update(context.Background()); err != nil || !prefix.State.Snapshot().Equal(s) {
				t.Fatal("prefix restore lost incomplete state", err)
			}
			cache := filepath.Join(t.TempDir(), "state.json")
			if err := r.SaveCheckpoint(cache); err != nil {
				t.Fatal(err)
			}
			next := NewHistory(h)
			defer next.Close()
			if !next.RestoreCheckpoint(cache) || !next.State.Snapshot().Equal(s) {
				t.Fatal("sparse ancestor checkpoint mismatch")
			}
		})
	}
}

func TestHistoryGapAlternateMustMatchAcrossChunks(t *testing.T) {
	for _, extraRecord := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_gap", true: "alternate_has_missing_record"}[extraRecord], func(t *testing.T) {
			home := t.TempDir()
			root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
			ref := root.ref(7)
			*ref.Bytes = 1
			sparse := withoutOrdinals(t, root.data, 2)
			if !extraRecord {
				if err := os.WriteFile(root.path, sparse, 0600); err != nil {
					t.Fatal(err)
				}
			}
			archive := compressFixture(t, root.path, true)
			plain := putLog(t, home, filepath.Join("sessions", "recovered", filepath.Base(root.path)), string(sparse))
			head := fixtureHistory(t, home, locateChat, locateOther, *ref.Ordinal, ref)
			h, err := (Locator{Home: home, ActivePath: head.path}).Resolve(context.Background(), locateChat)
			if err != nil {
				t.Fatal(err)
			}
			r := NewHistory(h)
			defer r.Close()
			for {
				_, err = r.update(context.Background(), 1)
				if err != nil || !r.Loading() {
					break
				}
			}
			if extraRecord {
				if !errors.Is(err, ErrLogAmbiguous) {
					t.Fatalf("alternate's extra record disappeared between chunks: %v", err)
				}
			} else {
				if err != nil {
					t.Fatal(err)
				}
				requireHistoryGap(t, r.State.Snapshot(), 1)
			}
			// The full-prefix fallback used without a reliable native change
			// stamp must make the same equivalence decision as chunked replay.
			s := h.Segments[0]
			e := (&BoundaryCache{}).equivalent(context.Background(), logNode{path: plain}, logNode{path: archive, archive: s.Alternate}, s)
			if extraRecord && !errors.Is(e, ErrLogAmbiguous) || !extraRecord && e != nil {
				t.Fatal("full-prefix equivalence disagreed with sparse replay", e)
			}
			r.Close()
			for _, path := range []string{plain, archive} {
				if err := os.Remove(path); err != nil {
					t.Fatal("sparse source handle retained after Close", err)
				}
			}
		})
	}
}

func TestHistoryGapCannotSubstituteMissingBoundary(t *testing.T) {
	for _, compressed := range []bool{false, true} {
		home := t.TempDir()
		root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, append(fixtureTurn("a", 1000, 1000, 100), fixtureTurn("discarded", 10000, 1000, 900)...)...)
		ref := root.ref(7)
		*ref.Bytes = 1
		if err := os.WriteFile(root.path, withoutOrdinals(t, root.data, 6), 0600); err != nil {
			t.Fatal(err)
		}
		if compressed {
			compressFixture(t, root.path, true)
		}
		head := fixtureHistory(t, home, locateChat, locateOther, *ref.Ordinal, ref)
		if _, err := (Locator{Home: home, ActivePath: head.path}).Resolve(context.Background(), locateChat); !errors.Is(err, ErrHistoryBoundary) {
			t.Fatalf("missing cut was replaced with a different history boundary: %v", err)
		}
	}
}

func TestHistoryGapCheckpointRejectsFalseCompleteness(t *testing.T) {
	home := t.TempDir()
	f := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	if err := os.WriteFile(f.path, withoutOrdinals(t, f.data, 2, 3), 0600); err != nil {
		t.Fatal(err)
	}
	h := resolveFixture(t, home, locateChat)
	r := readFixture(t, h)
	requireHistoryGap(t, r.State.Snapshot(), 2)
	cache := filepath.Join(t.TempDir(), "state.json")
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"history", "counts", "timing", "negative", "excessive", "old_format"} {
		t.Run(mode, func(t *testing.T) {
			var c checkpoint
			if err := json.Unmarshal(raw, &c); err != nil {
				t.Fatal(err)
			}
			switch mode {
			case "history":
				c.State.HistoryComplete = true
			case "counts":
				c.State.CountsIncomplete = false
			case "timing":
				c.State.TimingComplete = true
			case "negative":
				c.State.MissingRecords = -1
			case "excessive":
				c.State.MissingRecords = math.MaxInt64
			case "old_format":
				c.Version = checkpointVersion - 1
			}
			c.Integrity, err = c.integrity()
			if err != nil {
				t.Fatal(err)
			}
			b, err := json.Marshal(c)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(cache, b, 0600); err != nil {
				t.Fatal(err)
			}
			next := NewHistory(h)
			defer next.Close()
			if next.RestoreCheckpoint(cache) {
				t.Fatal("invalid sparse state accepted")
			}
			if _, err := next.Update(context.Background()); err != nil || !next.State.Snapshot().Equal(r.State.Snapshot()) {
				t.Fatal("invalid cache did not fall back to accurate replay", err)
			}
		})
	}
}

func TestHistoryGapRejectsInvalidSequenceAndBounds(t *testing.T) {
	for _, ordinal := range []string{"0", "-1", "9223372036854775807", "null"} {
		t.Run(ordinal, func(t *testing.T) {
			f := fixtureHistory(t, t.TempDir(), locateChat, locateChat, 0, nil, testRecord{"response_item", map[string]any{}, 2})
			data := strings.Replace(string(f.data), `"ordinal":1`, `"ordinal":`+ordinal, 1)
			if err := os.WriteFile(f.path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			r := New(f.path)
			defer r.Close()
			if _, err := r.Update(context.Background()); !errors.Is(err, ErrHistoryOrdinal) {
				t.Fatal("invalid sequence accepted", err)
			}
		})
	}
	f := fixtureHistory(t, t.TempDir(), locateChat, locateChat, 0, nil, testRecord{"response_item", map[string]any{}, 2})
	data := strings.Replace(string(f.data), `"ordinal":1`, `"ordinal":9223372036854775806`, 1)
	if err := os.WriteFile(f.path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(f.path)
	defer r.Close()
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	requireHistoryGap(t, r.State.Snapshot(), math.MaxInt64-2)
}
