package win

import (
	"time"
	"unsafe"

	popupstate "codex-usage-monitor/internal/popup"
)

// Conditions are cached on the existing UIA worker's MTA thread. They match
// semantic roles/properties, never accessible names, image paths, or chat text.
type popupLocator struct {
	condition, cache *comObject
}

type guiThreadInfo struct {
	Size, Flags                                   uint32
	Active, Focus, Capture, Menu, MoveSize, Caret uintptr
	CaretBounds                                   rect
}

func (p *popupLocator) close() {
	for _, object := range []*comObject{p.condition, p.cache} {
		if object != nil {
			comCall(object, 2)
		}
	}
	p.condition, p.cache = nil, nil
}

// combineCondition consumes both input references; CreateAnd/OrCondition adds
// its own references. Slots 25/28 are from Microsoft's IUIAutomation vtable.
func combineCondition(automation *comObject, slot int, left, right *comObject) *comObject {
	var result *comObject
	if left != nil && right != nil {
		if int32(comCall(automation, slot, uintptr(unsafe.Pointer(left)), uintptr(unsafe.Pointer(right)),
			uintptr(unsafe.Pointer(&result)))) < 0 {
			if result != nil {
				comCall(result, 2)
			}
			result = nil
		}
	}
	if left != nil {
		comCall(left, 2)
	}
	if right != nil {
		comCall(right, 2)
	}
	return result
}

func popupRoleCondition(automation *comObject, role string) *comObject {
	text := call(oleaut32, "SysAllocString", uintptr(unsafe.Pointer(utf(role))))
	if text == 0 {
		return nil
	}
	defer call(oleaut32, "SysFreeString", text)
	return propertyCondition(automation, 30101, variant{Type: 8, Data: [2]uintptr{text}}, 0)
}

func (p *popupLocator) prepare(automation *comObject) bool {
	if p.condition != nil && p.cache != nil {
		return true
	}
	p.close()
	roles := combineCondition(automation, 28, popupRoleCondition(automation, "dialog"), popupRoleCondition(automation, "alertdialog"))
	roles = combineCondition(automation, 28, roles, propertyCondition(automation, 30003, variant{Type: 3, Data: [2]uintptr{50009}}, 0))
	roles = combineCondition(automation, 28, roles, propertyCondition(automation, 30174, variant{Type: 11, Data: [2]uintptr{0xffff}}, 0))
	roles = combineCondition(automation, 28, roles, propertyCondition(automation, 30077, variant{Type: 11, Data: [2]uintptr{0xffff}}, 0))
	find := combineCondition(automation, 25,
		propertyCondition(automation, 30003, variant{Type: 3, Data: [2]uintptr{50004}}, 0),
		stringCondition(automation, 30011, popupstate.FindInputPrefix, 2))
	roles = combineCondition(automation, 28, roles, find)
	onscreen := propertyCondition(automation, 30022, variant{Type: 11}, 0)
	p.condition = combineCondition(automation, 25, roles, onscreen)
	// Never request Name, Value or LegacyValue from the search input.
	p.cache = newCache(automation, []int{30003, 30011, 30101, 30174, 30077})
	if p.condition == nil || p.cache == nil {
		p.close()
		return false
	}
	return true
}

func (p *popupLocator) locate(automation *comObject, window uintptr, root *comObject) popupstate.Observation {
	result := popupstate.Observation{Source: "uia_unavailable", At: time.Now()}
	if source := nativePopup(window); source != "" {
		result.Available, result.Visible, result.Source = true, true, source
		return result
	}
	if automation == nil || !p.prepare(automation) {
		return result
	}
	if root == nil {
		result.Source = "uia_root_unavailable"
		return result
	}
	// Bound candidates within this HWND. An ID substring match is only a
	// prefilter; validate the complete generated ID before hiding the strip.
	unknown := false
	count := findElementsScope(root, p.condition, p.cache, 7, func(found *comObject) bool {
		control, id := cachedInt(found, 30003), cachedString(found, 30011)
		source := popupstate.SurfaceSource(cachedString(found, 30101), control,
			id, cachedBool(found, 30174), cachedBool(found, 30077))
		if source == "" {
			// A malformed nonempty input ID is a definite non-match. Missing
			// metadata for a role/modal candidate cannot prove it has closed.
			unknown = unknown || control != 50004 || id == ""
			return false
		}
		result.Available, result.Visible, result.Source = true, true, source
		return true
	})
	if !result.Visible {
		if unknown || count < 0 || count > 32 {
			result.Source = "uia_popup_query_failed"
		} else {
			result.Available, result.Source = true, "clear"
		}
	}
	return result
}

