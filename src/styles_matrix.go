package main

// --- matrix ------------------------------------------------------------------

// matrixViz is a hardware-style LED meter: quantised dots, one column per band.
//
// Reads as an instrument rather than a chart, which is the point. Where barsViz
// draws a continuous height and therefore says "about 0.63", this draws the dot
// that is lit and says "six of eight" -- the quantisation is the information,
// not a loss of it. It is also the cheapest style in the registry: a dot is one
// Set, and the whole picture is a loop over columns with a compare per row.
type matrixViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
	// cells is how many dots tall a band can be. Capped below the row count
	// because a meter with as many steps as it has rows is not a meter; 8 is the
	// LED-matrix part of the reference, and the cap is what makes the
	// quantisation visible at all.
	cells int
}

const (
	// matrixGap is the column pitch: a lit LED, then that many empty columns.
	//
	// Without it the dots are adjacent cells, and adjacent cells with no space
	// between them are not dots. Measured at 120x40 on a full-spectrum track, in
	// colour: the lit columns merged with the unlit LEDs below them into one solid
	// rainbow slab with a stepped top edge, which reads as a filled area chart and
	// not as hardware at all. The gap is the entire difference between an LED
	// matrix and an area chart.
	//
	// One gap column, not more. At 250 columns a pitch of 2 gives 125 LEDs, which
	// is still dense enough to read as a meter; a wider pitch leaves more empty
	// column than LED.
	matrixGap = 2
	// matrixCells is the *minimum* height of the LED matrix, in dots.
	//
	// A floor, not a constant. The first version fixed it at 8 whatever the
	// terminal was, which is defensible on a 22-row grid and broken on a 45-row
	// one: measured at 120x40, eight rows of meter sat at the bottom and the
	// other thirty-two were an empty dark panel, which reads as a renderer that
	// gave up rather than as a meter.
	//
	// Still fixed *per dot*, which is the part that makes it a meter: with one dot
	// per row, every level is distinct and nothing is legible, because the eye
	// cannot compare two numbers that are one cell apart. Quantisation has to be
	// visible or there is no meter, just a bar chart with gaps.
	matrixCells = 8
	// matrixMaxCells is the ceiling. Past this the dots are too small to read as
	// separate and the column stops looking like hardware.
	matrixMaxCells = 24
	// matrixHeightPct is how much of the grid the matrix takes on a tall
	// terminal, as a percentage, before matrixMaxCells clamps it.
	//
	// A percentage rather than a fraction because this multiplies an int row count
	// and 0.7 there truncates to zero, which the compiler catches but which is a
	// needlessly confusing way to write rows*7/10.
	matrixHeightPct = 70
	// matrixGapInk is the brightness of the unlit dots of a column.
	//
	// Not zero: the point of a hardware meter is that the LEDs are *there*, dark,
	// and seeing the dark ones is what tells you which are lit. A matrix with only
	// the lit dots drawn is a bar chart with gaps.
	matrixGapInk = 0.14
)

func (m *matrixViz) Name() string { return "matrix" }
func (m *matrixViz) Heavy() bool  { return false }
func (m *matrixViz) CapScale(int, int) float64 {
	return 1
}

func (m *matrixViz) Resize(cols, rows int) {
	m.cols, m.rows = cols, rows
	m.n = bandCountFor(cols)
	m.viz.Resize(m.n)
	if len(m.scratch) != m.n {
		m.scratch = make([]float64, m.n)
	}
	// Dots scale with the grid, floored at matrixCells so a small terminal still
	// gets a meter and capped at matrixMaxCells so a tall one stays readable.
	m.cells = rows * matrixHeightPct / 100
	if m.cells < matrixCells {
		m.cells = matrixCells
	}
	if m.cells > matrixMaxCells {
		m.cells = matrixMaxCells
	}
	if m.cells > rows {
		m.cells = rows
	}
	if m.cells < 1 {
		m.cells = 1
	}
}

func (m *matrixViz) Reset() {
	m.viz = Visualizer{level: make([]float64, m.n), peak: make([]float64, m.n)}
}

// Push resamples the analyser's bands up to one per column, then feeds the
// smoother. See barsViz.Push for why the resample belongs on the way in.
func (m *matrixViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, m.n, m.scratch)
	m.viz.Push(m.scratch)
}

func (m *matrixViz) Paint(g *VizGrid) {
	if m.n == 0 || m.cols == 0 || m.cells == 0 {
		return
	}
	levels := m.viz.Level()
	peaks := m.viz.Peak()
	base := m.rows - m.cells
	// One LED every matrixGap columns. The gap is what makes these read as dots;
	// see matrixGap.
	for x := 0; x < m.cols && x < m.n; x += matrixGap {
		// Quantise with a round, not a floor. Floor puts a level of 0.99 in the
		// first dot and 1.01 in the second, so the meter jumps a whole step at
		// exactly full scale and never reads the level it is at below it. Round
		// maps 0..1 onto the nearest dot, which is the mapping that makes "six of
		// eight" mean the band is 0.69-0.81 of full.
		n := int(clamp01(levels[x])*float64(m.cells) + 0.5)
		if n > m.cells {
			n = m.cells
		}
		band := bandPos(x, m.n)
		for y := 0; y < m.cells; y++ {
			y0 := base + y
			if y < n {
				// Lit dots: brightness by height up the matrix, so the top of a
				// column is the brightest and a level is legible as a position as
				// well as a count.
				v := 0.45 + 0.55*float64(y+1)/float64(m.cells)
				g.Set(x, y0, rampFor(v), g.Color(band, v, 0))
			} else {
				// The dark LEDs, drawn rather than skipped. The point of a
				// hardware meter is that the LEDs are there, unlit, and seeing
				// which ones are dark is what tells you which are lit.
				g.Set(x, y0, rampFor(matrixGapInk), g.Color(band, 0, 0))
			}
		}
		// The cap, at the row just above the lit dots, in the brightest ramp
		// index there is. It is the hardware meter's peak indicator and it earns
		// its row: without it the meter shows where it is now and not how loud
		// the passage was.
		if p := int(clamp01(peaks[x])*float64(m.cells) + 0.5); p > n {
			if y0 := base - 1 + n; y0 >= 0 && y0 < m.rows {
				g.Set(x, y0, rampBright, g.Color(band, 1, 0))
			}
		}
	}
}
