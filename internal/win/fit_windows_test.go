package win

import (
	"codex-usage-monitor/internal/layout"
	"codex-usage-monitor/internal/metrics"
	"codex-usage-monitor/internal/presentation"
	"math"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"unsafe"
)

// Native off-screen tests: cross-compiling in WSL does not execute them.
// Native execution requires no Codex, window creation or user input.
func TestNativeStripFitsAtUniform12DIPAcrossDPIAndLanguages(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var text directText
	defer text.close()
	for _, dpi := range []int{96, 120, 144, 168, 192} {
		for _, lang := range []string{"en", "zh-CN"} {
			if err := text.ensure(dpi, lang); err != nil {
				t.Fatal(err)
			}
			measure := func(value string) int {
				n, err := text.width(value, textRegular)
				if err != nil {
					t.Fatal(err)
				}
				return n
			}
			for _, complete := range []bool{true, false} {
				for _, n := range []int64{0, 1999, 123456, 1999999, 999999999, 1000000000000, math.MaxInt64} {
					for _, rate := range []float64{31.6, 208, 999999, 9.223372036854776e21} {
						s := metrics.Snapshot{HasCounts: true, CountsComplete: complete, Turns: n, Steps: n, TPS: &rate, TPSApproximate: true}
						s = s.WithUsage(metrics.Usage{Input: n, Cached: n}, true)
						options := presentation.StripAlternatives(s, lang)
						wanted, ok := presentation.FitStripWidth(options, dpi, measure)
						if !ok {
							t.Fatalf("no native width retains labels dpi=%d lang=%s count=%d rate=%g", dpi, lang, n, rate)
						}
						for _, w := range []int{320, 360, 400, 480} {
							columns := layout.SplitStrip(scale(w, dpi), scale(26, dpi), dpi)
							chosen, fits := presentation.SelectStrip(options, columns.Left.W-scale(1, dpi), columns.Right.W-scale(1, dpi), measure)
							if !fits && scale(w, dpi) >= wanted {
								t.Fatalf("native fit failed dpi=%d lang=%s complete=%v count=%d rate=%g width=%d", dpi, lang, complete, n, rate, w)
							}
							if !fits {
								continue
							}
							if lang == "en" && (!strings.Contains(chosen.Left[0], "turn") || !strings.Contains(chosen.Left[0], "step")) || lang == "zh-CN" && (!strings.Contains(chosen.Left[0], "轮") || !strings.Contains(chosen.Left[0], "步")) {
								t.Fatal("count labels shortened")
							}
							if strings.TrimSpace(chosen.Left[3]) != "tok/s" || strings.Contains(strings.Join(chosen.Left[:], ""), "≈") {
								t.Fatal("strip TPS unit or approximation display policy changed")
							}
							label := "Cache hit "
							if lang == "zh-CN" {
								label = "缓存"
							}
							if !strings.Contains(chosen.Right[1], label) {
								t.Fatalf("native fit removed cache label: %q", chosen.Right)
							}
						}
					}
				}
			}
		}
	}
}

func TestNativeDirectWritePaintsExactProductionMatrix(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a := overlay{}
	defer a.text.close()
	defer a.surface.close()
	for _, dpi := range []int{96, 120, 144, 192} {
		for _, lang := range []string{"en", "zh-CN"} {
			for _, w := range []int{320, 360, 400} {
				for _, dark := range []bool{false, true} {
					for _, complete := range []bool{true, false} {
						a.dpi, a.cfg.Language, a.dark = dpi, lang, dark
						rate := 9.223372036854776e21
						s := metrics.Snapshot{HasCounts: true, CountsComplete: complete, Turns: math.MaxInt64, Steps: math.MaxInt64, TPS: &rate, TPSApproximate: true, HistoryComplete: complete, DecodeSamples: 1234567890123, DecodeSkipped: 1234567890123}
						s = s.WithUsage(metrics.Usage{Input: math.MaxInt64, Cached: math.MaxInt64}, true)
						a.data.Snapshot = s
						a.dirty = true
						wanted, err := a.desiredStripWidth()
						if err != nil {
							t.Fatal(err)
						}
						a.placement = nativeTestPlacement(max(w, (wanted*96+dpi/2)/dpi), dpi, true)
						model := presentation.Display(s, lang, "01a15da4-16e9-7951-8fce-50f7bef8bb83", "", false, false, true)
						if err := a.drawContent(model); err != nil {
							t.Fatalf("dpi=%d lang=%s width=%d dark=%v: %v", dpi, lang, w, dark, err)
						}
						if call(gdi32, "GdiFlush") == 0 {
							t.Fatal("flush off-screen draw")
						}
						palette := presentation.Colors(dark)
						pixels := unsafe.Slice((*uint32)(unsafe.Pointer(a.surface.bits)), a.placement.W*a.placement.H)
						ink := 0
						for y := 4; y < scale(22, dpi); y++ {
							for x := scale(14, dpi); x < a.placement.W/2-scale(8, dpi); x++ {
								if pixels[y*a.placement.W+x]&0xffffff != palette.Strip {
									ink++
								}
							}
						}
						if ink == 0 {
							t.Fatal("DirectWrite produced no visible glyphs")
						}
						if len(a.surface.frameLayouts) != 0 {
							t.Fatal("EndDraw retained frame references")
						}
					}
				}
			}
		}
	}
}

