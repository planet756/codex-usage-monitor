package mac

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
	"unsafe"

	"codex-usage-monitor/internal/rollout"
	"github.com/ebitengine/purego/objc"
)

func TestNativeBridgeUnicodeAndCGRectRoundTrip(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := loadNative(); err != nil {
		t.Fatal(err)
	}
	p := pool()
	defer p.Send(sel("drain"))
	const value = "Codex 使用量 · ≥123 · 12.34%"
	if got := cfString(uintptr(ns(value))); got != value {
		t.Fatalf("native string round trip: %q", got)
	}
	if unsafe.Sizeof(rect{}) != 32 || unsafe.Sizeof(sourceContext{}) != 80 {
		t.Fatal("native ABI layout changed")
	}
	want := box(-300, 120, 480, 26)
	v := objc.ID(objc.GetClass("NSValue")).Send(sel("valueWithRect:"), want)
	if got := objc.Send[rect](v, sel("rectValue")); got != want {
		t.Fatalf("native struct round trip: %+v", got)
	}
}

func TestNativeStateDatabaseResolvesIndexedLog(t *testing.T) {
	t.Setenv("CODEX_SQLITE_HOME", "")
	home := t.TempDir()
	const id = "00000000-0000-7000-8000-000000000001"
	if _, err := indexedRolloutPath(context.Background(), home, "local", id); !errors.Is(err, rollout.ErrStateIndexUnavailable) {
		t.Fatalf("missing state index: %v", err)
	}
	path := filepath.Join(home, "state_5.sqlite")
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read-only lookup created a state database")
	}
	a, err := loadSQLite()
	if err != nil {
		t.Fatal(err)
	}
	var db uintptr
	code := a.open(path, &db, 6, 0) // SQLITE_OPEN_READWRITE | SQLITE_OPEN_CREATE
	if db != 0 {
		defer a.closeDB(db)
	}
	if code != 0 {
		t.Fatalf("open state fixture: %d", code)
	}
	exec := func(sql, value string) {
		t.Helper()
		var stmt uintptr
		if a.prepare(db, sql, -1, &stmt, nil) != 0 {
			t.Fatal("prepare state fixture")
		}
		defer a.finalize(stmt)
		if value != "" && a.bind(stmt, 1, value, -1, ^uintptr(0)) != 0 {
			t.Fatal("bind fixture path")
		}
		if a.step(stmt) != 101 {
			t.Fatal("write state fixture")
		}
	}
	exec("CREATE TABLE threads(id TEXT PRIMARY KEY, rollout_path TEXT)", "")
	rolloutPath := filepath.Join(home, "会话", "history.jsonl")
	exec("INSERT INTO threads VALUES ('"+id+"', ?1)", rolloutPath)
	if got, err := indexedRolloutPath(context.Background(), home, "local", id); got != rolloutPath || err != nil {
		t.Fatalf("indexed path = %q, %v", got, err)
	}
}

func TestKqueueReportsAppendAndReleasesOnCancellation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "current")
	if err := os.WriteFile(p, []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changes := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- watchDirectory(ctx, dir, "current", changes) }()
	// Append repeatedly until the real watcher is armed; no timing assumption
	// about a goroutine getting scheduled is used to assert notification.
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	timeout := time.NewTimer(2 * time.Second)
	defer timeout.Stop()
loop:
	for {
		select {
		case <-tick.C:
			f, err := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = f.Write([]byte("b"))
			f.Close()
			if err != nil {
				t.Fatal(err)
			}
		case <-changes:
			break loop
		case err := <-done:
			t.Fatalf("watcher stopped early: %v", err)
		case <-timeout.C:
			t.Fatal("append was not observed")
		}
	}
	cancel()
	select {
	case err := <-done:
		if err != context.Canceled {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancel did not join the native watcher")
	}
}

func TestKqueueRearmsAfterAtomicReplacementAndRecreation(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "current")
	if err := os.WriteFile(p, []byte("first"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	changes := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- watchDirectory(ctx, dir, "current", changes) }()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != context.Canceled {
				t.Errorf("watcher stopped: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("watcher did not exit on cancellation")
		}
	})
	waitChange := func() {
		t.Helper()
		select {
		case <-changes:
		case <-time.After(2 * time.Second):
			t.Fatal("file replacement/recreation was not observed")
		}
	}
	quiet := func() {
		t.Helper()
		timer := time.NewTimer(100 * time.Millisecond)
		defer timer.Stop()
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-changes:
				timer.Reset(100 * time.Millisecond)
			case <-timer.C:
				return
			case <-deadline.C:
				t.Fatal("watcher did not become idle")
			}
		}
	}
	expectAppend := func() {
		t.Helper()
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		deadline := time.NewTimer(2 * time.Second)
		defer deadline.Stop()
		for {
			select {
			case <-ticker.C:
				f, err := os.OpenFile(p, os.O_WRONLY|os.O_APPEND, 0600)
				if err != nil {
					t.Fatal(err)
				}
				_, err = f.Write([]byte("append"))
				f.Close()
				if err != nil {
					t.Fatal(err)
				}
			case <-changes:
				return
			case <-deadline.C:
				t.Fatal("subsequent append was not observed on the current inode")
			}
		}
	}
	expectAppend() // Wait for initial registration without a scheduling assumption.
	quiet()
	if err := os.WriteFile(filepath.Join(dir, "replacement"), []byte("second"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(dir, "replacement"), p); err != nil {
		t.Fatal(err)
	}
	waitChange()
	quiet() // Drain rename events so only a later append can satisfy the test.
	expectAppend()
	quiet()
	if err := os.Remove(p); err != nil {
		t.Fatal(err)
	}
	waitChange()
	quiet()
	if err := os.WriteFile(p, []byte("third"), 0600); err != nil {
		t.Fatal(err)
	}
	waitChange()
	quiet()
	expectAppend()
}
