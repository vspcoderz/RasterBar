package main

import (
	"testing"
)

// --- styles ------------------------------------------------------------------

// TestEveryStyleFillsItsGridAndSurvivesResize is the broad safety net.
//
// Every style is exercised through a push/paint cycle at three sizes, including
// the shrink that a window drag produces. A style that keeps per-grid state and is
// not told about a resize either paints out of bounds or leaves a band of stale
// cells, and neither shows up without actually resizing one.
func TestEveryStyleFillsItsGridAndSurvivesResize(t *testing.T) {
	sizes := []struct{ cols, rows int }{{80, 22}, {20, 6}, {200, 60}, {40, 20}}
	for _, mk := range vizRegistry {
		v := mk()
		for _, s := range sizes {
			v.Resize(s.cols, s.rows)
			v.Push(&AudioFrame{
				Bands: rampBands(bands, 0.6),
				Wave:  rampWave(fftSize),
				Beat:  0.8,
				BPM:   120,
			})
			g := NewVizGrid(s.cols, s.rows, true, 2)
			g.Clear()
			v.Paint(g)
			if lit := cellsAboveBg(g); lit == 0 {
				t.Errorf("%s at %dx%d painted nothing at all", v.Name(), s.cols, s.rows)
			}
			// Shrinking after a paint is the case that finds stale state.
			v.Resize(20, 6)
			v.Push(&AudioFrame{Bands: rampBands(bands, 0.6), Wave: rampWave(fftSize), Beat: 0.8})
			g2 := NewVizGrid(20, 6, false, 1)
			g2.Clear()
			v.Paint(g2)
			v.Reset()
			v.Push(&AudioFrame{Bands: rampBands(bands, 0.6), Wave: rampWave(fftSize), Beat: 0.8})
			g2.Clear()
			v.Paint(g2)
		}
	}
}

// TestScopeIsTheOnlyStyleThatUsesTheWaveform documents the division of labour and
// catches a style that has quietly started ignoring its input.

// TestScopeIsTheOnlyStyleThatUsesTheWaveform documents the division of labour and
// catches a style that has quietly started ignoring its input.
func TestStylesOnlyDrawFromTheirOwnInput(t *testing.T) {
	bandsOnly := &AudioFrame{Bands: rampBands(bands, 1.0), Beat: 1.0}
	for _, mk := range vizRegistry {
		v := mk()
		v.Resize(60, 16)
		v.Push(bandsOnly)
		g := NewVizGrid(60, 16, false, 1)
		g.Clear()
		v.Paint(g)
		name := v.Name()
		switch {
		case waveOnlyStyles[name]:
			if cellsAboveBg(g) != 0 {
				t.Errorf("%s drew something from bands alone; it must use the waveform", name)
			}
		case tempoStyles[name]:
			// Exempt, and deliberately so. metro's only input is a tempo that is
			// 0 until the detector commits, so drawing nothing here is its correct
			// behaviour rather than a missing input. An exempt style still has to
			// pass every other registry-wide suite, including the real-audio one
			// that requires it to have drawn something by the end of a fixture.
		default:
			if cellsAboveBg(g) == 0 {
				t.Errorf("%s drew nothing from bands alone", name)
			}
		}
	}
	// The sets are not a loophole: every name in them must be a registered style,
	// or a typo would silently exempt nothing -- or exempt a style that does not
	// exist and let a real one through unexamined.
	for _, set := range []struct {
		name string
		m    map[string]bool
	}{{"waveOnlyStyles", waveOnlyStyles}, {"tempoStyles", tempoStyles}} {
		for name := range set.m {
			if _, ok := VizByName(name); !ok {
				t.Errorf("%s names %q, which is not a registered style", set.name, name)
			}
		}
	}
}

