package main

import "math"

// --- peaks -------------------------------------------------------------------

// peaksViz draws only the spectral peaks: the few bands that actually carry a
// note, each one a needle at its interpolated frequency and height.
//
// Every other spectrum style draws every band, which is honest about the data
// and useless for hearing *pitch*: 48 bars is a shape, not a chord. This throws
// away the noise floor, finds the local maxima, and draws the strongest few as
// needles. A triad reads as three needles at three heights; a bass note reads as
// one at the bottom; a hat reads as one at the top.
//
// The frequency is interpolated between the two bands around a peak, because a
// peak that is really at 221Hz lands between the 30Hz-spaced edges of two
// log-spaced bands and a needle pinned to the band index jumps by 11% of its
// own frequency -- which is a semitone, and a semitone of error is the
// difference between "in tune" and "out".
type peaksViz struct {
	cols, rows int
	viz        Visualizer
	scratch    []float64
	// need is the peak count above the floor, set in Resize.
	need int
	// bestVal/bestIdx are the insertion-sort scratch, sized in Resize rather than
	// per paint. A make() in the paint loop is two allocations per frame per
	// style, forever, and the perf rule is that painting allocates nothing.
	bestVal []float64
	bestIdx []int
}

// bandRatio is the frequency ratio between adjacent log-spaced bands.
//
// A package-level var, not a per-peak Pow: it is a constant of the analyser's
// layout, and recomputing it once per needle per frame is a pow() in a hot loop
// to produce a number that cannot change.
var bandRatio = math.Pow(nyquistHz()/loBandHz, 1.0/float64(bands))

const (
	// peaksFloor is the level below which a band is not considered a note.
	//
	// Measured against real material, not chosen and hoped for. 0.12 was set from a
	// synthesised tone and turned out to be far too high: on the 12s drum-and-bass
	// fixture, only three or four bands passed it, so a 120-column screen drew three
	// needles and 114 columns of empty panel -- the same failure as the old flat cap,
	// just reached a different way. Raising `need` cannot fix that, because the limit
	// is how many bands *qualify*, not how many slots there are.
	//
	// 0.04 admits the quieter partials, and the picture stays a comb because height
	// carries the level: a needle at 0.05 is a dot on the axis and a needle at 0.9
	// is a full-height spike. That is the honest reading -- the comb gets denser as
	// the music does -- and it is what a tuner display actually does.
	peaksFloor = 0.04
	// peaksMin and peaksMax bound the needle count. The first version capped at a
	// flat 6, which is right on an 80-column terminal and absurd on a 250-column
	// one: measured at 120x40 it drew six needles and left 114 columns empty, so
	// the picture was almost entirely background and read as a renderer that had
	// stopped. The count follows the width now, like every other spectrum style.
	peaksMin = 6
	peaksMax = 28
	// peaksPerCol is how many columns one needle gets. 5 at the low end because a
	// dense comb is not readable as separate notes; the point of the style is that
	// the gaps mean something.
	peaksPerCol = 5
	// peaksAxisInk is the axis the needles stand on.
	//
	// Brighter than barsViz's baselineInk on purpose. That value is 0.10, chosen to
	// be "visible as a line without being readable as a value" -- but in colour
	// mode the cell's *colour* is what shows, and Color(band, 0.10, 0) on a
	// by-band palette is very nearly black. Measured: peaks' axis was invisible
	// on screen, so the needles had nothing to stand on and the picture read as
	// dots floating in nothing.
	peaksAxisInk = 0.34
	// peaksFaintInk is the ceiling brightness of the spectrum curve behind the
	// needles, and peaksFaintFloor the level below which it is not drawn.
	//
	// Dim on purpose, and bounded well below 1: this is context, not content. If
	// it were bright it would be barsViz and the needles would be lost in it.
	peaksFaintInk   = 0.34
	peaksFaintFloor = 0.03
)

func (p *peaksViz) Name() string { return "peaks" }
func (p *peaksViz) Heavy() bool  { return false }
func (p *peaksViz) CapScale(int, int) float64 {
	return 1
}

func (p *peaksViz) Resize(cols, rows int) {
	p.cols, p.rows = cols, rows
	// n is the *analyser's* band count, not the drawn column count. A peak is a
	// property of the spectrum, so resampling it onto columns first would invent
	// maxima that the analysis never had -- the same rule resampleBands exists to
	// keep: interpolating a spectrum invents a peak that was never in the audio.
	p.viz.Resize(bands)
	if len(p.scratch) != bands {
		p.scratch = make([]float64, bands)
	}
	// Needles follow the width, floored so a narrow grid still shows a chord and
	// capped so a wide one does not become the spectrum again -- forty needles is
	// barsViz with extra steps, and the whole point is that the gaps mean
	// something.
	p.need = cols / peaksPerCol
	if p.need < peaksMin {
		p.need = peaksMin
	}
	if p.need > peaksMax {
		p.need = peaksMax
	}
	if len(p.bestVal) != p.need {
		p.bestVal = make([]float64, p.need)
		p.bestIdx = make([]int, p.need)
		for i := range p.bestVal {
			p.bestVal[i] = peaksFloor
			p.bestIdx[i] = -1
		}
	}
}

func (p *peaksViz) Reset() {
	p.viz = Visualizer{level: make([]float64, bands), peak: make([]float64, bands)}
}

// Push feeds the smoother with the analyser's own bands, unresampled.
func (p *peaksViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, bands, p.scratch)
	p.viz.Push(p.scratch)
}

