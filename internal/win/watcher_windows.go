package win

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"sync"
	"time"
	"unsafe"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/i18n"
	"codex-usage-monitor/internal/lifecycle"
	"golang.org/x/sys/windows"
)

const stopName = `Local\CodexUsageMonitor.Stop`

const (
	watchFallback = 1
	watchProbe    = 2
	watchRetry    = 3
	watchDrain    = 4
)

var wtsapi32 = windows.NewLazySystemDLL("wtsapi32.dll")

type workerExit struct {
	cmd        *exec.Cmd
	generation uint64
}

type watcher struct {
	hwnd              uintptr
	stop              windows.Handle
	configPath        string
	cfg               config.Config
	worker            *exec.Cmd
	exit              chan workerExit
	waits             sync.WaitGroup
	follow            lifecycle.Follow
	hooks             [2]uintptr
	pending           [16]uintptr
	pendingN          int
	probeArmed        bool
	scanPending       bool
	workerStopping    bool
	sessionRegistered bool
	stopping          bool
}

var currentWatcher *watcher
var watcherProc = callback(func(h uintptr, m uint32, w, l uintptr) uintptr {
	a := currentWatcher
	if a != nil {
		switch m {
		case wmTimer:
			switch w {
			case watchFallback:
				a.registerSession()
				a.idle() // Retry an event hook unavailable during early sign-in.
				a.scan()
			case watchProbe:
				a.probeWindows()
			case watchRetry:
				call(user32, "KillTimer", h, watchRetry)
				a.launch()
			case watchDrain:
				call(user32, "KillTimer", h, watchDrain)
				if a.workerStopping && a.worker != nil {
					_ = a.worker.Process.Kill() // Only our own stale statistics child.
				}
			}
			return 0
		case wmUpdate:
			a.reap()
			return 0
		case wmHostExited:
			if a.follow.HostExited(uint64(w)) && !a.stopping {
				a.stopWorker()
				a.idle()
				a.scan() // Catch a replacement already started during shutdown.
			}
			return 0
		case wmPowerBroadcast:
			if w == 0x12 || w == 0x7 { // Automatic / interactive resume.
				a.queueScan()
			}
			return 1
		case wmSessionChange:
			if w == 1 || w == 3 || w == 5 || w == 8 { // Connect, logon, unlock.
				a.queueScan()
			}
			return 0
		case wmSettingChange, wmUISettingsChanged:
			if cfg, err := config.Load(a.configPath); err == nil {
				a.cfg = cfg
				if m == wmUISettingsChanged {
					return 1 // Acknowledge the persisted UI settings reload.
				}
			}
			return 0
		case wmClose:
			a.shutdown()
			return 0
		case wmDestroy:
			call(user32, "PostQuitMessage", 0)
			return 0
		}
	}
	return call(user32, "DefWindowProcW", h, uintptr(m), w, l)
})

// OUTOFCONTEXT callbacks run on the GUI thread that owns the hook. Keep this
// permanent callback tiny: coalesce top-level HWNDs, never scan processes here.
var watcherEventProc = eventCallback(func(hook uintptr, event uint32, h uintptr, object, child int32, thread, timestamp uint32) uintptr {
	w := currentWatcher
	if w == nil || w.stopping || w.follow.PID != 0 || h == 0 || object != 0 || child != 0 ||
		call(user32, "GetAncestor", h, 2) != h {
		return 0
	}
	w.queueWindow(h)
	return 0
})

