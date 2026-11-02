package win

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
	"unsafe"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/i18n"
	"codex-usage-monitor/internal/layout"
	"codex-usage-monitor/internal/metrics"
	popupstate "codex-usage-monitor/internal/popup"
	"codex-usage-monitor/internal/presentation"
	"codex-usage-monitor/internal/source"
	"codex-usage-monitor/internal/status"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

type overlay struct {
	text                                   directText
	surface                                overlaySurface
	hwnd, host                             uintptr
	monitor                                uintptr
	pid                                    uint32
	configPath                             string
	version                                string
	cfg                                    config.Config
	dpi                                    int
	placement                              layout.Placement
	hasPlacement, shown, expanded          bool
	data                                   dataUpdate
	mu                                     sync.Mutex
	pending                                *dataUpdate
	geometryPending                        atomic.Bool
	lastButtons                            int16
	lastPaint                              paintKey
	stripWidthKey                          stripWidthKey
	stripWidth                             int
	dark                                   bool
	lastHostWidth, lastHostHeight          int
	lastBounds                             rect
	dirty                                  bool
	statusData                             metrics.Snapshot
	statusHealth                           healthKey
	statusReason                           string
	statusWritten                          time.Time
	opaquePixels                           int
	captionBounds                          rect
	captionSource                          string
	captionDetector                        captionDetector
	captionProbe                           status.CaptionProbe
	zOrderDirty                            bool
	viewRequests                           chan viewRequest
	viewResults                            chan viewProbe
	viewSelections                         chan viewSelection
	viewPending                            bool
	viewEpoch, viewPendingEpoch            uint64
	selectionDirty, viewEvents, probeArmed bool
	probeDelay                             time.Duration
	viewRequested                          time.Time
	viewRequestHost                        uintptr
	viewResult                             viewSelection
	popup                                  popupstate.State
	popupEventPending                      atomic.Bool
}

