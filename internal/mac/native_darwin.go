// Package mac provides the native AppKit/Accessibility adapter. It is loaded
// only on macOS and does not inject code into Desktop or start its backend.
package mac

import (
	"errors"
	"fmt"
	"runtime"
	"structs"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
)

type point struct {
	_    structs.HostLayout
	X, Y float64
}
type size struct {
	_    structs.HostLayout
	W, H float64
}
type rect struct {
	_      structs.HostLayout
	Origin point
	Size   size
}

func box(x, y, w, h float64) rect { return rect{Origin: point{X: x, Y: y}, Size: size{W: w, H: h}} }

var native struct {
	once             sync.Once
	err              error
	app              objc.ID
	release          func(uintptr)
	retain           func(uintptr) uintptr
	typeID           func(uintptr) uintptr
	hash             func(uintptr) uintptr
	equal            func(uintptr, uintptr) bool
	stringType       func() uintptr
	stringLength     func(uintptr) int64
	stringUTF8       func(uintptr, *byte, int64, uint32) bool
	arrayType        func() uintptr
	arrayCount       func(uintptr) int64
	arrayAt          func(uintptr, int64) uintptr
	booleanType      func() uintptr
	booleanValue     func(uintptr) bool
	urlType          func() uintptr
	urlString        func(uintptr) uintptr
	axTrusted        func() bool
	axApplication    func(int32) uintptr
	axSystemWide     func() uintptr
	axAttribute      func(uintptr, uintptr, *uintptr) int32
	axSetAttribute   func(uintptr, uintptr, uintptr) int32
	axTimeout        func(uintptr, float32) int32
	axValue          func(uintptr, int32, unsafe.Pointer) bool
	axObserver       func(int32, uintptr, *uintptr) int32
	axObserverSource func(uintptr) uintptr
	axObserve        func(uintptr, uintptr, uintptr, uintptr) int32
	axUnobserve      func(uintptr, uintptr, uintptr) int32
	runLoop          func() uintptr
	addSource        func(uintptr, uintptr, uintptr)
	removeSource     func(uintptr, uintptr, uintptr)
	defaultMode      uintptr
	runMode          func(uintptr, float64, bool) int32
	stop             func(uintptr)
	wake             func(uintptr)
	sourceCreate     func(uintptr, int64, *sourceContext) uintptr
	sourceSignal     func(uintptr)
	timerCreate      func(uintptr, float64, float64, uint64, int64, uintptr, uintptr) uintptr
	clock            func() float64
	axTrustedOptions func(uintptr) bool
}

type sourceContext struct {
	_                                                                          structs.HostLayout
	Version                                                                    int64
	Info, Retain, Release, Description, Equal, Hash, Schedule, Cancel, Perform uintptr
}

func loadNative() error {
	native.once.Do(func() {
		cocoa, err := purego.Dlopen("/System/Library/Frameworks/Cocoa.framework/Cocoa", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			native.err = err
			return
		}
		_ = cocoa // Framework must stay loaded for Objective-C class lifetimes.
		cf, err := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			native.err = err
			return
		}
		ax, err := purego.Dlopen("/System/Library/Frameworks/ApplicationServices.framework/ApplicationServices", purego.RTLD_NOW|purego.RTLD_GLOBAL)
		if err != nil {
			native.err = err
			return
		}
		for _, binding := range []struct {
			to   any
			lib  uintptr
			name string
		}{
			{&native.release, cf, "CFRelease"}, {&native.retain, cf, "CFRetain"}, {&native.typeID, cf, "CFGetTypeID"},
			{&native.hash, cf, "CFHash"}, {&native.equal, cf, "CFEqual"},
			{&native.stringType, cf, "CFStringGetTypeID"}, {&native.stringLength, cf, "CFStringGetLength"}, {&native.stringUTF8, cf, "CFStringGetCString"},
			{&native.arrayType, cf, "CFArrayGetTypeID"}, {&native.arrayCount, cf, "CFArrayGetCount"}, {&native.arrayAt, cf, "CFArrayGetValueAtIndex"},
			{&native.booleanType, cf, "CFBooleanGetTypeID"}, {&native.booleanValue, cf, "CFBooleanGetValue"},
			{&native.urlType, cf, "CFURLGetTypeID"}, {&native.urlString, cf, "CFURLGetString"},
			{&native.axTrusted, ax, "AXIsProcessTrusted"}, {&native.axApplication, ax, "AXUIElementCreateApplication"},
			{&native.axSystemWide, ax, "AXUIElementCreateSystemWide"},
			{&native.axAttribute, ax, "AXUIElementCopyAttributeValue"}, {&native.axTimeout, ax, "AXUIElementSetMessagingTimeout"},
			{&native.axSetAttribute, ax, "AXUIElementSetAttributeValue"},
			{&native.axValue, ax, "AXValueGetValue"}, {&native.axObserver, ax, "AXObserverCreate"},
			{&native.axObserverSource, ax, "AXObserverGetRunLoopSource"}, {&native.axObserve, ax, "AXObserverAddNotification"},
			{&native.axUnobserve, ax, "AXObserverRemoveNotification"},
			{&native.runLoop, cf, "CFRunLoopGetCurrent"}, {&native.addSource, cf, "CFRunLoopAddSource"}, {&native.removeSource, cf, "CFRunLoopRemoveSource"},
			{&native.runMode, cf, "CFRunLoopRunInMode"}, {&native.stop, cf, "CFRunLoopStop"}, {&native.wake, cf, "CFRunLoopWakeUp"},
			{&native.sourceCreate, cf, "CFRunLoopSourceCreate"}, {&native.sourceSignal, cf, "CFRunLoopSourceSignal"},
			{&native.timerCreate, cf, "CFRunLoopTimerCreate"}, {&native.clock, cf, "CFAbsoluteTimeGetCurrent"},
			{&native.axTrustedOptions, ax, "AXIsProcessTrustedWithOptions"},
		} {
			address, e := purego.Dlsym(binding.lib, binding.name)
			if e != nil {
				native.err = fmt.Errorf("native symbol %s: %w", binding.name, e)
				return
			}
			purego.RegisterFunc(binding.to, address)
		}
		// Only the system-wide AX element sets the timeout for every reference
		// in this process, including children and equal event-callback objects.
		system := native.axSystemWide()
		if system == 0 {
			native.err = errors.New("system-wide Accessibility element unavailable")
			return
		}
		code := native.axTimeout(system, float32(axCallTimeout.Seconds()))
		native.release(system)
		if code != 0 {
			native.err = fmt.Errorf("Accessibility messaging timeout: %d", code)
			return
		}
		mode, e := purego.Dlsym(cf, "kCFRunLoopDefaultMode")
		if e != nil {
			native.err = e
			return
		}
		native.defaultMode = *(*uintptr)(unsafe.Pointer(mode))
	})
	return native.err
}