// TestBarsDrawsABarOfTheExpectedHeight works the arithmetic out by hand.
//
// 40x10 grid, so the drawable height is rows-1 = 9 with the top row reserved for
// the peak cap. The bands are pushed loud and then three silent analyses, so the
// level and the peak have separated: level decays 0.82 per push and peak 0.93, so
// after three silent pushes level = 0.551 and peak = 0.804.
//
// h = int(0.551 * 9) = 4, so the bar fills grid rows 9,8,7,6. ph = int(0.804*9) = 7,
// which is above the bar and below the grid, so the cap goes on row 9-7 = 2.

// TestBarsDrawsABarOfTheExpectedHeight works the arithmetic out by hand.
//
// 40x10 grid, so the drawable height is rows-1 = 9 with the top row reserved for
// the peak cap. The bands are pushed loud and then three silent analyses, so the
// level and the peak have separated: level decays 0.82 per push and peak 0.93, so
// after three silent pushes level = 0.551 and peak = 0.804.
//
// h = int(0.551 * 9) = 4, so the bar fills grid rows 9,8,7,6. ph = int(0.804*9) = 7,
// which is above the bar and below the grid, so the cap goes on row 9-7 = 2.
func TestBarsDrawsABarOfTheExpectedHeight(t *testing.T) {
	const cols, rows = 40, 10
	const height = rows - 1

	v := &barsViz{}
	v.Resize(cols, rows)
	v.Push(&AudioFrame{Bands: ones(bands)})
	for i := 0; i < 3; i++ {
		v.Push(&AudioFrame{Bands: make([]float64, bands)})
	}

	lvl := v.viz.Level()[0]
	pk := v.viz.Peak()[0]
	wantH := int(lvl * height)
	wantPh := int(pk * height)
	if wantH <= 0 || wantPh <= wantH {
		t.Fatalf("setup: level %v peak %v gives no room for a cap (h=%d ph=%d)", lvl, pk, wantH, wantPh)
	}

	g := NewVizGrid(cols, rows, false, 1)
	g.Clear()
	v.Paint(g)

	for y := 0; y < wantH; y++ {
		row := rows - 1 - y
		if g.At(0, row) == 0 {
			t.Errorf("bar cell at row %d (of %d) is blank", row, wantH)
		}
	}
	// Everything above the bar must be blank: a bar that overdraws is claiming a
	// height it does not have.
	if row := rows - 1 - wantH; row >= 0 && g.At(0, row) != 0 {
		t.Errorf("row %d is drawn but is above the bar height %d", row, wantH)
	}
	// The cap, and a cap is the heaviest glyph in the ramp.
	capRow := rows - 1 - wantPh
	if g.At(0, capRow) != rampBright {
		t.Errorf("cap at row %d is %q, want the heaviest ramp character %q",
			capRow, rune(g.At(0, capRow)), rune(rampBright))
	}
}

// TestBarsCapIsSuppressedAtFullScale is the other half of the cap rule.
//
// At full scale the bar already occupies every drawable row, so there is no room
// above it. Drawing a cap anyway would either overwrite the top of the bar or
// claim a row that does not exist.

// TestBarsCapIsSuppressedAtFullScale is the other half of the cap rule.
//
// At full scale the bar already occupies every drawable row, so there is no room
// above it. Drawing a cap anyway would either overwrite the top of the bar or
// claim a row that does not exist.
func TestBarsCapIsSuppressedAtFullScale(t *testing.T) {
	v := &barsViz{}
	v.Resize(40, 10)
	v.Push(&AudioFrame{Bands: ones(bands)})
	g := NewVizGrid(40, 10, false, 1)
	g.Clear()
	v.Paint(g)
	// level == peak == 1, so the bar fills rows 1..9 and row 0 stays empty.
	for y := 1; y < 10; y++ {
		if g.At(0, y) == 0 {
			t.Errorf("row %d of a full bar is blank", y)
		}
	}
	if g.At(0, 0) != 0 {
		t.Errorf("row 0 drawn (%q) although the bar has no headroom for a cap",
			rune(g.At(0, 0)))
	}
}

