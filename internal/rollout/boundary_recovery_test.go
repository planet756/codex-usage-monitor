package rollout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHistoryRewrittenBytesRecoverTheExplicitOrdinalPrefix(t *testing.T) {
	for _, mode := range []string{"expanded", "shrunk_past_eof", "crlf"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			a := fixtureTurn("a", 1000, 1000, 100)
			root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, append(a, fixtureTurn("discarded", 9000, 1000, 900)...)...)
			base := root.ref(1 + len(a))
			if mode == "shrunk_past_eof" {
				*base.Bytes = int64(len(root.data) + 100)
			}
			fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base, fixtureTurn("b", 20000, 2000, 200)...)
			data := string(root.data)
			if mode == "expanded" {
				data = strings.ReplaceAll(data, `"type":`, `"type" : `)
			}
			if mode == "crlf" {
				data = strings.ReplaceAll(data, "\n", "\r\n")
			}
			if err := os.WriteFile(root.path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			h, err := (fixtureLocator{Home: home}).Resolve(context.Background(), locateChat)
			if err != nil {
				t.Fatalf("verified ordinal prefix rejected: %v", err)
			}
			r := readFixture(t, h)
			x := r.State.Snapshot()
			if x.Turns != 2 || x.Steps != 2 || x.DecodeTokens != 300 || x.DecodeMS != 3000 || !x.CountsComplete || !x.HistoryComplete {
				t.Fatalf("included discarded tail or lost valid inherited statistics: %+v", x)
			}
		})
	}
}

func TestHistoryOrdinalRecoveryRejectsConflictingOrUnverifiableBoundaries(t *testing.T) {
	for _, mode := range []string{"conflicting_complete_offset", "gap", "duplicate", "missing_ordinal", "invalid_json", "truncated", "missing_target", "zero_conflict"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
			base := root.ref(len(root.offsets) - 1)
			*base.Bytes = 1 // Always inside the header, including after fixture rewrites.
			data := string(root.data)
			switch mode {
			case "conflicting_complete_offset":
				*base.Bytes = root.offsets[2]
			case "gap":
				data = strings.Replace(data, `"ordinal":2,`, `"ordinal":3,`, 1)
			case "duplicate":
				data = strings.Replace(data, `"ordinal":2,`, `"ordinal":1,`, 1)
			case "missing_ordinal":
				data = strings.Replace(data, `"ordinal":2,`, "", 1)
			case "invalid_json":
				data = strings.Replace(data, `"ordinal":2,`, `"ordinal":,`, 1)
			case "truncated":
				data = data[:len(data)-1]
			case "missing_target":
				*base.Ordinal += 5
			case "zero_conflict":
				*base.Bytes = 0
			}
			if err := os.WriteFile(root.path, []byte(data), 0600); err != nil {
				t.Fatal(err)
			}
			fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base)
			if h, err := (fixtureLocator{Home: home}).Resolve(context.Background(), locateChat); err == nil {
				r := NewHistory(h)
				defer r.Close()
				if _, err := r.Update(context.Background()); err == nil && r.State.Snapshot().HistoryComplete {
					t.Fatal("unverifiable ordinal prefix certified complete")
				}
			}
		})
	}
}