var currentOverlay *overlay
var overlayProc = callback(func(h uintptr, m uint32, w, l uintptr) uintptr {
	a := currentOverlay
	if a != nil {
		switch m {
		case wmUpdate:
			a.mu.Lock()
			u := a.pending
			a.pending = nil
			a.mu.Unlock()
			if u != nil && (u.ID == "" || u.ID == a.viewResult.ID && (a.viewResult.Host == "" || u.Host == a.viewResult.Host)) {
				if u.ID != a.data.ID {
					a.collapse()
				}
				a.data = *u
				a.recordStatus(a.statusReason, nil)
				if !a.hasPlacement || a.paintKey(a.placement.W, a.placement.H, a.dpi) != a.lastPaint {
					a.dirty = true
					a.refresh(false)
				}
			}
			for {
				select {
				case probe := <-a.viewResults:
					v := probe.Selection
					if v.Window == a.viewRequestHost && probe.Epoch == a.viewPendingEpoch {
						a.viewPending = false
					}
					if probe.Epoch != a.viewEpoch {
						a.requestView(true)
						continue
					}
					a.viewEvents = probe.Events
					if v.Window == a.host {
						if hostSurfaceVisible(a.host) {
							a.popup.Observe(uint64(a.host), probe.Popup)
							a.setView(v)
						}
						a.refresh(false)
					}
				default:
					return 0
				}
			}
		case wmGeometry:
			a.geometryPending.Store(false)
			a.zOrderDirty = true
			a.refresh(false)
			return 0
		case wmTimer:
			if w == 2 {
				a.pollOutside()
				return 0
			}
			if w == 4 {
				call(user32, "KillTimer", h, 4)
				a.probeArmed = false
				a.probeDelay = 0
				a.checkPopup(true)
				return 0
			}
			a.refresh(false)
			a.checkPopup(false)
			delay := 2000
			if !a.viewEvents {
				delay = 500
			}
			if a.popup.Hidden {
				delay = 250
			}
			call(user32, "SetTimer", h, 1, uintptr(delay), 0)
			return 0
		case wmPopup:
			a.popupEventPending.Store(false)
			a.queueProbe(100 * time.Millisecond)
			return 0
		case wmViewChanged:
			if w != a.host {
				return 0
			}
			uiaPending.Store(false)
			kind := uiaKinds.Swap(0)
			if kind&uiaSelection != 0 {
				a.viewEpoch++
				a.selectionDirty = true
				a.setView(viewSelection{Window: a.host, Reason: "resolving_selected_chat"})
				a.requestView(true)
				a.refresh(false)
			} else if kind != 0 {
				a.queueProbe(500 * time.Millisecond)
			}
			return 0
		case wmReposition:
			a.hasPlacement = false
			a.refresh(true)
			return 0
		case wmLButtonUp:
			// Only the capsule toggles the panel. Clicks inside the panel
			// itself never collapse it or activate the host's underlying controls.
			p := point{int32(int16(uint16(l))), int32(int16(uint16(l >> 16)))}
			s := a.placement.Strip
			if (rect{int32(s.X), int32(s.Y), int32(s.X + s.W), int32(s.Y + s.H)}).contains(p) {
				a.expanded = !a.expanded
				a.refresh(true)
				if a.expanded {
					a.lastButtons = 0
					call(user32, "SetTimer", h, 2, 50, 0)
				} else {
					call(user32, "KillTimer", h, 2)
				}
			}
			return 0
		case wmRButtonUp:
			a.menu()
			return 0
		case wmMouseActivate:
			return 3 // MA_NOACTIVATE: preserve composer keyboard focus.
		case wmDpiChanged:
			a.refresh(true)
			return 0
		case wmSettingChange, wmUISettingsChanged:
			if m == wmSettingChange {
				a.text.invalidateRendering()
			}
			cfg, err := config.Load(a.configPath)
			if err != nil {
				return 0
			}
			a.cfg.Language, a.cfg.Theme = cfg.Language, cfg.Theme
			a.dark = darkTheme(a.cfg)
			// Persist invalidation before refresh: a popup or minimized host may
			// prevent painting now, but restoration must use the new language.
			a.lastPaint, a.dirty = paintKey{}, true
			if m == wmUISettingsChanged {
				// Acknowledge the reload before probing another process's window.
				// The queued refresh uses the invalidated bitmap even if the
				// settings change happened while the overlay was hidden.
				post(a.hwnd, wmGeometry)
				return 1
			}
			a.refresh(true)
			return 0
		case wmClose:
			call(user32, "DestroyWindow", h)
			return 0
		case wmDestroy:
			call(user32, "PostQuitMessage", 0)
			return 0
		}
	}
	return call(user32, "DefWindowProcW", h, uintptr(m), w, l)
})

var hostEventProc = eventCallback(func(hook uintptr, event uint32, h uintptr, object, child int32, thread, timestamp uint32) uintptr {
	a := currentOverlay
	if a == nil {
		return 0
	}
	if event == 0x800b && (object != 0 || child != 0) {
		return 0
	}
	if event == 3 || h == a.host || a.host == 0 {
		if a.geometryPending.CompareAndSwap(false, true) {
			post(a.hwnd, wmGeometry)
		}
	}
	return 0
})

