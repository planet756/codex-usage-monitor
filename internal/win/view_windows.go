package win

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"runtime"
	"time"
	"unsafe"

	"codex-usage-monitor/internal/ipc"
	popupstate "codex-usage-monitor/internal/popup"
	"codex-usage-monitor/internal/source"
	"golang.org/x/sys/windows"
)

type viewSelection struct {
	Window                   uintptr
	ChatPage                 bool
	ID, Host, Source, Reason string
	SelectedElements         int
	PageEvidenceRequired     bool
	HeaderProbe              [3]int // Matching Chat scopes, title controls, unique catalog matches.
}

type viewRequest struct {
	Window    uintptr
	Epoch     uint64
	Selection bool
	Refresh   bool
}

type viewProbe struct {
	Epoch     uint64
	Events    bool
	Selection viewSelection
	Popup     popupstate.Observation
}

type viewLocator struct {
	home                                                                       string
	root, document, cache, documentCondition, selectedCondition, textCondition *comObject
	events                                                                     uiaSubscriptions
	threadCatalog                                                              viewCatalogDB
	catalogIssue                                                               string
	host                                                                       uintptr
	bound                                                                      time.Time
	catalog                                                                    ipc.ViewCatalog
	catalogPath                                                                string
	catalogStamp                                                               time.Time
	toolbarCondition, titleCondition, composerCondition                        *comObject
	page                                                                       ipc.PageIdentity
	composerCache                                                              *comObject
	headerProbe                                                                [3]int
}

func propertyCondition(automation *comObject, id int, value variant, flags uintptr) *comObject {
	var condition *comObject
	if int32(comCall(automation, 24, uintptr(id), uintptr(unsafe.Pointer(&value)), flags, uintptr(unsafe.Pointer(&condition)))) < 0 {
		return nil
	}
	return condition
}

func legacyValue(el *comObject) string {
	if value := cachedString(el, 30093); value != "" {
		return value
	}
	var pattern *comObject
	if int32(comCall(el, 16, 10018, uintptr(unsafe.Pointer(&pattern)))) < 0 || pattern == nil {
		return ""
	}
	defer comCall(pattern, 2)
	var value uintptr
	if int32(comCall(pattern, 8, uintptr(unsafe.Pointer(&value)))) < 0 || value == 0 {
		return ""
	}
	defer call(oleaut32, "SysFreeString", value)
	n := call(oleaut32, "SysStringLen", value)
	if n > 4096 {
		return ""
	}
	return windows.UTF16ToString(unsafe.Slice((*uint16)(unsafe.Pointer(value)), int(n)))
}

func visibleViewElement(el *comObject) bool {
	v, ok := cachedValue(el, 30022)
	if !ok {
		return false
	}
	defer call(oleaut32, "VariantClear", uintptr(unsafe.Pointer(&v)))
	return v.Type == 11 && v.Data[0]&0xffff == 0
}

func findElements(root, condition, cache *comObject, visit func(*comObject) bool) int {
	return findElementsScope(root, condition, cache, 4, visit)
}

func findElementsScope(root, condition, cache *comObject, scope uintptr, visit func(*comObject) bool) int {
	var elements *comObject
	if condition == nil || int32(comCall(root, 8, scope, uintptr(unsafe.Pointer(condition)), uintptr(unsafe.Pointer(cache)), uintptr(unsafe.Pointer(&elements)))) < 0 || elements == nil {
		return -1
	}
	defer comCall(elements, 2)
	var count int32
	if int32(comCall(elements, 3, uintptr(unsafe.Pointer(&count)))) < 0 {
		return -1
	}
	for i := int32(0); i < min(count, 32); i++ {
		var el *comObject
		if int32(comCall(elements, 4, uintptr(i), uintptr(unsafe.Pointer(&el)))) < 0 || el == nil {
			return -1
		}
		stop := visit(el)
		comCall(el, 2)
		if stop {
			break
		}
	}
	return int(count)
}

