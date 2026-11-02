package win

import (
	"codex-usage-monitor/internal/layout"
	"codex-usage-monitor/internal/status"
	"syscall"
	"time"
	"unsafe"
)

type titlebarInfoEx struct {
	Size   uint32
	Bounds rect
	State  [6]uint32
	Parts  [6]rect
}

type captionDetector struct {
	host      uintptr
	window    rect
	button    rect // Window-relative, so moving the host requires no rescan.
	dpi       int
	style     uintptr
	maximized bool
	checked   time.Time
	source    string
	details   status.CaptionProbe
	found     bool
}

func (c *captionDetector) locate(host uintptr, dpi int, force bool) (rect, rect, string, status.CaptionProbe, bool) {
	var window rect
	if call(user32, "GetWindowRect", host, uintptr(unsafe.Pointer(&window))) == 0 {
		return window, rect{}, "unavailable", status.CaptionProbe{}, false
	}
	maximized := call(user32, "IsZoomed", host) != 0
	style := call(user32, "GetWindowLongPtrW", host, signed(-16))
	sameCaption := host == c.host && dpi == c.dpi && maximized == c.maximized && style == c.style
	sameGeometry := sameCaption && window.width() == c.window.width() && window.height() == c.window.height()
	cacheTTL := 30 * time.Second
	if !c.found {
		cacheTTL = time.Second
	}
	if !force && sameCaption && time.Since(c.checked) < cacheTTL {
		if !c.found {
			return window, rect{}, c.source, c.details, false
		}
		button := captionToScreen(c.button, window)
		dx := window.width() - c.window.width()
		button.Left += int32(dx)
		button.Right += int32(dx)
		if validCaption(window, button) {
			return window, button, c.source, c.details, c.found
		}
	}
	button, source, details, found := captionAnchor(host, window, dpi)
	// A busy app can time out while its existing, measured button geometry is
	// still valid. Keep that position rather than flickering on a transient miss.
	if !found && sameGeometry && c.found {
		button, source, found = captionToScreen(c.button, window), c.source, true
	}
	c.host, c.window, c.dpi, c.maximized = host, window, dpi, maximized
	c.style = style
	c.checked, c.source, c.details, c.found = time.Now(), source, details, found
	c.button = rect{button.Left - window.Left, button.Top - window.Top, button.Right - window.Left, button.Bottom - window.Top}
	return window, button, source, details, found
}

func captionToScreen(button, window rect) rect {
	return rect{button.Left + window.Left, button.Top + window.Top, button.Right + window.Left, button.Bottom + window.Top}
}

func captionAnchor(host uintptr, window rect, dpi int) (rect, string, status.CaptionProbe, bool) {
	var details status.CaptionProbe
	// WM_GETTITLEBARINFOEX provides the minimize button itself in screen space.
	// Bound the cross-process message wait so a busy host cannot hang our UI.
	info := titlebarInfoEx{Size: uint32(unsafe.Sizeof(titlebarInfoEx{}))}
	var result uintptr
	details.TitlebarOK = call(user32, "SendMessageTimeoutW", host, 0x033F, 0, uintptr(unsafe.Pointer(&info)), 0x2|0x20, 15, uintptr(unsafe.Pointer(&result))) != 0
	details.TitlebarBounds, details.TitlebarState = layoutRect(info.Parts[2]), info.State[2]
	if details.TitlebarOK {
		button := info.Parts[2]
		if info.State[2]&(0x8000|0x10000) == 0 && validCaption(window, button) {
			return button, "titlebar_info", details, true
		}
	}
	// Save the raw DWM result as well, including when the custom-caption probe
	// succeeds. These bounds are window-relative and may be empty for Electron.
	var buttons rect
	details.DWMResult = int32(call(dwmapi, "DwmGetWindowAttribute", host, 5, uintptr(unsafe.Pointer(&buttons)), uintptr(unsafe.Sizeof(buttons))))
	details.DWMBounds = layoutRect(buttons)
	// Electron's Window Controls Overlay draws its own caption buttons. Its
	// WinCaptionButtonContainer exposes HTMINBUTTON through WM_NCHITTEST even
	// when TITLEBARINFOEX and DWM report no system buttons. Ask the host directly;
	// querying does not depend on which window is under the mouse or in front.
	deadline := time.Now().Add(60 * time.Millisecond)
	button, found := layout.FindMinimize(layoutRect(window), dpi, func(x, y int) (int, bool) {
		if time.Now().After(deadline) {
			details.BudgetExceeded = true
			return 0, false
		}
		details.HitTestQueries++
		var code uintptr
		ok, err := callWithError(user32, "SendMessageTimeoutW", host, 0x0084, 0, uintptr(layout.HitTestPoint(x, y)), 0x2|0x20, 10, uintptr(unsafe.Pointer(&code)))
		if ok == 0 {
			if errno, ok := err.(syscall.Errno); ok {
				details.HitTestError = uint32(errno)
			}
			return 0, false
		}
		if code < 32 {
			details.HitTestCodes |= 1 << code
		}
		return int(code), true
	})
	if found {
		return rect{int32(button.X), int32(button.Y), int32(button.X + button.W), int32(button.Y + button.H)}, "nonclient_hit_test", details, true
	}
	// Standard caption groups can also be supplied by DWM. Their left edge is
	// the docking boundary; the returned rectangle is window-relative.
	if details.DWMResult >= 0 {
		buttons = captionToScreen(buttons, window)
		if validCaption(window, buttons) {
			return buttons, "dwm_caption_buttons", details, true
		}
	}
	return rect{}, "unavailable", details, false
}

func validCaption(window, button rect) bool {
	return button.width() > 0 && button.height() >= 16 && button.Left > window.Left+int32(window.width()/2) && button.Right <= window.Right && button.Top >= window.Top && button.Bottom <= window.Top+int32(min(window.height(), 200))
}

func (a *overlay) linkOwner() bool {
	if call(user32, "GetWindow", a.hwnd, 4) == a.host {
		return true
	}
	call(user32, "SetWindowLongPtrW", a.hwnd, signed(-8), a.host)
	return call(user32, "GetWindow", a.hwnd, 4) == a.host
}

// Keep the overlay in the host's Z-order group, including when Codex is in the
// background. Never put a background host's strip above the foreground app.
func (a *overlay) positionSurface(pX, pY, width, height int) bool {
	fg := call(user32, "GetForegroundWindow")
	after := uintptr(0) // HWND_TOP, within the host's non-topmost group.
	if fg != a.host && fg != a.hwnd {
		after = call(user32, "GetWindow", a.host, 3) // GW_HWNDPREV.
		if after == a.hwnd {
			return call(user32, "SetWindowPos", a.hwnd, 0, signed(pX), signed(pY), uintptr(width), uintptr(height), 0x10|0x40|0x200|0x4) != 0
		}
	}
	return call(user32, "SetWindowPos", a.hwnd, after, signed(pX), signed(pY), uintptr(width), uintptr(height), 0x10|0x40|0x200) != 0
}
