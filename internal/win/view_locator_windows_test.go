package win

import (
	"runtime"
	"syscall"
	"testing"
	"unsafe"
)

// A local COM provider exercises the actual locator without starting Codex.
// No source extraction or duplicate implementation of locate is used.
var selectionFixture struct {
	rows          []*comObject
	urls          map[*comObject]string
	array         *comObject
	document      *comObject
	conditionRows map[*comObject][]*comObject
	activeRows    []*comObject
	classes       map[*comObject]string
	names         map[*comObject]string
	automationIDs map[*comObject]string
	controlTypes  map[*comObject]int64
	runtimeIDs    map[*comObject][]int32
	roles         map[*comObject]string
	findCalls     map[*comObject]int
	findScopes    map[*comObject][]uintptr
}
var fixtureRef = syscall.NewCallback(func(self uintptr) uintptr { return 1 })
var fixtureFind = syscall.NewCallback(func(self, scope, condition, cache, out uintptr) uintptr {
	if selectionFixture.findCalls != nil {
		selectionFixture.findCalls[(*comObject)(unsafe.Pointer(condition))]++
	}
	if selectionFixture.findScopes != nil {
		key := (*comObject)(unsafe.Pointer(condition))
		selectionFixture.findScopes[key] = append(selectionFixture.findScopes[key], scope)
	}
	selectionFixture.activeRows = selectionFixture.rows
	if rows, ok := selectionFixture.conditionRows[(*comObject)(unsafe.Pointer(condition))]; ok {
		selectionFixture.activeRows = rows
	}
	*(**comObject)(unsafe.Pointer(out)) = selectionFixture.array
	return 0
})
var fixtureDocument = syscall.NewCallback(func(self, scope, condition, cache, out uintptr) uintptr {
	*(**comObject)(unsafe.Pointer(out)) = selectionFixture.document
	return 0
})
var fixtureRefresh = syscall.NewCallback(func(self, cache, out uintptr) uintptr {
	*(*uintptr)(unsafe.Pointer(out)) = self
	return 0
})
var fixtureLength = syscall.NewCallback(func(self, out uintptr) uintptr {
	*(*int32)(unsafe.Pointer(out)) = int32(len(selectionFixture.activeRows))
	return 0
})
var fixtureElement = syscall.NewCallback(func(self, index, out uintptr) uintptr {
	*(**comObject)(unsafe.Pointer(out)) = selectionFixture.activeRows[index]
	return 0
})
var fixtureProperty = syscall.NewCallback(func(self, id, out uintptr) uintptr {
	v := (*variant)(unsafe.Pointer(out))
	*v = variant{}
	if id == 30003 {
		*v = variant{Type: 3, Data: [2]uintptr{uintptr(selectionFixture.controlTypes[(*comObject)(unsafe.Pointer(self))])}}
	}
	if id == 30011 {
		value := selectionFixture.automationIDs[(*comObject)(unsafe.Pointer(self))]
		*v = variant{Type: 8, Data: [2]uintptr{call(oleaut32, "SysAllocString", uintptr(unsafe.Pointer(utf(value))))}}
	}
	if id == 30022 {
		*v = variant{Type: 11}
	}
	if id == 30093 {
		value := selectionFixture.urls[(*comObject)(unsafe.Pointer(self))]
		*v = variant{Type: 8, Data: [2]uintptr{call(oleaut32, "SysAllocString", uintptr(unsafe.Pointer(utf(value))))}}
	}
	if id == 30012 {
		value := selectionFixture.classes[(*comObject)(unsafe.Pointer(self))]
		*v = variant{Type: 8, Data: [2]uintptr{call(oleaut32, "SysAllocString", uintptr(unsafe.Pointer(utf(value))))}}
	}
	if id == 30101 {
		value := selectionFixture.roles[(*comObject)(unsafe.Pointer(self))]
		*v = variant{Type: 8, Data: [2]uintptr{call(oleaut32, "SysAllocString", uintptr(unsafe.Pointer(utf(value))))}}
	}
	if id == 30005 {
		value := selectionFixture.names[(*comObject)(unsafe.Pointer(self))]
		*v = variant{Type: 8, Data: [2]uintptr{call(oleaut32, "SysAllocString", uintptr(unsafe.Pointer(utf(value))))}}
	}
	return 0
})
var fixtureRuntime = syscall.NewCallback(func(self, out uintptr) uintptr {
	values := selectionFixture.runtimeIDs[(*comObject)(unsafe.Pointer(self))]
	*(*uintptr)(unsafe.Pointer(out)) = 0
	if len(values) == 0 {
		return 0x80004005
	}
	array := call(oleaut32, "SafeArrayCreateVector", 3, 0, uintptr(len(values)))
	if array == 0 {
		return 0x8007000e
	}
	for i, value := range values {
		index := int32(i)
		call(oleaut32, "SafeArrayPutElement", array, uintptr(unsafe.Pointer(&index)), uintptr(unsafe.Pointer(&value)))
	}
	*(*uintptr)(unsafe.Pointer(out)) = array
	return 0
})
var fixtureNoPattern = syscall.NewCallback(func(self, pattern, out uintptr) uintptr { *(*uintptr)(unsafe.Pointer(out)) = 0; return 0x80004005 })
var fixtureRootTable = [17]uintptr{1: fixtureRef, 2: fixtureRef, 4: fixtureRuntime, 7: fixtureDocument, 8: fixtureFind, 9: fixtureRefresh, 12: fixtureProperty, 16: fixtureNoPattern}
var fixtureArrayTable = [5]uintptr{1: fixtureRef, 2: fixtureRef, 3: fixtureLength, 4: fixtureElement}

