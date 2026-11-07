// Command release prepares immutable source metadata and commit-based release notes.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

type publishedRelease struct {
	Tag        string `json:"tag_name"`
	Draft      bool   `json:"draft"`
	Prerelease bool   `json:"prerelease"`
}

type metadata struct {
	Tag         string `json:"tag"`
	Version     string `json:"version"`
	SHA         string `json:"sha"`
	TagObject   string `json:"tag_object"`
	PreviousTag string `json:"previous_tag"`
	Prerelease  bool   `json:"prerelease"`
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("release", flag.ContinueOnError)
	tag := fs.String("tag", "", "existing version tag")
	sha := fs.String("sha", "", "source SHA from the tag push event")
	repo := fs.String("repo", "", "GitHub owner/repository")
	releasesPath := fs.String("releases", "", "JSON stream of GitHub releases")
	out := fs.String("out", "dist/release-metadata", "metadata output directory")
	githubOutput := fs.String("github-output", "", "GitHub Actions output file")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 || *releasesPath == "" {
		return errors.New("provide --tag, --sha, --repo and --releases")
	}
	releasesFile, err := os.Open(*releasesPath)
	if err != nil {
		return err
	}
	releases, err := readReleases(releasesFile)
	closeErr := releasesFile.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	m, notes, err := prepare(root, *tag, *sha, *repo, releases)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(*out, 0o755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "metadata.json"), append(data, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(*out, "CHANGELOG.md"), []byte(notes), 0o644); err != nil {
		return err
	}
	if *githubOutput != "" {
		f, err := os.OpenFile(*githubOutput, os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		_, writeErr := fmt.Fprintf(f, "tag=%s\nversion=%s\nsha=%s\ntag_object=%s\nprerelease=%t\n", m.Tag, m.Version, m.SHA, m.TagObject, m.Prerelease)
		closeErr := f.Close()
		return errors.Join(writeErr, closeErr)
	}
	return nil
}

// gh api --paginate --jq '.[] | {...}' emits consecutive JSON objects.
func readReleases(r io.Reader) ([]publishedRelease, error) {
	var releases []publishedRelease
	decoder := json.NewDecoder(r)
	for {
		var release publishedRelease
		if err := decoder.Decode(&release); err != nil {
			if errors.Is(err, io.EOF) {
				return releases, nil
			}
			return nil, fmt.Errorf("decode published releases: %w", err)
		}
		if release.Tag == "" {
			return nil, errors.New("release metadata has no tag_name")
		}
		releases = append(releases, release)
	}
}

func prepare(root, tag, eventSHA, repo string, releases []publishedRelease) (metadata, string, error) {
	return prepareHistory(gitHistory{root}, tag, eventSHA, repo, releases)
}

func prepareHistory(history releaseHistory, tag, eventSHA, repo string, releases []publishedRelease) (metadata, string, error) {
	var m metadata
	version, err := parseVersionTag(tag)
	if err != nil {
		return m, "", err
	}
	if !commitSHA.MatchString(eventSHA) ||
		!regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`).MatchString(repo) {
		return m, "", errors.New("invalid event SHA or GitHub repository")
	}
	sha, err := history.resolve("refs/tags/" + tag + "^{commit}")
	if err != nil {
		return m, "", err
	}
	expected, err := history.resolve(eventSHA + "^{commit}")
	if err != nil {
		return m, "", err
	}
	if sha != expected {
		return m, "", errors.New("tag no longer points to the triggering commit")
	}
	head, err := history.resolve("HEAD")
	if err != nil {
		return m, "", err
	}
	if head != sha {
		return m, "", errors.New("checkout does not match the release commit")
	}
	object, err := history.resolve("refs/tags/" + tag)
	if err != nil {
		return m, "", err
	}
	m = metadata{Tag: tag, Version: strings.TrimPrefix(tag, "v"), SHA: sha, TagObject: object, Prerelease: len(version.pre) != 0}
	m.PreviousTag, err = previousRelease(tag, version, releases)
	if err != nil {
		return m, "", err
	}
	previousSHA := ""
	if m.PreviousTag != "" {
		previousSHA, err = history.resolve("refs/tags/" + m.PreviousTag + "^{commit}")
		if err != nil {
			return m, "", fmt.Errorf("published baseline %s is unavailable: %w", m.PreviousTag, err)
		}
	}
	changes, err := history.changes(previousSHA, sha)
	if err != nil {
		return m, "", err
	}
	return m, renderNotes(m, repo, changes), nil
}
