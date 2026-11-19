package monitor

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"codex-usage-monitor/internal/ipc"
)

func TestConnectionCancellationClosesAndJoinsIdleReader(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		var current View
		done <- FollowConnection(ctx, client, make(chan View), &current, func(Route) {})
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle connection did not release its reader")
	}
	if _, err := client.Write([]byte{0}); err == nil {
		t.Fatal("connection still open")
	}
}

func TestConnectionCancelsBlockedWrite(t *testing.T) {
	client, server := net.Pipe()
	defer server.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var current View
		done <- FollowConnection(ctx, client, make(chan View), &current, func(Route) {})
	}()
	if err := ipc.WriteFrame(server, map[string]any{"type": "response", "method": "initialize", "resultType": "success", "result": map[string]string{"clientId": "monitor"}}); err != nil {
		t.Fatal(err)
	}
	// The peer never reads the initialization broadcast.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked write survived cancellation")
	}
}
