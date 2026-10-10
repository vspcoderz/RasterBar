package main

import "math"

// --- terrain -----------------------------------------------------------------

// terrainViz is a waterfall seen in perspective: the spectrum's history as a
// landscape receding to a horizon.
//
// The difference from waterfallViz is that this one *converges*. A flat
// waterfall is a spectrogram, honest about being a grid. Terrain scales each row
// toward a vanishing point as it recedes, so the near rows are full width and the
// far rows are narrow and dense, and the picture reads as depth: a kick is a
// cliff in the foreground, a sustained pad is a ridge in the distance.
//
// The horizon is the newest row, at the top. Newest-at-top matches waterfall, so
// time runs the same way down both, and the eye learns one scroll direction for
// the registry rather than two.
type terrainViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
	row        []float64 // the row about to be written
	hist       []float64 // rows*cols height field, owned here; see Paint
	dirty      bool
	// scaleX and offX are the per-row perspective, precomputed in Resize. Both
	// are constant down a row, and a per-cell divide for them is a per-cell
	// divide forever after -- this is the hoist the perf rules ask for, and here
	// it is what makes terrain cost the same as bars rather than three times it.
	scaleX, offX []float64
	// hScale is how tall a full-scale band is at the near edge.
	hScale float64
}

const (
	// terrainHorizon is the perspective exponent, 0 at the horizon and 1 at the
	// near edge.
	//
	// 1.6, not 1: a linear scale makes the far half of the landscape a flat
	// stripe of identical rows, which is a waterfall with extra steps. Above
	// about 2 the near rows get so wide that the resolution of the spectrum is
	// spent on the foreground and the history behind it is mush.
	terrainHorizon = 1.6
	// terrainNearFrac is the near edge's width as a fraction of the full width.
	// Below 1, so the near edge is inset and the landscape has a border.
	terrainNearFrac = 0.98
	// terrainHeightFrac is a full-scale band's height as a fraction of the rows,
	// at the near edge. Less than 1 so the tallest terrain never touches the row
	// above it, which is the next row's terrain.
	terrainHeightFrac = 0.55
)

func (t *terrainViz) Name() string { return "terrain" }
func (t *terrainViz) Heavy() bool  { return false }
func (t *terrainViz) CapScale(int, int) float64 {
	return 1
}

func (t *terrainViz) Resize(cols, rows int) {
	t.cols, t.rows = cols, rows
	// One band per column, as waterfallViz: this is an image of the spectrum over
	// time and it is supposed to cover the grid, so maxDrawnBands does not apply.
	t.n = cols
	if t.n < 2 {
		t.n = 2
	}
	t.viz.Resize(t.n)
	if len(t.row) != t.n {
		t.row = make([]float64, t.n)
		t.scratch = make([]float64, t.n)
		t.dirty = false
	}
	t.hist = make([]float64, cols*rows)
	t.scaleX = make([]float64, rows)
	t.offX = make([]float64, rows)
	for y := 0; y < rows; y++ {
		// Row 0 is the horizon and row rows-1 is the near edge, so the depth runs
		// 0..1 down the grid.
		d := safeDiv(float64(y), float64(maxInt(rows-1, 1)), 0)
		p := math.Pow(d, terrainHorizon)
		t.scaleX[y] = terrainNearFrac * p
		// Centred, so the convergence is symmetric and the vanishing point is the
		// middle column.
		t.offX[y] = (1 - t.scaleX[y]) / 2
	}
	t.hScale = float64(rows) * terrainHeightFrac
}

func (t *terrainViz) Reset() {
	t.viz = Visualizer{level: make([]float64, t.n), peak: make([]float64, t.n)}
	for i := range t.hist {
		t.hist[i] = 0
	}
	t.dirty = false
}

// Push captures the newest row at the analysis rate. See waterfallViz.Push:
// capturing per paint scrolls two identical rows for every real one and the
// history moves at twice the speed of the music.
func (t *terrainViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, t.n, t.scratch)
	t.viz.Push(t.scratch)
	copy(t.row, t.viz.Level())
	t.dirty = true
}

// Paint scrolls the history and draws the terrain.
//
// Owns its accumulation rather than reading the grid: the render loop clears the
// grid before every Paint, so a style that shifted the grid's own contents would
// have its history wiped by the call that draws it. See waterfallViz.Paint.
func (t *terrainViz) Paint(g *VizGrid) {
	if t.rows == 0 || t.cols == 0 {
		return
	}
	if t.dirty {
		for y := t.rows - 1; y > 0; y-- {
			copy(t.hist[y*t.cols:y*t.cols+t.cols], t.hist[(y-1)*t.cols:y*t.cols])
		}
		copy(t.hist[:t.cols], t.row)
		t.dirty = false
	}
	// Painted back to front: a near row must overwrite a far row where they
	// overlap, and near rows are the ones with height. Painting front to back
	// would leave the far rows drawn over the tops of the near ones, which reads
	// as the landscape being made of glass.
	for y := t.rows - 1; y >= 0; y-- {
		sx, ox := t.scaleX[y], t.offX[y]
		// Height shrinks with the row's own perspective, so a distant ridge is
		// short: the same number of cells that is a cliff in the foreground is a
		// bump at the horizon, and that is the entire depth cue.
		hs := t.hScale * (0.25 + 0.75*sx)
		// The row's drawn width, and where it starts.
		//
		// Sampled per *destination* column rather than stepping the source. A far
		// row covers fewer columns than the grid, and stepping the source along it
		// would leave a gap every other column -- holes in the middle of the
		// landscape, which read as dropped frames rather than as distance.
		// Sampling the destination means the far rows are dense and narrow.
		w := int(sx * float64(t.cols))
		if w < 1 {
			w = 1
		}
		x0 := int(ox * float64(t.cols))
		for xd := 0; xd < w; xd++ {
			x := x0 + xd
			if x < 0 || x >= t.cols {
				continue
			}
			// Average the source bands this destination cell covers, which is the
			// shrink case of resampleBands and is there for the same reason: the
			// analyser spreads one narrow tone across neighbouring bands, and
			// taking only one of them drops energy -- so a far row would lose the
			// notes the near rows show.
			i0 := xd * t.n / w
			i1 := (xd + 1) * t.n / w
			if i1 <= i0 {
				i1 = i0 + 1
			}
			if i1 > t.n {
				i1 = t.n
			}
			v := 0.0
			for i := i0; i < i1; i++ {
				v += t.hist[y*t.cols+i]
			}
			v = clamp01(v / float64(i1-i0))
			if v <= 0.01 {
				continue
			}
			h := int(v * hs)
			if h < 1 {
				h = 1
			}
			band := bandPos(i0, t.n)
			for k := 0; k < h; k++ {
				y0 := y - k
				if y0 < 0 {
					break
				}
				// Brighter at the top of each column, dimmer at its foot: a lit
				// surface. With a flat brightness per column the terrain is a bar
				// chart in perspective, which is what it looks like without this.
				tt := 1 - float64(k)/float64(h+1)
				vv := (0.4 + 0.6*tt) * (0.5 + 0.5*v)
				g.Set(x, y0, rampFor(vv), g.Color(band, tt*v, 0))
			}
		}
	}
}
