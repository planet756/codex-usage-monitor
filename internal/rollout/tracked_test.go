package rollout

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestTrackedReaderRecoversMovedPathWithOrWithoutOldFile(t *testing.T) {
	for _, removeOld := range []bool{false, true} {
		t.Run(map[bool]string{false: "old_still_exists", true: "old_missing"}[removeOld], func(t *testing.T) {
			d := t.TempDir()
			old, newPath := filepath.Join(d, "old.jsonl"), filepath.Join(d, "archive.jsonl")
			os.WriteFile(old, []byte(header+usageLine), 0600)
			current := old
			calls := 0
			tr := Tracked{ID: "test-session", CacheDir: d, Resolve: func() (History, error) { calls++; return History{Head: current}, nil }}
			now := time.Unix(100, 0)
			if changed, moved, err := tr.Update(context.Background(), now); err != nil || !changed || !moved {
				t.Fatalf("initial read: %v", err)
			}
			if err := tr.Save(); err != nil {
				t.Fatal(err)
			}
			oldCache := tr.CachePath
			os.WriteFile(newPath, []byte(header+strings.Replace(usageLine, `"input_tokens":100`, `"input_tokens":900`, 1)), 0600)
			current = newPath
			if removeOld {
				os.Remove(old)
			} else {
				tr.Update(context.Background(), now.Add(time.Second))
				if calls != 1 {
					t.Fatal("healthy notifications repeatedly queried the catalog")
				}
			}
			at := now.Add(31 * time.Second)
			if removeOld {
				at = now.Add(time.Second)
			}
			if changed, moved, err := tr.Update(context.Background(), at); err != nil || !changed || !moved || tr.Reader.State.Snapshot().Total != 920 || tr.CachePath == oldCache {
				t.Fatalf("relocation failed: %v", err)
			}
			if tr.Reader.State.Snapshot().Steps != 1 {
				t.Fatal("relocation added the old and new histories together")
			}
			if err := tr.Save(); err != nil {
				t.Fatal(err)
			}
			appendFile(t, newPath, strings.Replace(usageLine, `"response_id":"r"`, `"response_id":"r2"`, 1))
			if changed, moved, err := tr.Update(context.Background(), at.Add(time.Second)); err != nil || !changed || moved {
				t.Fatalf("new log stopped updating: %v", err)
			}
		})
	}
}

