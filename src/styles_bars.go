package main

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

	// rowVal and rowRamp cache the x-independent part of a bar cell: how far up
	// the bar a row sits. Recomputed once per row rather than once per cell,
	// because the gradient along a bar's length does not depend on the column.
	rowVal  []float64
	rowRamp []byte
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
	if len(b.rowVal) < rows {
		b.rowVal = make([]float64, rows)
		b.rowRamp = make([]byte, rows)
	}
}

// Reset clears the peaks, because a peak from before a seek is a peak from a
// different part of the song.
func (b *barsViz) Reset() {
	b.viz = Visualizer{level: make([]float64, b.n), peak: make([]float64, b.n)}
}

// Push resamples the analyser's bands up to one per column, then feeds the
// smoother.
//
// The resample has to happen here and not in Paint. The Visualizer is sized to the
// drawn column count, Push writes as many entries as the analyser produced, and
// without this the tail columns keep their zero initial value forever: on an
// 80-column terminal the analyser fills 48 of them and the right-hand third of the
// screen is permanently blank.
//
// Doing it on the way in rather than on the way out also means the attack, the
// release and the peak hold are computed per real band. Resampling afterwards
// would smear one band's hold across the two columns that share it, and the caps
// would stop lining up with the bars.
func (b *barsViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, b.n, b.scratch)
	b.viz.Push(b.scratch)
}

func (b *barsViz) Paint(g *VizGrid) {
	if b.n == 0 || b.cols == 0 || b.rows == 0 {
		return
	}
	levels := b.viz.Level()

	// Cells available for a bar's height. One row is reserved so the peak cap
	// always has somewhere to sit above the level, which is the whole point of
	// having it: a cap drawn on top of the bar is indistinguishable from a taller
	// bar.
	height := b.rows - 1
	if height < 1 {
		height = 1
	}

	// The height-dependent part of a cell, once per row.
	for y := 0; y < height; y++ {
		v := safeDiv(float64(height-y), float64(height), 0)
		b.rowVal[y] = v
		b.rowRamp[y] = rampFor(v)
	}

	for x := 0; x < b.cols && x < b.n; x++ {
		lv := clamp01(levels[x])
		h := int(lv * float64(height))
		if h > height {
			h = height
		}
		band := bandPos(x, b.n)

		// The axis, drawn before the bar so the bar overwrites it.
		//
		// A faint tick under every band gives the picture a floor and a reference.
		// Without one, a silent band is indistinguishable from a column that was
		// never drawn at all, so a quiet passage reads as a broken renderer rather
		// than as quiet music. One row of the lowest-contrast ink available, so it
		// looks like a line and not like data.
		g.Set(x, b.rows-1, rampFor(baselineInk), g.Color(band, baselineInk, 0))

		for y := 0; y < h; y++ {
			// Row 0 is the top of the grid, so the gradient runs from bright at the
			// top of the bar down to dimmer at the base. Brightness by height,
			// not by time: a bar that fades along its own length reads as depth,
			// which is not what it is showing.
			g.Set(x, b.rows-1-y, b.rowRamp[y], g.Color(band, b.rowVal[y], 0))
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
