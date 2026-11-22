package releasecheck

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"codex-usage-monitor/internal/semver"
)

const MaxWindowsAsset = 128 << 20

type WindowsAsset struct {
	Path   string
	sha256 string
}

// Verify checks the downloaded file using the handle the installer caller
// holds open against writes and deletion.
func (a WindowsAsset) Verify(file io.Reader) error {
	hash := sha256.New()
	n, err := io.Copy(hash, io.LimitReader(file, MaxWindowsAsset+1))
	if err != nil {
		return err
	}
	if n == 0 || n > MaxWindowsAsset || !strings.EqualFold(hex.EncodeToString(hash.Sum(nil)), a.sha256) {
		return errors.New("update SHA-256 does not match the published checksum")
	}
	return nil
}

func WindowsUpgrade(current, tag string) bool {
	installed, err := semver.ParseTag("v" + strings.TrimPrefix(current, "v"))
	next, nextErr := semver.ParseTag(tag)
	return err == nil && nextErr == nil && !next.IsPrerelease() && next.Compare(installed) > 0
}

// DownloadWindows downloads the published Windows payload and its expected digest.
// The caller must verify the file before using it.
func DownloadWindows(ctx context.Context, current, tag, dir string, portable bool) (WindowsAsset, error) {
	if !WindowsUpgrade(current, tag) {
		return WindowsAsset{}, errors.New("update must be a newer stable version")
	}
	httpClient := &http.Client{Timeout: 3 * time.Minute, CheckRedirect: func(req *http.Request, via []*http.Request) error {
		switch req.URL.Host {
		case "github.com", "release-assets.githubusercontent.com", "objects.githubusercontent.com":
			if req.URL.Scheme == "https" && req.URL.User == nil && len(via) <= 5 {
				return nil
			}
		}
		return errors.New("unexpected update download redirect")
	}}
	suffix := "setup.exe"
	if portable {
		suffix = "portable.zip"
	}
	name := "CodexUsageMonitor-" + tag + "-windows-amd64-" + suffix
	return downloadWindows(ctx, httpClient, ReleasesURL+"/download/"+tag+"/", name, dir)
}

func downloadWindows(ctx context.Context, client *http.Client, base, name, dir string) (WindowsAsset, error) {
	get := func(name string) (io.ReadCloser, error) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+name, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set("User-Agent", "CodexUsageMonitor")
		resp, err := client.Do(req)
		if err != nil {
			var requestErr *url.Error
			if errors.As(err, &requestErr) {
				err = requestErr.Err
			}
			return nil, fmt.Errorf("download %s: %w", name, err)
		}
		if resp.StatusCode != http.StatusOK {
			resp.Body.Close()
			return nil, fmt.Errorf("download %s: HTTP %d", name, resp.StatusCode)
		}
		return resp.Body, nil
	}
	checksums, err := get("SHA256SUMS")
	if err != nil {
		return WindowsAsset{}, err
	}
	const limit = 16 << 10
	data, err := io.ReadAll(io.LimitReader(checksums, limit+1))
	checksums.Close()
	if err != nil {
		return WindowsAsset{}, err
	}
	if len(data) > limit {
		return WindowsAsset{}, errors.New("update checksum manifest exceeds size limit")
	}
	want := ""
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.TrimPrefix(fields[1], "*") != name {
			continue
		}
		if want != "" {
			return WindowsAsset{}, errors.New("duplicate checksum in update manifest")
		}
		want = fields[0]
	}
	if digest, err := hex.DecodeString(want); err != nil || len(digest) != sha256.Size {
		return WindowsAsset{}, errors.New("update checksum is missing or invalid")
	}
	body, err := get(name)
	if err != nil {
		return WindowsAsset{}, err
	}
	defer body.Close()
	out, err := os.CreateTemp(dir, "download-*-"+name)
	if err != nil {
		return WindowsAsset{}, err
	}
	n, copyErr := io.Copy(out, io.LimitReader(body, MaxWindowsAsset+1))
	closeErr := out.Close()
	switch {
	case copyErr != nil:
		err = copyErr
	case closeErr != nil:
		err = closeErr
	case n == 0 || n > MaxWindowsAsset:
		err = errors.New("update payload has an invalid size")
	}
	if err != nil {
		os.Remove(out.Name())
		return WindowsAsset{}, err
	}
	return WindowsAsset{Path: out.Name(), sha256: want}, nil
}
