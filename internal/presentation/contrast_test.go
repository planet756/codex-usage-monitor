package presentation

import (
	"math"
	"testing"
)

func luminance(rgb uint32) float64 {
	linear := func(n uint32) float64 {
		v := float64(n) / 255
		if v <= .04045 {
			return v / 12.92
		}
		return math.Pow((v+.055)/1.055, 2.4)
	}
	return .2126*linear((rgb>>16)&255) + .7152*linear((rgb>>8)&255) + .0722*linear(rgb&255)
}

func TestSmallTextContrastBothThemesAndSurfaces(t *testing.T) {
	for _, dark := range []bool{false, true} {
		p := Colors(dark)
		for _, bg := range []uint32{p.Strip, p.Panel} {
			for _, fg := range []uint32{p.Text, p.Muted} {
				a, b := luminance(bg), luminance(fg)
				ratio := (max(a, b) + .05) / (min(a, b) + .05)
				if ratio < 7 {
					t.Fatalf("12 DIP text contrast %.2f below 7:1 dark=%v", ratio, dark)
				}
			}
		}
	}
}