// TestBarsDrawsOnlyTheAxisForSilence pins what a quiet passage looks like, and it
// used to assert the opposite.
//
// It wanted an empty grid. That was a reasonable thing to want at the time and it
// produced a black rectangle: no floor, no reference, and a silent band was
// indistinguishable from a column that was never drawn. Now silence is the grid's
// background plus one row of axis, and the test says so.
//
// The distinction that matters is height, not ink: silence must not draw anything
// *above* the axis, because that would be a bar pretending to be zero.

// TestBarsDrawsOnlyTheAxisForSilence pins what a quiet passage looks like, and it
// used to assert the opposite.
//
// It wanted an empty grid. That was a reasonable thing to want at the time and it
// produced a black rectangle: no floor, no reference, and a silent band was
// indistinguishable from a column that was never drawn. Now silence is the grid's
// background plus one row of axis, and the test says so.
//
// The distinction that matters is height, not ink: silence must not draw anything
// *above* the axis, because that would be a bar pretending to be zero.
func TestBarsDrawsOnlyTheAxisForSilence(t *testing.T) {
	const cols, rows = 40, 10
	v := &barsViz{}
	v.Resize(cols, rows)
	g := NewVizGrid(cols, rows, false, 1)
	for i := 0; i < 20; i++ {
		v.Push(&AudioFrame{Bands: make([]float64, bands)})
	}
	g.Clear()
	v.Paint(g)

	// Every column has its axis tick.
	for x := 0; x < cols; x++ {
		if g.At(x, rows-1) == 0 {
			t.Fatalf("column %d has no axis tick at the bottom row", x)
		}
	}
	// And nothing above it.
	for y := 0; y < rows-1; y++ {
		for x := 0; x < cols; x++ {
			if c := g.At(x, y); c != 0 && c != g.At(0, rows-1) {
				t.Errorf("cell (%d,%d) drawn for silence at %d, which is not the axis ink",
					x, y, c)
			}
		}
	}
}

// TestGridBackgroundIsNotBlack is the regression for the whole reason this change
// exists.
//
// An unlit cell was rgb 0, i.e. #000000, which on a black terminal is not "empty"
// so much as "the same colour as the terminal". The visualizer then looked like
// scattered debris floating in nothing, and a sparse style gave no sense of how big
// the picture was.

// TestBaselineIsAboveTheBackground pins the axis at the contrast it claims.
//
// baselineInk is documented as "above the background, well below anything a bar
// draws". If it ever drops to the background's own value the axis disappears, which
// is the bug this was added to fix.
func TestBaselineIsAboveTheBackground(t *testing.T) {
	g := NewVizGrid(8, 4, true, 1)
	g.SetPalette(paletteAt(0))
	g.Clear()
	if rampFor(baselineInk) <= g.bgRamp {
		t.Errorf("baseline ramp index %d is not above the background index %d",
			rampFor(baselineInk), g.bgRamp)
	}
}

// TestWaterfallDoesNotScrollWithoutNewAudio pins the dirty flag.
//
// Push captures a row and Paint scrolls it once. Without the flag, Paint would
// scroll a duplicate row on every frame the renderer was faster than the audio --
// 30 renders a second against 11 analyses -- and the history would move at nearly
// three times the speed of the music.