func RunOverlay(pid uint32, configPath, version string) error {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	setDPIAware()
	mutex, e := windows.CreateMutex(nil, false, utf(fmt.Sprintf(`Local\CodexUsageMonitor.Overlay.%d`, pid)))
	if errors.Is(e, windows.ERROR_ALREADY_EXISTS) {
		windows.CloseHandle(mutex)
		return nil
	}
	if e != nil {
		return e
	}
	defer windows.CloseHandle(mutex)
	h, e := windows.OpenProcess(windows.PROCESS_QUERY_LIMITED_INFORMATION|windows.SYNCHRONIZE, false, pid)
	if e != nil {
		return e
	}
	defer windows.CloseHandle(h)
	if !isCodex(h) {
		return errors.New("host process is not the OpenAI.Codex package")
	}
	cfg, e := config.Load(configPath)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a := &overlay{pid: pid, cfg: cfg, configPath: configPath, version: version, dpi: 96}
	a.viewRequests, a.viewResults, a.viewSelections = make(chan viewRequest, 1), make(chan viewProbe, 1), make(chan viewSelection, 1)
	defer a.text.close()
	defer a.surface.close()
	currentOverlay = a
	defer func() { currentOverlay = nil }()
	var workers sync.WaitGroup
	defer func() {
		uiaTarget.Store(0)
		uiaHost.Store(0)
		cancel()
		workers.Wait()
	}()
	if e = registerClass("CodexUsageMonitor.Overlay", overlayProc); e != nil {
		return e
	}
	a.hwnd, e = createWindow("CodexUsageMonitor.Overlay", 0x80000|0x80|0x08000000, 0x80000000)
	if e != nil {
		return e
	}
	a.dark = darkTheme(cfg)
	workers.Add(1)
	go func() {
		defer workers.Done()
		viewWorker(ctx, dataHome(cfg), a.viewRequests, func(v viewProbe) {
			select {
			case a.viewResults <- v:
				post(a.hwnd, wmUpdate)
			case <-ctx.Done():
			}
		})
	}()
	// OUTOFCONTEXT callbacks stay in this process; no code enters Codex.
	moveHook := call(user32, "SetWinEventHook", 0x800B, 0x800B, 0, hostEventProc, uintptr(pid), 0, 0x2)
	foregroundHook := call(user32, "SetWinEventHook", 3, 3, 0, hostEventProc, 0, 0, 0)
	popupHook := call(user32, "SetWinEventHook", 0x8002, 0x8005, 0, popupEventProc, uintptr(pid), 0, 0x2)
	menuHook := call(user32, "SetWinEventHook", 6, 7, 0, popupEventProc, uintptr(pid), 0, 0x2)
	dialogHook := call(user32, "SetWinEventHook", 0x10, 0x11, 0, popupEventProc, uintptr(pid), 0, 0x2)
	defer call(user32, "UnhookWinEvent", moveHook)
	defer call(user32, "UnhookWinEvent", foregroundHook)
	defer call(user32, "UnhookWinEvent", popupHook)
	defer call(user32, "UnhookWinEvent", menuHook)
	defer call(user32, "UnhookWinEvent", dialogHook)
	call(user32, "SetTimer", a.hwnd, 1, 500, 0)
	uiaTarget.Store(a.hwnd)
	defer uiaTarget.Store(0)
	changes := make(chan struct{}, 1)
	workers.Add(2)
	go func() { defer workers.Done(); _ = watchDirectory(ctx, dataHome(cfg), "config.toml", changes) }()
	go func() {
		defer workers.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-changes:
				post(a.hwnd, wmUISettingsChanged)
			}
		}
	}()
	workers.Add(1)
	go func() {
		defer workers.Done()
		engine(ctx, cfg, configPath, a.viewSelections, func(u dataUpdate) { a.mu.Lock(); a.pending = &u; a.mu.Unlock(); post(a.hwnd, wmUpdate) })
	}()
	stop, e := windows.OpenEvent(windows.SYNCHRONIZE, false, utf(stopName))
	if e != nil {
		stop = 0
	}
	if stop != 0 {
		defer windows.CloseHandle(stop)
	}
	// Retain these handles until the wait finishes. A local event also releases
	// the wait if the user closes this overlay before Codex exits.
	local, e := windows.CreateEvent(nil, 1, 0, nil)
	if e != nil {
		return e
	}
	defer windows.CloseHandle(local)
	waitDone := make(chan struct{})
	go func() {
		defer close(waitDone)
		hs := []windows.Handle{h, local}
		if stop != 0 {
			hs = append(hs, stop)
		}
		windows.WaitForMultipleObjects(hs, false, windows.INFINITE)
		post(a.hwnd, wmClose)
	}()
	a.refresh(true)
	e = messageLoop()
	uiaTarget.Store(0)
	uiaHost.Store(0)
	cancel()
	windows.SetEvent(local)
	<-waitDone
	return e
}

func (a *overlay) collapse() {
	if a.expanded {
		a.surface.close()
		a.dirty = true
	}
	a.expanded = false
	call(user32, "KillTimer", a.hwnd, 2)
}
func (a *overlay) hideWindow() {
	if a.shown {
		call(user32, "ShowWindow", a.hwnd, 0)
		call(user32, "SetWindowPos", a.hwnd, signed(-2), 0, 0, 0, 0, 0x10|0x1|0x2)
		a.shown = false
	}
	a.collapse()
}

