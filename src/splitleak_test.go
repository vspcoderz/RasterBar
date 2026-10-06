package main

import "testing"

// The split view's no-stale-cells suite.
//
// Why this file exists on its own: AGENTS.md records that a test double kinder
// than reality agrees with your bug, and the no-stale-cells emulator here was
// caught twice being too forgiving (ignoring SGR entirely, then treating LF at
// the last row as a cursor move instead of a scroll). Compositing two panes into
// one frame is a fresh place for a stale cell to hide — a copy that lands one row
// off, or a pane whose geometry and the compositor's disagree — and neither shows
// up as an error, only as the wrong picture.
//
// So the assertion is the same as everywhere else: reconstruct the screen and
// compare it to the frame that was handed in.

// splitPaneFrames makes a moving video-pane frame at a pane's geometry, as ffmpeg
// would hand it over: rows of (width * bytesPerPixel), and in colour mode two
// stacked pixel rows per cell row.
func splitPaneFrames(cols, rows int, mode ColorMode, glyph GlyphMode) [][]byte {
	perCell, bpc := perCellFor(mode, glyph), bytesPerCell(mode)
	out := make([][]byte, 6)
	for i := range out {
		f := make([]byte, paneFrameBytes(cols, rows, mode, glyph))
		for j := range f {
			f[j] = byte(j*3 + i*37)
		}
		// Vary a whole column band per frame so the diff cache has real work and
		// a missed copy cannot hide behind an unchanged frame.
		band := (i * 7) % cols
		for y := 0; y < rows*perCell; y++ {
			for x := band; x < minInt(band+3, cols); x++ {
				for k := 0; k < bpc; k++ {
					idx := y*cols*bpc + x*bpc + k
					if idx < len(f) {
						f[idx] = byte(i*91 + 5)
					}
				}
			}
		}
		out[i] = f
	}
	return out
}

// TestSplitScreenMatchesLastFrameMono is the mono split across both orientations
// and several divider positions, with a moving video pane beside moving bars.
func TestSplitScreenMatchesLastFrameMono(t *testing.T) {
	const cols, rows = 40, 14
	for _, videoLeft := range []bool{false, true} {
		for _, divider := range []int{10, 20, 30} {
			sl := computeSplitLayout(cols, rows, divider, videoLeft)
			name := modeName(ColorNone) + "/" + dirName(videoLeft) + "/" + itoa(divider)
			t.Run(name, func(t *testing.T) {
				sc := newScreen(cols, rows)
				r := NewDiffRenderer(sc, cols, rows)

				viz := &barsViz{}
				viz.Resize(sl.viz.cols, sl.viz.rows)
				g := NewVizGrid(sl.viz.cols, sl.viz.rows, false, 1)

				vidFrames := splitPaneFrames(sl.video.cols, sl.video.rows, ColorNone, GlyphCell)
				composite := make([]byte, frameBytesFor(cols, rows, ColorNone, GlyphCell))

				var last []byte
				for i := range vidFrames {
					fr := splitAudioFrame(i)
					viz.Push(&fr)
					g.Clear()
					viz.Paint(g)
					if !composeSplitFrame(composite, sl, cols, rows, ColorNone, GlyphCell,
						g.MonoFrame(), vidFrames[i]) {
						t.Fatalf("frame %d: compose refused", i)
					}
					last = append([]byte(nil), composite...)
					if err := r.Draw(last); err != nil {
						t.Fatalf("frame %d: %v", i, err)
					}
				}
				assertScreenMatchesMono(t, sc, cols, rows, last)
			})
		}
	}
}

