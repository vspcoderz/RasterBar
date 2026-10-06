package main

import "math"

// Beat and onset detection from the band magnitudes.
//
// Deliberately a pure type with no clock of its own. The tempting alternative is
// to detect onsets in the render loop, and that is wrong for a reason worth
// writing down: the render loop's rate is a function of the terminal
// (musicFPS drops as the grid grows), so a threshold tuned against "the last 30
// frames" would mean something different at 80x24 than at 300x120, and the
// detector would misbehave on exactly the big terminals it is hardest to debug.
//
// So this runs in the level tap instead, where the clock is the audio's own: one
// analysis per fftSize window, a fixed 92.9ms apart regardless of what anything
// is doing with the result. The visualizers read an already-computed envelope.

// BlockDur is how much audio one analysis covers, in seconds.
//
// Derived rather than written down, because the two constants that decide it
// (fftSize and spectrumRate) are the ones most likely to be tuned for quality or
// for pipe traffic. A hardcoded 0.09 would silently make every tempo estimate
// wrong the moment either moved.
func BlockDur() float64 { return float64(fftSize) / float64(spectrumRate) }

// onsetThreshold is how far above the recent baseline flux counts as an onset.
//
// 1.6x, not 1.0x, because a bare "above the baseline" fires on ordinary variation
// and the result is a strobe that is really a noise generator. 1.6x is above the
// variation of a steady passage and below the jump of a real transient.
//
// Adaptive rather than fixed, because absolute band levels vary enormously between
// a lofi loop and a mastered pop track. A fixed threshold is right for exactly one
// of them.
const onsetThreshold = 1.6

// onsetFloor is the absolute flux below which nothing is an onset, whatever the
// baseline says.
//
// Without it, silence is a problem: flux is zero, the baseline is zero, and
// 0 > 0*1.6 is false but only just -- one sample of dither in a digital-silent
// passage is many times the baseline and reads as a beat. A floor makes quiet mean
// quiet.
const onsetFloor = 0.02

// onsetCtx is how many earlier analyses a transient has to beat to count.
//
// Three blocks is about 280ms at BlockDur, which is a musical event rather than a
// single analysis. This is the test that actually carries the discrimination; see
// baseline.
const onsetCtx = 3

// refractoryBlocks is the dead time after an onset, in analyses.
//
// Three is about 280ms, so the fastest distinguishable rhythm is a sixteenth note
// at 214bpm. Without it, one loud hit that straddles two analysis windows reports
// twice and the tempo estimate comes out at double the truth.
const refractoryBlocks = 3

// beatDecay is how fast the envelope falls, per analysis.
//
// 0.72 at 92.9ms is about 270ms of glow from the onset. Long enough to see, short
// enough that the next eighth note does not land on top of the last one and turn
// the whole thing into a plateau.
const beatDecay = 0.72

// beatRise is how fast it rises. Instant, because the transient is the event.
const beatRise = 1.0

// onsetDetector tracks spectral flux and reports onsets, an envelope and a tempo
// estimate.
type onsetDetector struct {
	prev   []float64
	hist   []float64 // ring of recent flux, for the baseline
	ctx    []float64 // ring of the last onsetCtx flux values
	next   int       // next write index into hist
	cnext  int       // next write index into ctx
	filled int

	// onsets holds the block indices of recent onsets, newest last. Kept as a
	// ring of the last few because that is all a tempo estimate needs.
	onsets []int
	blocks int
	last   int // block of the last onset, for the refractory period

	beat float64
	bpm  float64

	// scratch buffers so the per-analysis baseline and tempo estimate allocate
	// nothing. Both are small and fixed-size; see baseline and tempoOfInto.
	baseScratch []float64
	tempScratch []float64
}

// onsetWindow is how many analyses the adaptive baseline is taken over.
//
// Roughly three seconds at BlockDur. Long enough to sit above a bar, short enough
// that a genuinely quiet passage lowers the threshold within a few seconds instead
// of staying deaf to the music for the whole track.
const onsetWindow = 32

// onsetHistory is how many onset times the tempo estimate reads.
const onsetHistory = 8