func TestNativeLocatorRechecksAmbiguityOnEveryQuery(t *testing.T) {
	defer func() {
		selectionFixture.rows = nil
		selectionFixture.urls = nil
		selectionFixture.array = nil
		selectionFixture.document = nil
	}()
	a, b := &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}
	selectionFixture.rows = []*comObject{a, b}
	selectionFixture.urls = map[*comObject]string{a: "codex://threads/00000000-0000-7000-8000-000000000001", b: "codex://threads/00000000-0000-7000-8000-000000000002"}
	selectionFixture.array = &comObject{&fixtureArrayTable[0]}
	root := &comObject{&fixtureRootTable[0]}
	v := viewLocator{host: 1, root: root, selectedCondition: root}
	for range 3 {
		got := v.locate(nil, 1, "")
		if got.ID != "" || got.Reason != "selected_chat_ambiguous" {
			t.Fatalf("ambiguous native query became verified: %+v", got)
		}
	}
	selectionFixture.rows = []*comObject{b}
	if got := v.locate(nil, 1, ""); got.ID != "00000000-0000-7000-8000-000000000002" {
		t.Fatalf("unique native row failed to recover: %+v", got)
	}
	selectionFixture.rows = make([]*comObject, 33)
	for i := range selectionFixture.rows {
		selectionFixture.rows[i] = b
	}
	if got := v.locate(nil, 1, ""); got.ID != "" || got.Reason != "uia_query_incomplete" {
		t.Fatal("truncated native query established uniqueness")
	}
	runtime.KeepAlive(a)
	runtime.KeepAlive(b)
	runtime.KeepAlive(root)
}

func TestNativeDocumentRouteTakesPrecedenceAndTracksNavigation(t *testing.T) {
	root, _ := pageFixture(t)
	document := &comObject{&fixtureRootTable[0]}
	selectionFixture.document = document
	v := viewLocator{host: 1, root: root, selectedCondition: root, documentCondition: root}
	defer v.close()

	const a = "00000000-0000-7000-8000-000000000001"
	const b = "00000000-0000-7000-8000-000000000002"
	const route = "codex://threads/"
	for _, tc := range []struct {
		name, document string
		sidebar        []string
		id, reason     string
	}{
		{"document beats retained sidebar", route + a, []string{b}, a, "verified"},
		{"document navigation refreshes identity", route + b, []string{a}, b, "verified"},
		{"document works without sidebar", route + b, nil, b, "verified"},
		{"new draft retires identity", "file:///app/index.html#/", []string{b}, "", "pending_chat_identity"},
		{"document recovers after draft", route + a, []string{b}, a, "verified"},
		{"missing document preserves ambiguity check", "", []string{a, b}, "", "selected_chat_ambiguous"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selectionFixture.urls[document] = tc.document
			var rows []*comObject
			for _, id := range tc.sidebar {
				row := &comObject{&fixtureRootTable[0]}
				selectionFixture.urls[row] = route + id
				rows = append(rows, row)
			}
			selectionFixture.conditionRows[root] = rows
			for range 3 {
				got := v.locate(nil, 1, "")
				if got.ID != tc.id || got.Reason != tc.reason {
					t.Fatalf("page identity: %+v; want ID %q, reason %q", got, tc.id, tc.reason)
				}
				if tc.id != "" && got.Source != "uia_document_url" {
					t.Fatalf("identity was not established by the current document: %+v", got)
				}
			}
		})
	}
}
