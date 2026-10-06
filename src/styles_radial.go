package main

import "math"

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

// Push resamples the analyser's bands to the spoke count. See barsViz.Push for
// why this belongs on the way in.
func (r *radialViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, r.n, r.scratch)
	r.viz.Push(r.scratch)
}

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
	for i, lv := range r.viz.Level() {
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
		// Along a spoke the ramp index and the palette colour are constant, so
		// they are resolved once here instead of once per step.
		rampIdx := rampFor(val)
		packed := g.Color(band, val, 0)
		steps := int(rad)
		if steps < 1 {
			steps = 1
		}
		for d := 0; d <= steps; d++ {
			g.Set(int(cx+dx*float64(d)), int(cy+dy*float64(d)), rampIdx, packed)
		}
	}
}
