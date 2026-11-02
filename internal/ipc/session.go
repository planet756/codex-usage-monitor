package ipc

// Session handles only initialization and read-only current-chat following.
// It never sends turn, tool, approval, or conversation-editing requests.
type Session struct {
	Router           Router
	Self             string
	Active           Selection
	viewID, viewHost string
}

// SetView supplies the chat read from the target HWND's selected page. A
// missing/ambiguous native identity clears the subscription and old counters.
func (s *Session) SetView(id, host string) (out []any, changed bool) {
	if !ValidThreadID(id) {
		id, host = "", ""
	}
	s.viewID, s.viewHost = id, host
	return s.reselect(nil)
}

func (s *Session) reselect(out []any) ([]any, bool) {
	if s.Self == "" {
		return out, false
	}
	next := s.Router.SelectView(s.viewID, s.viewHost)
	if next.ID == s.Active.ID && next.Host == s.Active.Host {
		s.Active = next
		return out, false
	}
	if s.Active.ID != "" {
		out = append(out, s.follow(s.Active, false))
	}
	s.Active = next
	if next.ID != "" {
		out = append(out, s.follow(next, true))
	}
	return out, true
}

func (s *Session) Handle(h *Frame) (out []any, selectionChanged bool) {
	if h == nil {
		return nil, false
	}
	initialized := h.Type == "response" && h.Method == "initialize" && h.ResultType == "success" && h.Result.ClientID != "" && len(h.Result.ClientID) <= 256
	if initialized {
		s.Self = h.Result.ClientID
		s.Router.SetSelf(s.Self)
		// Native Desktop clients respond to a newly connected client by sending
		// their current following state, even when no textbox is focused.
		out = append(out, s.broadcast("client-status-changed", 0, map[string]any{"clientId": s.Self, "clientType": "codex-usage-monitor", "isSelf": false, "status": "connected"}))
	}
	if h.Type == "client-discovery-request" {
		return append(out, map[string]any{"type": "client-discovery-response", "requestId": h.RequestID, "response": map[string]bool{"canHandle": false}}), false
	}
	if len(h.Targets) > 0 {
		mine := false
		for _, id := range h.Targets {
			mine = mine || id == s.Self
		}
		if !mine {
			return out, false
		}
	}
	changed := s.Router.Observe(h)
	if !changed && !initialized || s.Self == "" {
		return out, false
	}
	return s.reselect(out)
}

func (s *Session) Resync() any {
	return s.ResyncTo("")
}

func (s *Session) ResyncTo(owner string) any {
	if s.Active.ID == "" || s.Self == "" {
		return nil
	}
	message := s.follow(s.Active, true).(map[string]any)
	if owner != "" {
		message["targetClientIds"] = []string{owner}
	}
	return message
}

func (s *Session) follow(target Selection, following bool) any {
	return s.broadcast("thread-stream-following-changed", 1, map[string]any{"conversationId": target.ID, "hostId": target.Host, "following": following})
}

func (s *Session) broadcast(method string, version int, params any) any {
	return map[string]any{"type": "broadcast", "method": method, "version": version, "sourceClientId": s.Self, "params": params}
}
