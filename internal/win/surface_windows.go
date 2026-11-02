package win

import (
	"codex-usage-monitor/internal/layout"
	"fmt"
	"unsafe"
)

type surfaceKey struct {
	W, H, DPI    int
	Strip, Panel layout.Rect
	Expanded     bool
}
type overlaySurface struct {
	key                        surfaceKey
	dc, bitmap, previous, bits uintptr
	target, brush              *comObject
	frameLayouts               []*comObject
	rows                       []layout.PixelSpan
	opaque                     int
	regionDirty                bool
}

func (s *overlaySurface) close() {
	s.closeText()
	if s.dc != 0 {
		call(gdi32, "SelectObject", s.dc, s.previous)
	}
	if s.bitmap != 0 {
		call(gdi32, "DeleteObject", s.bitmap)
	}
	if s.dc != 0 {
		call(gdi32, "DeleteDC", s.dc)
	}
	*s = overlaySurface{}
}

func (s *overlaySurface) ensure(p layout.Placement, dpi int) error {
	key := surfaceKey{p.W, p.H, dpi, p.Strip, p.Panel, p.Expanded}
	if key == s.key && s.dc != 0 {
		return nil
	}
	s.close()
	s.dc = call(gdi32, "CreateCompatibleDC", 0)
	if s.dc == 0 {
		return fmt.Errorf("CreateCompatibleDC failed")
	}
	info := bitmapInfo{Header: bitmapHeader{Size: 40, Width: int32(p.W), Height: -int32(p.H), Planes: 1, BitCount: 32}}
	s.bitmap = call(gdi32, "CreateDIBSection", s.dc, uintptr(unsafe.Pointer(&info)), 0, uintptr(unsafe.Pointer(&s.bits)), 0, 0)
	if s.bitmap == 0 || s.bits == 0 {
		s.close()
		return fmt.Errorf("CreateDIBSection failed")
	}
	s.previous = call(gdi32, "SelectObject", s.dc, s.bitmap)
	if s.previous == 0 || s.previous == ^uintptr(0) {
		s.previous = 0
		s.close()
		return fmt.Errorf("SelectObject DIB failed")
	}
	s.key = key
	s.rows, s.opaque = layout.ShapeRows(p, dpi)
	s.regionDirty = true
	return nil
}