func nativeTestPlacement(w, dpi int, expanded bool) layout.Placement {
	p := layout.Placement{Rect: layout.Rect{W: scale(w, dpi), H: scale(26, dpi)}, Strip: layout.Rect{W: scale(w, dpi), H: scale(26, dpi)}}
	if expanded {
		p.Expanded = true
		p.Panel = layout.Rect{Y: p.H + scale(8, dpi), W: p.W, H: scale(layout.DetailHeight, dpi)}
		p.H = p.Panel.Y + p.Panel.H
	}
	return p
}

func TestNativeEmptyChatPaintsBothMetricGroupsAcrossWaitingStates(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a := overlay{}
	defer a.text.close()
	defer a.surface.close()
	for _, dpi := range []int{96, 120, 144, 168, 192} {
		for _, language := range []string{"en", "zh-CN"} {
			for _, connected := range []bool{false, true} {
				for _, expanded := range []bool{false, true} {
					for _, message := range []string{"Waiting for current chat identity", "New chat · waiting for chat ID"} {
						a.dpi, a.cfg.Language, a.dirty = dpi, language, true
						a.data.Snapshot = metrics.Snapshot{}
						a.data.ID, a.data.Message, a.data.Live.Available = "", message, connected
						w, err := a.desiredStripWidth()
						if err != nil {
							t.Fatal(err)
						}
						a.placement = nativeTestPlacement((w*96+dpi/2)/dpi, dpi, expanded)
						model := presentation.Display(a.data.Snapshot, language, "", message, false, false, expanded)
						if err := a.drawContent(model); err != nil {
							t.Fatalf("empty chat failed dpi=%d language=%s connected=%v expanded=%v: %v", dpi, language, connected, expanded, err)
						}
						if call(gdi32, "GdiFlush") == 0 {
							t.Fatal("flush empty-chat draw")
						}
						pixels := unsafe.Slice((*uint32)(unsafe.Pointer(a.surface.bits)), a.placement.W*a.placement.H)
						columns := layout.SplitStrip(a.placement.W, a.placement.Strip.H, dpi)
						for _, group := range []layout.Rect{columns.Left, columns.Right} {
							ink := 0
							for y := scale(4, dpi); y < scale(22, dpi); y++ {
								for x := group.X; x < group.X+group.W; x++ {
									if pixels[y*a.placement.W+x]&0xffffff != presentation.Colors(false).Strip {
										ink++
									}
								}
							}
							if ink == 0 {
								t.Fatal("waiting state replaced a metric group")
							}
						}
					}
				}
			}
		}
	}
}

func TestNativeFallbackTextFitsDetailBandsAtUniform12DIP(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var text directText
	defer text.close()
	for _, dpi := range []int{96, 120, 144, 168, 192} {
		for _, language := range []string{"en", "zh-CN"} {
			if err := text.ensure(dpi, language); err != nil {
				t.Fatal(err)
			}
			for _, value := range []string{"Resolving the selected chat", "正在识别当前会话", "样本：1/2 · 跳过 1 · 历史不完整", "𠮷 e\u0301 gypq"} {
				l, err := text.layout(value, textRegular)
				if err != nil || l.bounds.Height() > scale(layout.DetailTextHeight, dpi) {
					t.Fatalf("12-DIP fallback exceeds detail text band dpi=%d lang=%s: %+v %v", dpi, language, l, err)
				}
				if dpi == 96 && language == "zh-CN" {
					t.Logf("fallback ink height=%d, detail band=%d", l.bounds.Height(), layout.DetailTextHeight)
				}
			}
		}
	}
}

