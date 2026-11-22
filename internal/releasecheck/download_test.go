package releasecheck

import (
	"bytes"
	"crypto/sha256"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func TestDownloadWindowsVerifiesPublishedChecksum(t *testing.T) {
	const name = "CodexUsageMonitor-v0.1.4-windows-amd64-setup.exe"
	const payload = "fixture installer"
	for _, tc := range []struct {
		name, expectedPayload string
		duplicate, wantError  bool
	}{
		{name: "matching", expectedPayload: payload},
		{name: "mismatch", expectedPayload: "different installer", wantError: true},
		{name: "duplicate", expectedPayload: payload, duplicate: true, wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manifest := fmt.Sprintf("%x  %s\n", sha256.Sum256([]byte(tc.expectedPayload)), name)
			if tc.duplicate {
				manifest += manifest
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/SHA256SUMS":
					fmt.Fprint(w, manifest)
				case "/" + name:
					fmt.Fprint(w, payload)
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			dir := t.TempDir()
			asset, err := downloadWindows(t.Context(), server.Client(), server.URL+"/", name, dir)
			if tc.duplicate {
				if err == nil {
					t.Fatal("download accepted a duplicate checksum")
				}
				entries, err := os.ReadDir(dir)
				if err != nil || len(entries) != 0 {
					t.Fatalf("failed downloads must leave no payload: %v, %v", entries, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(asset.Path)
			if err != nil || string(got) != payload {
				t.Fatalf("downloaded payload = %q, %v", got, err)
			}
			if err := asset.Verify(bytes.NewReader(got)); (err != nil) != tc.wantError {
				t.Fatalf("Verify() error = %v", err)
			}
		})
	}
}
