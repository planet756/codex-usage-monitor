package rollout

import (
	"codex-usage-monitor/internal/ipc"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

const locateChat = "01a15da4-16e9-7951-8fce-50f7bef8bb83"
const locateOther = "01a15e35-dabf-7521-bd4c-a15b0df6694a"
const locateThird = "01a15e35-dabf-7521-bd4c-a15b0df6694b"

func logHeader(id string) string {
	return `{"type":"session_meta","payload":{"id":"` + id + `"}}` + "\n"
}
func logName(id string) string { return "rollout-2026-10-21T16-46-37-" + id + ".jsonl" }
func putLog(t testing.TB, home, relative, content string) string {
	t.Helper()
	path := filepath.Join(home, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	if id, _, ok := rolloutName(filepath.Base(path)); ok && ipc.ValidThreadID(id) {
		selectFixture(home, id, path)
		t.Cleanup(func() { fixtureSelections.Delete(home + "\x00" + id) })
	}
	return path
}

// This is the fixture's explicit Desktop selection, not a discovery algorithm.
var fixtureSelections sync.Map

func selectFixture(home, id, path string) {
	fixtureSelections.Store(home+"\x00"+strings.ToLower(id), path)
}

type fixtureLocator Locator

func (l fixtureLocator) Resolve(ctx context.Context, id string) (History, error) {
	if l.ActivePath == "" {
		if p, ok := fixtureSelections.Load(l.Home + "\x00" + strings.ToLower(id)); ok {
			l.ActivePath = p.(string)
		} else {
			l.ActivePath = filepath.Join(l.Home, "sessions", logName(id))
		}
	}
	return Locator(l).Resolve(ctx, id)
}

func TestCanonicalNameSeparatesLogicalAndPhysicalIdentity(t *testing.T) {
	logical, physical, ok := rolloutName("rollout-2026-10-21T19-25-50-" + locateChat + "_" + locateOther + ".jsonl")
	if !ok || logical != locateChat || physical != locateOther {
		t.Fatal("continuation treated as a different chat")
	}
	for _, name := range []string{"rollout-" + locateChat + ".jsonl", logName(locateChat) + ".bak", "rollout-2026-99-99T00-00-00-" + locateChat + ".jsonl", "rollout-2026-10-21T00-00-00-" + locateChat + "_" + locateOther + "_" + locateThird + ".jsonl"} {
		if _, _, ok := rolloutName(name); ok {
			t.Fatalf("invalid canonical name accepted: %s", name)
		}
	}
}

func TestLocatorEmptyIndexUsesMetadataAndIgnoresUnrelatedNewestLog(t *testing.T) {
	home := t.TempDir()
	want := putLog(t, home, "sessions/"+logName(locateChat), logHeader(locateChat)+usageLine)
	other := putLog(t, home, "sessions/"+logName(locateOther), logHeader(locateOther)+usageLine)
	old := time.Unix(1, 0)
	if err := os.Chtimes(want, old, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(other, time.Now(), time.Now()); err != nil {
		t.Fatal(err)
	}
	l := fixtureLocator{Home: home}
	h, err := l.Resolve(context.Background(), locateChat)
	if err != nil || h.Head != want || len(h.Segments) != 1 {
		t.Fatalf("resolution: %+v %v", h, err)
	}
	data, _ := os.ReadFile(want)
	info, _ := os.Stat(want)
	if string(data) != logHeader(locateChat)+usageLine || !info.ModTime().Equal(old) {
		t.Fatal("source log changed")
	}
}

func TestLocatorRequiresExplicitDesktopBranch(t *testing.T) {
	home := t.TempDir()
	path := putLog(t, home, "sessions/"+logName(locateChat), logHeader(locateChat))
	if _, err := (Locator{Home: home}).Resolve(context.Background(), locateChat); !errors.Is(err, ErrBranchUnverified) {
		t.Fatal("unselected branch was guessed", err)
	}
	putLog(t, home, "archived_sessions/"+logName(locateChat), logHeader(locateChat))
	h, err := (Locator{Home: home, ActivePath: path}).Resolve(context.Background(), locateChat)
	if err != nil || h.Head != path {
		t.Fatal("explicit head was replaced by a duplicate", err)
	}
}

func TestLocatorRejectsUnverifiedHeaders(t *testing.T) {
	for _, tc := range []struct {
		body string
		want error
	}{
		{logHeader(locateOther), ErrChatMismatch}, {"bad JSON\n", ErrLogIdentityUnverified},
		{usageLine + logHeader(locateChat), ErrLogIdentityUnverified}, {strings.TrimSpace(logHeader(locateChat)), ErrLogPending},
		{"", ErrLogPending}, {strings.Repeat(" ", MaxRecordBytes+1) + "\n", ErrLogIdentityUnverified},
		{`{"type":"session_meta","payload":{"id":"` + locateChat + `","history_mode":"paginated"}}` + "\n", ErrHistoryOrdinal},
	} {
		home := t.TempDir()
		putLog(t, home, "sessions/"+logName(locateChat), tc.body)
		if _, err := (fixtureLocator{Home: home}).Resolve(context.Background(), locateChat); !errors.Is(err, tc.want) {
			t.Fatalf("header accepted: %v want %v", err, tc.want)
		}
	}
}

func TestLocatorBoundsCancellationAndAccess(t *testing.T) {
	home := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	l := fixtureLocator{Home: home}
	if _, err := l.Resolve(ctx, locateChat); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := l.Resolve(context.Background(), "../foreign"); !errors.Is(err, ErrLogIdentityUnverified) {
		t.Fatal(err)
	}
	if _, err := l.Resolve(context.Background(), locateChat); !errors.Is(err, ErrLogNotFound) {
		t.Fatal(err)
	}
	l.Home = filepath.Join(home, "missing")
	if _, err := l.Resolve(context.Background(), locateChat); !errors.Is(err, ErrLogNotFound) {
		t.Fatal(err)
	}
	l.Home = home
	putLog(t, home, "sessions/"+logName(locateChat), logHeader(locateChat))
	if err := os.MkdirAll(filepath.Join(home, "archived_sessions", strings.Repeat("nested/", 18)), 0700); err != nil {
		t.Fatal(err)
	}
	if _, err := l.Resolve(context.Background(), locateChat); err != nil {
		t.Fatalf("unrelated directory blocked explicit head: %v", err)
	}
}

func TestLocatorArchivedMixedCaseAndBatchedDirectory(t *testing.T) {
	home := t.TempDir()
	for i := range 300 {
		putLog(t, home, fmt.Sprintf("sessions/unrelated-%d.jsonl", i), "not opened")
	}
	want := putLog(t, home, "archived_sessions/"+strings.ToUpper(logName(locateChat)), logHeader(strings.ToUpper(locateChat)))
	if h, err := (fixtureLocator{Home: home}).Resolve(context.Background(), locateChat); err != nil || h.Head != want {
		t.Fatalf("archive: %v", err)
	}
}

func TestLocatorSkipsDirectoryAndFileLinks(t *testing.T) {
	home := t.TempDir()
	want := putLog(t, home, "sessions/"+logName(locateChat), logHeader(locateChat))
	if err := os.Symlink(home, filepath.Join(home, "sessions", "loop")); err != nil {
		t.Skip(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "archived_sessions"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(want, filepath.Join(home, "archived_sessions", logName(locateChat))); err != nil {
		t.Fatal(err)
	}
	if h, err := (fixtureLocator{Home: home}).Resolve(context.Background(), locateChat); err != nil || h.Head != want {
		t.Fatalf("followed links: %v", err)
	}
}

func BenchmarkLocateWithoutIndex(b *testing.B) {
	home := b.TempDir()
	for i := range 1000 {
		putLog(b, home, fmt.Sprintf("sessions/%02d/rollout-2026-10-21T00-00-00-00000000-0000-7000-8000-%012d.jsonl", i/50, i), "not opened")
	}
	putLog(b, home, "sessions/00/"+logName(locateChat), logHeader(locateChat)+usageLine)
	l := fixtureLocator{Home: home}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := l.Resolve(context.Background(), locateChat); err != nil {
			b.Fatal(err)
		}
	}
}
