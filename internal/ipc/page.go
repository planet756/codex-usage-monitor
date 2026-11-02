package ipc

import "strings"

// A default new-chat label cannot establish identity even if one saved chat
// happens to have that exact title. An explicit URL can still identify it.
func PlaceholderChatTitle(title string) bool {
	switch strings.ToLower(strings.TrimSpace(title)) {
	case "", "chat", "new chat", "new thread", "untitled", "untitled chat", "聊天", "对话", "新聊天", "新对话", "新建对话", "新会话", "新建会话", "未命名":
		return true
	}
	return false
}

// PageObservation is a fresh, bounded native query of one HWND. Anchor is the
// visible composer's runtime ID, never its value or draft text. It identifies
// an accessibility object lifetime, not a chat; only use it for continuity when
// direct page evidence is absent, never to veto a freshly selected chat.
type PageObservation struct {
	Window                              uint64
	Anchor                              string
	Document, Header, Sidebar           Selection
	Ambiguous, Pending                  bool
	SidebarIncomplete, HeaderIncomplete bool
}

// PageIdentity retains only one verified editor binding. An unavailable editor,
// changed window, conflicting observation or explicit unbound route retires it.
// A previously selected row, on its own, never establishes the current page.
type PageIdentity struct {
	window uint64
	anchor string
	bound  Selection
}

// An unavailable native root retires cached continuity. Fresh page evidence
// can establish identity again without depending on an optional header.
func (p *PageIdentity) Invalidate() {
	p.bound = Selection{}
}

func (p *PageIdentity) Resolve(o PageObservation) (Selection, string) {
	previous := p.bound
	continuity := o.Window == p.window && o.Anchor != "" && o.Anchor == p.anchor
	*p = PageIdentity{window: o.Window, anchor: o.Anchor}
	if o.Ambiguous {
		return Selection{}, "selected_chat_ambiguous"
	}
	if o.Pending {
		return Selection{}, "pending_chat_identity"
	}
	var selected Selection
	for i, s := range []Selection{o.Document, o.Header, o.Sidebar} {
		if i == 1 && o.HeaderIncomplete || i == 2 && o.SidebarIncomplete {
			continue
		}
		if s.ID == "" {
			continue
		}
		if selected.ID != "" && (selected.ID != s.ID || selected.Host != "" && s.Host != "" && selected.Host != s.Host) {
			return Selection{}, "selected_chat_ambiguous"
		}
		if selected.ID == "" {
			selected = s
		} else if selected.Host == "" {
			selected.Host = s.Host
		}
	}
	if selected.ID != "" {
		p.bound = selected
		return selected, "verified"
	}
	if o.SidebarIncomplete || o.HeaderIncomplete {
		return Selection{}, "uia_query_incomplete"
	}
	if continuity && previous.ID != "" {
		p.bound = previous
		previous.Source = "uia_composer_continuity"
		return previous, "verified"
	}
	return Selection{}, "selected_chat_unavailable"
}

// ViewMessage describes observed page state. IPC following membership is not a
// count of open windows and must never be presented as evidence of ambiguity.
func ViewMessage(reason string) string {
	switch reason {
	case "pending_chat_identity":
		return "New chat · waiting for chat ID"
	case "selected_chat_ambiguous":
		return "Current chat identity is ambiguous"
	case "chat_catalog_unavailable":
		return "Chat catalog unavailable · retrying"
	case "verified":
		return "Waiting for selected chat data"
	default:
		return "Waiting for current chat identity"
	}
}
