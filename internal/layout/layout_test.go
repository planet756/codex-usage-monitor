package layout

import "testing"

func TestDetailTextBandsRetainCJKHeightWithoutOverlappingRows(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 168, 192} {
		s := func(n int) int { return (n*dpi + 48) / 96 }
		if s(DetailIdentityY)+s(DetailTextHeight) > s(DetailRows[0]) ||
			s(DetailRows[len(DetailRows)-1])+s(24) > s(DetailFooterY) ||
			s(DetailFooterY)+s(DetailTextHeight) > s(DetailHeight)-s(2) || DetailTextHeight < 24 {
			t.Fatalf("detail text bands overlap or clip at %d DPI", dpi)
		}
	}
}

func TestTitlebarCentersOnPageBandAtActualAndCrossScreenDPI(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 192} {
		s := func(n int) int { return (n*dpi + 48) / 96 }
		client := Rect{-1600, 83, s(1400), s(900)}
		window := Rect{client.X - s(8), client.Y - s(8), client.W + s(16), client.H + s(16)}
		button := Rect{client.X + client.W - s(138), client.Y + s(1), s(45), s(35)}
		p, ok := Titlebar(window, client, button, dpi, (StripWidth*dpi+48)/96, false)
		band := TitlebarBand(client, button, dpi)
		if !ok || abs((2*p.Y+p.Strip.H)-(2*band.Y+band.H)) > 1 {
			t.Fatalf("not centered on the page's titlebar: %+v", p)
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

func TestTitlebarStaysLeftOfMinimizeAndExpandsDownward(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 192} {
		s := func(n int) int { return (n*dpi + 48) / 96 }
		window := Rect{-2200, -160, s(1500), s(900)}
		minimize := Rect{window.X + window.W - s(138), window.Y + s(4), s(46), s(32)}
		closed, ok := Titlebar(window, window, minimize, dpi, (StripWidth*dpi+48)/96, false)
		if !ok || closed.X+closed.W > minimize.X-s(8) || closed.Y < minimize.Y || closed.Y+closed.H > minimize.Y+minimize.H {
			t.Fatalf("caption buttons would be covered at %d DPI: %+v", dpi, closed)
		}
		open, ok := Titlebar(window, window, minimize, dpi, (StripWidth*dpi+48)/96, true)
		if !ok || !open.Expanded || open.X != closed.X || open.Y != closed.Y || open.Strip != closed.Strip || open.Panel.Y < open.Strip.H+s(8) || open.Y+open.H > window.Y+window.H {
			t.Fatalf("panel did not open below the unchanged strip: %+v", open)
		}
	}
	if _, ok := Titlebar(Rect{W: 300, H: 800}, Rect{W: 300, H: 800}, Rect{X: 160, W: 46, H: 32}, 96, StripWidth, false); ok {
		t.Fatal("accepted a titlebar too narrow for the strip")
	}
}

func TestNarrowCaptionKeepsPanelAndStripInsideHost(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 192} {
		s := func(n int) int { return (n*dpi + 48) / 96 }
		for _, available := range []int{320, 340, 360, 400, 700} {
			window := Rect{X: -s(900), Y: -s(50), W: s(available + 226), H: s(800)}
			button := Rect{X: window.X + window.W - s(138), Y: window.Y + s(1), W: s(46), H: s(35)}
			p, ok := Titlebar(window, window, button, dpi, (StripWidth*dpi+48)/96, true)
			if !ok || !p.Expanded || p.W != s(StripWidth) || p.Panel.X != p.Strip.X || p.Panel.W != p.Strip.W || p.Panel.X+p.Panel.W != p.W || p.Panel.Y < p.Strip.H+s(8) {
				t.Fatalf("panel width or alignment differs from strip at %d DPI: %+v", dpi, p)
			}
		}
	}
}

func TestStripRuleStaysInPhysicalMiddleAtEveryDPI(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 192} {
		for _, dipWidth := range []int{320, 340, 360, 400} {
			width := (dipWidth*dpi + 48) / 96
			x := SplitStrip(width, (26*dpi+48)/96, dpi)
			if abs(2*x.Divider-width) > 1 || x.Left.X+x.Left.W >= x.Divider || x.Right.X <= x.Divider || x.Right.X+x.Right.W >= width {
				t.Fatalf("rule or data crossed its half: %+v", x)
			}
			inset := (4*dpi + 48) / 96
			if x.Divider-(x.Left.X+x.Left.W) != inset || x.Right.X-x.Divider != inset {
				t.Fatalf("divider spacing differs across halves at %d DPI: %+v", dpi, x)
			}
			if x.Left.X != (8*dpi+48)/96 || width-x.Right.X-x.Right.W != (18*dpi+48)/96 {
				t.Fatalf("text inset or disclosure reservation changed at %d DPI: %+v", dpi, x)
			}
			arrowLeft := width - (10*dpi+48)/96 - (3*dpi+48)/96
			if arrowLeft-x.Right.X-x.Right.W < (4*dpi+48)/96 {
				t.Fatalf("text crowded the disclosure arrow at %d DPI: %+v", dpi, x)
			}
		}
	}
}

func TestMeasuredWidthKeepsBothSurfacesInsideCaptionWithoutExtraPadding(t *testing.T) {
	for _, dpi := range []int{96, 120, 144, 168, 192} {
		s := func(n int) int { return (n*dpi + 48) / 96 }
		for _, available := range []int{320, 352, 400, 480, 700} {
			window := Rect{X: -s(900), Y: -s(50), W: s(available + 226), H: s(800)}
			button := Rect{X: window.X + window.W - s(138), Y: window.Y + s(1), W: s(46), H: s(35)}
			for _, wanted := range []int{320, 344, 400, 480, 800} {
				p, ok := Titlebar(window, window, button, dpi, s(wanted), true)
				if !ok || !p.Expanded || p.W > s(StripMaxWidth) || p.W > s(available) || p.Panel.W != p.Strip.W || p.X+p.W != button.X-s(8) {
					t.Fatalf("measured titlebar escaped available space: %+v", p)
				}
				c := SplitStrip(p.W, p.Strip.H, dpi)
				if c.Left.X != s(8) || c.Right.X-c.Divider != s(4) || c.Divider-c.Left.X-c.Left.W != s(4) {
					t.Fatal("widening added padding")
				}
			}
		}
	}
}
