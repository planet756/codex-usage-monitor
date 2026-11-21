package releasecheck

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCheckPublishedStableRelease(t *testing.T) {
	for _, tc := range []struct {
		name, current, body, wantURL string
		status                       int
		newer, development, failed   bool
	}{
		{"newer", "0.1.9", `{"tag_name":"v0.1.10","html_url":"https://other.example/download"}`, ReleasesURL + "/tag/v0.1.10", 200, true, false, false},
		{"same", "0.1.10", `{"tag_name":"v0.1.10"}`, "", 200, false, false, false},
		{"no downgrade", "0.2.0-rc.1", `{"tag_name":"v0.1.10"}`, "", 200, false, false, false},
		{"preview to stable", "0.1.10-rc.1", `{"tag_name":"v0.1.10"}`, ReleasesURL + "/tag/v0.1.10", 200, true, false, false},
		{"dev", "dev", `{"tag_name":"v0.1.10"}`, ReleasesURL + "/tag/v0.1.10", 200, false, true, false},
		{"unpublished", "0.1.2", `{"tag_name":"v0.1.10","draft":true}`, ReleasesURL, 200, false, false, true},
		{"preview marked", "0.1.2", `{"tag_name":"v0.1.10","prerelease":true}`, ReleasesURL, 200, false, false, true},
		{"preview tag", "0.1.2", `{"tag_name":"v0.1.10-rc.1"}`, ReleasesURL, 200, false, false, true},
		{"bad tag", "0.1.2", `{"tag_name":"v0.1.10/redirect"}`, ReleasesURL, 200, false, false, true},
		{"rate limited", "0.1.2", `{}`, ReleasesURL, 403, false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != http.MethodGet || r.Header.Get("Authorization") != "" {
					t.Error("expected a public metadata GET")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			result := check(t.Context(), server.Client(), server.URL, tc.current)
			if result.Newer != tc.newer || result.Development != tc.development || (result.Err != nil) != tc.failed {
				t.Fatalf("unexpected release check result: %+v", result)
			}
			message, url := result.Prompt("zh-CN")
			if url != tc.wantURL || message == "" {
				t.Fatalf("release dialog = %q, %q", message, url)
			}
			if !tc.failed && (!strings.Contains(message, "当前版本") || !strings.Contains(message, "v0.1.10")) {
				t.Fatalf("missing version information: %q", message)
			}
		})
	}
}
