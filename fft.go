package main

import "math"

// Hand-rolled radix-2 Cooley-Tukey FFT. Deliberately no dependency: this
// program's whole premise is a zero-dependency static binary (see PLAN.md), and
// a spectrum analyser is the one piece that would otherwise have pulled in
// gonum or a DSP package for ~80 lines of arithmetic.
//
// Verified against a synthetic sine wave in the tests: energy lands in the
// expected bin and neighbours stay near zero.

const (
	fftSize    = 1024 // power of two; ~93ms window at spectrumRate
	spectrumHz = 11025
	bands      = 48
)

// agcSpanDb is how many dB of dynamic range each band is shown across.
//
// 42dB is a little less than the usual 60dB of a spectrum analyser, because the top
// of a band is rarely occupied and showing all 60 leaves the picture squashed into
// the bottom two thirds of every bar.
const agcSpanDb = 42.0

// agcFallDb is how fast a band's tracked peak falls, in dB per analysis.
//
// 1.2dB per analysis is about 13dB per second at spectrumRate, so a note holds its
// place for roughly three seconds and then hands the top of the range back. Faster
// and the picture flickers with the treble; slower and a decaying bass never comes
// back down.
const agcFallDb = 1.2

// infDb is the floor for the gain's high-water mark before any signal arrives.
//
// -140 rather than zero: a peak of 0dB would mean "already as loud as possible",
// pinning the whole spectrum at full scale until it had fallen by 140dB, which at
// agcFallDb is over two minutes of solid block.
const infDb = -140.0

// agcGateDb is the absolute level below which nothing is drawn, no matter what the
// gain is doing.
//
// The gain is a high-water mark with a span under it, which is correct for music
// and catastrophic without this. Band levels are logged as dB relative to the FFT
// as computed, so a full-scale tone sits around +42dB and digital silence at
// -240dB. A quiet passage of real music has a *flat* spectrum, and a flat spectrum
// normalises against itself: peakDb tracks the noise, the floor lands 42dB below
// the noise, and every band therefore reads 1.0. Measured before this existed: a
// -120dBFS dither tone lit 48 of 48 bands with a mean of 0.825, i.e. a full-scale
// block where the signal was inaudible.
//
// -80dBFS was picked by measurement rather than taste. The per-band RMS of
// -120dBFS broadband noise came out at -92.7dB, and that has to be under the gate;
// a -100dBFS tone came out at -57.6dB, and that has to stay visible because it is
// a signal someone chose to record quietly. -80 sits between the two with about
// 13dB of margin on the noise side.
//
// Only the floor is gated, never the peak, so the shape of anything above the gate
// is untouched: a gate on the peak would clip a quiet passage flat, and a gate on
// the floor merely decides where the bottom of the picture is.
const agcGateDb = -80.0

type FFT struct {
	cos, sin []float64
	rev      []int
	re, im   []float64
}

func NewFFT(n int) *FFT {
	if n <= 0 || n&(n-1) != 0 {
		n = 1
	}
	f := &FFT{
		cos: make([]float64, n/2),
		sin: make([]float64, n/2),
		rev: make([]int, n),
		re:  make([]float64, n),
		im:  make([]float64, n),
	}
	for i := 0; i < n/2; i++ {
		angle := 2 * math.Pi * float64(i) / float64(n)
		f.cos[i] = math.Cos(angle)
		f.sin[i] = math.Sin(angle)
	}
	// bit-reversal permutation table, computed once
	bits := 0
	for (1 << bits) < n {
		bits++
	}
	for i := 0; i < n; i++ {
		r := 0
		for b := 0; b < bits; b++ {
			if i&(1<<b) != 0 {
				r |= 1 << (bits - 1 - b)
			}
		}
		f.rev[i] = r
	}
	return f
}

// hann window, computed once. Reduces spectral leakage so a single tone does
// not smear across neighbouring bands.
var hann = func() []float64 {
	w := make([]float64, fftSize)
	for i := range w {
		w[i] = 0.5 * (1 - math.Cos(2*math.Pi*float64(i)/float64(fftSize-1)))
	}
	return w
}()

// Forward transforms re/im in place. Input length must be len(f.rev).
func (f *FFT) Forward(re, im []float64) {
	n := len(re)
	if n != len(f.rev) {
		return
	}
	for i := 0; i < n; i++ {
		j := f.rev[i]
		if i < j {
			re[i], re[j] = re[j], re[i]
			im[i], im[j] = im[j], im[i]
		}
	}
	for size := 2; size <= n; size <<= 1 {
		half := size / 2
		step := n / size
		for i := 0; i < n; i += size {
			k := 0
			for j := i; j < i+half; j++ {
				c, s := f.cos[k], f.sin[k]
				tre := re[j+half]*c + im[j+half]*s
				tim := -re[j+half]*s + im[j+half]*c
				re[j+half] = re[j] - tre
				im[j+half] = im[j] - tim
				re[j] += tre
				im[j] += tim
				k += step
			}
		}
	}
}

// SpectrumAnalyzer turns PCM blocks into log-spaced band magnitudes.
type SpectrumAnalyzer struct {
	fft    *FFT
	re, im []float64
	edges  []int // bin index per band edge
	mags   []float64
	raw    []float64 // pre-smoothing magnitudes; see Raw
	smooth []float64
	// peakDb is the high-water mark for the automatic gain, in dB. One value for
	// the whole spectrum, not one per band; see Analyze.
	peakDb float64
	rate   int
	bands  int
	window []float64
}

