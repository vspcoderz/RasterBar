package main

import (
	"math"
	"testing"
)

// The noise gate exists because broadband noise drew as a solid block: eight
// seconds of -74dBFS flat spectrum lit 48 of 48 bands with a mean of 0.88, while
// real music at the same kind of moment lit 8. Level could not separate them and
// neither could shape alone -- see noiseLevelDb for both sets of measurements.
//
// What the gate owes is therefore two-sided, and both sides are asserted here
// because each earlier attempt satisfied exactly one of them. The first version
// hid the noise and dimmed real music by up to 99%, which no test caught because
// none of them ran real music. The second hid the music instead: measuring shape
// after normalisation inverted the gate, so quieter noise passed as less flat and
// drew brighter than louder noise.

// whiteNoise is deterministic broadband noise. Seed fixed so a failure reproduces.
func whiteNoise(amp float64, n int) []float64 {
	s := make([]float64, n)
	seed := uint64(0x9E3779B97F4A7C15)
	for i := range s {
		seed = seed*6364136223846793005 + 1442695040888963407
		s[i] = amp * (float64(seed>>11)/float64(int64(1<<53))*2 - 1)
	}
	return s
}

// toneMix is harmonically structured content -- what a note looks like, with all
// its energy in a few bands and the rest empty.
func toneMix(amp float64, n int) []float64 {
	s := make([]float64, n)
	for i := range s {
		t := float64(i) / float64(spectrumRate)
		s[i] = amp * (0.6*math.Sin(2*math.Pi*220*t) +
			0.3*math.Sin(2*math.Pi*440*t) +
			0.15*math.Sin(2*math.Pi*660*t))
	}
	return s
}

// settle runs a signal long enough for both the gain and the smoother to reach
// steady state, then reports what is actually on screen.
func settle(s []float64, frames int) (lit int, max float64) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)
	var mags []float64
	off := 0
	for i := 0; i < frames; i++ {
		if off+fftSize > len(s) {
			off = 0
		}
		mags = an.Analyze(s[off : off+fftSize])
		off += fftSize
	}
	for _, v := range mags {
		if v > 0.05 {
			lit++
		}
		if v > max {
			max = v
		}
	}
	return
}

// A quiet flat floor is the whole failure: it must draw nothing.
//
// Asserted on the mean rather than on one frame, because the smoother releases at
// 0.18 per analysis and a single gated frame stays visible for about 15 frames
// after. Checking the last frame therefore measures the decay tail, not the gate,
// and would fail on a correct implementation whenever a threshold crossing landed
// late in the run. The mean is what the eye integrates anyway.
func TestNoiseGateHidesBroadbandNoise(t *testing.T) {
	for _, dbfs := range []float64{-74, -80, -95} {
		amp := math.Pow(10, dbfs/20) * math.Sqrt2
		s := whiteNoise(amp, fftSize*80)
		an := NewSpectrumAnalyzer(spectrumRate, bands)

		var gated, frames int
		var sum float64 // mean band value over the tail, where the state is settled
		off := 0
		for i := 0; i < 60; i++ {
			if off+fftSize > len(s) {
				off = 0
			}
			mags := an.Analyze(s[off : off+fftSize])
			off += fftSize
			if an.noiseGain() == 0 {
				gated++
			}
			if i < 30 {
				continue
			}
			frames++
			for _, v := range mags {
				sum += v
			}
		}
		mean := sum / float64(frames*len(an.mags))

		if gated < 50 {
			t.Errorf("%v dBFS broadband noise: only %d/60 frames gated. Before this "+
				"gate the same signal drew a mean of 0.738 across all 48 bands, i.e. a "+
				"solid block", dbfs, gated)
		}
		// 0.15 is roughly where the pre-gate measurement (0.738) and a genuinely dark
		// panel (0.00) stop overlapping; it allows residue from threshold crossings
		// without permitting the block to come back.
		if mean > 0.15 {
			t.Errorf("%v dBFS broadband noise drew a mean band level of %.3f; want "+
				"<= 0.15. This is the case the gate exists for", dbfs, mean)
		}
	}
}

