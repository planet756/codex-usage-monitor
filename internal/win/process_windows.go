package win

import (
	"errors"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

type hostProcess struct {
	PID    uint32
	Handle windows.Handle
}

// Window notifications identify the candidate directly. Ordinary app events do
// not require a full process snapshot; a backend/CLI without a main window is
// not a candidate, even if its executable has the same name.
func hostFromWindow(hwnd uintptr) hostProcess {
	var pid uint32
	call(user32, "GetWindowThreadProcessId", hwnd, uintptr(unsafe.Pointer(&pid)))
	if !eligibleHostWindow(hwnd, pid) {
		return hostProcess{}
	}
	h, err := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if err != nil {
		return hostProcess{}
	}
	var image [1024]uint16
	n := uint32(len(image))
	if windows.QueryFullProcessImageName(h, 0, &image[0], &n) == nil {
		name := filepath.Base(windows.UTF16ToString(image[:n]))
		if (strings.EqualFold(name, "ChatGPT.exe") || strings.EqualFold(name, "Codex.exe")) && isCodex(h) {
			return hostProcess{PID: pid, Handle: h}
		}
	}
	windows.CloseHandle(h)
	return hostProcess{}
}

func isCodex(h windows.Handle) bool {
	var n uint32
	r := call(kernel32, "GetPackageFamilyName", uintptr(h), uintptr(unsafe.Pointer(&n)), 0)
	if r != uintptr(windows.ERROR_INSUFFICIENT_BUFFER) || n == 0 || n > 1024 {
		return false
	}
	b := make([]uint16, n)
	return call(kernel32, "GetPackageFamilyName", uintptr(h), uintptr(unsafe.Pointer(&n)), uintptr(unsafe.Pointer(&b[0]))) == 0 && strings.HasPrefix(windows.UTF16ToString(b), "OpenAI.Codex_")
}

func findHost() (hostProcess, error) {
	var session uint32
	if err := windows.ProcessIdToSessionId(windows.GetCurrentProcessId(), &session); err != nil {
		return hostProcess{}, err
	}
	s, e := windows.CreateToolhelp32Snapshot(windows.TH32CS_SNAPPROCESS, 0)
	if e != nil {
		return hostProcess{}, e
	}
	defer windows.CloseHandle(s)
	entries := make(map[uint32]windows.ProcessEntry32)
	p := windows.ProcessEntry32{Size: uint32(unsafe.Sizeof(windows.ProcessEntry32{}))}
	for e = windows.Process32First(s, &p); e == nil; e = windows.Process32Next(s, &p) {
		name := windows.UTF16ToString(p.ExeFile[:])
		if strings.EqualFold(name, "ChatGPT.exe") || strings.EqualFold(name, "Codex.exe") {
			entries[p.ProcessID] = p
		}
	}
	if !errors.Is(e, windows.ERROR_NO_MORE_FILES) {
		return hostProcess{}, e
	}
	var fallback hostProcess
	for _, p := range entries {
		var candidateSession uint32
		if windows.ProcessIdToSessionId(p.ProcessID, &candidateSession) != nil || candidateSession != session {
			continue
		}
		name := strings.ToLower(windows.UTF16ToString(p.ExeFile[:]))
		if name != "chatgpt.exe" && name != "codex.exe" {
			continue
		}
		h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, p.ProcessID)
		if e != nil {
			continue
		}
		if !isCodex(h) {
			windows.CloseHandle(h)
			continue
		}
		// Exclude both Electron children and the packaged codex.exe backend.
		// An unpackaged CLI with the same basename is not a package parent.
		if _, ok := entries[p.ParentProcessID]; ok {
			parent, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION, false, p.ParentProcessID)
			if e == nil {
				samePackage := isCodex(parent)
				windows.CloseHandle(parent)
				if samePackage {
					windows.CloseHandle(h)
					continue
				}
			}
		}
		candidate := hostProcess{p.ProcessID, h}
		if findHostWindow(p.ProcessID) != 0 {
			if fallback.Handle != 0 {
				windows.CloseHandle(fallback.Handle)
			}
			return candidate, nil
		}
		if fallback.Handle == 0 {
			fallback = candidate
		} else {
			windows.CloseHandle(h)
		}
	}
	return fallback, nil
}

// Store callbacks globally: syscall.NewCallback allocations are not reclaimable.
// Allocating a new callback on every enumeration would leak executable memory.
var enumPID uint32
var enumBest uintptr
var enumArea int
var enumProc = enumCallback(func(h, l uintptr) uintptr {
	if !eligibleHostWindow(h, enumPID) {
		return 1
	}
	var r rect
	call(user32, "GetWindowRect", h, uintptr(unsafe.Pointer(&r)))
	area := r.width() * r.height()
	if r.width() > 400 && r.height() > 250 && area > enumArea {
		enumBest = h
		enumArea = area
	}
	return 1
})

// Called only from each process's single locked GUI thread.
func findHostWindow(pid uint32) uintptr {
	if fg := call(user32, "GetForegroundWindow"); eligibleHostWindow(fg, pid) {
		return fg
	}
	enumPID = pid
	enumBest = 0
	enumArea = 0
	call(user32, "EnumWindows", enumProc, 0)
	return enumBest
}

func eligibleHostWindow(h uintptr, pid uint32) bool {
	if h == 0 || call(user32, "IsWindowVisible", h) == 0 {
		return false
	}
	var actual uint32
	call(user32, "GetWindowThreadProcessId", h, uintptr(unsafe.Pointer(&actual)))
	if actual != pid || call(user32, "GetWindowLongPtrW", h, signed(-20))&0x80 != 0 {
		return false
	}
	if call(user32, "GetWindow", h, 4) != 0 {
		owner := call(user32, "GetWindow", h, 4)
		var ownerBounds rect
		if call(user32, "IsWindowVisible", owner) != 0 &&
			call(user32, "GetWindowRect", owner, uintptr(unsafe.Pointer(&ownerBounds))) != 0 &&
			ownerBounds.width() > 400 && ownerBounds.height() > 250 {
			return false // Owned popups do not replace a visible, full-sized parent.
		}
		var class [128]uint16
		call(user32, "GetClassNameW", h, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
		// Electron main windows can have an invisible owner. Do not reject them
		// merely because GW_OWNER is nonzero; ordinary owned dialogs stay excluded.
		if !strings.HasPrefix(windows.UTF16ToString(class[:]), "Chrome_WidgetWin") {
			return false
		}
	}
	var r rect
	return call(user32, "GetWindowRect", h, uintptr(unsafe.Pointer(&r))) != 0 && r.width() > 400 && r.height() > 250
}
