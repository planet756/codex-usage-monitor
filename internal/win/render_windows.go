package win

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unsafe"

	"codex-usage-monitor/internal/i18n"
	"codex-usage-monitor/internal/layout"
	"codex-usage-monitor/internal/presentation"
)

type bitmapHeader struct {
	Size                   uint32
	Width, Height          int32
	Planes, BitCount       uint16
	Compression, SizeImage uint32
	XPels, YPels           int32
	Used, Important        uint32
}
type bitmapInfo struct {
	Header bitmapHeader
	Colors [1]uint32
}
type size struct{ Width, Height int32 }
type blend struct{ Op, Flags, Alpha, Format byte }

var errTextDoesNotFit = errors.New("statistics exceed available display bounds")

// drawContent renders into the off-screen DIB without a window or Desktop.
// The Windows regression matrix uses this exact production path.
func (a *overlay) drawContent(model presentation.Model) (err error) {
	tr := func(v string) string { return i18n.Text(a.cfg.Language, v) }
	p, s := a.placement, func(n int) int { return scale(n, a.dpi) }
	if p.W <= 0 || p.H <= 0 || p.W > 4_000_000/p.H {
		return fmt.Errorf("invalid overlay surface size")
	}
	if err = a.text.ensure(a.dpi, a.cfg.Language); err != nil {
		return err
	}
	if err = a.surface.ensure(p, a.dpi); err != nil {
		return err
	}
	if err = a.surface.beginText(&a.text, a.monitor); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, a.surface.endText()) }()
	palette := presentation.Colors(a.dark)
	fg, muted := palette.Text, palette.Muted
	a.surface.fill(rect{Right: int32(p.W), Bottom: int32(p.H)}, palette.Panel)
	a.surface.fill(rect{int32(p.Strip.X), int32(p.Strip.Y), int32(p.Strip.X + p.Strip.W), int32(p.Strip.Y + p.Strip.H)}, palette.Strip)
	type span struct {
		value string
		bold  bool
		color uint32
	}
	var textError error
	remember := func(e error) {
		if textError == nil {
			textError = e
		}
	}
	width := func(v span) int {
		n, e := a.text.width(v.value, v.bold)
		if e != nil {
			remember(e)
			return -1
		}
		return n
	}
	measure := func(vs []span) int {
		n := 0
		for _, v := range vs {
			m := width(v)
			if m < 0 {
				return p.W + 1
			}
			n += m
		}
		return n
	}
	inline := func(x, top, height int, vs []span) {
		for _, v := range vs {
			n := width(v)
			if textError != nil {
				return
			}
			if e := a.surface.drawText(&a.text, x, top, height, v.value, v.bold, v.color); e != nil {
				remember(fmt.Errorf("text band y=%d height=%d: %w", top, height, e))
			}
			x += n
		}
	}
	regular := func(value string) int { return width(span{value, textRegular, 0}) }
	// Only explanatory status/identity strings may ellipsize. Counters and
	// numeric values must fit in full after the shared candidate selection.
	label := func(x, top, width, height int, value string) {
		if regular(value) > width {
			runes := []rune(value)
			lo, hi := 0, len(runes)
			for lo < hi {
				mid := (lo + hi + 1) / 2
				if regular(string(runes[:mid])+"…") <= width {
					lo = mid
				} else {
					hi = mid - 1
				}
			}
			value = string(runes[:lo]) + "…"
		}
		if regular(value) > width {
			remember(errTextDoesNotFit)
			return
		}
		inline(x, top, height, []span{{value, textRegular, muted}})
	}
	columns := layout.SplitStrip(p.W, p.Strip.H, a.dpi)
	chosen, ok := presentation.SelectStrip(presentation.StripAlternatives(a.data.Snapshot, a.cfg.Language),
		columns.Left.W-s(1), columns.Right.W-s(1), regular)
	if textError != nil {
		return textError
	}
	if !ok {
		return fmt.Errorf("strip width=%d: %w", p.W, errTextDoesNotFit)
	}
	first := []span{{chosen.Left[0], textRegular, fg}, {chosen.Left[1], textRegular, muted}, {chosen.Left[2], textRegular, fg}, {chosen.Left[3], textRegular, muted}}
	second := []span{{chosen.Right[0], textRegular, fg}, {chosen.Right[1], textRegular, muted}, {chosen.Right[2], textRegular, fg}}
	for i, group := range [][]span{first, second} {
		area := columns.Left
		if i == 1 {
			area = columns.Right
		}
		n := measure(group)
		if n > area.W {
			return fmt.Errorf("strip group=%d measured=%d available=%d: %w", i, n, area.W, errTextDoesNotFit)
		}
		// Fixed starts keep short values from growing the left or centre gap.
		inline(area.X, p.Strip.Y, p.Strip.H, group)
	}
	a.surface.rule(columns.Divider, p.Strip.Y+s(7), columns.Divider, p.Strip.Y+p.Strip.H-s(7), palette.Border)
	cx, cy := p.W-s(10), p.Strip.Y+p.Strip.H/2
	if p.Expanded {
		a.surface.rule(cx-s(3), cy+s(1), cx, cy-s(2), muted)
		a.surface.rule(cx, cy-s(2), cx+s(3), cy+s(1), muted)
	} else {
		a.surface.rule(cx-s(3), cy-s(1), cx, cy+s(2), muted)
		a.surface.rule(cx, cy+s(2), cx+s(3), cy-s(1), muted)
	}
	if p.Expanded {
		py, left, right := p.Panel.Y, p.Panel.X+s(16), p.Panel.X+p.Panel.W-s(16)
		title := span{tr("Current chat usage"), textBold, fg}
		gap := regular("  ")
		countValue, ok := presentation.SelectText(presentation.CountAlternatives(a.data.Snapshot, a.cfg.Language),
			right-left-width(title)-gap-s(1), regular)
		if textError != nil {
			return textError
		}
		if !ok {
			return fmt.Errorf("detail title width=%d: %w", right-left, errTextDoesNotFit)
		}
		inline(left, py+s(10), s(26), []span{title, {"  " + countValue, textRegular, muted}})
		identity := model.Identity
		if identity == "" {
			identity = tr("Resolving the selected chat")
		}
		// Segoe UI's CJK fallback can exceed an 18-DIP line at 12 DIP. Use
		// the reserved row band so the measured ink fits without shrinking type.
		label(left, py+s(layout.DetailIdentityY), right-left, s(layout.DetailTextHeight), identity)
		for i, row := range model.Rows {
			var vs []span
			fits := false
			for _, option := range presentation.DetailAlternatives(row, a.cfg.Language) {
				vs = []span{{option.Value, textRegular, fg}}
				if option.Value == presentation.MissingValue {
					vs[0].color = muted
				}
				if option.Unit != "" {
					vs = append(vs, span{" " + option.Unit, textRegular, muted})
				}
				if regular(option.Label)+s(12)+measure(vs) <= right-left-s(1) {
					row, fits = option, true
					break
				}
			}
			if textError != nil {
				return textError
			}
			if !fits {
				return fmt.Errorf("detail row=%d width=%d: %w", i, right-left, errTextDoesNotFit)
			}
			y := py + s(layout.DetailRows[i])
			inline(left, y, s(24), []span{{row.Label, textRegular, muted}})
			inline(right-measure(vs), y, s(24), vs)
		}
		ruleY := py + s(layout.DetailRuleY)
		a.surface.rule(left, ruleY, right, ruleY, palette.Rule)
		status := tr(model.Coverage)
		if model.Coverage == presentation.SampleCoverage(a.data.Snapshot, a.cfg.Language) {
			var ok bool
			status, ok = presentation.SelectText(presentation.CoverageAlternatives(a.data.Snapshot, a.cfg.Language), right-left-s(1), regular)
			if textError != nil {
				return textError
			}
			if !ok {
				return fmt.Errorf("sample coverage width=%d: %w", right-left, errTextDoesNotFit)
			}
			inline(left, py+s(layout.DetailFooterY), s(layout.DetailTextHeight), []span{{status, textRegular, muted}})
		} else {
			label(left, py+s(layout.DetailFooterY), right-left, s(layout.DetailTextHeight), status)
		}
		a.surface.stroke(rect{int32(p.Panel.X), int32(p.Panel.Y), int32(p.Panel.X + p.Panel.W), int32(p.Panel.Y + p.Panel.H)}, s(8), palette.Border)
	}
	return textError
}

