// Package releasecheck checks the project's public GitHub Release metadata.
package releasecheck

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"codex-usage-monitor/internal/i18n"
	"codex-usage-monitor/internal/semver"
)

const (
	repository   = "planet756/codex-usage-monitor"
	ProjectURL   = "https://github.com/" + repository
	ReleasesURL  = ProjectURL + "/releases"
	latestAPI    = "https://api.github.com/repos/" + repository + "/releases/latest"
	maxReplySize = 1 << 20
)

var client = &http.Client{
	Timeout: 8 * time.Second,
	CheckRedirect: func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	},
}

type Result struct {
	Current, Latest    string
	Development, Newer bool
	Err                error
}

func Check(ctx context.Context, current string) Result {
	return check(ctx, client, latestAPI, current)
}

type Job struct {
	cancel context.CancelFunc
	done   chan struct{}
	result chan Result
}

// Start performs only the requested check. The UI can poll its result or
// supply a callback that posts a native message without blocking the worker.
func Start(parent context.Context, current string, ready func()) *Job {
	ctx, cancel := context.WithCancel(parent)
	job := &Job{cancel: cancel, done: make(chan struct{}), result: make(chan Result, 1)}
	go func() {
		defer close(job.done)
		result := Check(ctx, current)
		if ctx.Err() == nil {
			job.result <- result
			if ready != nil {
				ready()
			}
		}
	}()
	return job
}

func (job *Job) Poll() (Result, bool) {
	if job != nil {
		select {
		case result := <-job.result:
			return result, true
		default:
		}
	}
	return Result{}, false
}

func (job *Job) Close() {
	if job != nil {
		job.cancel()
		<-job.done
	}
}

func check(ctx context.Context, client *http.Client, endpoint, current string) (result Result) {
	result.Current = current
	result.Development = current == "dev"
	var installed semver.Version
	if !result.Development {
		tag := "v" + strings.TrimPrefix(current, "v")
		parsed, err := semver.ParseTag(tag)
		if err != nil {
			result.Err = err
			return result
		}
		result.Current, installed = tag, parsed
	}
	latestTag, err := fetchLatest(ctx, client, endpoint)
	if err != nil {
		result.Err = err
		return result
	}
	latest, err := semver.ParseTag(latestTag)
	if err != nil || latest.IsPrerelease() {
		result.Err = errors.New("latest release has an invalid stable version tag")
		return result
	}
	result.Latest = latestTag
	result.Newer = !result.Development && latest.Compare(installed) > 0
	return result
}

func fetchLatest(ctx context.Context, client *http.Client, endpoint string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "CodexUsageMonitor")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("GitHub release check returned HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxReplySize+1))
	if err != nil {
		return "", err
	}
	if len(body) > maxReplySize {
		return "", errors.New("release metadata exceeds size limit")
	}
	var release struct {
		Tag        string `json:"tag_name"`
		Draft      bool   `json:"draft"`
		Prerelease bool   `json:"prerelease"`
	}
	if err = json.Unmarshal(body, &release); err != nil {
		return "", err
	}
	if release.Draft || release.Prerelease {
		return "", errors.New("latest release is not a published stable release")
	}
	return release.Tag, nil
}

// Prompt returns the localized outcome and the optional page the user can open.
func (r Result) Prompt(language string) (message, url string) {
	if r.Err != nil {
		return i18n.Text(language, "Could not check for updates. Try again or open GitHub Releases."), ReleasesURL
	}
	message = i18n.Format(language, "Current version: %s\nLatest stable release: %s", r.Current, r.Latest)
	if r.Newer || r.Development {
		if r.Newer {
			message = i18n.Text(language, "A new version is available.") + "\n\n" + message
		}
		return message, ReleasesURL + "/tag/" + r.Latest
	}
	return i18n.Text(language, "No newer stable release is available.") + "\n\n" + message, ""
}
