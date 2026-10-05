package main

import "math"

// The visualizer styles.
//
// Six of them, all painting into the same neutral grid (see viz.go). They differ
// in what state they keep and what they do with 48 numbers, not in how they
// touch the terminal -- that is the grid's job.
//
// Cheap first in the registry (viz.go), because `v` should walk through the
// readable ones and end somewhere surprising rather than land on the particle
// field on the second press.

// maxDrawnBands caps how many bands a style draws.
//
// Not the other way round. One band per column is the right answer for bars, and
// 48 bands on an 80-column terminal would leave a third of the screen empty for
// no reason; but a 200-column terminal has 152 bands' worth of width and drawing
// 48 of them looks broken. So the count follows the grid and the analysis is
// resampled to match.
const maxDrawnBands = 128

// resampleBands maps the analyser's band array onto n output bands.
//
// Two cases, and they need different rules. When shrinking, groups of adjacent
// bands are averaged, because the analyser spreads one narrow tone across
// neighbouring bins and taking only every third would drop energy and make a
// hi-hat disappear at low band counts. When growing, the nearest band is
// repeated, because interpolating a spectrum invents a peak that was never in
// the audio -- and an invented peak in a visualiser is a lie about the music.
//
// out must have room for n. The caller owns it so that painting allocates
// nothing: this runs 30 times a second.
func resampleBands(src []float64, n int, out []float64) {
	if n <= 0 || len(out) < n {
		return
	}
	if len(src) == 0 {
		for i := 0; i < n; i++ {
			out[i] = 0
		}
		return
	}
	if n >= len(src) {
		for i := 0; i < n; i++ {
			j := i * len(src) / n
			if j >= len(src) {
				j = len(src) - 1
			}
			out[i] = src[j]
		}
		return
	}
	for i := 0; i < n; i++ {
		lo := i * len(src) / n
		hi := (i + 1) * len(src) / n
		if hi <= lo {
			hi = lo + 1
		}
		if hi > len(src) {
			hi = len(src)
		}
		var sum float64
		for k := lo; k < hi; k++ {
			sum += src[k]
		}
		out[i] = sum / float64(hi-lo)
	}
}

// bandCountFor is how many bands to draw on a grid of this width.
func bandCountFor(cols int) int {
	n := cols
	if n > maxDrawnBands {
		n = maxDrawnBands
	}
	if n < 2 {
		n = 2
	}
	return n
}

