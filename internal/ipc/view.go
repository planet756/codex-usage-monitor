package ipc

import (
	"net/url"
	"strings"
)

type PageKind uint8

const (
	PageUnknown PageKind = iota
	PageChat
	PageOther
)

// DocumentPage classifies only an explicit application route. The bootstrap
// document URL and arbitrary accessible text do not establish a page kind.
func DocumentPage(value string) PageKind {
	if id, _ := ViewRoute(value); id != "" {
		return PageChat
	}
	value = strings.TrimSpace(value)
	u, err := url.Parse(value)
	if err != nil {
		return PageUnknown
	}
	path := ""
	switch {
	case strings.HasPrefix(u.Fragment, "/"):
		fragment, err := url.Parse(u.Fragment)
		if err != nil {
			return PageUnknown
		}
		path = fragment.Path
	case u.Scheme == "codex" && u.Host != "":
		path = "/" + u.Host + u.Path
	case u.Scheme == "" && u.Host == "" && strings.HasPrefix(u.Path, "/"):
		path = u.Path
	}
	if path == "/" {
		return PageChat // The empty new-chat composer has no thread ID yet.
	}
	if path != "" && !strings.HasSuffix(path, ".html") {
		return PageOther
	}
	return PageUnknown
}

// ViewURL accepts only explicit chat routes, never UUIDs found in arbitrary
// document text, titles, query parameters or recently modified files.
func ViewRoute(value string) (string, string) {
	u, err := url.Parse(strings.TrimSpace(value))
	if err != nil {
		return "", ""
	}
	path := u.Path
	host := u.Query().Get("hostId")
	if u.Scheme == "codex" && u.Host == "threads" {
		path = "/threads" + path
	}
	if strings.HasPrefix(u.Fragment, "/") {
		path = u.Fragment
		if p, err := url.Parse(path); err == nil {
			path = p.Path
			if h := p.Query().Get("hostId"); h != "" {
				if host != "" && h != host {
					return "", ""
				}
				host = h
			}
		}
	}
	parts := strings.Split(strings.Trim(path, "/"), "/")
	if len(parts) != 2 || parts[0] != "threads" && parts[0] != "local" || !ValidThreadID(parts[1]) && !(strings.HasPrefix(parts[1], "client-new-thread:") && ValidThreadID(strings.TrimPrefix(parts[1], "client-new-thread:"))) {
		return "", ""
	}
	return strings.ToLower(parts[1]), host
}

func ViewURL(value string) (string, string) {
	id, host := ViewRoute(value)
	if !ValidThreadID(id) {
		return "", ""
	}
	return id, host
}
