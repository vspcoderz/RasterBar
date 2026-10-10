package main

import "math"

// --- swell -------------------------------------------------------------------

// swellViz is a waterfall of the *waveform* rather than of the spectrum.
//
// waterfallViz scrolls frequency against time; this scrolls amplitude against
// time. They answer different questions, and the reason to have both is that
// the spectrum's history shows *what* is playing while this shows *how it is
// shaped*: a passage of tremolo, a fade, a drum fill and a held note are all
// shapes in the waveform and none of them is a shape in a spectrogram.
type swellViz struct {
	cols, rows int
	// row is the waveform about to be written at the top, decimated to cols.
	row  []float64
	hist []float64 // rows*cols scroll history, owned here; see Paint
	// top stores whether a row has been captured since the last Paint, for the
	// reason waterfallViz carries the same flag.
	dirty bool
}

func (s *swellViz) Name() string { return "swell" }
func (s *swellViz) Heavy() bool  { return false }
func (s *swellViz) CapScale(int, int) float64 {
	return 1
}

func (s *swellViz) Resize(cols, rows int) {
	s.cols, s.rows = cols, rows
	if len(s.row) != cols {
		s.row = make([]float64, cols)
		s.dirty = false
	}
	// Resized, not resampled, for the reason waterfallViz's history is: a scroll
	// buffer of the wrong height has no correct mapping onto a new one, and
	// pretending otherwise puts rows of the wrong moment in the wrong place.
	s.hist = make([]float64, cols*rows)
}

func (s *swellViz) Reset() {
	for i := range s.hist {
		s.hist[i] = 0
	}
	s.dirty = false
}

// Push captures the newest row.
//
// On Push, not on Paint: Push runs at the analysis rate (~11/s) and Paint at up
// to 30/s, so capturing per paint would scroll two identical rows for every real
// one and the history would move at twice the speed of the music. Measured on
// waterfallViz, same bug, same fix.
func (s *swellViz) Push(f *AudioFrame) {
	n := len(f.Wave)
	if n < 2 || s.cols < 1 {
		return
	}
	for x := 0; x < s.cols; x++ {
		lo := x * n / s.cols
		hi := (x + 1) * n / s.cols
		if hi <= lo {
			hi = lo + 1
		}
		if hi > n {
			hi = n
		}
		// Peak magnitude rather than the mean. The mean of a waveform column is
		// near zero for anything but a tone -- the columns cancel -- so a
		// mean-decimated history is a flat line with occasional spikes, which is
		// not the picture. The peak is the waveform's envelope in one number.
		peak := 0.0
		for i := lo; i < hi; i++ {
			v := math.Abs(clampSigned(f.Wave[i]))
			if v > peak {
				peak = v
			}
		}
		s.row[x] = peak
	}
	s.dirty = true
}

// Paint scrolls the history down one row and blits it.
//
// Owns its accumulation rather than reading the grid's previous contents: the
// render loop clears the grid before every Paint, correctly, because a style may
// draw nothing on a given frame and a partial paint shows the previous frame
// through. See waterfallViz.Paint for the full argument -- the same bug, the
// same answer.
func (s *swellViz) Paint(g *VizGrid) {
	if s.rows == 0 || s.cols == 0 {
		return
	}
	if s.dirty {
		for y := s.rows - 1; y > 0; y-- {
			copy(s.hist[y*s.cols:y*s.cols+s.cols], s.hist[(y-1)*s.cols:y*s.cols])
		}
		copy(s.hist[:s.cols], s.row)
		s.dirty = false
	}
	mid := float64(s.rows-1) / 2
	span := mid
	if span < 1 {
		return
	}
	for y := 0; y < s.rows; y++ {
		// Faded with age, so the direction of time is readable. Without it every
		// row is equally bright and the history is a texture rather than a
		// scroll -- the same reason waterfallViz fades.
		age := 1 - float64(y)/float64(maxInt(s.rows, 1))
		fade := 0.25 + 0.75*age
		for x := 0; x < s.cols; x++ {
			v := clamp01(s.hist[y*s.cols+x]) * fade
			if v <= 0.01 {
				continue
			}
			// Signed about the centre: a history of *magnitudes* drawn upward
			// from the top row is a bar chart over time, which waterfall already
			// is. Mirrored about the middle, it is a scope over time, which is the
			// one that reads as a shape.
			d := int(math.Round(v * span))
			cx := int(math.Round(mid))
			band := bandPos(x, s.cols)
			g.Set(x, cx-d, rampFor(v), g.Color(band, v, 0))
			g.Set(x, cx+d, rampFor(v), g.Color(band, v, 0))
		}
	}
}
