package main

// The visualizer styles.
//
// Six of them, all painting into the same neutral grid (see viz.go). They differ
// in what state they keep and what they do with 48 numbers, not in how they
// touch the terminal -- that is the grid's job.
//
// Cheap first in the registry (viz.go), because `v` should walk through the
// readable ones and end somewhere surprising rather than land on the particle
// field on the second press.

// maxDrawnBands caps how many bands a style draws.
//
// Not the other way round. One band per column is the right answer for bars, and
// 48 bands on an 80-column terminal would leave a third of the screen empty for
// no reason; but a 200-column terminal has 152 bands' worth of width and drawing
// 48 of them looks broken. So the count follows the grid and the analysis is
// resampled to match.
const maxDrawnBands = 128

// resampleBands maps the analyser's band array onto n output bands.
//
// Two cases, and they need different rules. When shrinking, groups of adjacent
// bands are averaged, because the analyser spreads one narrow tone across
// neighbouring bins and taking only every third would drop energy and make a
// hi-hat disappear at low band counts. When growing, the nearest band is
// repeated, because interpolating a spectrum invents a peak that was never in
// the audio -- and an invented peak in a visualiser is a lie about the music.
//
// out must have room for n. The caller owns it so that painting allocates
// nothing: this runs 30 times a second.
func resampleBands(src []float64, n int, out []float64) {
	if n <= 0 || len(out) < n {
		return
	}
	if len(src) == 0 {
		for i := 0; i < n; i++ {
			out[i] = 0
		}
		return
	}
	if n >= len(src) {
		for i := 0; i < n; i++ {
			j := i * len(src) / n
			if j >= len(src) {
				j = len(src) - 1
			}
			out[i] = src[j]
		}
		return
	}
	for i := 0; i < n; i++ {
		lo := i * len(src) / n
		hi := (i + 1) * len(src) / n
		if hi <= lo {
			hi = lo + 1
		}
		if hi > len(src) {
			hi = len(src)
		}
		var sum float64
		for k := lo; k < hi; k++ {
			sum += src[k]
		}
		out[i] = sum / float64(hi-lo)
	}
}

// bandCountFor is how many bands to draw on a grid of this width.
func bandCountFor(cols int) int {
	n := cols
	if n > maxDrawnBands {
		n = maxDrawnBands
	}
	if n < 2 {
		n = 2
	}
	return n
}

// rampFor maps a value to a ramp index.
//
// Not inverted, and the loudest glyph is the *darkest* one in the string: the ramp
// runs from ' ' to '@' in order of ink density, so a high index is a cell that
// lays down a lot of ink. Loud therefore means a high index, which is why this is
// v*(len-1) and not (1-v).
func rampFor(v float64) byte {
	i := int(clamp01(v) * float64(len(ramp)-1))
	if i < 0 {
		i = 0
	}
	if i >= len(ramp) {
		i = len(ramp) - 1
	}
	return byte(i)
}

// rampBright is the heaviest ramp character, for peaks and cores.
var rampBright = byte(len(ramp) - 1)

// baselineInk is how much ink the axis line under the bars lays down.
//
// 0.10 is above the grid's background and well below anything a bar draws. The
// point is for it to be visible as a line without being readable as a value --
// there is a whole column of "this band is silent" underneath the spectrum and
// that is information, but it is not the information being displayed.
const baselineInk = 0.10

// bandPos is x/n as a 0..1 position across the spectrum, for palettes.
//
// The divisor is forced to at least 1 so a one-column grid cannot divide by zero
// on the way to a palette that would then be handed a NaN and produce a NaN
// colour, which reaches the terminal as a malformed SGR sequence.
func bandPos(i, n int) float64 {
	if n <= 1 {
		return 0
	}
	return float64(i) / float64(n-1)
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
