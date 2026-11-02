package win

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"codex-usage-monitor/internal/rollout"
	"golang.org/x/sys/windows"
)

func TestNativeWindowsSpecialPathsKeepHistoryAndReleaseFiles(t *testing.T) {
	base := t.TempDir()
	if len(filepath.VolumeName(base)) != 2 {
		t.Skip("native extended-drive and volume GUID tests need a local drive")
	}
	// The trailing space/dot is intentional. Stripping verbatim semantics or
	// converting to an ordinary path would address a different directory.
	home := `\\?\` + base + `\数据. `
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	// Go's generic TempDir cleanup may use ordinary naming rules. Remove this
	// test-owned verbatim tree explicitly before its parent TempDir is removed.
	t.Cleanup(func() {
		if err := os.RemoveAll(home); err != nil {
			t.Error(err)
		}
	})
	reverted := nativeUsage(t, 2, "revoked", "revoked-response", 99999, 100004)
	parent, data := nativeLog(t, home, resolverChat, resolverChat, 0, nil, nativeUsage(t, 1, "a", "a-response", 5, 5), reverted)
	cut := int64(len(data) - len(reverted))
	ref := map[string]any{"thread_id": resolverChat, "end_ordinal_exclusive": int64(2), "end_byte_offset": cut}
	head, _ := nativeLog(t, home, resolverChat, physicalChild, 2, ref, nativeUsage(t, 3, "b", "b-response", 3, 8))
	verify := func(t *testing.T, raw string) {
		t.Helper()
		h, p, err := resolveActiveThread(context.Background(), `Z:\unrelated`, "local", resolverChat, raw, nil)
		if err != nil || h.Head != raw || p.Path != raw || len(h.Segments) != 2 || h.Segments[0].Bytes != cut {
			t.Fatalf("explicit path/history identity changed: %+v %+v %v", h, p, err)
		}
		r := rollout.NewHistory(h)
		defer r.Close()
		if _, err := r.Update(context.Background()); err != nil {
			t.Fatal(err)
		}
		s := r.State.Snapshot()
		if s.Turns != 2 || s.Steps != 2 || !s.HasUsage || s.Usage.Output != 8 || !s.CountsComplete {
			t.Fatalf("special path changed selected-history accounting: %+v", s)
		}
	}
	t.Run("extended_drive", func(t *testing.T) { verify(t, head) })
	t.Run("volume_GUID", func(t *testing.T) {
		mount, _ := windows.UTF16PtrFromString(filepath.VolumeName(base) + `\`)
		var name [64]uint16
		if err := windows.GetVolumeNameForVolumeMountPoint(mount, &name[0], uint32(len(name))); err != nil {
			t.Fatal(err)
		}
		volume := windows.UTF16ToString(name[:])
		// Replace only the explicitly verified volume prefix, never search for
		// another directory or select a log by filename/mtime.
		raw := volume + strings.TrimPrefix(head, `\\?\`+filepath.VolumeName(base)+`\`)
		verify(t, raw)
	})
	// Negative control: the native exclusive-open probe must detect a source
	// handle left open, independently of GC and whole-process handle totals.
	open, err := os.Open(head)
	if err != nil {
		t.Fatal(err)
	}
	probeErr := exclusiveSourceProbe(head)
	if err := open.Close(); err != nil {
		t.Fatal(err)
	}
	if probeErr != windows.ERROR_SHARING_VIOLATION {
		t.Fatalf("exclusive probe missed an intentionally open file: %v", probeErr)
	}
	// Check every file and directory owned by discovery/replay after each
	// return, before any GC. This catches leaked source/directory handles even
	// if the runtime would eventually finalize them. Go/runtime/DWrite thread
	// handles are process-wide and are logged separately, not used as an oracle.
	verify(t, head)
	countHandles := func() uint32 {
		var count uint32
		if call(kernel32, "GetProcessHandleCount", ^uintptr(0), uintptr(unsafe.Pointer(&count))) == 0 {
			t.Fatal("GetProcessHandleCount")
		}
		return count
	}
	before := countHandles()
	for range 64 {
		verify(t, head)
		for _, p := range []string{parent, head, filepath.Dir(head), home} {
			if err := exclusiveSourceProbe(p); err != nil {
				t.Fatalf("source/directory handle retained after close: %v", err)
			}
		}
	}
	t.Logf("exclusive source/directory probes passed after 64 reads; process-wide handles before=%d after=%d", before, countHandles())
}

func exclusiveSourceProbe(path string) error {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING,
		windows.FILE_FLAG_BACKUP_SEMANTICS, 0)
	if err != nil {
		return err
	}
	return windows.CloseHandle(h)
}

func TestNativeWindowsMissingVerbatimComponentCannotReadNormalizedBranch(t *testing.T) {
	home := t.TempDir()
	head, _ := nativeLog(t, home, resolverChat, resolverChat, 0, nil, nativeUsage(t, 1, "a", "a-response", 5, 5))
	if len(filepath.VolumeName(home)) != 2 {
		t.Skip("needs a local Windows drive")
	}
	raw := `\\?\` + home + `\missing\..\` + strings.TrimPrefix(head, home+`\`)
	if _, _, err := resolveActiveThread(context.Background(), home, "local", resolverChat, raw, nil); err == nil {
		t.Fatal("an unavailable verbatim address was silently replaced by its cleaned path")
	}
}
