package mac

import (
	"context"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"syscall"

	"golang.org/x/sys/unix"
)

func dialIPC(home string) func(context.Context) (io.ReadWriteCloser, error) {
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		// Verified against Desktop 26.1027.61615: the current endpoint belongs
		// to CODEX_HOME, not a guessed temporary directory or backend process.
		p := filepath.Join(home, "ipc", "ipc.sock")
		info, err := os.Lstat(filepath.Dir(p))
		if err != nil {
			return nil, err
		}
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || !info.IsDir() || st.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0022 != 0 {
			return nil, errors.New("Desktop IPC directory ownership unavailable")
		}
		info, err = os.Lstat(p)
		if err != nil {
			return nil, err
		}
		st, ok = info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSocket == 0 || st.Uid != uint32(os.Getuid()) {
			return nil, errors.New("Desktop IPC socket ownership unavailable")
		}
		return (&net.Dialer{}).DialContext(ctx, "unix", p)
	}
}

func watchDirectory(ctx context.Context, dir, name string, out chan<- struct{}) error {
	kq, err := unix.Kqueue()
	if err != nil {
		return err
	}
	defer unix.Close(kq)
	var pipe [2]int
	if err = unix.Pipe(pipe[:]); err != nil {
		return err
	}
	defer unix.Close(pipe[0])
	defer unix.Close(pipe[1])
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); _, _ = unix.Write(pipe[1], []byte{1}) })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
	d, err := unix.Open(dir, unix.O_EVTONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	defer unix.Close(d)
	f := -1
	defer func() {
		if f >= 0 {
			unix.Close(f)
		}
	}()
	flags := uint32(unix.NOTE_WRITE | unix.NOTE_EXTEND | unix.NOTE_ATTRIB | unix.NOTE_RENAME | unix.NOTE_DELETE | unix.NOTE_REVOKE)
	changes := make([]unix.Kevent_t, 2)
	unix.SetKevent(&changes[0], pipe[0], unix.EVFILT_READ, unix.EV_ADD|unix.EV_CLEAR)
	unix.SetKevent(&changes[1], d, unix.EVFILT_VNODE, unix.EV_ADD|unix.EV_CLEAR)
	changes[1].Fflags = flags
	if _, err = unix.Kevent(kq, changes, nil, nil); err != nil {
		return err
	}
	// Vnode watches follow an open inode. A pathname can be atomically replaced
	// without changing the reader's selected path, so rebind it here. The
	// directory watch bridges deletion until a replacement appears.
	rearm := func() error {
		next, e := unix.Open(filepath.Join(dir, name), unix.O_EVTONLY|unix.O_CLOEXEC, 0)
		if e == unix.ENOENT {
			if f >= 0 {
				unix.Close(f)
				f = -1
			}
			return nil
		}
		if e != nil {
			return e
		}
		var current, candidate unix.Stat_t
		if e = unix.Fstat(next, &candidate); e != nil {
			unix.Close(next)
			return e
		}
		if f >= 0 && unix.Fstat(f, &current) == nil && current.Dev == candidate.Dev && current.Ino == candidate.Ino {
			unix.Close(next)
			return nil
		}
		var event unix.Kevent_t
		unix.SetKevent(&event, next, unix.EVFILT_VNODE, unix.EV_ADD|unix.EV_CLEAR)
		event.Fflags = flags
		if _, e = unix.Kevent(kq, []unix.Kevent_t{event}, nil, nil); e != nil {
			unix.Close(next)
			return e
		}
		if f >= 0 {
			unix.Close(f) // Closing removes the old vnode registration.
		}
		f = next
		return nil
	}
	if err = rearm(); err != nil {
		return err
	}
	events := make([]unix.Kevent_t, 4)
	for ctx.Err() == nil {
		n, e := unix.Kevent(kq, nil, events, nil)
		if e == unix.EINTR {
			continue
		}
		if e != nil {
			return e
		}
		for _, event := range events[:n] {
			if int(event.Ident) == pipe[0] {
				return ctx.Err()
			}
			if event.Flags&unix.EV_ERROR != 0 && event.Data != 0 {
				return unix.Errno(event.Data)
			}
			if int(event.Ident) == d && event.Fflags&(unix.NOTE_DELETE|unix.NOTE_RENAME|unix.NOTE_REVOKE) != 0 {
				return errors.New("watched directory unavailable")
			}
		}
		if err = rearm(); err != nil {
			return err
		}
		select {
		case out <- struct{}{}:
		default:
		}
	}
	return ctx.Err()
}
