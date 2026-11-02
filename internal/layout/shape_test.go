package layout

import "testing"

func TestCachedShapeKeepsCornersAndPanelGapTransparent(t *testing.T) {
	p := Placement{Rect: Rect{W: 440, H: 376}, Strip: Rect{W: 440, H: 26}, Panel: Rect{Y: 34, W: 440, H: 342}, Expanded: true}
	rows, opaque := ShapeRows(p, 96)
	if rows[0] != (PixelSpan{6, 434}) || rows[6] != (PixelSpan{0, 440}) || rows[30] != (PixelSpan{}) || rows[34] != (PixelSpan{8, 432}) || opaque <= 0 {
		t.Fatal("rounded corner or transparent gap changed")
	}
	for _, r := range rows {
		if r.Left < 0 || r.Right > 440 || r.Right < r.Left {
			t.Fatal("span escaped the surface")
		}
	}
}
