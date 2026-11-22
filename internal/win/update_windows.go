package win

import (
	"archive/zip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/i18n"
	"codex-usage-monitor/internal/releasecheck"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

const (
	updateMutex  = `Local\CodexUsageMonitor.Update`
	monitorEXE   = "CodexUsageMonitor.exe"
	uninstallKey = `Software\Microsoft\Windows\CurrentVersion\Uninstall\CodexUsageMonitor`
)

func updateCacheDir() (string, error) {
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "CodexUsageMonitor", "updates"), nil
}

func updateInProgress() bool {
	h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, utf(updateMutex))
	if err == nil {
		windows.CloseHandle(h)
	}
	return err == nil || errors.Is(err, windows.ERROR_ACCESS_DENIED)
}

func startUpdate(tag, configPath string) error {
	target, err := os.Executable()
	if err != nil {
		return err
	}
	target, err = filepath.EvalSymlinks(target)
	if err != nil {
		return err
	}
	dir, err := updateCacheDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// Windows can keep a running EXE mapped until exit. Reap old helper copies
	// here if their own cleanup could not remove the running image.
	old, _ := filepath.Glob(filepath.Join(dir, "updater-*.exe"))
	for _, path := range old {
		if info, err := os.Stat(path); err == nil && time.Since(info.ModTime()) > 24*time.Hour {
			_ = os.Remove(path)
		}
	}
	source, err := os.Open(target)
	if err != nil {
		return err
	}
	defer source.Close()
	helper, err := writeUpdateExecutable(dir, "updater-*.exe", source)
	if err != nil {
		return err
	}
	cmd := exec.Command(helper, "--update-target", target, "--update-version", tag, "--config", configPath)
	cmd.Dir = dir
	if err := cmd.Start(); err != nil {
		_ = os.Remove(helper)
		return err
	}
	// The updater must outlive the watcher and the overlay that requested it.
	_ = cmd.Process.Release()
	return nil
}

func RunUpdate(current, tag, target, configPath string) error {
	if !filepath.IsAbs(target) || !releasecheck.WindowsUpgrade(current, tag) {
		return errors.New("the updater requires an absolute executable path and a newer stable release")
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if sameShortcutPath(self, target) {
		return errors.New("the updater must run from a separate executable")
	}
	if dir, err := updateCacheDir(); err == nil && sameShortcutPath(filepath.Dir(self), dir) && strings.HasPrefix(filepath.Base(self), "updater-") {
		defer os.Remove(self)
	}
	h, err := windows.CreateMutex(nil, false, utf(updateMutex))
	if errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(h)
		return nil
	}
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if err := applyUpdate(current, tag, target, configPath); err != nil {
		showUpdateFailure(cfg.Language, err)
		return nil
	}
	title := i18n.Text(cfg.Language, "Software Update")
	if confirmUpdate(0, title, i18n.Format(cfg.Language, "%s was installed. Start Codex Usage Monitor now?", tag)) {
		if err := launchUpdatedMonitor(target, configPath); err != nil {
			messageBox(title, i18n.Text(cfg.Language, "Could not launch Codex Usage Monitor.")+"\n\n"+err.Error())
		}
	}
	return nil
}

func launchUpdatedMonitor(target, configPath string) error {
	cmd := exec.Command(target, "--watcher", "--config", configPath)
	cmd.Dir = filepath.Dir(target)
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return nil
}

func applyUpdate(current, tag, target, configPath string) (err error) {
	scope, err := installedScope(target)
	if err != nil {
		return err
	}
	dir, err := os.MkdirTemp("", "CodexUsageMonitor-update-")
	if err != nil {
		return err
	}
	defer os.Remove(dir)
	asset, err := releasecheck.DownloadWindows(context.Background(), current, tag, dir, scope == "")
	if err != nil {
		return err
	}
	defer os.Remove(asset.Path)
	// Hold the verified bytes open against writes and deletion until setup or
	// extraction is done, including while a user responds to Windows UAC.
	locked, err := windows.CreateFile(utf(asset.Path), windows.GENERIC_READ, windows.FILE_SHARE_READ, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(locked), asset.Path)
	defer file.Close()
	if err := asset.Verify(file); err != nil {
		return err
	}
	var staged string
	if scope == "" {
		staged, err = stagePortableExecutable(file, filepath.Dir(target))
		if err != nil {
			return err
		}
		defer os.Remove(staged)
		if err := verifyUpdateVersion(staged, tag); err != nil {
			return err
		}
	}
	if err := verifyUpdateVersion(target, current); err != nil {
		return fmt.Errorf("the current executable changed: %w", err)
	}
	// Stop under the original user token before setup can run as another user.
	stopped, err := stopForUpdate()
	if err != nil {
		return err
	}
	if stopped {
		defer func() {
			if err != nil && verifyUpdateVersion(target, current) == nil {
				err = errors.Join(err, launchUpdatedMonitor(target, configPath))
			}
		}()
	}
	if staged != "" {
		return replacePortableExecutable(staged, target)
	}
	if err := runUpdateSetup(asset.Path, scope); err != nil {
		return err
	}
	return verifyUpdateVersion(target, tag)
}

