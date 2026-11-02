package source

import (
	"errors"
	"strings"
	"testing"
)

func TestWindowsSourceMatrix(t *testing.T) {
	for _, tc := range []struct {
		name, raw        string
		options          Options
		want, home, kind string
	}{
		{"windows drive", `C:\Users\Alice\.codex\sessions\2026\10\24\chat.jsonl`, Options{}, `C:\Users\Alice\.codex\sessions\2026\10\24\chat.jsonl`, `C:\Users\Alice\.codex`, "windows_drive"},
		{"windows custom home", `D:/AI Data/custom/sessions/chat.jsonl`, Options{Home: `C:\irrelevant`}, `D:\AI Data\custom\sessions\chat.jsonl`, `D:\AI Data\custom`, "windows_drive"},
		{"archive Unicode", `E:\用户\配置\archived_sessions\chat.jsonl.zst`, Options{}, `E:\用户\配置\archived_sessions\chat.jsonl.zst`, `E:\用户\配置`, "windows_drive"},
		{"drive root", `F:\sessions\chat.jsonl`, Options{}, `F:\sessions\chat.jsonl`, `F:\`, "windows_drive"},
		{"UNC", `\\server\share\Codex\sessions\chat.jsonl`, Options{}, `\\server\share\Codex\sessions\chat.jsonl`, `\\server\share\Codex`, "windows_unc"},
		{"long drive", `\\?\C:\Code Data\sessions\chat.jsonl`, Options{}, `\\?\C:\Code Data\sessions\chat.jsonl`, `\\?\C:\Code Data`, "windows_drive"},
		{"long UNC", `\\?\UNC\server\share\Codex\sessions\chat.jsonl`, Options{}, `\\?\UNC\server\share\Codex\sessions\chat.jsonl`, `\\?\UNC\server\share\Codex`, "windows_unc"},
		{"file URI", `file:///C:/Code%20Data/sessions/chat.jsonl`, Options{}, `C:\Code Data\sessions\chat.jsonl`, `C:\Code Data`, "file_uri"},
		{"file URI UNC", `file://server/share/Codex/sessions/chat.jsonl`, Options{}, `\\server\share\Codex\sessions\chat.jsonl`, `\\server\share\Codex`, "file_uri"},
		{"file URI drive alias", `file:///mnt/d/AI%20Data/sessions/chat.jsonl`, Options{}, `D:\AI Data\sessions\chat.jsonl`, `D:\AI Data`, "file_uri"},
		{"long UNC lowercase", `\\?\unc\server\share\Codex\sessions\chat.jsonl`, Options{}, `\\?\unc\server\share\Codex\sessions\chat.jsonl`, `\\?\unc\server\share\Codex`, "windows_unc"},
		{"relative forward", `sessions/2026/chat.jsonl`, Options{Home: `D:\AI Data`}, `D:\AI Data\sessions\2026\chat.jsonl`, `D:\AI Data`, "relative"},
		{"relative native", `archived_sessions\chat.jsonl`, Options{Home: `D:\AI Data`}, `D:\AI Data\archived_sessions\chat.jsonl`, `D:\AI Data`, "relative"},
		{"relative explicit data home alias", `sessions/chat.jsonl`, Options{Home: `/mnt/d/AI Data`}, `D:\AI Data\sessions\chat.jsonl`, `D:\AI Data`, "relative"},
		{"drive alias C", `/mnt/c/Users/Bob/.codex/sessions/chat.jsonl`, Options{}, `C:\Users\Bob\.codex\sessions\chat.jsonl`, `C:\Users\Bob\.codex`, "drive_alias"},
		{"drive alias another drive", `/mnt/z/Shared Data/codex/archived_sessions/chat.jsonl.zst`, Options{}, `Z:\Shared Data\codex\archived_sessions\chat.jsonl.zst`, `Z:\Shared Data\codex`, "drive_alias"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Resolve(tc.raw, tc.options)
			if err != nil || got.Path != tc.want || got.Home != tc.home || got.Kind != tc.kind {
				t.Fatalf("%+v %v, want %q in %q (%s)", got, err, tc.want, tc.home, tc.kind)
			}
		})
	}
}

