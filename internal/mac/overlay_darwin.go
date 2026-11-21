package mac

import (
	"context"
	"errors"
	"fmt"
	"os/signal"
	"strings"
	"time"

	"codex-usage-monitor/internal/config"
	"codex-usage-monitor/internal/i18n"
	"codex-usage-monitor/internal/monitor"
	"codex-usage-monitor/internal/popup"
	"codex-usage-monitor/internal/presentation"
	"codex-usage-monitor/internal/releasecheck"
	"codex-usage-monitor/internal/source"
	"github.com/ebitengine/purego"
	"github.com/ebitengine/purego/objc"
	"golang.org/x/sys/unix"
)

type overlay struct {
	pid                                                         uint32
	configPath, version                                         string
	ctx                                                         context.Context
	releaseCheck                                                *releasecheck.Job
	cfg                                                         config.Config
	window, strip, delegate, settings, language, theme, startup objc.ID
	labels                                                      []objc.ID
	font                                                        objc.ID
	observerState
	requests          chan uint64
	results           chan observation
	view              monitor.View
	data              monitor.Update
	observed          observation
	popup             popup.State
	expanded, visible bool
	lastProbe         time.Time
	lastModel         presentation.Model
	lastFrame         rect
	hasModel          bool
	lastDark          bool
	epoch             uint64
	geometryAt        time.Time
}

var activeOverlay *overlay
var axEventCallback = purego.NewCallback(func(_ uintptr, element uintptr, notification uintptr, _ uintptr) {
	if a := activeOverlay; a != nil {
		name := cfString(notification)
		if (name == "AXMoved" || name == "AXResized") && a.observerWindow != 0 && native.equal(element, a.observerWindow) {
			p := pool()
			if r, ok := axRect(element); ok {
				a.geometryAt = time.Now()
				a.observed.Bounds.X = int(r.Origin.X)
				a.observed.Bounds.Y = int(r.Origin.Y)
				a.observed.Bounds.W = int(r.Size.W)
				a.observed.Bounds.H = int(r.Size.H)
				a.refresh()
			}
			p.Send(sel("drain"))
			return
		}
		if name == "AXMiniaturized" {
			a.observed.Minimized = true
			a.hide()
		}
		a.requestProbe()
	}
})

func (a *overlay) requestProbe() {
	a.epoch++
	select {
	case a.requests <- a.epoch:
	default:
		select {
		case <-a.requests:
		default:
		}
		select {
		case a.requests <- a.epoch:
		default:
		}
	}
}

func (a *overlay) hide() {
	if a.visible {
		a.window.Send(sel("orderOut:"), objc.ID(0))
		a.visible = false
	}
	a.expanded = false
}

