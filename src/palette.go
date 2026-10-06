package main

import (
	"math"
	"strings"
)

// Colour palettes for the visualizers.
//
// Each palette is a pure function of (band index, cell value, beat) to a packed
// 0xRRGGBB. No state, no lookup tables, no allocation.
//
// The reason it is arithmetic rather than a palette array is the 256-colour path.
// quant256 has to quantise whatever we produce, and a hand-picked list of 256
// colours would land badly on that grid: the cube's steps are coarse enough that
// neighbouring entries of a smooth hue sweep collapse into the same index. Doing
// the interpolation in RGB and letting the terminal's palette do the last step
// means the sweep degrades gracefully instead of banding.

// palette is one named colour scheme.
type palette struct {
	name string
	// mono reports that this palette draws no colour at all, which the grid
	// treats as "use the ramp index as luminance".
	mono bool
	// byBand reports that this palette encodes frequency in its hue, so the two
	// ends of the spectrum must be distinguishable.
	//
	// Recorded rather than inferred, because the interesting property is not
	// "does the function read its band argument" -- it is whether the palette is
	// *claiming* to show you frequency. `height` deliberately does not: it colours
	// by how tall a cell is, so that the eye reads "how loud" instead of "which
	// frequency", and its endpoints are identical by design.
	byBand bool
	// bg is the colour an *unlit* cell gets. Not black.
	//
	// Pure black looked like nothing at all: the grid became a black rectangle on
	// a black terminal, and a sparse style read as scattered debris rather than as
	// a picture with a floor. A very dark tint of the palette's own hue makes the
	// visualiser a panel you can see the extent of, and it costs nothing -- these
	// cells were being painted anyway, they were just being painted black.
	//
	// Kept dark on purpose. This is a background for content, so it has to sit
	// below everything the palette draws without competing with it.
	bg [3]float64
	// fn returns 0xRRGGBB. band is 0..1 across the spectrum, val is 0..1 across
	// the cell's own range, and beat is the onset envelope 0..1.
	fn func(band, val, beat float64) uint32
}

// palettes is the cycling order, matching what `c` walks, and the digit order:
// `1` selects index 0 through `9` selecting index 8, with `0` selecting the last.
//
// Index 0 is a real colour, not the mono entry. That ordering is load-bearing and
// the first version had it backwards, which made colour mode draw a completely
// black screen: the default palette was `auto`/mono, so every cell resolved to
// black and black-on-black is invisible. Nothing errored, the HUD worked, the
// transport worked, and the grid was simply empty.
//
// Mono is still in the list, because someone on a terminal that *can* do colour
// may not want it -- flickering hues are not for everyone, and `--mono` is a
// restart. It is last so reaching it takes deliberate presses rather than being
// where you land.
//
// The black-and-white entries are *not* `mono: true`. They are palettes that
// happen to return r==g==b, which is what lets them gradient; `mono: true` draws
// no colour and cannot. See graphiteHue.
var palettes = []palette{
	{name: "spectrum", byBand: true, bg: [3]float64{0.10, 0.03, 0.12}, fn: spectrumHue},
	{name: "height", bg: [3]float64{0.11, 0.04, 0.04}, fn: heightHue},
	{name: "ocean", byBand: true, bg: [3]float64{0.02, 0.06, 0.10}, fn: oceanHue},
	{name: "ember", byBand: true, bg: [3]float64{0.12, 0.05, 0.02}, fn: emberHue},
	{name: "graphite", bg: [3]float64{0.07, 0.07, 0.07}, fn: graphiteHue},
	{name: "ink", byBand: true, bg: [3]float64{0.06, 0.06, 0.07}, fn: inkHue},
	{name: "ice", bg: [3]float64{0.04, 0.07, 0.12}, fn: iceHue},
	{name: "magma", bg: [3]float64{0.11, 0.03, 0.05}, fn: magmaHue},
	{name: "viridis", byBand: true, bg: [3]float64{0.05, 0.07, 0.08}, fn: viridisHue},
	{name: "mono", mono: true, bg: [3]float64{0.09, 0.09, 0.09}},
}

// paletteCount is the number of direct-select keys, and the reason the digit
// table in player.go is generated rather than written out: one entry here and the
// key range follows, so adding an eleventh palette cannot desync the keys.
const paletteCount = 10

