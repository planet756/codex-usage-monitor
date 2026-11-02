package win

import (
	"golang.org/x/sys/windows"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"
)

const uiaSelection = 1
const uiaStructure = 2

var uiaTarget atomic.Uintptr
var uiaHost atomic.Uintptr
var uiaPending atomic.Bool
var uiaKinds atomic.Uint32

// These COM objects live in native memory; only permanent callback addresses
// and scalar fields survive a call. COM references own their exact lifetime.
type uiaSink struct {
	VTable uintptr
	Window uintptr
	Kind   uint32
	Refs   int32
	IID    windows.GUID
}

var unknownIID = windows.GUID{Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
var eventIID = windows.GUID{Data1: 0x146c3c17, Data2: 0xf12e, Data3: 0x4e22, Data4: [8]byte{0x8c, 0x27, 0xf8, 0x94, 0xb9, 0xb7, 0x9c, 0x69}}
var propertyIID = windows.GUID{Data1: 0x40cd37d4, Data2: 0xc756, Data3: 0x4b0c, Data4: [8]byte{0x8c, 0x6f, 0xbd, 0xdf, 0xee, 0xb1, 0x3b, 0x50}}
var structureIID = windows.GUID{Data1: 0xe81d1b4e, Data2: 0x11c5, Data3: 0x42f8, Data4: [8]byte{0x97, 0x54, 0xe7, 0x03, 0x6c, 0x79, 0xf0, 0x54}}

func signalUIA(s *uiaSink, kind uint32) {
	if s.Window != uiaHost.Load() {
		return
	}
	uiaKinds.Or(kind)
	if target := uiaTarget.Load(); target != 0 && uiaPending.CompareAndSwap(false, true) {
		if call(user32, "PostMessageW", target, wmViewChanged, s.Window, 0) == 0 {
			uiaPending.Store(false)
		}
	}
}

var sinkQuery = syscall.NewCallback(func(self, iid, out uintptr) uintptr {
	if out == 0 {
		return 0x80004003
	}
	*(*uintptr)(unsafe.Pointer(out)) = 0
	s := (*uiaSink)(unsafe.Pointer(self))
	id := *(*windows.GUID)(unsafe.Pointer(iid))
	if id != unknownIID && id != s.IID {
		return 0x80004002
	}
	atomic.AddInt32(&s.Refs, 1)
	*(*uintptr)(unsafe.Pointer(out)) = self
	return 0
})
var sinkAddRef = syscall.NewCallback(func(self uintptr) uintptr { return uintptr(atomic.AddInt32(&(*uiaSink)(unsafe.Pointer(self)).Refs, 1)) })

func releaseSink(self uintptr) uintptr {
	s := (*uiaSink)(unsafe.Pointer(self))
	n := atomic.AddInt32(&s.Refs, -1)
	if n == 0 {
		call(kernel32, "LocalFree", self)
	}
	return uintptr(n)
}

var sinkRelease = syscall.NewCallback(releaseSink)
var sinkEvent = syscall.NewCallback(func(self, sender, event uintptr) uintptr {
	signalUIA((*uiaSink)(unsafe.Pointer(self)), uiaSelection)
	return 0
})
var sinkProperty = syscall.NewCallback(func(self, sender, property, value uintptr) uintptr {
	el := (*comObject)(unsafe.Pointer(sender))
	if property == 30102 {
		if strings.Contains(cachedString(el, 30102), "current=page") {
			signalUIA((*uiaSink)(unsafe.Pointer(self)), uiaSelection)
		}
	} else if cachedInt(el, 30003) == 50030 {
		signalUIA((*uiaSink)(unsafe.Pointer(self)), uiaSelection)
	}
	return 0
})
var sinkStructure = syscall.NewCallback(func(self, sender, change, ids uintptr) uintptr {
	signalUIA((*uiaSink)(unsafe.Pointer(self)), uiaStructure)
	return 0
})
var eventTable = [4]uintptr{sinkQuery, sinkAddRef, sinkRelease, sinkEvent}
var propertyTable = [4]uintptr{sinkQuery, sinkAddRef, sinkRelease, sinkProperty}
var structureTable = [4]uintptr{sinkQuery, sinkAddRef, sinkRelease, sinkStructure}

func allocSink(window uintptr, kind uint32, iid windows.GUID, table *[4]uintptr) uintptr {
	p := call(kernel32, "LocalAlloc", 0x40, unsafe.Sizeof(uiaSink{}))
	if p == 0 {
		return 0
	}
	*(*uiaSink)(unsafe.Pointer(p)) = uiaSink{VTable: uintptr(unsafe.Pointer(table)), Window: window, Kind: kind, Refs: 1, IID: iid}
	return p
}

type uiaSubscriptions struct {
	automation, root, cache          *comObject
	event, property, structure       uintptr
	eventOK, propertyOK, structureOK bool
}

func (s *uiaSubscriptions) close() {
	if s.automation != nil {
		if s.eventOK {
			comCall(s.automation, 33, 20012, uintptr(unsafe.Pointer(s.root)), s.event)
		}
		if s.propertyOK {
			comCall(s.automation, 36, uintptr(unsafe.Pointer(s.root)), s.property)
		}
		if s.structureOK {
			comCall(s.automation, 38, uintptr(unsafe.Pointer(s.root)), s.structure)
		}
	}
	for _, p := range []uintptr{s.event, s.property, s.structure} {
		if p != 0 {
			releaseSink(p)
		}
	}
	if s.cache != nil {
		comCall(s.cache, 2)
	}
	*s = uiaSubscriptions{}
}
func (s *uiaSubscriptions) bind(a, root *comObject, window uintptr) {
	s.close()
	s.automation, s.root = a, root
	uiaHost.Store(window)
	uiaPending.Store(false)
	uiaKinds.Store(0)
	s.cache = newCache(a, []int{30003, 30102})
	s.event = allocSink(window, uiaSelection, eventIID, &eventTable)
	s.property = allocSink(window, uiaSelection, propertyIID, &propertyTable)
	s.structure = allocSink(window, uiaStructure, structureIID, &structureTable)
	if s.event != 0 {
		s.eventOK = int32(comCall(a, 32, 20012, uintptr(unsafe.Pointer(root)), 4, uintptr(unsafe.Pointer(s.cache)), s.event)) >= 0
	}
	properties := [3]int32{30102, 30045, 30093}
	if s.property != 0 {
		s.propertyOK = int32(comCall(a, 34, uintptr(unsafe.Pointer(root)), 4, uintptr(unsafe.Pointer(s.cache)), s.property, uintptr(unsafe.Pointer(&properties[0])), 3)) >= 0
	}
	if s.structure != 0 {
		s.structureOK = int32(comCall(a, 37, uintptr(unsafe.Pointer(root)), 4, uintptr(unsafe.Pointer(s.cache)), s.structure)) >= 0
	}
}

func newCache(a *comObject, properties []int) *comObject {
	var c *comObject
	if int32(comCall(a, 20, uintptr(unsafe.Pointer(&c)))) < 0 || c == nil {
		return nil
	}
	for _, id := range properties {
		if int32(comCall(c, 3, uintptr(id))) < 0 {
			comCall(c, 2)
			return nil
		}
	}
	comCall(c, 7, 1) // TreeScope_Element; descendants are queried explicitly.
	return c
}

func cachedValue(el *comObject, id int) (variant, bool) {
	var v variant
	if el == nil {
		return v, false
	}
	ok := int32(comCall(el, 12, uintptr(id), uintptr(unsafe.Pointer(&v)))) >= 0
	return v, ok
}
func cachedString(el *comObject, id int) string {
	v, ok := cachedValue(el, id)
	if !ok {
		return ""
	}
	defer call(oleaut32, "VariantClear", uintptr(unsafe.Pointer(&v)))
	if v.Type != 8 || v.Data[0] == 0 {
		return ""
	}
	n := call(oleaut32, "SysStringLen", v.Data[0])
	if n > 4096 {
		return ""
	}
	return windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(v.Data[0])), int(n)))
}
func cachedInt(el *comObject, id int) int64 {
	v, ok := cachedValue(el, id)
	if !ok {
		return -1
	}
	defer call(oleaut32, "VariantClear", uintptr(unsafe.Pointer(&v)))
	if v.Type != 3 {
		return -1
	}
	return int64(int32(v.Data[0]))
}
