package source

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

const testVolume = `\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}\`

func TestVerbatimWindowsSourcePreservesIdentity(t *testing.T) {
	for _, tc := range []struct{ home, kind string }{
		{`\\?\c:\Data\literal\..\`, "windows_drive"},
		{`\\?\C:\Data\.\用户. \`, "windows_drive"},
		{`\\?\UNC\server\share\Data\..\`, "windows_unc"},
		{`\\?\unc\server\share\Data\\`, "windows_unc"},
		{testVolume, "windows_volume"},
		{testVolume + `Data\..\用户. \`, "windows_volume"},
	} {
		raw := tc.home + `sessions\rollout.jsonl`
		p, err := Resolve(raw, Options{Home: `Z:\unrelated`})
		wantHome := strings.TrimSuffix(tc.home, `\`)
		if volume, ok := verbatimVolume(raw); ok && (len(wantHome) == volume || strings.HasSuffix(wantHome, `\`)) {
			wantHome += `\`
		}
		if err != nil || p.Path != raw || p.Home != wantHome || p.Kind != tc.kind {
			t.Fatalf("identity changed: %+v %v; want path %q home %q", p, err, raw, wantHome)
		}
		if got := JoinPath(p.Home, "sessions", "rollout.jsonl"); got != raw {
			t.Fatalf("history walk changed verbatim path: %q != %q", got, raw)
		}
		if got := JoinPath(DirPath(raw), "rollout.jsonl"); got != raw {
			t.Fatalf("watch directory changed verbatim path: %q != %q", got, raw)
		}
		if got, err := Directory(p.Home); err != nil || got != p.Home {
			t.Fatalf("data directory changed: %q %v", got, err)
		}
		p, err = Resolve(`sessions/chat.jsonl`, Options{Home: tc.home})
		if err != nil || p.Path != tc.home+`sessions\chat.jsonl` {
			t.Fatalf("relative source changed verbatim home: %+v %v", p, err)
		}
	}
}

func TestVerbatimWindowsSourceRejectsMalformedNamespace(t *testing.T) {
	for _, raw := range []string{
		`\\?\C:relative\sessions\chat.jsonl`, `\\?\C:/Data/sessions/chat.jsonl`,
		`//?/C:/Data/sessions/chat.jsonl`, `\\?\UNC\server`, `\\?\UNC\\share\sessions\chat.jsonl`,
		`\\?\UNC\..\share\sessions\chat.jsonl`, `\\?\UNC\server\.\sessions\chat.jsonl`,
		`\\?\Volume{01234567-89ab-cdef-0123-456789abcdef}relative\sessions\chat.jsonl`,
		`\\?\Volume{01234567-89ab-cdef-0123-456789abcdeg}\sessions\chat.jsonl`,
		`\\?\Volume{0123456789abcdef0123456789abcdef}\sessions\chat.jsonl`,
		`\\?\GLOBALROOT\Device\HarddiskVolume1\sessions\chat.jsonl`, `\\.\pipe\sessions\chat.jsonl`,
	} {
		if p, err := Resolve(raw, Options{Home: `C:\Data`}); !errors.Is(err, ErrUnsupported) {
			t.Fatalf("malformed or device namespace accepted: %q %+v %v", raw, p, err)
		}
	}
}

func TestVerbatimDirectoryRootsAndPortablePaths(t *testing.T) {
	for _, root := range []string{`\\?\C:\`, `\\?\UNC\server\share\`, testVolume} {
		for _, p := range []string{root, root + "chat.jsonl"} {
			if got := DirPath(p); got != root {
				t.Fatalf("root lost: %q -> %q", p, got)
			}
		}
	}
	bareShare := `\\?\UNC\server\share`
	if got := DirPath(bareShare); got != bareShare+`\` {
		t.Fatal(got)
	}
	base := filepath.Join(t.TempDir(), "Data")
	if got := JoinPath(base, "sessions", "chat.jsonl"); got != filepath.Join(base, "sessions", "chat.jsonl") || DirPath(got) != filepath.Join(base, "sessions") {
		t.Fatal("portable fixture path changed", got)
	}
}
