package main

import (
	"math"
	"strings"
	"testing"
)

// The visualizers are tested at the seams that can be wrong without a terminal:
// the band resampling, the ramp round-trip, the onset arithmetic, and one paint
// per style against a hand-worked grid.
//
// What is deliberately not tested here: anything about how the bytes reach the
// screen. VizGrid produces exactly the frame layouts the existing renderers
// already speak, so screen_test.go's no-stale-cells suite covers that path for
// free -- re-proving it for visualizers would be testing the renderers twice.

// --- ramp round-trip ---------------------------------------------------------

// TestRampLumRoundTripsThroughLevelFor pins the trick that lets a style choose an
// exact glyph and still paint through DiffRenderer unchanged.
//
// levelFor is what DiffRenderer calls on every cell. rampLum has to produce a
// luminance that maps back to the same ramp index, or every visualizer cell shows
// the neighbouring glyph's ink density and the whole grid is subtly wrong in a way
// no amount of staring at it would explain.
func TestRampLumRoundTripsThroughLevelFor(t *testing.T) {
	for idx := 0; idx < len(ramp); idx++ {
		got := levelFor(rampLum(idx))
		if got != ramp[idx] {
			t.Errorf("levelFor(rampLum(%d)) = %q, want %q", idx, got, ramp[idx])
		}
	}
}

func TestRampLumClampsOutOfRange(t *testing.T) {
	if got := rampFor(-1); got != 0 {
		t.Errorf("rampFor(-1) = %d, want the darkest glyph index 0", got)
	}
	if got := rampFor(2); got != byte(len(ramp)-1) {
		t.Errorf("rampFor(2) = %d, want the brightest index %d", got, len(ramp)-1)
	}
}

// TestRampForIsNotInverted guards the direction of the mapping, which is the one
// thing about it that is genuinely counter-intuitive.
//
// The ramp runs from ' ' to '@' in order of ink, so a HIGH index is a cell that
// lays down a lot of ink. Loud therefore means a high index. Getting this backwards
// renders a quiet passage as a wall of solid blocks and a loud one as silence.
func TestRampForIsNotInverted(t *testing.T) {
	quiet := rampFor(0.0)
	loud := rampFor(1.0)
	if quiet >= loud {
		t.Errorf("rampFor is inverted: quiet=%d loud=%d", quiet, loud)
	}
	if quiet != 0 {
		t.Errorf("rampFor(0) = %d, want 0 (a blank cell)", quiet)
	}
	if loud != byte(len(ramp)-1) {
		t.Errorf("rampFor(1) = %d, want %d", loud, len(ramp)-1)
	}
}

// --- band resampling ---------------------------------------------------------

func TestResampleBandsShrinksByAveraging(t *testing.T) {
	// 4 bands down to 2: each output is the mean of its pair, so no energy is lost.
	// Taking every other band instead would drop half the spectrum, which is what
	// makes a hi-hat vanish at low band counts.
	src := []float64{0, 1, 0, 1}
	out := make([]float64, 2)
	resampleBands(src, 2, out)
	if out[0] != 0.5 || out[1] != 0.5 {
		t.Errorf("shrink = %v, want [0.5 0.5]", out)
	}
}

func TestResampleBandsGrowsByRepeating(t *testing.T) {
	// Growing must NOT interpolate. An invented peak between two real ones is a
	// lie about the music, and the spectrum is the one thing here that is supposed
	// to be a measurement.
	src := []float64{0, 0, 1, 0}
	out := make([]float64, 8)
	resampleBands(src, 8, out)
	for i, v := range out {
		if v != 0 && v != 1 {
			t.Errorf("out[%d] = %v, invented an intermediate value", i, v)
		}
	}
	if out[4] != 1 || out[5] != 1 {
		t.Errorf("peak not repeated onto its columns: %v", out)
	}
}

func TestResampleBandsEmptySourceIsFlat(t *testing.T) {
	out := make([]float64, 4)
	resampleBands(nil, 4, out)
	for i, v := range out {
		if v != 0 {
			t.Errorf("out[%d] = %v, want 0 from an empty source", i, v)
		}
	}
}

