package main

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

func TestVersionOrderingAndBaselines(t *testing.T) {
	ordered := []string{"v1.0.0-alpha", "v1.0.0-alpha.1", "v1.0.0-alpha.beta", "v1.0.0-beta", "v1.0.0-beta.2", "v1.0.0-beta.11", "v1.0.0-rc.1", "v1.0.0", "v1.0.2", "v1.0.10", "v1.1.0", "v2.0.0", "v999999999999999999999999999999.0.0"}
	for i, a := range ordered {
		v, err := parseVersionTag(a)
		if err != nil {
			t.Fatal(err)
		}
		for j, b := range ordered {
			other, err := parseVersionTag(b)
			if err != nil {
				t.Fatal(err)
			}
			result := v.compare(other)
			if (i < j && result >= 0) || (i == j && result != 0) || (i > j && result <= 0) {
				t.Fatalf("incorrect SemVer order for %s and %s: %d", a, b, result)
			}
		}
	}
	a, _ := parseVersionTag("v1.0.0+build.001")
	b, _ := parseVersionTag("v1.0.0+other")
	if a.compare(b) != 0 {
		t.Fatal("build metadata changed version precedence")
	}
	for _, tag := range []string{"", "dev", "1.0.0", "v01.0.0", "v1.0.0-01", "v1.0.0-rc..1", "v1.0.0+", "v1.0.0\nignored"} {
		if _, err := parseVersionTag(tag); err == nil {
			t.Fatalf("accepted malformed tag %q", tag)
		}
	}

	for _, tc := range []struct {
		name, tag, want string
		releases        []publishedRelease
	}{
		{"first release", "v0.2.0", "", nil},
		{"patch stays on its line", "v0.2.2", "v0.2.1", []publishedRelease{{Tag: "v0.1.99"}, {Tag: "v0.2.0"}, {Tag: "v0.2.1"}, {Tag: "v0.3.0"}}},
		{"new line crosses branch ancestry", "v0.3.0", "v0.2.10", []publishedRelease{{Tag: "v0.2.9"}, {Tag: "v0.2.10"}, {Tag: "v0.3.0-rc.1", Prerelease: true}, {Tag: "v0.3.0", Draft: true}}},
		{"preview compares same version preview", "v0.3.0-rc.2", "v0.3.0-rc.1", []publishedRelease{{Tag: "v0.2.10"}, {Tag: "v0.3.0-rc.1", Prerelease: true}}},
		{"new preview ignores older previews", "v0.4.0-rc.1", "v0.3.0", []publishedRelease{{Tag: "v0.3.0"}, {Tag: "v0.3.1-rc.1", Prerelease: true}}},
		{"stable ignores even mislabeled preview tags", "v1.0.0", "v0.9.0", []publishedRelease{{Tag: "v0.9.0"}, {Tag: "v1.0.0-rc.1"}, {Tag: "unrelated-tag"}}},
		{"draft is not a baseline", "v0.2.2", "v0.2.0", []publishedRelease{{Tag: "v0.2.0"}, {Tag: "v0.2.1", Draft: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, err := parseVersionTag(tc.tag)
			if err != nil {
				t.Fatal(err)
			}
			got, err := previousRelease(tc.tag, v, tc.releases)
			if err != nil || got != tc.want {
				t.Fatalf("baseline = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	v, _ := parseVersionTag("v1.0.0")
	if _, err := previousRelease("v1.0.0", v, []publishedRelease{{Tag: "v1.0.0"}}); err == nil {
		t.Fatal("accepted an already published tag")
	}
}

func TestNotesParseAndClassifyActualRecords(t *testing.T) {
	sha := strings.Repeat("a", 40)
	var raw strings.Builder
	for _, fields := range [][2]string{
		{"feat(ui): Add a dark theme", ""},
		{"fix: Handle chat switches", "description\n\nBREAKING CHANGE: remove old setting\n"},
		{"perf(reader): Reduce allocations", ""},
		{"refactor: Share engine", ""},
		{"docs: Explain releases", ""},
		{"[cherry-pick] fix: Backported fix", ""},
		{"Fix [link] <markup>", ""},
		{"feat!: New config format", ""},
	} {
		raw.WriteString(sha + "\x00" + fields[0] + "\x00" + fields[1] + "\x00")
	}
	changes, err := parseChanges(raw.String())
	if err != nil {
		t.Fatal(err)
	}
	notes := renderNotes(metadata{Tag: "v0.3.0", PreviousTag: "v0.2.1"}, "owner/repo", changes)
	for _, want := range []string{"### Breaking Changes", "### Features", "### Fixes", "### Performance", "### Refactoring", "### Documentation", "### Other Changes", "ui: Add a dark theme", "reader: Reduce allocations", "Backported fix", `Fix \[link\] &lt;markup&gt;`, "compare/v0.2.1...v0.3.0", "/commit/" + sha} {
		if !strings.Contains(notes, want) {
			t.Errorf("notes missing %q:\n%s", want, notes)
		}
	}
	if strings.Contains(notes, "description") || strings.Contains(notes, "[cherry-pick]") || strings.Count(notes, "New config format") != 1 {
		t.Fatalf("unexpected duplicate or body content:\n%s", notes)
	}
	for _, raw := range []string{"bad", sha + "\x00\x00body\x00", "bad-sha\x00fix: broken\x00\x00"} {
		if _, err := parseChanges(raw); err == nil {
			t.Fatalf("accepted malformed log records %q", raw)
		}
	}
	empty := renderNotes(metadata{Tag: "v0.2.0"}, "owner/repo", nil)
	if !strings.Contains(empty, "No new changes") || !strings.Contains(empty, "/tree/v0.2.0") {
		t.Fatal(empty)
	}
}

type memoryHistory struct {
	refs              map[string]string
	logErr            error
	log               []releaseChange
	previous, current string
}

func (h *memoryHistory) resolve(ref string) (string, error) {
	sha, ok := h.refs[ref]
	if !ok {
		return "", errors.New("reference unavailable")
	}
	return sha, nil
}

func (h *memoryHistory) changes(previous, current string) ([]releaseChange, error) {
	h.previous, h.current = previous, current
	return h.log, h.logErr
}

func TestPrepareTagSnapshotWithHistoryBoundary(t *testing.T) {
	sha, previous, object := strings.Repeat("1", 40), strings.Repeat("2", 40), strings.Repeat("3", 40)
	fixture := func() *memoryHistory {
		return &memoryHistory{refs: map[string]string{
			"refs/tags/v0.3.0^{commit}": sha, "refs/tags/v0.3.0": object, sha + "^{commit}": sha,
			"HEAD":                      sha,
			"refs/tags/v0.2.1^{commit}": previous,
		}, log: []releaseChange{{SHA: sha, Subject: "feat: New version"}}}
	}
	h := fixture()
	m, notes, err := prepareHistory(h, "v0.3.0", sha, "owner/repo", []publishedRelease{{Tag: "v0.2.1"}})
	if err != nil || m.PreviousTag != "v0.2.1" || m.TagObject != object || h.previous != previous || h.current != sha || !strings.Contains(notes, "### Features") {
		t.Fatalf("release preparation: %+v, %q, %v", m, notes, err)
	}
	// No branch refs are available: the tag identifies the complete source
	// snapshot without a main or release-branch membership requirement.
	for _, failure := range []string{"missing-tag", "missing-event", "missing-tag-object", "moved-tag", "wrong-checkout", "missing-baseline", "log-error"} {
		t.Run(failure, func(t *testing.T) {
			h := fixture()
			switch failure {
			case "missing-tag":
				delete(h.refs, "refs/tags/v0.3.0^{commit}")
			case "missing-event":
				delete(h.refs, sha+"^{commit}")
			case "missing-tag-object":
				delete(h.refs, "refs/tags/v0.3.0")
			case "moved-tag":
				h.refs["refs/tags/v0.3.0^{commit}"] = previous
			case "wrong-checkout":
				h.refs["HEAD"] = previous
			case "missing-baseline":
				delete(h.refs, "refs/tags/v0.2.1^{commit}")
			case "log-error":
				h.logErr = errors.New("cannot compare release histories")
			}
			if _, _, err := prepareHistory(h, "v0.3.0", sha, "owner/repo", []publishedRelease{{Tag: "v0.2.1"}}); err == nil {
				t.Fatal("accepted invalid release state")
			}
		})
	}
	h = fixture()
	if _, _, err := prepareHistory(h, "v0.3.0", sha, "owner/repo", []publishedRelease{{Tag: "v0.3.0"}}); err == nil {
		t.Fatal("accepted published release")
	}
}

func TestCrossBranchLogUsesPatchEquivalence(t *testing.T) {
	args := changeLogArgs("old", "new")
	for _, option := range []string{"--cherry-pick", "--right-only", "--no-merges", "old...new"} {
		found := false
		for _, arg := range args {
			found = found || arg == option
		}
		if !found {
			t.Fatalf("missing patch-equivalence option %q: %v", option, args)
		}
	}
	want := []string{"log", "--no-merges", "--reverse", "-z", "--format=%H%x00%s%x00%b", "new", "--"}
	if got := changeLogArgs("", "new"); !reflect.DeepEqual(got, want) {
		t.Fatalf("first-release history: %v", got)
	}
}