func (a *overlay) paint(model presentation.Model) error {
	if err := a.drawContent(model); err != nil {
		return err
	}
	p := a.placement
	s := func(n int) int { return scale(n, a.dpi) }
	w, h := p.W, p.H
	dc, bits := a.surface.dc, a.surface.bits
	pixels := unsafe.Slice((*uint32)(unsafe.Pointer(bits)), w*h)
	// EndDraw copies the opaque Direct2D content to our DIB. Complete any GDI
	// transfer before updating alpha in memory for UpdateLayeredWindow.
	if call(gdi32, "GdiFlush") == 0 {
		return fmt.Errorf("GdiFlush failed")
	}
	// Layered-window hit testing passes alpha-zero pixels through. A matching
	// region additionally excludes the transparent gap between panel and strip.
	radius := s(6)
	for y, row := range a.surface.rows {
		clear(pixels[y*w : y*w+row.Left])
		for x := row.Left; x < row.Right; x++ {
			pixels[y*w+x] |= 0xff000000
		}
		clear(pixels[y*w+row.Right : (y+1)*w])
	}
	position := point{int32(p.X), int32(p.Y)}
	dimensions := size{int32(w), int32(h)}
	origin := point{}
	alpha := blend{Alpha: 255, Format: 1}
	if r, e := callWithError(user32, "UpdateLayeredWindow", a.hwnd, 0, uintptr(unsafe.Pointer(&position)), uintptr(unsafe.Pointer(&dimensions)), dc, uintptr(unsafe.Pointer(&origin)), 0, uintptr(unsafe.Pointer(&alpha)), 2); r == 0 {
		return fmt.Errorf("UpdateLayeredWindow: %w", e)
	}
	a.opaquePixels = a.surface.opaque
	// Install the region after UpdateLayeredWindow has established the actual
	// window dimensions, including the first paint and expand/collapse changes.
	if a.surface.regionDirty {
		region := call(gdi32, "CreateRoundRectRgn", uintptr(p.Strip.X), uintptr(p.Strip.Y), uintptr(p.Strip.X+p.Strip.W+1), uintptr(p.Strip.Y+p.Strip.H+1), uintptr(radius*2), uintptr(radius*2))
		if p.Expanded {
			panel := call(gdi32, "CreateRoundRectRgn", uintptr(p.Panel.X), uintptr(p.Panel.Y), uintptr(p.Panel.X+p.Panel.W+1), uintptr(p.Panel.Y+p.Panel.H+1), uintptr(s(16)), uintptr(s(16)))
			call(gdi32, "CombineRgn", region, region, panel, 2)
			call(gdi32, "DeleteObject", panel)
		}
		if call(user32, "SetWindowRgn", a.hwnd, region, 0) == 0 {
			call(gdi32, "DeleteObject", region)
			return fmt.Errorf("SetWindowRgn failed")
		}
		a.surface.regionDirty = false
	}
	return nil
}

func desktopTheme(path string) string {
	f, e := os.Open(path)
	if e != nil {
		return ""
	}
	defer f.Close()
	sc := bufio.NewScanner(io.LimitReader(f, 4<<20))
	section := ""
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			section = line
			continue
		}
		if section != "[desktop]" {
			continue
		}
		key, value, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "appearanceTheme" {
			continue
		}
		value, _, _ = strings.Cut(value, "#")
		value = strings.Trim(strings.TrimSpace(value), "\"'")
		if value == "light" || value == "dark" {
			return value
		}
	}
	return ""
}