func startApplication() error {
	if err := loadNative(); err != nil {
		return err
	}
	if native.app != 0 {
		return nil
	}
	native.app = objc.ID(objc.GetClass("NSApplication")).Send(sel("sharedApplication"))
	if native.app == 0 {
		return errors.New("AppKit application unavailable")
	}
	native.app.Send(sel("setActivationPolicy:"), int64(1))
	native.app.Send(sel("finishLaunching"))
	return nil
}

func sel(s string) objc.SEL { return objc.RegisterName(s) }
func ns(s string) objc.ID {
	return objc.ID(objc.GetClass("NSString")).Send(sel("stringWithUTF8String:"), s)
}
func newObject(class string) objc.ID { return objc.ID(objc.GetClass(class)).Send(sel("new")) }
func pool() objc.ID                  { return newObject("NSAutoreleasePool") }

func cfString(p uintptr) string {
	if p == 0 {
		return ""
	}
	if native.typeID(p) == native.urlType() {
		p = native.urlString(p)
	}
	if p == 0 || native.typeID(p) != native.stringType() {
		return ""
	}
	n := native.stringLength(p)
	if n < 0 || n > 4096 {
		return ""
	}
	b := make([]byte, n*4+1)
	if !native.stringUTF8(p, &b[0], int64(len(b)), 0x08000100) {
		return ""
	}
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return ""
}

func attr(el uintptr, name string) uintptr {
	var value uintptr
	if el == 0 || native.axAttribute(el, uintptr(ns(name)), &value) != 0 {
		return 0
	}
	return value
}
func axString(el uintptr, name string) string {
	v := attr(el, name)
	if v == 0 {
		return ""
	}
	defer native.release(v)
	return cfString(v)
}
func axBool(el uintptr, name string) bool {
	v := attr(el, name)
	if v == 0 {
		return false
	}
	defer native.release(v)
	return native.typeID(v) == native.booleanType() && native.booleanValue(v)
}
func eachArray(v uintptr, limit int, visit func(uintptr) bool) bool {
	if v == 0 || native.typeID(v) != native.arrayType() {
		return false
	}
	n := native.arrayCount(v)
	if n < 0 || n > int64(limit) {
		return false
	}
	for i := int64(0); i < n; i++ {
		if visit(native.arrayAt(v, i)) {
			break
		}
	}
	return true
}
func axRect(el uintptr) (rect, bool) {
	p, s := attr(el, "AXPosition"), attr(el, "AXSize")
	if p != 0 {
		defer native.release(p)
	}
	if s != 0 {
		defer native.release(s)
	}
	var r rect
	if p == 0 || s == 0 || !native.axValue(p, 1, unsafe.Pointer(&r.Origin)) || !native.axValue(s, 2, unsafe.Pointer(&r.Size)) {
		return rect{}, false
	}
	return r, r.Size.W > 0 && r.Size.H > 0
}

// AppKit is confined to the locked main thread. Every event iteration drains
// its autoreleased objects, including native strings created by callbacks.
func pump() {
	p := pool()
	defer p.Send(sel("drain"))
	until := objc.ID(objc.GetClass("NSDate")).Send(sel("dateWithTimeIntervalSinceNow:"), float64(0.05))
	event := native.app.Send(sel("nextEventMatchingMask:untilDate:inMode:dequeue:"), ^uint64(0), until, objc.ID(native.defaultMode), true)
	if event != 0 {
		native.app.Send(sel("sendEvent:"), event)
	}
	native.app.Send(sel("updateWindows"))
	runtime.KeepAlive(p)
}
