package win

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Run on native Windows. Tests create real Shell Links only in t.TempDir,
// without changing the user's Start menu or launching any shortcut target.
func TestSameShortcutPathUsesFileIdentityForAliases(t *testing.T) {
	d := t.TempDir()
	target := filepath.Join(d, "monitor.exe")
	alias := d + string(os.PathSeparator) + "." + string(os.PathSeparator) + "monitor.exe"
	other := filepath.Join(d, "other.exe")
	if err := os.WriteFile(target, []byte("target"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(other, []byte("other"), 0600); err != nil {
		t.Fatal(err)
	}
	if !sameShortcutPath(alias, target) || sameShortcutPath(other, target) || sameShortcutPath("", target) {
		t.Fatal("shortcut target aliases were not checked by file identity")
	}
}

func TestStartMenuLinkRoundTripMoveAndIdempotence(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "Programs", startMenuName)
	exe := filepath.Join(d, "Portable 中文", "CodexUsageMonitor.exe")
	cfg := filepath.Join(d, "Settings 中文", "custom settings.json")
	if err := os.MkdirAll(filepath.Dir(exe), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(exe, []byte("fixture; never executed"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := shellApartment(func() error {
		if err := ensureStartMenuLink(path, exe, cfg); err != nil {
			return err
		}
		args := "--config " + windows.EscapeArg(cfg)
		if ok, detail := startMenuLinkStatus(path, exe, args); !ok {
			t.Fatal("saved link did not preserve target, Unicode paths, working directory, arguments or icon:", detail)
		}
		got, err := func() (string, error) {
			link, persist, err := newShellLink()
			if err != nil {
				return "", err
			}
			defer comCall(link, 2)
			defer comCall(persist, 2)
			if int32(comCall(persist, 5, uintptr(unsafe.Pointer(utf(path))), 0)) < 0 {
				return "", fmt.Errorf("cannot load native shortcut")
			}
			value, ok := shellLinkField(link, 10, args)
			if !ok {
				return "", fmt.Errorf("cannot read shortcut arguments")
			}
			return value, nil
		}()
		if err != nil {
			return err
		}
		parts, err := windows.DecomposeCommandLine("monitor.exe " + got)
		if err != nil || len(parts) != 3 || parts[1] != "--config" || parts[2] != cfg {
			t.Fatalf("shortcut configuration arguments were not reversible: %q %v", got, err)
		}
		before, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		stamp := time.Unix(1_600_000_000, 0)
		if err := os.Chtimes(path, stamp, stamp); err != nil {
			return err
		}
		if err := ensureStartMenuLink(path, exe, cfg); err != nil {
			return err
		}
		after, _ := os.ReadFile(path)
		info, _ := os.Stat(path)
		if !bytes.Equal(before, after) || !info.ModTime().Equal(stamp) {
			t.Fatal("unchanged shortcut was rewritten")
		}
		moved := filepath.Join(d, "Moved folder", "CodexUsageMonitor.exe")
		if err := os.MkdirAll(filepath.Dir(moved), 0700); err != nil {
			return err
		}
		if err := os.WriteFile(moved, []byte("moved fixture"), 0600); err != nil {
			return err
		}
		if err := ensureStartMenuLink(path, moved, cfg); err != nil {
			return err
		}
		if ok, detail := startMenuLinkStatus(path, moved, args); !ok {
			t.Fatal("moving portable executable did not repair the shortcut:", detail)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestStartMenuLinkRepairsCorruptionAndRejectsInvalidPaths(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, startMenuName)
	exe := filepath.Join(d, "monitor.exe")
	cfg := filepath.Join(d, "settings.json")
	if err := os.WriteFile(exe, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("broken shortcut"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := shellApartment(func() error {
		if err := ensureStartMenuLink(path, exe, cfg); err != nil {
			return err
		}
		if ok, detail := startMenuLinkStatus(path, exe, "--config "+windows.EscapeArg(cfg)); !ok {
			t.Fatal("corrupt shortcut was not replaced:", detail)
		}
		before, _ := os.ReadFile(path)
		if ensureStartMenuLink(path, exe, cfg+"\x00invalid") == nil {
			t.Fatal("invalid path was accepted")
		}
		after, _ := os.ReadFile(path)
		if !bytes.Equal(before, after) {
			t.Fatal("failed repair changed the existing link")
		}
		leftovers, _ := filepath.Glob(filepath.Join(d, ".cum-menu-*"))
		if len(leftovers) != 0 {
			t.Fatal("shortcut write left temporary files")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}