// TestResampleBandsIdentity proves the analyser runs at a pass-through when the
// grid asks for exactly the bands it produced. 48 bands on a 48-column terminal is
// the common case and must not be altered.
func TestResampleBandsIdentity(t *testing.T) {
	src := make([]float64, bands)
	for i := range src {
		src[i] = float64(i) / float64(bands-1)
	}
	out := make([]float64, bands)
	resampleBands(src, bands, out)
	for i := range src {
		if out[i] != src[i] {
			t.Fatalf("band %d: got %v want %v", i, out[i], src[i])
		}
	}
}

func TestBandCountForClampsToTheAnalyserCeiling(t *testing.T) {
	// Wider than the ceiling: capped, or a 300-column terminal asks for 300 bands
	// from an analyser that produces 48.
	if got := bandCountFor(300); got != maxDrawnBands {
		t.Errorf("bandCountFor(300) = %d, want %d", got, maxDrawnBands)
	}
	if got := bandCountFor(40); got != 40 {
		t.Errorf("bandCountFor(40) = %d, want 40 (one per column)", got)
	}
	// Degenerate width must not produce a zero or a negative.
	if got := bandCountFor(0); got < 2 {
		t.Errorf("bandCountFor(0) = %d, want at least 2", got)
	}
}

// --- onset detection ---------------------------------------------------------

// TestOnsetDetectorNeverFiresOnSilence covers the floor.
//
// Without onsetFloor, flux is zero, the mean is zero, and the comparison is
// 0 > 0*1.6 -- which is false, but only just. A single sample of dither in a
// digital-silent passage is many times the mean and reads as a beat, so the whole
// envelope becomes a strobe during the quiet parts of a track.
func TestOnsetDetectorNeverFiresOnSilence(t *testing.T) {
	d := NewOnsetDetector(4)
	quiet := []float64{0, 0, 0, 0}
	for i := 0; i < 200; i++ {
		if _, onset := d.Push(quiet); onset {
			t.Fatalf("onset on silence at analysis %d", i)
		}
	}
	if d.Beat() != 0 {
		t.Errorf("beat envelope = %v after silence, want 0", d.Beat())
	}
}

// TestOnsetDetectorFiresOnATransient is the positive case: after the history is
// full of silence, a full-scale step in every band is unambiguously a transient.
//
// Four bands all going 0 -> 1 gives flux = 4/4 = 1.0 after the per-band
// normalisation, against a mean of 0 and a floor of 0.02.
func TestOnsetDetectorFiresOnATransient(t *testing.T) {
	d := NewOnsetDetector(4)
	quiet := []float64{0, 0, 0, 0}
	for i := 0; i < 40; i++ {
		d.Push(quiet)
	}
	beat, onset := d.Push([]float64{1, 1, 1, 1})
	if !onset {
		t.Fatal("no onset on a full-scale step after 40 silent analyses")
	}
	if beat != beatRise {
		t.Errorf("beat = %v on onset, want %v", beat, beatRise)
	}
}

// TestOnsetDetectorIgnoresReleases is the rule that stops a second "beat" per note.
//
// Spectral flux uses the positive difference only, so a decaying spectrum is a
// release and produces no flux at all. If releases counted, every note would
// report two onsets and the tempo estimate would run at double the truth.
func TestOnsetDetectorIgnoresReleases(t *testing.T) {
	d := NewOnsetDetector(4)
	quiet := []float64{0, 0, 0, 0}
	for i := 0; i < 40; i++ {
		d.Push(quiet)
	}
	if _, onset := d.Push([]float64{1, 1, 1, 1}); !onset {
		t.Fatal("setup: expected an onset on the step up")
	}
	// Now decay. Every band goes down, so the positive difference is zero.
	for step := 0.9; step > 0; step -= 0.1 {
		v := []float64{step, step, step, step}
		if _, onset := d.Push(v); onset {
			t.Errorf("onset on a release to %v: releases must not fire", v[0])
		}
	}
}

// TestOnsetDetectorDecaysBetweenOnsets pins the envelope's shape.
//
// The strobe draws the envelope, never the raw onset flag, because a flag that is
// true for exactly one analysis is invisible at 30fps. The decay has to be fast
// enough that consecutive eighth notes do not sum into a plateau, and the test is
// on the ratio rather than on an absolute value so the intent is what is pinned.
func TestOnsetDetectorDecaysBetweenOnsets(t *testing.T) {
	d := NewOnsetDetector(4)
	quiet := []float64{0, 0, 0, 0}
	for i := 0; i < 40; i++ {
		d.Push(quiet)
	}
	d.Push([]float64{1, 1, 1, 1})
	before := d.Beat()
	d.Push(quiet)
	after := d.Beat()
	if after >= before {
		t.Fatalf("envelope did not decay: %v -> %v", before, after)
	}
	if got := after / before; math.Abs(got-beatDecay) > 1e-9 {
		t.Errorf("one silent analysis decayed by %v, want exactly %v", got, beatDecay)
	}
}

