// Package source translates Desktop's explicit file identity into a Windows
// path. It performs no host discovery, filesystem search, or process launch.
package source

import (
	"errors"
	"net/url"
	"path"
	"strings"
)

var (
	ErrUnsupported     = errors.New("unsupported Windows source path")
	ErrHostUnsupported = errors.New("current chat source is not on this Windows host")
)

type Options struct{ Home, Host string }
type Plan struct {
	Path, Home, Kind string
	Mapped           bool
}

func cleanPOSIX(value string) bool {
	for rest := value; rest != ""; {
		part, next, _ := strings.Cut(rest, "/")
		if part == "." || part == ".." {
			return false
		}
		rest = next
	}
	return true
}

// driveAlias converts the explicit /mnt/<drive>/ representation of the same
// Windows file. It never infers a different mount root or searches for a file.
func driveAlias(raw string) (string, bool) {
	if !strings.HasPrefix(raw, "/mnt/") || !cleanPOSIX(raw) || strings.ContainsAny(raw, "\\:\x00") {
		return "", false
	}
	tail := strings.TrimPrefix(raw, "/mnt/")
	if len(tail) < 3 || tail[0] < 'a' || tail[0] > 'z' || tail[1] != '/' {
		return "", false
	}
	return windowsPath(strings.ToUpper(tail[:1]) + ":\\" + strings.ReplaceAll(tail[2:], "/", "\\"))
}

func windowsPath(raw string) (string, bool) {
	if raw == "" || strings.ContainsRune(raw, 0) {
		return "", false
	}
	if strings.HasPrefix(raw, verbatimPrefix) {
		_, ok := verbatimVolume(raw)
		return raw, ok
	}
	s := strings.ReplaceAll(raw, `\`, "/")
	// A verbatim namespace requires native separators. Converting a malformed
	// namespace into a different existing file would lose explicit identity.
	if strings.HasPrefix(s, "//?/") {
		return "", false
	}
	if len(s) >= 3 && driveLetter(s[0]) && s[1] == ':' && s[2] == '/' {
		p := strings.ToUpper(s[:1]) + ":" + strings.ReplaceAll(path.Clean(s[2:]), "/", `\`)
		return p, true
	}
	if strings.HasPrefix(s, "//") {
		parts := strings.Split(strings.TrimPrefix(s, "//"), "/")
		if len(parts) < 2 || !uncComponent(parts[0]) || !uncComponent(parts[1]) {
			return "", false
		}
		p := `\\` + parts[0] + `\` + parts[1]
		if len(parts) > 2 {
			tail := strings.TrimPrefix(path.Clean("/"+strings.Join(parts[2:], "/")), "/")
			if tail != "" {
				p += `\` + strings.ReplaceAll(tail, "/", `\`)
			}
		}
		return p, true
	}
	return "", false
}

func root(raw string) (string, string, bool, error) {
	if len(raw) > 32<<10 {
		return "", "", false, ErrUnsupported
	}
	if p, ok := windowsPath(raw); ok {
		kind := "windows_drive"
		if len(p) >= 11 && strings.EqualFold(p[:11], `\\?\Volume{`) {
			kind = "windows_volume"
		} else if strings.HasPrefix(p, `\\`) && !strings.HasPrefix(p, verbatimPrefix) || strings.HasPrefix(strings.ToUpper(p), `\\?\UNC\`) {
			kind = "windows_unc"
		}
		return p, kind, false, nil
	}
	if strings.HasPrefix(strings.ToLower(raw), "file:") {
		u, err := url.Parse(raw)
		if err != nil || u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
			return "", "", false, ErrUnsupported
		}
		p := u.Path
		if u.Host != "" && !strings.EqualFold(u.Host, "localhost") {
			p = "//" + u.Host + "/" + strings.TrimPrefix(p, "/")
		} else if len(p) >= 4 && p[0] == '/' && p[2] == ':' {
			p = p[1:]
		}
		if p, ok := windowsPath(p); ok {
			return p, "file_uri", true, nil
		}
		if p, ok := driveAlias(p); ok {
			return p, "file_uri", true, nil
		}
		return "", "", false, ErrUnsupported
	}
	if p, ok := driveAlias(raw); ok {
		return p, "drive_alias", true, nil
	}
	return "", "", false, ErrUnsupported
}

// Directory normalizes an explicit Windows data folder without touching disk.
func Directory(raw string) (string, error) { p, _, _, err := root(raw); return p, err }

func homeFromPath(p string) string {
	parts := strings.Split(p, `\`)
	at := -1
	for i, part := range parts {
		if strings.EqualFold(part, "sessions") || strings.EqualFold(part, "archived_sessions") {
			at = i
		}
	}
	if at < 1 {
		return ""
	}
	h := strings.Join(parts[:at], `\`)
	if strings.HasSuffix(h, ":") {
		h += `\`
	} else if volume, ok := verbatimVolume(p); ok && (len(h) == volume || strings.HasSuffix(h, `\`)) {
		h += `\`
	}
	if p, ok := windowsPath(h); ok {
		return p
	}
	return ""
}

func Resolve(raw string, o Options) (Plan, error) {
	// A remote host's C: path cannot establish local file identity. Host routing
	// remains opaque; this code never discovers or decodes a backend environment.
	if o.Host != "" && o.Host != "local" {
		return Plan{}, ErrHostUnsupported
	}
	p, kind, mapped, err := root(raw)
	rel := strings.ReplaceAll(raw, `\`, "/")
	if err != nil && len(raw) <= 32<<10 && !strings.HasPrefix(rel, "/") && !strings.ContainsAny(rel, ":\x00") && cleanPOSIX(rel) && (strings.HasPrefix(rel, "sessions/") || strings.HasPrefix(rel, "archived_sessions/")) {
		h, _, _, e := root(o.Home)
		if e != nil {
			return Plan{}, e
		}
		var ok bool
		if !strings.HasSuffix(h, `\`) {
			h += `\`
		}
		p, ok = windowsPath(h + strings.ReplaceAll(rel, "/", `\`))
		if !ok || len(p) > 32<<10 {
			return Plan{}, ErrUnsupported
		}
		kind, mapped, err = "relative", true, nil
	}
	if err != nil {
		return Plan{}, err
	}
	h := homeFromPath(p)
	if h == "" {
		h, _, _, err = root(o.Home)
		if err != nil {
			return Plan{}, err
		}
	}
	return Plan{Path: p, Home: h, Kind: kind, Mapped: mapped}, nil
}
