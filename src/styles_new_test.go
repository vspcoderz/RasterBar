package main

import (
	"math"
	"testing"
)

// --- the new styles ----------------------------------------------------------

// TestBandHzAgreesWithTheAnalyzersEdges is the test that stops bandHz drifting
// from the thing it describes.
//
// NewSpectrumAnalyzer builds the band layout into a private edges[] table and
// bandHz recomputes the same layout in closed form, so nothing but this test
// connects them. If someone changes the log spacing in the constructor, bandHz
// keeps answering the old question and scopeViz's zoom silently becomes wrong --
// a scope that is not zoomed correctly looks like a scope, so nothing else would
// report it.
//
// The tolerance is one FFT bin. edges[] is quantised to whole bins by
// int() truncation, so a band's upper edge can be up to one bin below the
// analytic value, and at the bottom of the range a band is narrower than a bin
// and is clamped regardless.
func TestBandHzAgreesWithTheAnalyzersEdges(t *testing.T) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)
	binHz := nyquistHz() / float64(fftSize/2)
	prev := 0.0
	for i := 0; i < bands; i++ {
		lo := float64(an.edges[i]) * binHz
		hi := float64(an.edges[i+1]) * binHz
		hz := bandHz(i, bands)
		if hz < lo-binHz {
			t.Errorf("band %d: bandHz = %.1fHz, below the analyser's own edge %.1fHz", i, hz, lo)
		}
		if hz > hi+binHz {
			t.Errorf("band %d: bandHz = %.1fHz, above the analyser's own edge %.1fHz", i, hz, hi)
		}
		if hz <= prev {
			t.Errorf("band %d: bandHz = %.1fHz, not above band %d's %.1fHz", i, hz, i-1, prev)
		}
		prev = hz
	}
	if hz := bandHz(0, bands); hz <= loBandHz {
		t.Errorf("band 0 centre = %.1fHz, not above the %.1fHz lowest edge", hz, loBandHz)
	}
	if hz := bandHz(bands-1, bands); hz >= nyquistHz() {
		t.Errorf("top band centre = %.1fHz, not below Nyquist %.1fHz", hz, nyquistHz())
	}
}

// TestBandHzSaturatesRatherThanInventingResolutions is the other half of the
// helper's contract: a style that resamples onto 128 columns must not be told
// there are 128 frequencies in there.
func TestBandHzSaturatesRatherThanInventingResolutions(t *testing.T) {
	// Past the analyser's band count, two drawn columns share one analyser band
	// and report the same frequency. Drawing more columns than the analysis can
	// resolve must not invent frequencies.
	n := 200
	if bandHz(100, n) != bandHz(100, n) {
		t.Fatal("bandHz is not deterministic")
	}
	a := bandHz(50, n)
	b := bandHz(51, n)
	if a != b {
		t.Errorf("columns 50 and 51 of %d reported %.2f and %.2fHz; the analyser has %d bands, so they share one",
			n, a, b, bands)
	}
	// A negative index is a bug in the caller, and it must not index backwards
	// into the table.
	if hz := bandHz(-1, bands); hz != loBandHz*math.Pow(nyquistHz()/loBandHz, 0.5/float64(bands)) {
		t.Errorf("bandHz(-1) = %v, which is not the clamped band 0 centre", hz)
	}
}

// TestDominantBandTiesResolveLow is the documented tie-break, and it matters
// because the tie case is a flat spectrum: a scope that zooms to whichever index
// it reached first would jump between 30Hz and 5kHz on music with no pitch.
func TestDominantBandTiesResolveLow(t *testing.T) {
	flat := make([]float64, bands)
	for i := range flat {
		flat[i] = 0.5
	}
	i, hz := dominantBand(flat)
	if i != 0 {
		t.Errorf("a flat spectrum resolved to band %d, want 0", i)
	}
	if math.Abs(hz-bandHz(0, bands)) > 1e-9 {
		t.Errorf("flat spectrum reported %.2fHz, want band 0's %.2fHz", hz, bandHz(0, bands))
	}
	// And the ordinary case: a single hot band wins.
	spike := make([]float64, bands)
	spike[24] = 1
	if i, _ := dominantBand(spike); i != 24 {
		t.Errorf("a spike at band 24 resolved to %d", i)
	}
}