// rampFor maps a value to a ramp index.
//
// Not inverted, and the loudest glyph is the *darkest* one in the string: the ramp
// runs from ' ' to '@' in order of ink density, so a high index is a cell that
// lays down a lot of ink. Loud therefore means a high index, which is why this is
// v*(len-1) and not (1-v).
func rampFor(v float64) byte {
	i := int(clamp01(v) * float64(len(ramp)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(ramp) {
		i = len(ramp) - 1
	}
	return byte(i)
}

// rampBright is the heaviest ramp character, for peaks and cores.
var rampBright = byte(len(ramp) - 1)

// bandPos is x/n as a 0..1 position across the spectrum, for palettes.
//
// The divisor is forced to at least 1 so a one-column grid cannot divide by zero
// on the way to a palette that would then be handed a NaN and produce a NaN
// colour, which reaches the terminal as a malformed SGR sequence.
func bandPos(i, n int) float64 {
	if n <= 1 {
		return 0
	}
	return float64(i) / float64(n-1)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// --- bars --------------------------------------------------------------------

// barsViz is the default: vertical bars from the bottom of the grid, with
// peak-hold caps.
//
// The peak caps are the part worth defending. A bar that only shows the current
// level is unreadable at any frame rate you would actually use: the eye compares
// one frame to the next, and a level that is already falling tells you nothing
// about how loud the passage was. The held cap is the only part that says "this
// is where it got to".
type barsViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
}

func (b *barsViz) Name() string { return "bars" }
func (b *barsViz) Heavy() bool  { return false }
func (b *barsViz) CapScale(int, int) float64 {
	return 1
}

func (b *barsViz) Resize(cols, rows int) {
	b.cols, b.rows = cols, rows
	b.n = bandCountFor(cols)
	b.viz.Resize(b.n)
	if len(b.scratch) != b.n {
		b.scratch = make([]float64, b.n)
	}
}

// Reset clears the peaks, because a peak from before a seek is a peak from a
// different part of the song.
func (b *barsViz) Reset() {
	b.viz = Visualizer{level: make([]float64, b.n), peak: make([]float64, b.n)}
}

func (b *barsViz) Push(f *AudioFrame) { b.viz.Push(f.Bands) }

func (b *barsViz) Paint(g *VizGrid) {
	if b.n == 0 || b.cols == 0 || b.rows == 0 {
		return
	}
	resampleBands(b.viz.Level(), b.n, b.scratch)
	levels := b.scratch

	// Cells available for a bar's height. One row is reserved so the peak cap
	// always has somewhere to sit above the level, which is the whole point of
	// having it: a cap drawn on top of the bar is indistinguishable from a taller
	// bar.
	height := b.rows - 1
	if height < 1 {
		height = 1
	}

	for x := 0; x < b.cols && x < b.n; x++ {
		lv := clamp01(levels[x])
		h := int(lv * float64(height))
		if h > height {
			h = height
		}
		band := bandPos(x, b.n)

		for y := 0; y < h; y++ {
			// Row 0 is the top of the grid, so the gradient runs from bright at the
			// top of the bar down to dimmer at the base. Brightness by height,
			// not by time: a bar that fades along its own length reads as depth,
			// which is not what it is showing.
			val := safeDiv(float64(height-y), float64(height), 0)
			g.Set(x, b.rows-1-y, rampFor(val), g.Color(band, val, 0))
		}
		// The cap, only where there is room above the bar for it.
		if pk := clamp01(b.viz.Peak()[x]); pk > 0 {
			ph := int(pk * float64(height))
			if ph > h && ph < b.rows {
				g.Set(x, b.rows-1-ph, rampBright, g.Color(band, 1, 0))
			}
		}
	}
}

// --- scope -------------------------------------------------------------------

// scopeViz draws the waveform as a line across the full width.
//
// The one style that does not use the band magnitudes at all: an oscilloscope
// shows amplitude against time, which is information the FFT has already thrown
// away. It also looks the most like the thing people mean by "music visualizer",
// which is presumably why it is second in the registry rather than last.
type scopeViz struct {
	cols, rows int
	wave       []float64
	palette    palette
}

func (s *scopeViz) Name() string { return "scope" }
func (s *scopeViz) Heavy() bool  { return false }
func (s *scopeViz) CapScale(int, int) float64 {
	return 1
}

func (s *scopeViz) Resize(cols, rows int) { s.cols, s.rows = cols, rows }

// Reset drops the waveform. There is nothing to keep across a seek: the window
// holds the last 93ms of the *old* position, and drawing it after a jump would
// show a fragment of the wrong part of the song for a frame.
func (s *scopeViz) Reset() { s.wave = s.wave[:0] }

// Push keeps the latest window.
//
// Copied rather than aliased: the tap recycles its sample buffer on the next
// read, ~11 times a second, so holding the slice would draw a half-overwritten
// waveform. One copy of 1024 floats at that rate is not a cost worth avoiding.
func (s *scopeViz) Push(f *AudioFrame) {
	if len(f.Wave) == 0 {
		return
	}
	s.wave = append(s.wave[:0], f.Wave...)
}

// Paint draws the waveform as a connected line.
func (s *scopeViz) Paint(g *VizGrid) {
	n := len(s.wave)
	if n == 0 || s.cols == 0 || s.rows == 0 {
		return
	}
	mid := (s.rows - 1) / 2
	prevY := -1
	for x := 0; x < s.cols; x++ {
		// Decimate: average the samples in this column's slice, so a
		// high-frequency waveform reads as a band of noise around the centre
		// instead of as one aliased sample.
		lo := x * n / s.cols
		hi := (x + 1) * n / s.cols
		if hi <= lo {
			hi = lo + 1
		}
		if hi > n {
			hi = n
		}
		amp := clampSigned(sumRange(s.wave, lo, hi) / float64(hi-lo))
		y := mid - int(amp*float64(mid))
		if y < 0 {
			y = 0
		}
		if y >= s.rows {
			y = s.rows - 1
		}
		// Fill the gap to the previous column so the line is connected. A
		// waveform that skips rows between columns reads as separate strokes
		// rather than as a signal.
		if prevY >= 0 && y != prevY {
			step := 1
			if y < prevY {
				step = -1
			}
			for yy := prevY; yy != y; yy += step {
				g.SetRamp(x, yy, rampFor(0.85))
			}
		}
		val := clamp01(0.5 + amp*0.5)
		g.Set(x, y, rampFor(val), g.Color(bandPos(x, s.cols), val, 0))
		prevY = y
	}
}

// sumRange adds a half-open range, for the decimating averages.
func sumRange(v []float64, lo, hi int) float64 {
	var sum float64
	for i := lo; i < hi && i < len(v); i++ {
		sum += v[i]
	}
	return sum
}

// clampSigned saturates to -1..1, for a waveform sample. A NaN becomes 0 rather
// than propagating: it would reach an int conversion and index a cell with a
// garbage row.
func clampSigned(v float64) float64 {
	switch {
	case v != v:
		return 0
	case v < -1:
		return -1
	case v > 1:
		return 1
	}
	return v
}

// --- mirror ------------------------------------------------------------------

// mirrorViz draws the spectrum mirrored about a horizontal centre axis.
//
// Reads as a shape rather than a chart, which is the point: the eye sees
// symmetry and the symmetry is the information. Unlike the others this is not
// trying to be legible as a measurement.
type mirrorViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
	fold       bool // kaleidoscope: fold the left half onto the right
}

func (m *mirrorViz) Name() string { return "mirror" }
func (m *mirrorViz) Heavy() bool  { return false }
func (m *mirrorViz) CapScale(int, int) float64 {
	return 1
}

func (m *mirrorViz) Resize(cols, rows int) {
	m.cols, m.rows = cols, rows
	m.n = bandCountFor(cols)
	m.viz.Resize(m.n)
	if len(m.scratch) != m.n {
		m.scratch = make([]float64, m.n)
	}
	// Below about 40 columns the fold makes a shape narrower than 20 cells, which
	// is not a shape. Above it, the closed form is what makes it worth having.
	m.fold = cols >= 40
}

func (m *mirrorViz) Reset() {
	m.viz = Visualizer{level: make([]float64, m.n), peak: make([]float64, m.n)}
}

func (m *mirrorViz) Push(f *AudioFrame) { m.viz.Push(f.Bands) }

func (m *mirrorViz) Paint(g *VizGrid) {
	if m.n == 0 || m.rows == 0 {
		return
	}
	half := m.rows / 2
	if half < 1 {
		return
	}
	resampleBands(m.viz.Level(), m.n, m.scratch)

	for x := 0; x < m.cols; x++ {
		// Kaleidoscope fold: reflect the left half onto the right so the shape
		// closes on itself.
		src := x
		if m.fold && x >= m.cols/2 {
			src = m.cols - 1 - x
		}
		bi := src * m.n / maxInt(m.cols, 1)
		if bi >= m.n {
			bi = m.n - 1
		}
		lv := clamp01(m.scratch[bi])
		h := int(lv * float64(half))
		band := bandPos(bi, m.n)
		for y := 0; y < h; y++ {
			val := safeDiv(float64(y+1), float64(half), 0)
			// Above the axis and below it, mirrored.
			g.Set(x, half-1-y, rampFor(val), g.Color(band, val, 0))
			g.Set(x, half+y, rampFor(val), g.Color(band, val, 0))
		}
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// --- waterfall ---------------------------------------------------------------

// waterfallViz scrolls the spectrum history downward, one row per frame.
//
// The only style whose output is a function of time rather than of the current
// moment, and for that reason the most useful one for hearing structure: a drop
// in the bass across six rows is a thing you can see, where on the bars it is a
// bar that is briefly shorter.
type waterfallViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	row        []float64 // the row about to be written
	dirty      bool      // a row has been captured since the last Paint
}

func (w *waterfallViz) Name() string { return "waterfall" }
func (w *waterfallViz) Heavy() bool  { return false }
func (w *waterfallViz) CapScale(int, int) float64 {
	return 1
}

func (w *waterfallViz) Resize(cols, rows int) {
	w.cols, w.rows = cols, rows
	w.n = bandCountFor(cols)
	w.viz.Resize(w.n)
	if len(w.row) != w.n {
		w.row = make([]float64, w.n)
		w.dirty = false
	}
}

// Reset clears the history. On a resize the row count changes and a scroll buffer
// of the old height is meaningless, so it restarts rather than resampling a
// rectangle nobody can map back onto a new one.
func (w *waterfallViz) Reset() {
	w.viz = Visualizer{level: make([]float64, w.n), peak: make([]float64, w.n)}
	w.dirty = false
}

func (w *waterfallViz) Push(f *AudioFrame) {
	w.viz.Push(f.Bands)
	// The row is captured on Push, not on Paint: Push happens once per analysis
	// (audio rate, ~11/s) while Paint may run at 30/s. Capturing on Paint would
	// scroll two identical rows for every real one and the history would move at
	// twice the speed of the music.
	resampleBands(w.viz.Level(), w.n, w.row)
	w.dirty = true
}

func (w *waterfallViz) Paint(g *VizGrid) {
	if w.rows == 0 || w.cols == 0 {
		return
	}
	if w.dirty {
		// Age the existing rows by shifting down one, then write the new one at
		// the top. An O(rows*cols) copy per frame, which at 60x120 and 30fps is
		// 216k cell moves a second -- cheaper than a ring buffer that every
		// painter would then have to unwrap, and the whole grid is being painted
		// anyway.
		for y := w.rows - 1; y > 0; y-- {
			copy(g.gray[y*w.cols:y*w.cols+w.cols], g.gray[(y-1)*w.cols:y*w.cols])
			copy(g.rgb[y*w.cols:y*w.cols+w.cols], g.rgb[(y-1)*w.cols:y*w.cols])
		}
		for i := range g.gray[:w.cols] {
			g.gray[i] = 0
			g.rgb[i] = 0
		}
		for x := 0; x < w.cols && x < w.n; x++ {
			val := clamp01(w.row[x])
			g.Set(x, 0, rampFor(val), g.Color(bandPos(x, w.n), val, 0))
		}
		w.dirty = false
	}
}

// --- radial ------------------------------------------------------------------

// radialViz draws the spectrum as spokes fanning out from a centre point.
//
// Heavy, and capped below, because it computes a distance per cell rather than
// indexing one.
//
// The aspect correction is not optional. A character cell is about twice as tall
// as it is wide, so a circle drawn on an uncorrected grid is an ellipse stretched
// to twice its intended height -- which reads as a broken renderer rather than as
// a circle. Every y offset is divided by the cell aspect before the distance test,
// the same defaultAspect the video path uses.
type radialViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
	aspect     float64
}

func (r *radialViz) Name() string { return "radial" }
func (r *radialViz) Heavy() bool  { return true }

func (r *radialViz) CapScale(cols, rows int) float64 {
	cells := cols * rows
	if cells <= 4000 {
		return 1
	}
	// Toward a floor rather than to zero: a big grid should show a smaller
	// radial, not no radial.
	return math.Max(0.35, 4000/float64(cells))
}

func (r *radialViz) Resize(cols, rows int) {
	r.cols, r.rows = cols, rows
	r.n = bandCountFor(cols)
	r.viz.Resize(r.n)
	if len(r.scratch) != r.n {
		r.scratch = make([]float64, r.n)
	}
	r.aspect = defaultAspect
}

func (r *radialViz) Reset() {
	r.viz = Visualizer{level: make([]float64, r.n), peak: make([]float64, r.n)}
}

func (r *radialViz) Push(f *AudioFrame) { r.viz.Push(f.Bands) }

func (r *radialViz) Paint(g *VizGrid) {
	if r.cols == 0 || r.rows == 0 {
		return
	}
	cx := float64(r.cols) / 2
	cy := float64(r.rows) / 2
	// The smaller half-axis, in *pixels* rather than cells, so the shape fits on
	// the short axis without ever running off the long one.
	maxR := math.Min(cx, cy*r.aspect)
	if maxR < 1 {
		return
	}
	resampleBands(r.viz.Level(), r.n, r.scratch)

	for i, lv := range r.scratch {
		lv := clamp01(lv)
		if lv <= 0.01 {
			continue
		}
		// One spoke per band, fanning from straight up out to a quarter turn each
		// side. A full 360 fan would put the bass and treble on top of each other
		// at the same angle, which is the one arrangement that makes the mapping
		// from colour to frequency unreadable.
		ang := (bandPos(i, r.n) - 0.5) * math.Pi
		dx := math.Sin(ang)
		dy := -math.Cos(ang) / r.aspect
		rad := lv * maxR
		val := lv
		band := bandPos(i, r.n)
		steps := int(rad)
		if steps < 1 {
			steps = 1
		}
		for d := 0; d <= steps; d++ {
			g.Set(int(cx+dx*float64(d)), int(cy+dy*float64(d)), rampFor(val), g.Color(band, val, 0))
		}
	}
}

// --- particles ---------------------------------------------------------------

// particlesViz is the heaviest style: dots launched on transients.
//
// Heavy because it carries per-particle state and integrates it every frame.
//
// The budget is applied at *construction*, not in the paint loop. A grid of
// 300x120 is 36000 cells, and clearing that much particle state on every style
// switch and every seek is a GC pause on the machine that can least afford one --
// the fix has to be not allocating it in the first place.
type particlesViz struct {
	cols, rows int
	budget     int
	px, py     []float64
	pvx, pvy   []float64
	life       []float64
	alive      int
	beat       float64
	prevBeat   float64
	aspect     float64
}

// particleBudget is the cap, derived from the grid so a big terminal does not
// cost proportionally more. 400 is where this stops reading as a field and starts
// reading as static.
func particleBudget(cols, rows int) int {
	n := cols * rows / 8
	if n > 400 {
		n = 400
	}
	if n < 24 {
		n = 24
	}
	return n
}

func (p *particlesViz) Name() string { return "particles" }
func (p *particlesViz) Heavy() bool  { return true }

func (p *particlesViz) CapScale(cols, rows int) float64 {
	return float64(particleBudget(cols, rows)) / 400
}

func (p *particlesViz) Resize(cols, rows int) {
	p.cols, p.rows = cols, rows
	p.aspect = defaultAspect
	p.budget = particleBudget(cols, rows)
	// Allocated once per resize, sized to the cap. This is the whole reason the
	// style is capped: no slice here is ever cols*rows long.
	p.px = make([]float64, p.budget)
	p.py = make([]float64, p.budget)
	p.pvx = make([]float64, p.budget)
	p.pvy = make([]float64, p.budget)
	p.life = make([]float64, p.budget)
	p.alive = 0
	p.beat, p.prevBeat = 0, 0
}

func (p *particlesViz) Reset() {
	for i := range p.life {
		p.life[i] = 0
	}
	p.alive = 0
	p.beat, p.prevBeat = 0, 0
}

func (p *particlesViz) Push(f *AudioFrame) {
	p.prevBeat = p.beat
	p.beat = clamp01(f.Beat)
	// Spawn on the *rise* of the envelope, not on its level. Level would spawn on
	// every frame of a loud passage and spend the whole budget in the first tenth
	// of a bar, leaving nothing for the rest of the track. A rise is one event per
	// onset.
	if rise := p.beat - p.prevBeat; rise > 0.25 {
		p.spawn(rise)
	}
	p.integrate()
}

// spawn adds particles, up to the budget.
func (p *particlesViz) spawn(rise float64) {
	// The golden angle spreads consecutive spawn directions evenly around a
	// circle without a random source, so the field is reproducible: the same
	// music gives the same picture, which makes a bug report actionable.
	n := int(rise * 12)
	if n < 1 {
		n = 1
	}
	for i := 0; i < n && p.alive < p.budget; i++ {
		k := p.alive
		p.px[k] = float64(p.cols) / 2
		p.py[k] = float64(p.rows) / 2
		ang := float64(k) * 2.399963
		speed := 0.5 + rise*0.8
		p.pvx[k] = math.Cos(ang) * speed
		// Flattened by the cell aspect, so a circle of particles is round on
		// screen rather than twice as tall.
		p.pvy[k] = math.Sin(ang) * speed / p.aspect
		p.life[k] = 1
		p.alive++
	}
}

// integrate advances every particle and retires the dead ones.
func (p *particlesViz) integrate() {
	// Retire by compacting in place rather than by freeing slots: the alive set
	// stays contiguous, so Paint is a single pass with no gaps to skip.
	w := 0
	for i := 0; i < p.alive; i++ {
		p.life[i] -= 0.02
		if p.life[i] <= 0 {
			continue
		}
		p.px[i] += p.pvx[i]
		p.py[i] += p.pvy[i]
		if w != i {
			p.px[w], p.px[i] = p.px[i], p.px[w]
			p.py[w], p.py[i] = p.py[i], p.py[w]
			p.pvx[w], p.pvx[i] = p.pvx[i], p.pvx[w]
			p.pvy[w], p.pvy[i] = p.pvy[i], p.pvy[w]
			p.life[w] = p.life[i]
		}
		w++
	}
	p.alive = w
}

func (p *particlesViz) Paint(g *VizGrid) {
	for i := 0; i < p.alive; i++ {
		val := clamp01(p.life[i])
		// Fade with life, so a particle dims as it dies rather than vanishing.
		g.Set(int(p.px[i]), int(p.py[i]), rampFor(0.3+0.6*val), g.Color(0.5, val, p.beat))
	}
	// A core on the beat, so there is something to see at the centre even when
	// every particle has already drifted off. Brightness tracks the envelope, so
	// it is a glow rather than a strobe.
	if p.beat > 0.3 {
		g.Set(p.cols/2, p.rows/2, rampBright, g.Color(0, 1, p.beat))
	}
}
