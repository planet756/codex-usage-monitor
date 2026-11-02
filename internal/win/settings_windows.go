package win

import (
	"fmt"
	"unsafe"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/i18n"
	"golang.org/x/sys/windows"
)

type paintStruct struct {
	DC                 uintptr
	Erase              int32
	Paint              rect
	Restore, IncUpdate int32
	Reserved           [32]byte
}

type settingsWindow struct {
	hwnd, language, theme, languageLabel, themeLabel, heading, subtitle, note, done uintptr
	font, smallFont, headerFont                                                     uintptr
	path                                                                            string
	applied                                                                         config.Config
	dpi                                                                             int
	saved                                                                           bool
	refreshUnconfirmed                                                              bool
}

var currentSettings *settingsWindow
var settingsRegistered bool

// Standard settings controls retain system-owned GDI text. These helpers only
// draw the settings separator; the telemetry renderer uses DirectWrite only.
func colorRef(rgb uint32) uintptr {
	return uintptr((rgb&0xff)<<16 | (rgb & 0xff00) | (rgb>>16)&0xff)
}
func line(dc uintptr, x1, y1, x2, y2 int, color uint32) {
	pen := call(gdi32, "CreatePen", 0, 1, colorRef(color))
	if pen == 0 {
		return
	}
	old := call(gdi32, "SelectObject", dc, pen)
	call(gdi32, "MoveToEx", dc, signed(x1), signed(y1), 0)
	call(gdi32, "LineTo", dc, signed(x2), signed(y2))
	call(gdi32, "SelectObject", dc, old)
	call(gdi32, "DeleteObject", pen)
}

var settingsProc = callback(func(h uintptr, m uint32, w, l uintptr) uintptr {
	s := currentSettings
	if s != nil && s.hwnd == h {
		switch m {
		case wmCommand:
			id, notification := uint16(w), uint16(w>>16)
			switch {
			case (id == 101 || id == 102) && notification == 1: // CBN_SELCHANGE
				s.apply()
			case id == 1 || id == 2: // Done, Enter, Escape.
				call(user32, "DestroyWindow", h)
			}
			return 0
		case 0x000F: // WM_PAINT: one quiet section rule; controls remain native.
			var ps paintStruct
			dc := call(user32, "BeginPaint", h, uintptr(unsafe.Pointer(&ps)))
			// System colours are COLORREF; drawing helpers accept RGB.
			p := func(n int) int { return scale(n, s.dpi) }
			line(dc, p(22), p(76), p(398), p(76), uint32(colorRef(uint32(call(user32, "GetSysColor", 15)))))
			call(user32, "EndPaint", h, uintptr(unsafe.Pointer(&ps)))
			return 0
		case 0x0138: // WM_CTLCOLORSTATIC: blend labels into the system surface.
			call(gdi32, "SetBkMode", w, 1)
			call(gdi32, "SetTextColor", w, call(user32, "GetSysColor", 8))
			return call(user32, "GetSysColorBrush", 5)
		case wmClose:
			call(user32, "DestroyWindow", h)
			return 0
		case wmDestroy:
			for _, font := range []uintptr{s.font, s.smallFont, s.headerFont} {
				if font != 0 {
					call(gdi32, "DeleteObject", font)
				}
			}
			currentSettings = nil // Never stop the companion's message loop.
			return 0
		}
	}
	return call(user32, "DefWindowProcW", h, uintptr(m), w, l)
})

