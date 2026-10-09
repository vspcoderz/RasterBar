package main

import (
	"math"
	"testing"
)

// --- palettes ----------------------------------------------------------------

// TestPaletteEndpointsDiffer pins the two ends of every hue sweep that claims to
// encode frequency.
//
// If the bass and the treble map to the same colour, a palette that says it is
// showing you the spectrum is showing you nothing: there is no way to tell which
// end of the log-spaced bands you are looking at.
func TestPaletteEndpointsDiffer(t *testing.T) {
	byBand := 0
	for _, p := range palettes {
		if !p.byBand {
			continue
		}
		byBand++
		lo := p.fn(0, 0.5, 0)
		hi := p.fn(1, 0.5, 0)
		if lo == hi {
			t.Errorf("palette %q claims to encode band but band 0 and band 1 are both %06x", p.name, lo)
		}
	}
	if byBand == 0 {
		t.Error("no palette encodes band; the byBand flag is not being maintained")
	}
}

// TestHeightPaletteIgnoresBand is the other half of that contract, and the
// omission is deliberate.
//
// `height` colours by cell height so the eye reads loudness. If it also swept the
// hue across the spectrum it would be encoding two things at once and neither
// would be readable.

// TestHeightPaletteIgnoresBand is the other half of that contract, and the
// omission is deliberate.
//
// `height` colours by cell height so the eye reads loudness. If it also swept the
// hue across the spectrum it would be encoding two things at once and neither
// would be readable.
func TestHeightPaletteIgnoresBand(t *testing.T) {
	h := paletteByName("height")
	if h.fn == nil {
		t.Fatal("the height palette has no function")
	}
	if h.byBand {
		t.Error("height is marked as encoding band; it deliberately does not")
	}
	if h.fn(0, 0.5, 0) != h.fn(1, 0.5, 0) {
		t.Error("height palette varies with band; it should vary with cell height")
	}
	if h.fn(0.5, 0.1, 0) == h.fn(0.5, 0.9, 0) {
		t.Error("height palette does not respond to the cell value at all")
	}
}

func paletteByName(name string) palette {
	for _, p := range palettes {
		if p.name == name {
			return p
		}
	}
	return palette{}
}

// TestHuePalettesUseTheConvertedRedComponent is the regression test for four
// palettes that threw away hsvToRGB's red and pinned it to 1:
//
//	_, g, b := hsvToRGB(hue, s, v); return rgb(1, g, b)
//
// For any hue past the red-to-yellow arc, hsvToRGB's red is the low `p` term, so
// forcing it to 1 sends every one of those hues to full red. The visible
// damage was that `ocean`, which claims to be "narrow cyan to blue", rendered
// pink; `viridis` did it correctly and proved the intent.
func TestHuePalettesUseTheConvertedRedComponent(t *testing.T) {
	blueOverRed := []struct {
		name string
		band float64
	}{
		{"ocean", 0}, {"ocean", 0.25}, {"ocean", 0.5}, {"ocean", 0.75}, {"ocean", 1},
		{"spectrum", 1},
	}
	for _, c := range blueOverRed {
		got := paletteByName(c.name).colorFor(c.band, 0.8, 0)
		r, b := got>>16&0xff, got&0xff
		if b <= r {
			t.Errorf("%s at band %.2f is %06x; blue (%d) must dominate red (%d)",
				c.name, c.band, got, b, r)
		}
	}
	// ember is the warm counterpart and runs the other way: red must dominate.
	for _, band := range []float64{0, 0.5, 1} {
		got := paletteByName("ember").colorFor(band, 0.8, 0)
		if got>>16&0xff <= got&0xff {
			t.Errorf("ember at band %.2f is %06x; red must dominate blue", band, got)
		}
	}
	// height runs violet (low) to red (high) in the cell value, so its low end
	// is the violet one.
	if got := paletteByName("height").colorFor(0.5, 0, 0); got&0xff <= got>>16&0xff {
		t.Errorf("height at its low end is %06x; that end of the ramp is violet", got)
	}
}

