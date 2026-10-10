package main

import "math"

// --- metro -------------------------------------------------------------------

// metroViz is a grid whose wavefront travels outward at the estimated tempo.
//
// This is the style that uses AudioFrame.BPM, which until now was computed by
// the onset detector and handed to the HUD and nothing else -- a tempo estimate
// thrown away on the floor. The wavefront's speed *is* the tempo: one crossing
// of the grid per beat, so at 128bpm the front moves 128 cells a minute and the
// picture is a metronome you can watch instead of a number you can read.
//
// It is also the one style that visibly misbehaves when the estimate is wrong,
// which is the correct fate for an estimate. A drifting tempo and a doubling
// tempo both look like the picture is broken, and it is, and the fix is not
// here -- the HUD already says the BPM is an estimate.
//
// The front is a *ring*, not a sweep across the rows, and the rows alternate
// phase so that a ring crossing a row boundary does not show a seam.
type metroViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
	// phase is the front's position, in cells of radius. Advanced per Push by the
	// tempo, not per paint, so it does not run slow on a capped style.
	phase float64
	// bpm is the smoothed tempo in use, and the fallback flag records whether it
	// is real.
	bpm   float64
	faked bool
	// rowPhase offsets each row's wavefront, so the grid's cells do not all light
	// at the same instant.
	rowPhase []float64
	// bandX is bandPos per column, for the colour.
	bandX []float64
	// level is the smoothed mean energy, which sets the front's brightness.
	level float64
	beat  float64
}

const (
	// metroDefaultBPM is the tempo used before the detector is confident.
	//
	// Required, not a nicety: BPM() is 0 until the detector has heard enough
	// onsets to commit, and 128bpm of silence is 40 seconds of an intro. A style
	// whose only input is the tempo must have something to do while the tempo is
	// unknown, and the honest choice is a plausible tempo with no claim attached
	// -- the HUD says "bpm ?" for exactly this interval.
	metroDefaultBPM = 128.0
	// metroBPMEase is how fast the displayed tempo follows a new estimate. Fast,
	// because a tempo that lags is a tempo that is wrong: the point is to track
	// the music, and 0.3 converges in a couple of beats.
	metroBPMEase = 0.3
	// metroRings is how many fronts are stacked across the grid's radius.
	//
	// The spacing is a property of the *geometry* and the tempo is carried by the
	// speed, not the other way round. The first version had it the other way --
	// one cell of travel per beat -- and that is arithmetically fatal at any real
	// tempo: at 128bpm an analysis is 1/10.77s, so a beat is 0.5s and a front
	// moves 0.5 cells per analysis, giving a spacing of 1 cell. Every cell in the
	// grid was then inside the front's width, so the whole screen lit every frame:
	// measured at 1.2ms/op, the most expensive style in the registry after
	// aurora, and a uniform glow rather than travelling rings. Four fronts across
	// the radius makes them legible and makes the per-cell work proportional to
	// the ink instead of to the grid.
	metroRings = 4
	// metroMinSpacing keeps a couple of cells between fronts on a narrow grid, so
	// a 20-column terminal does not get a solid block.
	metroMinSpacing = 2.0
	// metroWidth is the front's thickness as a fraction of the spacing. Under a
	// quarter, so the fronts stay separate bands with clear gaps between them.
	metroWidth = 0.22
	// metroRowSkew is the per-row phase offset, in cells. Non-zero so the front
	// is not a perfect circle of synchronised cells -- a true circle across a
	// square grid aliases into a dotted ring, and the skew turns it into a wave.
	//
	// Small on purpose, and that is a measured correction rather than taste. The
	// first version used 0.35 cells per row, which over 60 rows is 21 cells of
	// phase spread against a spacing of 25: the fronts stopped lining up into
	// rings and interfered with each other across the whole grid, lighting 61% of
	// the cells. At 0.04 the spread across 60 rows is 2.4 cells, a tenth of a
	// spacing, which is a visible wave and still a set of rings.
	metroRowSkew = 0.04
	// metroGate is the brightness multiplier at zero level, so a silent track is
	// dark but not empty. Same argument as barsViz's axis tick: nothing at all
	// reads as a broken renderer rather than as quiet music.
	metroGate = 0.12
)

func (m *metroViz) Name() string { return "metro" }
func (m *metroViz) Heavy() bool  { return false }
func (m *metroViz) CapScale(int, int) float64 {
	return 1
}

func (m *metroViz) Resize(cols, rows int) {
	m.cols, m.rows = cols, rows
	m.n = bandCountFor(cols)
	m.viz.Resize(m.n)
	if len(m.scratch) != m.n {
		m.scratch = make([]float64, m.n)
	}
	m.rowPhase = make([]float64, rows)
	for y := range m.rowPhase {
		m.rowPhase[y] = float64(y) * metroRowSkew
	}
	m.bandX = make([]float64, cols)
	for x := range m.bandX {
		m.bandX[x] = bandPos(x, cols)
	}
	m.bpm, m.phase, m.faked = 0, 0, true
}