// TestWaterfallDoesNotScrollWithoutNewAudio pins the dirty flag.
//
// Push captures a row and Paint scrolls it once. Without the flag, Paint would
// scroll a duplicate row on every frame the renderer was faster than the audio --
// 30 renders a second against 11 analyses -- and the history would move at nearly
// three times the speed of the music.
func TestWaterfallDoesNotScrollWithoutNewAudio(t *testing.T) {
	v := &waterfallViz{}
	v.Resize(40, 10)
	g := NewVizGrid(40, 10, false, 1)
	v.Push(&AudioFrame{Bands: rampBands(bands, 1.0)})
	g.Clear()
	v.Paint(g)

	first := topRowSnapshot(g)
	// Paint repeatedly with no intervening Push.
	for i := 0; i < 5; i++ {
		v.Paint(g)
	}
	if got := topRowSnapshot(g); got != first {
		t.Error("waterfall scrolled without new audio to scroll in")
	}
	// And it does scroll when there is new audio.
	v.Push(&AudioFrame{Bands: make([]float64, bands)})
	v.Paint(g)
	if topRowSnapshot(g) == first {
		t.Error("waterfall did not scroll after a new analysis")
	}
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
func TestRadialCorrectsForCellAspect(t *testing.T) {
	const cols, rows = 60, 30
	v := &radialViz{}
	v.Resize(cols, rows)

	// Half scale, so the vertical extent stays clear of the top of the grid. See
	// the note above: at full scale the aspect correction is unobservable.
	half := make([]float64, bands)
	for i := range half {
		half[i] = 0.5
	}
	v.Push(&AudioFrame{Bands: half})
	g := NewVizGrid(cols, rows, false, 1)
	g.Clear()
	v.Paint(g)

	minX, maxX, minY, maxY := cols, -1, rows, -1
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			if g.At(x, y) == 0 {
				continue
			}
			if x < minX {
				minX = x
			}
			if x > maxX {
				maxX = x
			}
			if y < minY {
				minY = y
			}
			if y > maxY {
				maxY = y
			}
		}
	}
	if maxY < 0 {
		t.Fatal("radial drew nothing for a fully lit spectrum")
	}

	const cellPx = 2 // rows are twice as tall as columns; that is the premise
	widthPx := maxX - minX
	heightPx := (maxY - minY) * cellPx
	if widthPx == 0 || heightPx == 0 {
		t.Fatalf("degenerate shape: %dpx wide, %dpx tall", widthPx, heightPx)
	}
	ratio := float64(heightPx) / float64(widthPx)
	if ratio < 0.40 || ratio > 0.62 {
		t.Errorf("radial is %.2f as tall as it is wide in pixels (%dpx vs %dpx); "+
			"want about 0.5 for a half-disc. A ratio near 1.0 means the aspect "+
			"correction is missing and the fan is stretched vertically.",
			ratio, heightPx, widthPx)
	}
}

// TestParticleBudgetIsCapped pins the low-end promise.
//
// The plan is explicit that the cap is applied at construction, not in the paint
// loop, because a 300x120 grid is 36000 cells of particle state and clearing that
// on every seek is a GC pause on the machine that can least afford one. The budget
// does scale with the grid below the ceiling -- a small terminal has fewer cells to
// put particles in -- but it stops dead at 400 rather than following the cells.

// TestParticleBudgetIsCapped pins the low-end promise.
//
// The plan is explicit that the cap is applied at construction, not in the paint
// loop, because a 300x120 grid is 36000 cells of particle state and clearing that
// on every seek is a GC pause on the machine that can least afford one. The budget
// does scale with the grid below the ceiling -- a small terminal has fewer cells to
// put particles in -- but it stops dead at 400 rather than following the cells.
func TestParticleBudgetIsCapped(t *testing.T) {
	const ceiling = 400
	for _, sz := range []struct{ cols, rows int }{
		{80, 24}, {200, 60}, {300, 120}, {1000, 1000},
	} {
		got := particleBudget(sz.cols, sz.rows)
		if got > ceiling {
			t.Errorf("particleBudget(%d,%d) = %d, over the ceiling of %d",
				sz.cols, sz.rows, got, ceiling)
		}
		// Never more particles than there are cells to put them in, or the style
		// paints a solid block instead of a field.
		if got > sz.cols*sz.rows {
			t.Errorf("particleBudget(%d,%d) = %d, more than the grid has cells",
				sz.cols, sz.rows, got)
		}
	}
	// Saturated at the ceiling on a huge grid, so the cost stops growing.
	if got := particleBudget(300, 120); got != ceiling {
		t.Errorf("particleBudget(300,120) = %d, want it saturated at %d", got, ceiling)
	}
	// Degenerate grid still gets something, so the style is never a no-op.
	if got := particleBudget(1, 1); got < 2 {
		t.Errorf("particleBudget(1,1) = %d, want a usable floor", got)
	}
}

// TestParticlesNeverExceedTheirBudget is the invariant the cap exists to protect.

