package mac

import (
	"testing"
	"time"
)

type fakeObserver struct {
	*fakeAX
	creates, closes int
	createFailures  int
	subscribes      map[axAttributeKey]int
	unsubscribes    map[axAttributeKey]int
	results         map[axAttributeKey]int32
}

func newFakeObserver() *fakeObserver {
	f := newFakeAX()
	f.refs[1] = 0
	return &fakeObserver{fakeAX: f, subscribes: make(map[axAttributeKey]int), unsubscribes: make(map[axAttributeKey]int), results: make(map[axAttributeKey]int32)}
}
func (f *fakeObserver) create() uintptr {
	f.creates++
	if f.creates <= f.createFailures {
		return 0
	}
	return 100
}
func (f *fakeObserver) close(uintptr)        { f.closes++ }
func (f *fakeObserver) application() uintptr { return f.retain(1) }
func (f *fakeObserver) subscribe(_ uintptr, el uintptr, name string) int32 {
	key := axAttributeKey{el, name}
	f.subscribes[key]++
	return f.results[key]
}
func (f *fakeObserver) unsubscribe(_ uintptr, el uintptr, name string) {
	f.unsubscribes[axAttributeKey{el, name}]++
}

func TestObserverRecoversAfterPermissionGrantAndSameWindowRegistrationFailure(t *testing.T) {
	f := newFakeObserver()
	f.createFailures = 1
	failed := axAttributeKey{20, "AXMoved"}
	f.results[failed] = -25204
	s := observerState{api: f}
	now := time.Now()
	s.update(20, now)
	if f.creates != 1 || s.observer != 0 || s.observerWindow != 20 {
		t.Fatalf("initial unavailable observer: %+v", s)
	}
	s.update(20, now.Add(time.Second))
	if f.creates != 1 {
		t.Fatal("creation retried without backoff")
	}
	s.update(20, now.Add(2*time.Second))
	if f.creates != 2 || s.observer == 0 || f.subscribes[failed] != 1 {
		t.Fatalf("observer did not recover: %+v", s)
	}
	delete(f.results, failed)
	s.update(20, now.Add(4*time.Second))
	if f.subscribes[failed] != 2 || !s.windowSubscriptions["AXMoved"] {
		t.Fatal("same-window failed notification was never retried")
	}
	if f.subscribes[axAttributeKey{20, "AXResized"}] != 1 {
		t.Fatal("successful registration was needlessly repeated")
	}
	s.close()
	if f.closes != 1 || f.refs[1] != 0 || f.refs[20] != 0 {
		t.Fatalf("observer resources leaked: %+v", f)
	}
}

func TestObserverUnsupportedAndAlreadyRegisteredAreTerminalUntilWindowChanges(t *testing.T) {
	f := newFakeObserver()
	unsupported := axAttributeKey{20, "AXSheetCreated"}
	already := axAttributeKey{20, "AXResized"}
	f.results[unsupported] = -25207
	f.results[already] = -25209
	s := observerState{api: f}
	now := time.Now()
	s.update(20, now)
	s.update(20, now.Add(3*time.Second))
	if f.subscribes[unsupported] != 1 || f.subscribes[already] != 1 {
		t.Fatal("terminal registration results retried indefinitely")
	}
	s.update(30, now.Add(3100*time.Millisecond))
	if s.observerWindow != 30 || f.refs[20] != 0 || f.subscribes[axAttributeKey{30, "AXMoved"}] != 1 {
		t.Fatal("window change did not immediately bind a new event source")
	}
	if f.unsubscribes[already] != 1 {
		t.Fatal("old window notification was not removed")
	}
	s.close()
	if f.closes != 1 || f.refs[1] != 0 || f.refs[30] != 0 {
		t.Fatal("observer did not release its app and window")
	}
}
