package monitor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"codex-usage-monitor/internal/source"
)

func TestVerifiedOwnerReplacesStaleIndexBranch(t *testing.T) {
	home := t.TempDir()
	cache := t.TempDir()
	const id = "00000000-0000-7000-8000-000000000001"
	const child = "00000000-0000-7000-8000-000000000002"
	folder := filepath.Join(home, "sessions")
	if err := os.MkdirAll(folder, 0700); err != nil {
		t.Fatal(err)
	}
	header := "{\"ordinal\":0,\"type\":\"session_meta\",\"payload\":{\"id\":\"" + id + "\",\"history_mode\":\"paginated\"}}\n"
	stale := filepath.Join(folder, "rollout-2026-11-20T10-00-00-"+id+".jsonl")
	current := filepath.Join(folder, "rollout-2026-11-20T10-00-00-"+id+"_"+child+".jsonl")
	b := header + "{\"ordinal\":1,\"type\":\"token_usage_record\",\"payload\":{\"turn_id\":\"old\",\"response_id\":\"revoked\",\"usage\":{\"input_tokens\":100,\"output_tokens\":20},\"thread_token_usage\":{\"input_tokens\":100,\"output_tokens\":20}}}\n"
	if err := os.WriteFile(stale, []byte(b), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(current, []byte(header), 0600); err != nil {
		t.Fatal(err)
	}
	resolve := source.ResolvePOSIX
	if runtime.GOOS == "windows" {
		resolve = source.Resolve
	}
	deps := Dependencies{
		IndexedPath: func(context.Context, string, string, string) (string, error) { return stale, nil },
		Resolve:     resolve,
		Watch:       func(ctx context.Context, _, _ string, _ chan<- struct{}) error { <-ctx.Done(); return ctx.Err() },
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	hints := make(chan string, 1)
	updates := make(chan Update, 8)
	done := make(chan struct{})
	go func() {
		defer close(done)
		runReader(ctx, home, "local", id, cache, time.Now(), hints, &sourceSelection{}, func(u Update) { updates <- u }, deps)
	}()
	defer func() { cancel(); <-done }()
	select {
	case u := <-updates:
		if !u.Snapshot.HasUsage || u.Snapshot.Total != 120 {
			t.Fatalf("initial old branch fixture did not load: usage=%t total=%d", u.Snapshot.HasUsage, u.Snapshot.Total)
		}
	case <-ctx.Done():
		t.Fatal("fixture did not load")
	}
	hints <- current
	for {
		select {
		case u := <-updates:
			if u.BranchVerified && !u.Loading && !u.Snapshot.HasUsage {
				return
			}
		case <-ctx.Done():
			t.Fatal("verified newer empty branch was ignored while SQLite still pointed to the revoked history")
		}
	}
}

func TestHistorySelectionDoesNotReturnToSupersededIndex(t *testing.T) {
	var selected sourceSelection
	samePath := strings.EqualFold
	for _, tc := range []struct{ index, hint, want string }{
		{"revoked", "", "revoked"},
		{"revoked", "new", "new"},
		{"revoked", "", "new"},
		{"NEW", "", "NEW"},
		{"revoked", "", "new"},
		{"index-advanced", "", "index-advanced"},
	} {
		got, err := selected.choose(tc.index, nil, tc.hint, samePath)
		if err != nil || got != tc.want {
			t.Fatalf("index=%q owner=%q: selected %q, %v; want %q", tc.index, tc.hint, got, err, tc.want)
		}
	}
}

func TestReaderManagerJoinsPreviousReaderBeforeSwitch(t *testing.T) {
	const first = "00000000-0000-7000-8000-000000000001"
	const second = "00000000-0000-7000-8000-000000000002"
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, "sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	log := func(id string) string {
		p := filepath.Join(home, "sessions", "rollout-2026-11-02T10-00-00-"+id+".jsonl")
		b := `{"ordinal":0,"type":"session_meta","payload":{"id":"` + id + `","history_mode":"paginated"}}` + "\n" + `{"ordinal":1,"type":"token_usage_record","payload":{"turn_id":"t","response_id":"r","usage":{"input_tokens":100,"output_tokens":20},"thread_token_usage":{"input_tokens":100,"output_tokens":20}}}` + "\n"
		if err := os.WriteFile(p, []byte(b), 0600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	a, b := log(first), log(second)
	resolve := source.ResolvePOSIX
	if runtime.GOOS == "windows" {
		resolve = source.Resolve
	}
	started := make(chan string, 2)
	stopping := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	deps := Dependencies{Resolve: resolve, IndexedPath: func(_ context.Context, _, _, id string) (string, error) {
		if id == first {
			return a, nil
		}
		return b, nil
	}, Watch: func(ctx context.Context, dir, name string, out chan<- struct{}) error {
		started <- name
		<-ctx.Done()
		if name == filepath.Base(a) {
			close(stopping)
			<-release
		}
		return ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	jobs := make(chan readerRequest, 1)
	updates := make(chan Update, 2)
	done := make(chan struct{})
	go func() {
		defer close(done)
		readerManager(ctx, home, filepath.Join(t.TempDir(), "settings.json"), jobs, updates, deps)
	}()
	defer func() {
		releaseOnce.Do(func() { close(release) })
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("reader manager did not join on shutdown")
		}
	}()
	waitStarted := func(want string) {
		t.Helper()
		select {
		case got := <-started:
			if got != filepath.Base(want) {
				t.Fatalf("started %q", got)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("reader did not start")
		}
	}
	jobs <- readerRequest{id: first, host: "local", generation: 1, started: time.Now()}
	waitStarted(a)
	select {
	case u := <-updates:
		if u.ID != first || u.Generation != 1 || !u.Snapshot.HasUsage || u.Snapshot.Total != 120 {
			t.Fatalf("first snapshot: %+v", u)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no accounting snapshot")
	}
	jobs <- readerRequest{id: second, host: "local", generation: 2, started: time.Now()}
	select {
	case <-stopping:
	case <-time.After(3 * time.Second):
		t.Fatal("previous reader was not cancelled")
	}
	select {
	case <-started:
		t.Fatal("replacement reader overlapped the cancelled watcher")
	default:
	}
	releaseOnce.Do(func() { close(release) })
	waitStarted(b)
	select {
	case u := <-updates:
		if u.ID != second || u.Generation != 2 || !u.Snapshot.HasUsage || u.Snapshot.Total != 120 {
			t.Fatalf("replacement snapshot: %+v", u)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("replacement did not publish")
	}
}
