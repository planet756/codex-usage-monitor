package win

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"codex-usage-monitor/internal/ipc"
	"golang.org/x/sys/windows"
)

func TestNativeIdleNavigationDiscoversOwnerAndFollowsOnlyThatOwner(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\cum-owner-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	server, err := windows.CreateNamedPipe(utf(name), windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	client, err := windows.CreateFile(utf(name), windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		windows.CloseHandle(server)
		t.Fatal(err)
	}
	if err := windows.ConnectNamedPipe(server, nil); err != nil && !errors.Is(err, windows.ERROR_PIPE_CONNECTED) {
		windows.CloseHandle(server)
		windows.CloseHandle(client)
		t.Fatal(err)
	}
	sf, cf := os.NewFile(uintptr(server), name), os.NewFile(uintptr(client), name)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	views := make(chan viewSelection, 1)
	routes := make(chan route, 16)
	done := make(chan error, 1)
	go func() {
		var current viewSelection
		done <- followConnection(ctx, cf, views, &current, func(r route) {
			select {
			case routes <- r:
			case <-ctx.Done():
			}
		})
	}()
	frames := make(chan *ipc.Frame, 16)
	readDone := make(chan struct{})
	go func() {
		defer close(readDone)
		var reader ipc.FrameReader
		for {
			f, err := reader.Read(sf)
			if err != nil {
				return
			}
			select {
			case frames <- f:
			case <-ctx.Done():
				return
			}
		}
	}()
	defer func() { cancel(); sf.Close(); cf.Close(); <-done; <-readDone }()
	write := func(v any) {
		t.Helper()
		if err := ipc.WriteFrame(sf, v); err != nil {
			t.Fatal(err)
		}
	}
	next := func(method string) *ipc.Frame {
		t.Helper()
		for {
			select {
			case f := <-frames:
				if f.Method == method {
					return f
				}
			case <-ctx.Done():
				t.Fatal("missing outbound message", method)
				return nil
			}
		}
	}
	write(map[string]any{"type": "response", "method": "initialize", "resultType": "success", "result": map[string]string{"clientId": "monitor"}})
	next("client-status-changed")
	const id = "00000000-0000-7000-8000-000000000001"
	views <- viewSelection{ID: id, Host: "local", Reason: "verified"}
	discovery := next("thread-owner-discovery")
	if discovery.Version != 1 || discovery.Params.Host != "local" || discovery.Params.ID != id {
		t.Fatal("wrong discovery routing")
	}
	write(map[string]any{"type": "response", "method": "thread-owner-discovery", "requestId": discovery.RequestID, "resultType": "success", "handledByClientId": "verified-owner"})
	follow := next("thread-stream-following-changed")
	if len(follow.Targets) != 1 || follow.Targets[0] != "verified-owner" {
		t.Fatal("snapshot was not requested from the verified owner")
	}
	write(map[string]any{"type": "broadcast", "method": "thread-stream-state-changed", "version": 11, "sourceClientId": "verified-owner", "targetClientIds": []string{"monitor"},
		"params": map[string]any{"conversationId": id, "hostId": "local", "change": map[string]any{"type": "snapshot", "revision": 1, "conversationState": map[string]string{"rolloutPath": `C:\data\selected.jsonl`}}}})
	for {
		select {
		case r := <-routes:
			if r.Live.RolloutPath != "" {
				if !r.Live.OwnerVerified || r.ID != id {
					t.Fatal("unverified route published")
				}
				return
			}
		case <-ctx.Done():
			t.Fatal("verified source never reached the engine")
			return
		}
	}
}
