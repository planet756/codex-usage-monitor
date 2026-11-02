package layout

type PixelSpan struct{ Left, Right int }

// ShapeRows precomputes the opaque horizontal span at each row. The strip and
// panel occupy distinct row bands, including their fully transparent gap.
func ShapeRows(p Placement, dpi int) ([]PixelSpan, int) {
	rows := make([]PixelSpan, p.H)
	inside := func(r Rect, x, y, radius int) bool {
		if x < r.X || y < r.Y || x >= r.X+r.W || y >= r.Y+r.H {
			return false
		}
		cx := max(r.X+radius, min(x, r.X+r.W-radius-1))
		cy := max(r.Y+radius, min(y, r.Y+r.H-radius-1))
		dx, dy := x-cx, y-cy
		return dx*dx+dy*dy <= radius*radius
	}
	n := 0
	for y := range rows {
		left, right := p.W, 0
		for x := 0; x < p.W; x++ {
			if inside(p.Strip, x, y, (6*dpi+48)/96) || (p.Expanded && inside(p.Panel, x, y, (8*dpi+48)/96)) {
				left = min(left, x)
				right = x + 1
			}
		}
		if right > left {
			rows[y] = PixelSpan{left, right}
			n += right - left
		}
	}
	return rows, n
}
