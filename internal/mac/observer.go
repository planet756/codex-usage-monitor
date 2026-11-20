package mac

import "time"

var appNotifications = []string{"AXFocusedWindowChanged", "AXWindowCreated", "AXApplicationActivated", "AXFocusedUIElementChanged"}
var windowNotifications = []string{"AXMoved", "AXResized", "AXMiniaturized", "AXDeminiaturized", "AXSheetCreated", "AXUIElementDestroyed"}

type observerAPI interface {
	create() uintptr // Attached to the current UI run loop on success.
	close(uintptr)
	application() uintptr
	subscribe(uintptr, uintptr, string) int32
	unsubscribe(uintptr, uintptr, string)
	retain(uintptr) uintptr
	release(uintptr)
	equal(uintptr, uintptr) bool
}

type observerState struct {
	api                                   observerAPI
	observer, observerApp, observerWindow uintptr
	appSubscriptions, windowSubscriptions map[string]bool
	retryAt                               time.Time
}

func (s *observerState) remove(el uintptr, subscriptions map[string]bool) {
	if el == 0 {
		return
	}
	if s.observer != 0 {
		for name := range subscriptions {
			s.api.unsubscribe(s.observer, el, name)
		}
	}
	s.api.release(el)
}

func (s *observerState) update(window uintptr, now time.Time) {
	changed := window != 0 && (s.observerWindow == 0 || !s.api.equal(s.observerWindow, window))
	if changed {
		s.remove(s.observerWindow, s.windowSubscriptions)
		s.observerWindow = s.api.retain(window)
		s.windowSubscriptions = nil
	}
	if !changed && now.Before(s.retryAt) {
		return
	}
	s.retryAt = now.Add(2 * time.Second)
	if s.observer == 0 {
		s.observer = s.api.create()
		if s.observer == 0 {
			return
		}
	}
	if s.observerApp == 0 {
		s.observerApp = s.api.application()
	}
	s.subscribe(s.observerApp, appNotifications, &s.appSubscriptions)
	s.subscribe(s.observerWindow, windowNotifications, &s.windowSubscriptions)
}

func (s *observerState) subscribe(el uintptr, names []string, completed *map[string]bool) {
	if el == 0 {
		return
	}
	if *completed == nil {
		*completed = make(map[string]bool)
	}
	for _, name := range names {
		if (*completed)[name] {
			continue
		}
		switch s.api.subscribe(s.observer, el, name) {
		case 0, -25209, -25207: // Registered, already registered, or unsupported.
			(*completed)[name] = true
		}
	}
}

func (s *observerState) close() {
	s.remove(s.observerWindow, s.windowSubscriptions)
	s.remove(s.observerApp, s.appSubscriptions)
	if s.observer != 0 {
		s.api.close(s.observer)
	}
	s.observer, s.observerApp, s.observerWindow = 0, 0, 0
}
