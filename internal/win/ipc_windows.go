package win

import (
	"codex-usage-monitor/internal/monitor"
	"context"
	"golang.org/x/sys/windows"
	"io"
	"os"
)

type route = monitor.Route

func dialIPC(ctx context.Context) (io.ReadWriteCloser, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(utf(`\\.\pipe\codex-ipc`), windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(h), "codex-ipc"), nil
}
func followConnection(ctx context.Context, f *os.File, views <-chan viewSelection, current *viewSelection, publish func(route)) error {
	return monitor.FollowConnection(ctx, f, views, current, publish)
}