// The symmetric failure: quiet but structured content is a signal someone chose to
// record quietly, and it has to survive. A pure tone is the sharpest possible
// contrast, so if anything passes, it does.
func TestNoiseGateKeepsQuietStructuredContent(t *testing.T) {
	for _, dbfs := range []float64{-55, -65, -75} {
		amp := math.Pow(10, dbfs/20) * math.Sqrt2
		lit, max := settle(toneMix(amp, fftSize*80), 60)
		if max < 0.4 {
			t.Errorf("%v dBFS tone reached max %.3f with %d/48 bands lit: a quiet but "+
				"structured signal must stay visible, and the shape test must not treat "+
				"contrast as flatness", dbfs, max, lit)
		}
		if lit == 0 {
			t.Errorf("%v dBFS tone lit nothing", dbfs)
		}
	}
}

// Loud content is never gated regardless of shape. Music's flattest frames are
// loud, which is the whole reason the two conditions are ANDed.
func TestNoiseGateLeavesLoudContentAlone(t *testing.T) {
	lit, max := settle(toneMix(0.5, fftSize*80), 60)
	if max < 0.8 || lit < 4 {
		t.Errorf("loud content reached max %.3f with %d/48 bands lit", max, lit)
	}
}

// Loud broadband noise is audible, so it draws. Gating it would be hiding a signal
// the listener can hear, which is a different claim from hiding an inaudible floor.
func TestNoiseGateDoesNotHideAudibleNoise(t *testing.T) {
	amp := math.Pow(10, -30.0/20) * math.Sqrt2
	_, max := settle(whiteNoise(amp, fftSize*80), 60)
	if max < 0.5 {
		t.Errorf("loud noise reached max %.3f; noiseLevelDb %v is above where it "+
			"should be, gating material that is plainly audible", max, noiseLevelDb)
	}
}

// The rule itself, isolated from the analyser: the two conditions are ANDed, and
// each one on its own is insufficient. Pinning this directly is what lets the
// thresholds be changed without needing three minutes of audio to notice they broke.
func TestNoiseGainRequiresBothConditions(t *testing.T) {
	cases := []struct {
		name      string
		stdDb     float64
		levelDbfs float64
		want      float64
		why       string
	}{
		{"quiet and flat", flatStdLo - 1, noiseLevelDb - 20, 0, "noise: this is the failure case"},
		{"quiet and structured", flatStdHi + 1, noiseLevelDb - 20, 1, "a quiet note is still a note"},
		{"loud and flat", flatStdLo - 1, noiseLevelDb + 20, 1, "loud frames are never gated"},
		{"loud and structured", flatStdHi + 1, noiseLevelDb + 20, 1, "music at its loudest"},
		{"quiet, mid ramp", (flatStdLo + flatStdHi) / 2, noiseLevelDb - 20,
			0.5, "the ramp must fade rather than step"},
	}
	for _, c := range cases {
		s := &SpectrumAnalyzer{stdDb: c.stdDb, levelDbfs: c.levelDbfs}
		got := s.noiseGain()
		if math.Abs(got-c.want) > 1e-9 {
			t.Errorf("%s: noiseGain() = %v, want %v (%s)", c.name, got, c.want, c.why)
		}
	}
}

// The ramp is what keeps material at the boundary from strobing at 93ms intervals.
func TestNoiseGainRampsMonotonically(t *testing.T) {
	prev := -1.0
	for std := flatStdLo - 1; std <= flatStdHi+1; std += 0.05 {
		g := (&SpectrumAnalyzer{stdDb: std, levelDbfs: noiseLevelDb - 5}).noiseGain()
		if g < prev-1e-12 {
			t.Fatalf("gain went backwards at stdDb %.2f: %.3f -> %.3f", std, prev, g)
		}
		if g < -1e-12 || g > 1+1e-12 {
			t.Fatalf("gain %.3f outside [0,1] at stdDb %.2f", g, std)
		}
		prev = g
	}
}