func NewSpectrumAnalyzer(rate, nBands int) *SpectrumAnalyzer {
	if nBands <= 0 {
		nBands = bands
	}
	if rate <= 0 {
		rate = spectrumHz
	}
	s := &SpectrumAnalyzer{
		peakDb: infDb,
		fft:    NewFFT(fftSize),
		re:     make([]float64, fftSize),
		im:     make([]float64, fftSize),
		mags:   make([]float64, nBands),
		raw:    make([]float64, nBands),
		smooth: make([]float64, nBands),
		edges:  make([]int, nBands+1),
		rate:   rate,
		bands:  nBands,
		window: hann,
	}
	// Log-spaced edges from 30Hz to Nyquist. Linear spacing would put
	// everything interesting (bass and mid) in the first two bars.
	nyquist := float64(rate) / 2
	const loHz = 30.0
	for i := 0; i <= nBands; i++ {
		frac := float64(i) / float64(nBands)
		hz := loHz * pow(nyquist/loHz, frac)
		bin := int(hz / nyquist * float64(fftSize/2))
		if bin < 1 {
			bin = 1
		}
		if bin > fftSize/2-1 {
			bin = fftSize/2 - 1
		}
		s.edges[i] = bin
	}
	return s
}

func pow(base, exp float64) float64 { return math.Pow(base, exp) }

// Analyze consumes mono float samples in [-1,1] and returns band magnitudes
// smoothed over time. Fewer than fftSize samples zero-pads.
func (s *SpectrumAnalyzer) Analyze(samples []float64) []float64 {
	for i := range s.re {
		s.re[i] = 0
		s.im[i] = 0
	}
	n := len(samples)
	if n > fftSize {
		n = fftSize
	}
	for i := 0; i < n; i++ {
		s.re[i] = samples[i] * s.window[i]
	}
	s.fft.Forward(s.re, s.im)

	for b := 0; b < s.bands; b++ {
		lo, hi := s.edges[b], s.edges[b+1]
		if hi <= lo {
			hi = lo + 1
		}
		var sum float64
		for k := lo; k < hi && k < fftSize/2; k++ {
			mag := math.Sqrt(s.re[k]*s.re[k] + s.im[k]*s.im[k])
			sum += mag * mag
		}
		rms := math.Sqrt(sum / float64(hi-lo))
		db := 20 * math.Log10(rms+1e-12)
		s.raw[b] = db
		if db > s.peakDb {
			s.peakDb = db
		}
	}

	// One automatic gain for the whole spectrum, applied after every band's level
	// is known.
	//
	// Global, not per band, and the reason is worth recording: per-band gain was the
	// first attempt and it destroys the only thing a spectrum is for. Normalising
	// each band against its own recent peak makes a loud band and an empty one both
	// read as full scale, so a 86Hz tone lit the lowest band *and* the highest
	// equally. TestSpectrumAnalyzerBands caught it -- "a loud low tone must move
	// the lowest band more than the highest" stopped being true the moment the
	// bands stopped being comparable.
	//
	// A single floor under all 48 bands keeps the frequency axis meaningful while
	// still adapting to how loud the material happens to be. What it buys: a modern
	// commercial master, which sits 10-15dB hotter than the -70..-10dB window this
	// used to assume, no longer pins every band at 1.0 and draws a solid block.
	// Measured on a T-Series pop track before the fix: band[0]=1.000,
	// band[last]=0.950, i.e. no spectrum at all.
	//
	// It is not an absolute level meter and nothing claims it is.
	// The guard is peakDb > infDb, i.e. > -140, which is the floor this is
	// allowed to fall to.
	//
	// It read > -infDb, i.e. > +140, until it was caught: peakDb reaches about
	// +42 at full scale, so the condition was never true, the subtraction never
	// ran, and the gain's high-water mark was a pure all-time maximum. Every
	// consequence downstream of "the gain falls" was therefore false -- the
	// comment on agcFallDb about a note holding its place for three seconds
	// described code that did nothing -- and nothing caught it, because the fall
	// has no behaviour of its own that fails loudly. It only fails to adapt.
	if s.peakDb > infDb {
		s.peakDb -= agcFallDb
	}
	floor := s.peakDb - agcSpanDb
	if floor < agcGateDb {
		floor = agcGateDb
	}

	for b := 0; b < s.bands; b++ {
		v := (s.raw[b] - floor) / agcSpanDb
		switch {
		case v != v: // NaN
			v = 0
		case v < 0:
			v = 0
		case v > 1:
			v = 1
		}
		// Kept before the smoothing, and separately from it, because the two have
		// different jobs and feeding one to the other breaks the second.
		s.raw[b] = v
		// asymmetric smoothing: fast attack, slow release reads better
		if v > s.smooth[b] {
			s.smooth[b] += (v - s.smooth[b]) * 0.6
		} else {
			s.smooth[b] += (v - s.smooth[b]) * 0.18
		}
		s.mags[b] = s.smooth[b]
	}
	return s.mags
}

// Raw returns the unsmoothed band magnitudes from the last analysis.
//
// Exists for the onset detector, and only for it. Detect and display need
// opposite things from the same spectrum:
//
//   - A bar wants a slow release, so a hit stays visible while it decays.
//   - An onset detector wants the opposite, and badly.
//
// Onset detection measures the *rise* between consecutive windows. Handed the
// smoothed output, the slow 0.18 release means each band only falls to 82% of its
// previous value per analysis, so the gap between transients closes by only ~18%
// instead of all the way to zero. The next transient's rise is therefore
// proportionally smaller than the one before, the flux shrinks geometrically, and
// after a few bars it sits under the detector's threshold forever.
//
// Measured on a 120bpm gated tone: the first onset fired and then nothing for the
// rest of the track, so the particle field spawned four dots and stopped. Fed the
// raw magnitudes, the same signal onsets on every transient.
func (s *SpectrumAnalyzer) Raw() []float64 { return s.raw }