// RunOverlay is one host-bound worker. Cancelling it joins the IPC, AX and
// file-reader workers before returning to the silent lifecycle watcher.
func RunOverlay(pid uint32, configPath, version string) error {
	lock, err := processLock(configPath, fmt.Sprintf("overlay-%d", pid))
	if errors.Is(err, unix.EWOULDBLOCK) {
		return nil
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	if err := startApplication(); err != nil {
		return err
	}
	p := pool()
	defer p.Send(sel("drain"))
	if !hostAlive(pid) {
		return errors.New("host process is not Codex Desktop")
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		return err
	}
	home, err := source.DirectoryPOSIX(config.Home(cfg))
	if err != nil {
		return err
	}
	ctx, cancel := signal.NotifyContext(context.Background(), unix.SIGTERM, unix.SIGINT)
	defer cancel()
	a := &overlay{pid: pid, cfg: cfg, configPath: configPath, version: version, ctx: ctx, requests: make(chan uint64, 1), results: make(chan observation, 1)}
	a.observerState.api = darwinObserver{pid: pid}
	activeOverlay = a
	defer func() { activeOverlay = nil }()
	if err = a.create(); err != nil {
		return err
	}
	defer a.close()
	defer func() { a.releaseCheck.Close() }()
	views := make(chan monitor.View, 1)
	updates := make(chan monitor.Update, 1)
	engineDone, viewDone := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(engineDone)
		monitor.Run(ctx, home, configPath, views, func(u monitor.Update) {
			select {
			case updates <- u:
			case <-ctx.Done():
			}
		}, monitor.Dependencies{
			Dial: dialIPC(home), IndexedPath: indexedRolloutPath, Watch: watchDirectory, Resolve: source.ResolvePOSIX,
		})
	}()
	go func() { defer close(viewDone); probeWorker(ctx, pid, home, a.requests, a.results) }()
	defer func() {
		cancel()
		<-engineDone
		<-viewDone
		for {
			select {
			case o := <-a.results:
				if o.Window != 0 {
					native.release(o.Window)
				}
			default:
				return
			}
		}
	}()
	a.observerState.update(0, time.Now())
	a.requestProbe()
	for ctx.Err() == nil && hostAlive(pid) {
		iteration := pool()
		select {
		case o := <-a.results:
			if o.Epoch != a.epoch {
				if o.Window != 0 {
					native.release(o.Window)
				}
				iteration.Send(sel("drain"))
				continue
			}
			if o.GeometryAt.Before(a.geometryAt) && o.View.Window == a.view.Window {
				o.Bounds = a.observed.Bounds
			}
			a.observed = o
			if o.Window != 0 {
				a.observerState.update(o.Window, time.Now())
				native.release(o.Window)
				a.observed.Window = 0
			}
			a.popup.Observe(uint64(o.View.Window), o.Popup)
			retainView := retainChat(a.view, o, a.popup.Hidden)
			if !retainView && o.View != a.view {
				if o.View.ID != a.view.ID || o.View.Host != a.view.Host {
					a.data = monitor.Update{ID: o.View.ID, Host: o.View.Host}
					a.expanded = false
					a.hasModel = false
				}
				a.view = o.View
				select {
				case views <- o.View:
				default:
					select {
					case <-views:
					default:
					}
					views <- o.View
				}
			}
		default:
		}
		select {
		case u := <-updates:
			if u.ID == a.view.ID && u.Host == a.view.Host {
				a.data = u
			}
		default:
		}
		a.finishReleaseCheck()
		if time.Since(a.lastProbe) >= 500*time.Millisecond {
			a.observerState.update(0, time.Now())
			a.requestProbe()
			a.lastProbe = time.Now()
		}
		a.refresh()
		iteration.Send(sel("drain"))
		pump()
	}
	return nil
}

func (a *overlay) create() error {
	class, err := objc.RegisterClass("CUMOverlayActions", objc.GetClass("NSObject"), nil, nil, []objc.MethodDef{
		{Cmd: sel("toggle:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
			if activeOverlay != nil {
				activeOverlay.expanded = !activeOverlay.expanded
				activeOverlay.hasModel = false
				activeOverlay.refresh()
			}
		}},
		{Cmd: sel("menu:"), Fn: func(_ objc.ID, _ objc.SEL, sender objc.ID) {
			if activeOverlay != nil {
				activeOverlay.action(int(sender.Send(sel("tag"))))
			}
		}},
		{Cmd: sel("settingsChanged:"), Fn: func(_ objc.ID, _ objc.SEL, _ objc.ID) {
			if activeOverlay != nil {
				activeOverlay.saveSettings()
			}
		}},
		{Cmd: sel("windowWillClose:"), Fn: func(_ objc.ID, _ objc.SEL, notification objc.ID) {
			if activeOverlay != nil {
				a := activeOverlay
				if notification.Send(sel("object")) == a.settings && hostAlive(a.pid) {
					app := objc.ID(objc.GetClass("NSRunningApplication")).Send(sel("runningApplicationWithProcessIdentifier:"), int32(a.pid))
					app.Send(sel("activateWithOptions:"), uint64(0))
				}
				activeOverlay.requestProbe()
			}
		}},
	})
	if err != nil {
		return err
	}
	a.delegate = objc.ID(class).Send(sel("new"))
	buttonClass, err := objc.RegisterClass("CUMStripButton", objc.GetClass("NSButton"), nil, nil, []objc.MethodDef{{Cmd: sel("rightMouseDown:"), Fn: func(self objc.ID, _ objc.SEL, event objc.ID) {
		if activeOverlay != nil {
			activeOverlay.showMenu(self, event)
		}
	}}})
	if err != nil {
		return err
	}
	a.window = objc.ID(objc.GetClass("NSPanel")).Send(sel("alloc")).Send(sel("initWithContentRect:styleMask:backing:defer:"), box(0, 0, 400, 26), uint64(1<<7), uint64(2), false)
	if a.window == 0 {
		return errors.New("native statistics panel unavailable")
	}
	a.window.Send(sel("setReleasedWhenClosed:"), false)
	a.window.Send(sel("setFloatingPanel:"), true)
	a.window.Send(sel("setBecomesKeyOnlyIfNeeded:"), true)
	a.window.Send(sel("setHidesOnDeactivate:"), false)
	a.window.Send(sel("setHasShadow:"), false)
	a.window.Send(sel("setLevel:"), int64(3))
	a.window.Send(sel("setCollectionBehavior:"), uint64((1<<0)|(1<<8))) // Spaces and fullscreen auxiliary.
	a.font = objc.ID(objc.GetClass("NSFont")).Send(sel("systemFontOfSize:"), float64(11))
	a.strip = objc.ID(buttonClass).Send(sel("alloc")).Send(sel("initWithFrame:"), box(0, 0, 400, 26))
	a.strip.Send(sel("setBordered:"), false)
	a.strip.Send(sel("setFont:"), a.font)
	a.strip.Send(sel("setTarget:"), a.delegate)
	a.strip.Send(sel("setAction:"), sel("toggle:"))
	a.window.Send(sel("contentView")).Send(sel("addSubview:"), a.strip)
	return nil
}