// TestScopeShowsTwoAndAHalfCycles is the whole point of the scope rewrite, as a
// number.
//
// The bug was a window: 1024 samples at 11025Hz is 92.9ms, which is 40.8 cycles
// of a 440Hz note, drawn across 80 columns. That is two columns per cycle and it
// aliases into a band.
//
// Worked by hand: at 440Hz, 2.5 cycles is 2.5 * 11025 / 440 = 62.6 samples, and
// the sample count truncates to 62.
func TestScopeShowsTwoAndAHalfCycles(t *testing.T) {
	s := &scopeViz{}
	s.Resize(80, 22)
	// 440Hz is in analyser band 24: the centre of band i is
	// 30 * (5512.5/30)^((i+0.5)/48), and ln(440/30)/ln(183.75) = 0.515, so
	// (i+0.5)/48 = 0.515 gives i = 24.2.
	spike := make([]float64, bands)
	spike[24] = 1
	s.Push(&AudioFrame{Bands: spike, Wave: rampWave(fftSize)})
	// Push slews the zoom's pitch rather than jumping to it, so the window is
	// seeded directly for a clean assertion on the arithmetic.
	s.hz = 440
	if got := s.scopeSamples(); got != 62 {
		t.Errorf("scopeSamples at 440Hz = %d, want 62 (2.5 * 11025 / 440)", got)
	}
	// And the old behaviour, as the number it was: 1024 samples, 40.8 cycles.
	oldCycles := scopeCycles * 11025.0 / 1024.0
	if cycles := 1024.0 * 440 / spectrumHz; cycles < 40 {
		t.Errorf("the old window showed only %.1f cycles at 440Hz; this test was written against 40.8", cycles)
	} else {
		t.Logf("old window: %.1f cycles at 440Hz, %.2f shown today", cycles, oldCycles)
	}
	// The floor holds when the pitch is bright enough to want a tiny window.
	s.hz = 5000
	if got := s.scopeSamples(); got != scopeMinSamples {
		t.Errorf("scopeSamples at 5kHz = %d, want the floor %d", got, scopeMinSamples)
	}
	// And never more than the window it was given.
	s.hz = 40
	if got := s.scopeSamples(); got > fftSize {
		t.Errorf("scopeSamples at 40Hz = %d, more than the %d-sample window", got, fftSize)
	}
}

// TestScopeTriggerFindsARisingCrossing pins the stability mechanism. Without it
// the trace slides sideways with the note's phase and there is nothing to see.
func TestScopeTriggerFindsARisingCrossing(t *testing.T) {
	s := &scopeViz{}
	s.Resize(80, 22)
	// A 440Hz sine, so there is a rising crossing every 25 samples.
	w := make([]float64, fftSize)
	for i := range w {
		w[i] = math.Sin(2 * math.Pi * 440 * float64(i) / spectrumHz)
	}
	s.wave = append(s.wave, w...)
	got := s.triggerStart(200)
	if got <= 0 || got >= 100 {
		t.Fatalf("triggerStart = %d, want a crossing inside the first half of the window", got)
	}
	// It must be a *rising* crossing: the sample before is at or below zero and
	// this one above.
	if !(w[got-1] <= 0 && w[got] > 0) {
		t.Errorf("triggerStart = %d is not a rising crossing: w[%d] = %v, w[%d] = %v",
			got, got-1, w[got-1], got, w[got])
	}
	// Silence must not trigger: a lock onto a noise crossing freezes the trace,
	// which reads as a hung renderer.
	s.wave = make([]float64, fftSize)
	if got := s.triggerStart(200); got != 0 {
		t.Errorf("triggerStart on silence = %d, want 0 (no trigger below the floor)", got)
	}
}

