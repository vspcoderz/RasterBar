package main

import (
	"math"
	"testing"
)

// Tests for the automatic gain: the high-water mark, its fall, and the absolute
// floor under it.
//
// This file exists because two bugs shipped together and neither failed loudly.
// They were downstream of each other, so every symptom they produced looked like a
// symptom of the other, and both were only findable by measuring the gain against
// known inputs rather than by looking at the spectrum.
//
//   - The fall was written as `peakDb > -infDb`, i.e. > +140. peakDb reaches about
//     +42 at full scale, so the condition was never true and the subtraction never
//     ran. The gain's high-water mark was a pure all-time maximum. The comment on
//     agcFallDb -- "a note holds its place for roughly three seconds" -- described
//     code that did nothing at all, and nothing caught it because a gain that fails
//     to fall produces no error, only a picture that stops adapting.
//
//   - The floor was unbounded below, so a quiet passage normalised against itself.
//     A flat spectrum is the pathological case and it is not exotic: any stretch of
//     broadband noise has one. peakDb tracks it, the floor lands 42dB under it,
//     and all 48 bands read 1.0. Measured before the gate: mean 0.825, lit 48/48 --
//     a full-scale block for a signal at -120dBFS, which is inaudible.
//
// That is the "scattered bright cells" in the bug report, and it is why every
// visualizer looked wrong: the styles were never involved, they were drawing what
// they were given.

// TestAGCFallActuallyFalls is the regression for the sign error.
//
// The whole point of agcFallDb is that a gain which only ever rises is not an
// automatic gain, it is a maximum. Feeding silence must walk peakDb down from the
// loud material that raised it, because that is what hands the range back after a
// loud section so a quiet one can be seen.
//
// Asserts a *large* drop rather than a nonzero one: the real behaviour is
// 1.2dB per analysis, so 30 analyses of silence should move it about 36dB. A
// threshold of 20dB leaves room for scheduling and still cannot be satisfied by a
// gain that is stuck.
func TestAGCFallActuallyFalls(t *testing.T) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)

	loud := toneBlock(0.9)
	for i := 0; i < 12; i++ {
		an.Analyze(loud)
	}
	peak := an.peakDb
	if peak < 30 {
		t.Fatalf("full-scale material only reached peakDb %.1f; the analyser is "+
			"not calibrated the way these tests assume", peak)
	}

	const quietFrames = 30
	for i := 0; i < quietFrames; i++ {
		an.Analyze(make([]float64, fftSize))
	}

	dropped := peak - an.peakDb
	if dropped < 20 {
		t.Errorf("peakDb fell only %.1fdB over %d silent analyses (peak %.1f -> %.1f); "+
			"agcFallDb is %.1f per analysis, so this should be near %.1fdB. A gain that "+
			"does not fall is an all-time maximum and never hands the range back.",
			dropped, quietFrames, peak, an.peakDb, agcFallDb, quietFrames*agcFallDb)
	}
}

// TestAGCFallStopsAtItsFloor is the other half of the same guard.
//
// peakDb must not fall without limit, because floor is derived from it and an
// unbounded floor would walk up out of the picture's range and leave the whole
// spectrum pinned at 0 for the rest of the track. infDb is the stop.
//
// The two assertions together pin the guard from both sides, which is the point:
// `peakDb > -infDb` satisfied neither, and testing only "does it fall" would have
// let a runaway fall through.
func TestAGCFallStopsAtItsFloor(t *testing.T) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)
	an.peakDb = 50

	for i := 0; i < 4000; i++ { // ~370s of silence, far past any realistic fall
		an.Analyze(make([]float64, fftSize))
	}

	// The guard subtracts first and checks before the next one, so it settles a
	// single step past the constant. Two steps of slack covers that without
	// loosening the test into meaninglessness: an unbounded fall is off by
	// thousands of dB here.
	if floor := an.peakDb; floor < infDb-2*agcFallDb {
		t.Errorf("peakDb fell to %.1f, past infDb %.1f; the gain would walk the "+
			"display floor out of range and flatten everything to nothing",
			floor, infDb)
	}
}

// TestInaudibleNoiseDoesNotRenderAsFullScale is the headline bug.
//
// The gain normalises each frame against a peak with a span beneath it, which is
// correct for music and wrong for anything with a flat spectrum. Broadband noise
// is flat by definition, so it normalises against itself: peakDb tracks the noise,
// the floor lands 42dB below the noise, and every band reads 1.0.
//
// The signal here is -120dBFS, which is inaudible -- it is the dither floor of a
// quiet digital signal, not something anyone recorded. Before agcGateDb it lit
// 48 of 48 bands with a mean of 0.825, i.e. a solid full-height block across the
// whole spectrum while nothing could be heard. That is exactly what a terminal
// visualizer must never do, and because it lives in the analyser rather than in a
// style, it corrupted every visualizer at once.
//
// Asserted on the maximum rather than the mean so the test cannot pass on a
// partially-decayed smoothing tail: no band of an inaudible signal may read as
// anything but nothing.
func TestInaudibleNoiseDoesNotRenderAsFullScale(t *testing.T) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)

	sig := noiseBlock(1e-6) // -120dBFS
	var mags []float64
	for i := 0; i < 60; i++ {
		mags = an.Analyze(sig)
	}

	var max, sum float64
	lit := 0
	for _, v := range mags {
		if v > max {
			max = v
		}
		sum += v
		if v > 0.05 {
			lit++
		}
	}
	mean := sum / float64(len(mags))

	if max > 0.10 || lit > 0 {
		t.Errorf("a -120dBFS noise floor rendered as a spectrum: max band %.3f, "+
			"%d of %d bands above 0.05, mean %.3f. Before agcGateDb this measured "+
			"max 1.000 / 48 lit / mean 0.825 -- a full-scale block for an inaudible "+
			"signal.", max, lit, len(mags), mean)
	}
}

