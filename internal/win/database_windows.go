package win

import (
	"unsafe"

	"golang.org/x/sys/windows"
)

var sqlite = windows.NewLazySystemDLL("winsqlite3.dll")

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