func installedScope(target string) (string, error) {
	for _, entry := range []struct {
		key   registry.Key
		scope string
	}{{registry.CURRENT_USER, "CurrentUser"}, {registry.LOCAL_MACHINE, "AllUsers"}} {
		key, err := registry.OpenKey(entry.key, uninstallKey, registry.QUERY_VALUE|registry.WOW64_64KEY)
		if errors.Is(err, registry.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		dir, _, err := key.GetStringValue("InstallLocation")
		key.Close()
		if errors.Is(err, registry.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(dir) && sameShortcutPath(target, filepath.Join(dir, monitorEXE)) {
			return entry.scope, nil
		}
	}
	return "", nil
}

func verifyUpdateVersion(path, version string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "--version")
	cmd.Dir = filepath.Dir(path)
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: windows.CREATE_NO_WINDOW}
	output, err := cmd.Output()
	if err != nil {
		return fmt.Errorf("read executable version: %w", err)
	}
	if strings.TrimSpace(string(output)) != "Codex Usage Monitor "+strings.TrimPrefix(version, "v") {
		return fmt.Errorf("the executable did not report version %s", version)
	}
	return nil
}

func runUpdateSetup(path, scope string) error {
	if scope == "CurrentUser" {
		cmd := exec.Command(path, "/S", "/CurrentUser")
		cmd.Dir = filepath.Dir(path)
		return cmd.Run()
	}
	workingDir, err := windows.GetSystemDirectory()
	if err != nil {
		return err
	}
	// Wait for the elevated process itself so UAC cancellation and installer
	// errors cannot be mistaken for a successful launcher exit.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if hr := int32(call(ole32, "CoInitializeEx", 0, 2|4)); hr < 0 {
		return fmt.Errorf("initialize installer shell: %#x", uint32(hr))
	}
	defer call(ole32, "CoUninitialize")
	type shellExecuteInfo struct {
		size, mask            uint32
		hwnd                  uintptr
		verb, file, args, dir *uint16
		show                  int32
		instance, idList      uintptr
		class                 *uint16
		classKey              uintptr
		hotKey                uint32
		icon                  uintptr
		process               windows.Handle
	}
	const waitForLaunch = 0x100
	const returnProcess = 0x40
	const noShellErrorDialog = 0x400
	info := shellExecuteInfo{mask: waitForLaunch | returnProcess | noShellErrorDialog,
		verb: utf("runas"), file: utf(path), args: utf("/S /AllUsers"), dir: utf(workingDir), show: 1}
	info.size = uint32(unsafe.Sizeof(info))
	result, err := callWithError(shell32, "ShellExecuteExW", uintptr(unsafe.Pointer(&info)))
	if result == 0 {
		return fmt.Errorf("start all-users setup: %w", err)
	}
	if info.process == 0 {
		return errors.New("setup did not return a process handle")
	}
	defer windows.CloseHandle(info.process)
	if _, err := windows.WaitForSingleObject(info.process, windows.INFINITE); err != nil {
		return err
	}
	var exit uint32
	if err := windows.GetExitCodeProcess(info.process, &exit); err != nil {
		return err
	}
	if exit != 0 {
		return fmt.Errorf("setup exited with code %d", exit)
	}
	return nil
}

func stopForUpdate() (bool, error) {
	deadline := time.Now().Add(15 * time.Second)
	for requested := false; ; requested = true {
		h, err := windows.OpenMutex(windows.SYNCHRONIZE, false, utf(watcherMutex))
		if errors.Is(err, windows.ERROR_FILE_NOT_FOUND) {
			return requested, nil
		}
		if err != nil {
			return false, err
		}
		windows.CloseHandle(h)
		if err := Stop(); err != nil {
			return false, err
		}
		if time.Now().After(deadline) {
			return false, errors.New("Codex Usage Monitor did not stop in time")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func replacePortableExecutable(staged, target string) error {
	// The old executable can remain mapped briefly while the watcher exits.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := os.Rename(staged, target)
		if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
			return err
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("replace the portable executable: %w", err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func stagePortableExecutable(file *os.File, dir string) (string, error) {
	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	z, err := zip.NewReader(file, info.Size())
	if err != nil {
		return "", err
	}
	var entry *zip.File
	for _, file := range z.File {
		if file.Name == monitorEXE {
			if entry != nil {
				return "", errors.New("portable archive contains more than one monitor executable")
			}
			entry = file
		}
	}
	if entry == nil || !entry.Mode().IsRegular() || entry.UncompressedSize64 == 0 || entry.UncompressedSize64 > releasecheck.MaxWindowsAsset {
		return "", errors.New("portable archive does not contain a valid monitor executable")
	}
	reader, err := entry.Open()
	if err != nil {
		return "", err
	}
	defer reader.Close()
	return writeUpdateExecutable(dir, ".monitor-update-*.exe", reader)
}

func writeUpdateExecutable(dir, pattern string, reader io.Reader) (string, error) {
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", err
	}
	_, copyErr := io.Copy(f, reader)
	if err := errors.Join(copyErr, f.Close()); err != nil {
		_ = os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}
