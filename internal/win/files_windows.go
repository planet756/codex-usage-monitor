package win

import (
	"context"
	"encoding/binary"
	"strings"
	"unicode/utf16"

	"golang.org/x/sys/windows"
)

func watchDirectory(ctx context.Context, dir, name string, out chan<- struct{}) error {
	h, e := windows.CreateFile(utf(dir), windows.FILE_LIST_DIRECTORY, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_BACKUP_SEMANTICS|windows.FILE_FLAG_OVERLAPPED, 0)
	if e != nil {
		return e
	}
	defer windows.CloseHandle(h)
	event, e := windows.CreateEvent(nil, 1, 0, nil)
	if e != nil {
		return e
	}
	defer windows.CloseHandle(event)
	var ov windows.Overlapped
	ov.HEvent = event
	buf := make([]byte, 16<<10)
	for ctx.Err() == nil {
		windows.ResetEvent(event)
		e = windows.ReadDirectoryChanges(h, &buf[0], uint32(len(buf)), false, windows.FILE_NOTIFY_CHANGE_SIZE|windows.FILE_NOTIFY_CHANGE_LAST_WRITE|windows.FILE_NOTIFY_CHANGE_FILE_NAME, nil, &ov, 0)
		if e != nil && e != windows.ERROR_IO_PENDING {
			return e
		}
		// Cancel the *specific* outstanding operation, then wait for completion
		// before reusing its OVERLAPPED or closing the directory handle.
		cancelled := make(chan struct{})
		stop := context.AfterFunc(ctx, func() {
			defer close(cancelled)
			windows.CancelIoEx(h, &ov)
		})
		var n uint32
		e = windows.GetOverlappedResult(h, &ov, &n, true)
		if !stop() {
			<-cancelled
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if e != nil {
			return e
		}
		changed := n == 0
		for at := uint32(0); at+12 <= n; {
			next := binary.LittleEndian.Uint32(buf[at:])
			length := binary.LittleEndian.Uint32(buf[at+8:])
			if length%2 != 0 || at+12+length > n {
				changed = true
				break
			}
			u := make([]uint16, length/2)
			for i := range u {
				u[i] = binary.LittleEndian.Uint16(buf[int(at)+12+i*2:])
			}
			if strings.EqualFold(string(utf16.Decode(u)), name) {
				changed = true
			}
			if next == 0 {
				break
			}
			if next < 12 || next > n-at {
				changed = true
				break
			}
			at += next
		}
		if changed {
			select {
			case out <- struct{}{}:
			default:
			}
		}
	}
	return ctx.Err()
}
