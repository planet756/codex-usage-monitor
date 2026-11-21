// Package monitor owns the shared Desktop connection and accounting lifecycle.
package monitor

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"io"
	"time"

	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/live"
)

type Route struct {
	ID, Host  string
	Connected bool
	Live      live.View
	epoch     uint64
}

func Follow(ctx context.Context, views <-chan View, out chan<- Route, dial func(context.Context) (io.ReadWriteCloser, error)) {
	publish := func(r Route) {
		select {
		case out <- r:
		case <-ctx.Done():
		}
	}
	delay := 250 * time.Millisecond
	var currentView View
	for ctx.Err() == nil {
		f, e := dial(ctx)
		if e == nil {
			delay = 250 * time.Millisecond
			closed := make(chan struct{})
			stop := context.AfterFunc(ctx, func() { defer close(closed); f.Close() })
			var nonce [16]byte
			_, _ = rand.Read(nonce[:])
			e = ipc.WriteFrame(f, map[string]any{"type": "request", "requestId": hex.EncodeToString(nonce[:]), "sourceClientId": "initializing-client", "version": 0, "method": "initialize", "params": map[string]string{"clientType": "codex-usage-monitor"}})
			if e == nil {
				select {
				case currentView = <-views:
				default:
				}
				publish(Route{Connected: true})
				e = FollowConnection(ctx, f, views, &currentView, publish)
			}
			if !stop() {
				<-closed
			}
			f.Close()
			if errors.Is(e, context.Canceled) {
				return
			}
		}
		publish(Route{})
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
func FollowConnection(parent context.Context, f io.ReadWriteCloser, views <-chan View, current *View, publish func(Route)) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { defer close(closed); f.Close() })
	defer func() {
		if !stop() {
			<-closed
		}
	}()
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
		publish(Route{ID: session.Active.ID, Host: session.Active.Host, Connected: true, Live: stream.View(), epoch: current.epoch})
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
			changedView := view.epoch != current.epoch
			*current = view
			out, changed := session.SetView(view.ID, view.Host)
			if err := update(out, changed || changedView); err != nil {
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
