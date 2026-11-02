package win

import (
	"fmt"
	"math"
	"strings"
	"unsafe"

	"codex-usage-monitor/internal/textlayout"
	"golang.org/x/sys/windows"
)

// ABI slots and structs follow Microsoft's dwrite.h and d2d1.h. Rendering is
// confined to the overlay's locked OS thread. Factories need no UIA apartment.
var (
	dwrite           = windows.NewLazySystemDLL("dwrite.dll")
	d2d1             = windows.NewLazySystemDLL("d2d1.dll")
	dwriteFactoryIID = windows.GUID{Data1: 0xb859ee5a, Data2: 0xd838, Data3: 0x4b5b, Data4: [8]byte{0xa2, 0xe8, 0x1a, 0xdc, 0x7d, 0x93, 0xdb, 0x48}}
	d2dFactoryIID    = windows.GUID{Data1: 0x06152247, Data2: 0x6f50, Data3: 0x465a, Data4: [8]byte{0x92, 0x45, 0x11, 0x8b, 0xfd, 0x3b, 0x60, 0x07}}
)

const (
	textRegular      = false
	textBold         = true
	textCacheEntries = 128
	textCacheUnits   = 16 * 1024
	textBoxWidth     = float32(32768)
	textBoxHeight    = float32(4096)
)

type dwriteMetrics struct {
	Left, Top, Width, WidthIncludingTrailingWhitespace, Height, LayoutWidth, LayoutHeight float32
	MaxBidiReorderingDepth, LineCount                                                     uint32
}
type d2dTargetProperties struct {
	Type, Format, AlphaMode uint32
	DpiX, DpiY              float32
	Usage, MinLevel         uint32
}
type d2dColor struct{ R, G, B, A float32 }
type d2dRect struct{ Left, Top, Right, Bottom float32 }
type d2dRoundedRect struct {
	Rect             d2dRect
	RadiusX, RadiusY float32
}
type nativeTextLayout struct {
	object *comObject
	bounds textlayout.Bounds
}
type directText struct {
	factory, drawingFactory *comObject
	renderingParams         *comObject
	monitor                 uintptr
	formats                 [2]*comObject
	cache                   *textlayout.Cache[*nativeTextLayout]
	dpi                     int
	language                string
}

func hresult(operation string, result uintptr) error {
	if int32(result) < 0 {
		return fmt.Errorf("%s: HRESULT 0x%08X", operation, uint32(result))
	}
	return nil
}
func releaseCOM(p **comObject) {
	if *p != nil {
		comCall(*p, 2)
		*p = nil
	}
}
func floatArg(f float32) uintptr { return uintptr(math.Float32bits(f)) }
func pointArg(x, y float32) uintptr {
	return uintptr(uint64(math.Float32bits(x)) | uint64(math.Float32bits(y))<<32)
}
func rgba(rgb uint32) d2dColor {
	return d2dColor{float32((rgb>>16)&255) / 255, float32((rgb>>8)&255) / 255, float32(rgb&255) / 255, 1}
}

func (t *directText) clearLayouts() {
	if t.cache != nil {
		t.cache.Close()
		t.cache = nil
	}
	for i := range t.formats {
		releaseCOM(&t.formats[i])
	}
	t.dpi, t.language = 0, ""
}
func (t *directText) close() {
	t.clearLayouts()
	t.invalidateRendering()
	releaseCOM(&t.drawingFactory)
	releaseCOM(&t.factory)
}
func (t *directText) invalidateRendering() {
	releaseCOM(&t.renderingParams)
	t.monitor = 0
}