// TestParticlesNeverExceedTheirBudget is the invariant the cap exists to protect.
func TestParticlesNeverExceedTheirBudget(t *testing.T) {
	v := &particlesViz{}
	v.Resize(80, 24)
	budget := v.budget
	// Hammer it with onsets far more often than real music produces them.
	for i := 0; i < 500; i++ {
		v.Push(&AudioFrame{Bands: rampBands(bands, 1.0), Beat: 1.0})
		if v.alive > budget {
			t.Fatalf("alive = %d, over the budget of %d", v.alive, budget)
		}
	}
	// And a long silence retires everything, so the field can recover.
	for i := 0; i < 200; i++ {
		v.Push(&AudioFrame{Bands: make([]float64, bands)})
	}
	if v.alive != 0 {
		t.Errorf("alive = %d after silence, want 0", v.alive)
	}
}

// TestHeavyStylesAreMarkedAndCapped keeps the low-end promise honest: the
// expensive styles have to say so, and their cost has to stop growing past some
// point rather than tracking the cell count forever.

// TestHeavyStylesAreMarkedAndCapped keeps the low-end promise honest: the
// expensive styles have to say so, and their cost has to stop growing past some
// point rather than tracking the cell count forever.
func TestHeavyStylesAreMarkedAndCapped(t *testing.T) {
	for _, mk := range vizRegistry {
		v := mk()
		// The scale is a ratio against the style's own ceiling, so 1 means
		// "already at the cap" rather than "uncapped". What must never happen is
		// going above 1 or to zero.
		for _, sz := range []struct{ cols, rows int }{{80, 24}, {300, 120}, {1000, 1000}} {
			scale := v.CapScale(sz.cols, sz.rows)
			if scale <= 0 || scale > 1 {
				t.Errorf("%s CapScale(%d,%d) = %v, want a value in (0,1]",
					v.Name(), sz.cols, sz.rows, scale)
			}
		}
		// The set, not a count. `heavy == 2` pins the *number* and says nothing
		// about *which* styles are expensive, so an eleventh style would satisfy
		// the assertion while the new expensive one quietly claimed to be cheap.
		// Both directions are checked: a style that is heavy must be declared, and
		// a declared style must actually be heavy, because the declaration is what
		// makes the render loop apply the cap.
		name := v.Name()
		if v.Heavy() && !heavyStyles[name] {
			t.Errorf("%s reports Heavy but is not in heavyStyles", name)
		}
		if !v.Heavy() && heavyStyles[name] {
			t.Errorf("%s is in heavyStyles but does not report Heavy, so the cap never applies", name)
		}
	}
	for name := range heavyStyles {
		if _, ok := VizByName(name); !ok {
			t.Errorf("heavyStyles names %q, which is not a registered style", name)
		}
	}
}

// TestHeavyStylesCostStopsGrowing is the real low-end claim: past the ceiling, a
// bigger terminal must not cost proportionally more.

// TestHeavyStylesCostStopsGrowing is the real low-end claim: past the ceiling, a
// bigger terminal must not cost proportionally more.
func TestHeavyStylesCostStopsGrowing(t *testing.T) {
	// 300x120 is where the particle budget saturates, so 600x240 must cost the
	// same as 300x120 and not four times as much.
	if particleBudget(600, 240) != particleBudget(300, 120) {
		t.Errorf("particle budget grows past the ceiling: %d vs %d",
			particleBudget(300, 120), particleBudget(600, 240))
	}
	// Radial's scale is a ratio against a cell budget, so it also has to stop
	// shrinking and then hold.
	small := (&radialViz{}).CapScale(300, 120)
	huge := (&radialViz{}).CapScale(1000, 1000)
	if huge > small {
		t.Errorf("radial CapScale grows with the grid: %v at 300x120, %v at 1000x1000", small, huge)
	}
	if huge < 0.35-1e-9 {
		t.Errorf("radial CapScale = %v, below the documented floor of 0.35", huge)
	}
}

// TestRegistryNamesAreUniqueAndResolvable keeps `--viz` and the HUD honest.

