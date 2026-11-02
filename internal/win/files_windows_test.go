package win

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestDirectoryWatchCancellationJoinsPendingIO(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	dir := t.TempDir()
	go func() { done <- watchDirectory(ctx, dir, "rollout.jsonl", make(chan struct{}, 1)) }()
	time.Sleep(20 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("watch cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("directory watch retained pending IO after cancellation")
	}
}