func (t *directText) ensureRendering(monitor uintptr) error {
	if t.renderingParams != nil && t.monitor == monitor {
		return nil
	}
	var params *comObject
	var result uintptr
	if monitor == 0 {
		result = comCall(t.factory, 10, uintptr(unsafe.Pointer(&params)))
	} else {
		result = comCall(t.factory, 11, monitor, uintptr(unsafe.Pointer(&params)))
	}
	if err := hresult("CreateMonitorRenderingParams", result); err != nil {
		releaseCOM(&params)
		return err
	}
	t.invalidateRendering()
	t.renderingParams, t.monitor = params, monitor
	return nil
}
func (t *directText) ensure(dpi int, language string) (err error) {
	if dpi <= 0 {
		return fmt.Errorf("invalid text DPI")
	}
	defer func() {
		if err != nil {
			t.close()
		}
	}()
	if t.factory == nil {
		if err = hresult("DWriteCreateFactory", call(dwrite, "DWriteCreateFactory", 0, uintptr(unsafe.Pointer(&dwriteFactoryIID)), uintptr(unsafe.Pointer(&t.factory)))); err != nil {
			return err
		}
	}
	if t.drawingFactory == nil {
		if err = hresult("D2D1CreateFactory", call(d2d1, "D2D1CreateFactory", 0, uintptr(unsafe.Pointer(&d2dFactoryIID)), 0, uintptr(unsafe.Pointer(&t.drawingFactory)))); err != nil {
			return err
		}
	}
	if t.dpi == dpi && t.language == language && t.cache != nil {
		return nil
	}
	t.clearLayouts()
	locale := "en-US"
	if language == "zh-CN" {
		locale = "zh-CN"
	}
	for i, weight := range []uintptr{400, 600} {
		if err = hresult("CreateTextFormat", comCall(t.factory, 15, uintptr(unsafe.Pointer(utf("Segoe UI"))), 0, weight, 0, 5,
			floatArg(12*float32(dpi)/96), uintptr(unsafe.Pointer(utf(locale))), uintptr(unsafe.Pointer(&t.formats[i])))); err != nil {
			return err
		}
		if err = hresult("SetWordWrapping", comCall(t.formats[i], 5, 1)); err != nil {
			return err
		}
	}
	t.cache = textlayout.NewCache(textCacheEntries, textCacheUnits, func(l *nativeTextLayout) { releaseCOM(&l.object) })
	t.dpi, t.language = dpi, language
	return nil
}

func (t *directText) layout(value string, bold bool) (*nativeTextLayout, error) {
	key := textlayout.Key{Text: value, Bold: bold}
	if l, ok := t.cache.Get(key); ok {
		return l, nil
	}
	// UI strings are fixed labels, numbers or an ID, never chat content. Reject
	// unexpected huge metadata before allocating an unbounded native layout.
	if len(value) > 4096 {
		return nil, fmt.Errorf("display text exceeds layout budget")
	}
	u, err := windows.UTF16FromString(value)
	if err != nil {
		return nil, fmt.Errorf("invalid display text: %w", err)
	}
	if len(u) > 1024 {
		return nil, fmt.Errorf("display text exceeds layout budget")
	}
	i := 0
	if bold {
		i = 1
	}
	l := &nativeTextLayout{}
	if err := hresult("CreateTextLayout", comCall(t.factory, 18, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)-1),
		uintptr(unsafe.Pointer(t.formats[i])), floatArg(textBoxWidth), floatArg(textBoxHeight), uintptr(unsafe.Pointer(&l.object)))); err != nil {
		releaseCOM(&l.object)
		return nil, err
	}
	fail := func(err error) (*nativeTextLayout, error) { releaseCOM(&l.object); return nil, err }
	var m dwriteMetrics
	if err := hresult("GetMetrics", comCall(l.object, 60, uintptr(unsafe.Pointer(&m)))); err != nil {
		return fail(err)
	}
	var overhang [4]float32
	if err := hresult("GetOverhangMetrics", comCall(l.object, 61, uintptr(unsafe.Pointer(&overhang)))); err != nil {
		return fail(err)
	}
	// An all-whitespace run has advances but no ink box. Do not interpret an
	// empty overhang box as ink filling the large unconstrained layout area.
	if strings.TrimSpace(value) == "" {
		overhang = [4]float32{0, 0, m.WidthIncludingTrailingWhitespace - m.LayoutWidth, m.Height - m.LayoutHeight}
	}
	var ok bool
	l.bounds, ok = textlayout.PixelBounds(m.WidthIncludingTrailingWhitespace, m.Height, m.LayoutWidth, m.LayoutHeight, overhang)
	if !ok || m.LineCount != 1 {
		return fail(fmt.Errorf("invalid single-line text bounds"))
	}
	if !t.cache.Put(key, l, len(u)-1) {
		return nil, fmt.Errorf("display text exceeds cache budget")
	}
	return l, nil
}

func (t *directText) width(value string, bold bool) (int, error) {
	if value == "" {
		return 0, nil
	}
	l, err := t.layout(value, bold)
	if err != nil {
		return 0, err
	}
	return l.bounds.Width(), nil
}