// Paint finds the peaks and draws one needle each.
//
// The scan is over bands, not over cells: a needle is a column of the grid with
// a height, so the drawing cost is need*rows and the finding cost is bands. Both
// are small and neither grows with the grid, which is why this is not a heavy
// style despite touching every column.
func (p *peaksViz) Paint(g *VizGrid) {
	if p.cols == 0 || p.rows == 0 || p.need == 0 {
		return
	}
	levels := p.viz.Level()
	if len(levels) < 3 {
		return
	}
	// Insertion sort of at most peaksMax entries, descending by level. Kept in
	// fields sized by Resize, so painting allocates nothing.
	//
	// Deliberately not a full sort of 48 bands: the top six are maintained in one
	// pass, and the pass is what makes this cheap enough not to be a heavy style.
	bestVal, bestIdx := p.bestVal, p.bestIdx
	for i := range bestVal {
		bestVal[i] = peaksFloor
		bestIdx[i] = -1
	}
	for i := 1; i < len(levels)-1; i++ {
		v := levels[i]
		if v < peaksFloor {
			continue
		}
		// A local maximum, not just a loud band: a monotonic rise has a loud end
		// and calling that a peak puts a needle on every sustained note's
		// shoulder.
		if v < levels[i-1] || v <= levels[i+1] {
			continue
		}
		// Insert into the top list if it beats the weakest kept.
		if v <= bestVal[p.need-1] {
			continue
		}
		j := p.need - 1
		for j > 0 && bestVal[j-1] < v {
			bestVal[j] = bestVal[j-1]
			bestIdx[j] = bestIdx[j-1]
			j--
		}
		bestVal[j] = v
		bestIdx[j] = i
	}

	// The spectrum itself, faint, behind the needles.
	//
	// Added because the style's premise -- only the peaks, so a triad reads as
	// three needles -- means a real track has three or four peaks in it, and the
	// rest of the grid was empty background. Measured at 120x40: three needles
	// and 114 empty columns, which reads as a renderer that failed rather than as
	// a sparse reading. The faint curve is the same data, drawn dim, so the space
	// between the needles carries the spectrum they were picked out of. It cannot
	// be mistaken for a needle because a needle is full-brightness and reaches
	// from the axis.
	axisY := p.rows - 1
	for x := 0; x < p.cols; x++ {
		bi := x * len(levels) / maxInt(p.cols, 1)
		if bi >= len(levels) {
			bi = len(levels) - 1
		}
		v := clamp01(levels[bi])
		if v <= peaksFaintFloor {
			continue
		}
		y := axisY - int(v*float64(axisY))
		if y < 0 {
			y = 0
		}
		b := bandPos(x, p.cols)
		g.Set(x, y, rampFor(peaksFaintInk*v), g.Color(b, v*peaksFaintInk, 0))
	}

	// The axis the needles stand on. Without it a needle is a floating segment
	// and the height of the pitch is unreadable -- a bass note and a hat at the
	// same level would be the same picture.
	for x := 0; x < p.cols; x++ {
		g.Set(x, axisY, rampFor(peaksAxisInk), g.Color(bandPos(x, p.cols), peaksAxisInk, 0))
	}

	for k := 0; k < p.need; k++ {
		if bestIdx[k] < 0 {
			continue
		}
		i := bestIdx[k]
		v := clamp01(bestVal[k])
		// Parabolic interpolation over the three bands around the peak: the
		// offset from the centre band, in band widths. |delta| < 0.5 always, for a
		// peak that is a local maximum of three points.
		denom := levels[i-1] - 2*levels[i] + levels[i+1]
		var delta float64
		if denom != 0 {
			delta = 0.5 * (levels[i-1] - levels[i+1]) / denom
			if delta > 0.5 {
				delta = 0.5
			} else if delta < -0.5 {
				delta = -0.5
			}
		}
		// Position across the grid by log-frequency, which is how the bands are
		// spaced, so the needles land where their pitch is. Linear in band index
		// would squash the whole treble into the right-hand tenth.
		hz := bandHz(i, bands) * math.Pow(bandRatio, delta)
		x := hzToPos(hz)
		col := int(math.Round(x * float64(p.cols)))
		if col < 0 {
			col = 0
		}
		if col >= p.cols {
			col = p.cols - 1
		}
		// The height is the band's level, and the needle is drawn from the axis up
		// to it, brightest at the tip: a needle whose brightness is uniform reads
		// as a hair rather than a peak, and the tip is the part that carries the
		// level.
		h := int(v * float64(axisY))
		for y := axisY - 1; y > axisY-h; y-- {
			t := safeDiv(float64(axisY-y), float64(maxInt(axisY, 1)), 0)
			g.Set(col, y, rampFor(0.3+0.7*t), g.Color(x, t, 0))
		}
		// A marker at the tip, in the brightest ramp index, so the pitch is a
		// thing you can point at rather than a thing you have to measure.
		if tip := axisY - h; tip > 0 {
			g.Set(col, tip, rampBright, g.Color(x, 1, 0))
		}
	}
}

// hzToPos maps a frequency onto 0..1 across the analyser's band range, in log.
//
// The same log scale as bandHz, so a needle's position is exactly the position
// of the band it came from -- which is the property the interpolation is worth:
// moving a needle by an eighth of a band moves it by the eighth of a band's
// width on screen, not by a pixel.
func hzToPos(hz float64) float64 {
	nyq := nyquistHz()
	if hz <= loBandHz {
		return 0
	}
	if hz >= nyq {
		return 1
	}
	return math.Log(hz/loBandHz) / math.Log(nyq/loBandHz)
}