// TestRegistryNamesAreUniqueAndResolvable keeps `--viz` and the HUD honest.
func TestRegistryNamesAreUniqueAndResolvable(t *testing.T) {
	seen := map[string]bool{}
	for _, name := range VizNames() {
		if name == "" {
			t.Error("a style has no name")
		}
		if seen[name] {
			t.Errorf("duplicate style name %q", name)
		}
		seen[name] = true
		if _, ok := VizByName(name); !ok {
			t.Errorf("VizByName(%q) failed for a registered style", name)
		}
	}
	if _, ok := VizByName("nonexistent"); ok {
		t.Error("VizByName resolved a style that does not exist")
	}
}

// TestEveryRegistryStyleIsInThePlan checks the list did not quietly drift.

// TestEveryRegistryStyleIsInThePlan checks the list did not quietly drift.
//
// A count, so a style added without a line here is visible in review. The rules
// that actually need pinning are the sets in viz.go, which this does not replace:
// 18 is a number, and a number says nothing about whether the new style was
// measured, capped or given a doc comment.
func TestEveryRegistryStyleIsInThePlan(t *testing.T) {
	if got := len(vizRegistry); got != 18 {
		t.Errorf("%d styles registered, want 18", got)
	}
}

// TestResetDropsEveryStyleState is the seek case, across the whole registry.
//
// A style that keeps a history and does not clear it on Reset draws the part of
// the song you just left, at the position you jumped to. That is invisible to
// every other test here -- they push, paint and assert -- because the state is
// always consistent with the audio it was fed.
//
// Eleven of the eighteen keep state, so this is the one test that would catch
// the eleventh forgetting.
//
// The bar is a *control* instance of the same style fed nothing but silence, not
// zero lit cells. That distinction is the whole test: matrixViz deliberately
// draws its unlit LEDs and metroViz deliberately draws its tempo front, so both
// light cells with no audio at all, and a "must be dark" assertion would fail
// them for drawing what they are supposed to draw. Comparing against a control
// isolates the thing that is actually the bug -- state that survived a reset
// that nothing but silence could have produced.
func TestResetDropsEveryStyleState(t *testing.T) {
	const cols, rows = 48, 14
	silence := func() *AudioFrame {
		return &AudioFrame{Bands: make([]float64, bands), Wave: make([]float64, fftSize)}
	}
	litAfter := func(v Viz) int {
		for i := 0; i < 3; i++ {
			v.Push(silence())
		}
		g := NewVizGrid(cols, rows, true, 2)
		g.Clear()
		v.Paint(g)
		return cellsAboveBg(g)
	}
	for _, mk := range vizRegistry {
		t.Run(mk().Name(), func(t *testing.T) {
			v := mk()
			v.Resize(cols, rows)
			// Loud and hot, several analyses deep, so any history is full.
			for i := 0; i < 6; i++ {
				v.Push(&AudioFrame{
					Bands: rampBands(bands, 0.9),
					Wave:  rampWave(fftSize),
					Beat:  1,
					BPM:   128,
				})
			}
			g := NewVizGrid(cols, rows, true, 2)
			g.Clear()
			v.Paint(g)
			if cellsAboveBg(g) == 0 {
				t.Fatalf("%s drew nothing before Reset, so this proves nothing", v.Name())
			}
			v.Reset()
			got := litAfter(v)
			// The control: a fresh instance of the same style, never fed anything
			// but silence.
			control := mk()
			control.Resize(cols, rows)
			want := litAfter(control)
			// Slack for a phosphor trail still decaying and for a style whose own
			// silence drawing depends on when the push happened. Not a full screen.
			slack := cols * rows / 16
			if got > want+slack {
				t.Errorf("%s left %d cells lit after Reset on silence; a fresh instance draws %d, want <= %d (+%d slack)",
					v.Name(), got, want, want, slack)
			}
		})
	}
}

// --- preferences -------------------------------------------------------------

// TestVizPrefsCycleWraps is the behaviour the `v` and `c` keys depend on: there is
// no end of the list and no dead key.
