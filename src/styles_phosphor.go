package main

// phosphor is the shared trail buffer: a per-cell intensity that fades, so a
// style can lay a curve down and keep a few of its previous positions.
//
// Three things about it are load-bearing and each was a way to get this wrong:
//
// **It decays in Push, not in Paint.** Push runs at the analysis rate (~11Hz)
// and Paint at up to 30Hz, or at `CapScale` of that when the style is heavy. A
// trail that decays per Paint is therefore three times longer on a capped style,
// so the same track looks different depending on the terminal. The trail is a
// record of the audio, so it belongs on the audio's clock -- the same rule
// waterfallViz's scroll follows, and the same one behind `TryFrame`.
//
// **It owns its own accumulation.** The render loop clears the grid before every
// Paint, correctly, because a style may draw nothing on a given frame and a
// partial paint shows the previous frame through. So nothing here can be a
// function of what the grid held last time.
//
// **Lay maxes, it does not add.** Overlapping segments of a curve accumulate
// into a white blob with additive blending, and a Lissajous crosses itself
// constantly -- so the core of the figure burns out and the crossings, which are
// the whole reason to draw one, disappear into it.
type phosphor struct {
	cols, rows int
	// decay is the fraction of intensity that survives one analysis. Small enough
	// that the trail is gone in a few hundred milliseconds, which is what a scope's
	// persistence is set to; a trail measured in seconds reads as motion blur.
	decay float64
	buf   []float64
	// bandX is bandPos(x, cols) per column: the colour's frequency argument.
	// Constant down a column and used once per lit cell, so it is precomputed
	// rather than divided in the blit loop.
	bandX []float64
}

// phosphorFloor is the intensity below which a cell is left at the background.
//
// Not zero: a trail that fades to nothing asymptotically leaves a permanent
// dither of cells one index above the background, which is exactly the "stale
// cells" shape of bug that screen_test.go exists to catch, here in a style that
// is honestly drawing.
const phosphorFloor = 0.04

func (p *phosphor) Resize(cols, rows int, decay float64) {
	p.cols, p.rows = cols, rows
	p.decay = decay
	if len(p.buf) < cols*rows {
		p.buf = make([]float64, cols*rows)
	}
	p.Clear()
	if len(p.bandX) != cols {
		p.bandX = make([]float64, cols)
		for x := range p.bandX {
			p.bandX[x] = bandPos(x, cols)
		}
	}
}

func (p *phosphor) Clear() {
	for i := range p.buf {
		p.buf[i] = 0
	}
}

// Decay fades the whole trail by one analysis step. The multiply is the entire
// cost and it is one pass over cells*rows, which is the same order as the blit
// that follows it.
func (p *phosphor) Decay() {
	for i, v := range p.buf {
		if v > 0 {
			p.buf[i] = v * p.decay
		}
	}
}

// Lay puts intensity at a cell, keeping the brighter of the new and the old.
func (p *phosphor) Lay(x, y int, v float64) {
	if x < 0 || y < 0 || x >= p.cols || y >= p.rows {
		return
	}
	if v > 1 {
		v = 1
	}
	i := y*p.cols + x
	if v > p.buf[i] {
		p.buf[i] = v
	}
}

// LayLine connects two cells, so a curve drawn sample by sample does not come
// out as a dotted line at low zoom.
//
// Integer steps with a sign comparison rather than a slope divide, because the
// slope is the same on both axes only when the step is 1 and a divide per
// segment is a divide per segment forever after.
func (p *phosphor) LayLine(x0, y0, x1, y1 int, v float64) {
	dx, dy := x1-x0, y1-y0
	step := 1
	if dy < 0 {
		step = -1
		dy = -dy
	}
	if dx < 0 {
		dx = -dx
	}
	if dx > dy {
		dy = dx
	}
	if dy == 0 {
		dy = 1
	}
	// A quarter-chord approximation: the error is under 4% and it is a multiply,
	// where math.Hypot would be a sqrt in the innermost loop of two styles.
	err := dx / 2
	x, y := x0, y0
	for i := 0; i <= dx; i++ {
		p.Lay(x, y, v)
		err -= dy
		if err < 0 {
			y += step
			err += dx
		}
		x++
	}
}

// Blit copies the trail into the grid, fading it through the ramp with age.
//
// Colours with beat 0 on purpose: the palette reads the beat argument as an
// accent, and passing the live envelope would repaint the entire trail in the
// accent colour on every onset -- the whole screen strobing, once per drum hit.
// The live trace is drawn by the style itself, with the beat, on top.
func (p *phosphor) Blit(g *VizGrid) {
	for y := 0; y < p.rows; y++ {
		row := y * p.cols
		for x := 0; x < p.cols; x++ {
			v := p.buf[row+x]
			if v <= phosphorFloor {
				continue
			}
			g.Set(x, y, rampFor(v), g.Color(p.bandX[x], v, 0))
		}
	}
}
