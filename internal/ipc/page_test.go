package ipc

import "testing"

func TestNewChatPlaceholderCannotResolveByCoincidentalSavedTitle(t *testing.T) {
	for _, title := range []string{"Chat", "New chat", "New thread", "Untitled", "新建对话", " 新会话 "} {
		if !PlaceholderChatTitle(title) {
			t.Fatalf("placeholder accepted: %q", title)
		}
	}
	if PlaceholderChatTitle("A specific chat title") {
		t.Fatal("specific title rejected")
	}
}

func TestCollapsedWorkspaceKeepsOnlyVerifiedVisibleComposer(t *testing.T) {
	var page PageIdentity
	known := Selection{ID: id1, Host: "local", Source: "uia_catalog_title"}
	if s, reason := page.Resolve(PageObservation{Window: 1, Anchor: "editor-a", Sidebar: known}); s != known || reason != "verified" {
		t.Fatalf("initial: %+v %s", s, reason)
	}
	for range 5 {
		s, reason := page.Resolve(PageObservation{Window: 1, Anchor: "editor-a"})
		if s.ID != id1 || s.Host != "local" || s.Source != "uia_composer_continuity" || reason != "verified" {
			t.Fatalf("collapsed: %+v %s", s, reason)
		}
	}
	if s, _ := page.Resolve(PageObservation{Window: 1, Anchor: "editor-b"}); s.ID != "" {
		t.Fatal("new editor inherited previous usage")
	}
	if s, _ := page.Resolve(PageObservation{Window: 1, Anchor: "editor-a"}); s.ID != "" {
		t.Fatal("retired editor binding revived after navigation")
	}
}

func TestHeaderResolvesCollapsedChatWithoutPriorSelection(t *testing.T) {
	var page PageIdentity
	s, reason := page.Resolve(PageObservation{Window: 1, Anchor: "editor", Header: Selection{ID: id2, Host: "local", Source: "uia_chat_header"}})
	if s.ID != id2 || s.Source != "uia_chat_header" || reason != "verified" {
		t.Fatalf("header: %+v %s", s, reason)
	}
}

func TestPageIdentityFencesIncompleteConflictingAndNewViews(t *testing.T) {
	for _, next := range []PageObservation{
		{Window: 2, Anchor: "same"},
		{Window: 1},
		{Window: 1, Anchor: "same", Ambiguous: true},
		{Window: 1, Anchor: "same", SidebarIncomplete: true},
		{Window: 1, Anchor: "same", Pending: true},
		{Window: 1, Anchor: "same", Header: Selection{ID: id1, Host: "local"}, Sidebar: Selection{ID: id2, Host: "local"}},
		{Window: 1, Anchor: "same", Header: Selection{ID: id1, Host: "remote"}, Sidebar: Selection{ID: id1, Host: "local"}},
	} {
		var page PageIdentity
		page.Resolve(PageObservation{Window: 1, Anchor: "same", Sidebar: Selection{ID: id1, Host: "local"}})
		if s, _ := page.Resolve(next); s.ID != "" {
			t.Fatalf("unsafe fallback: %+v -> %+v", next, s)
		}
		if s, _ := page.Resolve(PageObservation{Window: 1, Anchor: "same"}); s.ID != "" {
			t.Fatalf("invalidated binding survived: %+v", next)
		}
	}
}

func TestIndependentPageEvidenceSurvivesFailedOptionalQueries(t *testing.T) {
	var p PageIdentity
	s, reason := p.Resolve(PageObservation{Window: 1, Document: Selection{ID: id1, Host: "local"}, SidebarIncomplete: true})
	if s.ID != id1 || reason != "verified" {
		t.Fatal("valid document was globally vetoed", s, reason)
	}
	s, reason = p.Resolve(PageObservation{Window: 1, Header: Selection{ID: id2, Host: "local"}, SidebarIncomplete: true})
	if s.ID != id2 || reason != "verified" {
		t.Fatal("valid header was globally vetoed", s, reason)
	}
	if s, _ := p.Resolve(PageObservation{Window: 1, Anchor: "old", SidebarIncomplete: true}); s.ID != "" {
		t.Fatal("incomplete evidence inherited a prior page")
	}
}

func TestNavigationRetiresContinuityButFreshSidebarNeedsNoHeader(t *testing.T) {
	var p PageIdentity
	o := PageObservation{Window: 1, Anchor: "same-editor", Sidebar: Selection{ID: id1, Host: "local"}}
	p.Resolve(o)
	if s, _ := p.Resolve(PageObservation{Window: 1, Anchor: "same-editor"}); s.ID != id1 {
		t.Fatal("sidebar collapse lost continuity")
	}
	p.Invalidate()
	if s, _ := p.Resolve(PageObservation{Window: 1, Anchor: "same-editor"}); s.ID != "" {
		t.Fatal("navigation reused an editor's old chat")
	}
	o.Sidebar.ID = id2
	if s, _ := p.Resolve(o); s.ID != id2 {
		t.Fatal("fresh selected chat waited for a title")
	}
}