// TestSplitScreenMatchesLastFrameColor is the same in colour, over both glyph
// layouts. GlyphHalf is the one that can shear rather than merely mis-colour,
// because the renderer reads two pixel rows per cell row.
func TestSplitScreenMatchesLastFrameColor(t *testing.T) {
	const cols, rows = 32, 12
	for _, glyph := range []GlyphMode{GlyphHalf, GlyphCell} {
		for _, mode := range []ColorMode{ColorTrue, Color256} {
			for _, videoLeft := range []bool{false, true} {
				sl := computeSplitLayout(cols, rows, 14, videoLeft)
				t.Run(modeName(mode)+"/"+glyphName(glyph)+"/"+dirName(videoLeft), func(t *testing.T) {
					sc := newScreen(cols, rows)
					r := newColorRenderer(sc, cols, rows, mode, glyph)

					viz := &barsViz{}
					viz.Resize(sl.viz.cols, sl.viz.rows)
					g := NewVizGrid(sl.viz.cols, sl.viz.rows, true, perCellFor(mode, glyph))
					g.SetPalette(paletteAt(0))

					vidFrames := splitPaneFrames(sl.video.cols, sl.video.rows, mode, glyph)
					composite := make([]byte, frameBytesFor(cols, rows, mode, glyph))

					var last []byte
					for i := range vidFrames {
						fr := splitAudioFrame(i)
						viz.Push(&fr)
						g.Clear()
						viz.Paint(g)
						if !composeSplitFrame(composite, sl, cols, rows, mode, glyph,
							g.ColorFrame(), vidFrames[i]) {
							t.Fatalf("frame %d: compose refused", i)
						}
						last = append([]byte(nil), composite...)
						if err := r.Draw(last); err != nil {
							t.Fatalf("frame %d: %v", i, err)
						}
					}
					assertScreenMatches(t, sc, cols, rows, glyph, mode, last)
				})
			}
		}
	}
}

// TestSplitDividerMoveClearsTheAbandonedCells is the one that matters most, and
// the one a diff renderer can get wrong quietly.
//
// Moving the divider changes which cells belong to which pane. The cells the video
// pane used to own are now visualiser cells, and vice versa. If the renderer or
// the compositor does not mark them, the old picture survives in the new layout's
// space -- the same class as the overlay-without-ForceNext trap, and the same
// reason closing an overlay has to force a repaint.
func TestSplitDividerMoveClearsTheAbandonedCells(t *testing.T) {
	const cols, rows = 32, 12
	sl := computeSplitLayout(cols, rows, 16, false)
	sc := newScreen(cols, rows)
	r := newColorRenderer(sc, cols, rows, ColorTrue, GlyphHalf)

	viz := &barsViz{}
	viz.Resize(sl.viz.cols, sl.viz.rows)
	g := NewVizGrid(sl.viz.cols, sl.viz.rows, true, 2)
	g.SetPalette(paletteAt(0))
	vid := splitPaneFrames(sl.video.cols, sl.video.rows, ColorTrue, GlyphHalf)[0]
	composite := make([]byte, frameBytesFor(cols, rows, ColorTrue, GlyphHalf))

	// Draw the old layout.
	fr0 := splitAudioFrame(0)
	viz.Push(&fr0)
	g.Clear()
	viz.Paint(g)
	if !composeSplitFrame(composite, sl, cols, rows, ColorTrue, GlyphHalf, g.ColorFrame(), vid) {
		t.Fatal("compose refused")
	}
	if err := r.Draw(composite); err != nil {
		t.Fatal(err)
	}

	// The divider moves. A forced repaint is what makes this legitimate, and it is
	// exactly what applySplit does in the render loop.
	sl2 := computeSplitLayout(cols, rows, 8, false)
	r.ForceNext()
	viz.Resize(sl2.viz.cols, sl2.viz.rows)
	g2 := NewVizGrid(sl2.viz.cols, sl2.viz.rows, true, 2)
	g2.SetPalette(paletteAt(0))
	vid2 := splitPaneFrames(sl2.video.cols, sl2.video.rows, ColorTrue, GlyphHalf)[1]
	composite2 := make([]byte, frameBytesFor(cols, rows, ColorTrue, GlyphHalf))
	g2.Clear()
	viz.Paint(g2)
	if !composeSplitFrame(composite2, sl2, cols, rows, ColorTrue, GlyphHalf, g2.ColorFrame(), vid2) {
		t.Fatal("compose refused")
	}
	if err := r.Draw(composite2); err != nil {
		t.Fatal(err)
	}

	assertScreenMatches(t, sc, cols, rows, GlyphHalf, ColorTrue, composite2)
}

// splitAudioFrame is a moving band spectrum for the visualiser pane.
func splitAudioFrame(i int) AudioFrame {
	b := make([]float64, bands)
	for j := range b {
		b[j] = float64((j*3+i*11)%17) / 17.0
	}
	return AudioFrame{Bands: b, Beat: float64(i%3) / 3.0}
}

func dirName(videoLeft bool) string {
	if videoLeft {
		return "videoLeft"
	}
	return "videoRight"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	if neg {
		return "-" + string(b)
	}
	return string(b)
}
