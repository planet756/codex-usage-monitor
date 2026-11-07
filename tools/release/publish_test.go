package main

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPublishWindowsDraftAndRejectsChangedOrIncompleteRelease(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the publish job runs on Linux")
	}
	for _, tool := range []string{"bash", "jq", "zip", "unzip", "sha256sum"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skipf("publish fixture requires %s", tool)
		}
	}
	script, err := os.ReadFile("publish.sh")
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"success", "draft", "prerelease", "published", "moved", "missing-binary", "wrong-notes", "obsolete-draft-asset", "upload-failed"} {
		t.Run(mode, func(t *testing.T) {
			root := t.TempDir()
			write := func(name string, data []byte, permission os.FileMode) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, data, permission); err != nil {
					t.Fatal(err)
				}
			}
			m := metadata{Tag: "v1.0.0", Version: "1.0.0", SHA: strings.Repeat("1", 40), TagObject: strings.Repeat("2", 40)}
			if mode == "prerelease" {
				m.Tag, m.Version, m.Prerelease = "v1.0.0-rc.1", "1.0.0-rc.1", true
			}
			data, err := json.Marshal(m)
			if err != nil {
				t.Fatal(err)
			}
			notes := fmt.Appendf(nil, "## %s\n\n- Direct commit\n", m.Tag)
			binary := []byte("Windows artifact fixture; never executed")
			write("tools/release/publish.sh", script, 0o644)
			write("dist/release-metadata/metadata.json", data, 0o644)
			write("dist/release-metadata/CHANGELOG.md", notes, 0o644)
			write("dist/windows/CHANGELOG.md", notes, 0o644)
			write("dist/windows/SHA256SUMS", fmt.Appendf(nil, "%x  CodexUsageMonitor.exe\n", sha256.Sum256(binary)), 0o644)
			if mode != "missing-binary" {
				write("dist/windows/CodexUsageMonitor.exe", binary, 0o644)
			}
			if mode == "draft" || mode == "obsolete-draft-asset" {
				write("draft", nil, 0o644)
			}
			if mode == "wrong-notes" {
				write("dist/windows/CHANGELOG.md", []byte("Stale changelog"), 0o644)
			}
			write("bin/gh", []byte(`#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" >> "$MOCK_ROOT/calls"
case "$1 $2" in
  "api repos/owner/repo/git/ref/tags/$RELEASE_TAG")
    if [ "$MOCK_MODE" = moved ] && [ -f "$MOCK_ROOT/uploaded" ]; then
      echo changed
    else
      echo "$RELEASE_TAG_OBJECT"
    fi ;;
  "api --paginate")
    test "$3" = 'repos/owner/repo/releases?per_page=100'
    if [ "$MOCK_MODE" = published ]; then printf '{"tag_name":"%s","draft":false}\n' "$RELEASE_TAG"; fi
    if [[ "$MOCK_MODE" = draft || "$MOCK_MODE" = obsolete-draft-asset ]]; then
      printf '{"tag_name":"%s","draft":true}\n' "$RELEASE_TAG"
    fi ;;
  "release create")
    [[ " $* " = *" --verify-tag "* ]]
    [[ " $* " = *" --draft "* ]]
    touch "$MOCK_ROOT/draft" ;;
  "release upload")
    test -f "$MOCK_ROOT/draft"
    if [ "$MOCK_MODE" = upload-failed ]; then exit 2; fi
    shift 3
    for asset in "$@"; do
      if [[ "$asset" != --clobber ]]; then
        test -s "$asset"
        basename "$asset" >> "$MOCK_ROOT/upload-names"
      fi
    done
    touch "$MOCK_ROOT/uploaded" ;;
  "release view")
    if [[ " $* " = *" --json isDraft,databaseId,assets "* ]]; then
      if [ "$MOCK_MODE" = obsolete-draft-asset ]; then echo obsolete.zip >> "$MOCK_ROOT/upload-names"; fi
      jq -Rn '{draft:true,id:123,assets:[inputs]}' < "$MOCK_ROOT/upload-names"
    else
      echo true
    fi ;;
  "api --method")
    test "$3" = PATCH
    test "$4" = repos/owner/repo/releases/123
    [[ " $* " = *" draft=false "* ]]
    if [ "$RELEASE_PRERELEASE" = true ]; then
      [[ " $* " = *" make_latest=false "* ]]
    else
      [[ " $* " = *" make_latest=legacy "* ]]
    fi
    test -f "$MOCK_ROOT/uploaded"
    touch "$MOCK_ROOT/published" ;;
  "release edit")
    test -f "$MOCK_ROOT/draft"
    [[ " $* " != *" --draft "* ]] ;;
  *) echo 'Unexpected GitHub request' >&2; exit 1 ;;
esac
`), 0o755)
			cmd := exec.Command("bash", "tools/release/publish.sh")
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"MOCK_ROOT="+root, "MOCK_MODE="+mode, "GH_REPO=owner/repo", "RELEASE_TAG="+m.Tag,
				"RELEASE_VERSION="+m.Version, "RELEASE_SHA="+m.SHA, "RELEASE_TAG_OBJECT="+m.TagObject,
				"RELEASE_PRERELEASE="+fmt.Sprint(m.Prerelease))
			out, err := cmd.CombinedOutput()
			if strings.Contains(string(out), "Unexpected GitHub request") {
				t.Fatalf("invalid publish request: %s", out)
			}
			_, publishedErr := os.Stat(filepath.Join(root, "published"))
			if mode == "success" || mode == "draft" || mode == "prerelease" {
				if err != nil || publishedErr != nil {
					t.Fatalf("publish: %v, marker: %v\n%s", err, publishedErr, out)
				}
				entries, err := os.ReadDir(filepath.Join(root, "dist/releases"))
				if err != nil || len(entries) != 3 {
					t.Fatalf("expected two Windows artifacts and checksums: %v %v", entries, err)
				}
				verify := exec.Command("sha256sum", "--check", "SHA256SUMS")
				verify.Dir = filepath.Join(root, "dist/releases")
				if out, err := verify.CombinedOutput(); err != nil {
					t.Fatalf("release checksums: %s %v", out, err)
				}
				archive := filepath.Join(root, "dist/releases/CodexUsageMonitor-"+m.Tag+"-windows-amd64-portable.zip")
				if out, err := exec.Command("unzip", "-tq", archive).CombinedOutput(); err != nil {
					t.Fatalf("portable archive: %s %v", out, err)
				}
				packagedNotes, err := exec.Command("unzip", "-p", archive, "CHANGELOG.md").Output()
				if err != nil || string(packagedNotes) != string(notes) {
					t.Fatalf("packaged release notes: %q %v", packagedNotes, err)
				}
			} else if err == nil || !os.IsNotExist(publishedErr) {
				t.Fatalf("invalid release was not blocked: %v, marker: %v\n%s", err, publishedErr, out)
			}
			if mode == "upload-failed" {
				if _, err := os.Stat(filepath.Join(root, "draft")); err != nil {
					t.Fatal("failed upload did not retain the draft", err)
				}
			}
			if mode == "obsolete-draft-asset" && !strings.Contains(string(out), "exactly the Windows EXE") {
				t.Fatalf("unexpected draft assets did not block publication: %s", out)
			}
		})
	}
}