func (a *overlay) hide(reason string, e error) {
	a.hideWindow()
	a.hasPlacement = false
	a.recordStatus(reason, e)
}

func (a *overlay) refresh(force bool) {
	if a.host == 0 || call(user32, "IsWindow", a.host) == 0 {
		a.host = findHostWindow(a.pid)
		a.hasPlacement = false
	}
	if a.host == 0 {
		a.hide("host_not_found", nil)
		return
	}
	if !hostSurfaceVisible(a.host) {
		a.hide("host_hidden", nil)
		return
	}
	fg := call(user32, "GetForegroundWindow")
	if fg != a.host && eligibleHostWindow(fg, a.pid) {
		a.host = fg
		a.hasPlacement = false
	}
	a.requestView(false)
	if a.popup.Window != uint64(a.host) {
		a.popup.Observe(uint64(a.host), popupstate.Observation{Source: "checking", At: time.Now()})
	}
	if source := nativePopup(a.host); source != "" {
		a.popup.Observe(uint64(a.host), popupstate.Observation{Available: true, Visible: true, Source: source, At: time.Now()})
	}
	if a.popup.Hidden {
		a.hide("popup_open", nil)
		return
	}
	if a.viewResult.Window != a.host || !a.viewResult.ChatPage {
		a.hide("non_chat_page", nil)
		return
	}
	if !a.linkOwner() {
		a.hide("owner_link_failed", nil)
		return
	}
	host, ok := clientRect(a.host)
	if !ok {
		a.hide("geometry_failed", nil)
		return
	}
	dpi := dpiFor(a.host)
	previousDPI := a.dpi
	a.dpi = dpi // Record the actual host DPI even if placement is unavailable.
	window, minimize, source, details, found := a.captionDetector.locate(a.host, dpi, force)
	a.captionBounds, a.captionSource, a.captionProbe = minimize, source, details
	if !found {
		a.hide("caption_not_found", nil)
		return
	}
	p, fits := layout.Titlebar(layoutRect(window), layoutRect(host), layoutRect(minimize), dpi, scale(layout.StripWidth, dpi), a.expanded)
	if !fits {
		a.hide("insufficient_space", nil)
		return
	}
	requestedWidth, err := a.desiredStripWidth()
	if err != nil {
		if errors.Is(err, errTextDoesNotFit) {
			a.hide("insufficient_space", err)
		} else {
			a.hide("paint_failed", err)
		}
		return
	}
	if requestedWidth != p.W {
		p, fits = layout.Titlebar(layoutRect(window), layoutRect(host), layoutRect(minimize), dpi, requestedWidth, a.expanded)
	}
	if !fits {
		a.hide("insufficient_space", nil)
		return
	}
	if a.expanded && !p.Expanded {
		a.collapse()
	}
	monitor := call(user32, "MonitorFromWindow", a.host, 2)
	shapeChanged := !a.hasPlacement || dpi != previousDPI || monitor != a.monitor || host.width() != a.lastHostWidth || host.height() != a.lastHostHeight
	a.monitor = monitor
	a.lastHostWidth, a.lastHostHeight = host.width(), host.height()
	a.placement, a.hasPlacement = p, true
	if force || shapeChanged || a.dirty || !a.shown {
		key := a.paintKey(p.W, p.H, dpi)
		if force || key != a.lastPaint {
			if e := a.paint(key.Model); e != nil {
				if errors.Is(e, errTextDoesNotFit) {
					a.hide("insufficient_space", e)
					return
				}
				a.hide("paint_failed", e)
				return
			}
			a.lastPaint = key
		}
		a.dirty = false
	}
	// Ownership keeps this surface above Codex while respecting other apps.
	bounds := rect{int32(p.X), int32(p.Y), int32(p.X + p.W), int32(p.Y + p.H)}
	if !a.shown || bounds != a.lastBounds || a.zOrderDirty {
		if !a.positionSurface(p.X, p.Y, p.W, p.H) {
			a.hide("show_failed", errors.New("SetWindowPos failed"))
			return
		}
		a.lastBounds = bounds
		a.zOrderDirty = false
	}
	a.shown = true
	a.recordStatus("visible", nil)
}

