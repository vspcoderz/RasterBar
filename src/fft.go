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

// analysisHz is how often the tap produces an analysed frame.
//
// Derived, not a separate constant: the tap reads in fftSize-sized blocks
// (`io.ReadFull` in LevelTap.pump) so the hop is exactly one window, which is
// why AGENTS.md's "~11Hz" is this number and not a round figure. A style that
// advances a clock per Push -- metro, whose whole premise is tempo -- has to use
// this and not assume 11, or its wavefront drifts by 2.3%.
//
// The float64 conversion is load-bearing. spectrumHz and fftSize are untyped
// integer constants, so `spectrumHz / fftSize` is integer division evaluated at
// compile time: 11025/1024 is 10, not 10.766, and a tempo-driven style advancing
// by one beat per analysis would be 7.7% slow -- a tempo that is visibly wrong
// for a reason that reads as a rounding mistake somewhere else.
const analysisHz = float64(spectrumHz) / float64(fftSize)

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
// -240dB.
//
// Only the floor is gated, never the peak, so the shape of anything above the gate
// is untouched: a gate on the peak would clip a quiet passage flat, and a gate on
// the floor merely decides where the bottom of the picture is.
//
// It is deliberately shallow. This gate can only reject what is genuinely
// sub-audible -- dither, codec residue, the bottom of a 24-bit capture -- and it
// cannot do anything about audible noise, because level alone is the wrong axis.
// A -74dBFS noise floor measures -46dB per band, 34dB above this gate, while real
// quiet material measures anywhere from -19dB up: no absolute threshold on band
// level separates them. See noiseLevelDb for what does.
const agcGateDb = -80.0

// The noise gate: broadband noise draws as a solid block, and neither level nor
// shape decides that alone.
//
// Measured on eight seconds of deliberately flat spectrum -- the case that defeats
// a level-based gate:
//
//	segment         total level  bands lit  mean/max
//	noise -74dBFS       -74.8       48/48      0.88    solid block
//	noise -80dBFS       -84.8       48/48      0.75    solid block
//	loud music           -4.5        8/48      0.11    correct
//	quiet tone          -43.0        2/48      0.04    correct
//
// Level alone cannot separate them: the noise measures -46dB per band, 34dB above
// agcGateDb, while real quiet content measures from -19dB up. Shape alone cannot
// either, which is the mistake both earlier attempts made -- measured across three
// real tracks, the band spread of music (p01 5.27dB) and of noise (p95 5.18dB)
// touch, so any single threshold on shape trades music for noise one-for-one.
//
// Together they separate cleanly, because the two populations differ for different
// reasons: music's flattest moments are loud (its quiet passages are sparse, hence
// high-contrast), and noise is quiet at every moment. Measured over 1452 frames of
// music and 1072 of noise:
//
//	                p05        p50        p95
//	music level   -28.4 dBFS  -12.8 dBFS  -9.1 dBFS
//	noise level   -98.9 dBFS  -78.8 dBFS  -58.6 dBFS
//
//	stdDb <= 6 AND level <= -50dBFS  ->  music gated 0.0%   noise gated 97.3%
//
// Flat then means: quiet *and* structureless. Quiet music keeps its contrast and
// survives; loud noise is audible and is left alone.
//
// Two details both cost a wrong answer when violated:
//
// The shape has to be measured in dB. In linear terms the spread is dominated by
// whatever is loudest, so frames with very different content yield similar ratios;
// in dB the spread is the dynamic range itself.
//
// It has to be measured before normalisation. Normalisation clamps at the floor,
// and a clamp manufactures contrast -- a frame of -98dBFS noise lands most of its
// bands at exactly zero and a few just above, which looks like structure and is
// purely an artefact of the floor. That version inverted the gate completely:
// quieter noise passed as *less* flat and drew brighter, so -58dBFS noise rendered
// above -78dBFS noise. It passed on tonal fixtures and failed on every broadband
// one, and is recorded here rather than silently overwritten.
const (
	// flatStdLo/flatStdHi are the band-spread ramp, in dB, applied only to frames
	// already qualified as quiet. Below flatStdLo a quiet frame is flat; above
	// flatStdHi it has structure. Between them it fades rather than blinks, because
	// the gate runs every 93ms and a step would strobe on material sitting there.
	//
	// The bottom of the ramp sits at 6dB: white noise measured p50 3.34, p95 5.20,
	// max 8.33 across 400 frames, so 6 keeps roughly 97% of noise frames fully dark
	// while still leaving room above it for the tail. Lower it to 4 and 23% of noise
	// frames pick up partial gain -- measured, not assumed -- which is the low-level
	// fuzz this gate is supposed to remove.
	//
	// What buys the higher setting is that the ramp only ever runs on quiet frames,
	// and quiet music has spread to spare: every quiet fixture measured came in well
	// clear of it (9.82, 10.16), so none of them lose any height. Loud music never
	// reaches the ramp at all -- 1 of 1452 measured frames was quiet enough to be
	// tested, and it sat above the ramp too.
	//
	// The top sits at 9dB because that is where noise stops: across 1072 noise
	// frames the spread runs p95 5.18, p99 7.26, max 9.50. Sitting it at 7 -- near
	// music's median -- let the loudest 1.5% of noise frames reach full gain and
	// flash the panel, and the smoother's slow release (0.18) then held that flash
	// for about 15 frames, roughly 1.4s of residue. At 9 no measured noise frame
	// reaches full gain.
	flatStdLo = 6.0
	flatStdHi = 9.0

	// noiseLevelDb is the level above which a frame is never treated as noise,
	// in dBFS of the input window.
	//
	// -50 sits 21dB under music's p05 (-28.4) and 9dB over noise's p95 (-58.6), so
	// it is the wide end of the gap rather than the middle of it: dropping to -60
	// would only stop gating the loudest 5% of noise, while raising it towards -40
	// would start eating into quiet music to no benefit.
	noiseLevelDb = -50.0
)

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
	// stdDb is the standard deviation of the last frame's band levels, in dB,
	// measured on the spectrum as the signal produced it -- before the gain, the
	// floor or the clamp. Kept because it is the one number that says whether the
	// frame has any shape to it, and because the alternative (recomputing it after
	// normalisation) measures the display instead of the signal and gives the wrong
	// answer. See noiseLevelDb.
	stdDb float64
	// levelDbfs is the RMS of the last input window in dBFS, the other half of the
	// noise gate's condition. Band levels cannot supply it: they are unnormalised
	// FFT magnitudes carrying a scale offset, so they do not mean what their name
	// suggests. See noiseLevelDb.
	levelDbfs float64
	rate      int
	bands     int
	window    []float64
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

