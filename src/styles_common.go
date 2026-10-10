package main

import "math"

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

// sumRange adds a half-open range, for the decimating averages.
//
// Half-open, and clamped to the slice's length, because the callers divide by
// the width they asked for rather than the width they got: a short read at the
// end of the window must not divide by a count that includes samples that were
// never there.
func sumRange(v []float64, lo, hi int) float64 {
	var sum float64
	for i := lo; i < hi && i < len(v); i++ {
		sum += v[i]
	}
	return sum
}

// clampSigned saturates to -1..1, for a waveform sample.
//
// A NaN becomes 0 rather than propagating: it would reach an int conversion and
// index a cell with a garbage row, which in a full-screen style is a panic in
// the render loop rather than one wrong pixel.
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

// lerp, smoothstep, hash2 and vnoise are the arithmetic the styles share.
//
// They live here rather than in each style because several of them want the
// same two lines, and three copies of an easing curve is three places to change
// it when the picture looks wrong.

// lerp is linear interpolation. t is not clamped: the styles clamp at the ends
// where it matters and an extra branch per call sits in a per-cell path.
func lerp(a, b, t float64) float64 { return a + (b-a)*t }

// smoothstep is the classic 3t²-2t³ ease, clamped at both ends.
//
// The clamp is not defensive tidiness. A style that runs its field coordinate
// past 1 and out the other side gets 3-2t² > 1 here, which is a field that
// inverts instead of repeating, and it looks like a rendering fault rather than
// an arithmetic one.
func smoothstep(t float64) float64 {
	if t <= 0 {
		return 0
	}
	if t >= 1 {
		return 1
	}
	return t * t * (3 - 2*t)
}

// hash2 is a 32-bit integer hash to 0..1.
//
// Deterministic on purpose, for the reason particlesViz uses the golden angle
// instead of a random source: the same music must give the same picture, or a
// bug report is a description of a frame nobody else can reproduce.
func hash2(x, y int) float64 {
	h := uint32(x)*0x9E3779B1 ^ uint32(y)*0x85EBCA77
	h ^= h >> 15
	h *= 0x2545F491
	h ^= h >> 13
	return float64(h) / 4294967296.0
}

// vnoise is 2D value noise in 0..1: a lattice of hashes, smoothly interpolated.
//
// Not fBm and not a gradient noise with derivatives -- the styles using it want
// a soft, cheap, non-repeating drift, and four octaves of that is four times the
// hash arithmetic in a loop that already runs per cell per frame. That per-cell
// cost is why auroraViz is the registry's heavy style, and it is heavy for
// exactly this reason rather than for the sake of having one.
func vnoise(x, y float64) float64 {
	fx, fy := math.Floor(x), math.Floor(y)
	xi, yi := int(fx), int(fy)
	tx, ty := smoothstep(x-fx), smoothstep(y-fy)
	a := lerp(hash2(xi, yi), hash2(xi+1, yi), tx)
	b := lerp(hash2(xi, yi+1), hash2(xi+1, yi+1), tx)
	return lerp(a, b, ty)
}
