package mac

import (
	"codex-usage-monitor/internal/layout"
	"github.com/ebitengine/purego/objc"
)

type darwinAX struct{}

func (darwinAX) enable(app uintptr) int32 {
	value := objc.ID(objc.GetClass("NSNumber")).Send(sel("numberWithBool:"), true)
	return native.axSetAttribute(app, uintptr(ns("AXManualAccessibility")), uintptr(value))
}

func (darwinAX) attribute(el uintptr, name string) (uintptr, int32) {
	var value uintptr
	code := native.axAttribute(el, uintptr(ns(name)), &value)
	if code != 0 {
		return 0, code
	}
	return value, code
}
func (darwinAX) text(value uintptr) string { return cfString(value) }
func (darwinAX) boolean(value uintptr) bool {
	return native.typeID(value) == native.booleanType() && native.booleanValue(value)
}
func (darwinAX) array(value uintptr, limit int, visit func(uintptr) bool) bool {
	return eachArray(value, limit, visit)
}
func (darwinAX) bounds(el uintptr) (layout.Rect, bool) {
	r, ok := axRect(el)
	return layout.Rect{X: int(r.Origin.X), Y: int(r.Origin.Y), W: int(r.Size.W), H: int(r.Size.H)}, ok
}
func (darwinAX) retain(el uintptr) uintptr { return native.retain(el) }
func (darwinAX) release(el uintptr)        { native.release(el) }
func (darwinAX) equal(a, b uintptr) bool   { return native.equal(a, b) }
func (darwinAX) key(el uintptr) uintptr    { return native.hash(el) }