func showSettings(owner uintptr, path string) {
	if currentSettings != nil {
		call(user32, "ShowWindow", currentSettings.hwnd, 9)
		call(user32, "SetForegroundWindow", currentSettings.hwnd)
		return
	}
	// The watcher and overlay have separate message loops but share one settings UI.
	if existing := call(user32, "FindWindowW", uintptr(unsafe.Pointer(utf("CodexUsageMonitor.Settings"))), 0); existing != 0 {
		call(user32, "ShowWindow", existing, 9)
		call(user32, "SetForegroundWindow", existing)
		return
	}
	cfg, err := config.Load(path)
	if err != nil {
		messageBox("Settings", err.Error())
		return
	}
	fail := func(err error) { messageBox(i18n.Text(cfg.Language, "Settings"), err.Error()) }
	if !settingsRegistered {
		wc := wndClass{Size: uint32(unsafe.Sizeof(wndClass{})), Proc: settingsProc,
			Instance: call(kernel32, "GetModuleHandleW", 0), Background: 6,
			Cursor: call(user32, "LoadCursorW", 0, 32512), Name: utf("CodexUsageMonitor.Settings")}
		if call(user32, "RegisterClassExW", uintptr(unsafe.Pointer(&wc))) == 0 {
			fail(fmt.Errorf("RegisterClassExW failed"))
			return
		}
		settingsRegistered = true
	}
	dpi := dpiFor(owner)
	p := func(n int) int { return scale(n, dpi) }
	r := rect{Right: int32(p(420)), Bottom: int32(p(312))}
	const style = 0x00C80000 // Caption and system menu; native fixed-size settings.
	call(user32, "AdjustWindowRectEx", uintptr(unsafe.Pointer(&r)), style, 0, 0x10000)
	x, y := (int(call(user32, "GetSystemMetrics", 0))-r.width())/2, (int(call(user32, "GetSystemMetrics", 1))-r.height())/2
	var anchor rect
	if call(user32, "GetWindowRect", owner, uintptr(unsafe.Pointer(&anchor))) != 0 && anchor.width() > r.width() && anchor.height() > r.height() {
		x, y = int(anchor.Left)+(anchor.width()-r.width())/2, int(anchor.Top)+(anchor.height()-r.height())/2
	}
	h := call(user32, "CreateWindowExW", 0x10000, uintptr(unsafe.Pointer(utf("CodexUsageMonitor.Settings"))),
		uintptr(unsafe.Pointer(utf(i18n.Text(cfg.Language, "Settings")))), style,
		signed(x), signed(y), uintptr(r.width()), uintptr(r.height()), owner, 0, call(kernel32, "GetModuleHandleW", 0), 0)
	if h == 0 {
		fail(fmt.Errorf("CreateWindowExW failed"))
		return
	}
	s := &settingsWindow{hwnd: h, path: path, applied: cfg, dpi: dpi}
	currentSettings = s
	makeFont := func(size, weight int) uintptr {
		return call(gdi32, "CreateFontW", signed(-p(size)), 0, 0, 0, uintptr(weight), 0, 0, 0, 1, 0, 0, 4, 0, uintptr(unsafe.Pointer(utf("Segoe UI"))))
	}
	s.font, s.smallFont, s.headerFont = makeFont(13, 400), makeFont(12, 400), makeFont(18, 600)
	if s.font == 0 || s.smallFont == 0 || s.headerFont == 0 {
		call(user32, "DestroyWindow", h)
		fail(fmt.Errorf("CreateFontW failed"))
		return
	}
	makeControl := func(class, label string, flags uint32, id, x, y, w, height int, font uintptr) uintptr {
		child := call(user32, "CreateWindowExW", 0, uintptr(unsafe.Pointer(utf(class))), uintptr(unsafe.Pointer(utf(label))),
			uintptr(flags)|0x50000000, signed(p(x)), signed(p(y)), uintptr(p(w)), uintptr(p(height)), h, uintptr(id), call(kernel32, "GetModuleHandleW", 0), 0)
		call(user32, "SendMessageW", child, 0x30, font, 1)
		return child
	}
	s.heading = makeControl("STATIC", "", 0, 0, 22, 18, 376, 26, s.headerFont)
	s.subtitle = makeControl("STATIC", "Codex Usage Monitor", 0, 0, 22, 46, 376, 20, s.smallFont)
	s.languageLabel = makeControl("STATIC", "", 0, 0, 22, 92, 376, 22, s.font)
	s.language = makeControl("COMBOBOX", "", 0x00210003, 101, 22, 118, 376, 180, s.font)
	for _, label := range []string{"English", "简体中文"} {
		call(user32, "SendMessageW", s.language, 0x143, 0, uintptr(unsafe.Pointer(utf(label))))
	}
	s.themeLabel = makeControl("STATIC", "", 0, 0, 22, 172, 376, 22, s.font)
	s.theme = makeControl("COMBOBOX", "", 0x00210003, 102, 22, 198, 376, 180, s.font)
	s.note = makeControl("STATIC", "", 0, 0, 22, 271, 272, 22, s.smallFont)
	s.done = makeControl("BUTTON", "", 0x00010001, 1, 314, 266, 84, 32, s.font)
	for _, control := range []uintptr{s.heading, s.subtitle, s.languageLabel, s.language, s.themeLabel, s.theme, s.note, s.done} {
		if control == 0 {
			call(user32, "DestroyWindow", h)
			fail(fmt.Errorf("Could not create settings controls"))
			return
		}
	}
	for _, control := range []uintptr{s.language, s.theme} {
		if control == 0 {
			continue
		}
		call(user32, "SendMessageW", control, 0x153, ^uintptr(0), uintptr(p(24))) // CB_SETITEMHEIGHT, selection.
	}
	s.labels()
	call(user32, "ShowWindow", h, 5)
	call(user32, "SetForegroundWindow", h)
	call(user32, "SetFocus", s.language)
}