func (v *viewLocator) loadCatalog(home string) {
	paths := []string{source.JoinPath(home, ".codex-global-state.json")}

	for _, path := range paths {
		info, err := os.Stat(path)
		if err != nil || info.Size() > 16<<20 {
			continue
		}
		if path == v.catalogPath && info.ModTime() == v.catalogStamp {
			return
		}
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(f, (16<<20)+1))
		f.Close()
		if err != nil || len(b) > 16<<20 {
			continue
		}
		var state struct {
			Atoms ipc.ViewCatalog `json:"electron-persisted-atom-state"`
		}
		if json.Unmarshal(b, &state) == nil {
			v.catalog, v.catalogPath, v.catalogStamp = state.Atoms, path, info.ModTime()
			return
		}
	}
}

func (v *viewLocator) identify(el *comObject, automation *comObject, home string) (string, string, string) {
	if id, host := v.resolveURL(legacyValue(el), home); id != "" {
		return id, host, "uia_selected_url"
	}
	if id, host := v.resolveURL(cachedString(el, 30045), home); id != "" {
		return id, host, "uia_selected_url"
	}
	if s := v.catalogSelection(cachedString(el, 30005)); s.ID != "" {
		return s.ID, s.Host, "uia_catalog_title"
	}
	// A row's accessible name can include its age or status. Read only text
	// children of that already selected row, not the conversation document.
	condition := v.textCondition
	if condition == nil {
		return "", "", ""
	}
	var candidates ipc.ViewCandidates
	n := findElements(el, condition, v.cache, func(child *comObject) bool {
		if visibleViewElement(child) {
			candidates.Add(v.catalogSelection(cachedString(child, 30005)))
		}
		return false
	})
	s, _ := candidates.Result(n >= 0 && n <= 32)
	return s.ID, s.Host, "uia_catalog_title"
}

func (v *viewLocator) locate(automation *comObject, window uintptr, home string) viewSelection {
	result := viewSelection{Window: window, Reason: "selected_chat_unavailable"}
	v.catalogIssue = ""
	root := v.root
	if root == nil {
		v.page.Invalidate()
		result.Reason = "uia_root_unavailable"
		return result
	}
	// A native Document's legacy value may carry the actual current route.
	if v.document != nil {
		v.document = refreshElement(v.document, v.cache)
	}
	if v.document == nil && v.documentCondition != nil {
		comCall(root, 7, 4, uintptr(unsafe.Pointer(v.documentCondition)), uintptr(unsafe.Pointer(v.cache)), uintptr(unsafe.Pointer(&v.document)))
	}
	pending := false
	if v.document != nil && visibleViewElement(v.document) {
		value := legacyValue(v.document)
		switch ipc.DocumentPage(value) {
		case ipc.PageOther:
			v.page.Invalidate()
			v.threadCatalog.close()
			result.Reason = "non_chat_page"
			return result
		case ipc.PageChat:
			result.ChatPage = true
		}
		result.ID, result.Host = v.resolveURL(value, home)
		pending = result.ChatPage && result.ID == ""
		if result.ID != "" {
			result.Source, result.Reason = "uia_document_url", "verified"
			// An explicit page route establishes identity without consulting
			// optional titles in other catalog files or the retained sidebar.
			selected, reason := v.page.Resolve(ipc.PageObservation{Window: uint64(window), Anchor: v.composerAnchor(), Document: ipc.Selection{ID: result.ID, Host: result.Host, Source: result.Source}})
			result.ID, result.Host, result.Source, result.Reason = selected.ID, selected.Host, selected.Source, reason
			return result
		}
		if pending {
			v.page.Invalidate()
			v.threadCatalog.close()
			result.Reason = "pending_chat_identity"
			return result
		}
	}
	document := ipc.Selection{ID: result.ID, Host: result.Host, Source: result.Source}
	anchor := v.composerAnchor()
	result.ChatPage = result.ChatPage || anchor != ""
	condition := v.selectedCondition
	var candidates ipc.ViewCandidates
	result.SelectedElements = findElements(root, condition, v.cache, func(el *comObject) bool {
		if !visibleViewElement(el) {
			return false
		}
		id, host, source := v.identify(el, automation, home)
		if id == "" {
			for _, value := range []string{legacyValue(el), cachedString(el, 30045)} {
				if client, _ := ipc.ViewRoute(value); client != "" && !ipc.ValidThreadID(client) {
					pending = true
				}
			}
			return false
		}
		return candidates.Add(ipc.Selection{ID: id, Host: host, Source: source})
	})
	selected, ambiguous := candidates.Result(true)
	sidebarIncomplete := result.SelectedElements < 0 || result.SelectedElements > 32
	if sidebarIncomplete {
		selected = ipc.Selection{}
	}
	var header ipc.Selection
	headerAmbiguous, headerIncomplete := false, false
	observation := ipc.PageObservation{Window: uint64(window), Anchor: anchor, Document: document, Sidebar: selected}
	result.PageEvidenceRequired = document.ID == "" && selected.ID == ""
	// A freshly queried aria-current row is primary page evidence. The editor
	// object's lifetime can change during editing; an optional header must not
	// turn that into a chat change or veto an explicitly selected saved chat.
	if !ambiguous && !pending && (result.PageEvidenceRequired || !result.ChatPage) {
		header, headerAmbiguous, headerIncomplete = v.headerSelection()
		result.HeaderProbe = v.headerProbe
		result.ChatPage = result.ChatPage || !headerIncomplete && v.headerProbe[0] > 0
	}
	observation.Header, observation.Ambiguous = header, ambiguous || headerAmbiguous
	observation.SidebarIncomplete = sidebarIncomplete
	observation.HeaderIncomplete, observation.Pending = headerIncomplete, pending
	if v.catalogIssue != "" && !pending {
		// Failed/ambiguous catalog evidence must not revive the previous chat
		// through editor continuity, even if the editor object was reused.
		v.page.Invalidate()
		observation.Ambiguous = observation.Ambiguous || v.catalogIssue == "selected_chat_ambiguous"
		observation.SidebarIncomplete = observation.SidebarIncomplete || v.catalogIssue == "chat_catalog_unavailable"
		observation.HeaderIncomplete = observation.HeaderIncomplete || v.catalogIssue == "chat_catalog_unavailable"
	}
	selected, reason := v.page.Resolve(observation)
	if selected.ID == "" && v.catalogIssue != "" && !pending {
		reason = v.catalogIssue
	}
	result.ID, result.Host, result.Source = selected.ID, selected.Host, selected.Source
	result.Reason = reason
	return result
}

