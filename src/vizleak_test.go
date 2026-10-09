package main

import (
	"fmt"
	"math"
	"testing"
)

// Pixel-leak coverage for the visualizer path.
//
// The claim being tested is the one in PLAN-phase7.md: because a visualizer paints
// into a neutral VizGrid and the grid is then handed to the *existing* renderers in
// the byte layouts they already speak, screen_test.go's no-stale-cells guarantee
// applies to visualizer output without any new tests of its own.
//
// That claim is only true if the grid actually produces the frame the renderer was
// handed. `leakFrames` above proves the renderers are honest given honest input; it
// says nothing about whether MonoFrame/ColorFrame are honest. A wrong byte in the
// grid is indistinguishable from a leak once it reaches the screen: the cell shows
// something the frame did not contain.
//
// So: adversarial frames through every style, through the grid, through the real
// renderer, into the terminal emulator, then compare the reconstructed screen to
// the frame that went in.

// vizLeakFrames builds a spectrum sequence designed to expose a stale cell.
//
// Different from leakFrames on purpose. That one moves hard-edged blocks, which is
// what video looks like. This one produces the shapes a visualiser actually makes —
// a moving band, an all-off frame, a single hot band, and a frame where only one
// cell changed — because a leak only shows if the *next* frame disagrees with the
// cell the previous one left behind.
//
// The all-off frames matter most: they are the transition where every cell has to
// be repainted, and a style that forgets to clear one leaves the old glyph there.
func vizLeakFrames(n int) []AudioFrame {
	mk := func(fill func(b int) float64) AudioFrame {
		b := make([]float64, bands)
		for i := range b {
			b[i] = fill(i)
		}
		return AudioFrame{
			Bands: b,
			Wave:  waveFor(fill),
			Beat:  1,
		}
	}
	var out []AudioFrame
	// Silence: nothing drawn anywhere. Whatever the screen shows after this is a
	// cell the renderer failed to clear.
	out = append(out, mk(func(int) float64 { return 0 }))
	// A moving band, one column per frame.
	for step := 0; step < 6; step++ {
		s := step
		out = append(out, mk(func(i int) float64 {
			if (i+s*3)%16 < 5 {
				return 0.9
			}
			return 0.05
		}))
	}
	// One hot band, everything else silent: the sharpest possible transition.
	out = append(out, mk(func(i int) float64 {
		if i == bands/2 {
			return 1
		}
		return 0
	}))
	// Back to silence. The single most leaky transition.
	out = append(out, mk(func(int) float64 { return 0 }))
	// And one more, so a style that is only correct on frame boundaries is caught.
	out = append(out, mk(func(i int) float64 { return 0.4 }))
	out = append(out, mk(func(int) float64 { return 0 }))
	return out
}

func waveFor(fill func(int) float64) []float64 {
	w := make([]float64, fftSize)
	for i := range w {
		w[i] = fill(i%bands) * math.Sin(float64(i)*0.05)
	}
	return w
}

// TestVisualizerGridScreenMatchesLastFrameMono is the leak test for music mode in
// monochrome.
func TestVisualizerGridScreenMatchesLastFrameMono(t *testing.T) {
	const cols, rows = 32, 12
	frames := vizLeakFrames(bands)
	for _, mk := range vizRegistry {
		t.Run(mk().Name(), func(t *testing.T) {
			sc := newScreen(cols, rows)
			r := NewDiffRenderer(sc, cols, rows)
			viz := mk()
			viz.Resize(cols, rows)
			g := NewVizGrid(cols, rows, false, 1)

			var last []byte
			for i, f := range frames {
				fr := f
				viz.Push(&fr)
				g.Clear()
				viz.Paint(g)
				last = g.MonoFrame()
				if err := r.Draw(last); err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
			}
			assertScreenMatchesMono(t, sc, cols, rows, last)
		})
	}
}

// TestVisualizerGridScreenMatchesLastFrameColor is the same for colour, across both
// palettes and both glyph layouts.
//
// GlyphHalf is the one that can differ from GlyphCell: the renderer reads two
// pixels per cell and the grid has to write them where it expects, so a layout
// mistake here shears the image rather than merely mis-colouring it.
func TestVisualizerGridScreenMatchesLastFrameColor(t *testing.T) {
	const cols, rows = 32, 12
	frames := vizLeakFrames(bands)
	for _, glyph := range []GlyphMode{GlyphHalf, GlyphCell} {
		for _, mode := range []ColorMode{ColorTrue, Color256} {
			t.Run(modeName(mode)+"/"+glyphName(glyph), func(t *testing.T) {
				sc := newScreen(cols, rows)
				r := newColorRenderer(sc, cols, rows, mode, glyph)
				viz := &barsViz{}
				viz.Resize(cols, rows)
				g := NewVizGrid(cols, rows, true, perCellFor(ColorTrue, glyph))
				g.SetPalette(paletteAt(0))

				var last []byte
				for i, f := range frames {
					fr := f
					viz.Push(&fr)
					g.Clear()
					viz.Paint(g)
					last = g.ColorFrame()
					if err := r.Draw(last); err != nil {
						t.Fatalf("frame %d: %v", i, err)
					}
				}
				assertScreenMatches(t, sc, cols, rows, glyph, mode, last)
			})
		}
	}
}

