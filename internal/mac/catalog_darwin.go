package mac

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"unsafe"

	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/monitor"
	"github.com/ebitengine/purego"
)

type sqliteAPI struct {
	open     func(string, *uintptr, int32, uintptr) int32
	closeDB  func(uintptr) int32
	prepare  func(uintptr, string, int32, *uintptr, *uintptr) int32
	finalize func(uintptr) int32
	bind     func(uintptr, int32, string, int32, uintptr) int32
	step     func(uintptr) int32
	text     func(uintptr, int32) uintptr
	bytes    func(uintptr, int32) int32
	busy     func(uintptr, int32) int32
}

var loadSQLite = sync.OnceValues(func() (*sqliteAPI, error) {
	lib, err := purego.Dlopen("/usr/lib/libsqlite3.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
	if err != nil {
		return nil, err
	}
	a := &sqliteAPI{}
	for _, b := range []struct {
		to   any
		name string
	}{{&a.open, "sqlite3_open_v2"}, {&a.closeDB, "sqlite3_close_v2"}, {&a.prepare, "sqlite3_prepare_v2"}, {&a.finalize, "sqlite3_finalize"}, {&a.bind, "sqlite3_bind_text"}, {&a.step, "sqlite3_step"}, {&a.text, "sqlite3_column_text"}, {&a.bytes, "sqlite3_column_bytes"}, {&a.busy, "sqlite3_busy_timeout"}} {
		purego.RegisterLibFunc(b.to, lib, b.name)
	}
	return a, nil
})

func (a *sqliteAPI) columnText(stmt uintptr, col int32, limit int32) string {
	p := a.text(stmt, col)
	n := a.bytes(stmt, col)
	if p == 0 || n <= 0 || n > limit {
		return ""
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(p)), int(n)))
}

func indexedRolloutPath(ctx context.Context, home, host, id string) (string, error) {
	return monitor.IndexedRolloutPath(ctx, home, host, id, readIndexText)
}

func readIndexText(path, statement, key string) (string, error) {
	unavailable := errors.New("read Codex index")
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return "", unavailable
	}
	a, err := loadSQLite()
	if err != nil {
		return "", err
	}
	var db uintptr
	code := a.open(path, &db, 1, 0) // SQLITE_OPEN_READONLY
	if db != 0 {
		defer a.closeDB(db)
	}
	if code != 0 {
		return "", unavailable
	}
	a.busy(db, 25)
	var stmt uintptr
	if a.prepare(db, statement, -1, &stmt, nil) != 0 {
		return "", unavailable
	}
	defer a.finalize(stmt)
	if a.bind(stmt, 1, key, -1, ^uintptr(0)) != 0 {
		return "", unavailable
	}
	switch a.step(stmt) {
	case 101: // SQLITE_DONE
		return "", nil
	case 100: // SQLITE_ROW
		result := a.columnText(stmt, 0, 32<<10)
		if a.step(stmt) == 101 {
			return result, nil
		}
	}
	return "", unavailable
}

type catalogDB struct {
	*sqliteAPI
	db   uintptr
	file os.FileInfo
}

func (d *catalogDB) close() {
	if d.db != 0 {
		d.closeDB(d.db)
		d.db = 0
	}
	d.file = nil
}
func (d *catalogDB) lookup(home, title string) (ipc.Selection, string) {
	if ipc.PlaceholderChatTitle(title) {
		return ipc.Selection{}, ""
	}
	p := filepath.Join(home, "sqlite", "codex.db")
	info, err := os.Stat(p)
	if err != nil || !info.Mode().IsRegular() {
		d.close()
		return ipc.Selection{}, "chat_catalog_unavailable"
	}
	if d.file != nil && !os.SameFile(d.file, info) {
		d.close()
	}
	if d.sqliteAPI == nil {
		api, e := loadSQLite()
		if e != nil {
			return ipc.Selection{}, "chat_catalog_unavailable"
		}
		d.sqliteAPI = api
	}
	if d.db == 0 {
		if d.open(p, &d.db, 1, 0) != 0 {
			d.close()
			return ipc.Selection{}, "chat_catalog_unavailable"
		}
		d.file = info
		d.busy(d.db, 25)
	}
	var stmt uintptr
	if d.prepare(d.db, "SELECT DISTINCT lower(thread_id),host_id FROM local_thread_catalog WHERE display_title=?1 COLLATE BINARY AND missing_candidate=0 LIMIT 2", -1, &stmt, nil) != 0 {
		return ipc.Selection{}, "chat_catalog_unavailable"
	}
	defer d.finalize(stmt)
	if d.bind(stmt, 1, title, -1, ^uintptr(0)) != 0 {
		return ipc.Selection{}, "chat_catalog_unavailable"
	}
	var candidates ipc.ViewCandidates
	for {
		code := d.step(stmt)
		if code == 101 {
			break
		}
		if code != 100 {
			return ipc.Selection{}, "chat_catalog_unavailable"
		}
		s := ipc.Selection{ID: d.columnText(stmt, 0, 4096), Host: d.columnText(stmt, 1, 4096), Source: "ax_catalog_title"}
		if !ipc.ValidThreadID(s.ID) || s.Host == "" {
			return ipc.Selection{}, "chat_catalog_unavailable"
		}
		candidates.Add(s)
	}
	current, err := os.Stat(p)
	if err != nil || !os.SameFile(info, current) {
		d.close()
		return ipc.Selection{}, "chat_catalog_unavailable"
	}
	s, ambiguous := candidates.Result(true)
	if ambiguous {
		return ipc.Selection{}, "selected_chat_ambiguous"
	}
	return s, ""
}