// bgColor is the packed background for an unlit cell.
func (p palette) bgColor() uint32 {
	return rgb(p.bg[0], p.bg[1], p.bg[2])
}

// paletteMonoName is the entry that draws no colour.
const paletteMonoName = "mono"

// hsvToRGB is the standard conversion, written out because there is no HSV in
// the standard library and a colour wheel dependency is exactly what this project
// does not have.
//
// h is 0..1 around the wheel, s and v are 0..1. The six-case form is the
// textbook one and avoids the modulo-and-compare version's per-pixel division.
func hsvToRGB(h, s, v float64) (r, g, b float64) {
	h = h - math.Floor(h)
	i := math.Floor(h * 6)
	f := h*6 - i
	p := v * (1 - s)
	q := v * (1 - f*s)
	t := v * (1 - (1-f)*s)
	switch int(i) % 6 {
	case 0:
		return v, t, p
	case 1:
		return q, v, p
	case 2:
		return p, v, t
	case 3:
		return p, q, v
	case 4:
		return t, p, v
	default:
		return v, p, q
	}
}

// rgb packs components into 0xRRGGBB.
func rgb(r, g, b float64) uint32 {
	pack := func(v float64) uint32 {
		v = clamp01(v)
		return uint32(v*255 + 0.5)
	}
	return pack(r)<<16 | pack(g)<<8 | pack(b)
}

// spectrumHue runs the spectrum across the wheel: bass red, treble violet.
//
// hue 0.00 -> 0.78, not 0.0 -> 1.0. A full turn would put the top octave back at
// red, where it started, and the two ends of the spectrum would be the same
// colour -- so the bass and the hi-hat would be indistinguishable and there would
// be no way to tell which end of the log-spaced bands you were looking at.
// Stopping short of a full turn keeps the mapping monotonic.
func spectrumHue(band, val, beat float64) uint32 {
	hue := clamp01(band) * 0.78
	// Saturation and value ride the beat, so a transient brightens and warms the
	// whole spectrum without changing which colour each band is.
	s := 0.55 + 0.35*clamp01(val)
	v := 0.45 + 0.45*clamp01(val) + 0.2*clamp01(beat)
	r, g, b := hsvToRGB(hue, s, v)
	return rgb(r, g, b)
}

// heightHue colours by how tall the cell is, so every bar is a gradient of its
// own length and the peaks are the brightest thing on screen.
//
// band is ignored deliberately. This is the palette where the eye reads "how
// loud" rather than "which frequency", and that only works if colour does not
// also encode frequency.
func heightHue(band, val, beat float64) uint32 {
	// 0.62 -> 0.0: violet through to red as the bar rises.
	hue := 0.62 * (1 - clamp01(val))
	s := 0.85
	v := 0.30 + 0.60*clamp01(val)
	r, g, b := hsvToRGB(hue, s, v)
	return rgb(r, g, b)
}

// oceanHue is a narrow hue range with low saturation, for a dark terminal.
//
// Stays in 0.5-0.65 (cyan to blue) rather than sweeping the wheel, so a loud
// passage changes brightness and not colour. A wide-sweep palette on a dark
// background is fine until the whole spectrum hits the top of its range at once,
// which is exactly what a kick drum does.
func oceanHue(band, val, beat float64) uint32 {
	hue := 0.50 + 0.15*clamp01(band)
	s := 0.45 + 0.20*clamp01(beat)
	v := 0.30 + 0.60*clamp01(val)
	r, g, b := hsvToRGB(hue, s, v)
	return rgb(r, g, b)
}

// emberHue is the warm counterpart to ocean, for a light-ish terminal scheme.
//
// Sits at 0.02-0.12 (red to orange) and leans on value, with the beat pushing
// saturation up rather than brightness, so a transient reads as heat instead of
// as a flash.
func emberHue(band, val, beat float64) uint32 {
	hue := 0.02 + 0.10*clamp01(band)
	s := 0.60 + 0.30*clamp01(beat)
	v := 0.35 + 0.60*clamp01(val)
	r, g, b := hsvToRGB(hue, s, v)
	return rgb(r, g, b)
}