// DCRenderTarget uses an opaque BGRA surface. ClearType is composited against
// the actual background before the final rounded-window alpha mask is applied.
// Coordinates and cached layouts are physical pixels; the target DPI is 96,
// while the format's em size is exactly 12*dpi/96, never a stretched bitmap.
func (s *overlaySurface) beginText(t *directText, monitor uintptr) error {
	if err := t.ensureRendering(monitor); err != nil {
		return err
	}
	if s.target == nil {
		props := d2dTargetProperties{Type: 1, Format: 87, AlphaMode: 3, DpiX: 96, DpiY: 96}
		if err := hresult("CreateDCRenderTarget", comCall(t.drawingFactory, 16, uintptr(unsafe.Pointer(&props)), uintptr(unsafe.Pointer(&s.target)))); err != nil {
			s.closeText()
			return err
		}
		color := rgba(0)
		if err := hresult("CreateSolidColorBrush", comCall(s.target, 8, uintptr(unsafe.Pointer(&color)), 0, uintptr(unsafe.Pointer(&s.brush)))); err != nil {
			s.closeText()
			return err
		}
		comCall(s.target, 34, 0) // DEFAULT: follows DirectWrite/system rendering settings.
	}
	r := rect{Right: int32(s.key.W), Bottom: int32(s.key.H)}
	if err := hresult("BindDC", comCall(s.target, 57, s.dc, uintptr(unsafe.Pointer(&r)))); err != nil {
		s.closeText()
		return err
	}
	comCall(s.target, 36, uintptr(unsafe.Pointer(t.renderingParams)))
	comCall(s.target, 48) // BeginDraw; balanced by endText even on layout failure.
	return nil
}
func (s *overlaySurface) closeText() {
	s.releaseFrameLayouts()
	releaseCOM(&s.brush)
	releaseCOM(&s.target)
}
func (s *overlaySurface) endText() error {
	err := hresult("EndDraw", comCall(s.target, 49, 0, 0))
	s.releaseFrameLayouts()
	if err != nil {
		s.closeText()
	} // recreate target and brushes on the next paint.
	return err
}
func (s *overlaySurface) releaseFrameLayouts() {
	for i := range s.frameLayouts {
		releaseCOM(&s.frameLayouts[i])
	}
	s.frameLayouts = s.frameLayouts[:0]
}
func (s *overlaySurface) ink(color uint32) uintptr {
	c := rgba(color)
	comCall(s.brush, 8, uintptr(unsafe.Pointer(&c))) // ID2D1SolidColorBrush::SetColor
	return uintptr(unsafe.Pointer(s.brush))
}
func (s *overlaySurface) fill(r rect, color uint32) {
	f := d2dRect{float32(r.Left), float32(r.Top), float32(r.Right), float32(r.Bottom)}
	comCall(s.target, 17, uintptr(unsafe.Pointer(&f)), s.ink(color))
}
func (s *overlaySurface) rule(x1, y1, x2, y2 int, color uint32) {
	// Half-pixel centring makes one-physical-pixel structural strokes crisp.
	comCall(s.target, 15, pointArg(float32(x1)+.5, float32(y1)+.5), pointArg(float32(x2)+.5, float32(y2)+.5), s.ink(color), floatArg(1), 0)
}
func (s *overlaySurface) stroke(r rect, radius int, color uint32) {
	f := d2dRoundedRect{d2dRect{float32(r.Left) + .5, float32(r.Top) + .5, float32(r.Right) - .5, float32(r.Bottom) - .5}, float32(radius), float32(radius)}
	comCall(s.target, 18, uintptr(unsafe.Pointer(&f)), s.ink(color), floatArg(1), 0)
}
func (s *overlaySurface) drawText(t *directText, x, top, height int, value string, bold bool, color uint32) error {
	if value == "" {
		return nil
	}
	l, err := t.layout(value, bold)
	if err != nil {
		return err
	}
	if l.bounds.Height() > height {
		return fmt.Errorf("measured text height=%d available=%d: %w", l.bounds.Height(), height, errTextDoesNotFit)
	}
	ox, oy := l.bounds.Origin(x, top, height)
	// Pin through EndDraw, even if later measurements evict this cache entry.
	comCall(l.object, 1)
	s.frameLayouts = append(s.frameLayouts, l.object)
	// Draw the exact layout used for width selection; no separate GDI metrics,
	// trimming, transform, or bitmap interpolation can invalidate the fit.
	comCall(s.target, 28, pointArg(ox, oy), uintptr(unsafe.Pointer(l.object)), s.ink(color), 0)
	return nil
}
