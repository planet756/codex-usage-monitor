package mac

import (
	"context"
	"time"

	"codex-usage-monitor/internal/layout"
)

const (
	axCallTimeout  = 40 * time.Millisecond
	axProbeTimeout = 250 * time.Millisecond
)

// attribute returns an owned reference; array visits borrowed references.
// Keeping this boundary small lets the real selection policy run without AppKit.
type accessibility interface {
	enable(uintptr) int32
	attribute(uintptr, string) (uintptr, int32)
	text(uintptr) string
	boolean(uintptr) bool
	array(uintptr, int, func(uintptr) bool) bool
	bounds(uintptr) (layout.Rect, bool)
	retain(uintptr) uintptr
	release(uintptr)
	equal(uintptr, uintptr) bool
	key(uintptr) uintptr
}

type axQuery struct {
	ax       accessibility
	ctx      context.Context
	complete bool
}

func (q *axQuery) live() bool {
	if q.ctx.Err() != nil {
		q.complete = false
	}
	return q.complete
}

func (q *axQuery) copy(el uintptr, name string) uintptr {
	if el == 0 || !q.live() {
		return 0
	}
	v, code := q.ax.attribute(el, name)
	switch code {
	case -25200, -25202, -25203, -25204, -25211:
		// Failure, invalid element, unavailable API, cannot complete, or not
		// authorized. Unsupported optional attributes and no-value are normal.
		q.complete = false
	}
	if !q.live() {
		if v != 0 {
			q.ax.release(v)
		}
		return 0
	}
	return v
}

func (q *axQuery) text(el uintptr, name string) string {
	v := q.copy(el, name)
	if v == 0 {
		return ""
	}
	defer q.ax.release(v)
	return q.ax.text(v)
}

func (q *axQuery) flag(el uintptr, name string) bool {
	v := q.copy(el, name)
	if v == 0 {
		return false
	}
	defer q.ax.release(v)
	return q.ax.boolean(v)
}

func (q *axQuery) each(el uintptr, name string, limit int, visit func(uintptr) bool) {
	v := q.copy(el, name)
	if v == 0 {
		return
	}
	defer q.ax.release(v)
	if !q.ax.array(v, limit, func(child uintptr) bool { return !q.live() || visit(child) }) {
		q.complete = false
	}
}

func (q *axQuery) bounds(el uintptr) (layout.Rect, bool) {
	if !q.live() {
		return layout.Rect{}, false
	}
	r, ok := q.ax.bounds(el)
	if !ok {
		q.complete = false
	}
	return r, q.live()
}

func (q *axQuery) dialog(el uintptr) bool {
	role := q.text(el, "AXRole")
	subrole := q.text(el, "AXSubrole")
	return dialogRole(role, subrole) || q.flag(el, "AXModal")
}

func dialogRole(role, subrole string) bool {
	return role == "AXSheet" || role == "AXMenu" || subrole == "AXDialog" || subrole == "AXSystemDialog" || subrole == "AXApplicationDialog" || subrole == "AXApplicationAlertDialog"
}

type axActivation struct {
	requested bool
	code      int32
	retryAt   time.Time
}

func (a *axActivation) ensure(ctx context.Context, ax accessibility, app uintptr, now time.Time) {
	if app == 0 || ctx.Err() != nil || a.requested || now.Before(a.retryAt) {
		return
	}
	a.code = ax.enable(app)
	a.requested = a.code == 0
	// Electron debounces successful AXManualAccessibility requests for two
	// seconds. Repeating a successful set on each probe would postpone it
	// indefinitely. Failed requests may be retried after permission is granted.
	a.retryAt = now.Add(3 * time.Second)
}