func (m *metroViz) Reset() {
	m.viz = Visualizer{level: make([]float64, m.n), peak: make([]float64, m.n)}
	m.bpm, m.phase, m.faked = 0, 0, true
	m.level, m.beat = 0, 0
}

// Push advances the front by one beat's worth of travel at the current tempo.
//
// The spacing is a property of the grid (see metroRings), so the speed is
// `spacing * bpm/60` cells per second and an analysis is 1/analysisHz seconds.
// The front therefore crosses the grid in a number of beats that is literally the
// tempo, rather than at a rate that happens to look right.
func (m *metroViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, m.n, m.scratch)
	m.viz.Push(m.scratch)
	m.beat = clamp01(f.Beat)

	if f.BPM > 0 {
		if m.faked || m.bpm <= 0 {
			m.bpm, m.faked = f.BPM, false
		} else {
			m.bpm = lerp(m.bpm, f.BPM, metroBPMEase)
		}
	} else if m.faked {
		m.bpm = metroDefaultBPM
	}
	if m.bpm < metroMinBPM {
		m.bpm = metroMinBPM
	}
	m.phase += m.spacing() * m.bpm / 60.0 / analysisHz
	// Wrapped so the float cannot drift. phase is a radius in cells and the
	// largest useful value is a few times the grid's diagonal; past that the front
	// is off screen and the exact value is only lost precision.
	span := math.Max(float64(m.cols)+float64(m.rows), 1)
	m.phase = math.Mod(m.phase, span*metroRings)

	var sum float64
	lv := m.viz.Level()
	for _, v := range lv {
		sum += clamp01(v)
	}
	m.level = sum / float64(maxInt(len(lv), 1))
}

// spacing is the distance between adjacent fronts, in cells: the grid's radius
// divided by the number of fronts, floored so a narrow grid still shows gaps.
func (m *metroViz) spacing() float64 {
	cx := float64(m.cols) / 2
	cy := float64(m.rows) / 2
	// The radius along the *long* axis, so the fronts are across the grid's width
	// and the count is the same on every window shape. The short axis is what
	// limits how many are visible, which is what the floor is for.
	r := math.Max(cx, cy)
	s := r / metroRings
	if s < metroMinSpacing {
		s = metroMinSpacing
	}
	return s
}

// metroMinBPM keeps a detector that reports a near-zero tempo from parking the
// front for tens of seconds.
const metroMinBPM = 4.0

func (m *metroViz) Paint(g *VizGrid) {
	if m.cols == 0 || m.rows == 0 {
		return
	}
	cx := float64(m.cols) / 2
	cy := float64(m.rows) / 2
	spacing := m.spacing()
	width := spacing * metroWidth
	for y := 0; y < m.rows; y++ {
		// The row's wavefront phase, skewed by row so the front is a wave.
		ph := m.phase + m.rowPhase[y]
		dy := float64(y) - cy
		dy2 := dy * dy
		for x := 0; x < m.cols; x++ {
			dx := float64(x) - cx
			// sqrt, not math.Hypot. Hypot is overflow-safe and costs about six
			// times a square root for inputs that are grid coordinates and cannot
			// overflow, and it was the single biggest term in this style's paint:
			// 2.67ms/op at 200x60, the worst in the registry, from a function
			// this style declared itself cheap. Measured before and after.
			r := math.Sqrt(dx*dx + dy2)
			// Distance to the nearest crossing of the front's spacing, which makes
			// every cell's brightness a function of its phase rather than of its
			// distance -- the front repeats across the grid instead of expanding
			// once and leaving.
			//
			// Wrapped to [0, spacing/2] by floor-and-subtract rather than
			// math.Mod, for the same reason. d is already non-negative (Abs above),
			// so the truncation toward zero is the floor and the result is
			// correct for the whole range.
			d := math.Abs(ph - r)
			d -= spacing * math.Floor(d/spacing)
			if d > spacing/2 {
				d = spacing - d
			}
			// The front's own brightness: a smooth falloff across the band width,
			// so the edge is soft. A hard threshold here is a checkerboard.
			//
			// A cubic ease rather than math.Exp: the exponential is the same
			// shape to the eye over the range that matters, and Exp was the second
			// half of this style's paint cost. smoothstep is 3 multiplies.
			ink := smoothstep(1 - d/width)
			// Scaled by the music's level, so a silent grid is dark and a loud one
			// is bright. Without it the tempo is still visible in silence, which is
			// a lie about the music.
			ink *= metroGate + 1.5*m.level
			if ink <= 0.04 {
				continue
			}
			v := clamp01(ink)
			g.Set(x, y, rampFor(v), g.Color(m.bandX[x], v, m.beat))
		}
	}
}
