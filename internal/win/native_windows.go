// Package win contains the native Windows implementation. No browser engine,
// injected DLL, HTTP server, or additional UI runtime is used.
package win

import (
	"fmt"
	"sync"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

type procKey struct {
	dll  *windows.LazyDLL
	name string
}

var nativeProcs sync.Map

func nativeProc(d *windows.LazyDLL, name string) *windows.LazyProc {
	key := procKey{d, name}
	p, ok := nativeProcs.Load(key)
	if !ok {
		p, _ = nativeProcs.LoadOrStore(key, d.NewProc(name))
	}
	return p.(*windows.LazyProc)
}

var (
	user32   = windows.NewLazySystemDLL("user32.dll")
	kernel32 = windows.NewLazySystemDLL("kernel32.dll")
	gdi32    = windows.NewLazySystemDLL("gdi32.dll")
	ole32    = windows.NewLazySystemDLL("ole32.dll")
	oleaut32 = windows.NewLazySystemDLL("oleaut32.dll")
	dwmapi   = windows.NewLazySystemDLL("dwmapi.dll")
)

//go:uintptrescapes
func call(d *windows.LazyDLL, name string, a ...uintptr) uintptr {
	r, _, _ := nativeProc(d, name).Call(a...)
	return r
}

//go:uintptrescapes
func callWithError(d *windows.LazyDLL, name string, a ...uintptr) (uintptr, error) {
	r, _, e := nativeProc(d, name).Call(a...)
	return r, e
}
func utf(s string) *uint16 { p, _ := windows.UTF16PtrFromString(s); return p }
func signed(n int) uintptr { return uintptr(n) }

type point struct{ X, Y int32 }
type rect struct{ Left, Top, Right, Bottom int32 }

func (r rect) width() int  { return int(r.Right - r.Left) }
func (r rect) height() int { return int(r.Bottom - r.Top) }
func (r rect) contains(p point) bool {
	return p.X >= r.Left && p.X < r.Right && p.Y >= r.Top && p.Y < r.Bottom
}

type msg struct {
	HWND           uintptr
	Message        uint32
	WParam, LParam uintptr
	Time           uint32
	Pt             point
	Private        uint32
}
type wndClass struct {
	Size, Style                        uint32
	Proc                               uintptr
	ClassExtra, WindowExtra            int32
	Instance, Icon, Cursor, Background uintptr
	Menu, Name                         *uint16
	SmallIcon                          uintptr
}

const (
	wmDestroy           = 0x0002
	wmClose             = 0x0010
	wmTimer             = 0x0113
	wmCommand           = 0x0111
	wmLButtonUp         = 0x0202
	wmRButtonUp         = 0x0205
	wmMouseActivate     = 0x0021
	wmDpiChanged        = 0x02E0
	wmSettingChange     = 0x001A
	wmUpdate            = 0x8001
	wmGeometry          = 0x8002
	wmReposition        = 0x8004
	wmPopup             = 0x8005
	wmUISettingsChanged = 0x8006 // Private, pointer-free companion notification.
	wmHostExited        = 0x8007
	wmViewChanged       = 0x8008
	wmPowerBroadcast    = 0x0218
	wmSessionChange     = 0x02B1
)

func registerClass(name string, proc uintptr) error {
	wc := wndClass{Size: uint32(unsafe.Sizeof(wndClass{})), Proc: proc, Instance: call(kernel32, "GetModuleHandleW", 0),
		Cursor: call(user32, "LoadCursorW", 0, 32512), Name: utf(name)}
	if call(user32, "RegisterClassExW", uintptr(unsafe.Pointer(&wc))) == 0 {
		return fmt.Errorf("RegisterClassExW: %w", windows.GetLastError())
	}
	return nil
}

func createWindow(class string, exstyle, style uint32) (uintptr, error) {
	h, _, e := user32.NewProc("CreateWindowExW").Call(uintptr(exstyle), uintptr(unsafe.Pointer(utf(class))), uintptr(unsafe.Pointer(utf("Codex Usage Monitor"))), uintptr(style), 0, 0, 0, 0, 0, 0, call(kernel32, "GetModuleHandleW", 0), 0)
	if h == 0 {
		return 0, fmt.Errorf("CreateWindowExW: %w", e)
	}
	return h, nil
}

func post(h uintptr, m uint32) { call(user32, "PostMessageW", h, uintptr(m), 0, 0) }
func messageLoop() error {
	var m msg
	for {
		n := int32(call(user32, "GetMessageW", uintptr(unsafe.Pointer(&m)), 0, 0, 0))
		if n == 0 {
			return nil
		}
		if n < 0 {
			return fmt.Errorf("GetMessageW: %w", windows.GetLastError())
		}
		if currentSettings != nil && (m.HWND == currentSettings.hwnd || call(user32, "IsChild", currentSettings.hwnd, m.HWND) != 0) &&
			call(user32, "IsDialogMessageW", currentSettings.hwnd, uintptr(unsafe.Pointer(&m))) != 0 {
			continue
		}
		call(user32, "TranslateMessage", uintptr(unsafe.Pointer(&m)))
		call(user32, "DispatchMessageW", uintptr(unsafe.Pointer(&m)))
	}
}

func messageBox(title, text string) {
	call(user32, "MessageBoxW", 0, uintptr(unsafe.Pointer(utf(text))), uintptr(unsafe.Pointer(utf(title))), 0x40)
}
func dpiFor(h uintptr) int {
	n := call(user32, "GetDpiForWindow", h)
	if n == 0 {
		return 96
	}
	return int(n)
}
func scale(n, dpi int) int { return (n*dpi + 48) / 96 }
func clientRect(h uintptr) (rect, bool) {
	var r rect
	if call(user32, "GetClientRect", h, uintptr(unsafe.Pointer(&r))) == 0 {
		return r, false
	}
	p := point{}
	if call(user32, "ClientToScreen", h, uintptr(unsafe.Pointer(&p))) == 0 {
		return r, false
	}
	r.Left += p.X
	r.Right += p.X
	r.Top += p.Y
	r.Bottom += p.Y
	return r, true
}

type menuEntry struct {
	ID      uint32
	Label   string
	Checked bool
}

func popup(h uintptr, entries []menuEntry) uint32 {
	m := call(user32, "CreatePopupMenu")
	defer call(user32, "DestroyMenu", m)
	for _, e := range entries {
		flags := uintptr(0)
		if e.Checked {
			flags = 8
		}
		if e.ID == 0 {
			flags = 0x800
		}
		call(user32, "AppendMenuW", m, flags, uintptr(e.ID), uintptr(unsafe.Pointer(utf(e.Label))))
	}
	var p point
	call(user32, "GetCursorPos", uintptr(unsafe.Pointer(&p)))
	call(user32, "SetForegroundWindow", h)
	id := call(user32, "TrackPopupMenu", m, 0x100|0x2, signed(int(p.X)), signed(int(p.Y)), 0, h, 0)
	post(h, 0)
	return uint32(id)
}

func setDPIAware() {
	if user32.NewProc("SetProcessDpiAwarenessContext").Find() == nil {
		call(user32, "SetProcessDpiAwarenessContext", ^uintptr(3))
	}
}

// Keep callback signatures typed: NewCallback validates its arguments only at
// runtime, including during package initialization before main can show errors.
func callback(fn func(uintptr, uint32, uintptr, uintptr) uintptr) uintptr {
	return syscall.NewCallback(fn)
}

func enumCallback(fn func(uintptr, uintptr) uintptr) uintptr {
	return syscall.NewCallback(fn)
}

// Windows ignores WINEVENTPROC's return register, but Go's callback bridge still
// requires one uintptr-sized return value even for a native void callback.
func eventCallback(fn func(uintptr, uint32, uintptr, int32, int32, uint32, uint32) uintptr) uintptr {
	return syscall.NewCallback(fn)
}