type stripWidthKey struct {
	DPI      int
	Language string
	Model    presentation.Model
}

// Width planning shares the native text-layout cache. Unchanged idle geometry
// notifications do not rebuild candidates or remeasure text. Expanded-only
// changes also reuse this plan, keeping both surfaces aligned while opening.
func (a *overlay) desiredStripWidth() (int, error) {
	if a.stripWidth > 0 && !a.dirty && a.stripWidthKey.DPI == a.dpi && a.stripWidthKey.Language == a.cfg.Language {
		return a.stripWidth, nil
	}
	model := presentation.Display(a.data.Snapshot, a.cfg.Language, a.data.ID, a.data.Message, a.data.Loading, a.data.Live.Unsupported, false)
	key := stripWidthKey{a.dpi, a.cfg.Language, model}
	if a.stripWidth > 0 && key == a.stripWidthKey {
		return a.stripWidth, nil
	}
	if err := a.text.ensure(a.dpi, a.cfg.Language); err != nil {
		return 0, err
	}
	var measureError error
	width, ok := presentation.FitStripWidth(presentation.StripAlternatives(a.data.Snapshot, a.cfg.Language), a.dpi, func(value string) int {
		n, err := a.text.width(value, textRegular)
		if err != nil {
			measureError = err
			return -1
		}
		return n
	})
	if measureError != nil {
		return 0, measureError
	}
	if !ok {
		return 0, errTextDoesNotFit
	}
	a.stripWidth, a.stripWidthKey = width, key
	return width, nil
}

type paintKey struct {
	Width, Height, DPI int
	Monitor            uintptr
	Expanded, Dark     bool
	Language           string
	Model              presentation.Model
}

func (a *overlay) paintKey(width, height, dpi int) paintKey {
	return paintKey{width, height, dpi, a.monitor, a.expanded, a.dark, a.cfg.Language, presentation.Display(a.data.Snapshot, a.cfg.Language, a.data.ID, a.data.Message, a.data.Loading, a.data.Live.Unsupported, a.expanded)}
}