// The band layout as a function, for the styles.
//
// NewSpectrumAnalyzer builds the same mapping into edges[] and keeps it private,
// which left the visualizers with no way to turn a band index into a frequency.
// scopeViz needs it to decide how much of the waveform to show (see
// scopeCycles): showing the tap's full 1024-sample window puts 41 cycles of a
// 440Hz note across an 80-column screen, two columns per cycle, which aliases
// into a band and makes every note look identical.
//
// Derived from the same closed form as the constructor above rather than read
// back out of it, so a style asking a question about a band does not need an
// analyser instance -- but the two must agree, and TestBandHzAgreesWithTheEdges
// is what holds them to it.
//
// n is the number of *drawn* bands, not the analyser's. Styles resample on the
// way in (resampleBands), so index i of n drawn bands is analyser band
// i*bands/n, and past n > bands the mapping saturates: the analyser really
// cannot resolve more frequencies than it has bins.
func bandHz(i, n int) float64 {
	if n < 1 {
		return loBandHz
	}
	// Clamped, not short-circuited: a caller with an off-by-one index gets band 0
	// or the top band, which is a usable answer, rather than a bare 30Hz that
	// means "no band" and quietly reads as the bottom of the spectrum.
	if i < 0 {
		i = 0
	}
	if i > n-1 {
		i = n - 1
	}
	j := i * bands / n
	if j < 0 {
		j = 0
	}
	if j > bands-1 {
		j = bands - 1
	}
	// Geometric centre of the band, not the arithmetic one: the edges are
	// logarithmic, so the midpoint in Hz of a band from 30Hz to 33Hz is 31.5 and
	// the geometric centre is the same thing here -- but for a band from 3000 to
	// 3300 the arithmetic midpoint is 3150 and the geometric one is 3147, and it
	// is the geometric one that matches what resampleBands averaged.
	return loBandHz * math.Pow(nyquistHz()/loBandHz, (float64(j)+0.5)/float64(bands))
}

// loBandHz is the analyser's lowest band edge. Duplicated as a constant rather
// than read from the constructor's local so bandHz can be called from a test
// with no analyser at all.
const loBandHz = 30.0

func nyquistHz() float64 { return spectrumHz / 2 }

