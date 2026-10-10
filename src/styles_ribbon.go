package main

import "math"

// --- ribbon ------------------------------------------------------------------

// ribbonViz draws the waveform as a filled band about its own centre line.
//
// The scope draws a line and this draws the *area* between a column's minimum
// and maximum, which is what makes the two different at a glance: a quiet passage
// is a hairline and a loud one is a slab, so loudness is legible as thickness
// instead of as height. On a transients-heavy track the ribbon is almost all
// attack and spike, which reads as a waveform in the way a bar chart of a
// waveform does not.
type ribbonViz struct {
	cols, rows int
	wave       []float64
	// envLo and envHi are the per-column minimum and maximum of the last window.
	//
	// Owned here rather than recomputed per paint, for the reason scopeViz keeps
	// its window: the columns' slices do not depend on how tall the grid is, so
	// a resize only changes how they are drawn and not what they are.
	envLo []float64
	envHi []float64
	// ink is the onset envelope, smoothed here so a single transient does not
	// strobe the whole ribbon.
	ink float64
	// haveWave records whether any waveform has been pushed since the last
	// Reset.
	//
	// Needed because a zero envelope is not the same as no signal: with all-zero
	// extremes the drawn band collapses to a single row, and a style that draws
	// its centre line would put a lit hairline across a silent track. That is one
	// cell of drawing from no input at all, which is what
	// TestStylesOnlyDrawFromTheirOwnInput exists to catch.
	haveWave bool
}

const (
	// ribbonInset is the fraction of the half-height the largest possible signal
	// is drawn into.
	//
	// Below 1, so a full-scale waveform does not touch the first or last row and
	// the picture has a margin. A signal that fills the grid edge to edge has no
	// frame, and it clips the moment the input is a touch over full scale.
	ribbonInset = 0.92
	// ribbonAttack and ribbonRelease are the per-analysis envelope multipliers.
	//
	// Fast on the way up and slow on the way down, so the ribbon flashes on a hit
	// and fades through the sustain. Equal rates give a ribbon that flickers with
	// the analysis rate, which reads as instability rather than as dynamics.
	ribbonAttack  = 0.55
	ribbonRelease = 0.22
	// ribbonCoreInk is the brightness of the centre line, the one part of the
	// drawing that is not level-dependent.
	ribbonCoreInk = 0.9
)

func (r *ribbonViz) Name() string { return "ribbon" }
func (r *ribbonViz) Heavy() bool  { return false }
func (r *ribbonViz) CapScale(int, int) float64 {
	return 1
}

func (r *ribbonViz) Resize(cols, rows int) {
	r.cols, r.rows = cols, rows
	if len(r.envLo) != cols {
		r.envLo = make([]float64, cols)
		r.envHi = make([]float64, cols)
	}
}

func (r *ribbonViz) Reset() {
	r.wave = r.wave[:0]
	r.haveWave = false
	for i := range r.envLo {
		r.envLo[i], r.envHi[i] = 0, 0
	}
	r.ink = 0
}

// Push decimates the window into a per-column envelope.
//
// At the analysis rate, not the paint rate: the envelope is a measurement of the
// audio, and taking it per paint would make it a measurement of the renderer's
// frame rate too -- a 60Hz terminal and a 15Hz one would show different bands
// for the same music.
func (r *ribbonViz) Push(f *AudioFrame) {
	n := len(f.Wave)
	if n < 2 || r.cols < 1 {
		return
	}
	r.haveWave = true
	peak := 0.0
	for x := 0; x < r.cols; x++ {
		lo := x * n / r.cols
		hi := (x + 1) * n / r.cols
		if hi <= lo {
			hi = lo + 1
		}
		if hi > n {
			hi = n
		}
		loV, hiV := 0.0, 0.0
		for i := lo; i < hi; i++ {
			v := clampSigned(f.Wave[i])
			if v < loV {
				loV = v
			}
			if v > hiV {
				hiV = v
			}
		}
		r.envLo[x], r.envHi[x] = loV, hiV
		// Peak envelope, not RMS: this is a picture of the signal's extent and
		// RMS would halve the thickness of everything, which is a constant
		// discount on the one quantity being displayed.
		ext := hiV - loV
		if ext > peak {
			peak = ext
		}
	}
	// The envelope brightens the whole ribbon, so it is smoothed hard: a value
	// taken straight from this analysis jumps by its full step every 90ms.
	if peak > r.ink {
		r.ink = lerp(r.ink, peak, ribbonAttack)
	} else {
		r.ink = lerp(r.ink, peak, ribbonRelease)
	}
}

func (r *ribbonViz) Paint(g *VizGrid) {
	if !r.haveWave || r.cols == 0 || r.rows < 2 {
		return
	}
	mid := float64(r.rows-1) / 2
	span := mid * ribbonInset
	if span < 0.5 {
		return
	}
	beat := r.ink
	for x := 0; x < r.cols && x < len(r.envLo); x++ {
		top := int(math.Round(mid - clampSigned(r.envHi[x])*span))
		bot := int(math.Round(mid - clampSigned(r.envLo[x])*span))
		if top < 0 {
			top = 0
		}
		if bot >= r.rows {
			bot = r.rows - 1
		}
		// The band is filled between the extremes rather than sampled per row, so
		// its brightness is a function of the band itself: a thick slab is bright
		// all the way through and a hairline is dim, which is the thickness
		// reading the style is for.
		//
		// bandPos by column rather than by value, so the ribbon runs across the
		// palette the way a spectrum does. A ribbon coloured by height would be
		// two colours at most.
		band := bandPos(x, r.cols)
		for y := top; y <= bot; y++ {
			// Denser toward the edges, so the ribbon has an outline. A flat fill
			// has no boundary at the signal's extremes, which is the only part of
			// the drawing that carries information.
			edge := smoothstep(safeDiv(math.Abs(float64(y)-mid), span, 0))
			v := (0.35 + 0.65*edge) * (0.6 + 0.4*clamp01(beat))
			g.Set(x, y, rampFor(v), g.Color(band, clamp01(v), beat))
		}
		y := int(math.Round(mid))
		if y >= 0 && y < r.rows {
			g.Set(x, y, rampFor(ribbonCoreInk), g.Color(band, ribbonCoreInk, beat))
		}
	}
}
