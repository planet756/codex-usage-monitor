package mac

import (
	"os"
	"path/filepath"
	"unsafe"

	"codex-usage-monitor/internal/ipc"
	"github.com/ebitengine/purego"
)

type catalogDB struct {
	db       uintptr
	file     os.FileInfo
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
	if d.open == nil {
		lib, e := purego.Dlopen("/usr/lib/libsqlite3.dylib", purego.RTLD_NOW|purego.RTLD_LOCAL)
		if e != nil {
			return ipc.Selection{}, "chat_catalog_unavailable"
		}
		for _, b := range []struct {
			to   any
			name string
		}{{&d.open, "sqlite3_open_v2"}, {&d.closeDB, "sqlite3_close_v2"}, {&d.prepare, "sqlite3_prepare_v2"}, {&d.finalize, "sqlite3_finalize"}, {&d.bind, "sqlite3_bind_text"}, {&d.step, "sqlite3_step"}, {&d.text, "sqlite3_column_text"}, {&d.bytes, "sqlite3_column_bytes"}, {&d.busy, "sqlite3_busy_timeout"}} {
			purego.RegisterLibFunc(b.to, lib, b.name)
		}
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
		column := func(col int32) string {
			p := d.text(stmt, col)
			n := d.bytes(stmt, col)
			if p == 0 || n < 0 || n > 4096 {
				return ""
			}
			return string(unsafe.Slice((*byte)(unsafe.Pointer(p)), int(n)))
		}
		s := ipc.Selection{ID: column(0), Host: column(1), Source: "ax_catalog_title"}
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
