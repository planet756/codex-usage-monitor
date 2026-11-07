package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

type releaseHistory interface {
	resolve(ref string) (string, error)
	changes(previous, current string) ([]releaseChange, error)
}

type gitHistory struct{ root string }

func (h gitHistory) resolve(ref string) (string, error) {
	return git(h.root, "rev-parse", "--verify", ref)
}

func changeLogArgs(previous, current string) []string {
	args := []string{"log", "--no-merges", "--reverse", "-z", "--format=%H%x00%s%x00%b"}
	if previous == "" {
		return append(args, current, "--")
	}
	// Symmetric difference with patch equivalence also handles backports whose
	// commit IDs differ between main and the previous release branch.
	return append(args, "--cherry-pick", "--right-only", previous+"..."+current, "--")
}

func (h gitHistory) changes(previous, current string) ([]releaseChange, error) {
	if previous != "" {
		if _, err := git(h.root, "merge-base", previous, current); err != nil {
			return nil, fmt.Errorf("release and baseline must share history: %w", err)
		}
	}
	output, err := git(h.root, changeLogArgs(previous, current)...)
	if err != nil {
		return nil, err
	}
	return parseChanges(output)
}

func git(root string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(output)), nil
}
