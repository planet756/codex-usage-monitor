package win

import (
	"runtime"
	"sync/atomic"
	"testing"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// These tests use real HWNDs/events, without launching Codex or an overlay.
// They are compiled in WSL and must be executed on native Windows.
func hiddenTestWatcher(t *testing.T) *watcher {
	t.Helper()
	runtime.LockOSThread()
	t.Cleanup(runtime.UnlockOSThread)
	stop, err := windows.CreateEvent(nil, 1, 0, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { windows.CloseHandle(stop) })
	w := &watcher{stop: stop, exit: make(chan workerExit, 1)}
	old := currentWatcher
	currentWatcher = w
	t.Cleanup(func() { currentWatcher = old })
	const class = "CodexUsageMonitor.Watcher.Test"
	if err := registerClass(class, watcherProc); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		call(user32, "UnregisterClassW", uintptr(unsafe.Pointer(utf(class))), call(kernel32, "GetModuleHandleW", 0))
	})
	w.hwnd, err = createWindow(class, 0x80, 0)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		w.shutdown()
		w.waits.Wait()
		var m msg
		for call(user32, "PeekMessageW", uintptr(unsafe.Pointer(&m)), 0, 0x12, 0x12, 1) != 0 {
		}
	})
	return w
}

func TestWatcherCoalescesAndBoundsWindowBursts(t *testing.T) {
	w := hiddenTestWatcher(t)
	w.queueWindow(100)
	for range 200 {
		w.queueWindow(100)
	}
	if w.pendingN != 1 || !w.probeArmed {
		t.Fatal("duplicate events grew the pending window queue")
	}
	for hwnd := uintptr(101); hwnd <= 300; hwnd++ {
		w.queueWindow(hwnd)
	}
	if w.pendingN != len(w.pending) || w.pending[w.pendingN-1] != 300 {
		t.Fatal("event storm exceeded its bound or dropped the latest window")
	}
	w.unhook()
	if w.pendingN != 0 || w.probeArmed || w.scanPending {
		t.Fatal("attaching a host left idle event work scheduled")
	}
}

func TestStopWakesIdleWatcherWithoutPolling(t *testing.T) {
	w := hiddenTestWatcher(t)
	w.waitForStop()
	var timedOut atomic.Bool
	guardDone := make(chan struct{})
	go func() {
		timer := time.NewTimer(2 * time.Second)
		defer timer.Stop()
		select {
		case <-timer.C:
			timedOut.Store(true)
			post(w.hwnd, wmClose)
		case <-guardDone:
		}
	}()
	windows.SetEvent(w.stop)
	err := messageLoop()
	close(guardDone)
	w.waits.Wait()
	if err != nil || timedOut.Load() || !w.stopping || call(user32, "IsWindow", w.hwnd) != 0 {
		t.Fatalf("idle stop did not wake and close the watcher: %v", err)
	}
}
