package source

import (
	"net/url"
	"path"
	"strings"
)

// DirectoryPOSIX accepts an explicit local path without discovering a host or
// changing case. Dot components are rejected before filesystem/symlink lookup.
func DirectoryPOSIX(raw string) (string, error) {
	if len(raw) > 32<<10 || strings.ContainsAny(raw, "\\\x00") {
		return "", ErrUnsupported
	}
	if strings.HasPrefix(strings.ToLower(raw), "file:") {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" || u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
			return "", ErrUnsupported
		}
		raw = u.Path
	}
	if !strings.HasPrefix(raw, "/") || strings.HasPrefix(raw, "//") || !cleanPOSIX(raw) || strings.ContainsAny(raw, "\\\x00") {
		return "", ErrUnsupported
	}
	return path.Clean(raw), nil
}

// ResolvePOSIX is used by macOS. Windows path and drive-alias handling remains
// unchanged in Resolve; a remote host can never lend a path local ownership.
func ResolvePOSIX(raw string, o Options) (Plan, error) {
	if o.Host != "" && o.Host != "local" {
		return Plan{}, ErrHostUnsupported
	}
	kind, mapped := "posix", false
	if strings.HasPrefix(strings.ToLower(raw), "file:") {
		kind, mapped = "file_uri", true
	}
	if len(raw) <= 32<<10 && cleanPOSIX(raw) && (strings.HasPrefix(raw, "sessions/") || strings.HasPrefix(raw, "archived_sessions/")) && !strings.ContainsAny(raw, "\\:\x00") {
		home, err := DirectoryPOSIX(o.Home)
		if err != nil {
			return Plan{}, err
		}
		raw = path.Join(home, raw)
		kind, mapped = "relative", true
	}
	p, err := DirectoryPOSIX(raw)
	if err != nil {
		return Plan{}, err
	}
	home := ""
	parts := strings.Split(p, "/")
	for i, part := range parts {
		if part == "sessions" || part == "archived_sessions" {
			home = strings.Join(parts[:i], "/")
			if home == "" {
				home = "/"
			}
		}
	}
	if home == "" {
		home, err = DirectoryPOSIX(o.Home)
		if err != nil {
			return Plan{}, err
		}
	}
	return Plan{Path: p, Home: home, Kind: kind, Mapped: mapped}, nil
}