func cachedBool(el *comObject, property int) bool {
	v, ok := cachedValue(el, property)
	if !ok {
		return false
	}
	defer call(oleaut32, "VariantClear", uintptr(unsafe.Pointer(&v)))
	return v.Type == 11 && v.Data[0]&0xffff != 0
}

func nativePopup(host uintptr) string {
	if host == 0 || call(user32, "IsWindow", host) == 0 {
		return ""
	}
	if call(user32, "IsWindowEnabled", host) == 0 {
		return "native_host_disabled"
	}
	thread := call(user32, "GetWindowThreadProcessId", host, 0)
	info := guiThreadInfo{Size: uint32(unsafe.Sizeof(guiThreadInfo{}))}
	if thread != 0 && call(user32, "GetGUIThreadInfo", thread, uintptr(unsafe.Pointer(&info))) != 0 &&
		info.Flags&0x1c != 0 && info.Menu != 0 &&
		(info.Menu == host || call(user32, "GetAncestor", info.Menu, 2) == host || ownedBy(info.Menu, host)) {
		return "native_menu"
	}
	active := call(user32, "GetLastActivePopup", host)
	if active != 0 && active != host && call(user32, "IsWindowVisible", active) != 0 && ownedBy(active, host) {
		var hostPID, popupPID uint32
		call(user32, "GetWindowThreadProcessId", host, uintptr(unsafe.Pointer(&hostPID)))
		call(user32, "GetWindowThreadProcessId", active, uintptr(unsafe.Pointer(&popupPID)))
		if hostPID == popupPID {
			return "native_owned_popup"
		}
	}
	return ""
}

func ownedBy(window, host uintptr) bool {
	if window == 0 || host == 0 || window == host {
		return false
	}
	for depth := 0; window != 0 && depth < 16; depth++ {
		window = call(user32, "GetWindow", window, 4)
		if window == host {
			return true
		}
	}
	return false
}

var popupEventProc = eventCallback(func(hook uintptr, event uint32, h uintptr, object, child int32, thread, timestamp uint32) uintptr {
	a := currentOverlay
	if a == nil || a.host == 0 || h == 0 {
		return 0
	}
	if h == a.host || call(user32, "GetAncestor", h, 2) == a.host || ownedBy(h, a.host) {
		if a.popupEventPending.CompareAndSwap(false, true) {
			post(a.hwnd, wmPopup)
		}
	}
	return 0
})

func (a *overlay) checkPopup(urgent bool) {
	if !hostSurfaceVisible(a.host) {
		return
	}
	if source := nativePopup(a.host); source != "" {
		a.popup.Observe(uint64(a.host), popupstate.Observation{Available: true, Visible: true, Source: source, At: time.Now()})
		a.hide("popup_open", nil)
	}
	a.requestView(urgent)
}

func hostSurfaceVisible(host uintptr) bool {
	if host == 0 || call(user32, "IsIconic", host) != 0 || call(user32, "IsWindowVisible", host) == 0 {
		return false
	}
	var cloaked uint32
	return int32(call(dwmapi, "DwmGetWindowAttribute", host, 14, uintptr(unsafe.Pointer(&cloaked)), 4)) < 0 || cloaked == 0
}

func (a *overlay) queueProbe(delay time.Duration) {
	if !a.probeArmed || delay < a.probeDelay {
		a.probeArmed = true
		a.probeDelay = delay
		call(user32, "SetTimer", a.hwnd, 4, uintptr(delay/time.Millisecond), 0)
	}
}