// TestScopeBeamUsesExtentNotMean pins the other half of the "meh" diagnosis: the
// old Paint drew `sumRange(...)/(hi-lo)` per column, which is a lowpass, and a
// lowpass of a symmetric signal is zero.
//
// The fixture is the worst case for the old code and the best case for the new
// one: a slice of +0.9,+0.9,-0.9,-0.9 has a mean of exactly 0. The old trace
// would have drawn a flat centre line through it -- "no signal" -- while the
// signal is at 0.9 of full scale in both directions. beamRow has to span the
// grid.
func TestScopeBeamUsesExtentNotMean(t *testing.T) {
	s := &scopeViz{}
	s.Resize(80, 22)
	s.wave = []float64{0.9, 0.9, -0.9, -0.9}
	mid := float64(s.rows-1) / 2

	// The mean the old implementation would have drawn: exactly zero.
	mean := sumRange(s.wave, 0, 4) / 4
	if math.Abs(mean) > 1e-12 {
		t.Fatalf("fixture mean = %v, want exactly 0 for this test to mean anything", mean)
	}

	top, bot := s.beamRow(0, 4, mid)
	// 0.9 of a half-height of 10.5 is 9.45 rows, so the beam is rows 1..20 of 22:
	// near the full height, and centred on the middle row.
	if bot-top < (s.rows-2)*8/10 {
		t.Errorf("beam rows %d..%d (%d tall) for a ±0.9 slice; the mean of that slice is 0 and the old code drew it as silence",
			top, bot, bot-top+1)
	}
	if up, down := mid-float64(top), float64(bot)-mid; math.Abs(up-down) > 1 {
		t.Errorf("beam rows %d..%d are not symmetric about the mid row %.1f: %.1f above, %.1f below",
			top, bot, mid, up, down)
	}
	// A full-scale signal reaches further than a half-scale one, which is the
	// property the old mean-based trace could not have.
	s.wave = []float64{0.45, 0.45, -0.45, -0.45}
	halfTop, halfBot := s.beamRow(0, 4, mid)
	if halfBot-halfTop >= bot-top {
		t.Errorf("a half-scale slice drew a beam %d rows tall, not less than the full-scale %d",
			halfBot-halfTop, bot-top)
	}
}

// TestAuroraIsGatedByTheMusicLevel is a regression pin for a bug this batch
// introduced and caught: value noise is 0..1 by construction, so a field drawn
// straight from it lit 654 of a 672-cell grid with *silence* playing. The picture
// had stopped being a reaction to anything.
func TestAuroraIsGatedByTheMusicLevel(t *testing.T) {
	const cols, rows = 48, 14
	count := func(v Viz) int {
		for i := 0; i < 3; i++ {
			v.Push(&AudioFrame{Bands: make([]float64, bands), Wave: make([]float64, fftSize)})
		}
		g := NewVizGrid(cols, rows, false, 1)
		g.Clear()
		v.Paint(g)
		return cellsAboveBg(g)
	}
	silent := count(&auroraViz{})

	loud := &auroraViz{}
	loud.Resize(cols, rows)
	for i := 0; i < 6; i++ {
		loud.Push(&AudioFrame{Bands: rampBands(bands, 0.9), Wave: rampWave(fftSize)})
	}
	g := NewVizGrid(cols, rows, false, 1)
	g.Clear()
	loud.Paint(g)
	if lit := cellsAboveBg(g); lit <= silent {
		t.Errorf("aurora lit %d cells with music and %d in silence; the field is not reacting to anything",
			lit, silent)
	}
	// Silence must leave it substantially dark, not merely "less lit". The gate is
	// 0.1, so a uniform 0..1 field at that gain lands under the floor almost
	// everywhere.
	if silent > cols*rows/3 {
		t.Errorf("aurora lit %d of %d cells in silence, want a dark grid", silent, cols*rows)
	}
}

// TestMetroUsesTheTempoEstimate is the reason metro exists: BPM was computed by
// the onset detector and read by nothing but the HUD.
//
// The advance is one beat of travel, and the spacing is a property of the grid:
// at 60x16 the radius is 30, so 30/4 fronts means a spacing of 7.5 cells. At
// 120bpm an analysis is 1/analysisHz = 0.0929s, and a beat is 0.5s, so the front
// moves 7.5 * 0.5/0.0929 = 40.4 cells per analysis.
func TestMetroUsesTheTempoEstimate(t *testing.T) {
	m := &metroViz{}
	m.Resize(60, 16)
	wantSpacing := 30.0 / metroRings
	if got := m.spacing(); got != wantSpacing {
		t.Fatalf("spacing = %v, want %v", got, wantSpacing)
	}
	oneBeat := wantSpacing * (120.0 / 60.0) / analysisHz
	m.phase = 0
	m.Push(&AudioFrame{Bands: rampBands(bands, 0.5), BPM: 120})
	if math.Abs(m.phase-oneBeat) > 1e-9 {
		t.Errorf("after one 120bpm analysis the front advanced %.3f cells, want %.3f (one beat at %.2fHz)",
			m.phase, oneBeat, analysisHz)
	}
	// Twice the tempo, twice the speed, from the same start.
	m2 := &metroViz{}
	m2.Resize(60, 16)
	m2.Push(&AudioFrame{Bands: rampBands(bands, 0.5), BPM: 240})
	if math.Abs(m2.phase-2*oneBeat) > 1e-9 {
		t.Errorf("at 240bpm the front advanced %.3f cells, want %.3f", m2.phase, 2*oneBeat)
	}
}