func (a *overlay) close() {
	a.observerState.close()
	if a.settings != 0 {
		a.settings.Send(sel("close"))
		a.settings.Send(sel("release"))
	}
	if a.window != 0 {
		a.window.Send(sel("close"))
		a.window.Send(sel("release"))
	}
	if a.strip != 0 {
		a.strip.Send(sel("release"))
	}
	if a.delegate != 0 {
		a.delegate.Send(sel("release"))
	}
}

func color(rgb uint32) objc.ID {
	return objc.ID(objc.GetClass("NSColor")).Send(sel("colorWithSRGBRed:green:blue:alpha:"), float64(rgb>>16&255)/255, float64(rgb>>8&255)/255, float64(rgb&255)/255, float64(1))
}
func (a *overlay) dark() bool {
	if a.cfg.Theme != "auto" {
		return a.cfg.Theme == "dark"
	}
	defaults := objc.ID(objc.GetClass("NSUserDefaults")).Send(sel("standardUserDefaults"))
	return cfString(uintptr(defaults.Send(sel("stringForKey:"), ns("AppleInterfaceStyle")))) == "Dark"
}
func (a *overlay) measure(value string) int {
	attrs := objc.ID(objc.GetClass("NSDictionary")).Send(sel("dictionaryWithObject:forKey:"), a.font, ns("NSFont"))
	s := objc.Send[size](ns(value), sel("sizeWithAttributes:"), attrs)
	return int(s.W + 0.999)
}

func (a *overlay) refresh() {
	front := objc.ID(objc.GetClass("NSWorkspace")).Send(sel("sharedWorkspace")).Send(sel("frontmostApplication"))
	if uint32(front.Send(sel("processIdentifier"))) != a.pid || !native.axTrusted() || !a.view.ChatPage || a.popup.Hidden || a.observed.Minimized || a.observed.Bounds.W < 400 {
		a.hide()
		return
	}
	maxWidth := min(480, a.observed.Bounds.W-160)
	width := 320
	var fitted presentation.FittedStrip
	var title string
	fits := false
	for ; width <= maxWidth; width += 8 {
		fitted, fits = presentation.SelectStrip(presentation.StripAlternatives(a.data.Snapshot, a.cfg.Language), (width-36)/2, (width-36)/2, a.measure)
		if fits {
			title = strings.Join(fitted.Left[:], "") + "   │   " + strings.Join(fitted.Right[:], "") + "  ⌄"
			if a.measure(title) <= width-16 {
				break
			}
			fits = false
		}
	}
	if !fits {
		a.hide()
		return
	}
	height := 26
	if a.expanded && a.observed.Bounds.H >= 410 {
		height += 350
	} else {
		a.expanded = false
	}
	screens := objc.ID(objc.GetClass("NSScreen")).Send(sel("screens"))
	if screens.Send(sel("count")) == 0 {
		a.hide()
		return
	}
	primary := objc.Send[rect](screens.Send(sel("objectAtIndex:"), uint64(0)), sel("frame"))
	b := a.observed.Bounds
	frame := box(float64(b.X+b.W-width-8), primary.Origin.Y+primary.Size.H-float64(b.Y+9+height), float64(width), float64(height))
	if frame != a.lastFrame {
		sizeChanged := frame.Size != a.lastFrame.Size
		a.window.Send(sel("setFrame:display:"), frame, true)
		a.lastFrame = frame
		if sizeChanged {
			a.hasModel = false
		}
	}
	model := presentation.Display(a.data.Snapshot, a.cfg.Language, a.data.ID, a.data.Message, a.data.Loading, a.data.Live.Unsupported, a.expanded)
	dark := a.dark()
	if !a.hasModel || model != a.lastModel || dark != a.lastDark {
		palette := presentation.Colors(a.dark())
		a.window.Send(sel("setBackgroundColor:"), color(palette.Panel))
		a.strip.Send(sel("setWantsLayer:"), true)
		a.strip.Send(sel("layer")).Send(sel("setBackgroundColor:"), color(palette.Strip).Send(sel("CGColor")))
		a.strip.Send(sel("setFrame:"), box(0, float64(height-26), float64(width), 26))
		if a.expanded {
			title = strings.TrimSuffix(title, "⌄") + "⌃"
		}
		attributes := newObject("NSMutableDictionary")
		attributes.Send(sel("setObject:forKey:"), a.font, ns("NSFont"))
		attributes.Send(sel("setObject:forKey:"), color(palette.Text), ns("NSColor"))
		attributed := objc.ID(objc.GetClass("NSAttributedString")).Send(sel("alloc")).Send(sel("initWithString:attributes:"), ns(title), attributes)
		a.strip.Send(sel("setAttributedTitle:"), attributed)
		attributed.Send(sel("release"))
		attributes.Send(sel("release"))
		if a.expanded {
			a.drawDetails(model, width, height, palette)
		} else {
			a.clearLabels()
		}
		a.lastModel = model
		a.lastDark = dark
		a.hasModel = true
	}
	if !a.visible {
		a.window.Send(sel("orderFrontRegardless"))
		a.visible = true
	}
}

