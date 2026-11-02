package win

import (
	"codex-usage-monitor/internal/layout"
	"testing"
)

func TestSurfaceReusesResourcesUntilShapeChanges(t *testing.T) {
	var s overlaySurface
	defer s.close()
	p := layout.Placement{Rect: layout.Rect{W: 440, H: 26}, Strip: layout.Rect{W: 440, H: 26}}
	if err := s.ensure(p, 96); err != nil {
		t.Fatal(err)
	}
	dc, bitmap, bits := s.dc, s.bitmap, s.bits
	s.regionDirty = false
	for range 100 {
		p.X++ // Moving the host must not allocate a new canvas or region.
		if err := s.ensure(p, 96); err != nil {
			t.Fatal(err)
		}
		if s.dc != dc || s.bitmap != bitmap || s.bits != bits || s.regionDirty {
			t.Fatal("unchanged shape recreated native drawing resources")
		}
	}
	p.H, p.Expanded, p.Panel = 376, true, layout.Rect{Y: 34, W: 440, H: 342}
	if err := s.ensure(p, 96); err != nil {
		t.Fatal(err)
	}
	if !s.regionDirty || len(s.rows) != 376 || s.target != nil || s.brush != nil {
		t.Fatal("shape change did not rebuild its derived resources")
	}
	s.close()
	s.close()
	if s.dc != 0 || s.bitmap != 0 || s.bits != 0 || s.rows != nil || s.target != nil || s.brush != nil || s.frameLayouts != nil {
		t.Fatal("close retained resources")
	}
}
