package rollout

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"codex-usage-monitor/internal/metrics"
)

func TestOversizedStateIsRejectedBeforeReplacingGoodCache(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "source")
	cache := filepath.Join(dir, "cache")
	os.WriteFile(p, []byte(header+usageLine), 0600)
	r := New(p)
	r.Update(context.Background())
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(cache)
	turn := r.State.Turns["t"]
	turn.Model = make([]metrics.Interval, metrics.MaxIntervals+1)
	if r.SaveCheckpoint(cache) == nil {
		t.Fatal("oversized state was encoded and saved")
	}
	after, _ := os.ReadFile(cache)
	if string(before) != string(after) {
		t.Fatal("budget failure destroyed the good checkpoint")
	}
}

func TestMaximumBudgetCheckpointFitsAndRestores(t *testing.T) {
	dir := t.TempDir()
	p, cache := filepath.Join(dir, "source"), filepath.Join(dir, "cache")
	if err := os.WriteFile(p, []byte(header+usageLine), 0600); err != nil {
		t.Fatal(err)
	}
	r := New(p)
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	turn := r.State.Turns["t"]
	turn.Model = make([]metrics.Interval, metrics.MaxIntervals)
	for i := range turn.Model {
		turn.Model[i] = metrics.Interval{Start: 1_700_000_000_000 + int64(i)*3, End: 1_700_000_000_002 + int64(i)*3}
	}
	if err := r.SaveCheckpoint(cache); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(cache)
	if info.Size() > MaxCheckpointBytes {
		t.Fatalf("bounded state exceeded cache ceiling: %d", info.Size())
	}
	resumed := New(p)
	if !resumed.RestoreCheckpoint(cache) || len(resumed.State.Turns["t"].Model) != metrics.MaxIntervals {
		t.Fatal("maximum-budget checkpoint failed round trip")
	}
	t.Logf("maximum-budget checkpoint: %d bytes", info.Size())
}

func TestCacheQuotaPreservesActiveAndForeignFiles(t *testing.T) {
	dir := t.TempDir()
	keep := filepath.Join(dir, fmt.Sprintf("%064x.json", 0))
	for i := 0; i < CacheFiles+3; i++ {
		p := filepath.Join(dir, fmt.Sprintf("%064x.json", i))
		os.WriteFile(p, []byte("x"), 0600)
		stamp := time.Unix(int64(i+1), 0)
		os.Chtimes(p, stamp, stamp)
	}
	foreign := filepath.Join(dir, "notes.json")
	os.WriteFile(foreign, []byte("keep"), 0600)
	for _, kind := range []string{"seen"} {
		path := indexCachePath(keep, kind)
		if err := os.WriteFile(path, []byte("active"), 0600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(path, time.Unix(1, 0), time.Unix(1, 0)); err != nil {
			t.Fatal(err)
		}
	}
	pruneMu.Lock()
	lastPrune = time.Time{}
	pruneMu.Unlock()
	PruneCache(dir, keep)
	if _, err := os.Stat(keep); err != nil {
		t.Fatal("active checkpoint was removed")
	}
	for _, kind := range []string{"seen"} {
		if _, err := os.Stat(indexCachePath(keep, kind)); err != nil {
			t.Fatal("active index sidecar was removed")
		}
	}
	if b, _ := os.ReadFile(foreign); string(b) != "keep" {
		t.Fatal("non-cache file was removed")
	}
	entries, _ := os.ReadDir(dir)
	n := 0
	for _, e := range entries {
		if len(e.Name()) == 69 && (strings.HasSuffix(e.Name(), ".json") || strings.HasSuffix(e.Name(), ".seen") || strings.HasSuffix(e.Name(), ".turn")) {
			n++
		}
	}
	if n > CacheFiles {
		t.Fatal("cache file quota was not enforced")
	}
}