func TestHsvToRGBEndpoints(t *testing.T) {
	// Zero saturation is a grey regardless of hue, which is the property the
	// one-pixel colour path relies on when it has nothing to say with colour.
	for _, h := range []float64{0, 0.25, 0.5, 0.75, 1.0} {
		r, g, b := hsvToRGB(h, 0, 0.5)
		if math.Abs(r-g) > 1e-9 || math.Abs(g-b) > 1e-9 {
			t.Errorf("hsvToRGB(%v, 0, 0.5) = (%v,%v,%v), want a grey", h, r, g, b)
		}
	}
	// Full saturation, full value at hue 0 is red.
	r, g, b := hsvToRGB(0, 1, 1)
	if math.Abs(r-1) > 1e-9 || math.Abs(g) > 1e-9 || math.Abs(b) > 1e-9 {
		t.Errorf("hsvToRGB(0,1,1) = (%v,%v,%v), want (1,0,0)", r, g, b)
	}
}

func TestRGBClampsComponents(t *testing.T) {
	// Out-of-range components must not wrap. A wrapped byte produces a colour
	// nobody chose, and there is nothing to catch it downstream.
	got := rgb(2, -1, 0.5)
	if got>>16 != 0xff {
		t.Errorf("red overflowed: %06x", got)
	}
	if (got>>8)&0xff != 0 {
		t.Errorf("green underflowed: %06x", got)
	}
}

func TestPaletteAtClampsIndex(t *testing.T) {
	if paletteAt(-1).name != palettes[0].name {
		t.Error("paletteAt(-1) should be the first palette")
	}
	if paletteAt(999).name != palettes[0].name {
		t.Error("paletteAt(999) should be the first palette, not a panic")
	}
}

// TestDefaultPaletteIsNotMono is the regression test for a black screen.
//
// The palette list originally led with the mono entry, so every cell resolved to
// colour 0 and colour mode drew nothing at all: no error, working transport,
// working HUD, an empty grid. Black-on-black is not a failure any assertion about
// bytes would have caught, which is why this is pinned here.

// TestDefaultPaletteIsNotMono is the regression test for a black screen.
//
// The palette list originally led with the mono entry, so every cell resolved to
// colour 0 and colour mode drew nothing at all: no error, working transport,
// working HUD, an empty grid. Black-on-black is not a failure any assertion about
// bytes would have caught, which is why this is pinned here.
func TestDefaultPaletteIsNotMono(t *testing.T) {
	if palettes[0].mono {
		t.Errorf("palette 0 is %q, which draws no colour", palettes[0].name)
	}
	if palettes[0].fn == nil {
		t.Error("the default palette produces no colour at all")
	}
	// And it must actually produce ink for a mid-range cell.
	if got := palettes[0].fn(0.5, 0.5, 0); got == 0 {
		t.Error("the default palette returns black for a mid-range cell")
	}
}

// TestMonoPaletteProducesNoColour is what makes `c` safe to press.
//
// The mono entry draws through the grey ramp rather than a hue, so cycling colour
// on a terminal that cannot do colour -- or on one where the user does not want it
// -- leaves the visualizer readable instead of turning it into a black rectangle.

// TestBaselineIsAboveTheBackground pins the axis at the contrast it claims.
//
// baselineInk is documented as "above the background, well below anything a bar
// draws". If it ever drops to the background's own value the axis disappears, which
// is the bug this was added to fix.

