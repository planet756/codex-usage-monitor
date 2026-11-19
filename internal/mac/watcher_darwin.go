package mac

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"syscall"
	"time"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/lifecycle"
	"codex-usage-monitor/internal/source"
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"golang.org/x/sys/unix"
)

const desktopBundle = "com.openai.codex"

func findHost() uint32 {
	apps := objc.ID(objc.GetClass("NSRunningApplication")).Send(sel("runningApplicationsWithBundleIdentifier:"), ns(desktopBundle))
	n := apps.Send(sel("count"))
	for i := objc.ID(0); i < n; i++ {
		a := apps.Send(sel("objectAtIndex:"), i)
		if a.Send(sel("isTerminated")) == 0 {
			return uint32(a.Send(sel("processIdentifier")))
		}
	}
	return 0
}
func hostAlive(pid uint32) bool {
	a := objc.ID(objc.GetClass("NSRunningApplication")).Send(sel("runningApplicationWithProcessIdentifier:"), int32(pid))
	return a != 0 && cfString(uintptr(a.Send(sel("bundleIdentifier")))) == desktopBundle && a.Send(sel("isTerminated")) == 0
}

var watcherWake func()
var wakeSource uintptr
var watcherLoop uintptr
var wakeCallback = purego.NewCallback(func(_ uintptr) {
	if watcherWake != nil {
		watcherWake()
	}
})
var fallbackCallback = purego.NewCallback(func(_ uintptr, _ uintptr) {
	if watcherWake != nil {
		watcherWake()
	}
})

func signalWatcher() { native.sourceSignal(wakeSource); native.wake(watcherLoop) }

