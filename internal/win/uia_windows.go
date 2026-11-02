package win

import (
	"codex-usage-monitor/internal/layout"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

// The vtable slots follow Microsoft's UIAutomationClient.h (IUIAutomation and
// IUIAutomationElement). COM is initialized on a dedicated MTA thread, never on
// the overlay's message thread. No accessible names or document text are read.
var automationCLSID = windows.GUID{Data1: 0xFF48DBA4, Data2: 0x60EF, Data3: 0x4201, Data4: [8]byte{0xAA, 0x87, 0x54, 0x10, 0x3E, 0xEF, 0x59, 0x4E}}
var automationIID = windows.GUID{Data1: 0x30CBE57D, Data2: 0xD9D0, Data3: 0x452A, Data4: [8]byte{0xAB, 0x13, 0x7A, 0xC5, 0xAC, 0x48, 0x25, 0xEE}}

type comObject struct{ vtable *uintptr }

//go:uintptrescapes
func comCall(object *comObject, slot int, args ...uintptr) uintptr {
	fn := unsafe.Slice(object.vtable, slot+1)[slot]
	a := append([]uintptr{uintptr(unsafe.Pointer(object))}, args...)
	r, _, _ := syscall.SyscallN(fn, a...)
	runtime.KeepAlive(object)
	return r
}

// VARIANT is 24 bytes on Win64; the ABI passes it indirectly.
type variant struct {
	Type     uint16
	Reserved [3]uint16
	Data     [2]uintptr
}

func layoutRect(r rect) layout.Rect {
	return layout.Rect{X: int(r.Left), Y: int(r.Top), W: r.width(), H: r.height()}
}
