package main

import "math"

// --- wheel -------------------------------------------------------------------

// wheelViz is a polar spectrum: the full circle, one angular slice per band, and
// each slice drawn *thick* rather than long.
//
// The difference from radialViz is the whole point of having both. Radial draws
// a spoke from the centre out to the band's level, so level is a length and the
// picture is a fan -- legible, but the loudest bands all start at the same point
// and overlap, so two loud bands at different frequencies draw the same shape at
// two radii and the eye has to compare radii from a common origin. The wheel
// gives every band its own radius *band*: a loud wedge is a thick arc at a fixed
// inner radius. Then the eye reads thickness, which is a comparison it makes in
// one glance, and the spectrum becomes a ring that pulses rather than a fan.
//
// Full 360, not radial's 180-degree sweep: a ring has no inside-outside, so there
// is no reason to waste half the circle, and the closed sweep is what makes the
// ends of the spectrum meet at the top, which reads as the spectrum wrapping
// around.
type wheelViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
	// cos/sin per band, computed in Resize and refreshed whenever the aspect
	// changes. A per-paint Cos/Sin is a transcendental per band per frame for
	// two numbers that only move when the grid does.
	cos, sin []float64
	aspect   float64
	// inner is the radius the wedges start at. Non-zero so there is a hub: a
	// wedge from r=0 to r=level is a pie chart, and every band would overlap
	// every other at the centre.
	inner float64
	// spin is the accumulated rotation, advanced per Push.
	spin float64
}

const (
	// wheelInnerFrac is the hub radius as a fraction of the outer radius.
	//
	// 0.42: enough that the middle is clear (a pie chart is not a wheel), little
	// enough that the ring's thickness is most of the radius (a thin annulus is a
	// circle, and the band angles are unreadable at that width).
	wheelInnerFrac = 0.42
	// wheelSpin is radians per analysis. A fixed orientation is a static ring, so
	// it creeps: at ~10.8 analyses a second this is about a turn every 58
	// seconds, slow enough to read as drift.
	wheelSpin = 0.11
)

func (w *wheelViz) Name() string { return "wheel" }
func (w *wheelViz) Heavy() bool  { return false }
func (w *wheelViz) CapScale(int, int) float64 {
	return 1
}

func (w *wheelViz) Resize(cols, rows int) {
	w.cols, w.rows = cols, rows
	w.n = bandCountFor(cols)
	w.viz.Resize(w.n)
	if len(w.scratch) != w.n {
		w.scratch = make([]float64, w.n)
	}
	if len(w.cos) != w.n {
		w.cos = make([]float64, w.n)
		w.sin = make([]float64, w.n)
	}
	w.aspect = defaultAspect
	w.updateGeometry()
}

// updateGeometry recomputes the per-band angles and the radii.
//
// Separate from Resize because the hub depends on the aspect and the aspect is
// a property of the font, not the grid -- and because this is the loop worth
// hoisting: it is bands*2 transcendental calls, and doing it per paint would put
// them in the hot loop for no reason, since the geometry only changes on resize.
func (w *wheelViz) updateGeometry() {
	cx := float64(w.cols) / 2
	cy := float64(w.rows) / 2
	// The outer radius in *pixels*, so the ring is round: a cell is
	// defaultAspect times as tall as it is wide, and taking the smaller half-axis
	// in pixels is what keeps a 200x30 grid from drawing an ellipse 6x too tall.
	outer := math.Min(cx, cy*w.aspect)
	if outer < 1 {
		w.inner = 0
		return
	}
	w.inner = outer * wheelInnerFrac
	// One slice per band over the full circle. The half-step offset puts a band
	// boundary at the top of the circle rather than a band centre, so the two ends
	// of the spectrum meet exactly there and the ring closes cleanly.
	for i := 0; i < w.n; i++ {
		ang := (float64(i)+0.5)/float64(w.n)*2*math.Pi - math.Pi/2
		w.cos[i] = math.Cos(ang)
		w.sin[i] = math.Sin(ang)
	}
}

