package textlayout

import "math"

type Bounds struct{ Left, Top, Right, Bottom float64 }

// PixelBounds includes trailing whitespace, fallback glyph ink overhangs and a
// physical-pixel antialias margin. Overhangs are relative to the layout box,
// not the logical text width. Measurement and drawing use these same bounds.
func PixelBounds(advance, height, boxWidth, boxHeight float32, overhang [4]float32) (Bounds, bool) {
	values := []float32{advance, height, boxWidth, boxHeight, overhang[0], overhang[1], overhang[2], overhang[3]}
	for _, v := range values {
		if math.IsNaN(float64(v)) || math.IsInf(float64(v), 0) {
			return Bounds{}, false
		}
	}
	if advance < 0 || height <= 0 || boxWidth <= 0 || boxHeight <= 0 {
		return Bounds{}, false
	}
	b := Bounds{
		math.Floor(min(0, -float64(overhang[0]))) - 1,
		math.Floor(min(0, -float64(overhang[1]))) - 1,
		math.Ceil(max(float64(advance), float64(boxWidth)+float64(overhang[2]))) + 1,
		math.Ceil(max(float64(height), float64(boxHeight)+float64(overhang[3]))) + 1,
	}
	w, h := b.Right-b.Left, b.Bottom-b.Top
	return b, w > 0 && w <= 65536 && h > 0 && h <= 8192
}

func (b Bounds) Width() int  { return int(b.Right - b.Left) }
func (b Bounds) Height() int { return int(b.Bottom - b.Top) }

// Origin centres the measured ink bounds on whole physical pixels. Drawing
// never scales a pre-rendered bitmap, including at fractional display DPI.
func (b Bounds) Origin(x, y, height int) (float32, float32) {
	return float32(float64(x) - b.Left), float32(float64(y) + math.Floor(float64(height-b.Height())/2) - b.Top)
}
