package main

import (
	"strings"
	"testing"
)

// TestBlockDurTracksTheFFTConstants keeps the tempo estimate honest.
//
// The block duration is derived from fftSize and spectrumRate rather than written
// down, because those are the two constants most likely to be retuned. A hardcoded
// 0.09 would make every estimate wrong the moment either moved, and nothing would
// fail -- the numbers would just quietly stop being BPM.
func TestBlockDurTracksTheFFTConstants(t *testing.T) {
	want := float64(fftSize) / float64(spectrumRate)
	if BlockDur() != want {
		t.Errorf("BlockDur() = %v, want %v", BlockDur(), want)
	}
	// And sanity-check the value itself: one 1024-sample window at 11025Hz.
	if d := BlockDur(); d < 0.05 || d > 0.15 {
		t.Errorf("BlockDur() = %v, want about 0.093s for a 1024 window at 11025Hz", d)
	}
}

// TestMonoPaletteProducesNoColour is what makes `c` safe to press.
//
// The mono entry draws through the grey ramp rather than a hue, so cycling colour
// on a terminal that cannot do colour -- or on one where the user does not want it
// -- leaves the visualizer readable instead of turning it into a black rectangle.
func TestMonoPaletteProducesNoColour(t *testing.T) {
	m := paletteByName(paletteMonoName)
	if !m.mono {
		t.Fatalf("the %q palette must be the mono entry", paletteMonoName)
	}
	if m.colorFor(0.5, 0.5, 0.5) != 0 {
		t.Error("the mono palette produced a colour")
	}
}

// --- grid and painters -------------------------------------------------------

// TestVizGridClearEmptiesEveryCell is the anti-stale-cell check.
//
// A style is allowed to paint nothing on a given frame -- a waterfall between
// scrolls, a radial whose rings all fell below the threshold. If Clear only reset
// what the style drew, the previous frame would show through and the visualizer
// would look stuck.

// --- grid and painters -------------------------------------------------------

// TestVizGridClearEmptiesEveryCell is the anti-stale-cell check.
//
// A style is allowed to paint nothing on a given frame -- a waterfall between
// scrolls, a radial whose rings all fell below the threshold. If Clear only reset
// what the style drew, the previous frame would show through and the visualizer
// would look stuck.
func TestVizGridClearEmptiesEveryCell(t *testing.T) {
	g := NewVizGrid(8, 4, true, 2)
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			g.Set(x, y, 40, 0x00ff00)
		}
	}
	g.Clear()
	for y := 0; y < 4; y++ {
		for x := 0; x < 8; x++ {
			if g.At(x, y) != 0 {
				t.Fatalf("cell (%d,%d) survived Clear: ramp index %d", x, y, g.At(x, y))
			}
		}
	}
}

func TestVizGridSetIgnoresOutOfBounds(t *testing.T) {
	// A style that computes a cell from a terminal resize can very easily be one
	// row or column out on the frame right after it. Clamping here means that shows
	// up as a missing dot rather than as a panic in the render loop.
	g := NewVizGrid(4, 3, false, 1)
	g.Set(-1, 0, 10, 0)
	g.Set(0, -1, 10, 0)
	g.Set(4, 0, 10, 0)
	g.Set(0, 3, 10, 0)
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			if g.At(x, y) != 0 {
				t.Fatalf("out-of-bounds Set wrote cell (%d,%d)", x, y)
			}
		}
	}
}

// TestMonoFrameIsExactlyOneBytePerCell pins the contract DiffRenderer checks its
// input against.

// TestMonoFrameIsExactlyOneBytePerCell pins the contract DiffRenderer checks its
// input against.
func TestMonoFrameIsExactlyOneBytePerCell(t *testing.T) {
	for _, c := range []struct{ cols, rows int }{{8, 4}, {80, 22}, {200, 60}} {
		g := NewVizGrid(c.cols, c.rows, false, 1)
		g.Set(0, 0, rampBright, 0)
		if got := len(g.MonoFrame()); got != c.cols*c.rows {
			t.Errorf("%dx%d: mono frame is %d bytes, want %d", c.cols, c.rows, got, c.cols*c.rows)
		}
	}
}