func processLock(configPath, name string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(configPath), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(filepath.Dir(configPath), name+".lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

type workerExit struct {
	cmd        *exec.Cmd
	generation uint64
}

// RunWatcher owns no reader, IPC connection, AX observer or drawing surface.
// With Desktop absent, CFRunLoop sleeps until an application event, a stop
// request or the same low-frequency lifecycle fallback used by Windows.
func RunWatcher(configPath string, manual bool) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err := startApplication(); err != nil {
		return err
	}
	p := pool()
	defer p.Send(sel("drain"))
	lock, err := processLock(configPath, "watcher")
	if errors.Is(err, unix.EWOULDBLOCK) {
		if manual {
			showAlert("Codex Usage Monitor", "Already running. Open Codex to use the statistics bar.")
		}
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	if _, err = os.Stat(configPath); errors.Is(err, os.ErrNotExist) {
		if err = config.Save(configPath, cfg); err != nil {
			return err
		}
	}
	if manual && !native.axTrusted() {
		options := objc.ID(objc.GetClass("NSDictionary")).Send(sel("dictionaryWithObject:forKey:"), objc.ID(objc.GetClass("NSNumber")).Send(sel("numberWithBool:"), true), ns("AXTrustedCheckOptionPrompt"))
		native.axTrustedOptions(uintptr(options))
	}
	if err = ensureStartup(configPath, cfg); err != nil {
		return err
	}
	watcherLoop = native.runLoop()
	wakeSource = native.sourceCreate(0, 0, &sourceContext{Perform: wakeCallback})
	if wakeSource == 0 {
		return errors.New("watcher run-loop source unavailable")
	}
	native.addSource(watcherLoop, wakeSource, native.defaultMode)
	defer func() { native.removeSource(watcherLoop, wakeSource, native.defaultMode); native.release(wakeSource) }()
	controlPath := filepath.Join(filepath.Dir(configPath), "watcher.sock")
	// The singleton lock is held before retiring a stale control socket.
	if info, e := os.Lstat(controlPath); e == nil {
		st, ok := info.Sys().(*syscall.Stat_t)
		if !ok || info.Mode()&os.ModeSocket == 0 || st.Uid != uint32(os.Getuid()) {
			return errors.New("existing monitor control path is not an owned socket")
		}
		if err = os.Remove(controlPath); err != nil {
			return err
		}
	} else if !errors.Is(e, os.ErrNotExist) {
		return e
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: controlPath, Net: "unix"})
	if err != nil {
		return err
	}
	defer listener.Close()
	if err = os.Chmod(controlPath, 0600); err != nil {
		return err
	}
	stop := make(chan struct{}, 1)
	var background sync.WaitGroup
	background.Add(1)
	go func() {
		defer background.Done()
		for {
			c, e := listener.AcceptUnix()
			if e != nil {
				return
			}
			_ = c.SetReadDeadline(time.Now().Add(time.Second))
			var b [4]byte
			n, _ := io.ReadFull(c, b[:])
			c.Close()
			if n == 4 && string(b[:]) == "stop" {
				select {
				case stop <- struct{}{}:
				default:
				}
				signalWatcher()
				return
			}
		}
	}()
	exits := make(chan workerExit, 1)
	var worker *exec.Cmd
	var shutdown workerShutdown
	var follow lifecycle.Follow
	var timer uintptr
	var nextFire func(uintptr, float64)
	stopping := false
	watcherWake = func() {
		iteration := pool()
		defer iteration.Send(sel("drain"))
		defer func() {
			if timer == 0 {
				return
			}
			delay := lifecycle.FallbackInterval
			if follow.PID != 0 && follow.WorkerGeneration == 0 && !follow.RetryAt.IsZero() {
				delay = min(delay, max(100*time.Millisecond, time.Until(follow.RetryAt)))
			}
			if shutdown.generation != 0 {
				delay = min(delay, time.Second)
			}
			nextFire(timer, native.clock()+delay.Seconds())
		}()
		select {
		case <-stop:
			stopping = true
			native.stop(watcherLoop)
			return
		default:
		}
		select {
		case exit := <-exits:
			if exit.cmd == worker && follow.WorkerExited(exit.generation, time.Now()) {
				worker = nil
			}
		default:
		}
		if follow.PID != 0 && !hostAlive(follow.PID) {
			follow.HostExited(follow.Generation)
		}
		switch shutdown.poll(&follow, time.Now()) {
		case shutdownTerminate:
			_ = worker.Process.Signal(unix.SIGTERM)
		case shutdownKill:
			_ = worker.Process.Kill()
		}
		if follow.PID == 0 {
			if pid := findHost(); pid != 0 {
				follow.Attach(pid)
			}
		}
		if !follow.Ready(time.Now()) {
			return
		}
		executable, e := os.Executable()
		if e != nil {
			follow.LaunchFailed(time.Now())
			return
		}
		cmd := exec.Command(executable, "--overlay", "--host-pid", strconv.FormatUint(uint64(follow.PID), 10), "--config", configPath)
		if e = cmd.Start(); e != nil {
			follow.LaunchFailed(time.Now())
			return
		}
		worker = cmd
		generation := follow.WorkerStarted(time.Now())
		background.Add(1)
		go func() { defer background.Done(); _ = cmd.Wait(); exits <- workerExit{cmd, generation}; signalWatcher() }()
	}
	defer func() {
		listener.Close()
		if worker != nil {
			_ = worker.Process.Signal(unix.SIGTERM)
			select {
			case <-exits:
			case <-time.After(lifecycle.WorkerStopLimit):
				_ = worker.Process.Kill()
			}
		}
		background.Wait()
		watcherWake = nil
	}()
	class, e := objc.RegisterClass("CUMWatcherEvents", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{{Cmd: sel("changed:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
		if watcherWake != nil {
			watcherWake()
		}
	}}})
	if e != nil {
		return e
	}
	observer := objc.ID(class).Send(sel("new"))
	defer observer.Send(sel("release"))
	workspace := objc.ID(objc.GetClass("NSWorkspace")).Send(sel("sharedWorkspace"))
	center := workspace.Send(sel("notificationCenter"))
	for _, name := range []string{"NSWorkspaceDidLaunchApplicationNotification", "NSWorkspaceDidTerminateApplicationNotification", "NSWorkspaceDidWakeNotification"} {
		center.Send(sel("addObserver:selector:name:object:"), observer, sel("changed:"), ns(name), objc.ID(0))
	}
	defer center.Send(sel("removeObserver:"), observer)
	var addTimer func(uintptr, uintptr, uintptr)
	var invalidate func(uintptr)
	cf, _ := purego.Dlopen("/System/Library/Frameworks/CoreFoundation.framework/CoreFoundation", purego.RTLD_NOW|purego.RTLD_GLOBAL)
	purego.RegisterLibFunc(&addTimer, cf, "CFRunLoopAddTimer")
	purego.RegisterLibFunc(&invalidate, cf, "CFRunLoopTimerInvalidate")
	purego.RegisterLibFunc(&nextFire, cf, "CFRunLoopTimerSetNextFireDate")
	timer = native.timerCreate(0, native.clock(), lifecycle.FallbackInterval.Seconds(), 0, 0, fallbackCallback, 0)
	if timer == 0 {
		return errors.New("watcher fallback timer unavailable")
	}
	defer native.release(timer)
	defer invalidate(timer)
	addTimer(watcherLoop, timer, native.defaultMode)
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, unix.SIGTERM, unix.SIGINT)
	defer signal.Stop(signals)
	signalDone := make(chan struct{})
	defer close(signalDone)
	background.Add(1)
	go func() {
		defer background.Done()
		select {
		case <-signals:
			select {
			case stop <- struct{}{}:
			default:
			}
			signalWatcher()
		case <-signalDone:
		}
	}()
	// Reaps and native notifications wake the source immediately. A short
	// retry after a failed worker is bounded by the shared lifecycle state.
	watcherWake()
	for !stopping {
		iteration := pool()
		native.runMode(native.defaultMode, lifecycle.FallbackInterval.Seconds(), true)
		iteration.Send(sel("drain"))
	}
	return nil
}

