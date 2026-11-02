package layout

type Rect struct{ X, Y, W, H int }
type Placement struct {
	Rect
	Strip    Rect
	Panel    Rect
	Expanded bool
}

const DetailHeight = 342
const DetailRuleY = 198
const DetailFooterY = 316
const DetailIdentityY = 36
const DetailTextHeight = 24

// Both surfaces share a compact content-dependent width. Text labels and units
// are retained; extra width is content space, never extra padding.
const StripWidth = 320
const StripMaxWidth = 480
const StripWidthStep = 8

var DetailRows = [9]int{60, 92, 116, 140, 164, 208, 232, 256, 280}

type StripColumns struct {
	Divider     int
	Left, Right Rect
}

// SplitStrip fixes the rule at the physical strip midpoint. Each text group
// has its own bounded half, with the disclosure arrow reserved on the right.
func SplitStrip(width, height, dpi int) StripColumns {
	if dpi <= 0 {
		dpi = 96
	}
	s := func(n int) int { return (n*dpi + 48) / 96 }
	mid := width / 2
	// The chevron reaches 13 DIP from the right edge. Leave 5 DIP between
	// its ink and this text boundary, rather than an oversized empty gutter.
	outer, inner, disclosure := s(8), s(4), s(18)
	return StripColumns{mid, Rect{X: outer, W: max(0, mid-outer-inner), H: height},
		Rect{X: mid + inner, W: max(0, width-mid-inner-disclosure), H: height}}
}

// Titlebar places the strip immediately to the left of the native minimize
// button. Its panel opens downwards without moving the strip or hitting the
// caption buttons. All inputs are physical screen coordinates.
func Titlebar(window, client, minimize Rect, dpi, requestedWidth int, expanded bool) (Placement, bool) {
	if dpi <= 0 {
		dpi = 96
	}
	s := func(n int) int { return (n*dpi + 48) / 96 }
	gap := s(8)
	w := min(max(s(StripWidth), requestedWidth), s(StripMaxWidth), minimize.X-window.X-gap-s(80))
	h := min(s(26), minimize.H-s(4))
	if w < s(StripWidth) || h < s(16) || minimize.X < window.X || minimize.Y < window.Y || minimize.X+minimize.W > window.X+window.W || minimize.Y+minimize.H > window.Y+min(window.H, s(96)) {
		return Placement{}, false
	}
	band := TitlebarBand(client, minimize, dpi)
	p := Placement{Rect: Rect{minimize.X - gap - w, band.Y + (band.H-h)/2, w, h}, Strip: Rect{W: w, H: h}}
	if expanded {
		ph := s(DetailHeight)
		if p.Y+h+gap+ph <= window.Y+window.H {
			p.Panel = Rect{Y: h + gap, W: w, H: ph}
			p.H += gap + ph
			p.Expanded = true
		}
	}
	return p, true
}

// Desktop's CSS title bar is 44 DIP; its Electron controls overlay is 36 DIP
// with a one-DIP top inset. Derive zoom from the measured controls, while using
// the client top to exclude Windows' invisible resize border from centering.
func TitlebarBand(client, minimize Rect, dpi int) Rect {
	if dpi <= 0 {
		dpi = 96
	}
	s := func(n int) int { return (n*dpi + 48) / 96 }
	height := max(s(44), ((minimize.H+s(1))*44+18)/36)
	return Rect{client.X, client.Y, client.W, min(client.H, height)}
}