// TestOnsetDetectorWarmupIsSilent pins the window requirement.
//
// The mean is only meaningful once the window is full, and firing before then
// means the first transient of every track reports a beat during what is usually
// silence -- the first 3 seconds of a lofi track, or of a fade-in.
func TestOnsetDetectorWarmupIsSilent(t *testing.T) {
	d := NewOnsetDetector(4)
	quiet := []float64{0, 0, 0, 0}
	// Fill everything but the last slot.
	for i := 0; i < onsetWindow-1; i++ {
		d.Push(quiet)
	}
	if _, onset := d.Push([]float64{1, 1, 1, 1}); onset {
		t.Fatal("fired before the window was full")
	}
	// The next analysis now has a full window and a mean that includes the spike.
	// It should still not fire, because the spike's own flux raised the bar.
	if _, onset := d.Push([]float64{1, 1, 1, 1}); onset {
		t.Error("two identical steps in a row: the second is not a new onset")
	}
}

// --- tempo -------------------------------------------------------------------

// TestTempoOfUsesTheMedianNotTheMean is the property the estimate rests on.
//
// One missed onset between two beats doubles one interval. Eight samples: the
// median is unmoved by one or two wrong values, the mean is not. With a mean, a
// single dropped beat in a busy passage would put the readout 20% out.
func TestTempoOfUsesTheMedianNotTheMean(t *testing.T) {
	// Seven intervals of 6, one of 12 (a single missed onset).
	onsets := []int{0, 6, 12, 18, 24, 30, 36, 48}
	if got := tempoOf(onsets); got <= 0 {
		t.Fatalf("tempoOf returned %v", got)
	}

	// Work out the same answer from the median interval directly: 6 blocks.
	wantPeriod := 6 * BlockDur()
	if want := 60 / wantPeriod; math.Abs(tempoOf(onsets)-want) > 0.01 {
		t.Errorf("tempo = %v, want the median interval's %v", tempoOf(onsets), want)
	}
}

func TestTempoOfNeedsThreeOnsets(t *testing.T) {
	// One or two onsets cannot distinguish a tempo from a fluke, so it says nothing
	// rather than printing a confident wrong number.
	if got := tempoOf([]int{0, 6}); got != 0 {
		t.Errorf("tempoOf(2 onsets) = %v, want 0 (not enough evidence)", got)
	}
	if got := tempoOf(nil); got != 0 {
		t.Errorf("tempoOf(nil) = %v, want 0", got)
	}
}

// TestTempoOfPromotesHalfTime pins the doubling branch.
//
// A detector cannot tell a beat from a half beat, and picking the faster of the
// two plausible readings is the one that tracks what people tap along to. Twelve
// blocks apart is 1.11s per hit = 54bpm, below the 60 floor, so it doubles the
// interval to 24 blocks = 107.7bpm.
func TestTempoOfPromotesHalfTime(t *testing.T) {
	got := tempoOf([]int{0, 12, 24, 36, 48})
	// 12 blocks is 1.11s per onset = 53.8bpm, below the floor. Halving the
	// interval to 6 blocks doubles the rate to 107.7, which is the reading that
	// matches what someone would tap along to.
	want := 60 / (6 * BlockDur())
	if math.Abs(got-want) > 0.01 {
		t.Errorf("tempo = %v, want %v (12 blocks halved to 6)", got, want)
	}
	if got <= 60 || got >= 180 {
		t.Errorf("tempo = %v, outside the 60-180 range it promises", got)
	}
}

