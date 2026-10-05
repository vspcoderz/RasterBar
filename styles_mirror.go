package main

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

// Push resamples the analyser's bands up to one per column. See barsViz.Push for
// why this belongs on the way in.
func (m *mirrorViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, m.n, m.scratch)
	m.viz.Push(m.scratch)
}

func (m *mirrorViz) Paint(g *VizGrid) {
	if m.n == 0 || m.rows == 0 {
		return
	}
	half := m.rows / 2
	if half < 1 {
		return
	}
	levels := m.viz.Level()

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
		lv := clamp01(levels[bi])
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