// dominantBand is the loudest band in an analysed frame and its frequency.
//
// The index is over the analyser's own bands, because that is what AudioFrame
// carries -- resampling happens inside the styles, downstream of this.
//
// Ties resolve to the lower band. A tie means the spectrum is flat, and on a
// flat spectrum the honest answer is "the bass end", not whichever index the
// comparison happened to reach first, because a scope that zooms to a tie would
// jump between 30Hz and 5kHz on a passage that has no pitch at all.
func dominantBand(mags []float64) (int, float64) {
	best, bestI := 0.0, 0
	for i, v := range mags {
		if v > best {
			best, bestI = v, i
		}
	}
	if bestI >= bands {
		bestI = bands - 1
	}
	return bestI, bandHz(bestI, bands)
}

// Analyze consumes mono float samples in [-1,1] and returns band magnitudes
// smoothed over time. Fewer than fftSize samples zero-pads.
func (s *SpectrumAnalyzer) Analyze(samples []float64) []float64 {
	n := len(samples)
	if n > fftSize {
		n = fftSize
	}
	// Only the tail of re needs clearing: [0:n) is fully overwritten below, and im
	// is never written by the sample loop, so it is cleared in full.
	for i := n; i < len(s.re); i++ {
		s.re[i] = 0
	}
	for i := range s.im {
		s.im[i] = 0
	}
	s.measureLevel(samples[:n])
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
			// re^2 + im^2, summed directly. Taking the Sqrt per bin and
			// immediately squaring it back was several hundred wasted Sqrts per
			// window; the only Sqrt needed is the RMS below.
			sum += s.re[k]*s.re[k] + s.im[k]*s.im[k]
		}
		rms := math.Sqrt(sum / float64(hi-lo))
		db := 20 * math.Log10(rms+1e-12)
		s.raw[b] = db
		if db > s.peakDb {
			s.peakDb = db
		}
	}
	s.measureShape()

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

	g := s.noiseGain()

	for b := 0; b < s.bands; b++ {
		// The gate multiplies before the clamp so that a suppressed frame lands on
		// zero rather than on whatever the floor happened to be.
		v := (s.raw[b] - floor) / agcSpanDb * g
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

// measureShape records how spread out the frame's band levels are, in dB.
//
// Called while raw still holds band dB -- the whole point of the field's
// existence. Everything after this line is normalisation, and normalisation is
// exactly what must not be part of the measurement; see noiseLevelDb for what
// happens when it is.
//
// Digital silence reads as flat and is suppressed, which is the correct outcome
// for a different reason: with no signal there is no spectrum to draw.
func (s *SpectrumAnalyzer) measureShape() {
	mean := 0.0
	for b := 0; b < s.bands; b++ {
		mean += s.raw[b]
	}
	mean /= float64(s.bands)
	var vs float64
	for b := 0; b < s.bands; b++ {
		d := s.raw[b] - mean
		vs += d * d
	}
	s.stdDb = math.Sqrt(vs / float64(s.bands))
}

// measureLevel records the input window's true RMS in dBFS.
//
// Measured off the samples rather than off the bands, deliberately: band levels
// carry the FFT's own scale, which is why a -74dBFS noise floor reads -46 in band
// terms and agcGateDb cannot be the number its comment says it is. Getting the
// level right is what lets the gate be stated in units that mean something.
//
// Digital silence floors at -240 rather than negative infinity so that the value
// stays finite and comparable; nothing downstream divides by it.
func (s *SpectrumAnalyzer) measureLevel(samples []float64) {
	if len(samples) == 0 {
		s.levelDbfs = -240
		return
	}
	var acc float64
	for _, v := range samples {
		acc += v * v
	}
	rms := math.Sqrt(acc / float64(len(samples)))
	if rms <= 0 {
		s.levelDbfs = -240
		return
	}
	s.levelDbfs = 20 * math.Log10(rms)
}

// noiseGain is how much of a frame survives the noise gate.
//
// Two conditions, both required, because neither decides alone -- see noiseLevelDb
// for the measurements. A loud frame is never gated: music's flattest moments are
// loud, so the shape test only ever applies to material that is also quiet, and
// quiet + structureless is the one description noise fits and music does not.
//
// The shape test ramps rather than steps because the gate runs every 93ms and a
// step strobes on material sitting at the boundary. The level test steps, because
// music stays 21dB clear of it and a ramp there would only dim real content.
func (s *SpectrumAnalyzer) noiseGain() float64 {
	if s.levelDbfs > noiseLevelDb {
		return 1
	}
	switch {
	case s.stdDb >= flatStdHi:
		return 1
	case s.stdDb <= flatStdLo:
		return 0
	}
	return (s.stdDb - flatStdLo) / (flatStdHi - flatStdLo)
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