func TestHistoryRecoveredBoundaryCacheRevalidatesChangedSources(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	base := root.ref(len(root.offsets) - 1)
	fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base, fixtureTurn("b", 10000, 1000, 200)...)
	data := strings.ReplaceAll(string(root.data), `"type":`, `"type" : `)
	if err := os.WriteFile(root.path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	cache := &BoundaryCache{}
	l := fixtureLocator{Home: home, Boundaries: cache}
	first, err := l.Resolve(context.Background(), locateChat)
	if err != nil {
		t.Fatal(err)
	}
	second, err := l.Resolve(context.Background(), locateChat)
	if err != nil || first.key() != second.key() || len(cache.entries) != 1 {
		t.Fatalf("recovered boundary not reused: %v", err)
	}
	data = strings.ReplaceAll(data, `"type" : `, `"type"   :   `)
	if err := os.WriteFile(root.path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	third, err := l.Resolve(context.Background(), locateChat)
	if err != nil || third.key() == first.key() {
		t.Fatalf("rewritten boundary was not remapped: %v", err)
	}
	data = strings.Replace(data, `"ordinal":2,`, `"ordinal":3,`, 1)
	if err := os.WriteFile(root.path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := time.Now().Add(time.Second)
	if err := os.Chtimes(root.path, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if h, err := l.Resolve(context.Background(), locateChat); err == nil {
		r := NewHistory(h)
		defer r.Close()
		if _, err := r.Update(context.Background()); err == nil && r.State.Snapshot().HistoryComplete {
			t.Fatal("cached boundary hid a changed invalid source")
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = l.Resolve(ctx, locateChat); !errors.Is(err, context.Canceled) {
		t.Fatal("cancelled recovery continued")
	}
}

func TestHistoryRecoveredPrefixCheckpointAndSourceRewrite(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	base := root.ref(len(root.offsets) - 1)
	fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base, fixtureTurn("b", 10000, 1000, 200)...)
	data := strings.ReplaceAll(string(root.data), `"type":`, `"type" : `)
	if err := os.WriteFile(root.path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	l := fixtureLocator{Home: home, Boundaries: &BoundaryCache{}}
	resolve := func() (History, error) { return l.Resolve(context.Background(), locateChat) }
	tracked := Tracked{ID: locateChat, CacheDir: filepath.Join(home, "monitor-cache"), Resolve: resolve}
	now := time.Unix(100, 0)
	if _, _, err := tracked.Update(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	defer func() { tracked.Reader.Close() }()
	if tracked.Reader.RecoveredBoundaries() != 1 {
		t.Fatal("lost recovered-boundary diagnostic")
	}
	if err := tracked.Save(); err != nil {
		t.Fatal(err)
	}
	h, err := resolve()
	if err != nil {
		t.Fatal(err)
	}
	resumed := NewHistory(h)
	defer resumed.Close()
	if !resumed.RestoreCheckpoint(tracked.CachePath) || !resumed.State.Snapshot().Equal(tracked.Reader.State.Snapshot()) {
		t.Fatal("recovered history checkpoint changed statistics")
	}
	old := tracked.Reader
	data = strings.ReplaceAll(data, `"type" : `, `"type"  :  `)
	if err := os.WriteFile(root.path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	if _, moved, err := tracked.Update(context.Background(), now.Add(time.Minute)); err != nil || !moved || tracked.Reader == old || !old.closed {
		t.Fatalf("source rewrite retained stale boundary reader: %v", err)
	}
	if !tracked.Reader.State.Snapshot().Equal(resumed.State.Snapshot()) {
		t.Fatal("serialization rewrite changed statistics")
	}
}

func TestOrdinalBoundaryScanHonorsCancellationAndRecordBudget(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		size := 256 << 10
		if oversized {
			size = MaxRecordBytes + 1
		}
		home := t.TempDir()
		root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, testRecord{"response_item", map[string]any{"padding": strings.Repeat("x", size)}, 1})
		node, err := nodeAt(context.Background(), root.path)
		if err != nil {
			t.Fatal(err)
		}
		f, err := os.Open(root.path)
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithCancel(context.Background())
		var scanContext context.Context = &limitedContext{Context: ctx, remaining: 3, cancel: cancel}
		if oversized {
			scanContext = ctx
		}
		_, err = ordinalBoundary(scanContext, f, int64(len(root.data)), Segment{StartOrdinal: 0, EndOrdinal: 2, Header: node.headerHash})
		f.Close()
		cancel()
		want := context.Canceled
		if oversized {
			want = ErrHistoryBoundary
		}
		if !errors.Is(err, want) {
			t.Fatalf("bounded recovery returned %v; want %v", err, want)
		}
	}
}