func TestTrackedReaderCancellationCatalogFailureAndForeignIdentity(t *testing.T) {
	d := t.TempDir()
	p := filepath.Join(d, "source")
	os.WriteFile(p, []byte(header+usageLine), 0600)
	lookupErr := errors.New("catalog busy")
	fail := true
	calls := 0
	tr := Tracked{ID: "test-session", CacheDir: d, Resolve: func() (History, error) {
		calls++
		if fail {
			return History{}, lookupErr
		}
		return History{Head: p}, nil
	}}
	now := time.Unix(100, 0)
	if _, _, err := tr.Update(context.Background(), now); !errors.Is(err, lookupErr) {
		t.Fatal("initial resolution error was lost")
	}
	fail = false
	tr.Update(context.Background(), now.Add(2*time.Second))
	tr.Save()
	fail = true
	if _, _, err := tr.Update(context.Background(), now.Add(time.Minute)); err != nil || tr.Reader.State.Snapshot().Total != 120 {
		t.Fatal("temporary catalog failure discarded readable log")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	before := calls
	if _, _, err := tr.Update(ctx, now.Add(time.Hour)); !errors.Is(err, context.Canceled) || calls != before {
		t.Fatal("cancelled reader continued resolution")
	}
	os.WriteFile(p, []byte(strings.Replace(header, "test-session", "foreign-session", 1)+usageLine), 0600)
	if _, _, err := tr.Update(context.Background(), now.Add(2*time.Minute)); !errors.Is(err, ErrChatMismatch) {
		t.Fatal("foreign session was accepted")
	}
	if err := tr.Save(); err != nil {
		t.Fatal(err)
	}
	cache, _ := os.ReadFile(tr.CachePath)
	if strings.Contains(string(cache), "foreign-session") {
		t.Fatal("foreign state was checkpointed under the selected chat")
	}
}

func TestTrackedUnindexedLogRecoversArchiveMoveAndIsolatesChats(t *testing.T) {
	home := t.TempDir()
	path := putLog(t, home, "sessions/2026/"+logName(locateChat), logHeader(locateChat)+usageLine)
	other := putLog(t, home, "sessions/2026/"+logName(locateOther), logHeader(locateOther)+strings.Replace(usageLine, `"input_tokens":100`, `"input_tokens":900`, 1))
	l := fixtureLocator{Home: home}
	now := time.Unix(100, 0)
	for _, tc := range []struct {
		id, path string
		total    int64
	}{{locateChat, path, 120}, {locateOther, other, 920}} {
		tr := Tracked{ID: tc.id, CacheDir: t.TempDir(), Resolve: func() (History, error) { return l.Resolve(context.Background(), tc.id) }}
		if _, _, err := tr.Update(context.Background(), now); err != nil || tr.Reader.Path != tc.path || tr.Reader.State.Snapshot().Total != tc.total || tr.Reader.State.Snapshot().Steps != 1 {
			t.Fatalf("chat data mixed: %s %v", tc.id, err)
		}
		tr.Reader.Close()
	}
	tr := Tracked{ID: locateChat, CacheDir: t.TempDir(), Resolve: func() (History, error) { return l.Resolve(context.Background(), locateChat) }}
	defer func() {
		if tr.Reader != nil {
			tr.Reader.Close()
		}
	}()
	if _, _, err := tr.Update(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(home, "archived_sessions", logName(locateChat))
	if err := os.MkdirAll(filepath.Dir(archive), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(path, archive); err != nil {
		t.Fatal(err)
	}
	selectFixture(home, locateChat, archive)
	if _, moved, err := tr.Update(context.Background(), now.Add(time.Second)); err != nil || !moved || tr.Reader.Path != archive || tr.Reader.State.Snapshot().Total != 120 {
		t.Fatalf("unindexed archive relocation failed: %v", err)
	}
}

func TestTrackedExplicitIdentityFailureStaysBlockedUntilSourceRecovers(t *testing.T) {
	home := t.TempDir()
	path := putLog(t, home, "sessions/"+logName(locateChat), logHeader(locateChat)+usageLine)
	l := fixtureLocator{Home: home, ActivePath: path}
	calls := 0
	tr := Tracked{ID: locateChat, CacheDir: t.TempDir(), Resolve: func() (History, error) { calls++; return l.Resolve(context.Background(), locateChat) }}
	defer func() {
		if tr.Reader != nil {
			tr.Reader.Close()
		}
	}()
	now := time.Unix(100, 0)
	if _, _, err := tr.Update(context.Background(), now); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(path, []byte(logHeader(locateOther)+usageLine), 0600)
	if _, _, err := tr.Update(context.Background(), now.Add(31*time.Second)); !errors.Is(err, ErrChatMismatch) {
		t.Fatal("identity failure hidden", err)
	}
	before := calls
	if _, _, err := tr.Update(context.Background(), now.Add(32*time.Second)); !errors.Is(err, ErrChatMismatch) || calls != before {
		t.Fatal("blocked identity resumed", err)
	}
	os.WriteFile(path, []byte(logHeader(locateChat)+usageLine), 0600)
	if _, _, err := tr.Update(context.Background(), now.Add(33*time.Second)); err != nil {
		t.Fatal("verified source did not recover", err)
	}
}

func TestTrackedMissingLogRetryAndPartialHeaderRecovery(t *testing.T) {
	home := t.TempDir()
	l := fixtureLocator{Home: home}
	calls := 0
	tr := Tracked{ID: locateChat, CacheDir: t.TempDir(), Resolve: func() (History, error) { calls++; return l.Resolve(context.Background(), locateChat) }}
	defer func() {
		if tr.Reader != nil {
			tr.Reader.Close()
		}
	}()
	now := time.Unix(100, 0)
	if _, _, err := tr.Update(context.Background(), now); !errors.Is(err, ErrLogNotFound) || tr.Reader != nil {
		t.Fatal("missing log started history reader")
	}
	partial := strings.TrimSuffix(logHeader(locateChat), "\n")
	path := putLog(t, home, "sessions/"+logName(locateChat), partial)
	if _, _, err := tr.Update(context.Background(), now.Add(time.Second)); !errors.Is(err, ErrLogNotFound) || calls != 1 {
		t.Fatal("pending discovery ignored retry delay")
	}
	if _, _, err := tr.Update(context.Background(), now.Add(2*time.Second)); !errors.Is(err, ErrLogPending) || tr.Reader != nil {
		t.Fatal("partial header treated as a verified session")
	}
	appendFile(t, path, "\n"+usageLine)
	if _, _, err := tr.Update(context.Background(), now.Add(4*time.Second)); err != nil || tr.Reader.State.Snapshot().Total != 120 {
		t.Fatalf("new log never became readable: %v", err)
	}
	if err := os.WriteFile(path, []byte(usageLine), 0600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := tr.Update(context.Background(), now.Add(5*time.Second)); !errors.Is(err, ErrLogIdentityUnverified) {
		t.Fatal("replacement without session identity was accepted")
	}
}

func TestCancellationDuringPathResolutionDoesNotStartNewReader(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	tr := Tracked{ID: "test", CacheDir: t.TempDir(), Resolve: func() (History, error) { cancel(); return History{Head: "not-opened"}, nil }}
	if _, _, err := tr.Update(ctx, time.Now()); !errors.Is(err, context.Canceled) || tr.Reader != nil {
		t.Fatal("cancelled resolution started a reader")
	}
}
