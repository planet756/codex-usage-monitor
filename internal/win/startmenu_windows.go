package win

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"unsafe"

	"codex-usage-monitor/internal/storage"
	"golang.org/x/sys/windows"
)

const startMenuName = "Codex Usage Monitor.lnk"

var shell32 = windows.NewLazySystemDLL("shell32.dll")
var shellLinkCLSID = windows.GUID{Data1: 0x00021401, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
var shellLinkIID = windows.GUID{Data1: 0x000214f9, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}
var persistFileIID = windows.GUID{Data1: 0x0000010b, Data4: [8]byte{0xc0, 0, 0, 0, 0, 0, 0, 0x46}}

func shellApartment(action func() error) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if hr := int32(call(ole32, "CoInitializeEx", 0, 2)); hr < 0 {
		return fmt.Errorf("initialize shortcut COM: 0x%08x", uint32(hr))
	}
	defer call(ole32, "CoUninitialize")
	return action()
}

// StartMenu touches only this application's current-user shortcut. It does not
// change sign-in startup or launch Codex. Automatic registration is manual-only.
func StartMenu(enable bool, configPath string) error {
	return shellApartment(func() error {
		flags := uint32(windows.KF_FLAG_DONT_VERIFY)
		if enable {
			flags = windows.KF_FLAG_CREATE
		}
		programs, err := windows.KnownFolderPath(windows.FOLDERID_Programs, flags)
		if err != nil {
			return err
		}
		path := filepath.Join(programs, startMenuName)
		if !enable {
			if err := windows.DeleteFile(utf(path)); err != nil {
				if errors.Is(err, os.ErrNotExist) {
					return nil
				}
				return err
			}
			call(shell32, "SHChangeNotify", 4, 0x2005, uintptr(unsafe.Pointer(utf(path))), 0)
			return nil
		}
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		configPath, err = filepath.Abs(configPath)
		if err != nil {
			return err
		}
		return ensureStartMenuLink(path, exe, configPath)
	})
}

func newShellLink() (*comObject, *comObject, error) {
	var link, persist *comObject
	hr := int32(call(ole32, "CoCreateInstance", uintptr(unsafe.Pointer(&shellLinkCLSID)), 0, 1,
		uintptr(unsafe.Pointer(&shellLinkIID)), uintptr(unsafe.Pointer(&link))))
	if hr < 0 || link == nil {
		if link != nil {
			comCall(link, 2)
		}
		return nil, nil, fmt.Errorf("create Shell Link: 0x%08x", uint32(hr))
	}
	hr = int32(comCall(link, 0, uintptr(unsafe.Pointer(&persistFileIID)), uintptr(unsafe.Pointer(&persist))))
	if hr < 0 || persist == nil {
		if persist != nil {
			comCall(persist, 2)
		}
		comCall(link, 2)
		return nil, nil, fmt.Errorf("query shortcut persistence: 0x%08x", uint32(hr))
	}
	return link, persist, nil
}

// A shell link can expand an 8.3 target into a longer path when loaded. Read at
// least MAX_PATH characters and one beyond the expected value so that neither
// expansion nor truncation can make a different target appear to match.
// Slots follow IShellLinkW / IPersistFile in the Win32 SDK headers.
func shellLinkField(link *comObject, slot int, expected string) (string, bool) {
	buf := make([]uint16, max(260, len(windows.StringToUTF16(expected))+1))
	var icon int32
	var hr uintptr
	switch slot {
	case 3:
		hr = comCall(link, slot, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), 0, 4)
	case 16:
		hr = comCall(link, slot, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)), uintptr(unsafe.Pointer(&icon)))
	default:
		hr = comCall(link, slot, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf)))
	}
	if int32(hr) < 0 || slot == 16 && icon != 0 {
		return "", false
	}
	return windows.UTF16ToString(buf), true
}

