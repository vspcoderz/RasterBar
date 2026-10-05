package main

import (
	"math"
	"testing"
)

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