func RunWatcher(configPath string, notifyExisting bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	setDPIAware()
	_, statErr := os.Stat(configPath)
	firstRun := errors.Is(statErr, os.ErrNotExist)
	cfg, e := config.Load(configPath)
	if e != nil {
		return e
	}
	// Create defaults before registering startup. If settings cannot be saved,
	// leave Windows startup untouched and report the failure.
	if firstRun {
		if e = config.Save(configPath, cfg); e != nil {
			return e
		}
	}
	// Manual launches also repair an entry after moving the portable directory.
	// Do this before the singleton check, including when an older copy is active.
	// Sign-in startup does no shortcut work and owns no Shell COM references.
	if notifyExisting {
		if err := StartMenu(true, configPath); err != nil {
			messageBox(i18n.Text(cfg.Language, "Start menu shortcut could not be created"), err.Error())
		}
	}
	// Register the default before the singleton check, so a manual launch also
	// repairs paths after moving the portable folder. Explicit opt-out persists.
	// Matching entries are only read; automatic sign-in adds no recurring work.
	if err := lifecycle.EnsureStartup(cfg.StartWithWindows,
		func() (bool, error) { return StartupStatus(configPath) },
		func() error { return Startup(true, configPath) }); err != nil {
		messageBox(i18n.Text(cfg.Language, "Startup setting failed"), err.Error())
	}
	mutex, e := windows.CreateMutex(nil, false, utf(`Local\CodexUsageMonitor.Watcher`))
	if errors.Is(e, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(mutex)
		if notifyExisting {
			messageBox("Codex Usage Monitor", i18n.Text(cfg.Language, "Already running.\n\nOpen Codex and right-click the statistics bar for Settings or Quit."))
		}
		return nil
	}
	if e != nil {
		return e
	}
	defer windows.CloseHandle(mutex)
	stop, e := windows.CreateEvent(nil, 1, 0, utf(stopName))
	if e != nil {
		return e
	}
	defer windows.CloseHandle(stop)
	windows.ResetEvent(stop)
	w := &watcher{stop: stop, configPath: configPath, cfg: cfg, exit: make(chan workerExit, 1)}
	currentWatcher = w
	defer func() { currentWatcher = nil }()
	if e = registerClass("CodexUsageMonitor.Watcher", watcherProc); e != nil {
		return e
	}
	w.hwnd, e = createWindow("CodexUsageMonitor.Watcher", 0x80, 0)
	if e != nil {
		return e
	}
	w.registerSession()
	w.idle()
	w.scan()
	// --stop and the bar's Quit wake us immediately, even with Codex absent.
	w.waitForStop()
	e = messageLoop()
	w.shutdown()
	w.waits.Wait()
	if w.worker != nil {
		select {
		case <-w.exit:
		case <-time.After(lifecycle.WorkerStopLimit):
			_ = w.worker.Process.Kill()
			<-w.exit
		}
	}
	return e
}

func (w *watcher) waitForStop() {
	w.waits.Add(1)
	go func() {
		defer w.waits.Done()
		windows.WaitForSingleObject(w.stop, windows.INFINITE)
		post(w.hwnd, wmClose)
	}()
}

func (w *watcher) stopped() bool {
	if w.stopping {
		return true
	}
	if n, _ := windows.WaitForSingleObject(w.stop, 0); n == windows.WAIT_OBJECT_0 {
		post(w.hwnd, wmClose)
		return true
	}
	return false
}

func (w *watcher) timer(id uintptr, delay time.Duration) {
	call(user32, "SetTimer", w.hwnd, id, uintptr(max(1, (delay+time.Millisecond-1)/time.Millisecond)), 0)
}

func (w *watcher) registerSession() {
	if !w.stopping && !w.sessionRegistered {
		w.sessionRegistered = call(wtsapi32, "WTSRegisterSessionNotification", w.hwnd, 0) != 0
	}
}

func (w *watcher) idle() {
	if w.stopping || w.follow.PID != 0 {
		return
	}
	for i, event := range []uintptr{0x8002, 3} { // Window show and foreground.
		if w.hooks[i] == 0 {
			w.hooks[i] = call(user32, "SetWinEventHook", event, event, 0, watcherEventProc, 0, 0, 0x2)
		}
	}
	// A fixed low-frequency fallback also covers failed hooks, hidden launches
	// and missed events. No process timer runs while a host is bound.
	w.timer(watchFallback, lifecycle.FallbackInterval)
}

func (w *watcher) unhook() {
	for i, hook := range w.hooks {
		if hook != 0 {
			call(user32, "UnhookWinEvent", hook)
			w.hooks[i] = 0
		}
	}
	call(user32, "KillTimer", w.hwnd, watchFallback)
	call(user32, "KillTimer", w.hwnd, watchProbe)
	clear(w.pending[:])
	w.pendingN, w.probeArmed, w.scanPending = 0, false, false
}

func (w *watcher) armProbe() {
	if !w.probeArmed {
		w.probeArmed = true
		w.timer(watchProbe, lifecycle.EventDelay)
	}
}

func (w *watcher) queueScan() {
	if !w.stopping && w.follow.PID == 0 {
		w.scanPending = true
		w.armProbe()
	}
}

func (w *watcher) queueWindow(h uintptr) {
	for _, pending := range w.pending[:w.pendingN] {
		if pending == h {
			return
		}
	}
	if w.pendingN < len(w.pending) {
		w.pending[w.pendingN] = h
		w.pendingN++
	} else {
		w.pending[len(w.pending)-1] = h // Retain the latest window in a burst.
	}
	w.armProbe()
}