func sameShortcutPath(actual, expected string) bool {
	if strings.EqualFold(actual, expected) {
		return true
	}
	// Shell links may resolve a junction or volume alias while loading. Accept
	// only another name for the same existing file or directory.
	a, aerr := os.Stat(actual)
	b, berr := os.Stat(expected)
	return aerr == nil && berr == nil && os.SameFile(a, b)
}

func startMenuLinkStatus(path, exe, args string) (bool, string) {
	link, persist, err := newShellLink()
	if err != nil {
		return false, err.Error()
	}
	defer comCall(link, 2)
	defer comCall(persist, 2)
	if hr := int32(comCall(persist, 5, uintptr(unsafe.Pointer(utf(path))), 0)); hr < 0 {
		return false, fmt.Sprintf("load shortcut: 0x%08x", uint32(hr))
	}
	for _, field := range []struct {
		name     string
		slot     int
		expected string
		path     bool
	}{
		{"target", 3, exe, true}, {"working directory", 8, filepath.Dir(exe), true},
		{"arguments", 10, args, false}, {"icon", 16, exe, true},
	} {
		value, ok := shellLinkField(link, field.slot, field.expected)
		if !ok || field.path && !sameShortcutPath(value, field.expected) || !field.path && value != field.expected {
			return false, fmt.Sprintf("%s: got %q (readable=%v), want %q", field.name, value, ok, field.expected)
		}
	}
	return true, ""
}

func startMenuLinkMatches(path, exe, args string) bool {
	ok, _ := startMenuLinkStatus(path, exe, args)
	return ok
}

// The caller owns a Shell apartment. Existing correct links are not rewritten;
// repairs are saved beside the final link and then atomically replace it.
func ensureStartMenuLink(path, exe, configPath string) error {
	for _, s := range []string{path, exe, configPath} {
		if strings.ContainsAny(s, "\x00\r\n\"") {
			return errors.New("invalid shortcut path")
		}
	}
	args := "--config " + windows.EscapeArg(configPath)
	if startMenuLinkMatches(path, exe, args) {
		return nil
	}
	link, persist, err := newShellLink()
	if err != nil {
		return err
	}
	var temp string
	defer func() {
		if persist != nil {
			comCall(persist, 2)
		}
		if link != nil {
			comCall(link, 2)
		}
		if temp != "" {
			os.Remove(temp)
		}
	}()
	for _, field := range []struct {
		slot  int
		value string
	}{
		{20, exe}, {9, filepath.Dir(exe)}, {11, args}, {7, "Current-chat usage statistics for Codex Desktop"},
	} {
		if hr := int32(comCall(link, field.slot, uintptr(unsafe.Pointer(utf(field.value))))); hr < 0 {
			return fmt.Errorf("set shortcut field: 0x%08x", uint32(hr))
		}
	}
	if hr := int32(comCall(link, 17, uintptr(unsafe.Pointer(utf(exe))), 0)); hr < 0 {
		return fmt.Errorf("set shortcut icon: 0x%08x", uint32(hr))
	}
	if hr := int32(comCall(link, 15, 1)); hr < 0 {
		return fmt.Errorf("set shortcut window state: 0x%08x", uint32(hr))
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".cum-menu-*.tmp")
	if err != nil {
		return err
	}
	temp = f.Name()
	if err := f.Close(); err != nil {
		return err
	}
	if hr := int32(comCall(persist, 6, uintptr(unsafe.Pointer(utf(temp))), 1)); hr < 0 {
		return fmt.Errorf("save shortcut: 0x%08x", uint32(hr))
	}
	// Drop any implementation-owned file references before replacing the link.
	comCall(persist, 2)
	persist = nil
	comCall(link, 2)
	link = nil
	_, existed := os.Stat(path)
	if err := storage.Replace(temp, path); err != nil {
		return err
	}
	event := uintptr(2)
	if existed == nil {
		event = 0x2000
	}
	call(shell32, "SHChangeNotify", event, 0x2005, uintptr(unsafe.Pointer(utf(path))), 0)
	return nil
}
