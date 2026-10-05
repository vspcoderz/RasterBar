package main

// --- scope -------------------------------------------------------------------

// scopeViz draws the waveform as a line across the full width.
//
// The one style that does not use the band magnitudes at all: an oscilloscope
// shows amplitude against time, which is information the FFT has already thrown
// away. It also looks the most like the thing people mean by "music visualizer",
// which is presumably why it is second in the registry rather than last.
type scopeViz struct {
	cols, rows int
	wave       []float64
	palette    palette
}

func (s *scopeViz) Name() string { return "scope" }
func (s *scopeViz) Heavy() bool  { return false }
func (s *scopeViz) CapScale(int, int) float64 {
	return 1
}

func (s *scopeViz) Resize(cols, rows int) { s.cols, s.rows = cols, rows }

// Reset drops the waveform. There is nothing to keep across a seek: the window
// holds the last 93ms of the *old* position, and drawing it after a jump would
// show a fragment of the wrong part of the song for a frame.
func (s *scopeViz) Reset() { s.wave = s.wave[:0] }

// Push keeps the latest window.
//
// Copied rather than aliased: the tap recycles its sample buffer on the next
// read, ~11 times a second, so holding the slice would draw a half-overwritten
// waveform. One copy of 1024 floats at that rate is not a cost worth avoiding.
func (s *scopeViz) Push(f *AudioFrame) {
	if len(f.Wave) == 0 {
		return
	}
	s.wave = append(s.wave[:0], f.Wave...)
}

// Paint draws the waveform as a connected line.
func (s *scopeViz) Paint(g *VizGrid) {
	n := len(s.wave)
	if n == 0 || s.cols == 0 || s.rows == 0 {
		return
	}
	mid := (s.rows - 1) / 2
	prevY := -1
	for x := 0; x < s.cols; x++ {
		// Decimate: average the samples in this column's slice, so a
		// high-frequency waveform reads as a band of noise around the centre
		// instead of as one aliased sample.
		lo := x * n / s.cols
		hi := (x + 1) * n / s.cols
		if hi <= lo {
			hi = lo + 1
		}
		if hi > n {
			hi = n
		}
		amp := clampSigned(sumRange(s.wave, lo, hi) / float64(hi-lo))
		y := mid - int(amp*float64(mid))
		if y < 0 {
			y = 0
		}
		if y >= s.rows {
			y = s.rows - 1
		}
		// Fill the gap to the previous column so the line is connected. A
		// waveform that skips rows between columns reads as separate strokes
		// rather than as a signal.
		if prevY >= 0 && y != prevY {
			step := 1
			if y < prevY {
				step = -1
			}
			for yy := prevY; yy != y; yy += step {
				g.SetRamp(x, yy, rampFor(0.85))
			}
		}
		val := clamp01(0.5 + amp*0.5)
		g.Set(x, y, rampFor(val), g.Color(bandPos(x, s.cols), val, 0))
		prevY = y
	}
}

// sumRange adds a half-open range, for the decimating averages.
func sumRange(v []float64, lo, hi int) float64 {
	var sum float64
	for i := lo; i < hi && i < len(v); i++ {
		sum += v[i]
	}
	return sum
}

// clampSigned saturates to -1..1, for a waveform sample. A NaN becomes 0 rather
// than propagating: it would reach an int conversion and index a cell with a
// garbage row.
func clampSigned(v float64) float64 {
	switch {
	case v != v:
		return 0
	case v < -1:
		return -1
	case v > 1:
		return 1
	}
	return v
}
