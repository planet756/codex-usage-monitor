package win

import (
	"strconv"
	"strings"
	"unsafe"

	"codex-usage-monitor/internal/ipc"
)

func stringCondition(a *comObject, property int, value string, flags uintptr) *comObject {
	b := call(oleaut32, "SysAllocString", uintptr(unsafe.Pointer(utf(value))))
	if b == 0 {
		return nil
	}
	defer call(oleaut32, "SysFreeString", b)
	return propertyCondition(a, property, variant{Type: 8, Data: [2]uintptr{b}}, flags)
}

// Desktop's Chat toolbar survives sidebar project collapse. Only its title
// component is queried, never arbitrary text from the transcript or menus.
func (v *viewLocator) headerSelection() (ipc.Selection, bool, bool) {
	v.headerProbe = [3]int{}
	if v.toolbarCondition == nil || v.titleCondition == nil {
		return ipc.Selection{}, false, false
	}
	var candidates ipc.ViewCandidates
	add := func(name string) {
		selection := v.catalogSelection(name)
		if selection.ID != "" {
			v.headerProbe[2]++
		}
		candidates.Add(selection)
	}
	incomplete := false
	captionAmbiguous := false
	n := findElements(v.root, v.toolbarCondition, v.cache, func(toolbar *comObject) bool {
		if !visibleViewElement(toolbar) || !classTokens(cachedString(toolbar, 30012), "min-w-0", "shrink-0", "select-none") {
			return false
		}
		v.headerProbe[0]++
		titleCount := 0
		count := findElements(toolbar, v.titleCondition, v.cache, func(title *comObject) bool {
			if !visibleViewElement(title) || !chatTitleClass(cachedString(title, 30012)) {
				return false
			}
			v.headerProbe[1]++
			titleCount++
			name := cachedString(title, 30005)
			if name != "" {
				add(name)
				return false
			}
			count := findElements(title, v.textCondition, v.cache, func(text *comObject) bool {
				if visibleViewElement(text) {
					add(cachedString(text, 30005))
				}
				return false
			})
			incomplete = incomplete || count < 0 || count > 32
			return false
		})
		incomplete = incomplete || count < 0 || count > 32
		if titleCount == 0 && v.textCondition != nil {
			var direct []headerText
			count := findElementsScope(toolbar, v.textCondition, v.cache, 2, func(text *comObject) bool {
				direct = append(direct, headerText{cachedString(text, 30005), cachedString(text, 30101),
					cachedInt(text, 30003), visibleViewElement(text)})
				return false
			})
			incomplete = incomplete || count < 0 || count > 32
			name, ambiguous := directChatCaption(direct)
			captionAmbiguous = captionAmbiguous || ambiguous
			if name != "" && !ambiguous {
				v.headerProbe[1]++
				add(name)
			}
		}
		return false
	})
	s, ambiguous := candidates.Result(true)
	s.Source = "uia_chat_header"
	return s, ambiguous || captionAmbiguous, incomplete || n < 0 || n > 32
}

func (v *viewLocator) composerAnchor() string {
	if v.composerCondition == nil || v.composerCache == nil {
		return ""
	}
	anchor, count := "", 0
	n := findElements(v.root, v.composerCondition, v.composerCache, func(el *comObject) bool {
		if !visibleViewElement(el) || !classTokens(cachedString(el, 30012), "ProseMirror") {
			return false
		}
		count++
		anchor = elementRuntimeID(el)
		return count > 1
	})
	if n < 0 || n > 32 || count != 1 {
		return ""
	}
	return anchor
}

func elementRuntimeID(el *comObject) string {
	var array uintptr
	if int32(comCall(el, 4, uintptr(unsafe.Pointer(&array)))) < 0 || array == 0 {
		return ""
	}
	defer call(oleaut32, "SafeArrayDestroy", array)
	if call(oleaut32, "SafeArrayGetDim", array) != 1 {
		return ""
	}
	var kind uint16
	var lower, upper int32
	if int32(call(oleaut32, "SafeArrayGetVartype", array, uintptr(unsafe.Pointer(&kind)))) < 0 || kind != 3 ||
		int32(call(oleaut32, "SafeArrayGetLBound", array, 1, uintptr(unsafe.Pointer(&lower)))) < 0 ||
		int32(call(oleaut32, "SafeArrayGetUBound", array, 1, uintptr(unsafe.Pointer(&upper)))) < 0 {
		return ""
	}
	n := int64(upper) - int64(lower) + 1
	if n < 1 || n > 32 {
		return ""
	}
	var data uintptr
	if int32(call(oleaut32, "SafeArrayAccessData", array, uintptr(unsafe.Pointer(&data)))) < 0 {
		return ""
	}
	defer call(oleaut32, "SafeArrayUnaccessData", array)
	if data == 0 {
		return ""
	}
	var key strings.Builder
	for _, value := range unsafe.Slice((*int32)(unsafe.Pointer(data)), int(n)) {
		key.WriteString(strconv.FormatInt(int64(value), 10))
		key.WriteByte('/')
	}
	return key.String()
}
