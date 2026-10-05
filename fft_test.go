package main

import (
	"encoding/binary"
	"math"
	"testing"
)

func TestFFTIsPowerOfTwoSized(t *testing.T) {
	for _, n := range []int{0, 1, 3, 100} {
		f := NewFFT(n)
		size := len(f.rev)
		if size&(size-1) != 0 {
			t.Errorf("NewFFT(%d) gave non-power-of-two size %d", n, size)
		}
	}
}

// A pure tone must put its energy in the matching FFT bin. This is the test
// that proves the hand-rolled FFT is actually correct and not just fast.

// A pure tone must put its energy in the matching FFT bin. This is the test
// that proves the hand-rolled FFT is actually correct and not just fast.
func TestFFTFindsPureTone(t *testing.T) {
	const rate = 11025
	const toneBin = 64 // 64 * rate/fftSize = 689 Hz
	f := NewFFT(fftSize)
	re := make([]float64, fftSize)
	im := make([]float64, fftSize)
	for i := 0; i < fftSize; i++ {
		ang := 2 * math.Pi * float64(toneBin) * float64(i) / float64(fftSize)
		re[i] = math.Cos(ang)
	}
	f.Forward(re, im)

	mag := func(bin int) float64 {
		return math.Hypot(re[bin], im[bin])
	}
	peak := mag(toneBin)
	if peak < 1e-6 {
		t.Fatalf("no energy at expected bin %d (mag=%g)", toneBin, peak)
	}
	// the peak must dominate its neighbours, or the transform is wrong
	for _, nb := range []int{toneBin - 8, toneBin - 2, toneBin + 2, toneBin + 8} {
		if mag(nb) > peak*0.05 {
			t.Errorf("leak into bin %d: %g vs peak %g", nb, mag(nb), peak)
		}
	}
	// DC and the far end should be near zero for a mid-band tone
	if mag(1) > peak*0.05 {
		t.Errorf("unexpected DC energy: %g", mag(1))
	}
}

func TestSpectrumAnalyzerBands(t *testing.T) {
	s := NewSpectrumAnalyzer(11025, 32)
	samples := make([]float64, fftSize)
	mags := s.Analyze(samples)
	if len(mags) != 32 {
		t.Fatalf("got %d bands, want 32", len(mags))
	}
	for i, m := range mags {
		if m < 0 || m > 1 {
			t.Errorf("band %d = %v, want 0..1", i, m)
		}
	}
	// silence must not light up the meter
	sum := 0.0
	for _, m := range mags {
		sum += m
	}
	if sum > 0.5 {
		t.Errorf("silence produced energy: sum=%v", sum)
	}

	// A loud low tone must light the low end, not the high end.
	//
	// Asserted as "the loudest band is in the bottom quarter and beats the top band"
	// rather than "band 0 beats band 31", because the second version is coupled to
	// the absolute calibration. With 32 log-spaced bands from 30Hz, band 0 covers
	// 30-35Hz and an 86Hz tone is in band 6; the old assertion only passed because
	// the fixed -70..-10dB window happened to put a little window leakage in band 0
	// and nothing at all in band 31. Change the gain calibration and it broke,
	// which says the assertion was measuring the wrong thing.
	low := make([]float64, fftSize)
	for i := range low {
		ang := 2 * math.Pi * 8 * float64(i) / float64(fftSize) // ~86 Hz
		low[i] = math.Cos(ang)
	}
	// Analysed several times, not once: the display smoothing has a 0.6 attack
	// coefficient, so a single analysis of a fresh analyser can never exceed 0.6
	// however loud the input is. Letting it settle also checks that the automatic
	// gain converges rather than drifting.
	s2 := NewSpectrumAnalyzer(11025, 32)
	var m2 []float64
	for i := 0; i < 6; i++ {
		m2 = s2.Analyze(low)
	}

	peak := 0
	for i, v := range m2 {
		if v > m2[peak] {
			peak = i
		}
	}
	if m2[peak] < 0.8 {
		t.Errorf("loudest band %d only reached %v; a full-scale tone should be near "+
			"the top of the range", peak, m2[peak])
	}
	if peak > len(m2)/4 {
		t.Errorf("86Hz peaked in band %d of %d; the frequency axis is not monotonic",
			peak, len(m2))
	}
	top := m2[len(m2)-1]
	if m2[peak] <= top {
		t.Errorf("low tone did not favour the low end: peak band %d = %v, top band = %v",
			peak, m2[peak], top)
	}
}

func TestAbsFloat(t *testing.T) {
	if absFloat(-2.5) != 2.5 || absFloat(2.5) != 2.5 || absFloat(0) != 0 {
		t.Error("absFloat is wrong")
	}
}

func TestDriftThresholdIsSane(t *testing.T) {
	// Too small and ordinary jitter causes constant audible seeking; too large
	// and the desync becomes obvious.
	if driftCorrectThreshold < 0.1 || driftCorrectThreshold > 0.5 {
		t.Errorf("driftCorrectThreshold = %v, want between 0.1 and 0.5 seconds", driftCorrectThreshold)
	}
}

func TestRMSLevel(t *testing.T) {
	if got := rmsLevel(nil); got != 0 {
		t.Errorf("empty pcm = %v, want 0", got)
	}
	if got := rmsLevel([]byte{1}); got != 0 {
		t.Errorf("odd byte pcm = %v, want 0", got)
	}
	// silence
	silence := make([]byte, 400)
	if got := rmsLevel(silence); got != 0 {
		t.Errorf("silence = %v, want 0", got)
	}
	// full-scale square-ish signal should map near the top
	loud := make([]byte, 400)
	for i := 0; i < len(loud); i += 2 {
		binary.LittleEndian.PutUint16(loud[i:], uint16(int16(30000)))
	}
	if got := rmsLevel(loud); got < 0.8 {
		t.Errorf("loud signal = %v, want > 0.8", got)
	}
	// a mid-level tone must land strictly between silence and full scale,
	// which is the whole reason for the dB mapping
	mid := make([]byte, 400)
	for i := 0; i < len(mid); i += 2 {
		binary.LittleEndian.PutUint16(mid[i:], uint16(int16(3000)))
	}
	got := rmsLevel(mid)
	if got <= 0 || got >= 1 {
		t.Errorf("mid signal = %v, want strictly between 0 and 1", got)
	}
}