func (a *overlay) statusSnapshot(reason string, e error) status.Report {
	fg := call(user32, "GetForegroundWindow")
	var fgPID uint32
	call(user32, "GetWindowThreadProcessId", fg, uintptr(unsafe.Pointer(&fgPID)))
	detail := ""
	if e != nil {
		detail = e.Error()
	}
	var actual rect
	call(user32, "GetWindowRect", a.hwnd, uintptr(unsafe.Pointer(&actual)))
	r := status.Report{ProcessID: windows.GetCurrentProcessId(), HostPID: a.pid, HostWindow: uint64(a.host), ForegroundWindow: uint64(fg), ForegroundPID: fgPID, Visible: a.shown, Reason: reason, DPI: a.dpi, Bounds: a.placement.Rect, HasUsage: a.data.Snapshot.HasUsage, HasTiming: a.data.Snapshot.HasTiming, Error: detail}
	r.NativeVisible = call(user32, "IsWindowVisible", a.hwnd) != 0
	r.ActualBounds = layout.Rect{X: int(actual.Left), Y: int(actual.Top), W: actual.width(), H: actual.height()}
	r.HostTopmost = call(user32, "GetWindowLongPtrW", a.host, signed(-20))&0x8 != 0
	r.OverlayTopmost = call(user32, "GetWindowLongPtrW", a.hwnd, signed(-20))&0x8 != 0
	r.OpaquePixels = a.opaquePixels
	r.PlacementMode = "titlebar-right"
	r.CaptionBounds = layoutRect(a.captionBounds)
	r.CaptionSource = a.captionSource
	r.CaptionProbe = a.captionProbe
	r.OwnerLinked = call(user32, "GetWindow", a.hwnd, 4) == a.host
	r.IPCConnected = a.data.Connected
	r.DataObservedAt = a.data.ObservedAt
	r.DataMessage, r.DataLoading = a.data.Message, a.data.Loading
	r.UsageStale = a.data.Snapshot.UsageStale
	r.Load = a.data.Timing
	r.RecoveredBoundaries = a.data.RecoveredBoundaries
	r.LiveAvailable = a.data.Live.Available
	r.LiveFrames = a.data.Live.Frames
	r.LiveRevision = a.data.Live.Revision
	r.LiveResyncs = a.data.Live.Resyncs
	r.SkippedFrames = a.data.Live.Skipped
	r.LiveUnsupported = a.data.Live.Unsupported
	r.BranchVerified = a.data.BranchVerified
	r.SourcePathMapped, r.SourceKind = a.data.SourceMapped, a.data.SourceKind
	r.ViewSource, r.ViewReason, r.ViewSelectedElements = a.viewResult.Source, a.viewResult.Reason, a.viewResult.SelectedElements
	r.ChatPage = a.viewResult.ChatPage
	r.ViewPageRequired, r.ViewHeaderProbe = a.viewResult.PageEvidenceRequired, a.viewResult.HeaderProbe
	r.PopupAvailable, r.PopupHidden, r.PopupSource = a.popup.Available, a.popup.Hidden, a.popup.Source
	r.TPS, r.TPSApproximate = a.data.Snapshot.TPS, a.data.Snapshot.TPSApproximate
	r.TurnCount, r.StepCount, r.CountsKnown, r.CountsComplete = a.data.Snapshot.Turns, a.data.Snapshot.Steps, a.data.Snapshot.HasCounts, a.data.Snapshot.CountsComplete
	r.DecodeMS, r.DecodeSamples, r.DecodeSkipped = a.data.Snapshot.DecodeMS, a.data.Snapshot.DecodeSamples, a.data.Snapshot.DecodeSkipped
	r.TimingComplete, r.TimingLimited = a.data.Snapshot.TimingComplete, a.data.Snapshot.TimingLimited
	r.OwnerVerified = a.data.Live.OwnerVerified
	r.HistoryComplete, r.Malformed, r.Oversized = a.data.Snapshot.HistoryComplete, a.data.Snapshot.Malformed, a.data.Snapshot.Oversized
	r.MissingRecords = a.data.Snapshot.MissingRecords
	var cloaked uint32
	if int32(call(dwmapi, "DwmGetWindowAttribute", a.hwnd, 14, uintptr(unsafe.Pointer(&cloaked)), 4)) == 0 {
		r.Cloaked = cloaked != 0
	}
	if a.shown {
		p := point{int32(a.placement.X + a.placement.Strip.X + a.placement.Strip.W/2), int32(a.placement.Y + a.placement.Strip.Y + a.placement.Strip.H/2)}
		packed := uintptr(uint64(uint32(p.X)) | uint64(uint32(p.Y))<<32)
		hit := call(user32, "WindowFromPoint", packed)
		root := call(user32, "GetAncestor", hit, 2)
		r.SampleWindow = uint64(root)
		r.SampleHitsOverlay = root == a.hwnd
		call(user32, "GetWindowThreadProcessId", root, uintptr(unsafe.Pointer(&r.SamplePID)))
		var class [128]uint16
		call(user32, "GetClassNameW", root, uintptr(unsafe.Pointer(&class[0])), uintptr(len(class)))
		r.SampleClass = windows.UTF16ToString(class[:])
	}
	return r
}

type healthKey struct {
	Generation                                        uint64
	TimingComplete, TimingLimited, OwnerVerified      bool
	Reason, Error, Data                               string
	Connected, Loading, Unsupported, Complete, Counts bool
	Malformed, Oversized, MissingRecords              int64
	RecoveredBoundaries                               int
	UsageStale                                        bool
	LoadMode                                          string
	CountsKnown                                       bool
	ViewReason                                        string
	ChatPage                                          bool
	ViewPageRequired                                  bool
	ViewHeaderProbe                                   [3]int
}