// TestMetroFrontsAreLegible is the regression pin for the first metro, which was
// arithmetically fine and visually dead.
//
// At 128bpm the old front moved one cell per beat, so consecutive fronts were one
// cell apart and every cell in the grid was inside the front's width: the whole
// screen lit, every frame, which cost 1.2ms/op and showed a uniform glow. The
// spacing is now a fraction of the grid's radius, so the fronts have to be
// several cells apart.
func TestMetroFrontsAreLegible(t *testing.T) {
	m := &metroViz{}
	m.Resize(200, 60)
	s := m.spacing()
	if s < metroMinSpacing {
		t.Errorf("spacing = %v cells; the fronts would be adjacent and the screen would be uniformly lit", s)
	}
	// And the fraction of the grid that lights must be a small minority.
	m.Push(&AudioFrame{Bands: rampBands(bands, 0.6), BPM: 128})
	m.Push(&AudioFrame{Bands: rampBands(bands, 0.6), BPM: 128})
	g := NewVizGrid(200, 60, true, 2)
	g.Clear()
	m.Paint(g)
	lit := cellsAboveBg(g)
	if lit > 200*60/2 {
		t.Errorf("metro lit %d of %d cells; the fronts are too wide or too close to be rings",
			lit, 200*60)
	}
	if lit == 0 {
		t.Error("metro drew nothing")
	}
}

// TestMetroMovesWithoutATempo is the bug the fallback exists for. BPM is 0 until
// the detector has heard enough onsets, which is the whole first verse of most
// tracks. A style whose only input is the tempo that sits frozen for 40 seconds
// reads as a hung renderer.
func TestMetroMovesWithoutATempo(t *testing.T) {
	m := &metroViz{}
	m.Resize(60, 16)
	for i := 0; i < 5; i++ {
		m.Push(&AudioFrame{Bands: rampBands(bands, 0.5)})
	}
	if m.phase <= 0 {
		t.Error("metro's front never advanced with BPM == 0")
	}
	if !m.faked {
		t.Error("metro did not record that its tempo is the fallback")
	}
	// And a real tempo takes over from the fallback.
	m.Push(&AudioFrame{Bands: rampBands(bands, 0.5), BPM: 96})
	if m.faked {
		t.Error("metro kept the fallback tempo after a real estimate arrived")
	}
}

// TestScrollStylesOnlyAdvanceOnANewAnalysis is the waterfall rule applied to the
// two new scroll styles.
//
// Push runs at the analysis rate and Paint at up to 30Hz. A history that
// advanced per paint would scroll two identical rows for every real one and move
// at twice the speed of the music -- measured on waterfallViz before this test
// existed.
func TestScrollStylesOnlyAdvanceOnANewAnalysis(t *testing.T) {
	for _, mk := range []func() Viz{func() Viz { return &swellViz{} }, func() Viz { return &terrainViz{} }} {
		v := mk()
		const cols, rows = 32, 8
		v.Resize(cols, rows)
		v.Push(&AudioFrame{Bands: rampBands(bands, 0.8), Wave: rampWave(fftSize)})
		g := NewVizGrid(cols, rows, false, 1)
		g.Clear()
		v.Paint(g)
		first := cellsAboveBg(g)
		if first == 0 {
			t.Fatalf("%s drew nothing", v.Name())
		}
		// Three paints with no analysis between them must produce an identical
		// grid. Not "similar": identical, because the top row is the newest and
		// the rows below it are older copies of the same thing.
		want := gridCopy(g)
		for i := 0; i < 3; i++ {
			g.Clear()
			v.Paint(g)
			if !sameGrid(want, g) {
				t.Fatalf("%s changed its picture on paint %d with no new analysis", v.Name(), i+2)
			}
		}
		// And one more analysis does move it.
		v.Push(&AudioFrame{Bands: rampBands(bands, 0.2), Wave: rampWave(fftSize)})
		g.Clear()
		v.Paint(g)
		if sameGrid(want, g) {
			t.Errorf("%s did not scroll after a new analysis", v.Name())
		}
	}
}

