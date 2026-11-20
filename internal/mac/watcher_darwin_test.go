package mac

import (
	"errors"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestSingletonLockExcludesAndReleases(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	first, err := processLock(path, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := processLock(path, "watcher")
	if err == nil {
		second.Close()
		t.Fatal("second watcher acquired singleton lock")
	}
	if !errors.Is(err, unix.EWOULDBLOCK) {
		t.Fatal(err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	third, err := processLock(path, "watcher")
	if err != nil {
		t.Fatal(err)
	}
	third.Close()
}
