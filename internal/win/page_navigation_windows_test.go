package win

import (
	"os"
	"path/filepath"
	"testing"

	"codex-usage-monitor/internal/metrics"
)

func TestNativeFreshSidebarSurvivesEditorChangesAndNewDraftClearsUsage(t *testing.T) {
	root, selected := pageFixture(t)
	composer, toolbar, title := &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}
	toolbars, titles := &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}
	selectionFixture.urls[selected] = "codex://threads/" + resolverChat + "?hostId=local"
	selectionFixture.classes[composer] = "ProseMirror"
	selectionFixture.runtimeIDs[composer] = []int32{42, 7, 1}
	selectionFixture.classes[toolbar] = "min-w-0 shrink-0 select-none"
	selectionFixture.classes[title] = "min-w-0 px-1.5 text-base font-medium text-default"
	selectionFixture.names[title] = "New chat"
	selectionFixture.conditionRows[root] = []*comObject{selected}
	selectionFixture.conditionRows[composer] = []*comObject{composer}
	selectionFixture.conditionRows[toolbars] = []*comObject{toolbar}
	selectionFixture.conditionRows[titles] = []*comObject{title}
	home := t.TempDir()
	v := viewLocator{host: 1, home: home, root: root, selectedCondition: root, composerCondition: composer, composerCache: root, toolbarCondition: toolbars, titleCondition: titles}
	defer v.close()
	if got := v.locate(nil, 1, home); got.ID != resolverChat {
		t.Fatal("initial sidebar selection", got)
	}
	for range 3 {
		v.locate(nil, 1, home)
	}
	if selectionFixture.findCalls[toolbars] != 0 {
		t.Fatal("stable sidebar polls queried the header tree")
	}
	monitor := overlay{viewResult: viewSelection{Window: 1, ID: resolverChat, Host: "local"},
		viewSelections: make(chan viewSelection, 1), data: dataUpdate{ID: resolverChat, Host: "local",
			Snapshot: metrics.Snapshot{HasCounts: true, Turns: 99, Steps: 999, HasUsage: true}}}
	selectionFixture.runtimeIDs[composer] = []int32{42, 7, 2}
	if got := v.locate(nil, 1, home); got.ID != resolverChat || selectionFixture.findCalls[toolbars] != 0 {
		t.Fatal("editor refresh incorrectly required a header for a fresh current row", got)
	}
	// An actually unselected/new page has neither a current row nor a bound
	// document; a changed editor alone may not retain the previous usage.
	selectionFixture.runtimeIDs[composer] = []int32{42, 7, 3}
	selectionFixture.conditionRows[root] = nil
	for range 5 {
		got := v.locate(nil, 1, home)
		if got.ID != "" {
			t.Fatal("new editor inherited stale sidebar statistics", got)
		}
		monitor.setView(got)
		if monitor.data.Snapshot.HasCounts || monitor.data.Snapshot.HasUsage || monitor.data.Snapshot.Turns != 0 {
			t.Fatal("overlay retained old statistics while navigation was unverified")
		}
	}
	if selectionFixture.findCalls[toolbars] != 5 {
		t.Fatal("navigation bypassed the header query")
	}
	v.releaseRoot()
	v.root = root
	if got := v.locate(nil, 1, home); got.ID != "" {
		t.Fatal("provider rebind revived the old selection", got)
	}
	document := &comObject{&fixtureRootTable[0]}
	client := "client-new-thread:00000000-0000-7000-8000-000000000002"
	selectionFixture.urls[document] = "file:///app/index.html#/local/" + client + "?hostId=local"
	v.document = document
	if got := v.locate(nil, 1, home); got.ID != "" || got.Reason != "pending_chat_identity" {
		t.Fatal("unbound new chat inherited old statistics", got)
	}
	state := `{"electron-persisted-atom-state":{"client-thread-bindings-v1":{"` + client + `":"` + physicalChild + `"}}}`
	if err := os.WriteFile(filepath.Join(home, ".codex-global-state.json"), []byte(state), 0600); err != nil {
		t.Fatal(err)
	}
	selectionFixture.conditionRows[root] = nil
	if got := v.locate(nil, 1, home); got.ID != physicalChild || got.Host != "local" {
		t.Fatal("new page binding did not recover", got)
	}
	selectionFixture.urls[document] = ""
	if got := v.locate(nil, 1, home); got.ID != physicalChild || got.Source != "uia_composer_continuity" {
		t.Fatal("collapsed workspace lost the verified new page", got)
	}
}

func TestNativePageRefreshKeepsVerifiedComposer(t *testing.T) {
	root, selected := pageFixture(t)
	composer := &comObject{&fixtureRootTable[0]}
	selectionFixture.urls[selected] = "codex://threads/" + resolverChat + "?hostId=local"
	selectionFixture.classes[composer] = "ProseMirror"
	selectionFixture.runtimeIDs[composer] = []int32{42, 7, 1}
	selectionFixture.conditionRows[root] = []*comObject{selected}
	selectionFixture.conditionRows[composer] = []*comObject{composer}
	v := viewLocator{host: 1, root: root, selectedCondition: root, composerCondition: composer, composerCache: root}
	defer v.close()
	if got := v.locate(nil, 1, ""); got.ID != resolverChat {
		t.Fatal("initial binding", got)
	}
	v.releaseRoot()
	v.root = root
	selectionFixture.conditionRows[root] = nil // Project remains collapsed.
	if got := v.locate(nil, 1, ""); got.ID != resolverChat || got.Source != "uia_composer_continuity" {
		t.Fatal("same-window provider refresh lost a verified unchanged editor", got)
	}
}