func (a *overlay) clearLabels() {
	for _, label := range a.labels {
		label.Send(sel("removeFromSuperview"))
	}
	a.labels = nil
}
func (a *overlay) label(text string, x, y, w, h float64, rgb uint32) {
	l := objc.ID(objc.GetClass("NSTextField")).Send(sel("labelWithString:"), ns(text))
	l.Send(sel("setFrame:"), box(x, y, w, h))
	l.Send(sel("setFont:"), a.font)
	l.Send(sel("setTextColor:"), color(rgb))
	a.window.Send(sel("contentView")).Send(sel("addSubview:"), l)
	a.labels = append(a.labels, l)
}
func (a *overlay) drawDetails(m presentation.Model, width, height int, p presentation.Palette) {
	a.clearLabels()
	y := float64(height - 26 - 30)
	a.label(i18n.Text(a.cfg.Language, "Current chat usage"), 12, y, float64(width-24), 20, p.Text)
	y -= 24
	a.label(m.Counts, 12, y, float64(width-24), 18, p.Muted)
	y -= 26
	for _, row := range m.Rows {
		chosen := row
		for _, option := range presentation.DetailAlternatives(row, a.cfg.Language) {
			if a.measure(option.Label)+a.measure(option.Value+" "+option.Unit) <= width-44 {
				chosen = option
				break
			}
		}
		value := strings.TrimSpace(chosen.Value + " " + chosen.Unit)
		valueWidth := a.measure(value) + 2
		a.label(chosen.Label, 12, y, float64(width-valueWidth-36), 20, p.Muted)
		a.label(value, float64(width-12-valueWidth), y, float64(valueWidth), 20, p.Text)
		y -= 25
	}
	a.label(i18n.Text(a.cfg.Language, m.Coverage), 12, 12, float64(width-24), 30, p.Muted)
}

func (a *overlay) showMenu(view, event objc.ID) {
	menu := newObject("NSMenu")
	defer menu.Send(sel("release"))
	menu.Send(sel("setAutoenablesItems:"), false)
	updateLabel := "Check for Updates…"
	if a.releaseCheck != nil {
		updateLabel = "Checking for Updates…"
	}
	for i, title := range []string{"Settings", "Status", updateLabel, "Project Homepage", "Quit"} {
		if i == 2 || i == 4 {
			menu.Send(sel("addItem:"), objc.ID(objc.GetClass("NSMenuItem")).Send(sel("separatorItem")))
		}
		item := objc.ID(objc.GetClass("NSMenuItem")).Send(sel("alloc")).Send(sel("initWithTitle:action:keyEquivalent:"), ns(i18n.Text(a.cfg.Language, title)), sel("menu:"), ns(""))
		item.Send(sel("setTarget:"), a.delegate)
		item.Send(sel("setTag:"), int64(i+1))
		menu.Send(sel("addItem:"), item)
		item.Send(sel("release"))
	}
	objc.ID(objc.GetClass("NSMenu")).Send(sel("popUpContextMenu:withEvent:forView:"), menu, event, view)
}
func (a *overlay) action(tag int) {
	switch tag {
	case 1:
		a.openSettings()
	case 2:
		showAlert("Codex Usage Monitor "+a.version, fmt.Sprintf("Desktop PID: %d\nAccessibility: %t\nIPC connected: %t\nCurrent page verified: %t\nHistory branch verified: %t\nAX nodes checked: %d", a.pid, native.axTrusted(), a.data.Connected, a.view.ID != "", a.data.BranchVerified, a.observed.Nodes))
	case 3:
		if a.releaseCheck == nil {
			a.releaseCheck = releasecheck.Start(a.ctx, a.version, nil)
		}
	case 4:
		a.openProjectPage(releasecheck.ProjectURL)
	case 5:
		if err := Stop(a.configPath); err != nil {
			showAlert("Quit", err.Error())
		}
	}
}

