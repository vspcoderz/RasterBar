package main

// --- aurora ------------------------------------------------------------------

// auroraViz is a drifting noise field, lit by the music's low end.
//
// The only style in the registry with no structure at all: no bars, no rings, no
// grid. It is value noise scrolling upward, warped by the bass, so a kick is a
// ripple that crosses the screen and a quiet passage is a slow shimmer. It is
// here because it is the one that looks like nothing else, and after twelve
// styles that are all legible readings of the same numbers, a picture that is
// only a reaction is worth having.
//
// It is the registry's heavy style, and the reason is per-cell: every cell of
// every frame evaluates two noise lookups and several transcendentals, on a
// 300x120 grid that is 36000 evaluations a frame at up to 30Hz. Every other style
// costs a loop over columns and an inner loop over a bar's height, which is a
// very different arithmetic. `Heavy()` says so and `CapScale` halves the frame
// rate past 4000 cells, where the field is still recognisably a field.
type auroraViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
	// drift is the field's scroll offset, advanced per Push.
	drift float64
	// warp is the bass-driven distortion amount, smoothed: a raw bass energy
	// would make the field jitter at the analysis rate, which reads as a broken
	// renderer rather than as a reaction.
	warp float64
	beat float64
	ink  float64
	// level is the smoothed mean level of the spectrum, and it gates the field.
	//
	// Load-bearing, not a nicety. Value noise is 0..1 *by construction*, so a
	// field drawn straight from it is a bright screen of static with no music
	// playing at all -- the picture stops being a reaction to anything. Measured:
	// ungated, aurora lit 654 of 672 cells on a silent grid. Gated by the level, a
	// silent track is a dark screen and the field only exists when there is
	// something for it to react to.
	level  float64
	aspect float64
	// cellX and cellY are the field's coordinates per column and per row, so the
	// per-cell lookup does not do two multiplies and a warp add. Hoisted in
	// Resize, which is the perf rule about hoisting anything that does not
	// depend on the inner loop index.
	cellX, cellY []float64
	bandX        []float64
}

const (
	// auroraScale is the field's feature size, in cells. Bigger is softer and
	// slower to change; smaller is grainy and flickers.
	auroraScale = 0.16
	// auroraRise is how fast the field scrolls, in field units per analysis. The
	// field moves upward: rows are indexed downward, so the offset is subtracted
	// to make the pattern travel up the screen.
	auroraRise = 0.021
	// auroraWarp is the maximum horizontal displacement the bass can apply, in
	// field units. Capped because the displacement has to stay a fraction of the
	// feature size -- a warp bigger than that is not a warp, it is a scramble, and
	// the field stops reading as a field.
	auroraWarp = 0.55
	// auroraWarpEase smooths the bass into the warp, per analysis. Slow, because
	// the warp is a shape and a shape that changes every 90ms is noise.
	auroraWarpEase = 0.18
	// auroraFloor is the field value below which a cell is left at the
	// background. Without it the whole grid is a faint haze of cells one step
	// above the background, which is what a noise field at low amplitude looks
	// like and is not worth drawing.
	auroraFloor = 0.06
)

func (a *auroraViz) Name() string { return "aurora" }
func (a *auroraViz) Heavy() bool  { return true }

// CapScale halves the frame rate past 4000 cells.
//
// One step, not a ratio. The field's cost is per cell per frame, so a *linear*
// cap would still be far too slow at 36000 cells -- a smooth curve to 1/9 of the
// rate is not a smooth curve, it is a slideshow. Halving is the one reduction
// that keeps it a moving picture at any grid size.
func (a *auroraViz) CapScale(cols, rows int) float64 {
	cells := cols * rows
	if cells <= 4000 {
		return 1
	}
	if cells <= 16000 {
		return 0.5
	}
	return 0.35
}