func (w *watcher) probeWindows() {
	call(user32, "KillTimer", w.hwnd, watchProbe)
	w.probeArmed = false
	pending, full := w.pending, w.scanPending
	n := w.pendingN
	clear(w.pending[:])
	w.pendingN, w.scanPending = 0, false
	if w.stopped() || w.follow.PID != 0 {
		return
	}
	if full {
		w.scan()
		return
	}
	for _, hwnd := range pending[:n] {
		if h := hostFromWindow(hwnd); h.Handle != 0 {
			w.attach(h)
			return
		}
	}
}

func (w *watcher) scan() {
	if w.stopped() || w.follow.PID != 0 {
		return
	}
	if h, err := findHost(); err == nil && h.Handle != 0 {
		w.attach(h)
	}
}

func (w *watcher) attach(h hostProcess) {
	if !w.follow.Attach(h.PID) {
		windows.CloseHandle(h.Handle)
		return
	}
	w.unhook()
	call(user32, "KillTimer", w.hwnd, watchRetry)
	generation := w.follow.Generation
	w.waits.Add(1)
	go func() {
		defer w.waits.Done()
		defer windows.CloseHandle(h.Handle)
		if n, err := windows.WaitForMultipleObjects([]windows.Handle{h.Handle, w.stop}, false, windows.INFINITE); err == nil && n == windows.WAIT_OBJECT_0 {
			call(user32, "PostMessageW", w.hwnd, wmHostExited, uintptr(generation), 0)
		}
	}()
	w.launch()
}

func (w *watcher) launch() {
	if w.stopped() || !w.follow.Ready(time.Now()) {
		return
	}
	exe, e := os.Executable()
	if e != nil {
		w.retryLaunch()
		return
	}
	cmd := exec.Command(exe, "--overlay", "--host-pid", strconv.FormatUint(uint64(w.follow.PID), 10), "--config", w.configPath)
	if e = cmd.Start(); e != nil {
		w.retryLaunch()
		return
	}
	w.worker = cmd
	w.workerStopping = false
	generation := w.follow.WorkerStarted(time.Now())
	go func() {
		_ = cmd.Wait()
		w.exit <- workerExit{cmd: cmd, generation: generation}
		post(w.hwnd, wmUpdate)
	}()
}

func (w *watcher) retryLaunch() {
	w.follow.LaunchFailed(time.Now())
	w.timer(watchRetry, time.Until(w.follow.RetryAt))
}

func (w *watcher) reap() {
	select {
	case result := <-w.exit:
		if result.cmd != w.worker || !w.follow.WorkerExited(result.generation, time.Now()) {
			return
		}
		w.worker, w.workerStopping = nil, false
		call(user32, "KillTimer", w.hwnd, watchDrain)
		if w.stopped() {
			return
		}
		if w.follow.PID == 0 {
			w.scan()
		} else if w.follow.Ready(time.Now()) {
			w.launch()
		} else {
			w.timer(watchRetry, time.Until(w.follow.RetryAt))
		}
	default:
	}
}

func (w *watcher) stopWorker() {
	call(user32, "KillTimer", w.hwnd, watchRetry)
	if w.worker == nil || w.workerStopping {
		return
	}
	w.workerStopping = true
	h := call(user32, "FindWindowW", uintptr(unsafe.Pointer(utf("CodexUsageMonitor.Overlay"))), 0)
	var pid uint32
	call(user32, "GetWindowThreadProcessId", h, uintptr(unsafe.Pointer(&pid)))
	if h != 0 && pid == uint32(w.worker.Process.Pid) {
		post(h, wmClose)
	}
	w.timer(watchDrain, lifecycle.WorkerStopLimit)
}

func (w *watcher) shutdown() {
	if w.stopping {
		return
	}
	w.stopping = true
	windows.SetEvent(w.stop)
	w.unhook()
	call(user32, "KillTimer", w.hwnd, watchRetry)
	call(user32, "KillTimer", w.hwnd, watchDrain)
	if w.sessionRegistered {
		call(wtsapi32, "WTSUnRegisterSessionNotification", w.hwnd)
		w.sessionRegistered = false
	}
	call(user32, "DestroyWindow", w.hwnd)
}

func Stop() error {
	h, e := windows.OpenEvent(windows.EVENT_MODIFY_STATE, false, utf(stopName))
	if errors.Is(e, windows.ERROR_FILE_NOT_FOUND) {
		return nil
	}
	if e != nil {
		return e
	}
	defer windows.CloseHandle(h)
	return windows.SetEvent(h)
}
