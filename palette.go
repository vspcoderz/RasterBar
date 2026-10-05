package main

import "math"

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

// palettes is the cycling order, matching what `c` walks.
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
var palettes = []palette{
	{name: "spectrum", byBand: true, bg: [3]float64{0.10, 0.03, 0.12}, fn: spectrumHue},
	{name: "height", bg: [3]float64{0.11, 0.04, 0.04}, fn: heightHue},
	{name: "ocean", byBand: true, bg: [3]float64{0.02, 0.06, 0.10}, fn: oceanHue},
	{name: "ember", byBand: true, bg: [3]float64{0.12, 0.05, 0.02}, fn: emberHue},
	{name: "mono", mono: true, bg: [3]float64{0.09, 0.09, 0.09}},
}

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
	_, g, b := hsvToRGB(hue, s, v)
	return rgb(1, g, b)
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
	_, g, b := hsvToRGB(hue, s, v)
	return rgb(1, g, b)
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
	_, g, b := hsvToRGB(hue, s, v)
	return rgb(1, g, b)
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
	_, g, b := hsvToRGB(hue, s, v)
	return rgb(1, g, b)
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

// paletteAt returns a palette by index, clamped.
func paletteAt(i int) palette {
	if i < 0 || i >= len(palettes) {
		return palettes[0]
	}
	return palettes[i]
}
