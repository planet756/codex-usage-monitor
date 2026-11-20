package monitor

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"codex-usage-monitor/internal/source"
)

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
	deps := Dependencies{Resolve: resolve, Watch: func(ctx context.Context, dir, name string, out chan<- struct{}) error {
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
	jobs <- readerRequest{id: first, host: "local", path: a, generation: 1, started: time.Now()}
	waitStarted(a)
	select {
	case u := <-updates:
		if u.ID != first || u.Generation != 1 || !u.Snapshot.HasUsage || u.Snapshot.Total != 120 {
			t.Fatalf("first snapshot: %+v", u)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no accounting snapshot")
	}
	jobs <- readerRequest{id: second, host: "local", path: b, generation: 2, started: time.Now()}
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