// TestColorFrameLayoutMatchesTheRenderer is the load-bearing size assertion.
//
// ColorDiffRenderer reads rows*2 pixels per row in half-block mode and indexes the
// bottom pixel one full row down. A frame of the wrong size is rejected outright,
// and one of the *right* size but the wrong layout draws a sheared grid.

// TestColorFrameLayoutMatchesTheRenderer is the load-bearing size assertion.
//
// ColorDiffRenderer reads rows*2 pixels per row in half-block mode and indexes the
// bottom pixel one full row down. A frame of the wrong size is rejected outright,
// and one of the *right* size but the wrong layout draws a sheared grid.
func TestColorFrameLayoutMatchesTheRenderer(t *testing.T) {
	for _, perCell := range []int{1, 2} {
		g := NewVizGrid(10, 5, true, perCell)
		g.Set(3, 2, rampBright, 0x112233)
		f := g.ColorFrame()
		want := 10 * 5 * perCell * 3
		if len(f) != want {
			t.Fatalf("pixPerCell=%d: frame is %d bytes, want %d", perCell, len(f), want)
		}
		stride := 10 * perCell * 3
		top := 2*stride + 3*3
		if f[top] != 0x11 || f[top+1] != 0x22 || f[top+2] != 0x33 {
			t.Errorf("top pixel at (%d,%d) = %02x%02x%02x, want 112233", 3, 2, f[top], f[top+1], f[top+2])
		}
		if perCell == 2 {
			bot := top + 10*3
			if f[bot] != 0x11 || f[bot+1] != 0x22 || f[bot+2] != 0x33 {
				t.Errorf("bottom pixel = %02x%02x%02x, want 112233 (fg==bg)", f[bot], f[bot+1], f[bot+2])
			}
		}
	}
}

// TestColorFrameIgnoresColourInMonoMode is what makes the mono palette safe.
//
// With colour off the packed value is meaningless and the luminance has to come
// from the ramp instead, or a mono terminal would show whatever colour was left in
// the buffer.

// TestColorFrameIgnoresColourInMonoMode is what makes the mono palette safe.
//
// With colour off the packed value is meaningless and the luminance has to come
// from the ramp instead, or a mono terminal would show whatever colour was left in
// the buffer.
func TestColorFrameIgnoresColourInMonoMode(t *testing.T) {
	g := NewVizGrid(4, 2, false, 1)
	g.Set(0, 0, rampBright, 0x00ff00)
	f := g.ColorFrame()
	if f[0] != f[1] || f[1] != f[2] {
		t.Errorf("mono cell is %02x%02x%02x, want a neutral grey from the ramp", f[0], f[1], f[2])
	}
}

// --- styles ------------------------------------------------------------------

// TestEveryStyleFillsItsGridAndSurvivesResize is the broad safety net.
//
// Every style is exercised through a push/paint cycle at three sizes, including
// the shrink that a window drag produces. A style that keeps per-grid state and is
// not told about a resize either paints out of bounds or leaves a band of stale
// cells, and neither shows up without actually resizing one.

// TestGridBackgroundIsNotBlack is the regression for the whole reason this change
// exists.
//
// An unlit cell was rgb 0, i.e. #000000, which on a black terminal is not "empty"
// so much as "the same colour as the terminal". The visualizer then looked like
// scattered debris floating in nothing, and a sparse style gave no sense of how big
// the picture was.
func TestGridBackgroundIsNotBlack(t *testing.T) {
	for _, name := range paletteNames() {
		p := paletteByName(name)
		g := NewVizGrid(8, 4, true, 1)
		g.SetPalette(p)
		g.Clear()
		bg := g.bg
		if bg == 0 {
			t.Errorf("palette %q has a black background; the grid will be invisible "+
				"on a black terminal", name)
		}
		// And it must actually be dark -- this is a background for content.
		r, gg, b := float64(bg>>16)/255, float64((bg>>8)&0xff)/255, float64(bg&0xff)/255
		lum := 0.2126*r + 0.7152*gg + 0.0722*b
		if lum > 0.20 {
			t.Errorf("palette %q background is %.2f luminance; too bright to sit "+
				"under content", name, lum)
		}
		// Every cell must have it, not just the ones a style happened to touch.
		for i := range g.rgb {
			if g.rgb[i] != bg {
				t.Fatalf("cell %d is %06x, want the background %06x", i, g.rgb[i], bg)
			}
		}
	}
}