func (s *settingsWindow) labels() {
	lang := s.applied.Language
	note := "Changes apply immediately."
	if s.saved {
		note = "Saved"
	}
	if s.refreshUnconfirmed {
		note = "Saved · refresh not confirmed"
	}
	for control, label := range map[uintptr]string{s.hwnd: "Settings", s.heading: "Settings", s.languageLabel: "Language",
		s.themeLabel: "Appearance", s.note: note, s.done: "Done"} {
		call(user32, "SetWindowTextW", control, uintptr(unsafe.Pointer(utf(i18n.Text(lang, label)))))
	}
	languageIndex := uintptr(0)
	if lang == "zh-CN" {
		languageIndex = 1
	}
	call(user32, "SendMessageW", s.language, 0x14E, languageIndex, 0)
	call(user32, "SendMessageW", s.theme, 0x14B, 0, 0)
	for _, label := range []string{"Automatic", "Light", "Dark"} {
		call(user32, "SendMessageW", s.theme, 0x143, 0, uintptr(unsafe.Pointer(utf(i18n.Text(lang, label)))))
	}
	themeIndex := uintptr(0)
	if s.applied.Theme == "light" {
		themeIndex = 1
	} else if s.applied.Theme == "dark" {
		themeIndex = 2
	}
	call(user32, "SendMessageW", s.theme, 0x14E, themeIndex, 0)
	call(user32, "RedrawWindow", s.hwnd, 0, 0, 0x185) // Invalidate + erase + all children + update now.
}

func (s *settingsWindow) apply() {
	language, theme := call(user32, "SendMessageW", s.language, 0x147, 0, 0), call(user32, "SendMessageW", s.theme, 0x147, 0, 0)
	if language > 1 || theme > 2 {
		return
	}
	cfg, err := config.UpdateUI(s.path, []string{"en", "zh-CN"}[language], []string{"auto", "light", "dark"}[theme])
	if err != nil {
		s.labels() // Restore the committed selection on a failed save.
		messageBox(i18n.Text(s.applied.Language, "Settings could not be saved"), err.Error())
		return
	}
	s.applied, s.saved = cfg, true
	s.refreshUnconfirmed = !notifySettingsChanged()
	s.labels()
}

// One permanent callback; NewCallback allocations cannot be reclaimed.
var settingsTargets []uintptr
var settingsEnumProc = enumCallback(func(h, l uintptr) uintptr {
	var class [64]uint16
	if call(user32, "GetClassNameW", h, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class))) != 0 {
		switch windows.UTF16ToString(class[:]) {
		case "CodexUsageMonitor.Watcher", "CodexUsageMonitor.Overlay":
			settingsTargets = append(settingsTargets, h)
		}
	}
	return 1
})

// Called on the settings window's GUI thread. Collect before sending so a
// recipient can process its own messages without invalidating enumeration state.
func settingsWindows() []uintptr {
	settingsTargets = nil
	if call(user32, "EnumWindows", settingsEnumProc, 0) == 0 {
		settingsTargets = nil
		return nil
	}
	targets := settingsTargets
	settingsTargets = nil
	return targets
}

func notifySettingsWindow(target uintptr) bool {
	var acknowledged uintptr
	if call(user32, "SendMessageTimeoutW", target, wmUISettingsChanged, 0, 0, 0x22, 250,
		uintptr(unsafe.Pointer(&acknowledged))) != 0 && acknowledged == 1 {
		return true
	}
	// A busy recipient may still catch up after the bounded synchronous attempt.
	// The settings note reports that refresh was not confirmed, rather than
	// silently treating an unacknowledged reload as applied.
	post(target, wmUISettingsChanged)
	return false
}

func notifySettingsChanged() bool {
	targets := settingsWindows()
	confirmed := len(targets) > 0
	for _, target := range targets {
		if !notifySettingsWindow(target) && call(user32, "IsWindow", target) != 0 {
			confirmed = false
		}
	}
	return confirmed
}
