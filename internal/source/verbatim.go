package source

import (
	"path/filepath"
	"strings"
)

const verbatimPrefix = `\\?\`

// verbatimVolume returns the end of the explicit volume name (before its root
// separator). Only file namespaces are accepted; device objects are not logs.
func verbatimVolume(p string) (int, bool) {
	if !strings.HasPrefix(p, verbatimPrefix) || strings.ContainsAny(p, "/\x00") {
		return 0, false
	}
	s := p[len(verbatimPrefix):]
	if len(s) >= 3 && driveLetter(s[0]) && s[1:3] == `:\` {
		return len(verbatimPrefix) + 2, true
	}
	if len(s) >= 4 && strings.EqualFold(s[:4], `UNC\`) {
		server, rest, ok := strings.Cut(s[4:], `\`)
		if !ok || !uncComponent(server) {
			return 0, false
		}
		share, _, _ := strings.Cut(rest, `\`)
		if !uncComponent(share) {
			return 0, false
		}
		return len(verbatimPrefix) + 4 + len(server) + 1 + len(share), true
	}
	// Volume GUIDs have no UUID version restriction and need no drive letter.
	if len(s) >= 45 && strings.EqualFold(s[:7], "Volume{") && s[43:45] == `}\` && volumeGUID(s[7:43]) {
		return len(verbatimPrefix) + 44, true
	}
	return 0, false
}

func driveLetter(b byte) bool { return b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z' }

func uncComponent(s string) bool {
	return s != "" && s != "." && s != ".." && !strings.ContainsAny(s, ":\x00?*")
}

func volumeGUID(s string) bool {
	if len(s) != 36 {
		return false
	}
	for i := range s {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if s[i] != '-' {
				return false
			}
		} else if b := s[i]; !(b >= '0' && b <= '9' || b >= 'a' && b <= 'f' || b >= 'A' && b <= 'F') {
			return false
		}
	}
	return true
}

// JoinPath appends caller-owned relative names. Windows verbatim paths must
// bypass filepath.Join/Clean: dot components, repeated separators and trailing
// spaces/dots belong to their original file identity. Other paths use the host
// platform's normal rules, including portable rollout fixtures on Linux.
func JoinPath(base string, names ...string) string {
	if _, ok := verbatimVolume(base); !ok {
		return filepath.Join(append([]string{base}, names...)...)
	}
	for _, name := range names {
		if name == "" {
			continue
		}
		if !strings.HasSuffix(base, `\`) {
			base += `\`
		}
		base += name
	}
	return base
}

// DirPath removes only the final component of a verbatim file address and
// retains the root separator, especially for a volume GUID root directory.
func DirPath(p string) string {
	if volume, ok := verbatimVolume(p); ok {
		at := strings.LastIndexByte(p, '\\')
		if at <= volume {
			if len(p) == volume {
				return p + `\`
			}
			return p[:volume+1]
		}
		return p[:at]
	}
	return filepath.Dir(p)
}
