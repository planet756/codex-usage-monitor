package main

import (
	"fmt"

	"codex-usage-monitor/internal/semver"
)

func previousRelease(tag string, current semver.Version, releases []publishedRelease) (string, error) {
	var bestTag string
	var best semver.Version
	for _, release := range releases {
		if release.Draft {
			continue
		}
		if release.Tag == tag {
			return "", fmt.Errorf("tag %s already has a published release; use a new version", tag)
		}
		candidate, err := semver.ParseTag(release.Tag)
		if err != nil || candidate.Compare(current) >= 0 {
			continue
		}
		// A preview may follow earlier previews of this exact version. Stable
		// releases and new version lines always retain the last stable baseline.
		if release.Prerelease || candidate.IsPrerelease() {
			if !current.IsPrerelease() || !candidate.IsPrerelease() || !current.SameCore(candidate) {
				continue
			}
		}
		if bestTag == "" || candidate.Compare(best) > 0 || (candidate.Compare(best) == 0 && release.Tag > bestTag) {
			best, bestTag = candidate, release.Tag
		}
	}
	return bestTag, nil
}