// Shape has to be measured on the raw band levels. After normalisation the floor
// clamps most bands of a quiet frame to exactly zero, which manufactures contrast
// out of nothing and inverts the result: -98dBFS noise then reads as less flat
// than -78dBFS noise and draws brighter. That is not a theoretical risk, it is
// what the first version did.
func TestShapeIsMeasuredBeforeTheClamp(t *testing.T) {
	loud := NewSpectrumAnalyzer(spectrumRate, bands)
	quiet := NewSpectrumAnalyzer(spectrumRate, bands)
	la := math.Pow(10, -70.0/20) * math.Sqrt2
	qa := math.Pow(10, -95.0/20) * math.Sqrt2
	for i := 0; i < 60; i++ {
		loud.Analyze(whiteNoise(la, fftSize))
		quiet.Analyze(whiteNoise(qa, fftSize))
	}
	// Same spectrum at two levels, so its spread must be the same. Only the level
	// differs -- if stdDb tracks the level instead, the measurement is running
	// somewhere it must not be running.
	if math.Abs(loud.stdDb-quiet.stdDb) > 1.5 {
		t.Errorf("the same spectrum measured %.2fdB apart at two levels (%.2f vs %.2f): "+
			"stdDb is picking up the floor, which is the clamp and not the signal",
			math.Abs(loud.stdDb-quiet.stdDb), loud.stdDb, quiet.stdDb)
	}
}

// Band levels are unnormalised FFT magnitudes, not dBFS -- a full-scale tone reads
// about +42. Anything stating a threshold in dBFS must therefore get the level off
// the samples.
func TestLevelIsMeasuredInDBFSOffTheSamples(t *testing.T) {
	an := NewSpectrumAnalyzer(spectrumRate, bands)
	amp := math.Pow(10, -40.0/20) * math.Sqrt2 // -40dBFS RMS
	s := toneMix(amp, fftSize)
	// toneMix's peak sum is ~1.05, so rescale to the requested RMS exactly.
	var acc float64
	for _, v := range s {
		acc += v * v
	}
	want := 20 * math.Log10(math.Sqrt(acc/float64(len(s))))
	an.Analyze(s)
	if diff := math.Abs(an.levelDbfs - want); diff > 0.5 {
		t.Errorf("levelDbfs = %.2f, want %.2f: it must be the window RMS in dBFS, "+
			"not a band level", an.levelDbfs, want)
	}
	// Halving the amplitude must cost 6.02dB. Band levels are unnormalised FFT
	// magnitudes and would still scale correctly here, so this pairs with the check
	// above rather than replacing it: the offset is what the band levels get wrong.
	quiet := NewSpectrumAnalyzer(spectrumRate, bands)
	quiet.Analyze(toneMix(amp/2, fftSize))
	if d := an.levelDbfs - quiet.levelDbfs; math.Abs(d-6.02) > 0.05 {
		t.Errorf("halving amplitude changed levelDbfs by %.3fdB, want 6.02", d)
	}
}

// Digital silence must be dark and must not produce non-finite values anywhere.
func TestSilenceIsDarkAndFinite(t *testing.T) {
	lit, max := settle(make([]float64, fftSize*40), 40)
	if lit != 0 || max != 0 {
		t.Errorf("silence lit %d bands, max %.3f", lit, max)
	}
	an := NewSpectrumAnalyzer(spectrumRate, bands)
	for i := 0; i < 20; i++ {
		mags := an.Analyze(make([]float64, fftSize))
		if math.IsNaN(an.levelDbfs) || math.IsInf(an.levelDbfs, 0) {
			t.Fatalf("levelDbfs = %v on silence", an.levelDbfs)
		}
		for b, v := range mags {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Fatalf("band %d = %v on silence", b, v)
			}
		}
	}
}
