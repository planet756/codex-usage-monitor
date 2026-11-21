package win

import (
	"context"
	"errors"
	"os"
	"unsafe"

	"codex-usage-monitor/internal/monitor"
	"golang.org/x/sys/windows"
)

var sqlite = windows.NewLazySystemDLL("winsqlite3.dll")

func indexedRolloutPath(ctx context.Context, home, host, id string) (string, error) {
	return monitor.IndexedRolloutPath(ctx, home, host, id, readIndexText)
}

func readIndexText(path, statement, key string) (string, error) {
	unavailable := errors.New("read Codex index")
	if info, err := os.Stat(path); err != nil || !info.Mode().IsRegular() {
		return "", unavailable
	}
	filename, err := windows.BytePtrFromString(path)
	if err != nil || nativeProc(sqlite, "sqlite3_open_v2").Find() != nil {
		return "", unavailable
	}
	var db uintptr
	code := call(sqlite, "sqlite3_open_v2", uintptr(unsafe.Pointer(filename)), uintptr(unsafe.Pointer(&db)), 1, 0) // SQLITE_OPEN_READONLY
	if db != 0 {
		defer call(sqlite, "sqlite3_close_v2", db)
	}
	if code != 0 {
		return "", unavailable
	}
	call(sqlite, "sqlite3_busy_timeout", db, 25)
	var stmt uintptr
	query, _ := windows.BytePtrFromString(statement)
	if call(sqlite, "sqlite3_prepare_v2", db, uintptr(unsafe.Pointer(query)), signed(-1), uintptr(unsafe.Pointer(&stmt)), 0) != 0 {
		return "", unavailable
	}
	defer call(sqlite, "sqlite3_finalize", stmt)
	value, err := windows.BytePtrFromString(key)
	if err != nil || call(sqlite, "sqlite3_bind_text", stmt, 1, uintptr(unsafe.Pointer(value)), signed(-1), ^uintptr(0)) != 0 {
		return "", unavailable
	}
	switch call(sqlite, "sqlite3_step", stmt) {
	case 101: // SQLITE_DONE
		return "", nil
	case 100: // SQLITE_ROW
		result := sqlText(stmt, 0)
		if call(sqlite, "sqlite3_step", stmt) == 101 {
			return result, nil
		}
	}
	return "", unavailable
}

func sqlText(stmt uintptr, col int) string {
	p := call(sqlite, "sqlite3_column_text", stmt, uintptr(col))
	if p == 0 {
		return ""
	}
	n := call(sqlite, "sqlite3_column_bytes", stmt, uintptr(col))
	if n > 1<<20 {
		return ""
	}
	return string(unsafe.Slice((*byte)(unsafe.Pointer(p)), int(n)))
}
