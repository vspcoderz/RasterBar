package main

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
	scratch    []float64 // one band per column; see Push
	row        []float64 // the row about to be written
	hist       []float64 // rows*cols scroll history, owned here; see Paint
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
		w.scratch = make([]float64, w.n)
		w.dirty = false
	}
	// The history is resized, not resampled. A scroll buffer of the wrong height
	// has no correct mapping onto a new one, and pretending otherwise puts rows of
	// the wrong song in the wrong place.
	w.hist = make([]float64, cols*rows)
}

func (w *waterfallViz) Reset() {
	w.viz = Visualizer{level: make([]float64, w.n), peak: make([]float64, w.n)}
	for i := range w.hist {
		w.hist[i] = 0
	}
	w.dirty = false
}

func (w *waterfallViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, w.n, w.scratch)
	w.viz.Push(w.scratch)
	// The row is captured on Push, not on Paint: Push happens once per analysis
	// (audio rate, ~11/s) while Paint may run at 30/s. Capturing on Paint would
	// scroll two identical rows for every real one and the history would move at
	// twice the speed of the music.
	copy(w.row, w.viz.Level())
	w.dirty = true
}

// Paint scrolls the history down and writes the newest row at the top.
//
// Owns its own accumulation rather than relying on the grid's previous contents,
// which is the whole subtlety here. The render loop clears the grid before every
// Paint -- correctly, because a style is allowed to draw nothing on a given frame
// and a partial paint shows the previous frame through -- so a waterfall that
// shifted `g` would have its history wiped by the very call that draws it.
//
// Measured with that bug in place: ten loud analyses produced exactly `cols` lit
// cells, one row, with rows 1..11 blank. It does not look like a leak and it is not
// one; it looks like a renderer that stopped updating the bottom of the screen.
//
// So the history lives in the style, in its own buffer, and Paint copies it into
// the grid. That costs one extra rows*cols*2 copy per frame against shifting in
// place -- 216k cell moves a second at 60x120 and 30fps -- and buys a style that
// does not care what the grid held before, which is the only arrangement that
// survives being driven from a render loop that clears.
func (w *waterfallViz) Paint(g *VizGrid) {
	if w.rows == 0 || w.cols == 0 {
		return
	}
	if !w.dirty {
		// No new analysis since the last paint. Repainting the same history is
		// correct and still necessary: the caller cleared the grid.
		w.blit(g)
		return
	}
	// Scroll the history down one row.
	for y := w.rows - 1; y > 0; y-- {
		copy(w.hist[y*w.cols:y*w.cols+w.cols], w.hist[(y-1)*w.cols:y*w.cols])
	}
	copy(w.hist[:w.cols], w.row)
	w.dirty = false
	w.blit(g)
}

// blit copies the style's own history into the grid, with colours recomputed
// rather than stored.
//
// Storing rgb as well would make a palette switch leave the old colours on screen
// with no way to notice, since the cell values would be unchanged and the diff
// renderer would skip every one of them. Recomputing here is what makes `c` work
// on a style that keeps a history.
func (w *waterfallViz) blit(g *VizGrid) {
	for y := 0; y < w.rows; y++ {
		// Fade with age so the scroll reads as depth: the newest row is full
		// contrast and the oldest is nearly gone. Without it a waterfall is a
		// field of equally bright rows and the direction of time is invisible.
		age := 1 - float64(y)/float64(maxInt(w.rows, 1))
		val0 := age * 0.85
		for x := 0; x < w.cols; x++ {
			v := clamp01(w.hist[y*w.cols+x]) * val0
			g.Set(x, y, rampFor(v), g.Color(bandPos(x, w.n), v, 0))
		}
	}
}
