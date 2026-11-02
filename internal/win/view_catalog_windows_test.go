package win

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unsafe"

	"codex-usage-monitor/internal/metrics"
	"codex-usage-monitor/internal/rollout"
	"golang.org/x/sys/windows"
)

type catalogFixtureDB struct {
	t    *testing.T
	path string
	db   uintptr
}

func newCatalogFixture(t *testing.T, home, name string) *catalogFixtureDB {
	t.Helper()
	dir := filepath.Join(home, "sqlite")
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	d := &catalogFixtureDB{t: t, path: filepath.Join(dir, name)}
	p, _ := windows.BytePtrFromString(d.path)
	if call(sqlite, "sqlite3_open_v2", uintptr(unsafe.Pointer(p)), uintptr(unsafe.Pointer(&d.db)), 6, 0) != 0 {
		t.Fatal("create isolated catalog")
	}
	t.Cleanup(d.close)
	d.exec("CREATE TABLE local_thread_catalog(thread_id TEXT,host_id TEXT,display_title TEXT,missing_candidate INTEGER)")
	return d
}

func (d *catalogFixtureDB) exec(query string) {
	d.t.Helper()
	p, _ := windows.BytePtrFromString(query)
	if call(sqlite, "sqlite3_exec", d.db, uintptr(unsafe.Pointer(p)), 0, 0, 0) != 0 {
		d.t.Fatal("update isolated catalog")
	}
}

func (d *catalogFixtureDB) row(id, host, title string, missing int) {
	d.t.Helper()
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "''") + "'" }
	d.exec(fmt.Sprintf("INSERT INTO local_thread_catalog VALUES(%s,%s,%s,%d)", quote(id), quote(host), quote(title), missing))
}

func (d *catalogFixtureDB) close() {
	if d.db != 0 {
		call(sqlite, "sqlite3_close_v2", d.db)
		d.db = 0
	}
}

