package monitor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"codex-usage-monitor/internal/rollout"
	"codex-usage-monitor/internal/source"
)

func TestSharedResolverRequiresOwnerAndExplicitBranch(t *testing.T) {
	const id = "00000000-0000-7000-8000-000000000001"
	home := t.TempDir()
	p := filepath.Join(home, "sessions", "rollout-2026-11-02T10-00-00-"+id+".jsonl")
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(`{"type":"session_meta","payload":{"id":"`+id+`"}}`+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ResolveActiveThread(context.Background(), home, "local", id, "", nil, source.ResolvePOSIX); !errors.Is(err, rollout.ErrBranchUnverified) {
		t.Fatal(err)
	}
	if _, _, err := ResolveActiveThread(context.Background(), home, "remote", id, p, nil, source.ResolvePOSIX); !errors.Is(err, source.ErrHostUnsupported) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := ResolveActiveThread(ctx, home, "local", id, p, nil, source.ResolvePOSIX); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	resolve := source.ResolvePOSIX
	if runtime.GOOS == "windows" {
		resolve = source.Resolve
	}
	h, plan, err := ResolveActiveThread(context.Background(), home, "local", id, p, nil, resolve)
	if err != nil || h.Head != p || plan.Path != p {
		t.Fatalf("explicit source: %+v %+v %v", h, plan, err)
	}
	if _, _, err = ResolveActiveThread(context.Background(), home, "local", "00000000-0000-7000-8000-000000000002", p, nil, resolve); !errors.Is(err, rollout.ErrChatMismatch) {
		t.Fatal(err)
	}
}