// NewOnsetDetector builds a detector for a given band count.
func NewOnsetDetector(nBands int) *onsetDetector {
	if nBands <= 0 {
		nBands = bands
	}
	return &onsetDetector{
		prev: make([]float64, nBands),
		hist: make([]float64, onsetWindow),
		ctx:  make([]float64, onsetCtx),
		// Negative so the first analysis is already past the refractory period
		// rather than being swallowed by a zero-valued one.
		last:        -refractoryBlocks - 1,
		baseScratch: make([]float64, onsetWindow),
		tempScratch: make([]float64, 0, onsetHistory),
	}
}

// Push folds one set of band magnitudes in and reports the new envelope and whether
// an onset landed on this analysis.
//
// Pure difference, no threshold on the bands themselves: a decaying spectrum is a
// release, and firing on releases gives two "beats" per note.
func (d *onsetDetector) Push(mags []float64) (beat float64, onset bool) {
	var flux float64
	n := len(d.prev)
	if len(mags) < n {
		n = len(mags)
	}
	for i := 0; i < n; i++ {
		if diff := mags[i] - d.prev[i]; diff > 0 {
			flux += diff
		}
		d.prev[i] = mags[i]
	}
	// Normalised by band count so the threshold means the same thing whether the
	// analyser was built with 48 bands or 8.
	if n > 0 {
		flux /= float64(n)
	}

	// Both references are read before the insert. The window has to be full of
	// *earlier* analyses before the newest one is judged against it: inserting first
	// would let the sample being tested help define the threshold it has to beat.
	wasFull := d.filled >= len(d.hist)
	base := d.baseline()
	local := d.localMax()

	d.hist[d.next] = flux
	d.next = (d.next + 1) % len(d.hist)
	if d.filled < len(d.hist) {
		d.filled++
	}
	d.ctx[d.cnext] = flux
	d.cnext = (d.cnext + 1) % len(d.ctx)

	d.blocks++
	onset = wasFull &&
		d.blocks-d.last > refractoryBlocks &&
		flux > onsetFloor &&
		flux >= local &&
		flux > base*onsetThreshold

	if onset {
		d.last = d.blocks
		d.record(d.blocks)
		d.beat = beatRise
	} else {
		d.beat *= beatDecay
	}
	return d.beat, onset
}

// baseline is the median of the recent flux, before the newest sample.
//
// The median, not the mean, and this is the single most consequential choice in
// the file. Onsets are rare by definition, so they are outliers, and a mean is
// dragged up by exactly the events it is meant to detect. For a perfectly periodic
// signal -- one transient every fifth analysis, silence in between -- the mean IS
// the transient height, and mean*1.6 is a threshold nothing can ever cross.
//
// Measured on a 120bpm gated tone, with the mean: one onset at the very start and
// then silence for the rest of the track, so the particle field spawned four dots
// and stopped. The median of that same signal is 0, the threshold drops out of the
// way, and the local-max test decides.
//
// It still does the job the mean was there for: through a sustained loud passage
// the median is high, so ordinary variation stays underneath it.
func (d *onsetDetector) baseline() float64 {
	if d.filled == 0 {
		return 0
	}
	v := d.baseScratch[:d.filled]
	copy(v, d.hist[:d.filled])
	return median(v)
}

// localMax is the largest flux in the preceding onsetCtx analyses.
//
// A transient is a *local* peak: louder than the ~280ms around it. A steady tone
// has flux 0 in every window and fails this. A loud passage has high flux
// everywhere, so nothing stands above its neighbours. That is why a median
// baseline is allowed to be zero without the detector becoming a noise generator.
func (d *onsetDetector) localMax() float64 {
	var m float64
	for i := 0; i < len(d.ctx); i++ {
		if d.ctx[i] > m {
			m = d.ctx[i]
		}
	}
	return m
}

// record appends an onset, keeping the most recent onsetHistory of them.
func (d *onsetDetector) record(block int) {
	d.onsets = append(d.onsets, block)
	if len(d.onsets) > onsetHistory {
		d.onsets = d.onsets[len(d.onsets)-onsetHistory:]
	}
	d.bpm = tempoOfInto(d.onsets, d.tempScratch)
}

