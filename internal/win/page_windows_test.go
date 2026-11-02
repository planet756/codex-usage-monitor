package win

import (
	"os"
	"path/filepath"
	"testing"
	"unsafe"

	"golang.org/x/sys/windows"
)

func pageFixture(t *testing.T) (*comObject, *comObject) {
	t.Helper()
	selectionFixture.array = &comObject{&fixtureArrayTable[0]}
	selectionFixture.urls = make(map[*comObject]string)
	selectionFixture.conditionRows = make(map[*comObject][]*comObject)
	selectionFixture.classes = make(map[*comObject]string)
	selectionFixture.names = make(map[*comObject]string)
	selectionFixture.automationIDs = make(map[*comObject]string)
	selectionFixture.controlTypes = make(map[*comObject]int64)
	selectionFixture.runtimeIDs = make(map[*comObject][]int32)
	selectionFixture.roles = make(map[*comObject]string)
	selectionFixture.findCalls = make(map[*comObject]int)
	selectionFixture.findScopes = make(map[*comObject][]uintptr)
	t.Cleanup(func() {
		selectionFixture.rows, selectionFixture.activeRows = nil, nil
		selectionFixture.urls, selectionFixture.conditionRows = nil, nil
		selectionFixture.classes, selectionFixture.runtimeIDs = nil, nil
		selectionFixture.names = nil
		selectionFixture.automationIDs, selectionFixture.controlTypes = nil, nil
		selectionFixture.array, selectionFixture.document = nil, nil
		selectionFixture.findCalls = nil
		selectionFixture.roles, selectionFixture.findScopes = nil, nil
	})
	return &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}
}

func TestNativeCollapsedSidebarKeepsEditorAndNewEditorClearsChat(t *testing.T) {
	root, selected := pageFixture(t)
	composer := &comObject{&fixtureRootTable[0]}
	selectionFixture.urls[selected] = "codex://threads/" + resolverChat + "?hostId=local"
	selectionFixture.classes[composer] = "ProseMirror focus:outline-none"
	selectionFixture.runtimeIDs[composer] = []int32{42, 7, 123}
	selectionFixture.conditionRows[root] = []*comObject{selected}
	selectionFixture.conditionRows[composer] = []*comObject{composer}
	v := viewLocator{host: 1, root: root, selectedCondition: root, composerCondition: composer, composerCache: root}
	if got := v.locate(nil, 1, ""); got.ID != resolverChat {
		t.Fatalf("initial: %+v", got)
	}
	selectionFixture.conditionRows[root] = nil
	for range 3 {
		if got := v.locate(nil, 1, ""); got.ID != resolverChat || got.Source != "uia_composer_continuity" {
			t.Fatalf("collapsed: %+v", got)
		}
	}
	selectionFixture.runtimeIDs[composer] = []int32{42, 7, 124}
	if got := v.locate(nil, 1, ""); got.ID != "" {
		t.Fatalf("new chat inherited usage: %+v", got)
	}
	selectionFixture.runtimeIDs[composer] = make([]int32, 33)
	if got := v.composerAnchor(); got != "" {
		t.Fatal("oversized runtime ID accepted")
	}
}