// graphiteHue is the black-and-white palette, gradient by value.
//
// Not `mono: true`. That flag means "draw no colour at all", which routes the
// cell through the grey ramp as a single flat glyph and cannot gradient -- and a
// flat grey spectrum is exactly what this palette exists to avoid. Returning
// r==g==b from fn instead gives a real luminance ramp: dark at the foot of a bar,
// white at the top, with the beat lifting it a further tenth.
//
// The +0.80 ceiling is deliberate. Pure white has no headroom, so a loud passage
// would flatten at the top and the peaks would stop reading as peaks.
func graphiteHue(band, val, beat float64) uint32 {
	g := clamp01(0.12 + 0.80*clamp01(val) + 0.10*clamp01(beat))
	return rgb(g, g, g)
}

// inkHue is the other black-and-white one: gradient by band, in pure greys.
//
// The same split as height (value) against spectrum (band), and for the same
// reason -- a reader has to be able to ask "how loud" or "which frequency" and
// get one answer. Ink answers the second: bass is black, treble is white.
//
// band drives the base grey and val only lifts it, so a quiet treble band stays
// dimmer than a loud bass one. Letting val dominate instead would make every
// palette look like graphite.
func inkHue(band, val, beat float64) uint32 {
	g := 0.10 + 0.70*clamp01(band)
	g *= 0.45 + 0.55*clamp01(val)
	g = clamp01(g + 0.06*clamp01(beat))
	return rgb(g, g, g)
}

// iceHue is a cold monochrome gradient, deep navy at rest to near-white at the
// top of a bar.
//
// Interpolated straight in RGB rather than through the wheel, because the ramp is
// a straight line in colour space and a hue sweep would put the middle of a bar
// somewhere green instead of halfway up.
func iceHue(band, val, beat float64) uint32 {
	t := clamp01(val)
	t = clamp01(t + 0.10*clamp01(beat))
	return rgb(0.25+0.70*t, 0.45+0.52*t, 0.80+0.20*t)
}

// magmaHue is the heat ramp: black through red and orange to yellow.
//
// Two linear segments meeting at t=0.5 rather than five stops from a table. Same
// reason the rest of this file interpolates: a lookup list bands badly once
// quant256 gets hold of it, and a two-segment ramp has one seam to hide instead
// of four.
func magmaHue(band, val, beat float64) uint32 {
	t := clamp01(val + 0.08*clamp01(beat))
	if t < 0.5 {
		u := t / 0.5
		return rgb(0.05+0.95*u, 0.01+0.18*u, 0.20*(1-u))
	}
	u := (t - 0.5) / 0.5
	return rgb(1, 0.19+0.76*u, 0.01*u)
}

// viridisHue is the perceptually-ordered one: dark purple through teal to
// yellow, which is the ramp people expect from a scientific plot.
//
// band pushes the hue *back* toward blue while val pushes it forward toward
// yellow, so the two axes stay separable. Driving hue from val alone would look
// identical to magma and the band axis would be wasted.
func viridisHue(band, val, beat float64) uint32 {
	hue := 0.34 + 0.24*clamp01(val) - 0.10*clamp01(band)
	s := 0.80 - 0.30*clamp01(band)
	v := 0.28 + 0.66*clamp01(val) + 0.10*clamp01(beat)
	r, g, b := hsvToRGB(hue, s, v)
	return rgb(r, g, b)
}

// colorFor asks the palette for a cell.
//
// The three inputs are normalised here rather than by each palette, so a new
// palette cannot accidentally receive an out-of-range band index and index
// something out of bounds.
func (p palette) colorFor(band, val, beat float64) uint32 {
	if p.fn == nil {
		return 0
	}
	return p.fn(clamp01(band), clamp01(val), clamp01(beat))
}

// paletteIndexByName resolves a palette name to its index, case-insensitively,
// or -1.
//
// One helper because this lookup was written inline twice in main.go -- once to
// resolve a name to an index and once to validate one -- and two copies of a name
// list is how they drift the first time a palette is added. The case-insensitive
// match is why it lives here rather than as a test helper: --palette has always
// accepted `--palette SPECTRUM`, and a refactor must not take that away.
func paletteIndexByName(name string) int {
	for i, p := range palettes {
		if strings.EqualFold(p.name, name) {
			return i
		}
	}
	return -1
}

// paletteAt returns a palette by index, clamped.
func paletteAt(i int) palette {
	if i < 0 || i >= len(palettes) {
		return palettes[0]
	}
	return palettes[i]
}