// TestEveryPaletteGradients is the requirement, asserted.
//
// Every palette has to be a gradient, and the black-and-white ones especially --
// "black and white with a gradient" and "no colour at all" are different things,
// and the second is what the `mono` flag already was. A palette that returned a
// constant would pass every other test in this file: right length, in-range
// values, no control characters. It would just be the flat thing the ten-palette
// exercise exists to replace.
//
// Asserted as variation across val rather than as strictly rising luminance.
// Rising luminance is what most of these do, but spectrum also raises saturation
// with val, and for some hues that lowers luminance -- so a monotonicity
// assertion would encode a property the palettes do not actually promise.
func TestEveryPaletteGradients(t *testing.T) {
	const steps = 20
	for _, p := range palettes {
		if p.mono {
			// Draws no colour, so it cannot gradient. Reaching it needs a
			// deliberate press rather than being the default; see palettes.
			if p.fn != nil {
				t.Errorf("palette %q is mono but has an fn", p.name)
			}
			continue
		}
		if p.fn == nil {
			t.Errorf("palette %q has no fn and is not mono, so every cell resolves to black", p.name)
			continue
		}
		distinct := map[uint32]bool{}
		for i := 0; i <= steps; i++ {
			distinct[p.colorFor(0.5, float64(i)/steps, 0)] = true
		}
		if len(distinct) < steps/2 {
			t.Errorf("palette %q produced %d distinct colours across %d levels; "+
				"that is a flat fill, not a gradient", p.name, len(distinct), steps+1)
		}
	}
}

// TestGreyscalePalettesAreNotMono is the distinction the request turned on.
//
// graphite and ink return r==g==b, which is how they get a real luminance ramp.
// The `mono` flag means the opposite -- no colour at all, routed through the grey
// ramp as one flat glyph. Reaching for `mono` to get black and white would have
// produced the flat thing instead of the gradient.

// TestGreyscalePalettesAreNotMono is the distinction the request turned on.
//
// graphite and ink return r==g==b, which is how they get a real luminance ramp.
// The `mono` flag means the opposite -- no colour at all, routed through the grey
// ramp as one flat glyph. Reaching for `mono` to get black and white would have
// produced the flat thing instead of the gradient.
func TestGreyscalePalettesAreNotMono(t *testing.T) {
	for _, name := range []string{"graphite", "ink"} {
		p := paletteByName(name)
		if p.name == "" {
			t.Fatalf("palette %q is missing", name)
		}
		if p.mono {
			t.Errorf("palette %q is mono, so it cannot gradient", name)
		}
		if p.fn == nil {
			t.Fatalf("palette %q has no fn", name)
		}
		// Pick a mid value and insist the three channels agree.
		c := p.colorFor(0.5, 0.5, 0)
		r, g, b := c>>16&0xff, c>>8&0xff, c&0xff
		if r != g || g != b {
			t.Errorf("palette %q returned %d,%d,%d; wanted r==g==b", name, r, g, b)
		}
	}
}

// TestPalettesTableInvariants pins the assumptions the digit keys depend on.

// TestPalettesTableInvariants pins the assumptions the digit keys depend on.
func TestPalettesTableInvariants(t *testing.T) {
	if len(palettes) != paletteCount {
		t.Errorf("len(palettes) = %d, paletteCount = %d; the digit table would desync",
			len(palettes), paletteCount)
	}
	if palettes[0].mono {
		t.Errorf("palettes[0] is %q and mono; a mono default draws black on black, "+
			"which is invisible and looks like an empty grid", palettes[0].name)
	}
	if !palettes[len(palettes)-1].mono {
		t.Errorf("palettes[%d] is %q and not mono; mono is meant to be reachable "+
			"only by deliberate presses", len(palettes)-1, palettes[len(palettes)-1].name)
	}
	seen := map[string]bool{}
	for i, p := range palettes {
		if p.name == "" {
			t.Errorf("palette %d has no name, so --palette cannot select it", i)
		}
		if seen[p.name] {
			t.Errorf("palette name %q is duplicated at %d", p.name, i)
		}
		seen[p.name] = true
	}
}

// TestChromeRowsForMatchesTheHud is the coupling that would otherwise be a pair of
// constants drifting apart.