func topRowSnapshot(g *VizGrid) string {
	var sb strings.Builder
	for x := 0; x < g.cols; x++ {
		sb.WriteByte(g.At(x, 0))
	}
	return sb.String()
}

// TestRadialCorrectsForCellAspect is the one that would have looked like a bug.
//
// A character cell is about twice as tall as it is wide. Without dividing the
// vertical offset by the aspect ratio the spokes come out an ellipse stretched to
// twice its intended height, and a circle is the single shape every viewer
// recognises -- so the error reads as "the renderer is broken" rather than as "the
// maths is slightly off".
//
// The assertion is stated in pixels, not cells, because that is where the property
// lives: a round shape is round in pixels and stretched in cells. Two earlier
// versions of this test were both wrong. One lit a single band, so it measured the
// slope of one line. The other lit a ramp and compared cell counts directly, which
// asks a fan to be twice as tall as it is wide -- true of neither a circle nor a
// fan.
//
// Every band is lit, and the drawn extent is converted to pixels: a column is 1px,
// a row is 2px.
//
// The expected ratio is 0.5, not 1.0, and the reason is the shape: the fan sweeps
// from -90 to +90 degrees off vertical, so it is a half-disc -- twice as wide as it
// is tall, with the arc round. A half-disc is the right answer for a fan; asking
// for width == height would be asking for a quarter-disc.
//
// It discriminates, but only with headroom, and getting that wrong cost three
// attempts. maxR is already derived from the aspect (min(cx, cy*aspect)), so at
// full scale the corrected and uncorrected shapes are identical: the corrected one
// reaches exactly the top of the grid, and the uncorrected one overflows it and is
// clamped back to the same place. Two errors cancelling is a real property of the
// code, not a test artefact -- but it means a full-scale spectrum cannot tell them
// apart. So the bands sit at half scale, where the broken version's extra reach is
// visible instead of clipped.

// --- preferences -------------------------------------------------------------

// TestVizPrefsCycleWraps is the behaviour the `v` and `c` keys depend on: there is
// no end of the list and no dead key.
func TestVizPrefsCycleWraps(t *testing.T) {
	p := newVizPrefs()
	n := len(vizRegistry)
	for i := 0; i < n; i++ {
		p.nextStyle()
	}
	if p.style != 0 {
		t.Errorf("after %d presses style = %d, want 0", n, p.style)
	}
	p.prevStyle()
	if p.style != n-1 {
		t.Errorf("one press back from 0 = %d, want %d", p.style, n-1)
	}
}

func TestVizPrefsPaletteCycleWraps(t *testing.T) {
	p := newVizPrefs()
	for i := 0; i < len(palettes); i++ {
		p.nextPalette()
	}
	if p.palette != 0 {
		t.Errorf("after %d presses palette = %d, want 0", len(palettes), p.palette)
	}
}

// TestVizPrefsClampSurvivesABadIndex: a zero-value prefs must be usable, because
// playTrack falls back to newVizPrefs when a caller passes none.

// TestVizPrefsClampSurvivesABadIndex: a zero-value prefs must be usable, because
// playTrack falls back to newVizPrefs when a caller passes none.
func TestVizPrefsClampSurvivesABadIndex(t *testing.T) {
	var p vizPrefs
	p.style = 99
	p.palette = -4
	p.clamp()
	if p.style != 0 || p.palette != 0 {
		t.Errorf("clamp left %d/%d, want 0/0", p.style, p.palette)
	}
	if p.StyleName() == "" || p.PaletteName() == "" {
		t.Error("a clamped prefs must still name a style and a palette")
	}
	if p.makeViz() == nil {
		t.Error("makeViz returned nil for a clamped prefs")
	}
}

// --- the HUD strip -----------------------------------------------------------

// TestMiniBarsIsExactlyWidth is the fixed-width invariant every HUD row depends on.
//
// The renderers diff against the previous paint, so a row that changes length
// leaves the tail of the longer previous row on screen forever. This is the same
// invariant fit() enforces, and the strip has to hold it too.

