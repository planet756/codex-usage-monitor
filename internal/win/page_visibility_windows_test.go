package win

import (
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"codex-usage-monitor/internal/metrics"
	popupstate "codex-usage-monitor/internal/popup"
	"golang.org/x/sys/windows"
)

func TestNativeChatPageRoutesOverrideRetainedSidebarAndComposer(t *testing.T) {
	root, selected := pageFixture(t)
	doc, composer := &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}
	selectionFixture.urls[selected] = "codex://threads/" + resolverChat + "?hostId=local"
	selectionFixture.classes[composer] = "ProseMirror"
	selectionFixture.runtimeIDs[composer] = []int32{42, 1}
	selectionFixture.conditionRows[root] = []*comObject{selected}
	selectionFixture.conditionRows[composer] = []*comObject{composer}
	v := viewLocator{root: root, document: doc, selectedCondition: root, composerCondition: composer, composerCache: root}
	defer v.close()
	for _, route := range []string{"/local/" + resolverChat + "?hostId=local", "/plugins", "/settings/appearance", "/automations", "/", "/local/" + resolverChat + "?hostId=local"} {
		selectionFixture.urls[doc] = "file:///app/index.html#" + route
		got := v.locate(nil, 1, "")
		switch route {
		case "/plugins", "/settings/appearance", "/automations":
			if got.ChatPage || got.ID != "" || got.Reason != "non_chat_page" {
				t.Fatal("application route inherited the old chat", got)
			}
		case "/":
			if !got.ChatPage || got.ID != "" || got.Reason != "pending_chat_identity" {
				t.Fatal("new draft inherited the retained selected row", got)
			}
		default:
			if !got.ChatPage || got.ID != resolverChat {
				t.Fatal("returning chat did not recover", got)
			}
		}
	}
	selectionFixture.urls[doc] = "file:///app/index.html"
	selectionFixture.conditionRows[composer] = nil
	if got := v.locate(nil, 1, ""); got.ChatPage {
		t.Fatal("sidebar selection alone exposed the overlay", got)
	}
	selectionFixture.conditionRows[composer] = []*comObject{composer}
	if got := v.locate(nil, 1, ""); !got.ChatPage || got.ID != resolverChat {
		t.Fatal("fresh composer and selected chat did not restore visibility", got)
	}
}

func TestNativeChatPageDraftAndTitlebarNeedNoResolvedChatID(t *testing.T) {
	root, composer := pageFixture(t)
	selectionFixture.classes[composer] = "ProseMirror"
	selectionFixture.runtimeIDs[composer] = []int32{42, 2}
	selectionFixture.conditionRows[root] = nil
	selectionFixture.conditionRows[composer] = []*comObject{composer}
	v := viewLocator{root: root, selectedCondition: root, composerCondition: composer, composerCache: root}
	defer v.close()
	if got := v.locate(nil, 1, ""); !got.ChatPage || got.ID != "" {
		t.Fatal("draft composer was hidden before obtaining an ID", got)
	}
	toolbar, condition := &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}
	selectionFixture.classes[toolbar] = "min-w-0 shrink-0 select-none"
	selectionFixture.conditionRows[composer] = nil
	selectionFixture.conditionRows[condition] = []*comObject{toolbar}
	v.toolbarCondition, v.titleCondition = condition, root
	if got := v.locate(nil, 1, ""); !got.ChatPage || got.ID != "" {
		t.Fatal("chat toolbar incorrectly required a catalog title or editor", got)
	}
}

func TestNativeChatPageGateHidesStripAndExpandedPanelDespiteLateData(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	window := func() uintptr {
		h := call(user32, "CreateWindowExW", 0x08000080, uintptr(unsafe.Pointer(utf("STATIC"))), 0,
			0x90000000, signed(-32000), signed(-32000), 16, 16, 0, 0, 0, 0)
		if h == 0 {
			t.Fatal("create isolated test window")
		}
		return h
	}
	host := window()
	defer call(user32, "DestroyWindow", host)
	strip := window()
	defer call(user32, "DestroyWindow", strip)
	a := overlay{host: host, hwnd: strip, pid: windows.GetCurrentProcessId(),
		configPath:   filepath.Join(t.TempDir(), "settings.json"),
		viewResult:   viewSelection{Window: host, ID: resolverChat, Host: "local"},
		viewRequests: make(chan viewRequest, 1),
		popup:        popupstate.State{Window: uint64(host)}}
	for range 3 {
		a.data = dataUpdate{ID: resolverChat, Snapshot: metrics.Snapshot{HasCounts: true, Turns: 12, Steps: 34}}
		a.shown, a.expanded = true, true
		a.refresh(false)
		if a.shown || a.expanded || call(user32, "IsWindowVisible", strip) != 0 || a.statusReason != "non_chat_page" {
			t.Fatal("late accounting data bypassed the chat-page display gate", a.statusReason)
		}
	}
}

func TestNativeChatPageStructuralRefreshDoesNotInvalidateChatIdentity(t *testing.T) {
	a := overlay{host: 1, viewResult: viewSelection{Window: 1, ChatPage: true, ID: resolverChat},
		viewRequests: make(chan viewRequest, 1), viewEvents: true, viewRequested: time.Now().Add(-time.Second)}
	a.requestView(true)
	request := <-a.viewRequests
	if !request.Refresh || request.Selection || a.viewResult.ID != resolverChat {
		t.Fatal("structural page refresh was treated as identity navigation", request)
	}
}