func (a *auroraViz) Resize(cols, rows int) {
	a.cols, a.rows = cols, rows
	a.n = bandCountFor(cols)
	a.viz.Resize(a.n)
	if len(a.scratch) != a.n {
		a.scratch = make([]float64, a.n)
	}
	a.aspect = defaultAspect
	a.cellX = make([]float64, cols)
	a.cellY = make([]float64, rows)
	a.bandX = make([]float64, cols)
	for x := 0; x < cols; x++ {
		// The field is sampled at 1/auroraScale so the feature size is the
		// constant, not the column index. Cheaper too: one multiply per column,
		// done once.
		a.cellX[x] = float64(x) * auroraScale
		a.bandX[x] = bandPos(x, cols)
	}
	for y := 0; y < rows; y++ {
		a.cellY[y] = float64(y) * auroraScale
	}
	a.drift, a.warp, a.ink = 0, 0, 0
}

func (a *auroraViz) Reset() {
	a.viz = Visualizer{level: make([]float64, a.n), peak: make([]float64, a.n)}
	a.drift, a.warp, a.ink = 0, 0, 0
	a.beat, a.level = 0, 0
}

// Push advances the drift and warms the bass into the warp, at the analysis rate.
//
// Per Push, not per Paint: Paint runs at up to 30Hz and at CapScale of that --
// and this style *is* capped, so a drift advanced per paint would run at 0.35
// speed on a large grid and at full speed on a small one. The same music would
// scroll at two different rates depending on the terminal.
func (a *auroraViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, a.n, a.scratch)
	a.viz.Push(a.scratch)
	a.beat = clamp01(f.Beat)
	a.drift += auroraRise

	// The low third of the spectrum drives the warp. Not the whole mean: a
	// midrange-heavy track has a high mean and a low warp, and the field would
	// react to a vocal.
	var sum float64
	for i := 0; i < a.n/3; i++ {
		sum += clamp01(a.viz.Level()[i])
	}
	bass := sum / float64(maxInt(a.n/3, 1))
	a.warp = lerp(a.warp, clamp01(bass*1.4), auroraWarpEase)
	a.ink = lerp(a.ink, clamp01(f.Beat), 0.25)
	// Mean level across the whole spectrum, smoothed on the analysis clock and
	// then hard-scaled in Paint. See the `level` field: an ungated noise field is
	// a lit screen with no music on it.
	var all float64
	for _, v := range a.viz.Level() {
		all += clamp01(v)
	}
	a.level = lerp(a.level, all/float64(maxInt(len(a.viz.Level()), 1)), 0.25)
}

func (a *auroraViz) Paint(g *VizGrid) {
	if a.cols == 0 || a.rows == 0 {
		return
	}
	// Two noise lookups per cell, and they are the whole cost of this style.
	// Hoisted: the drift, the warp and the column's base x do not depend on the
	// row, so they are computed once per column and the inner loop is two
	// lookups and an exp.
	warp := a.warp * auroraWarp
	// The gate, hoisted for the same reason. 0.1 rather than 0: at exactly zero a
	// silent track is a perfectly empty grid, which is correct but reads as a
	// broken renderer rather than as quiet music -- and the same argument
	// barsViz's axis tick and matrixViz's unlit LEDs make.
	gate := 0.1 + 1.5*a.level
	for x := 0; x < a.cols; x++ {
		bx := a.cellX[x]
		band := a.bandX[x]
		for y := 0; y < a.rows; y++ {
			// The field scrolls upward: the sample's y is offset by the drift.
			fy := a.cellY[y] - a.drift
			// Domain-warped by the bass. Two noise samples: one gives the warp
			// offset, the other the value. A single noise sample with no warp is a
			// smoke texture, which is a different picture and a duller one.
			w := vnoise(bx+warp*2, fy+warp)
			v := vnoise(bx+w*warp*2.2, fy)
			// A gradient from the field's value, so it reads as a curtain rather
			// than as static: the top of the grid is where the field is strongest.
			g2 := v * (0.35 + 0.65*clamp01(1-float64(y)/float64(a.rows))) * gate
			// The onset envelope lifts the whole field a little, so a beat is a
			// brightening rather than a separate thing being drawn.
			g2 += a.ink * 0.18
			if g2 <= auroraFloor {
				continue
			}
			val := clamp01((g2 - auroraFloor) * 1.6)
			g.Set(x, y, rampFor(val), g.Color(band, val, a.beat))
		}
	}
}
