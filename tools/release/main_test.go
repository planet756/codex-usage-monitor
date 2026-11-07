package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func newRepository(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test creates a temporary Git repository")
	}
	root := t.TempDir()
	copyBuildScript(t, root)
	testGit(t, root, "init", "-b", "main")
	testGit(t, root, "config", "user.name", "Release Test")
	testGit(t, root, "config", "user.email", "release@example.invalid")
	testGit(t, root, "config", "commit.gpgsign", "false")
	testGit(t, root, "config", "tag.gpgsign", "false")
	return root
}

func copyBuildScript(t *testing.T, root string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "build.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "build.sh"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func testGit(t *testing.T, root string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-c", "core.hooksPath=" + filepath.Join(root, "no-hooks")}, args...)...)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull)
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, output)
	}
	return strings.TrimSpace(string(output))
}

func commit(t *testing.T, root, message string) string {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "change.txt"), []byte(message), 0o644); err != nil {
		t.Fatal(err)
	}
	testGit(t, root, "add", ".")
	testGit(t, root, "commit", "-m", message)
	return testGit(t, root, "rev-parse", "HEAD")
}

func TestPrepareUsesPublishedVersionAndKeepsBranches(t *testing.T) {
	root := newRepository(t)
	initial := commit(t, root, "initial release")
	testGit(t, root, "tag", "-a", "v0.1.0", "-m", "first release")
	commit(t, root, "fix: direct commit without a pull request")
	testGit(t, root, "tag", "v0.1.1")
	commit(t, root, "feat: preview feature")
	testGit(t, root, "tag", "v0.2.0-rc.1")
	target := commit(t, root, "feat: final feature")
	testGit(t, root, "tag", "-a", "v0.2.0", "-m", "release")
	future := commit(t, root, "must not appear in this release")
	testGit(t, root, "update-ref", "refs/remotes/origin/main", future)
	testGit(t, root, "checkout", "-b", "other", initial)
	commit(t, root, "unrelated branch")
	testGit(t, root, "tag", "v9.9.9")
	testGit(t, root, "checkout", "--detach", target)
	before := testGit(t, root, "status", "--porcelain")
	releases := []publishedRelease{
		{Tag: "v9.9.9"},
		{Tag: "v0.2.0", Draft: true},
		{Tag: "v0.2.0-rc.1", Prerelease: true},
		{Tag: "v0.1.1", Draft: true},
		{Tag: "v0.1.0"},
	}
	m, notes, err := prepare(root, "v0.2.0", target, "owner/repo", releases)
	if err != nil {
		t.Fatal(err)
	}
	if m.SHA != target || m.PreviousTag != "v0.1.0" || m.Prerelease || m.Version != "0.2.0" || m.TagObject == target {
		t.Fatalf("unexpected annotated tag metadata: %+v", m)
	}
	for _, text := range []string{"direct commit without a pull request", "preview feature", "final feature", "compare/v0.1.0...v0.2.0"} {
		if !strings.Contains(notes, text) {
			t.Errorf("release notes missing %q: %s", text, notes)
		}
	}
	for _, text := range []string{"initial release", "must not appear", "unrelated branch"} {
		if strings.Contains(notes, text) {
			t.Errorf("release notes contain %q", text)
		}
	}
	if testGit(t, root, "rev-parse", "main") != future || testGit(t, root, "status", "--porcelain") != before {
		t.Fatal("release preparation changed main or the checkout")
	}
}

func TestPrepareFirstReleaseAndRejectsChangedOrPublishedTags(t *testing.T) {
	root := newRepository(t)
	target := commit(t, root, "first change")
	testGit(t, root, "tag", "-a", "v1.0.0-rc.1", "-m", "preview")
	testGit(t, root, "update-ref", "refs/remotes/origin/main", target)
	object := testGit(t, root, "rev-parse", "refs/tags/v1.0.0-rc.1")
	m, notes, err := prepare(root, "v1.0.0-rc.1", object, "owner/repo", nil)
	if err != nil || !m.Prerelease || m.PreviousTag != "" || !strings.Contains(notes, "first change") {
		t.Fatalf("first release: %+v, %q, %v", m, notes, err)
	}
	if _, _, err := prepare(root, "v1.0.0-rc.1", target, "owner/repo", []publishedRelease{{Tag: "v1.0.0-rc.1"}}); err == nil {
		t.Fatal("accepted an already published tag")
	}
	newer := commit(t, root, "later change")
	testGit(t, root, "tag", "-f", "v1.0.0-rc.1")
	testGit(t, root, "update-ref", "refs/remotes/origin/main", newer)
	if _, _, err := prepare(root, "v1.0.0-rc.1", target, "owner/repo", nil); err == nil {
		t.Fatal("accepted a moved tag")
	}
	testGit(t, root, "update-ref", "refs/remotes/origin/main", target)
	if _, _, err := prepare(root, "v1.0.0-rc.1", newer, "owner/repo", nil); err != nil {
		t.Fatalf("tag must not require a matching remote branch or membership in main: %v", err)
	}
}

func TestPrereleaseBaselineIncludesEarlierPublishedPreview(t *testing.T) {
	root := newRepository(t)
	commit(t, root, "stable change")
	testGit(t, root, "tag", "v1.0.0")
	commit(t, root, "first preview")
	testGit(t, root, "tag", "v1.1.0-rc.1")
	target := commit(t, root, "second preview")
	testGit(t, root, "tag", "v1.1.0-rc.2")
	testGit(t, root, "update-ref", "refs/remotes/origin/main", target)
	m, notes, err := prepare(root, "v1.1.0-rc.2", target, "owner/repo", []publishedRelease{
		{Tag: "v1.0.0"}, {Tag: "v1.1.0-rc.1", Prerelease: true},
	})
	if err != nil || m.PreviousTag != "v1.1.0-rc.1" || strings.Contains(notes, "first preview") {
		t.Fatalf("preview boundary: %+v, %q, %v", m, notes, err)
	}
}

