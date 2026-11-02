package rollout

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBoundaryCachePersistsAndRevalidates(t *testing.T) {
	home := t.TempDir()
	root := fixtureHistory(t, home, locateChat, locateChat, 0, nil, fixtureTurn("a", 1000, 1000, 100)...)
	base := root.ref(len(root.offsets) - 1)
	fixtureHistory(t, home, locateChat, locateOther, *base.Ordinal, base, fixtureTurn("b", 10000, 2000, 200)...)
	data := strings.ReplaceAll(string(root.data), `"type":`, `"type" : `)
	if err := os.WriteFile(root.path, []byte(data), 0600); err != nil {
		t.Fatal(err)
	}
	first := &BoundaryCache{}
	l := fixtureLocator{Home: home, Boundaries: first}
	h, err := l.Resolve(context.Background(), locateChat)
	if err != nil {
		t.Fatal(err)
	}
	cache := filepath.Join(t.TempDir(), "boundary.json")
	if err := first.Save(cache); err != nil {
		t.Fatal(err)
	}
	second := &BoundaryCache{}
	if !second.Load(cache) || len(second.entries) != 1 {
		t.Fatal("mapping did not persist")
	}
	l.Boundaries = second
	loaded, err := l.Resolve(context.Background(), locateChat)
	if err != nil || h.key() != loaded.key() || second.dirty {
		t.Fatalf("mapping was not reused: %v", err)
	}
	changed := strings.ReplaceAll(data, `"type" : `, `"type"   :   `)
	if err := os.WriteFile(root.path, []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	updated, err := l.Resolve(context.Background(), locateChat)
	if err != nil {
		t.Fatal(err)
	}
	if updated.Segments[0].Bytes == loaded.Segments[0].Bytes || !second.dirty {
		t.Fatal("changed boundary was reused")
	}
	if got := readFixture(t, updated).State.Snapshot(); got.Turns != 2 || !got.HistoryComplete {
		t.Fatalf("mapping changed statistics: %+v", got)
	}
	raw, err := os.ReadFile(cache)
	if err != nil {
		t.Fatal(err)
	}
	raw[len(raw)/2] ^= 1
	if err := os.WriteFile(cache, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if (&BoundaryCache{}).Load(cache) {
		t.Fatal("corrupt mapping accepted")
	}
	if err := os.WriteFile(cache, make([]byte, MaxCheckpointBytes+1), 0600); err != nil {
		t.Fatal(err)
	}
	if (&BoundaryCache{}).Load(cache) {
		t.Fatal("oversized mapping accepted")
	}
}
