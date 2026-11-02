package win

import (
	"codex-usage-monitor/internal/ipc"
	"codex-usage-monitor/internal/source"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

type viewCatalogDB struct {
	db, stmt uintptr
	path     string
	file     os.FileInfo
}

func (d *viewCatalogDB) close() {
	if d.stmt != 0 {
		call(sqlite, "sqlite3_finalize", d.stmt)
	}
	if d.db != 0 {
		call(sqlite, "sqlite3_close_v2", d.db)
	}
	*d = viewCatalogDB{}
}

func (d *viewCatalogDB) query(path, name string, info os.FileInfo) (ipc.Selection, string) {
	fail := func() (ipc.Selection, string) {
		d.close()
		return ipc.Selection{}, "chat_catalog_unavailable"
	}
	if d.db == 0 {
		value, err := windows.BytePtrFromString(path)
		if err != nil || nativeProc(sqlite, "sqlite3_open_v2").Find() != nil {
			return fail()
		}
		// SQLITE_OPEN_READONLY: discovery must not create a missing database.
		if call(sqlite, "sqlite3_open_v2", uintptr(unsafe.Pointer(value)), uintptr(unsafe.Pointer(&d.db)), 1, 0) != 0 {
			return fail()
		}
		d.path, d.file = path, info
		call(sqlite, "sqlite3_busy_timeout", d.db, 25)
	}
	if d.stmt == 0 {
		query, _ := windows.BytePtrFromString("SELECT DISTINCT lower(thread_id),host_id FROM local_thread_catalog WHERE display_title=?1 COLLATE BINARY AND missing_candidate=0 LIMIT 2")
		if call(sqlite, "sqlite3_prepare_v2", d.db, uintptr(unsafe.Pointer(query)), signed(-1), uintptr(unsafe.Pointer(&d.stmt)), 0) != 0 {
			return fail()
		}
	}
	stmt := d.stmt
	defer func() { call(sqlite, "sqlite3_reset", stmt); call(sqlite, "sqlite3_clear_bindings", stmt) }()
	label, err := windows.BytePtrFromString(name)
	if err != nil || call(sqlite, "sqlite3_bind_text", stmt, 1, uintptr(unsafe.Pointer(label)), signed(-1), ^uintptr(0)) != 0 {
		return ipc.Selection{}, "chat_catalog_unavailable"
	}
	var candidates ipc.ViewCandidates
	for {
		code := call(sqlite, "sqlite3_step", stmt)
		if code == 101 {
			selected, _ := candidates.Result(true)
			return selected, ""
		}
		if code != 100 {
			return ipc.Selection{}, "chat_catalog_unavailable"
		}
		candidate := ipc.Selection{ID: sqlText(stmt, 0), Host: sqlText(stmt, 1)}
		if !ipc.ValidThreadID(candidate.ID) || candidate.Host == "" {
			return ipc.Selection{}, "chat_catalog_unavailable"
		}
		if candidates.Add(candidate) {
			return ipc.Selection{}, "selected_chat_ambiguous"
		}
	}
}

// Only the current Desktop catalog establishes title identity. Missing or
// unreadable metadata must never fall back to a database from an older build.
func (d *viewCatalogDB) lookup(home, name string) (ipc.Selection, string) {
	path := source.JoinPath(home, "sqlite", "codex.db")
	info, err := os.Stat(path)
	if err != nil || !info.Mode().IsRegular() {
		d.close()
		return ipc.Selection{}, "chat_catalog_unavailable"
	}
	// Windows Stat can defer loading the file ID. Capture it before opening
	// SQLite so a later replacement cannot change this observation's identity.
	if !os.SameFile(info, info) {
		d.close()
		return ipc.Selection{}, "chat_catalog_unavailable"
	}
	if d.path != path || d.file != nil && !os.SameFile(d.file, info) {
		d.close()
	}
	s, issue := d.query(path, name, info)
	if issue != "" {
		d.close()
		return ipc.Selection{}, issue
	}
	current, err := os.Stat(path)
	if err != nil || !os.SameFile(info, current) {
		d.close()
		return ipc.Selection{}, "chat_catalog_unavailable"
	}
	return s, ""
}

func (v *viewLocator) catalogSelection(name string) ipc.Selection {
	if ipc.PlaceholderChatTitle(name) || v.home == "" {
		return ipc.Selection{}
	}
	s, issue := v.threadCatalog.lookup(v.home, name)
	if issue != "" && v.catalogIssue != "selected_chat_ambiguous" {
		v.catalogIssue = issue
	}
	return s
}
