package win

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func TestIdleIPCConnectionCancellationClosesAndJoinsReader(t *testing.T) {
	name := fmt.Sprintf(`\\.\pipe\cum-test-%d-%d`, os.Getpid(), time.Now().UnixNano())
	path := utf(name)
	server, err := windows.CreateNamedPipe(path, windows.PIPE_ACCESS_DUPLEX|windows.FILE_FLAG_OVERLAPPED,
		windows.PIPE_TYPE_BYTE|windows.PIPE_WAIT, 1, 4096, 4096, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer windows.CloseHandle(server)
	client, err := windows.CreateFile(path, windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil,
		windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		t.Fatal(err)
	}
	f := os.NewFile(uintptr(client), name)
	if f == nil {
		windows.CloseHandle(client)
		t.Fatal("pipe wrapper unavailable")
	}
	defer f.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		var view viewSelection
		done <- followConnection(ctx, f, make(chan viewSelection), &view, func(route) {})
	}()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("connection cancellation: %v", err)
		}
		// Windows File.Stat queries the raw handle and may report the native
		// invalid-handle error instead of wrapping os.ErrClosed (Go 1.26).
		if _, err := f.Stat(); !errors.Is(err, os.ErrClosed) && !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			t.Fatalf("connection returned before closing its pipe: %v", err)
		}
		if _, err := windows.GetFileType(client); !errors.Is(err, windows.ERROR_INVALID_HANDLE) {
			t.Fatalf("native pipe handle still valid after connection return: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("idle pipe reader was not joined after cancellation")
	}
}