// TestMusicFPSFallsWithTheGrid is the low-end budget.
//
// Repainting a whole grid 30 times a second is fine at 80x22 and hopeless at
// 300x120, so the rate has to track the cell count rather than being a constant.
func TestMusicFPSFallsWithTheGrid(t *testing.T) {
	small := musicFPS(80, 22)
	big := musicFPS(300, 120)
	if small <= big {
		t.Errorf("musicFPS does not fall with the grid: %d at 80x22, %d at 300x120", small, big)
	}
	if small < 24 {
		t.Errorf("musicFPS(80,22) = %d, want a smooth 24 or better on a small grid", small)
	}
	if big > small {
		t.Errorf("musicFPS(300,120) = %d, above the small-grid rate", big)
	}
}

// --- audio format selection --------------------------------------------------

// TestPickAudioPrefersAStreamWithNoVideo is the low-end decision in one assertion.
//
// The level tap's ffmpeg is given -vn, so it never *outputs* video -- but ffmpeg
// still decodes what it is handed before discarding it. Pointed at a progressive
// format that means decoding a 360p h264 video purely to throw it away, on every
// track, on the machine that can least afford it.

// TestLevelTapArgsAreRealtimeAndSeeked pins the two flags whose absence is silent.
//
// This is not the kind of test that "proves nothing" by recomputing the code's own
// answer. It pins a decision that was already got wrong: the tap shipped without
// -re, analysed the entire track in under a second, and every visible symptom --
// a frozen spectrum, a stalled onset envelope, a particle field that fired once --
// pointed somewhere else entirely. Only the arg list can catch it.
//
// -re: without it ffmpeg pushes as fast as the CPU allows, so the whole track is
// consumed while mpv is still on the first second.
//
// -ss: the tap is a separate ffmpeg on a separate copy of the stream, so after a
// seek it would otherwise analyse from the top of the file.
func TestLevelTapArgsAreRealtimeAndSeeked(t *testing.T) {
	has := func(args []string, flag string) bool {
		for _, a := range args {
			if a == flag {
				return true
			}
		}
		return false
	}

	args := levelTapArgs("https://example/audio", 0)
	if !has(args, "-re") {
		t.Errorf("the level tap is not paced: %v\n"+
			"Without -re ffmpeg decodes as fast as it can, the whole track is "+
			"analysed in the first second, and the visualizer freezes while mpv "+
			"is still playing.", args)
	}
	// No -ss at offset 0: seeking to zero is a no-op and an extra input flag is
	// another thing that can differ between the two paths.
	if has(args, "-ss") {
		t.Errorf("offset 0 should not seek: %v", args)
	}
	// -vn matters for cost: the tap decodes whatever it is handed, so a video
	// format would be decoded in full and discarded.
	if !has(args, "-vn") {
		t.Errorf("the level tap will decode video it then discards: %v", args)
	}

	seeked := levelTapArgs("https://example/audio", 90)
	if !has(seeked, "-re") {
		t.Error("-re missing from the seeked tap")
	}
	if !has(seeked, "-ss") {
		t.Errorf("the seeked tap does not seek, so it analyses the wrong part: %v", seeked)
	}
	// The offset and the URL are a pair; find the -ss and check the number after it.
	for i, a := range seeked {
		if a == "-ss" {
			if i+1 >= len(seeked) {
				t.Fatal("-ss with no value")
			}
			if seeked[i+1] != "90.000" {
				t.Errorf("-ss %q, want %q", seeked[i+1], "90.000")
			}
		}
	}
}

// --- helpers -----------------------------------------------------------------

// rampBands is a plausible spectrum: quiet at the bottom, loud in the middle,
// rolling off at the top. Flat arrays make every style's output degenerate.

// cellsAboveBg counts cells carrying ink *beyond the background*.
//
// litCells counts anything non-zero, which used to be the same thing and now is
// not: the grid paints a background into every cell on Clear, so litCells is the
// full grid on every frame and every "did this style draw anything" assertion built
// on it became vacuous.
func cellsAboveBg(g *VizGrid) int {
	n := 0
	for i, v := range g.gray {
		if v != g.bgRamp || g.rgb[i] != g.bg {
			n++
		}
	}
	return n
}

func litCells(g *VizGrid) int {
	n := 0
	for _, v := range g.gray {
		if v != 0 {
			n++
		}
	}
	return n
}
