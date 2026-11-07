package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

type releaseChange struct {
	SHA, Subject, Body string
}

var commitSHA = regexp.MustCompile(`^[0-9a-f]{40}$`)
var conventionalSubject = regexp.MustCompile(`^([a-z]+)(?:\(([^\r\n)]+)\))?(!)?:[ \t]+(.+)$`)
var breakingFooter = regexp.MustCompile(`(?m)^BREAKING(?: CHANGE|-CHANGE):[ \t]*\S`)

func parseChanges(output string) ([]releaseChange, error) {
	if output == "" {
		return nil, nil
	}
	fields := strings.Split(strings.TrimSuffix(output, "\x00"), "\x00")
	if len(fields)%3 != 0 {
		return nil, fmt.Errorf("invalid git log record count")
	}
	changes := make([]releaseChange, 0, len(fields)/3)
	for i := 0; i < len(fields); i += 3 {
		if !commitSHA.MatchString(fields[i]) || fields[i+1] == "" || strings.ContainsAny(fields[i+1], "\r\n") {
			return nil, fmt.Errorf("invalid git log commit record")
		}
		changes = append(changes, releaseChange{fields[i], fields[i+1], fields[i+2]})
	}
	return changes, nil
}

func changeCategory(change releaseChange) (string, string) {
	subject := strings.TrimPrefix(change.Subject, "[cherry-pick] ")
	match := conventionalSubject.FindStringSubmatch(subject)
	category := "Other Changes"
	if match != nil {
		subject = match[4]
		if match[2] != "" {
			subject = match[2] + ": " + subject
		}
		switch match[1] {
		case "feat":
			category = "Features"
		case "fix":
			category = "Fixes"
		case "perf":
			category = "Performance"
		case "refactor":
			category = "Refactoring"
		case "docs":
			category = "Documentation"
		}
	}
	if (match != nil && match[3] == "!") || breakingFooter.MatchString(change.Body) {
		category = "Breaking Changes"
	}
	return category, subject
}

func renderNotes(m metadata, repo string, changes []releaseChange) string {
	var notes strings.Builder
	fmt.Fprintf(&notes, "## %s\n", m.Tag)
	groups := make(map[string][]string)
	escape := strings.NewReplacer("\\", "\\\\", "`", "\\`", "*", "\\*", "_", "\\_", "[", "\\[", "]", "\\]", "<", "&lt;", ">", "&gt;")
	for _, change := range changes {
		category, subject := changeCategory(change)
		groups[category] = append(groups[category], fmt.Sprintf("- %s ([%s](https://github.com/%s/commit/%s))\n", escape.Replace(subject), change.SHA[:7], repo, change.SHA))
	}
	for _, category := range []string{"Breaking Changes", "Features", "Fixes", "Performance", "Refactoring", "Documentation", "Other Changes"} {
		if len(groups[category]) != 0 {
			fmt.Fprintf(&notes, "\n### %s\n\n%s", category, strings.Join(groups[category], ""))
		}
	}
	if len(changes) == 0 {
		notes.WriteString("\nNo new changes since the previous release.\n")
	}
	if m.PreviousTag == "" {
		fmt.Fprintf(&notes, "\n**Source:** https://github.com/%s/tree/%s\n", repo, url.PathEscape(m.Tag))
	} else {
		fmt.Fprintf(&notes, "\n**Full changelog:** https://github.com/%s/compare/%s...%s\n", repo, url.PathEscape(m.PreviousTag), url.PathEscape(m.Tag))
	}
	return notes.String()
}