func Stop(configPath string) error {
	c, err := net.DialTimeout("unix", filepath.Join(filepath.Dir(configPath), "watcher.sock"), time.Second)
	if errors.Is(err, os.ErrNotExist) || errors.Is(err, unix.ECONNREFUSED) {
		return nil
	}
	if err != nil {
		return err
	}
	defer c.Close()
	_, err = c.Write([]byte("stop"))
	return err
}

func Diagnostics(configPath string) ([]byte, error) {
	if err := loadNative(); err != nil {
		return nil, err
	}
	p := pool()
	defer p.Send(sel("drain"))
	pid := findHost()
	result := struct {
		Platform          string `json:"platform"`
		DesktopRunning    bool   `json:"desktop_running"`
		Accessibility     bool   `json:"accessibility"`
		AXEnableRequested bool   `json:"accessibility_tree_requested"`
		AXEnableCode      int32  `json:"accessibility_tree_error"`
		ChatPage          bool   `json:"chat_page"`
		IdentityVerified  bool   `json:"identity_verified"`
		ViewSource        string `json:"view_source"`
		ViewReason        string `json:"view_reason"`
		AXNodes           int    `json:"ax_nodes"`
		HeaderProbe       [3]int `json:"header_probe"`
		Popup             bool   `json:"popup"`
	}{Platform: "macOS", DesktopRunning: pid != 0, Accessibility: native.axTrusted()}
	if pid != 0 && result.Accessibility {
		cfg, err := config.Load(configPath)
		if err != nil {
			return nil, err
		}
		home, err := source.DirectoryPOSIX(config.Home(cfg))
		if err != nil {
			return nil, err
		}
		db := catalogDB{}
		defer db.close()
		v := locator{app: native.axApplication(int32(pid)), ax: darwinAX{}, lookup: db.lookup}
		defer v.close()
		o := v.probe(context.Background(), home)
		result.AXEnableRequested, result.AXEnableCode = o.AXEnableRequested, o.AXEnableCode
		if o.Window != 0 {
			defer native.release(o.Window)
		}
		result.ChatPage, result.IdentityVerified, result.ViewSource, result.ViewReason, result.AXNodes, result.HeaderProbe, result.Popup = o.View.ChatPage, o.View.ID != "", o.View.Source, o.View.Reason, o.Nodes, o.View.HeaderProbe, o.Popup.Visible
	}
	return json.MarshalIndent(result, "", "  ")
}

func showAlert(title, message string) {
	a := newObject("NSAlert")
	defer a.Send(sel("release"))
	a.Send(sel("setMessageText:"), ns(title))
	a.Send(sel("setInformativeText:"), ns(message))
	a.Send(sel("runModal"))
}

func ShowError(message string) {
	if startApplication() == nil {
		p := pool()
		defer p.Send(sel("drain"))
		showAlert("Codex Usage Monitor", message)
	}
}