func viewWorker(ctx context.Context, home string, requests <-chan viewRequest, publish func(viewProbe)) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var automation *comObject
	if int32(call(ole32, "CoInitializeEx", 0, 0)) >= 0 {
		defer call(ole32, "CoUninitialize")
		if int32(call(ole32, "CoCreateInstance", uintptr(unsafe.Pointer(&automationCLSID)), 0, 1, uintptr(unsafe.Pointer(&automationIID)), uintptr(unsafe.Pointer(&automation)))) < 0 {
			automation = nil
		}
	}
	if automation != nil {
		defer comCall(automation, 2)
		// IUIAutomation2 permits bounded provider queries; disabling AutoSetFocus
		// ensures this read-only observer never redirects keyboard focus.
		iid := windows.GUID{Data1: 0x34723aff, Data2: 0x0c9d, Data3: 0x49d0, Data4: [8]byte{0x98, 0x96, 0x7a, 0xb5, 0x2d, 0xf8, 0xcd, 0x8a}}
		var automation2 *comObject
		if int32(comCall(automation, 0, uintptr(unsafe.Pointer(&iid)), uintptr(unsafe.Pointer(&automation2)))) >= 0 && automation2 != nil {
			comCall(automation2, 59, 0)
			comCall(automation2, 61, 200)
			comCall(automation2, 63, 500)
			comCall(automation2, 2)
		}
	}
	v := viewLocator{}
	p := popupLocator{}
	defer p.close()
	var last viewSelection
	var checked time.Time
	wasPopup := false
	defer v.close()

	for {
		select {
		case <-ctx.Done():
			return
		case request := <-requests:
			window := request.Window
			if automation != nil {
				v.bind(automation, window, home)
			}
			o := p.locate(automation, window, v.root)
			changedWindow := last.Window != window
			if changedWindow {
				last, checked, wasPopup = viewSelection{Window: window}, time.Time{}, false
			}
			blocked := o.Visible || !o.Available && wasPopup
			// Dialog focus traps can remove the selected row from accessibility.
			// A known chat stays subscribed while its overlay is hidden.
			if (!blocked || last.ID == "") && (request.Selection || request.Refresh || changedWindow || wasPopup && !blocked || time.Since(checked) >= 2*time.Second) {
				if automation == nil {
					last = viewSelection{Window: window, Reason: "uia_unavailable"}
				} else {
					if request.Selection {
						v.page.Invalidate()
					}
					candidate := v.locate(automation, window, home)
					// A popup may open while selection lookup is in progress.
					// Recheck before an empty result could clear a verified chat.
					if last.ID != "" && candidate.ID == "" {
						o = p.locate(automation, window, v.root)
						if !o.Visible {
							last = candidate
						}
					} else {
						last = candidate
					}
				}
				checked = time.Now()
			}
			if o.Available {
				wasPopup = o.Visible
			}
			publish(viewProbe{Epoch: request.Epoch, Events: v.events.eventOK && v.events.propertyOK && v.events.structureOK, Selection: last, Popup: o})
			if o.Source == "uia_popup_query_failed" && time.Since(v.bound) >= 5*time.Second {
				v.releaseRoot()
			}
		}
	}
}

