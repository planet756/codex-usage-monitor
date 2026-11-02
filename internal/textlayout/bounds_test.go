package textlayout

import (
	"math"
	"testing"
)

func TestPixelBoundsIncludeFallbackInkTrailingSpacesAndFractionalDPI(t *testing.T) {
	for _, dpi := range []float32{96, 120, 144, 168, 192} {
		s := dpi / 96
		advance, height := float32(100.125)*s, float32(15.25)*s
		left, top, right, bottom := float32(1.125)*s, float32(.25)*s, advance+2.25*s, height+.75*s
		b, ok := PixelBounds(advance, height, 32768, 4096, [4]float32{left, top, right - 32768, bottom - 4096})
		if !ok || b.Left > -float64(left) || b.Top > -float64(top) || b.Right < float64(right) || b.Bottom < float64(bottom) {
			t.Fatalf("glyph ink clipped at DPI %.0f: %+v", dpi, b)
		}
		x, y := b.Origin(12, 5, 40)
		if float64(x)+b.Left != 12 || math.Trunc(float64(y)) != float64(y) {
			t.Fatal("origin lies between physical pixels")
		}
		if float64(y)+b.Top < 5 || float64(y)+b.Bottom > 45 {
			t.Fatal("centering escapes text band")
		}
	}
	// Trailing spaces are advances even when the ink ends much earlier.
	b, ok := PixelBounds(100, 16, 32768, 4096, [4]float32{-2, -2, 50 - 32768, 14 - 4096})
	if !ok || b.Width() != 102 {
		t.Fatal("trailing spaces lost", b)
	}
}

func TestInvalidOrUnboundedMetricsFailClosed(t *testing.T) {
	for _, v := range []float32{float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		if _, ok := PixelBounds(v, 16, 32768, 4096, [4]float32{}); ok {
			t.Fatal("invalid width accepted")
		}
		if _, ok := PixelBounds(20, 16, 32768, 4096, [4]float32{v}); ok {
			t.Fatal("invalid ink accepted")
		}
	}
	for _, width := range []float32{-1, 1e9, 1e30} {
		if _, ok := PixelBounds(width, 16, 32768, 4096, [4]float32{}); ok {
			t.Fatal("unbounded layout accepted")
		}
	}
}
