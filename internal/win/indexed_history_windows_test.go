package win

import (
	"context"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"codex-usage-monitor/internal/monitor"
	"codex-usage-monitor/internal/source"
	"golang.org/x/sys/windows"
)

func TestNativeHistoryWithoutDesktopIPC(t *testing.T) {
	t.Setenv("CODEX_SQLITE_HOME", "")
	home := t.TempDir()
	indexed, _ := nativeLog(t, home, resolverChat, physicalChild, 0, nil, nativeUsage(t, 1, "turn", "selected", 20, 20))
	replacement, _ := nativeLog(t, home, resolverChat, resolverChat, 0, nil, nativeUsage(t, 1, "turn", "other", 90, 90))
	foreign, _ := nativeLog(t, home, physicalChild, physicalChild, 0, nil, nativeUsage(t, 1, "turn", "foreign", 40, 40))
	db := &catalogFixtureDB{t: t, path: filepath.Join(home, "state_5.sqlite")}
	filename, _ := windows.BytePtrFromString(db.path)
	if call(sqlite, "sqlite3_open_v2", uintptr(unsafe.Pointer(filename)), uintptr(unsafe.Pointer(&db.db)), 6, 0) != 0 {
		t.Fatal("create state index")
	}
	t.Cleanup(db.close)
	db.exec("CREATE TABLE threads(id TEXT PRIMARY KEY, rollout_path TEXT NOT NULL)")
	db.exec("INSERT INTO threads VALUES ('" + resolverChat + "','" + strings.ReplaceAll(indexed, "'", "''") + "')")
	db.exec("INSERT INTO threads VALUES ('" + physicalChild + "','" + strings.ReplaceAll(foreign, "'", "''") + "')")
	const wrongOwner = "00000000-0000-7000-8000-000000000003"
	db.exec("INSERT INTO threads VALUES ('" + wrongOwner + "','" + strings.ReplaceAll(foreign, "'", "''") + "')")
	catalog := newCatalogFixture(t, home, "codex.db")
	catalog.row(resolverChat, "local", "First chat", 0)
	catalog.row(physicalChild, "local", "Same name", 0)
	catalog.row(physicalChild, "remote", "Same name", 0)
	catalog.close()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	configPath := filepath.Join(t.TempDir(), "settings.json")
	views := make(chan viewSelection, 1)
	updates := make(chan dataUpdate, 8)
	done := make(chan struct{})
	client, server := net.Pipe()
	defer server.Close()
	defer client.Close()
	go io.Copy(io.Discard, server)
	var connect atomic.Bool
	go func() {
		defer close(done)
		monitor.Run(ctx, home, configPath, views, func(u dataUpdate) {
			select {
			case updates <- u:
			case <-ctx.Done():
			}
		}, monitor.Dependencies{
			Dial: func(context.Context) (io.ReadWriteCloser, error) {
				if connect.CompareAndSwap(true, false) {
					return client, nil
				}
				return nil, os.ErrNotExist
			},
			IndexedPath: indexedRolloutPath, Watch: watchDirectory, Resolve: source.Resolve,
		})
	}()
	defer func() { cancel(); <-done }()

	waitFor := func(check func(dataUpdate) bool) dataUpdate {
		t.Helper()
		for {
			select {
			case u := <-updates:
				if check(u) {
					return u
				}
			case <-ctx.Done():
				t.Fatal("timed out waiting for history update")
			}
		}
	}
	views <- viewSelection{ChatPage: true, ID: resolverChat, Reason: "verified"}
	first := waitFor(func(u dataUpdate) bool { return u.ID == resolverChat && u.Snapshot.HasUsage })
	if first.Connected || !first.BranchVerified || first.Snapshot.Total != 120 {
		t.Fatalf("indexed history: connected=%t verified=%t total=%d", first.Connected, first.BranchVerified, first.Snapshot.Total)
	}
	connect.Store(true)
	afterConnect := waitFor(func(u dataUpdate) bool { return u.Connected })
	if !afterConnect.Snapshot.HasUsage || afterConnect.Generation != first.Generation {
		t.Fatal("an idle IPC connection interrupted indexed history")
	}
	views <- viewSelection{ChatPage: true, ID: physicalChild, Host: "local", Reason: "verified"}
	second := waitFor(func(u dataUpdate) bool { return u.ID == physicalChild && u.Snapshot.HasUsage })
	if second.Snapshot.Total != 140 {
		t.Fatalf("switched chat total: %d", second.Snapshot.Total)
	}
	views <- viewSelection{ChatPage: true, ID: resolverChat, Host: "local", Reason: "verified"}
	back := waitFor(func(u dataUpdate) bool { return u.ID == resolverChat && u.Snapshot.HasUsage })
	if back.Snapshot.Total != 120 {
		t.Fatalf("returning to first chat: %d", back.Snapshot.Total)
	}
	db.exec("UPDATE threads SET rollout_path='" + strings.ReplaceAll(replacement, "'", "''") + "' WHERE id='" + resolverChat + "'")
	replaced := waitFor(func(u dataUpdate) bool { return u.ID == resolverChat && u.Snapshot.HasUsage && u.Snapshot.Total == 190 })
	if !replaced.BranchVerified || replaced.Generation != back.Generation {
		t.Fatal("index relocation did not refresh the selected branch in place")
	}
	views <- viewSelection{ChatPage: true, ID: wrongOwner, Host: "local", Reason: "verified"}
	mismatch := waitFor(func(u dataUpdate) bool { return u.ID == wrongOwner && !u.Loading })
	if mismatch.Snapshot.HasUsage || mismatch.BranchVerified || mismatch.Message != "Chat ID mismatch · usage withheld" {
		t.Fatalf("wrong owner's history was not rejected: %q", mismatch.Message)
	}
	views <- viewSelection{ChatPage: true, ID: physicalChild, Reason: "verified"}
	ambiguous := waitFor(func(u dataUpdate) bool { return u.ID == physicalChild && !u.Loading })
	if ambiguous.Snapshot.HasUsage || ambiguous.BranchVerified || ambiguous.Message != "Waiting for Desktop current history branch" {
		t.Fatalf("ambiguous host accepted: %q", ambiguous.Message)
	}
	views <- viewSelection{ChatPage: true, ID: resolverChat, Host: "remote", Reason: "verified"}
	waitFor(func(u dataUpdate) bool { return u.ID == resolverChat && u.Host == "remote" && !u.Snapshot.HasUsage })
}
