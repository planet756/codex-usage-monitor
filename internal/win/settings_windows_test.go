package win

import (
	"path/filepath"
	"runtime"
	"testing"
	"unsafe"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/metrics"
)

func TestPaintCacheIncludesLanguageWithoutDataOrGeometryChanges(t *testing.T) {
	a := overlay{cfg: config.Config{Language: "en"}}
	english := a.paintKey(440, 26, 96)
	a.cfg.Language = "zh-CN"
	if a.paintKey(440, 26, 96) == english {
		t.Fatal("language change reused the English bitmap")
	}
}

// Uses real hidden HWNDs and the production notification/receiver paths. This
// test is cross-compiled in WSL; execution requires native Windows.
func TestUISettingsNotificationReloadsWatcherAndInvalidatesHiddenOverlay(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	path := filepath.Join(t.TempDir(), "settings.json")
	cfg := config.Config{Language: "en", Theme: "light", CodexHome: t.TempDir()}
	if err := config.Save(path, cfg); err != nil {
		t.Fatal(err)
	}
	rate := 80.0
	w := &watcher{configPath: path, cfg: cfg}
	a := &overlay{configPath: path, cfg: cfg, pid: ^uint32(0),
		data: dataUpdate{ID: "test-chat", Snapshot: metrics.Snapshot{TPS: &rate, Turns: 2, Steps: 5}}}
	oldWatcher, oldOverlay := currentWatcher, currentOverlay
	currentWatcher, currentOverlay = w, a
	defer func() { currentWatcher, currentOverlay = oldWatcher, oldOverlay }()
	var handles []uintptr
	var classes []string
	defer func() {
		for _, h := range handles {
			call(user32, "DestroyWindow", h)
		}
		for _, class := range classes {
			call(user32, "UnregisterClassW", uintptr(unsafe.Pointer(utf(class))), call(kernel32, "GetModuleHandleW", 0))
		}
		// Both production window procedures post WM_QUIT when destroyed. Do not
		// leave it on this reusable test thread's queue.
		var m msg
		for call(user32, "PeekMessageW", uintptr(unsafe.Pointer(&m)), 0, 0x12, 0x12, 1) != 0 {
		}
	}()
	for _, item := range []struct {
		class string
		proc  uintptr
		out   *uintptr
	}{{"CodexUsageMonitor.Watcher", watcherProc, &w.hwnd}, {"CodexUsageMonitor.Overlay", overlayProc, &a.hwnd}} {
		if err := registerClass(item.class, item.proc); err != nil {
			t.Fatal(err)
		}
		classes = append(classes, item.class)
		h, err := createWindow(item.class, 0x80, 0)
		if err != nil {
			t.Fatal(err)
		}
		*item.out = h
		handles = append(handles, h)
	}
	seen := map[uintptr]bool{}
	for _, h := range settingsWindows() {
		seen[h] = true
	}
	if !seen[w.hwnd] || !seen[a.hwnd] {
		t.Fatal("notification discovery omitted a hidden companion window")
	}
	for _, lang := range []string{"zh-CN", "en"} {
		updated := cfg
		updated.Language, updated.CodexHome = lang, "changed-source-requires-restart"
		if err := config.Save(path, updated); err != nil {
			t.Fatal(err)
		}
		a.lastPaint, a.dirty = paintKey{Language: "old-language-bitmap"}, false
		for _, h := range handles {
			if !notifySettingsWindow(h) {
				t.Fatal("settings reload was not acknowledged")
			}
		}
		if w.cfg.Language != lang || a.cfg.Language != lang || !a.dirty || a.lastPaint != (paintKey{}) {
			t.Fatal("reload failed to update language or retain hidden repaint invalidation")
		}
		if a.cfg.CodexHome != cfg.CodexHome || a.data.ID != "test-chat" || a.data.Snapshot.TPS != &rate || a.data.Snapshot.Steps != 5 {
			t.Fatal("UI reload changed the active data source or accounting state")
		}
	}

}

func TestSameChatOnDifferentHostClearsOldStatistics(t *testing.T) {
	a := overlay{viewResult: viewSelection{ID: "chat", Host: "local"}, viewSelections: make(chan viewSelection, 1),
		data: dataUpdate{ID: "chat", Host: "local", Snapshot: metrics.Snapshot{HasCounts: true, Turns: 99}}}
	a.setView(viewSelection{ID: "chat", Host: "remote-peer"})
	if a.data.Snapshot.HasCounts || a.data.Host != "remote-peer" {
		t.Fatal("host switch reused another host's statistics")
	}
}
