package win

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

func PrepareConsole() {
	call(kernel32, "AttachConsole", ^uintptr(0))
	if h, e := windows.GetStdHandle(windows.STD_OUTPUT_HANDLE); e == nil && h != 0 && h != windows.InvalidHandle {
		os.Stdout = os.NewFile(uintptr(h), "stdout")
	}
	if h, e := windows.GetStdHandle(windows.STD_ERROR_HANDLE); e == nil && h != 0 && h != windows.InvalidHandle {
		os.Stderr = os.NewFile(uintptr(h), "stderr")
	}
}
func ShowError(text string) { messageBox("Codex Usage Monitor", text) }

type memoryCounters struct {
	Size, Faults                                                                                          uint32
	PeakWorkingSet, WorkingSet, PeakPaged, Paged, PeakNonPaged, NonPaged, Pagefile, PeakPagefile, Private uintptr
}
type Resources struct {
	PID           uint32 `json:"pid"`
	WorkingSet    uint64 `json:"working_set_bytes"`
	PrivateCommit uint64 `json:"private_commit_bytes"`
	GDI           uint32 `json:"gdi_objects"`
	USER          uint32 `json:"user_objects"`
}

func Diagnostics() ([]Resources, error) {
	exe, e := os.Executable()
	if e != nil {
		return nil, e
	}
	exe, e = filepath.Abs(exe)
	if e != nil {
		return nil, e
	}
	s, e := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if e != nil {
		return nil, e
	}
	defer windows.CloseHandle(s)
	var out []Resources
	p := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for e = windows.Process32First(s, &p); e == nil; e = windows.Process32Next(s, &p) {
		if p.ProcessID == windows.GetCurrentProcessId() || !strings.EqualFold(windows.UTF16ToString(p.ExeFile[:]), filepath.Base(exe)) {
			continue
		}
		h, e := windows.OpenProcess(windows.PROCESS_QUERY_INFORMATION|windows.PROCESS_VM_READ, false, p.ProcessID)
		if e != nil {
			continue
		}
		var name [32768]uint16
		n := uint32(len(name))
		if windows.QueryFullProcessImageName(h, 0, &name[0], &n) != nil || !strings.EqualFold(windows.UTF16ToString(name[:n]), exe) {
			windows.CloseHandle(h)
			continue
		}
		m := memoryCounters{Size: uint32(unsafe.Sizeof(memoryCounters{}))}
		if call(kernel32, "K32GetProcessMemoryInfo", uintptr(h), uintptr(unsafe.Pointer(&m)), uintptr(m.Size)) == 0 {
			windows.CloseHandle(h)
			return nil, fmt.Errorf("GetProcessMemoryInfo failed")
		}
		out = append(out, Resources{PID: p.ProcessID, WorkingSet: uint64(m.WorkingSet), PrivateCommit: uint64(m.Private), GDI: uint32(call(user32, "GetGuiResources", uintptr(h), 0)), USER: uint32(call(user32, "GetGuiResources", uintptr(h), 1))})
		windows.CloseHandle(h)
	}
	return out, nil
}
