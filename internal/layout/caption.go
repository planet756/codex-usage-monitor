package layout

// FindMinimize measures a custom caption button using WM_NCHITTEST results.
// hit reads the host at a physical screen point; false aborts discovery (for
// example, on a timeout). No mouse movement, click, focus or UIA tree is needed.
func FindMinimize(window Rect, dpi int, hit func(x, y int) (int, bool)) (Rect, bool) {
	if dpi <= 0 {
		dpi = 96
	}
	s := func(n int) int { return (n*dpi + 48) / 96 }
	x0, x1 := max(window.X+window.W/2, window.X+window.W-s(320)), window.X+window.W
	y0, y1 := window.Y, window.Y+min(window.H, s(96))
	if x1 <= x0 || y1 <= y0 {
		return Rect{}, false
	}
	queries, failed := 0, false
	inside := func(x, y int) bool {
		if failed || queries >= 256 {
			failed = true
			return false
		}
		queries++
		code, ok := hit(x, y)
		failed = !ok
		return ok && code == 8 // HTMINBUTTON, including Electron's custom caption.
	}
	// Start at the usual caption centre, then cover larger/zoomed title bars.
	for _, dy := range []int{20, 36, 8, 52, 68, 84, 94} {
		y := window.Y + s(dy)
		if y >= y1 {
			continue
		}
		for x := x1 - 1; x >= x0; x -= max(1, s(12)) {
			if !inside(x, y) {
				if failed {
					return Rect{}, false
				}
				continue
			}
			// The minimize button is one contiguous rectangle. Refine all
			// four edges to physical pixels instead of assuming a button width.
			first := func(lo, hi int, predicate func(int) bool) int {
				for lo < hi && !failed {
					mid := lo + (hi-lo)/2
					if predicate(mid) {
						hi = mid
					} else {
						lo = mid + 1
					}
				}
				return lo
			}
			left := first(x0, x, func(v int) bool { return inside(v, y) })
			right := first(x, x1, func(v int) bool { return !inside(v, y) })
			top := first(y0, y, func(v int) bool { return inside(x, v) })
			bottom := first(y, y1, func(v int) bool { return !inside(x, v) })
			bounds := Rect{left, top, right - left, bottom - top}
			if failed || bounds.W < s(16) || bounds.H < s(16) {
				return Rect{}, false
			}
			return bounds, true
		}
	}
	return Rect{}, false
}

// HitTestPoint preserves signed 16-bit screen coordinates in a WM_NCHITTEST
// LPARAM, including monitors to the left of or above the primary display.
func HitTestPoint(x, y int) uint32 {
	return uint32(uint16(x)) | uint32(uint16(y))<<16
}
