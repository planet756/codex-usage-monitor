package source

import (
	"errors"
	"testing"
)

func TestPOSIXSourcesPreserveLocalIdentity(t *testing.T) {
	for _, tc := range []struct{ raw, home, want, root string }{
		{"/Users/Alice/.codex/sessions/a.jsonl", "", "/Users/Alice/.codex/sessions/a.jsonl", "/Users/Alice/.codex"},
		{"file://localhost/Users/Alice/AI%20Data/archived_sessions/a.jsonl.zst", "", "/Users/Alice/AI Data/archived_sessions/a.jsonl.zst", "/Users/Alice/AI Data"},
		{"sessions/2026/a.jsonl", "/Users/Alice/自定义", "/Users/Alice/自定义/sessions/2026/a.jsonl", "/Users/Alice/自定义"},
		{"/Volumes/CaseSensitive/A.jsonl", "/Users/Alice/.codex", "/Volumes/CaseSensitive/A.jsonl", "/Users/Alice/.codex"},
	} {
		p, err := ResolvePOSIX(tc.raw, Options{Home: tc.home, Host: "local"})
		if err != nil || p.Path != tc.want || p.Home != tc.root {
			t.Fatalf("%q: %+v %v", tc.raw, p, err)
		}
	}
	for _, raw := range []string{"../sessions/a", "sessions/../a", "/Users/a/../b/sessions/a", `C:\sessions\a`, `sessions\a`, "file://remote/Users/a/sessions/a", "file:///Users/a/sessions/a?secret=x", "file:///Users/a/%00/sessions/a", "/Users/a\x00/sessions/a"} {
		if _, err := ResolvePOSIX(raw, Options{Home: "/Users/a/.codex"}); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
	if _, err := ResolvePOSIX("/Users/a/.codex/sessions/a", Options{Host: "remote"}); !errors.Is(err, ErrHostUnsupported) {
		t.Fatal(err)
	}
}