func TestDriveAliasBoundaries(t *testing.T) {
	for drive := byte('a'); drive <= 'z'; drive++ {
		raw := "/mnt/" + string(drive) + "/用户/Data/sessions/source.jsonl"
		want := strings.ToUpper(string(drive)) + `:\用户\Data\sessions\source.jsonl`
		if p, ok := driveAlias(raw); !ok || p != want {
			t.Fatal(raw, p, ok)
		}
	}
	for _, raw := range []string{"", "/mnt/c", "/mnt/c/", "/mnt/cc/source.jsonl", "/mnt/1/source.jsonl", "/mnt/C/source.jsonl", "/home/a/sessions/source.jsonl", "/c/source.jsonl", "mnt/c/source.jsonl", "/mnt/c/a/../source.jsonl", "/mnt/c/./source.jsonl", "/mnt/c/a\\source.jsonl", "/mnt/c/a:stream", "/mnt/c/a\x00b", `C:\data\source.jsonl`} {
		if p, ok := driveAlias(raw); ok || p != "" {
			t.Fatal("unproved drive mapping", raw, p)
		}
	}
}

func FuzzResolveSource(f *testing.F) {
	for _, s := range []string{`C:\Data\sessions\a.jsonl`, "/mnt/c/Data/sessions/a.jsonl", "file:///home/a/.codex/sessions/a.jsonl", `\\?\UNC\server\share\sessions\a.jsonl`, testVolume + `sessions\a.jsonl`, `\\?\C:\Data\..\sessions\a.jsonl`, "sessions/../outside", "file:file:"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, raw string) {
		p, err := Resolve(raw, Options{Home: `C:\Data`})
		if err == nil {
			if q, ok := windowsPath(p.Path); !ok || q != p.Path {
				t.Fatalf("noncanonical path %+v", p)
			}
			if _, ok := windowsPath(p.Home); !ok {
				t.Fatalf("invalid source root %+v", p)
			}
		}
	})
}

func TestSourceDoesNotInferFilesOrEscapeRoots(t *testing.T) {
	for _, raw := range []string{`/home/alice/.codex/sessions/chat.jsonl`, `/data/codex/sessions/chat.jsonl`} {
		if _, err := Resolve(raw, Options{}); !errors.Is(err, ErrUnsupported) {
			t.Fatal("non-Windows source inferred", raw, err)
		}
	}
	for _, raw := range []string{`sessions/../outside.jsonl`, `sessions\..\outside.jsonl`, `C:relative.jsonl`, `\rooted\sessions\chat.jsonl`, `file:///C:/data/sessions/chat.jsonl?token=secret`, `file://user:secret@host/share/sessions/chat.jsonl`, `/home/a/../b/sessions/chat.jsonl`, `/home/a\b/sessions/chat.jsonl`, "/home/a\x00b/sessions/chat.jsonl"} {
		if _, err := Resolve(raw, Options{Home: `C:\Data`}); err == nil {
			t.Fatal("ambiguous source accepted", raw)
		}
	}
}

func TestWindowsSourceRequiresNoBackendMetadataAndCannotBorrowRemoteIdentity(t *testing.T) {
	for _, host := range []string{"", "local"} {
		p, err := Resolve("/mnt/c/Users/Alice/.codex/sessions/current.jsonl", Options{Host: host})
		if err != nil || p.Path != `C:\Users\Alice\.codex\sessions\current.jsonl` || !p.Mapped {
			t.Fatal(p, err)
		}
	}
	for _, host := range []string{"remote-peer", "ssh:example"} {
		if _, err := Resolve(`C:\Data\sessions\current.jsonl`, Options{Host: host}); !errors.Is(err, ErrHostUnsupported) {
			t.Fatal("remote path reused locally", err)
		}
	}
	if _, err := Resolve("file:///home/user/sessions/current.jsonl", Options{Home: `C:\Data`}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("native directory was inferred", err)
	}
	if _, err := Resolve(strings.Repeat("x", (32<<10)+1), Options{Home: `C:\Data`}); !errors.Is(err, ErrUnsupported) {
		t.Fatal("oversized source accepted", err)
	}
	for raw, want := range map[string]string{`C:\Data`: `C:\Data`, "/mnt/d/AI Data": `D:\AI Data`, `file:///C:/AI%20Data`: `C:\AI Data`} {
		if p, err := Directory(raw); err != nil || p != want {
			t.Fatal(p, err)
		}
	}
}

func BenchmarkWindowsSource(b *testing.B) {
	for _, tc := range []struct{ name, raw string }{{"native", `C:\Users\Alice\.codex\sessions\chat.jsonl`}, {"drive_alias", "/mnt/c/Users/Alice/.codex/sessions/chat.jsonl"}} {
		b.Run(tc.name, func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				if _, err := Resolve(tc.raw, Options{}); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