// Beat is the current envelope value.
func (d *onsetDetector) Beat() float64 { return d.beat }

// BPM is the tempo estimate in beats per minute, or 0 while it is not confident.
//
// It is an estimate and the HUD labels it as one. Nothing that fits in a render
// loop is accurate; what it can be is stable, which is why this is a median of
// intervals rather than a mean and why the range is clamped.
func (d *onsetDetector) BPM() float64 { return d.bpm }

// tempoOf estimates a tempo from recent onset block indices.
//
// Median intervals, not mean: one missed onset between two beats doubles one
// interval, and a mean moves by a third. With eight samples, the median is
// unmoved by one or two wrong values and the mean is not.
//
// The interval is then rescaled by powers of two until the rate is in range,
// because the detector cannot tell a beat from a half beat and picking the
// faster of the two plausible readings is the one that tracks what people tap
// along to.
func tempoOf(onsets []int) float64 {
	return tempoOfInto(onsets, nil)
}

// tempoOfInto is tempoOf with a caller-owned scratch buffer for the interval
// list, so the per-onset tempo estimate allocates nothing.
func tempoOfInto(onsets []int, scratch []float64) float64 {
	if len(onsets) < 3 {
		return 0
	}
	intervals := scratch[:0]
	for i := 1; i < len(onsets); i++ {
		if gap := onsets[i] - onsets[i-1]; gap > 0 {
			intervals = append(intervals, float64(gap))
		}
	}
	if len(intervals) == 0 {
		return 0
	}
	med := median(intervals)
	if med <= 0 {
		return 0
	}
	// Rescale the interval by powers of two until the rate lands in range.
	//
	// The direction is the part that is easy to get backwards: halving the
	// interval DOUBLES the tempo. A detector that hears one onset every 1.11s is
	// reporting 54bpm, which is a half-time reading of a track at 107, and the
	// fix is to halve the interval, not to double it. Doubling pushes it further
	// from the range and the estimate comes back as nothing at all.
	for i := 0; i < 4; i++ {
		bpm := 60 / (med * BlockDur())
		switch {
		case bpm > 180:
			med *= 2 // too fast: the detector is hearing every subdivision
		case bpm < 60:
			med /= 2 // too slow: it is hearing every other beat
		default:
			return bpm
		}
	}
	return 0
}

// median is the middle value, averaging the two in the middle for an even count.
//
// It sorts v in place, so callers must pass a buffer they own. The private
// callers all pass a scratch slice; the tests pass literals, which they do not
// read again.
func median(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	insertionSortFloats(v)
	mid := len(v) / 2
	if len(v)%2 == 1 {
		return v[mid]
	}
	return (v[mid-1] + v[mid]) / 2
}

// insertionSortFloats sorts in place.
//
// Not sort.Slice: this runs a dozen times a second on a slice of eight, and the
// closure and reflection behind sort.Slice cost more than the entire sort at that
// size. Measured on the real input length rather than assumed.
func insertionSortFloats(v []float64) {
	for i := 1; i < len(v); i++ {
		x := v[i]
		j := i - 1
		for j >= 0 && v[j] > x {
			v[j+1] = v[j]
			j--
		}
		v[j+1] = x
	}
}

// clamp01 is the saturating clamp every envelope and band value goes through.
func clamp01(v float64) float64 {
	switch {
	case v != v: // NaN
		return 0
	case v < 0:
		return 0
	case v > 1:
		return 1
	}
	return v
}

// safeDiv divides, returning `def` for a zero or non-finite denominator.
//
// Not because the denominators are ever expected to be zero, but because they
// legitimately are on a resize to a very small grid, and a NaN that reaches a
// cell index panics the render loop from inside a painter.
func safeDiv(a, b, def float64) float64 {
	if b == 0 || math.IsNaN(b) || math.IsInf(b, 0) {
		return def
	}
	v := a / b
	if math.IsNaN(v) || math.IsInf(v, 0) {
		return def
	}
	return v
}