// TestTempoOfRefusesAbsurdRates covers both ends of the clamp.
//
// A tempo outside the range is not a tempo, it is the detector hearing a hi-hat.
// Reporting 400bpm would be worse than reporting nothing.
func TestTempoOfRefusesAbsurdRates(t *testing.T) {
	// Two blocks apart is 0.186s per onset = 323bpm. Halving gets further away,
	// doubling the interval to 4 blocks gives 161bpm, which is in range -- so this
	// one resolves rather than failing. What must not happen is a rate outside the
	// range ever being reported.
	for _, onsets := range [][]int{
		{0, 2, 4, 6, 8},
		{0, 4, 8, 12, 16},
		{0, 200, 400, 600, 800},
	} {
		if got := tempoOf(onsets); got != 0 && (got < 60 || got > 180) {
			t.Errorf("tempoOf(%v) = %v, outside the range it promises", onsets, got)
		}
	}
}

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

func TestMedianAveragesTheMiddlePair(t *testing.T) {
	if got := median([]float64{3, 1, 2}); got != 2 {
		t.Errorf("median of 3 values = %v, want 2", got)
	}
	// Even count: the two middle values average.
	if got := median([]float64{4, 1, 3, 2}); got != 2.5 {
		t.Errorf("median of 4 values = %v, want 2.5", got)
	}
	if got := median(nil); got != 0 {
		t.Errorf("median(nil) = %v, want 0", got)
	}
}

// TestOnsetsRecurOnARepeatingPulseTrain is the regression test for the bug that
// made the particle field spawn four dots and then stop.
//
// It runs a real gated pulse train through the real SpectrumAnalyzer and the real
// onsetDetector, because the bug lived in the seam between them: the detector was
// being handed the analyser's *smoothed* output, whose 0.18 release leaves each
// band at 82% of its previous value, so consecutive transients produced
// geometrically smaller rises until the flux fell under the threshold for good.
//
// Handing the detector the raw magnitudes is the fix, and this test is what holds
// it there: an onset count of one would be a pass against almost any other
// assertion.
func TestOnsetsRecurOnARepeatingPulseTrain(t *testing.T) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)
	d := NewOnsetDetector(bands)

	// One 50ms burst of 440Hz every five analyses, i.e. ~129bpm. 50ms is shorter
	// than one 93ms analysis window, which is the hard case: the transient has to
	// be caught by the window it happens to land in rather than by a window
	// straddling it.
	//
	// The run has to outlast the warmup. onsetWindow is 32 analyses, so the first
	// ~6.4 beats are spent filling the baseline and produce no onsets at all by
	// design. A shorter test than that scores 1 onset and looks like the bug it
	// is meant to catch -- which is exactly what happened the first time.
	const gateBlocks = 5
	const beats = 24
	window := make([]float64, fftSize)
	onsets := 0

	for b := 0; b < gateBlocks*beats; b++ {
		for i := range window {
			if b%gateBlocks == 0 && i < fftSize/18 { // ~50ms
				window[i] = 0.6 * math.Sin(2*math.Pi*440*float64(i)/spectrumRate)
			} else {
				window[i] = 0
			}
		}
		an.Analyze(window)
		if _, onset := d.Push(an.Raw()); onset {
			onsets++
		}
	}

	// 24 beats, minus ~6.4 of warmup, so somewhere around 17. A detector that only
	// catches the first transient scores 1; one that over-fires scores in the
	// dozens.
	if onsets < 12 {
		t.Errorf("only %d onsets across %d beats; the detector stopped after the first", onsets, beats)
	}
	if onsets > 20 {
		t.Errorf("%d onsets across %d beats; it is firing on noise as well as beats", onsets, beats)
	}

	// And the tempo estimate, which is only reachable if the onsets kept coming.
	// Derived from the gate rather than hardcoded, so changing gateBlocks cannot
	// silently invalidate the expectation.
	want := 60 / (gateBlocks * BlockDur())
	if got := d.BPM(); math.Abs(got-want) > 1.0 {
		t.Errorf("BPM = %v, want %v for a %d-analysis gate", got, want, gateBlocks)
	}
}

// TestOnsetsDoNotFiringOnASteadyTone is the other half: a tone with no transient
// in it must produce exactly one onset, at the start, and then silence.
func TestOnsetsDoNotFireOnASteadyTone(t *testing.T) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)
	d := NewOnsetDetector(bands)

	window := make([]float64, fftSize)
	onsets := 0
	for b := 0; b < 60; b++ {
		for i := range window {
			window[i] = 0.5 * math.Sin(2*math.Pi*440*float64(i)/spectrumRate)
		}
		an.Analyze(window)
		if _, onset := d.Push(an.Raw()); onset {
			onsets++
		}
	}
	// The window has to fill first, so the attack at the start is not even judged.
	// After that a constant tone has zero flux every window and cannot onset.
	if onsets != 0 {
		t.Errorf("%d onsets on a steady tone, want 0: a tone that never changes has nothing to detect", onsets)
	}
}