func (a *overlay) openSettings() {
	if a.settings == 0 {
		a.settings = objc.ID(objc.GetClass("NSWindow")).Send(sel("alloc")).Send(sel("initWithContentRect:styleMask:backing:defer:"), box(0, 0, 340, 180), uint64(3), uint64(2), false)
		a.settings.Send(sel("setReleasedWhenClosed:"), false)
		a.settings.Send(sel("setTitle:"), ns(i18n.Text(a.cfg.Language, "Settings")))
		a.settings.Send(sel("setDelegate:"), a.delegate)
		root := a.settings.Send(sel("contentView"))
		for i, spec := range []struct {
			label string
			items []string
		}{{"Language", []string{"English", "简体中文"}}, {"Theme", []string{"Auto", "Light", "Dark"}}} {
			y := float64(130 - i*45)
			label := objc.ID(objc.GetClass("NSTextField")).Send(sel("labelWithString:"), ns(i18n.Text(a.cfg.Language, spec.label)))
			label.Send(sel("setFrame:"), box(18, y, 90, 22))
			root.Send(sel("addSubview:"), label)
			control := objc.ID(objc.GetClass("NSPopUpButton")).Send(sel("alloc")).Send(sel("initWithFrame:pullsDown:"), box(115, y, 200, 26), false)
			for _, item := range spec.items {
				control.Send(sel("addItemWithTitle:"), ns(item))
			}
			control.Send(sel("setTarget:"), a.delegate)
			control.Send(sel("setAction:"), sel("settingsChanged:"))
			root.Send(sel("addSubview:"), control)
			if i == 0 {
				a.language = control
			} else {
				a.theme = control
			}
			control.Send(sel("release"))
		}
		a.startup = objc.ID(objc.GetClass("NSButton")).Send(sel("alloc")).Send(sel("initWithFrame:"), box(18, 25, 290, 26))
		a.startup.Send(sel("setButtonType:"), uint64(3))
		title := "Start at login"
		if a.cfg.Language == "zh-CN" {
			title = "登录时启动"
		}
		a.startup.Send(sel("setTitle:"), ns(title))
		a.startup.Send(sel("setTarget:"), a.delegate)
		a.startup.Send(sel("setAction:"), sel("settingsChanged:"))
		root.Send(sel("addSubview:"), a.startup)
		a.startup.Send(sel("release"))
	}
	language := int64(0)
	if a.cfg.Language == "zh-CN" {
		language = 1
	}
	a.language.Send(sel("selectItemAtIndex:"), language)
	theme := int64(0)
	if a.cfg.Theme == "light" {
		theme = 1
	} else if a.cfg.Theme == "dark" {
		theme = 2
	}
	a.theme.Send(sel("selectItemAtIndex:"), theme)
	on := int64(0)
	if startupEnabled(a.cfg) {
		on = 1
	}
	a.startup.Send(sel("setState:"), on)
	a.settings.Send(sel("center"))
	a.settings.Send(sel("makeKeyAndOrderFront:"), objc.ID(0))
	native.app.Send(sel("activateIgnoringOtherApps:"), true)
}
func (a *overlay) saveSettings() {
	language := "en"
	if a.language.Send(sel("indexOfSelectedItem")) == 1 {
		language = "zh-CN"
	}
	theme := "auto"
	switch a.theme.Send(sel("indexOfSelectedItem")) {
	case 1:
		theme = "light"
	case 2:
		theme = "dark"
	}
	cfg, err := config.UpdateUI(a.configPath, language, theme)
	if err == nil {
		err = Startup(a.configPath, a.startup.Send(sel("state")) != 0)
	}
	if err != nil {
		showAlert("Settings", err.Error())
		return
	}
	a.cfg = cfg
	a.cfg.StartAtLogin = new(bool)
	*a.cfg.StartAtLogin = a.startup.Send(sel("state")) != 0
	a.hasModel = false
}