func TestBuildVersionValidation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the build-script contract is verified by Linux CI")
	}
	shell, err := exec.LookPath("sh")
	if err != nil {
		t.Fatal("build-script validation requires sh: ", err)
	}
	root := t.TempDir()
	copyBuildScript(t, root)
	for _, version := range []string{"dev", "0.0.0", "1.2.3", "1.2.3-rc.1", "1.2.3-1.alpha+build.007", "1.2.3+build.01"} {
		t.Run("valid/"+version, func(t *testing.T) {
			cmd := exec.Command(shell, "build.sh", "--version")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "BUILD_VERSION="+version)
			out, err := cmd.CombinedOutput()
			if err != nil || strings.TrimSpace(string(out)) != version {
				t.Fatalf("version %q: %s %v", version, out, err)
			}
		})
	}
	for _, version := range []string{"", "v1.2.3", "01.2.3", "1.2", "1.2.3-01", "1.2.3-rc..1", "1.2.3\ninvalid", "1.2.3 -X main.other=value"} {
		t.Run("invalid/"+version, func(t *testing.T) {
			cmd := exec.Command(shell, "build.sh", "--version")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "BUILD_VERSION="+version)
			if out, err := cmd.CombinedOutput(); err == nil {
				t.Fatalf("accepted %q: %s", version, out)
			}
		})
	}
}

func TestReadReleaseMetadata(t *testing.T) {
	data := `{"tag_name":"v1.0.0","draft":false,"prerelease":false,"published_at":"2026-01-01T00:00:00Z"}
{"tag_name":"v1.1.0-rc.1","draft":true,"prerelease":true,"published_at":null}`
	releases, err := readReleases(strings.NewReader(data))
	if err != nil || len(releases) != 2 || !releases[1].Draft {
		t.Fatalf("release stream: %+v %v", releases, err)
	}
	for _, data := range []string{`{`, `{}`, `{"tag_name":123}`} {
		if _, err := readReleases(strings.NewReader(data)); err == nil {
			t.Fatalf("accepted invalid release metadata %q", data)
		}
	}
}

func TestRunWritesWorkflowOutputs(t *testing.T) {
	root := newRepository(t)
	sha := commit(t, root, "feat: published snapshot")
	testGit(t, root, "tag", "-a", "v1.0.0", "-m", "release")
	testGit(t, root, "update-ref", "refs/remotes/origin/main", sha)
	for _, name := range []string{"releases.json", "outputs"} {
		if err := os.WriteFile(filepath.Join(root, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(root)
	if err := run([]string{"--tag", "v1.0.0", "--sha", sha, "--repo", "owner/repo", "--releases", "releases.json", "--github-output", "outputs"}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile("dist/release-metadata/metadata.json")
	if err != nil {
		t.Fatal(err)
	}
	var m metadata
	if err := json.Unmarshal(data, &m); err != nil || m.SHA != sha || m.Version != "1.0.0" {
		t.Fatalf("metadata: %+v %v", m, err)
	}
	outputs, err := os.ReadFile("outputs")
	if err != nil || !strings.Contains(string(outputs), "sha="+sha+"\n") || !strings.Contains(string(outputs), "prerelease=false\n") {
		t.Fatalf("workflow outputs: %q %v", outputs, err)
	}
	notes, err := os.ReadFile("dist/release-metadata/CHANGELOG.md")
	if err != nil || !strings.Contains(string(notes), "published snapshot") {
		t.Fatalf("packaged notes: %q %v", notes, err)
	}
}

func TestReleaseBranchesAndEquivalentBackportNotes(t *testing.T) {
	root := newRepository(t)
	base := commit(t, root, "initial release")
	testGit(t, root, "tag", "v0.2.0")
	testGit(t, root, "branch", "release/0.2", base)
	writeCommit := func(file, message string) string {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, file), []byte(message), 0o644); err != nil {
			t.Fatal(err)
		}
		testGit(t, root, "add", file)
		testGit(t, root, "commit", "-m", message)
		return testGit(t, root, "rev-parse", "HEAD")
	}
	writeCommit("feature.txt", "feat: Next version feature")
	fix := writeCommit("fix.txt", "fix: Critical stable fix")
	testGit(t, root, "checkout", "release/0.2")
	testGit(t, root, "cherry-pick", "-x", fix)
	patch := testGit(t, root, "rev-parse", "HEAD")
	testGit(t, root, "tag", "-a", "v0.2.1", "-m", "patch")
	testGit(t, root, "checkout", "-b", "release/0.3", "main")
	target := testGit(t, root, "rev-parse", "HEAD")
	testGit(t, root, "tag", "-a", "v0.3.0", "-m", "next version")
	m, notes, err := prepare(root, "v0.3.0", target, "owner/repo", []publishedRelease{{Tag: "v0.2.0"}, {Tag: "v0.2.1"}})
	if err != nil || m.PreviousTag != "v0.2.1" || !strings.Contains(notes, "Next version feature") || strings.Contains(notes, "Critical stable fix") {
		t.Fatalf("cross-branch release: %+v, %q, %v", m, notes, err)
	}
	testGit(t, root, "checkout", "release/0.2")
	m, notes, err = prepare(root, "v0.2.1", patch, "owner/repo", []publishedRelease{{Tag: "v0.2.0"}, {Tag: "v0.3.0"}})
	if err != nil || m.PreviousTag != "v0.2.0" || strings.Contains(notes, "Next version feature") || !strings.Contains(notes, "Critical stable fix") {
		t.Fatalf("maintenance release: %+v, %q, %v", m, notes, err)
	}
}