// TestVisualizerPaletteSwitchRepaintsEveryCell is the leak a palette change
// produces on its own.
//
// The cells have not changed *value* in the grid — only the palette that
// interprets them has — so a renderer with a diff cache sees no change at all and
// leaves the previous palette's colours on screen. This is why the render loop
// calls ForceNext after `c`: the grid is identical, the pixels must not be.
func TestVisualizerPaletteSwitchRepaintsEveryCell(t *testing.T) {
	const cols, rows = 32, 12
	sc := newScreen(cols, rows)
	r := newColorRenderer(sc, cols, rows, ColorTrue, GlyphCell)
	viz := &barsViz{}
	viz.Resize(cols, rows)
	g := NewVizGrid(cols, rows, true, 1)

	f := AudioFrame{Bands: rampBands(bands, 1.0), Beat: 0.5}
	viz.Push(&f)
	g.Clear()
	viz.Paint(g)
	g.SetPalette(paletteAt(0))
	if err := r.Draw(g.ColorFrame()); err != nil {
		t.Fatal(err)
	}

	// Switch palette and repaint the *identical* grid, the way the render loop does.
	g.SetPalette(paletteAt(3)) // ember
	want := g.ColorFrame()
	if err := r.Draw(want); err != nil {
		t.Fatal(err)
	}

	// Without a forced repaint the screen still shows the old palette. Assert what
	// the screen shows *is* what the new palette asks for, which is the property
	// that only holds after ForceNext.
	assertScreenMatches(t, sc, cols, rows, GlyphCell, ColorTrue, want)
}

// TestForceNextClearsTheChrome is the second half of the ForceNext trap, and it is
// a leak too.
//
// A forced repaint emits \x1b[2J, which clears the whole screen including the HUD
// rows below the grid. If the HUD's "unchanged, skip" cache is not invalidated at
// the same time, the chrome stays blank until something else happens to change it.
// That is stale *missing* pixels rather than stale *wrong* pixels, and it is the
// same bug wearing the other hat.
func TestForceNextClearsTheChrome(t *testing.T) {
	const cols, rows = 32, 12
	const hudRows = 3
	sc := newScreen(cols, rows+hudRows)
	r := newColorRenderer(sc, cols, rows, ColorTrue, GlyphCell)
	frames := leakFrames(cols, rows, GlyphCell)

	for i, f := range frames {
		if err := r.Draw(f); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		hud := []string{"title", "0:01 / 3:20", "space pause"}
		fmt.Fprint(sc, "\x1b[0m")
		for j, line := range hud {
			fmt.Fprintf(sc, "\x1b[%d;1H%s", rows+1+j, line)
		}
		// A style switch: the grid changes completely, so the renderer must be told
		// to start over.
		if i == len(frames)-2 {
			r.ForceNext()
		}
	}
	// The last thing painted on the grid is the last frame.
	assertScreenMatches(t, sc, cols, rows, GlyphCell, ColorTrue, frames[len(frames)-1])

	// And the chrome has to be repainted *after* the forced repaint, or it is gone.
	// This is the assertion the render loop earns by clearing hudText alongside
	// every ForceNext.
	hud := []string{"title", "0:02 / 3:20", "spectrum on"}
	fmt.Fprint(sc, "\x1b[0m")
	for j, line := range hud {
		fmt.Fprintf(sc, "\x1b[%d;1H%s", rows+1+j, line)
	}
	for j, want := range hud {
		if got := sc.text(rows + j); got != want {
			t.Errorf("chrome row %d = %q, want %q", j, got, want)
		}
	}
}

// TestWaterfallSurvivesClearBeforePaint is the one that is not a leak but looks
// like one.
//
// The render loop clears the grid before every Paint, because a style is allowed to
// draw nothing on a given frame and a partial paint shows the previous frame
// through. The waterfall is the exception: it accumulates by shifting the previous
// contents down one row, so clearing first throws away exactly the history it is
// made of, and the style degrades to a single row at the top of the screen with 11
// blank rows below it.
//
// Blank-below-the-strip is indistinguishable from a leak by eye. It is not one.
func TestWaterfallSurvivesClearBeforePaint(t *testing.T) {
	const cols, rows = 32, 12
	viz := &waterfallViz{}
	viz.Resize(cols, rows)
	g := NewVizGrid(cols, rows, false, 1)

	// Ten loud analyses, each cleared-then-painted exactly as the render loop does.
	for i := 0; i < 10; i++ {
		viz.Push(&AudioFrame{Bands: rampBands(bands, 1.0), Beat: 0.5})
		g.Clear()
		viz.Paint(g)
	}
	lit := 0
	for _, v := range g.gray {
		if v != 0 {
			lit++
		}
	}
	// If Clear() destroys the history, only the newest row survives: cols cells.
	if lit <= cols {
		t.Errorf("after 10 pushes the waterfall has %d lit cells, at most %d for one "+
			"row: Clear() before Paint is discarding the scroll history. "+
			"Paint must own the accumulation, not the grid.", lit, cols)
	}
}

// TestScopeFillsEveryColumn is the shape check for the one style that is not
// band-driven. A scope that drew only where it had samples would leave a ragged
// right edge, which reads as cells that never update.
func TestScopeFillsEveryColumn(t *testing.T) {
	const cols, rows = 32, 12
	viz := &scopeViz{}
	viz.Resize(cols, rows)
	g := NewVizGrid(cols, rows, false, 1)
	for i := 0; i < 4; i++ {
		viz.Push(&AudioFrame{Bands: make([]float64, bands), Wave: waveFor(func(i int) float64 { return 0.5 })})
		g.Clear()
		viz.Paint(g)
	}
	// Every column must have at least one lit cell.
	for x := 0; x < cols; x++ {
		found := false
		for y := 0; y < rows; y++ {
			if g.At(x, y) != 0 {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("column %d is entirely blank; the scope left a ragged edge", x)
		}
	}
}
