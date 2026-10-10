package main

// --- rays --------------------------------------------------------------------

// raysViz draws the spectrum as light shafts rising from the bottom edge.
//
// Bars are a chart; this is a picture of the same numbers, and the difference is
// that a shaft has no top edge. A bar ends at its level and the row above it is
// background; a shaft fades into the background over several rows, so the eye
// reads a glow rather than a quantity. Combined with the beam only reaching the
// shaft on an onset, it is the style that looks like stage lighting.
//
// The shafts are one column wide with a gap, not a continuous field: the gap is
// what makes them shafts. Drawn edge to edge they are bars with a gradient, which
// barsViz already is.
type raysViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
	// gap is the column pitch of the shafts, chosen in Resize from the width.
	gap int
	// headInk is the onset envelope, which brightens the shaft's *head* and
	// nothing else: a whole picture that flashes is a strobe, and the point is
	// that only the leading edge pops.
	headInk  float64
	beat     float64
	prevBeat float64
}

const (
	// rayGapMin is the narrowest gap allowed between shafts, in columns.
	//
	// The gap is what makes them shafts rather than bars. It is derived from the
	// *column count*, not the band count: the first version used the band count,
	// which is capped at maxDrawnBands, so at 200 columns the gap came out at 3
	// and the grid grew 67 shafts instead of the ~40 the constant asks for --
	// caught by TestRaysLeaveHeadroomAndGaps, which asserts the shaft count and
	// not just the pitch.
	rayGapMin = 2
	// rayGapMax is the widest gap. At 8 columns a one-column shaft still reads as
	// a shaft rather than as an accident, and the ceiling is what stops a 300
	// column terminal from drawing a dozen of them.
	rayGapMax = 8
	// rayShafts is how many shafts the gap is chosen to fit, at any width.
	rayShafts = 40
	// rayHeightFrac is how much of the grid a full-scale shaft reaches.
	//
	// Below 1, and that is the fix for the first version: shaft length was
	// level*rows, so every loud band ran to the very top of the grid and the
	// picture was a solid mass with a stair-stepped edge and no headroom at all.
	// Measured at 120x40 that filled 38 of 40 rows. With headroom the loudest
	// shaft stops short of the top and the shape is a shape.
	rayHeightFrac = 0.78
	// rayTailFrac is how far above its head a shaft fades to nothing, as a
	// fraction of its own length.
	//
	// Was 0.55, and that was the other half of the solid mass: a shaft's tail
	// reached more than half its own height again, so the gaps between adjacent
	// shafts were filled by their neighbours' tails. 0.22 keeps the glow visible
	// and the shafts separate.
	rayTailFrac = 0.22
	// rayHeadInk is the brightness of the shaft's leading cell.
	rayHeadInk = 0.95
	// rayBodyInk is the brightness one row further from the head, so the shaft's
	// end is a gradient rather than a cliff.
	rayBodyInk = 0.42
	// rayEpsilon keeps the tail from being zero rows for a silent band, which
	// would draw no cell at all and make a silent spectrum leave the grid empty.
	rayEpsilon = 1.0
)

func (r *raysViz) Name() string { return "rays" }
func (r *raysViz) Heavy() bool  { return false }
func (r *raysViz) CapScale(int, int) float64 {
	return 1
}

func (r *raysViz) Resize(cols, rows int) {
	r.cols, r.rows = cols, rows
	r.n = bandCountFor(cols)
	r.viz.Resize(r.n)
	if len(r.scratch) != r.n {
		r.scratch = make([]float64, r.n)
	}
	// The gap follows the *width*, so the shaft count stays near rayShafts at any
	// terminal size. Derived from cols rather than r.n: the shafts are laid out
	// across the columns, and r.n is capped at maxDrawnBands, so scaling the gap
	// by it under-spaced every grid wider than that cap.
	r.gap = cols / rayShafts
	if r.gap < rayGapMin {
		r.gap = rayGapMin
	}
	if r.gap > rayGapMax {
		r.gap = rayGapMax
	}
}

func (r *raysViz) Reset() {
	r.viz = Visualizer{level: make([]float64, r.n), peak: make([]float64, r.n)}
	r.beat, r.prevBeat, r.headInk = 0, 0, 0
}

// Push resamples to one band per shaft, feeds the smoother, and records the
// onset. See barsViz.Push for why the resample belongs on the way in.
func (r *raysViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, r.n, r.scratch)
	r.viz.Push(r.scratch)
	r.prevBeat = r.beat
	r.beat = clamp01(f.Beat)
}

func (r *raysViz) Paint(g *VizGrid) {
	if r.n == 0 || r.cols == 0 || r.rows == 0 {
		return
	}
	levels := r.viz.Level()
	// The reachable height, with headroom above it. See rayHeightFrac: a shaft
	// that reaches the top of the grid has no headroom, and a picture with no
	// headroom is a solid mass.
	usable := float64(r.rows) * rayHeightFrac
	if usable < 1 {
		usable = 1
	}
	// The head's brightness follows the *rise* of the envelope, not its level:
	// a level-following head glows continuously through a loud passage, which is
	// a glow, not a pop.
	if rise := r.beat - r.prevBeat; rise > 0 {
		r.headInk = clamp01(r.headInk + rise*2)
	} else {
		r.headInk = maxFloat(r.headInk*raysHeadFall, 0)
	}
	for i := 0; i < r.n; i++ {
		lv := clamp01(levels[i])
		// The length of a shaft is its level, plus a tail that is itself
		// proportional -- so a silent band gets one cell of shaft, not none. A
		// band drawn as literally nothing is indistinguishable from a column the
		// renderer never reached, and a quiet passage reads as a broken renderer
		// rather than as quiet music. See barsViz's axis tick for the same
		// argument.
		length := lv*usable + rayEpsilon
		if length < 1 {
			length = 1
		}
		if length > usable {
			length = usable
		}
		band := bandPos(i, r.n)
		x := i * r.gap
		if x >= r.cols {
			break
		}
		head := int(length)
		if head > r.rows {
			head = r.rows
		}
		for d := 0; d < head; d++ {
			y := r.rows - 1 - d
			// Fade with distance from the head, on a curve rather than linearly:
			// a linear fade spends half its cells below the level the ramp can
			// even show, because the bottom of the ramp is a space.
			t := 1 - float64(d)/length
			ink := rayBodyInk + (rayHeadInk-rayBodyInk)*smoothstep(t)
			if d == 0 {
				ink = maxFloat(ink, r.headInk)
			}
			g.Set(x, y, rampFor(ink), g.Color(band, t, r.headInk))
		}
		// The tail above the head, fading to the background over rayTailFrac of
		// the shaft's own length.
		tail := int(length * rayTailFrac)
		for d := head; d < head+tail && d < r.rows; d++ {
			t := 1 - float64(d-head)/maxFloat(float64(tail), 1)
			ink := rayBodyInk * 0.5 * t
			if ink <= 0.02 {
				continue
			}
			g.Set(x, r.rows-1-d, rampFor(ink), g.Color(band, t, r.headInk))
		}
	}
}

// raysHeadFall is how fast the head's flash decays per analysis. Fast: a flash
// that takes a second to fade is a level, and this is meant to be a pop.
const raysHeadFall = 0.7

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
