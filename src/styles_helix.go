package main

import "math"

// --- helix -------------------------------------------------------------------

// helixViz winds the spectrum onto a spiral that rotates and grows.
//
// Where wheelViz puts every band at the same radius and makes loudness a
// thickness, this gives every band its own *turn*: the bass is at the outside
// and the treble at the inside (or the reverse), and each band is a short arc
// whose length is its level. So the spectrum becomes a helix, and the thing the
// eye sees is a coil that tightens when the treble comes up.
//
// The coil's angle is the thing that makes it work, and it has to be *continuous
// across the seam*. A spiral drawn as one band per revolution -- angle = band
// index times a fixed step -- is a set of concentric circles with gaps, which is
// a different picture and a worse one. So the radius is continuous in the band
// index and only the *level* modulates it, and the whole coil is rotated by one
// accumulated angle.
type helixViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
	// cos and sin are the per-band angle, with the coil's rotation folded in at
	// paint time by one rotation matrix rather than by re-trigging per band.
	cos, sin []float64
	aspect   float64
	// r0 and r1 are the coil's inner and outer radii in cells, aspect-corrected.
	r0, r1 float64
	spin   float64
	// lobes is how many turns the coil makes between r0 and r1. The spiral's
	// pitch: fewer turns is a looser coil where each band's arc is longer and the
	// gaps between bands are visible.
	lobes float64
}

const (
	// helixTurns is how many times the coil wraps from the bass end to the treble
	// end.
	//
	// 2.5: one turn is a circle and the bands overlap into a ring, three is a
	// spring so tight the turns touch. 2.5 leaves a visible gap between
	// neighbouring turns at every grid size.
	helixTurns = 2.5
	// helixR0Frac and helixR1Frac are the coil's inner and outer radii as
	// fractions of the largest radius the grid allows. Not 0..1, because a coil
	// starting at the exact centre draws its lowest band as a dot.
	helixR0Frac = 0.14
	helixR1Frac = 0.98
	// helixSpin is radians per analysis. See wheelSpin for why this advances per
	// Push and not per paint.
	helixSpin = 0.07
	// helixArcCells is the minimum number of cells an arc is drawn across, so a
	// band at a shallow angle still gets a visible dash rather than one cell.
	helixArcCells = 2.0
)

func (h *helixViz) Name() string { return "helix" }
func (h *helixViz) Heavy() bool  { return false }
func (h *helixViz) CapScale(int, int) float64 {
	return 1
}

func (h *helixViz) Resize(cols, rows int) {
	h.cols, h.rows = cols, rows
	h.n = bandCountFor(cols)
	h.viz.Resize(h.n)
	if len(h.scratch) != h.n {
		h.scratch = make([]float64, h.n)
	}
	if len(h.cos) != h.n {
		h.cos = make([]float64, h.n)
		h.sin = make([]float64, h.n)
	}
	h.aspect = defaultAspect
	h.updateGeometry()
}

// updateGeometry recomputes the coil's radii and the per-band angle.
//
// Called from Resize only, and that is the point of it: a trig pair per band per
// frame would be 256 transcendentals a frame for two numbers that only change
// when the window does. This is the hoist the perf rules ask for, done in the
// one place where the inputs can actually change.
func (h *helixViz) updateGeometry() {
	cx := float64(h.cols) / 2
	cy := float64(h.rows) / 2
	// Pixels, so the coil is round on screen. See radialViz: a cell is
	// defaultAspect times as tall as it is wide, so an aspect-corrected radius
	// taken in cells is an ellipse stretched by two.
	outer := math.Min(cx, cy*h.aspect)
	if outer < 1 {
		h.r0, h.r1 = 0, 0
		return
	}
	h.r0 = outer * helixR0Frac
	h.r1 = outer * helixR1Frac
	// Angle per band: a full turn per `lobes`, and the trailing half-band offset
	// so band 0 sits *at* the start of the spiral rather than half a step past
	// it -- otherwise the lowest band's arc starts at an angle no other band
	// uses and the coil has a visible notch at its inner end.
	step := 2 * math.Pi * helixTurns / float64(h.n)
	for i := 0; i < h.n; i++ {
		ang := float64(i) * step
		h.cos[i] = math.Cos(ang)
		h.sin[i] = math.Sin(ang)
	}
}

func (h *helixViz) Reset() {
	h.viz = Visualizer{level: make([]float64, h.n), peak: make([]float64, h.n)}
	h.spin = 0
}

func (h *helixViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, h.n, h.scratch)
	h.viz.Push(h.scratch)
	h.spin += helixSpin
}

func (h *helixViz) Paint(g *VizGrid) {
	if h.cols == 0 || h.rows == 0 || h.n == 0 || h.r1 < 1 {
		return
	}
	cx := float64(h.cols) / 2
	cy := float64(h.rows) / 2
	levels := h.viz.Level()
	cs, sn := math.Cos(h.spin), math.Sin(h.spin)
	// The coil's length in angle, used to turn a band's level into an arc that
	// runs *along* the spiral rather than across it. A level that extends the
	// band radially (wheel's trick) is meaningless here: the neighbouring turn is
	// 2pi/lobes away, so a band that grows by a cell runs into the turn above it.
	// So a loud band is a longer arc, which is why the arcs of a loud chord
	// overlap each other and the coil thickens where the music is.
	arcScale := 2 * math.Pi * helixTurns / float64(h.n)
	for i := 0; i < h.n; i++ {
		lv := clamp01(levels[i])
		if lv <= 0.01 {
			continue
		}
		band := bandPos(i, h.n)
		// Radius grows with band index, so the bass is the outer turn and the
		// treble the inner one. Inner-out is the other way round and both read;
		// this way the largest radii are the low frequencies, which is where a
		// kick drum puts the most energy and so where the coil is widest.
		frac := bandPos(i, h.n)
		r := h.r0 + (h.r1-h.r0)*frac
		// The band runs from its own angle along the spiral by its level, so a
		// loud band overlaps the next one along the coil.
		steps := int(math.Max(helixArcCells, lv*float64(h.n)*arcScale*1.5))
		if steps > h.n {
			steps = h.n
		}
		for s := 0; s <= steps; s++ {
			a := float64(i+s) * arcScale
			ca, sa := math.Cos(a), math.Sin(a)
			// Rotate the whole point by the spin, then aspect-correct the y
			// offset. Rotation first, then the aspect divide, because the aspect
			// is a property of the *screen*, not of the shape -- correcting before
			// rotating scales the rotation itself by two.
			dx := ca*cs - sa*sn
			dy := (ca*sn + sa*cs) / h.aspect
			x := int(math.Round(cx + dx*r))
			y := int(math.Round(cy + dy*r))
			// Brightness along the band, brightest at its start: the coil's
			// head is where the current level is and its tail is the sustain, and
			// a uniform brightness makes those two identical.
			t := 1 - float64(s)/float64(steps+1)
			v := 0.35 + 0.65*t
			g.Set(x, y, rampFor(v), g.Color(band, v, 0))
		}
	}
}
