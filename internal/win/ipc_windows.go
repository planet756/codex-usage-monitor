package win

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"time"

	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/live"
	"golang.org/x/sys/windows"
)

type route struct {
	ID         string
	Host       string
	ViewReason string
	Connected  bool
	Live       live.View
}

func followIPC(ctx context.Context, views <-chan viewSelection, out chan<- route) {
	publish := func(r route) {
		select {
		case out <- r:
		case <-ctx.Done():
		}
	}
	delay := 250 * time.Millisecond
	var currentView viewSelection
	for ctx.Err() == nil {
		h, e := windows.CreateFile(utf(`\\.\pipe\codex-ipc`), windows.GENERIC_READ|windows.GENERIC_WRITE, 0, nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OVERLAPPED, 0)
		if e != nil {
			publish(route{})
			select {
			case <-ctx.Done():
				return
			case <-time.After(delay):
			}
			delay = min(delay*2, 5*time.Second)
			continue
		}
		delay = 250 * time.Millisecond
		// os.File's Windows poller supports cancellable overlapped named-pipe I/O.
		f := os.NewFile(uintptr(h), "codex-ipc")
		if f == nil {
			windows.CloseHandle(h)
			return
		}
		closed := make(chan struct{})
		stop := context.AfterFunc(ctx, func() { defer close(closed); f.Close() })
		var nonce [16]byte
		_, _ = rand.Read(nonce[:])
		e = ipc.WriteFrame(f, map[string]any{"type": "request", "requestId": hex.EncodeToString(nonce[:]), "sourceClientId": "initializing-client", "version": 0, "method": "initialize", "params": map[string]string{"clientType": "codex-usage-monitor"}})
		if e == nil {
			publish(route{Connected: true})
			e = followConnection(ctx, f, views, &currentView, publish)
		}
		if !stop() {
			<-closed
		}
		f.Close()
		publish(route{})
		if errors.Is(e, context.Canceled) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, 5*time.Second)
	}
}

// A bounded reader allows native view changes to unsubscribe an idle chat
// immediately, without waiting for another frame on that chat's stream.
func followConnection(parent context.Context, f *os.File, views <-chan viewSelection, current *viewSelection, publish func(route)) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	type incoming struct {
		frame *ipc.Frame
		err   error
	}
	frames := make(chan incoming, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		var reader ipc.FrameReader
		for {
			b, err := reader.Read(f)
			select {
			case frames <- incoming{b, err}:
			case <-ctx.Done():
				return
			}
			if err != nil && !errors.Is(err, ipc.ErrFrameSkipped) {
				return
			}
		}
	}()
	defer func() { cancel(); f.Close(); <-done }()
	var session ipc.Session
	session.SetView(current.ID, current.Host)
	var stream live.Stream
	recovery := live.NewRecovery()
	defer recovery.Close()
	update := func(out []any, changed bool) error {
		for _, message := range out {
			if err := ipc.WriteFrame(f, message); err != nil {
				return err
			}
		}
		if changed {
			stream.Reset(session.Active.ID, session.Active.Host, session.Self)
			recovery.Update(false)
			recovery.Update(stream.NeedsResync())
		}
		return nil
	}
	emit := func() {
		publish(route{ID: session.Active.ID, Host: session.Active.Host, ViewReason: current.Reason, Connected: true, Live: stream.View()})
	}
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-recovery.C():
			if stream.NeedsResync() {
				message := stream.Discover()
				if message == nil && stream.Owner() != "" {
					message = session.ResyncTo(stream.Owner())
				}
				if message != nil {
					if err := ipc.WriteFrame(f, message); err != nil {
						return err
					}
					recovery.Attempted()
				} else {
					recovery.Update(false)
				}
			}
			recovery.Update(stream.NeedsResync())
		case view := <-views:
			*current = view
			out, changed := session.SetView(view.ID, view.Host)
			if err := update(out, changed); err != nil {
				return err
			}
			emit()
		case frame := <-frames:
			if errors.Is(frame.err, ipc.ErrFrameSkipped) {
				if errors.Is(frame.err, ipc.ErrFrameOversized) {
					stream.SkipOversized()
				} else {
					stream.Skip()
				}
				recovery.Update(stream.NeedsResync())
				emit()
				continue
			}
			if frame.err != nil {
				return frame.err
			}
			out, selectionChanged := session.Handle(frame.frame)
			if err := update(out, selectionChanged); err != nil {
				return err
			}
			changed, snapshot := stream.Apply(frame.frame)
			if snapshot {
				if err := ipc.WriteFrame(f, session.ResyncTo(stream.Owner())); err != nil {
					return err
				}
			}
			recovery.Update(stream.NeedsResync())
			if changed || selectionChanged {
				emit()
			}
		}
	}
}
