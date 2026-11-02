package layout

import "testing"

func TestCustomCaptionWithoutNativeBounds(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 168, 192} {
		for _, height := range []int{28, 36, 54, 72} {
			s := func(n int) int { return (n*dpi + 48) / 96 }
			window := Rect{-2400, -150, s(1400), s(900)}
			want := Rect{window.X + window.W - s(145), window.Y + s(5), s(47), s(height)}
			queries := 0
			got, ok := FindMinimize(window, dpi, func(x, y int) (int, bool) {
				queries++
				if x >= want.X && x < want.X+want.W && y >= want.Y && y < want.Y+want.H {
					return 8, true
				}
				if x >= want.X+want.W && y < want.Y+want.H {
					return 9, true // Neighboring maximize button must be excluded.
				}
				return 1, true
			})
			if !ok || got != want || queries > 100 {
				t.Fatalf("dpi=%d height=%d: got %+v, want %+v; %d queries", dpi, height, got, want, queries)
			}
			p, fits := Titlebar(window, window, got, dpi, (StripWidth*dpi+48)/96, true)
			band := TitlebarBand(window, got, dpi)
			if !fits || p.X+p.W >= want.X || p.Y < band.Y || p.Y+p.Strip.H > band.Y+band.H {
				t.Fatalf("custom caption dock would overlap controls: %+v", p)
			}
		}
	}
}

func TestCaptionProbeStopsOnUnresponsiveHostOrMissingButton(t *testing.T) {
	for _, responsive := range []bool{false, true} {
		queries := 0
		_, ok := FindMinimize(Rect{W: 1400, H: 900}, 144, func(x, y int) (int, bool) {
			queries++
			return 1, responsive
		})
		if ok || queries > 256 || (!responsive && queries != 1) {
			t.Fatalf("unexpected discovery result: %t, %d queries", ok, queries)
		}
	}
}

func TestCaptionProbeHandlesTallOffsetCaptionAndRefinementTimeout(t *testing.T) {
	window := Rect{W: 1400, H: 900}
	want := Rect{X: 1262, Y: 49, W: 46, H: 32}
	for _, failAt := range []int{0, 80} {
		queries := 0
		got, ok := FindMinimize(window, 96, func(x, y int) (int, bool) {
			queries++
			if failAt != 0 && queries == failAt {
				return 0, false
			}
			if x >= want.X && x < want.X+want.W && y >= want.Y && y < want.Y+want.H {
				return 8, true
			}
			return 1, true
		})
		if failAt == 0 && (!ok || got != want) {
			t.Fatalf("offset caption not measured: %+v", got)
		}
		if failAt != 0 && (ok || queries != failAt) {
			t.Fatalf("used incomplete caption geometry after timeout: %+v (%d queries)", got, queries)
		}
	}
}

func TestHitTestPointKeepsNegativeMonitorCoordinates(t *testing.T) {
	for _, p := range [][2]int{{-2400, -150}, {1240, 30}, {-20, 800}} {
		packed := HitTestPoint(p[0], p[1])
		if int(int16(uint16(packed))) != p[0] || int(int16(uint16(packed>>16))) != p[1] {
			t.Fatalf("screen point changed: %v -> %#x", p, packed)
		}
	}
}