func refreshElement(el, cache *comObject) *comObject {
	if el == nil {
		return nil
	}
	var fresh *comObject
	comCall(el, 9, uintptr(unsafe.Pointer(cache)), uintptr(unsafe.Pointer(&fresh)))
	comCall(el, 2)
	return fresh
}

func (v *viewLocator) releaseRoot() {
	v.events.close()
	for _, el := range []*comObject{v.document, v.root} {
		if el != nil {
			comCall(el, 2)
		}
	}
	v.document, v.root = nil, nil
	// A same-window provider refresh preserves the current binding. Navigation
	// or failure to reacquire the root invalidates it before reuse.
}
func (v *viewLocator) close() {
	v.releaseRoot()
	v.page = ipc.PageIdentity{}
	for _, el := range []*comObject{v.cache, v.documentCondition, v.selectedCondition, v.textCondition, v.toolbarCondition, v.titleCondition, v.composerCondition, v.composerCache} {
		if el != nil {
			comCall(el, 2)
		}
	}
	v.threadCatalog.close()
}
func (v *viewLocator) bind(a *comObject, window uintptr, home string) {
	if v.root != nil && v.host == window && (v.events.eventOK && v.events.propertyOK && v.events.structureOK || time.Since(v.bound) < 30*time.Second) {
		return
	}
	v.releaseRoot()
	if v.host != window {
		v.page = ipc.PageIdentity{}
	}
	v.host, v.home = window, home
	if v.cache == nil {
		v.cache = newCache(a, []int{30003, 30005, 30012, 30022, 30045, 30093, 30101, 30102})
		if v.cache == nil {
			return
		}
		v.documentCondition = propertyCondition(a, 30003, variant{Type: 3, Data: [2]uintptr{50030}}, 0)
		v.textCondition = propertyCondition(a, 30003, variant{Type: 3, Data: [2]uintptr{50020}}, 0)
		v.toolbarCondition = propertyCondition(a, 30003, variant{Type: 3, Data: [2]uintptr{50021}}, 0)
		v.titleCondition = stringCondition(a, 30012, "font-medium", 2)
		v.composerCondition = stringCondition(a, 30012, "ProseMirror", 2)
		// Do not request Name/Value/LegacyValue from an editor: those can
		// contain an unsent draft. Only class, visibility and runtime ID are used.
		v.composerCache = newCache(a, []int{30012, 30022})
		text := call(oleaut32, "SysAllocString", uintptr(unsafe.Pointer(utf("current=page"))))
		v.selectedCondition = propertyCondition(a, 30102, variant{Type: 8, Data: [2]uintptr{text}}, 2)
		call(oleaut32, "SysFreeString", text)
	}
	if int32(comCall(a, 6, window, uintptr(unsafe.Pointer(&v.root)))) >= 0 && v.root != nil {
		v.bound = time.Now()
		v.events.bind(a, v.root, window)
	}
}
func (v *viewLocator) resolveURL(value, home string) (string, string) {
	id, host := ipc.ViewRoute(value)
	if ipc.ValidThreadID(id) {
		return id, host
	}
	if id != "" {
		v.loadCatalog(home)
		if resolved := v.catalog.Bindings[id]; ipc.ValidThreadID(resolved) {
			return resolved, host
		}
	}
	return "", ""
}
