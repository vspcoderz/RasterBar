package main

import "math"

// --- lissajous ---------------------------------------------------------------

// lissajousViz plots the waveform against itself, offset by a delay.
//
// The classic oscillation plot: x is the signal and y is the same signal a
// quarter-period later, so a pure tone draws a clean ellipse and a mix of two
// tones draws the figure whose ratio of frequencies is the ratio of the two
// delays. It is the one style where the *shape* is the reading, so it says
// things about the music that a spectrum cannot: two notes a fifth apart make a
// closed figure, a drifting note makes a wobbling one.
//
// The delay is not fixed and not a frequency estimate. A fixed delay gives a
// figure that changes shape every time the pitch moves, which is the opposite
// of the point: the whole reason to plot against a delay is that the delay is
// held constant while the music moves through it.
type lissajousViz struct {
	cols, rows int
	wave       []float64
	trail      phosphor
	// spin is the delay in samples, and it is *derived* from the pitch rather
	// than picked: a quarter period at the dominant frequency is the delay that
	// makes a sine an ellipse rather than a diagonal smear, and "diagonal smear"
	// is what a fixed delay produces on every note but middle C.
	spin float64
	// spinTarget is where spin is heading, so it eases rather than jumping.
	spinTarget float64
	phase      float64
	beat       float64
}

const (
	// lissajousDelayCycles is the delay, as a fraction of the dominant period.
	//
	// A quarter period is the canonical choice: it is what makes a sine close
	// into an ellipse of aspect 1, which is the figure people recognise as "an
	// oscillation plot". A third period gives a figure with three lobes, which is
	// prettier but harder to read at a glance, and a *fixed number of samples*
	// gives a different figure for every pitch -- which is to say the figure says
	// nothing about the music and everything about the delay.
	lissajousDelayCycles = 0.25
	// lissajousMinDelay floors the delay so a bright treble does not collapse it
	// to a couple of samples and draw a straight line: at 1 sample of delay the
	// "figure" is the diagonal.
	lissajousMinDelay = 8
	// lissajousEase is how far the delay follows a new pitch per analysis. Slower
	// than scopeHzSlew because the delay is what the eye tracks as the figure's
	// identity: a figure that reshapes between two analyses does not read as one
	// object moving.
	lissajousEase = 0.08
	// lissajousTrailDecay leaves about 25 analyses on screen -- roughly two
	// seconds. Much longer than the scope's: this style is not a measuring
	// instrument, it is a picture, and the older traces are half of it.
	lissajousTrailDecay = 0.89
	// lissajousRot is how far the plot spins per analysis, in radians.
	//
	// A fixed orientation is a static picture, which on a still frame looks
	// correct and on screen looks dead. Slow enough that it reads as a drift
	// rather than a carousel: at 11 analyses a second, 0.04 rad is about a turn
	// every 24 seconds.
	lissajousRot = 0.04
)

func (l *lissajousViz) Name() string { return "lissajous" }
func (l *lissajousViz) Heavy() bool  { return false }
func (l *lissajousViz) CapScale(int, int) float64 {
	return 1
}

func (l *lissajousViz) Resize(cols, rows int) {
	l.cols, l.rows = cols, rows
	l.spin, l.spinTarget = 0, 0
	l.trail.Resize(cols, rows, lissajousTrailDecay)
}

func (l *lissajousViz) Reset() {
	l.wave = l.wave[:0]
	l.spin, l.spinTarget = 0, 0
	l.phase, l.beat = 0, 0
	l.trail.Clear()
}

func (l *lissajousViz) Push(f *AudioFrame) {
	if len(f.Wave) > 0 {
		l.wave = append(l.wave[:0], f.Wave...)
	}
	l.beat = clamp01(f.Beat)

	// A quarter of the dominant period, in samples. The dominant band is the
	// pitch, and the pitch is the delay -- which is the inversion that makes this
	// style work: the same arithmetic that gives scopeViz its zoom gives this one
	// its figure.
	_, hz := dominantBand(f.Bands)
	if hz > 0 {
		d := int(lissajousDelayCycles * spectrumHz / hz)
		if d < lissajousMinDelay {
			d = lissajousMinDelay
		}
		l.spinTarget = float64(d)
	}
	if l.spin <= 0 {
		l.spin = l.spinTarget
	} else {
		l.spin = lerp(l.spin, l.spinTarget, lissajousEase)
	}
	l.phase += lissajousRot
	l.trail.Decay()
}

func (l *lissajousViz) Paint(g *VizGrid) {
	l.trail.Blit(g)
	n := len(l.wave)
	if n < 4 || l.cols == 0 || l.rows == 0 {
		return
	}
	delay := int(l.spin)
	if delay < 1 {
		delay = 1
	}
	if delay >= n/2 {
		// The window is shorter than twice the delay, so the figure cannot close.
		// Halving it keeps a real figure rather than a short smear, and the floor
		// above has already kept it from being a diagonal.
		delay = maxInt(n/4, 1)
	}
	cx := float64(l.cols) / 2
	cy := float64(l.rows) / 2
	rx := cx - 0.5
	ry := cy - 0.5
	if rx < 1 {
		rx = 1
	}
	if ry < 1 {
		ry = 1
	}
	rot := math.Cos(l.phase)
	sin := math.Sin(l.phase)
	// Step the waveform so a whole period lands in about one column's worth of
	// curve, independent of how many samples the window holds. Otherwise a long
	// window draws a figure so dense it fills solid and a short one draws a
	// sparse scatter, and the density -- not the music -- decides what it looks
	// like.
	step := (n - delay) / maxInt(l.cols, 1)
	if step < 1 {
		step = 1
	}
	prevX, prevY := 0, 0
	first := true
	// Brightness rises with the onset envelope, so a beat is a flare along the
	// whole figure. Not per-point: the figure is one object and lighting its
	// points separately reads as noise on the line.
	ink := 0.55 + 0.45*l.beat
	for i := 0; i+delay < n; i += step {
		// Elliptical, so a circle of signal is a circle on screen. The cell
		// aspect divides the y radius for the same reason radialViz divides every
		// y offset: a cell is twice as tall as it is wide, and an aspect-corrected
		// figure that is not corrected here reads as a figure squashed vertically.
		vx := clampSigned(l.wave[i])
		vy := clampSigned(l.wave[i+delay])
		x := int(math.Round(cx + vx*rx))
		y := int(math.Round(cy - vy*ry/defaultAspect*2))
		// Rotate about the centre, in cell space, so the spin is a rotation of
		// the drawn figure rather than a change in its aspect.
		if l.phase != 0 {
			dx := float64(x) - cx
			dy := float64(y) - cy
			x = int(math.Round(cx + dx*rot - dy*sin))
			y = int(math.Round(cy + dx*sin + dy*rot))
		}
		if first {
			l.trail.Lay(x, y, ink)
			first = false
		} else {
			l.trail.LayLine(prevX, prevY, x, y, ink)
		}
		prevX, prevY = x, y
	}
	l.trail.Blit(g)
}