func TestNewChatResolvesOnlyAfterExactBindingThenSubscribesWithoutMembership(t *testing.T) {
	var page PageIdentity
	var session Session
	session.Handle(decodeTest([]byte(`{"type":"response","method":"initialize","resultType":"success","result":{"clientId":"monitor"}}`)))
	session.SetView(id1, "local")
	client := "client-new-thread:" + id2
	catalog := ViewCatalog{}
	id, host := catalog.URL("/local/" + client + "?hostId=local")
	selection, reason := page.Resolve(PageObservation{Window: 1, Anchor: "new", Document: Selection{ID: id, Host: host}, Pending: id == ""})
	if reason != "pending_chat_identity" || selection.ID != "" {
		t.Fatal("unbound client fabricated identity")
	}
	if out, changed := session.SetView(selection.ID, selection.Host); !changed || len(out) != 1 || session.Active.ID != "" {
		t.Fatal("new draft retained previous subscription")
	}
	catalog.Bindings = map[string]string{client: id2}
	id, host = catalog.URL("/local/" + client + "?hostId=local")
	selection, reason = page.Resolve(PageObservation{Window: 1, Anchor: "new", Document: Selection{ID: id, Host: host}})
	if reason != "verified" || selection.ID != id2 {
		t.Fatal("exact binding failed")
	}
	if out, changed := session.SetView(selection.ID, selection.Host); !changed || len(out) != 1 || session.Active.ID != id2 {
		t.Fatal("new chat waited for membership announcement")
	}
	if out, changed := session.SetView(id1, ""); !changed || len(out) != 1 || session.Active.ID != "" {
		t.Fatal("unknown host was guessed")
	}
}

func TestWaitingMessageDoesNotInferOpenWindowsFromRetainedChats(t *testing.T) {
	if ViewMessage("selected_chat_unavailable") != "Waiting for current chat identity" {
		t.Fatal("unavailable page reported multiple windows")
	}
	if ViewMessage("selected_chat_ambiguous") != "Current chat identity is ambiguous" {
		t.Fatal("conflict lost")
	}
	if ViewMessage("chat_catalog_unavailable") != "Chat catalog unavailable · retrying" {
		t.Fatal("catalog failure lost")
	}
	if ViewMessage("pending_chat_identity") != "New chat · waiting for chat ID" {
		t.Fatal("draft state lost")
	}
}

func TestEditorObjectChangesDoNotVetoFreshSelectedChat(t *testing.T) {
	var page PageIdentity
	old := Selection{ID: id1, Host: "local", Source: "uia_catalog_title"}
	page.Resolve(PageObservation{Window: 1, Anchor: "old-editor", Sidebar: old})
	next := PageObservation{Window: 1, Anchor: "new-editor", Sidebar: old}
	for range 5 {
		if s, reason := page.Resolve(next); s.ID != id1 || reason != "verified" {
			t.Fatalf("editor-object change vetoed a fresh selected page: %+v %s", s, reason)
		}
	}
	page.Invalidate()
	if s, _ := page.Resolve(next); s.ID != id1 {
		t.Fatal("fresh selected row could not recover after provider unavailability")
	}
	next.Header = Selection{ID: id2, Host: "local", Source: "uia_chat_header"}
	if s, reason := page.Resolve(next); s.ID != "" || reason != "selected_chat_ambiguous" {
		t.Fatal("conflicting header and old sidebar accepted", s, reason)
	}
	next.Sidebar = Selection{}
	if s, reason := page.Resolve(next); s.ID != id2 || reason != "verified" {
		t.Fatal("verified destination header did not recover", s, reason)
	}
	if s, _ := page.Resolve(PageObservation{Window: 1, Anchor: "new-editor"}); s.ID != id2 {
		t.Fatal("collapsed workspace lost new binding")
	}
}

func TestFreshSidebarChangeDoesNotWaitForOptionalHeaderOrEditor(t *testing.T) {
	var page PageIdentity
	a := Selection{ID: id1, Host: "local"}
	b := Selection{ID: id2, Host: "local"}
	page.Resolve(PageObservation{Window: 1, Anchor: "editor-a", Sidebar: a})
	next := PageObservation{Window: 1, Anchor: "editor-a", Sidebar: b}
	for range 3 {
		if s, _ := page.Resolve(next); s.ID != id2 {
			t.Fatal("fresh selected chat waited for an optional header", s)
		}
	}
	next.Anchor, next.Document = "editor-b", b
	if s, reason := page.Resolve(next); s.ID != id2 || reason != "verified" {
		t.Fatal("current document failed to recover", s, reason)
	}
}

func TestNewChatRetiresOldSubscriptionUntilPageBindingArrives(t *testing.T) {
	for _, withSidebar := range []bool{false, true} {
		var page PageIdentity
		var session Session
		session.Handle(decodeTest([]byte(`{"type":"response","method":"initialize","resultType":"success","result":{"clientId":"monitor"}}`)))
		old := Selection{ID: id1, Host: "local"}
		page.Resolve(PageObservation{Window: 1, Anchor: "old", Sidebar: old})
		session.SetView(id1, "local")
		next := PageObservation{Window: 1, Anchor: "draft", Sidebar: old, Pending: true}
		if !withSidebar {
			next.Sidebar = Selection{}
		}
		for range 3 {
			s, _ := page.Resolve(next)
			session.SetView(s.ID, s.Host)
			if s.ID != "" || session.Active.ID != "" {
				t.Fatal("draft retained previous chat subscription")
			}
		}
		next.Sidebar = Selection{}
		next.Pending = false
		next.Document = Selection{ID: id2, Host: "local", Source: "uia_document_url"}
		s, reason := page.Resolve(next)
		session.SetView(s.ID, s.Host)
		if s.ID != id2 || reason != "verified" || session.Active.ID != id2 {
			t.Fatal("exact new-chat page binding failed to subscribe", s, reason)
		}
	}
}
