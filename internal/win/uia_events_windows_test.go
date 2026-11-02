package win

import (
	"testing"
	"unsafe"
)

func TestCOMEventSinkRetainsNativeObjectUntilLastRelease(t *testing.T) {
	p := allocSink(123, uiaSelection, eventIID, &eventTable)
	if p == 0 {
		t.Fatal("native event sink allocation failed")
	}
	var out uintptr
	result := int32(comCall((*comObject)(unsafe.Pointer(p)), 0, uintptr(unsafe.Pointer(&eventIID)), uintptr(unsafe.Pointer(&out))))
	if result < 0 || out != p {
		releaseSink(p)
		t.Fatal("QueryInterface rejected the event interface")
	}
	if releaseSink(out) != 1 {
		t.Fatal("COM reference was not retained")
	}
	if releaseSink(p) != 0 {
		t.Fatal("native sink did not release its allocation")
	}
}

func TestOldUIAWindowCannotInvalidateNewWindow(t *testing.T) {
	oldHost := uiaHost.Load()
	defer uiaHost.Store(oldHost)
	uiaHost.Store(2)
	uiaKinds.Store(0)
	uiaPending.Store(false)
	signalUIA(&uiaSink{Window: 1}, uiaSelection)
	if uiaKinds.Load() != 0 || uiaPending.Load() {
		t.Fatal("late old-window event affected current selection")
	}
}