// TestTheNoiseGateStillLetsQuietSignalsThrough is the guard against over-fixing.
//
// agcGateDb is a hard absolute floor, and a hard floor on a spectrum is one bad
// constant away from hiding material someone deliberately recorded quietly. A
// gate set from the noise side alone would clamp at -90 and be defensible; one
// set too high would silence real content and be worse than the bug.
//
// Measured anchors, both of which the test depends on: a -100dBFS tone lands at
// -57.6dB in the band it occupies and must stay visible, while -120dBFS broadband
// noise lands at -92.7dB per band and must not. The gap between them is about
// 35dB, and -80 sits in it with 13dB to spare on the noise side. Asserting a
// clear band above 0.3 rather than merely nonzero keeps the test honest if the
// gate is ever moved: it has to be *visible*, not just present.
func TestTheNoiseGateStillLetsQuietSignalsThrough(t *testing.T) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)

	sig := toneBlock(1e-5) // -100dBFS, quiet but deliberately so
	var mags []float64
	for i := 0; i < 60; i++ {
		mags = an.Analyze(sig)
	}

	var max float64
	for _, v := range mags {
		if v > max {
			max = v
		}
	}
	if max < 0.30 {
		t.Errorf("a -100dBFS tone rendered at max %.3f; agcGateDb %.1f is set high "+
			"enough to hide quiet recordings, which trades the reported bug for a "+
			"worse one", max, agcGateDb)
	}
}

// TestFullScaleSpectrumIsUnchangedByTheGate is the control.
//
// The gate must be inert on material it has no business touching, which is the
// common case: any track loud enough for the gain's own floor to sit above the
// gate. If this ever fails, the gate is no longer a floor on the bottom of the
// picture and has started clipping the top of it.
//
// Also checks the shape, not just the peak. A single 440Hz tone must light its
// own bands and leave the rest of the spectrum alone; a gate that quietly
// compressed the span would show up here as the wrong number of lit bands long
// before it showed up as a wrong-looking picture.
func TestFullScaleSpectrumIsUnchangedByTheGate(t *testing.T) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)

	sig := toneBlock(0.9)
	var mags []float64
	for i := 0; i < 12; i++ {
		mags = an.Analyze(sig)
	}

	var max float64
	lit := 0
	for _, v := range mags {
		if v > max {
			max = v
		}
		if v > 0.05 {
			lit++
		}
	}

	if max < 0.90 {
		t.Errorf("full-scale tone peaks at %.3f, want ~1.0; agcGateDb %.1f has "+
			"raised the floor into the signal", max, agcGateDb)
	}
	// A 440Hz tone at this calibration occupies a couple of bands out of 48. Ten
	// would mean the gate or the span had smeared it.
	if lit > 10 {
		t.Errorf("%d of 48 bands lit for a single tone; the gain has flattened the "+
			"spectrum rather than scaled it", lit)
	}
}

// TestGainRecoversAfterALoudSectionIs the transition the fixed fall exists for.
//
// The failure it replaces: peakDb frozen at a track's loudest moment meant the
// floor was pinned there too, so everything quieter than that peak by more than
// agcSpanDb drew as nothing. A quiet bridge after a loud chorus went black.
//
// The bar is deliberately loose about how fast. agcFallDb is 1.2dB per analysis
// and BlockDur is ~93ms, so 40 analyses is about 45dB of fall -- comfortably more
// than agcSpanDb -- and any quiet material within reach of the restored floor has
// to come back. What matters is that it comes back at all.
func TestGainRecoversAfterALoudSection(t *testing.T) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)

	for i := 0; i < 12; i++ {
		an.Analyze(toneBlock(0.9))
	}

	// A quiet but structurally identical section: same tone, 40dB down.
	quiet := toneBlock(9e-3)
	// Let the peak fall first, as it would across a real decrescendo.
	for i := 0; i < 40; i++ {
		an.Analyze(make([]float64, fftSize))
	}
	var mags []float64
	for i := 0; i < 6; i++ {
		mags = an.Analyze(quiet)
	}

	var max float64
	for _, v := range mags {
		if v > max {
			max = v
		}
	}
	if max < 0.25 {
		t.Errorf("a section 40dB below the track's peak rendered at max %.3f after "+
			"40 silent analyses; the gain never handed the range back. This is the "+
			"state the -infDb sign error left it permanently in.", max)
	}
}

// noiseBlock is deterministic broadband noise, so these tests measure the gain and
// not a random seed.
//
// Deterministic on purpose and not merely convenient: a flaky threshold test on
// random noise would be indistinguishable from a real regression the first time
// someone ran it, and these assertions are exactly the kind that get re-run until
// they pass.
func noiseBlock(amp float64) []float64 {
	s := make([]float64, fftSize)
	x := uint64(0x2545F4914F6CDD1D)
	for i := range s {
		x ^= x << 13
		x ^= x >> 7
		x ^= x << 17
		s[i] = amp * (float64(x%2001)/1000 - 1)
	}
	return s
}

// toneBlock is one window of a full-scale sine at 440Hz.
func toneBlock(amp float64) []float64 {
	s := make([]float64, fftSize)
	for i := range s {
		s[i] = amp * math.Sin(2*math.Pi*440*float64(i)/spectrumRate)
	}
	return s
}