func (a *overlay) recordStatus(reason string, err error) {
	key := healthKey{Reason: reason, Data: a.data.Message, Connected: a.data.Connected, Loading: a.data.Loading, Unsupported: a.data.Live.Unsupported, Complete: a.data.Snapshot.HistoryComplete, Counts: a.data.Snapshot.CountsComplete, Malformed: a.data.Snapshot.Malformed, Oversized: a.data.Snapshot.Oversized}
	key.Generation = a.data.Generation
	key.TimingComplete, key.TimingLimited, key.OwnerVerified = a.data.Snapshot.TimingComplete, a.data.Snapshot.TimingLimited, a.data.Live.OwnerVerified
	key.RecoveredBoundaries = a.data.RecoveredBoundaries
	key.UsageStale = a.data.Snapshot.UsageStale
	key.MissingRecords = a.data.Snapshot.MissingRecords
	key.LoadMode = a.data.Timing.Mode
	key.CountsKnown = a.data.Snapshot.HasCounts
	key.ChatPage = a.viewResult.ChatPage
	key.ViewReason, key.ViewPageRequired, key.ViewHeaderProbe = a.viewResult.Reason, a.viewResult.PageEvidenceRequired, a.viewResult.HeaderProbe
	if err != nil {
		key.Error = err.Error()
	}
	sameHealth := key == a.statusHealth
	if sameHealth && a.statusData.Equal(a.data.Snapshot) {
		return
	}
	if sameHealth && time.Since(a.statusWritten) < 2*time.Second {
		return
	}
	report := a.statusSnapshot(reason, err)
	if status.Write(filepath.Join(filepath.Dir(a.configPath), "overlay-status.json"), report) == nil {
		a.statusData = a.data.Snapshot
		a.statusHealth = key
		a.statusReason = reason
		a.statusWritten = time.Now()
	}
}

func (a *overlay) pollOutside() {
	if !a.expanded {
		return
	}
	var p point
	call(user32, "GetCursorPos", uintptr(unsafe.Pointer(&p)))
	buttons := int16(call(user32, "GetAsyncKeyState", 1)) | int16(call(user32, "GetAsyncKeyState", 2))
	escape := int16(call(user32, "GetAsyncKeyState", 0x1B)) < 0
	local := point{p.X - int32(a.placement.X), p.Y - int32(a.placement.Y)}
	strip, panel := a.placement.Strip, a.placement.Panel
	inside := rect{int32(strip.X), int32(strip.Y), int32(strip.X + strip.W), int32(strip.Y + strip.H)}.contains(local) || rect{int32(panel.X), int32(panel.Y), int32(panel.X + panel.W), int32(panel.Y + panel.H)}.contains(local)
	if escape || (buttons < 0 && a.lastButtons >= 0 && !inside) {
		a.collapse()
		a.refresh(true)
	}
	a.lastButtons = buttons
}

func (a *overlay) menu() {
	a.collapse()
	tr := func(s string) string { return i18n.Text(a.cfg.Language, s) }
	enabled, _ := StartupStatus(a.configPath)
	switch popup(a.hwnd, []menuEntry{{1, tr("Status"), false}, {2, tr("Settings"), false},
		{3, tr("Start with Windows"), enabled}, {5, tr("Reposition"), false}, {0, "", false}, {4, tr("Quit"), false}}) {
	case 1:
		reason := a.statusReason
		if a.shown {
			reason = "visible"
		}
		report := a.statusSnapshot(reason, nil)
		text := presentation.Diagnostics(a.cfg.Language, report)
		_ = status.Write(filepath.Join(filepath.Dir(a.configPath), "overlay-status.json"), report)
		messageBox(tr("Status"), text+"\n\n"+tr("Version")+": "+a.version)
	case 2:
		showSettings(a.hwnd, a.configPath)
	case 3:
		if err := Startup(!enabled, a.configPath); err != nil {
			messageBox(tr("Startup setting failed"), err.Error())
		}
	case 5:
		a.hasPlacement = false
	case 4:
		_ = Stop()
		post(a.hwnd, wmClose)
	}
	a.refresh(true)
}

func darkTheme(cfg config.Config) bool {
	if cfg.Theme == "dark" {
		return true
	}
	if cfg.Theme == "light" {
		return false
	}
	// Read only the relevant [desktop] setting; never decode or log the whole
	// config, which may contain unrelated provider settings.
	if value := desktopTheme(source.JoinPath(dataHome(cfg), "config.toml")); value != "" {
		return value == "dark"
	}
	k, e := registry.OpenKey(registry.CURRENT_USER, `Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if e != nil {
		return false
	}
	defer k.Close()
	v, _, e := k.GetIntegerValue("AppsUseLightTheme")
	return e == nil && v == 0
}