func TestNativeCatalogUsesOnlyCurrentDatabase(t *testing.T) {
	home := t.TempDir()
	current := newCatalogFixture(t, home, "codex.db")
	current.row(resolverChat, "local", "Exact title 中文", 0)
	current.close()
	legacy := newCatalogFixture(t, home, "codex-dev.db")
	legacy.row(physicalChild, "remote", "Exact title 中文", 0)
	legacy.row(physicalChild, "local", "Legacy only", 0)
	legacy.close()
	before, err := os.ReadFile(legacy.path)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 12 {
		if err := os.WriteFile(filepath.Join(home, "sqlite", fmt.Sprintf("codex-other-%d.db", i)), []byte("not a database"), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(home, "sqlite", "codex-preview.db"), 0700); err != nil {
		t.Fatal(err)
	}
	var catalog viewCatalogDB
	defer catalog.close()
	if s, issue := catalog.lookup(home, "Exact title 中文"); issue != "" || s.ID != resolverChat || s.Host != "local" || catalog.path != current.path {
		t.Fatal("other databases affected current identity", s, issue)
	}
	if s, issue := catalog.lookup(home, "Legacy only"); issue != "" || s.ID != "" {
		t.Fatal("legacy-only identity was accepted", s, issue)
	}
	if err := exclusiveSourceProbe(legacy.path); err != nil {
		t.Fatal("legacy database was opened", err)
	}
	after, err := os.ReadFile(legacy.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("legacy database changed", err)
	}
}

func TestNativeCatalogQueriesRetainExactTitlesAndFreshRows(t *testing.T) {
	home := t.TempDir()
	d := newCatalogFixture(t, home, "codex.db")
	d.exec("PRAGMA journal_mode=WAL")
	for range 4 {
		d.row(strings.ToUpper(resolverChat), "local", "Exact title", 0)
	}
	d.row(physicalChild, "local", "Exact title", 1)
	d.row(physicalChild, "local", "Other title", 0)
	var catalog viewCatalogDB
	defer catalog.close()
	if s, issue := catalog.lookup(home, "Exact title"); issue != "" || s.ID != resolverChat {
		t.Fatal("duplicates or excluded rows changed identity", s, issue)
	}
	for _, title := range []string{"Exact", "exact title", "Exact title' OR 1=1--"} {
		if s, issue := catalog.lookup(home, title); issue != "" || s.ID != "" {
			t.Fatal("title was not matched exactly", s, issue)
		}
	}
	d.row(physicalChild, "local", "After creation", 0)
	if s, issue := catalog.lookup(home, "After creation"); issue != "" || s.ID != physicalChild {
		t.Fatal("new committed WAL row did not appear", s, issue)
	}
	d.row(physicalChild, "local", "Exact title", 0)
	if s, issue := catalog.lookup(home, "Exact title"); issue != "selected_chat_ambiguous" || s.ID != "" {
		t.Fatal("cached query hid a newly ambiguous title", s, issue)
	}
}

func TestNativeCatalogFailureCannotFallBackAndRecovers(t *testing.T) {
	for _, mode := range []string{"missing", "corrupt", "schema", "invalid identity", "invalid host"} {
		t.Run(mode, func(t *testing.T) {
			home := t.TempDir()
			legacy := newCatalogFixture(t, home, "codex-dev.db")
			legacy.row(physicalChild, "local", "Saved title", 0)
			legacy.close()
			path := filepath.Join(home, "sqlite", "codex.db")
			if mode == "corrupt" {
				if err := os.WriteFile(path, []byte("invalid sqlite"), 0600); err != nil {
					t.Fatal(err)
				}
			} else if mode != "missing" {
				d := newCatalogFixture(t, home, "codex.db")
				switch mode {
				case "schema":
					d.exec("DROP TABLE local_thread_catalog")
				case "invalid identity":
					d.row("not-a-chat", "local", "Saved title", 0)
				case "invalid host":
					d.row(resolverChat, "", "Saved title", 0)
				}
				d.close()
			}
			var catalog viewCatalogDB
			defer catalog.close()
			if s, issue := catalog.lookup(home, "Saved title"); issue != "chat_catalog_unavailable" || s.ID != "" || catalog.db != 0 || catalog.stmt != 0 {
				t.Fatal("failed catalog fell back or retained resources", s, issue)
			}
			if mode == "missing" {
				if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
					t.Fatal("read-only lookup created the missing database", err)
				}
			} else if err := os.Remove(path); err != nil {
				t.Fatal("failed catalog remained pinned", err)
			}
			newCatalogFixture(t, home, "codex.db").row(resolverChat, "local", "Saved title", 0)
			if s, issue := catalog.lookup(home, "Saved title"); issue != "" || s.ID != resolverChat {
				t.Fatal("catalog did not recover without restarting", s, issue)
			}
		})
	}
}

func TestNativeCatalogConnectionReuseHomeChangeAndRelease(t *testing.T) {
	home, next := t.TempDir(), t.TempDir()
	d := newCatalogFixture(t, home, "codex.db")
	d.row(resolverChat, "local", "Saved title", 0)
	d.close()
	before, err := os.ReadFile(d.path)
	if err != nil {
		t.Fatal(err)
	}
	var catalog viewCatalogDB
	defer catalog.close()
	if s, issue := catalog.lookup(home, "Saved title"); issue != "" || s.ID != resolverChat {
		t.Fatal(s, issue)
	}
	db, stmt := catalog.db, catalog.stmt
	for range 64 {
		if s, issue := catalog.lookup(home, "Saved title"); issue != "" || s.ID != resolverChat || catalog.db != db || catalog.stmt != stmt {
			t.Fatal("idle query did not reuse its connection and statement", s, issue)
		}
	}
	other := newCatalogFixture(t, next, "codex.db")
	other.row(physicalChild, "local", "Saved title", 0)
	other.close()
	if s, issue := catalog.lookup(next, "Saved title"); issue != "" || s.ID != physicalChild || catalog.path != other.path {
		t.Fatal("home change retained the old catalog", s, issue)
	}
	if err := exclusiveSourceProbe(d.path); err != nil {
		t.Fatal("old catalog remained pinned after home change", err)
	}
	catalog.close()
	catalog.close()
	if err := exclusiveSourceProbe(other.path); err != nil {
		t.Fatal("catalog remained pinned after close", err)
	}
	after, err := os.ReadFile(d.path)
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("read-only lookup modified source data", err)
	}
}