func TestNativeNewChatBindingRefreshesWithoutFocus(t *testing.T) {
	root, doc := pageFixture(t)
	home := t.TempDir()
	client := "client-new-thread:00000000-0000-7000-8000-000000000001"
	selectionFixture.document = doc
	selectionFixture.urls[doc] = "file:///app/index.html#/local/" + client + "?hostId=local"
	v := viewLocator{host: 1, home: home, root: root, selectedCondition: root, documentCondition: root}
	if got := v.locate(nil, 1, home); got.ID != "" || got.Reason != "pending_chat_identity" {
		t.Fatalf("unbound: %+v", got)
	}
	state := `{"electron-persisted-atom-state":{"client-thread-bindings-v1":{"` + client + `":"` + resolverChat + `"}}}`
	if err := os.WriteFile(filepath.Join(home, ".codex-global-state.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	if got := v.locate(nil, 1, home); got.ID != resolverChat || got.Host != "local" || got.Reason != "verified" {
		t.Fatalf("binding was not refreshed: %+v", got)
	}
}

func TestNativeCatalogKeepsHostAndRejectsDuplicateTitles(t *testing.T) {
	root, title := pageFixture(t)
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "sqlite"), 0700); err != nil {
		t.Fatal(err)
	}
	path, _ := windows.BytePtrFromString(filepath.Join(home, "sqlite", "codex.db"))
	var db uintptr
	if call(sqlite, "sqlite3_open_v2", uintptr(unsafe.Pointer(path)), uintptr(unsafe.Pointer(&db)), 6, 0) != 0 {
		t.Fatal("open test catalog")
	}
	defer call(sqlite, "sqlite3_close_v2", db)
	exec := func(query string) {
		q, _ := windows.BytePtrFromString(query)
		if call(sqlite, "sqlite3_exec", db, uintptr(unsafe.Pointer(q)), 0, 0, 0) != 0 {
			t.Fatal("write test catalog")
		}
	}
	exec("CREATE TABLE local_thread_catalog(thread_id TEXT,host_id TEXT,display_title TEXT,missing_candidate INTEGER);INSERT INTO local_thread_catalog VALUES('" + resolverChat + "','local','Exact header',0);")
	v := viewLocator{home: home}
	defer v.close()
	if s := v.catalogSelection("Exact header"); s.ID != resolverChat || s.Host != "local" {
		t.Fatalf("catalog: %+v", s)
	}
	if s := v.catalogSelection("Exact"); s.ID != "" {
		t.Fatal("partial title matched")
	}
	toolbar, toolbarCondition, titleCondition := &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}
	selectionFixture.classes[toolbar] = "min-w-0 shrink-0 select-none"
	// Real installed Desktop 26.1021.2667.0 default Chat title classes. The
	// old px-1.5 fixture hid the production selector's dependency on padding.
	selectionFixture.classes[title] = "min-w-0 text-base font-medium text-default px-button-toolbar-inline min-h-token-button-composer content-center -ms-0.5 truncate leading-6"
	selectionFixture.names[title] = "Exact header"
	selectionFixture.conditionRows[toolbarCondition] = []*comObject{toolbar}
	selectionFixture.conditionRows[titleCondition] = []*comObject{title}
	v.root, v.selectedCondition, v.toolbarCondition, v.titleCondition = root, root, toolbarCondition, titleCondition
	if got := v.locate(nil, 1, home); got.ID != resolverChat || got.Host != "local" || got.Source != "uia_chat_header" {
		t.Fatalf("header without any sidebar: %+v", got)
	}
	v.page.Invalidate()
	if got := v.locate(nil, 1, home); got.ID != resolverChat || got.Reason != "verified" || !got.PageEvidenceRequired || got.HeaderProbe != ([3]int{1, 1, 1}) {
		t.Fatalf("real current title could not discharge page verification: %+v", got)
	}
	// Replay the real accessible shape: wrapper omitted; direct Text child.
	caption, textCondition := &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}
	selectionFixture.names[caption] = "Exact header"
	selectionFixture.roles[caption] = "description"
	selectionFixture.controlTypes[caption] = 50020
	selectionFixture.conditionRows[titleCondition] = nil
	selectionFixture.conditionRows[textCondition] = []*comObject{caption}
	v.textCondition = textCondition
	v.page.Invalidate()
	if got := v.locate(nil, 1, home); got.ID != resolverChat || got.HeaderProbe != ([3]int{1, 1, 1}) {
		t.Fatalf("real classless caption did not recover the current page: %+v", got)
	}
	scopes := selectionFixture.findScopes[textCondition]
	selectionFixture.conditionRows[root] = make([]*comObject, 33)
	for i := range selectionFixture.conditionRows[root] {
		selectionFixture.conditionRows[root][i] = title
	}
	v.page.Invalidate()
	if got := v.locate(nil, 1, home); got.ID != resolverChat || got.Source != "uia_chat_header" || got.SelectedElements != 33 {
		t.Fatal("partial sidebar suppressed the independent header", got)
	}
	selectionFixture.conditionRows[root] = nil
	if len(scopes) == 0 || scopes[len(scopes)-1] != 2 {
		t.Fatal("caption query traversed descendants instead of direct children")
	}
	second := &comObject{&fixtureRootTable[0]}
	selectionFixture.names[second], selectionFixture.roles[second], selectionFixture.controlTypes[second] = "Unrelated status", "description", 50020
	selectionFixture.conditionRows[textCondition] = []*comObject{caption, second}
	if got := v.locate(nil, 1, home); got.ID != "" || got.Reason != "selected_chat_ambiguous" {
		t.Fatal("multiple captions established identity", got)
	}
	selectionFixture.conditionRows[textCondition] = []*comObject{caption}
	exec("INSERT INTO local_thread_catalog VALUES('" + resolverChat + "','remote','Exact header',0);")
	if s := v.catalogSelection("Exact header"); s.ID != "" {
		t.Fatal("ambiguous host matched")
	}
	if got := v.locate(nil, 1, home); got.ID != "" {
		t.Fatal("ambiguous header retained previous chat")
	}
}