func gridCopy(g *VizGrid) []byte {
	out := make([]byte, len(g.gray))
	copy(out, g.gray)
	return out
}

func sameGrid(want []byte, g *VizGrid) bool {
	if len(want) != len(g.gray) {
		return false
	}
	for i := range want {
		if want[i] != g.gray[i] {
			return false
		}
	}
	return true
}

// TestMatrixQuantisesToNearestDot pins the rounding that makes the meter a meter.
//
// 8 dots, so a band at 0.6 of full scale lights 5 of them: round(0.6*8) = 5, and
// floor would have said 4 and put every level in the lower eighth of its own
// range.
func TestMatrixQuantisesToNearestDot(t *testing.T) {
	const cols, rows = 20, 12
	m := &matrixViz{}
	m.Resize(cols, rows)
	if m.cells != matrixCells {
		t.Fatalf("cells = %d, want %d", m.cells, matrixCells)
	}
	// level 0.6: rampBands is not flat, so push an explicitly flat spectrum at a
	// known fraction by scaling the smoother directly through Push.
	loud := make([]float64, bands)
	for i := range loud {
		loud[i] = 0.6
	}
	m.Push(&AudioFrame{Bands: loud})
	g := NewVizGrid(cols, rows, false, 1)
	g.Clear()
	m.Paint(g)
	lit := litDotsInColumn(g, 0, m.rows-m.cells, m.cells)
	if lit != 5 {
		t.Errorf("a band at 0.6 lit %d of %d dots, want 5 (round(0.6*8))", lit, matrixCells)
	}
	// The dark LEDs are drawn too: the point of a hardware meter is that the
	// unlit ones are visible.
	if unlit := m.cells - lit; unlit != 3 {
		t.Errorf("%d dark dots expected, got %d", unlit, m.cells-lits(g))
	}
}

func lits(g *VizGrid) int { return cellsAboveBg(g) }

// litDotsInColumn counts the cells in one column's matrix that carry more than
// the gap ink, i.e. the ones the meter shows as lit.
func litDotsInColumn(g *VizGrid, x, base, cells int) int {
	n := 0
	for y := base; y < base+cells; y++ {
		if g.At(x, y) > rampFor(matrixGapInk) {
			n++
		}
	}
	return n
}

// TestPhosphorFadesAndClears covers the shared trail buffer's two invariants:
// it is a max, not a sum, and it decays on Push rather than on Paint.
func TestPhosphorFadesAndClears(t *testing.T) {
	p := phosphor{}
	p.Resize(8, 4, 0.5)
	p.Lay(3, 2, 0.8)
	if got := p.buf[2*8+3]; got != 0.8 {
		t.Fatalf("Lay stored %v, want 0.8", got)
	}
	// Max, not add: a second, fainter pass must not brighten the cell.
	p.Lay(3, 2, 0.2)
	if got := p.buf[2*8+3]; got != 0.8 {
		t.Errorf("a fainter Lay changed the cell to %v, want the max 0.8", got)
	}
	// Out of bounds is a no-op, not a panic: LayLine runs this on every segment.
	p.Lay(-1, 0, 1)
	p.Lay(0, 99, 1)
	p.LayLine(-5, -5, 99, 99, 1)

	p.Decay()
	if got := p.buf[2*8+3]; math.Abs(got-0.4) > 1e-9 {
		t.Errorf("after one decay the cell is %v, want 0.4 (0.8 * 0.5)", got)
	}
	p.Clear()
	for i, v := range p.buf {
		if v != 0 {
			t.Fatalf("Clear left %v non-zero at %d", v, i)
		}
	}
}

// TestPhosphorBlitLeavesDarkCellsAlone: a trail below the floor must leave the
// cell at the background, or the grid picks up a permanent dither one index
// above the background and the leak suite reports stale cells.
func TestPhosphorBlitLeavesDarkCellsAlone(t *testing.T) {
	p := phosphor{}
	p.Resize(6, 3, 0.9)
	p.buf[1] = phosphorFloor / 2
	p.buf[2] = 0.5
	g := NewVizGrid(6, 3, false, 1)
	g.SetPalette(palettes[0])
	g.Clear()
	p.Blit(g)
	if got := g.At(1, 0); got != g.bgRamp {
		t.Errorf("a cell below the floor was painted at ramp %d, want the background %d", got, g.bgRamp)
	}
	if got := g.At(2, 0); got == g.bgRamp {
		t.Error("a cell above the floor was left at the background")
	}
}