func TestNativeMeasuredWidthCachesIdleGeometryAndTracksLanguageAndDPI(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a := overlay{dpi: 144, dirty: true}
	a.cfg.Language = "en"
	defer a.text.close()
	defer a.surface.close()
	rate := 208.0
	a.data.Snapshot = metrics.Snapshot{HasCounts: true, CountsComplete: true, Turns: 12, Steps: 133, TPS: &rate, TPSApproximate: true}.WithUsage(metrics.Usage{Input: 1000}, true)
	w, err := a.desiredStripWidth()
	if err != nil || w < scale(layout.StripWidth, a.dpi) || w > scale(layout.StripMaxWidth, a.dpi) {
		t.Fatal("could not plan full labels", w, err)
	}
	a.dirty = false
	if allocs := testing.AllocsPerRun(100, func() {
		if got, err := a.desiredStripWidth(); err != nil || got != w {
			t.Fatal("idle width changed", got, err)
		}
	}); allocs != 0 {
		t.Fatalf("idle width planning allocates: %g", allocs)
	}
	a.cfg.Language = "zh-CN"
	if _, err := a.desiredStripWidth(); err != nil || a.stripWidthKey.Language != "zh-CN" {
		t.Fatal("language did not invalidate width", err)
	}
	a.dpi = 192
	if _, err := a.desiredStripWidth(); err != nil || a.stripWidthKey.DPI != 192 {
		t.Fatal("DPI did not invalidate width", err)
	}
	a.data.Snapshot.Turns, a.data.Snapshot.Steps = math.MaxInt64, math.MaxInt64
	a.data.Snapshot.CountsComplete = false
	a.dirty = true
	if _, err := a.desiredStripWidth(); err != nil {
		t.Fatal("large counts cannot retain labels", err)
	}
}

func TestNativeDirectWriteCacheTargetReuseAndRelease(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	a := overlay{dpi: 144}
	a.cfg.Language = "en"
	a.placement = nativeTestPlacement(400, 144, false)
	defer a.text.close()
	defer a.surface.close()
	model := presentation.Display(metrics.Snapshot{}, "en", "", "", false, false, false)
	if err := a.drawContent(model); err != nil {
		t.Fatal(err)
	}
	target, dc := a.surface.target, a.surface.dc
	params := a.text.renderingParams
	l, err := a.text.layout("100.00%", false)
	if err != nil {
		t.Fatal(err)
	}
	for range 100 {
		a.placement.X++
		if err := a.drawContent(model); err != nil {
			t.Fatal(err)
		}
		cached, err := a.text.layout("100.00%", false)
		if err != nil || cached != l || a.surface.target != target || a.surface.dc != dc || a.text.renderingParams != params {
			t.Fatal("motion/update recreated text resources")
		}
	}
	closed := a.placement
	a.placement = nativeTestPlacement(400, 144, true)
	bad := presentation.Display(metrics.Snapshot{}, "en", "", "", false, false, true)
	bad.Rows[0].Value = strings.Repeat("x", 2048)
	if err := a.drawContent(bad); err == nil {
		t.Fatal("over-budget text accepted")
	}
	if len(a.surface.frameLayouts) != 0 {
		t.Fatal("failed frame retained references")
	}
	a.placement = closed
	if err := a.drawContent(model); err != nil {
		t.Fatal("could not recover from failed frame", err)
	}
	a.text.invalidateRendering()
	if err := a.drawContent(model); err != nil || a.text.renderingParams == nil {
		t.Fatal("rendering settings did not recover", err)
	}
	a.surface.close()
	if a.surface.target != nil || a.surface.brush != nil {
		t.Fatal("collapse retained target")
	}
	a.text.close()
	a.text.close()
	if a.text.factory != nil || a.text.drawingFactory != nil || a.text.renderingParams != nil || a.text.cache != nil {
		t.Fatal("close retained text resources")
	}
}

func TestNativeTextFallbackSpacesAndUniformEmSize(t *testing.T) {
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	var text directText
	defer text.close()
	for _, dpi := range []int{96, 120, 144, 168, 192} {
		for _, language := range []string{"en", "zh-CN"} {
			if err := text.ensure(dpi, language); err != nil {
				t.Fatal(err)
			}
			for _, format := range text.formats {
				// Win64 float return is in XMM0 (SyscallN's second result).
				_, bits, _ := syscall.SyscallN(unsafe.Slice(format.vtable, 26)[25], uintptr(unsafe.Pointer(format)))
				if got := math.Float32frombits(uint32(bits)); got != 12*float32(dpi)/96 {
					t.Fatalf("font is not 12 DIP: %g", got)
				}
			}
			for _, value := range []string{"≥9e18轮·≥9e18步", "≈9e21", "100.00%", "9,223,372,036,854,775,807", "轮步缓存命中率", "gypq  ", "  ", "\t", "e\u0301", "𠮷"} {
				for _, bold := range []bool{false, true} {
					l, err := text.layout(value, bold)
					if err != nil {
						t.Fatal(err)
					}
					n, err := text.width(value, bold)
					if err != nil || n != l.bounds.Width() || n <= 0 {
						t.Fatal("measuring and drawing disagree")
					}
					cached, err := text.layout(value, bold)
					if err != nil || cached != l {
						t.Fatal("measurement did not reuse drawing layout")
					}
				}
			}
			for _, value := range []string{"invalid\x00metadata", strings.Repeat("x", 2048), "two\nlines"} {
				if _, err := text.layout(value, false); err == nil {
					t.Fatal("invalid metadata accepted")
				}
			}
		}
	}
}