// --- palettes ----------------------------------------------------------------

// TestPaletteEndpointsDiffer pins the two ends of every hue sweep that claims to
// encode frequency.
//
// If the bass and the treble map to the same colour, a palette that says it is
// showing you the spectrum is showing you nothing: there is no way to tell which
// end of the log-spaced bands you are looking at.
func TestPaletteEndpointsDiffer(t *testing.T) {
	byBand := 0
	for _, p := range palettes {
		if !p.byBand {
			continue
		}
		byBand++
		lo := p.fn(0, 0.5, 0)
		hi := p.fn(1, 0.5, 0)
		if lo == hi {
			t.Errorf("palette %q claims to encode band but band 0 and band 1 are both %06x", p.name, lo)
		}
	}
	if byBand == 0 {
		t.Error("no palette encodes band; the byBand flag is not being maintained")
	}
}

// TestHeightPaletteIgnoresBand is the other half of that contract, and the
// omission is deliberate.
//
// `height` colours by cell height so the eye reads loudness. If it also swept the
// hue across the spectrum it would be encoding two things at once and neither
// would be readable.
func TestHeightPaletteIgnoresBand(t *testing.T) {
	h := paletteByName("height")
	if h.fn == nil {
		t.Fatal("the height palette has no function")
	}
	if h.byBand {
		t.Error("height is marked as encoding band; it deliberately does not")
	}
	if h.fn(0, 0.5, 0) != h.fn(1, 0.5, 0) {
		t.Error("height palette varies with band; it should vary with cell height")
	}
	if h.fn(0.5, 0.1, 0) == h.fn(0.5, 0.9, 0) {
		t.Error("height palette does not respond to the cell value at all")
	}
}

func paletteByName(name string) palette {
	for _, p := range palettes {
		if p.name == name {
			return p
		}
	}
	return palette{}
}

func TestHsvToRGBEndpoints(t *testing.T) {
	// Zero saturation is a grey regardless of hue, which is the property the
	// one-pixel colour path relies on when it has nothing to say with colour.
	for _, h := range []float64{0, 0.25, 0.5, 0.75, 1.0} {
		r, g, b := hsvToRGB(h, 0, 0.5)
		if math.Abs(r-g) > 1e-9 || math.Abs(g-b) > 1e-9 {
			t.Errorf("hsvToRGB(%v, 0, 0.5) = (%v,%v,%v), want a grey", h, r, g, b)
		}
	}
	// Full saturation, full value at hue 0 is red.
	r, g, b := hsvToRGB(0, 1, 1)
	if math.Abs(r-1) > 1e-9 || math.Abs(g) > 1e-9 || math.Abs(b) > 1e-9 {
		t.Errorf("hsvToRGB(0,1,1) = (%v,%v,%v), want (1,0,0)", r, g, b)
	}
}

func TestRGBClampsComponents(t *testing.T) {
	// Out-of-range components must not wrap. A wrapped byte produces a colour
	// nobody chose, and there is nothing to catch it downstream.
	got := rgb(2, -1, 0.5)
	if got>>16 != 0xff {
		t.Errorf("red overflowed: %06x", got)
	}
	if (got>>8)&0xff != 0 {
		t.Errorf("green underflowed: %06x", got)
	}
}

func TestPaletteAtClampsIndex(t *testing.T) {
	if paletteAt(-1).name != palettes[0].name {
		t.Error("paletteAt(-1) should be the first palette")
	}
	if paletteAt(999).name != palettes[0].name {
		t.Error("paletteAt(999) should be the first palette, not a panic")
	}
}