func (w *wheelViz) Reset() {
	w.viz = Visualizer{level: make([]float64, w.n), peak: make([]float64, w.n)}
	w.spin = 0
}

// Push resamples to one band per slice and advances the rotation.
//
// On Push, not in Paint: Paint runs at up to 30Hz and at CapScale of that for a
// heavy style, so a rotation advanced per paint would run three times slower on
// a capped style and the same music would spin at two different speeds.
func (w *wheelViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, w.n, w.scratch)
	w.viz.Push(w.scratch)
	w.spin += wheelSpin
}

func (w *wheelViz) Paint(g *VizGrid) {
	if w.cols == 0 || w.rows == 0 || w.n == 0 || w.inner < 1 {
		return
	}
	cx := float64(w.cols) / 2
	cy := float64(w.rows) / 2
	// The ring's outer edge, and the band of thickness a full-scale band gets.
	outer := w.inner / wheelInnerFrac
	thick := outer - w.inner
	if thick < 1 {
		return
	}
	levels := w.viz.Level()
	peaks := w.viz.Peak()
	// Hoisted out of the band loop: the rotation is the same for every band, so
	// this is two transcendentals per frame rather than two per band per frame.
	// The per-band direction is already in cos/sin.
	cs, sn := math.Cos(w.spin), math.Sin(w.spin)
	for i := 0; i < w.n; i++ {
		lv := clamp01(levels[i])
		// A minimum thickness of one cell, for the same reason barsViz draws an
		// axis under every band: a band drawn as literally nothing is
		// indistinguishable from a slice the renderer never reached.
		h := int(lv*thick + 0.5)
		if h < 1 {
			h = 1
		}
		band := bandPos(i, w.n)
		// Rotate the slice's direction by the accumulated spin. The angle itself
		// is fixed in updateGeometry; only this rotates, so the per-band trig is
		// still hoisted out of the paint loop.
		dx := w.cos[i]*cs - w.sin[i]*sn
		dy := (w.cos[i]*sn + w.sin[i]*cs) / w.aspect
		for d := 0; d < h; d++ {
			r := w.inner + float64(d)
			x := int(math.Round(cx + dx*r))
			y := int(math.Round(cy + dy*r))
			// Brightness falls off outward, so the wedge has a gradient from the
			// hub and the ring reads as lit from inside. Uniform wedges at this
			// density blur into a ring with no structure at all.
			t := 1 - float64(d)/float64(h+1)
			v := 0.3 + 0.7*t
			g.Set(x, y, rampFor(v), g.Color(band, v, 0))
		}
		// The peak marker: a tick just outside the wedge, at the peak's radius, so
		// the ring shows where it has been as well as where it is. Drawn across
		// the slice's width rather than as one dot, because one dot on a slice
		// that is 3 cells wide sits at an arbitrary angle within the band.
		if pk := clamp01(peaks[i]); pk > lv+0.02 {
			pr := w.inner + pk*thick
			tick := int(w.thickFor(pk))
			for d := -tick; d <= tick; d++ {
				// Perpendicular to the slice, so the marker spans the band.
				x := int(math.Round(cx + dx*pr - dy*float64(d)))
				y := int(math.Round(cy + dy*pr + dx*float64(d)*w.aspect))
				g.Set(x, y, rampBright, g.Color(band, 1, 0))
			}
		}
	}
}

// thickFor is the peak marker's half-width in cells.
//
// Proportional to the slice's angular width, because that is what "spans the
// band" means on a grid that is not square: at 200 columns a slice is several
// cells across and a one-cell marker sits inside one band by accident, while at
// 40 columns it is under a cell wide and a wide marker bleeds into its
// neighbours.
func (w *wheelViz) thickFor(v float64) float64 {
	n := math.Max(float64(w.n), 1)
	// Arc length of one slice at the ring's outer radius, halved.
	arc := 2 * math.Pi * (w.inner / wheelInnerFrac) / n
	half := arc / 2
	if half > 2 {
		half = 2
	}
	return maxFloat(half, 0.5) * smoothstep(clamp01(v))
}