func TestNativeCatalogWindowsPaths(t *testing.T) {
	base := t.TempDir()
	homes := []string{filepath.Join(base, "资料 数据")}
	if len(filepath.VolumeName(base)) == 2 {
		verbatim := `\\?\` + base + `\资料. `
		if err := os.Mkdir(verbatim, 0700); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if err := os.RemoveAll(verbatim); err != nil {
				t.Error(err)
			}
		})
		homes = append(homes, verbatim)
	}
	for i, home := range homes {
		t.Run(fmt.Sprint(i), func(t *testing.T) {
			d := newCatalogFixture(t, home, "codex.db")
			d.row(resolverChat, "local", "Exact title", 0)
			d.close()
			var catalog viewCatalogDB
			defer catalog.close()
			if got, issue := catalog.lookup(home, "Exact title"); issue != "" || got.ID != resolverChat {
				t.Fatal("Windows catalog path identity", got, issue)
			}
			catalog.close()
			if err := exclusiveSourceProbe(d.path); err != nil {
				t.Fatal("Windows catalog path remained pinned", err)
			}
		})
	}
}

func TestNativeCatalogRetainsIdentityAcrossReplacement(t *testing.T) {
	home := t.TempDir()
	d := newCatalogFixture(t, home, "codex.db")
	d.row(resolverChat, "local", "Saved title", 0)
	d.close()
	var catalog viewCatalogDB
	defer catalog.close()
	if s, issue := catalog.lookup(home, "Saved title"); issue != "" || s.ID != resolverChat {
		t.Fatal(s, issue)
	}
	before := catalog.file
	catalog.close()
	if err := os.Rename(d.path, d.path+".saved"); err != nil {
		t.Fatal("catalog remained pinned", err)
	}
	replacement := newCatalogFixture(t, home, "codex.db")
	replacement.row(physicalChild, "local", "Saved title", 0)
	replacement.close()
	if s, issue := catalog.lookup(home, "Saved title"); issue != "" || s.ID != physicalChild || os.SameFile(before, catalog.file) {
		t.Fatal("a replaced file inherited the original catalog identity", s, issue)
	}
}

func TestNativeCatalogPageSwitchDraftConflictAndExplicitRoute(t *testing.T) {
	root, selected := pageFixture(t)
	home := t.TempDir()
	prod := newCatalogFixture(t, home, "codex.db")
	prod.row(resolverChat, "local", "Production chat", 0)
	prod.row(physicalChild, "local", "Recent chat", 0)
	composer, document := &comObject{&fixtureRootTable[0]}, &comObject{&fixtureRootTable[0]}
	selectionFixture.classes[composer] = "ProseMirror"
	selectionFixture.runtimeIDs[composer] = []int32{42, 1}
	selectionFixture.conditionRows[composer] = []*comObject{composer}
	selectionFixture.conditionRows[root] = []*comObject{selected}
	v := viewLocator{home: home, root: root, document: document, selectedCondition: root, composerCondition: composer, composerCache: root}
	defer v.close()
	a := overlay{viewSelections: make(chan viewSelection, 1)}
	for _, tc := range []struct{ title, id string }{{"Production chat", resolverChat}, {"Recent chat", physicalChild}, {"Production chat", resolverChat}} {
		selectionFixture.names[selected] = tc.title
		got := v.locate(nil, 1, home)
		if !got.ChatPage || got.ID != tc.id {
			t.Fatal("page did not follow its catalog identity", got)
		}
		a.setView(got)
		if a.data.ID != tc.id || a.data.Snapshot.HasCounts || a.data.Snapshot.HasUsage {
			t.Fatal("page switch retained prior statistics")
		}
		a.data.Snapshot = metrics.Snapshot{HasCounts: true, Turns: 3, Steps: 10, HasUsage: true}
	}
	selectionFixture.urls[document] = "file:///app/index.html#/"
	got := v.locate(nil, 1, home)
	a.setView(got)
	if !got.ChatPage || got.ID != "" || got.Reason != "pending_chat_identity" || a.data.Snapshot.HasCounts || v.threadCatalog.db != 0 {
		t.Fatal("new draft inherited saved statistics", got)
	}
	selectionFixture.urls[document] = ""
	selectionFixture.names[selected] = "Created just now"
	if got = v.locate(nil, 1, home); got.ID != "" {
		t.Fatal("unsaved chat inherited the old identity", got)
	}
	prod.row(resolverChat, "local", "Created just now", 0)
	if got = v.locate(nil, 1, home); got.ID != resolverChat {
		t.Fatal("first saved message required restarting the monitor", got)
	}
	prod.row(physicalChild, "local", "Created just now", 0)
	for range 3 {
		if got = v.locate(nil, 1, home); got.ID != "" || got.Reason != "selected_chat_ambiguous" {
			t.Fatal("editor continuity hid a conflicting title", got)
		}
	}
	selectionFixture.urls[document] = "file:///app/index.html#/local/" + resolverChat + "?hostId=local"
	if got = v.locate(nil, 1, home); got.ID != resolverChat || got.Source != "uia_document_url" {
		t.Fatal("an explicit page ID was blocked by optional catalog titles", got)
	}
	a.setView(got)
	a.data.Snapshot = metrics.Snapshot{HasCounts: true, Turns: 3, Steps: 10, HasUsage: true}
	selectionFixture.urls[document] = ""
	prod.exec("DROP TABLE local_thread_catalog")
	if got = v.locate(nil, 1, home); got.ID != "" || got.Reason != "chat_catalog_unavailable" {
		t.Fatal("failed catalog query reused the previous page binding", got)
	}
	a.setView(got)
	if a.data.Snapshot.HasCounts || a.data.Snapshot.HasUsage {
		t.Fatal("failed catalog query retained the previous statistics")
	}
	selectionFixture.urls[document] = "file:///app/index.html#/plugins"
	if got = v.locate(nil, 1, home); got.ChatPage || got.ID != "" || v.threadCatalog.db != 0 {
		t.Fatal("application page retained the chat", got)
	}
}

func TestNativeCatalogOldChatKeepsVerifiedHistory(t *testing.T) {
	home := t.TempDir()
	revoked := nativeUsage(t, 2, "revoked", "revoked-response", 99999, 100004)
	_, data := nativeLog(t, home, resolverChat, resolverChat, 0, nil, nativeUsage(t, 1, "old", "old-response", 5, 5), revoked)
	cut := int64(len(data) - len(revoked))
	ref := map[string]any{"thread_id": resolverChat, "end_ordinal_exclusive": int64(2), "end_byte_offset": cut}
	head, _ := nativeLog(t, home, resolverChat, physicalChild, 2, ref, nativeUsage(t, 3, "new", "new-response", 3, 8))
	// The current directory is created after the old log and replacement branch.
	newCatalogFixture(t, home, "codex.db").row(resolverChat, "local", "Old conversation", 0)
	var catalog viewCatalogDB
	defer catalog.close()
	s, issue := catalog.lookup(home, "Old conversation")
	if issue != "" || s.ID != resolverChat {
		t.Fatal("old chat missing from current catalog", s, issue)
	}
	h, _, err := resolveActiveThread(context.Background(), home, s.Host, s.ID, head, nil)
	if err != nil || len(h.Segments) != 2 || h.Segments[0].Bytes != cut {
		t.Fatal("verified historical branch was lost", err)
	}
	r := rollout.NewHistory(h)
	defer r.Close()
	if _, err := r.Update(context.Background()); err != nil {
		t.Fatal(err)
	}
	x := r.State.Snapshot()
	if x.Turns != 2 || x.Steps != 2 || x.Usage.Output != 8 || !x.CountsComplete || !x.HistoryComplete {
		t.Fatal("old history was lost or a reverted response was included")
	}
	if _, _, err := resolveActiveThread(context.Background(), home, s.Host, physicalChild, head, nil); !errors.Is(err, rollout.ErrChatMismatch) {
		t.Fatal("catalog identity did not constrain selected log", err)
	}
}