// TestDefaultPaletteIsNotMono is the regression test for a black screen.
//
// The palette list originally led with the mono entry, so every cell resolved to
// colour 0 and colour mode drew nothing at all: no error, working transport,
// working HUD, an empty grid. Black-on-black is not a failure any assertion about
// bytes would have caught, which is why this is pinned here.
func TestDefaultPaletteIsNotMono(t *testing.T) {
	if palettes[0].mono {
		t.Errorf("palette 0 is %q, which draws no colour", palettes[0].name)
	}
	if palettes[0].fn == nil {
		t.Error("the default palette produces no colour at all")
	}
	// And it must actually produce ink for a mid-range cell.
	if got := palettes[0].fn(0.5, 0.5, 0); got == 0 {
		t.Error("the default palette returns black for a mid-range cell")
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
func TestStylesOnlyDrawFromTheirOwnInput(t *testing.T) {
	bandsOnly := &AudioFrame{Bands: rampBands(bands, 1.0), Beat: 1.0}
	for _, mk := range vizRegistry {
		v := mk()
		v.Resize(60, 16)
		v.Push(bandsOnly)
		g := NewVizGrid(60, 16, false, 1)
		g.Clear()
		v.Paint(g)
		if v.Name() == "scope" {
			if cellsAboveBg(g) != 0 {
				t.Error("scope drew something from bands alone; it must use the waveform")
			}
			continue
		}
		if cellsAboveBg(g) == 0 {
			t.Errorf("%s drew nothing from bands alone", v.Name())
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

func paletteNames() []string {
	out := make([]string, 0, len(palettes))
	for _, p := range palettes {
		out = append(out, p.name)
	}
	return out
}

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
func TestHeavyStylesAreMarkedAndCapped(t *testing.T) {
	heavy := 0
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
		if v.Heavy() {
			heavy++
		}
	}
	if heavy != 2 {
		t.Errorf("%d heavy styles, want 2 (radial and particles)", heavy)
	}
}

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
func TestEveryRegistryStyleIsInThePlan(t *testing.T) {
	if got := len(vizRegistry); got != 6 {
		t.Errorf("%d styles registered, want 6", got)
	}
}

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
func TestMiniBarsIsExactlyWidth(t *testing.T) {
	for _, width := range []int{1, 5, 20, 80, 200} {
		for _, n := range []int{0, 1, 48, 200} {
			b := make([]float64, n)
			for i := range b {
				b[i] = float64(i) / float64(maxInt(n, 1))
			}
			got := miniBars(b, width, nil)
			if len([]rune(got)) != width {
				t.Errorf("miniBars(%d bands, width %d) is %d cells, want %d",
					n, width, len([]rune(got)), width)
			}
		}
	}
}

func TestMiniBarsEmptySourceIsBlankNotPanicking(t *testing.T) {
	got := miniBars(nil, 20, nil)
	if len(got) != 20 {
		t.Fatalf("length %d, want 20", len(got))
	}
	if strings.TrimSpace(got) != "" {
		t.Errorf("no bands gave %q, want blanks", got)
	}
}

func TestMiniBarsIsMonotonic(t *testing.T) {
	// Quiet must lay down less ink than loud, or the strip is noise.
	quiet := []float64{0.05, 0.05, 0.05, 0.05}
	loud := []float64{0.95, 0.95, 0.95, 0.95}
	q := miniBars(quiet, 4, nil)
	l := miniBars(loud, 4, nil)
	for i := range q {
		if q[i] >= l[i] {
			t.Errorf("cell %d: quiet %q is not lighter than loud %q", i, rune(q[i]), rune(l[i]))
		}
	}
}

// TestHudStripAddsARow pins the layout coupling.
//
// Non-empty strip means four rows, and empty means three. The render loop indexes
// the returned lines and writes them below the grid, so a count that did not match
// the layout's chrome budget would either overwrite the video or leave a stale row.
func TestHudStripAddsARow(t *testing.T) {
	base := hud{title: "t", pos: 10, dur: 100, queue: 1, total: 3}
	if got := len(base.lines(80)); got != hudStripRows {
		t.Errorf("without a strip: %d rows, want %d", got, hudStripRows)
	}
	withStrip := base
	withStrip.strip = strings.Repeat("=", 80)
	if got := len(withStrip.lines(80)); got != hudStripRows+1 {
		t.Errorf("with a strip: %d rows, want %d", got, hudStripRows+1)
	}
}

// TestChromeRowsForMatchesTheHud is the coupling that would otherwise be a pair of
// constants drifting apart.
func TestChromeRowsForMatchesTheHud(t *testing.T) {
	if chromeRowsFor(false) != hudStripRows {
		t.Errorf("chromeRowsFor(false) = %d, want %d", chromeRowsFor(false), hudStripRows)
	}
	if chromeRowsFor(true) != hudStripRows+1 {
		t.Errorf("chromeRowsFor(true) = %d, want %d", chromeRowsFor(true), hudStripRows+1)
	}
}

// TestHudStatusOutranksTheFooter pins the priority order.
//
// A status line is the answer to a keypress the user just made, and the four
// seconds it lives for are when they are still looking for it.
func TestHudStatusOutranksTheFooter(t *testing.T) {
	h := hud{paused: true, volume: 50, showHints: true, status: "viz waterfall"}
	if got := h.footer(); !strings.Contains(got, "viz waterfall") {
		t.Errorf("footer = %q, want the status line to win", got)
	}
	h.status = ""
	if got := h.footer(); !strings.Contains(got, "PAUSED") {
		t.Errorf("footer = %q, want the pause banner once the status expires", got)
	}
}

// TestStripCostsTheVideoAGridRow is the whole argument for putting the strip in the
// chrome rather than over the grid.
//
// If the strip were free the video would keep all its rows and there would be no
// reason for the layout to care. It is not free: it takes a row, and that is what
// makes the trade visible.
func TestStripCostsTheVideoAGridRow(t *testing.T) {
	plain := computeLayout(80, 24, 0, 0, chromeRowsFor(false))
	withStrip := computeLayout(80, 24, 0, 0, chromeRowsFor(true))
	if withStrip.rows >= plain.rows {
		t.Errorf("strip did not cost a row: plain %d, with strip %d", plain.rows, withStrip.rows)
	}
	if withStrip.cols != plain.cols {
		t.Errorf("strip changed the width: plain %d, with strip %d", plain.cols, withStrip.cols)
	}
}

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
func TestPickAudioPrefersAStreamWithNoVideo(t *testing.T) {
	formats := []ytFormat{
		// A progressive format with the highest bitrate, which is exactly what a
		// bitrate-sorted preference would pick.
		{FormatID: "18", URL: "https://progressive", TBR: 2000, VCodec: "avc1", ACodec: "mp4a"},
		// An audio-only DASH format at a much lower bitrate.
		{FormatID: "140", URL: "https://audio-only", TBR: 130, VCodec: "none", ACodec: "mp4a"},
	}
	got, err := pickAudio(formats)
	if err != nil {
		t.Fatalf("pickAudio: %v", err)
	}
	if got != "https://audio-only" {
		t.Errorf("pickAudio chose %q, want the audio-only stream", got)
	}
}

func TestPickAudioFallsBackToProgressive(t *testing.T) {
	// Some uploads only have progressive. Refusing would mean the track is
	// unplayable in music mode, which is worse than decoding video we then discard.
	formats := []ytFormat{
		{FormatID: "18", URL: "https://progressive", TBR: 800, VCodec: "avc1", ACodec: "mp4a"},
	}
	got, err := pickAudio(formats)
	if err != nil {
		t.Fatalf("pickAudio: %v", err)
	}
	if got != "https://progressive" {
		t.Errorf("pickAudio = %q, want the progressive fallback", got)
	}
}

func TestPickAudioSkipsM3u8(t *testing.T) {
	// m3u8 has to be re-resolved by ffmpeg, which loses control of the clock and
	// makes the tap's pacing a guess.
	formats := []ytFormat{
		{FormatID: "hls", URL: "https://m3u8", TBR: 900, VCodec: "none", ACodec: "mp4a", Protocol: "m3u8"},
	}
	if _, err := pickAudio(formats); err == nil {
		t.Error("pickAudio accepted an m3u8 format")
	}
}

func TestPickAudioRejectsVideoOnly(t *testing.T) {
	formats := []ytFormat{
		{FormatID: "137", URL: "https://video", TBR: 4000, VCodec: "avc1", ACodec: "none"},
	}
	if _, err := pickAudio(formats); err == nil {
		t.Error("pickAudio accepted a video-only format as audio")
	}
}

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
func rampBands(n int, scale float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		t := float64(i) / float64(maxInt(n-1, 1))
		out[i] = clamp01((0.3 + 0.7*math.Sin(t*math.Pi)) * scale)
	}
	return out
}

func ones(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 1
	}
	return out
}

// rampWave is a deterministic waveform, so a scope test is reproducible rather
// than dependent on a random source.
func rampWave(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.Sin(float64(i) * 0.05)
	}
	return out
}

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
