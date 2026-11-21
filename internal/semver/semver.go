package semver

import (
	"fmt"
	"regexp"
	"strings"
)

var versionPattern = regexp.MustCompile(`^v(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-([0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*))?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// Version is the precedence of a strict v-prefixed SemVer tag.
type Version struct {
	major, minor, patch string
	pre                 []string
}

func ParseTag(tag string) (Version, error) {
	match := versionPattern.FindStringSubmatch(tag)
	if match == nil {
		return Version{}, fmt.Errorf("invalid release tag %q: expected v followed by a semantic version", tag)
	}
	v := Version{major: match[1], minor: match[2], patch: match[3]}
	if match[4] != "" {
		v.pre = strings.Split(match[4], ".")
		for _, identifier := range v.pre {
			if numeric(identifier) && len(identifier) > 1 && identifier[0] == '0' {
				return Version{}, fmt.Errorf("invalid release tag %q: numeric prerelease identifier has a leading zero", tag)
			}
		}
	}
	return v, nil
}

func (v Version) SameCore(other Version) bool {
	return v.major == other.major && v.minor == other.minor && v.patch == other.patch
}

func (v Version) IsPrerelease() bool { return len(v.pre) != 0 }

func (v Version) Compare(other Version) int {
	for _, pair := range [][2]string{{v.major, other.major}, {v.minor, other.minor}, {v.patch, other.patch}} {
		if result := compareNumber(pair[0], pair[1]); result != 0 {
			return result
		}
	}
	if len(v.pre) == 0 && len(other.pre) != 0 {
		return 1
	}
	if len(other.pre) == 0 && len(v.pre) != 0 {
		return -1
	}
	for i := 0; i < len(v.pre) && i < len(other.pre); i++ {
		a, b := v.pre[i], other.pre[i]
		var result int
		switch {
		case numeric(a) && numeric(b):
			result = compareNumber(a, b)
		case numeric(a):
			result = -1
		case numeric(b):
			result = 1
		default:
			result = strings.Compare(a, b)
		}
		if result != 0 {
			return result
		}
	}
	return len(v.pre) - len(other.pre)
}

// Compare digit strings without imposing a machine-sized limit on SemVer numbers.
func compareNumber(a, b string) int {
	if len(a) != len(b) {
		return len(a) - len(b)
	}
	return strings.Compare(a, b)
}

func numeric(s string) bool {
	for _, char := range s {
		if char < '0' || char > '9' {
			return false
		}
	}
	return s != ""
}
