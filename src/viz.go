package main

import (
	"fmt"
	"strings"
	"time"
)

// The visualizer framework: what a style is, what it draws into, and how that
// becomes terminal output.
//
// The central decision here is that a visualizer paints into a neutral cell grid
// and *then* two thin painters convert that grid into the frame layouts the
// existing renderers already speak. Neither renderer is modified.
//
// That is not tidiness, it is the whole reason the design is safe. DiffRenderer
// and ColorDiffRenderer already carry the diff cache, the SGR elision and the
// bandwidth budget, and screen_test.go already proves they leave no stale cells
// over moving content. Routing visualizer output through them means that
// regression suite covers the visualizers for free. A third bespoke painter
// would mean re-deriving all of it, and the pixel-leak bug in ba35b10 is proof
// of how quietly that goes wrong.

// AudioFrame is one analysed moment: everything a style is allowed to draw from.
//
// Bands are the raw log-spaced magnitudes. Smoothing is deliberately NOT applied
// here -- it is a display concern owned by Visualizer, so switching style does
// not change what the analysis says, only how it is drawn.
type AudioFrame struct {
	Bands []float64 // 0..1 per log-spaced band, low to high
	Wave  []float64 // recent time-domain samples, -1..1, oldest first
	// Beat is the onset envelope: 1 on a transient, decaying after. Drawn rather
	// than the raw onset flag, because a bare boolean makes a strobe that is on
	// for exactly one frame and therefore invisible.
	Beat float64
	// BPM is a tempo estimate, 0 while not confident. An estimate, and the HUD
	// says so.
	BPM float64
}

// Viz is one visualizer style.
//
// Reset is separate from Resize because they happen at different times and for
// different reasons: a resize keeps the history (the bands do not care how wide
// the terminal is), while a reset is a seek or a style switch, where keeping
// history would mean drawing a spectrum of the part of the song you just left.
type Viz interface {
	// Name is what the HUD shows and what the style cycling reports.
	Name() string
	// Resize tells the style the grid it has. Cheap; called on SIGWINCH.
	Resize(cols, rows int)
	// Reset drops accumulated state: waterfall rows, particle positions.
	Reset()
	// Push folds in one analysed moment.
	Push(f *AudioFrame)
	// Paint fills the grid. Called once per frame, always over a full grid that
	// Paint is expected to cover completely.
	Paint(g *VizGrid)
	// Heavy reports that this style costs more than the others.
	//
	// Not a reason to disable it. A style that only runs on a big terminal is a
	// style most people never see, so heavy styles are capped instead: fewer
	// particles, half the frame rate.
	Heavy() bool
	// CapScale is how much this style's internal budget shrinks on a grid too
	// large to afford it. 1 means no reduction.
	CapScale(cols, rows int) float64
}

// VizGrid is the neutral cell buffer every style paints into.
//
// Gray holds a ramp *index*, not a luminance. Holding an index is what lets a
// style choose an exact glyph -- the scope wants a line, the particles want a
// dot -- instead of a smear of ramp characters, and rampLum converts it back for
// the renderer without that choice being lost. See rampLum.
type VizGrid struct {
	cols, rows int
	gray       []byte   // cols*rows ramp indices
	rgb        []uint32 // cols*rows packed 0xRRGGBB, ignored when color is off
	color      bool

	// frame is the painter's scratch, kept across frames so painting allocates
	// nothing. Sized cols*rows*pixPerCell*3.
	frame []byte
	// mono is the mono painter's scratch, same reasoning as frame.
	mono []byte
	// pixPerCell matches the renderer's glyph layout, so the two agree on where a
	// cell's second pixel lives.
	pixPerCell int
	// pal is the active colour scheme. It lives on the grid rather than on each
	// style so that switching palettes is one assignment instead of six, and so a
	// new style cannot forget to be told about it.
	pal palette
	// bg and bgRamp are what Clear() fills with, so that an unlit cell is a
	// deliberate dark panel rather than black-on-black nothing. See palette.bg.
	bg     uint32
	bgRamp byte
}

// NewVizGrid allocates a grid. pixPerCell is 2 for the half-block layout and 1
// otherwise, matching ColorDiffRenderer.
func NewVizGrid(cols, rows int, color bool, pixPerCell int) *VizGrid {
	if cols < 1 {
		cols = 1
	}
	if rows < 1 {
		rows = 1
	}
	if pixPerCell < 1 {
		pixPerCell = 1
	}
	return &VizGrid{
		cols:       cols,
		rows:       rows,
		gray:       make([]byte, cols*rows),
		rgb:        make([]uint32, cols*rows),
		color:      color,
		pixPerCell: pixPerCell,
		frame:      make([]byte, cols*rows*pixPerCell*3),
	}
}

// Clear empties the grid to its background.
//
// Every cell, on every frame. Not "whatever the style overwrites" -- styles are
// allowed to paint nothing at all this frame (a waterfall between scrolls, a
// radial whose rings all fell below the threshold), and a partial paint means the
// previous frame shows through, which reads as the visualizer being stuck.
//
// "Empties" is the background colour, not black. A sparse style on a black
// terminal otherwise produced scattered glyphs floating in nothing, with no way to
// see where the picture ended.
func (g *VizGrid) Clear() {
	gray, rgbv := g.bgRamp, g.bg
	for i := range g.gray {
		g.gray[i] = gray
		g.rgb[i] = rgbv
	}
}

// Set puts a cell. idx is a ramp index; packed is 0xRRGGBB and ignored in mono.
func (g *VizGrid) Set(x, y int, idx byte, packed uint32) {
	if x < 0 || y < 0 || x >= g.cols || y >= g.rows {
		return
	}
	i := y*g.cols + x
	g.gray[i] = idx
	g.rgb[i] = packed
}

// SetRamp is Set without a colour, for styles that only draw in mono terms.
func (g *VizGrid) SetRamp(x, y int, idx byte) { g.Set(x, y, idx, 0) }

// Color resolves a cell colour through the active palette.
//
// Returns 0 when the palette produces no colour, which is the correct answer
// rather than a sentinel: ColorFrame ignores the packed value entirely in mono,
// so a style can call this unconditionally and not branch on the colour mode.
//
// band is the position across the spectrum (0..1), val the cell's own value
// (0..1), beat the onset envelope (0..1). Which of them the palette reads is its
// own business -- that is what makes a spectrum palette and a height palette
// different palettes rather than different styles.
func (g *VizGrid) Color(band, val, beat float64) uint32 {
	if !g.color || g.pal.mono {
		return 0
	}
	return g.pal.colorFor(band, val, beat)
}

// SetPalette switches the grid's colour scheme. Called when the user presses `c`.
//
// The background is cached into the grid rather than read from the palette per
// cell, because Clear touches every cell and a palette lookup per cell per frame is
// a function call in the hottest loop in the program.
func (g *VizGrid) SetPalette(p palette) {
	g.pal = p
	g.bg = p.bgColor()
	// The mono ramp has no colour, so the background has to be a glyph. Index 1 is
	// the second-darkest character in the ramp -- just enough to be visible against
	// a black terminal without reading as content.
	g.bgRamp = 1
}

// At returns a cell's ramp index.
func (g *VizGrid) At(x, y int) byte {
	if x < 0 || y < 0 || x >= g.cols || y >= g.rows {
		return 0
	}
	return g.gray[y*g.cols+x]
}

// rampLum converts a ramp index to the luminance byte whose levelFor is that
// same index.
//
// This is the trick that lets a visualizer keep exact glyph control while still
// painting through DiffRenderer unchanged. levelFor maps b*len(ramp)/256, so
// feeding it a luminance in [idx*256/len, (idx+1)*256/len) round-trips back to
// ramp[idx]. The interval is 3.66 bytes wide at len(ramp)=70, so it is never
// empty and the smallest luminance in it always exists.
//
// The inverse, mapping a luminance back to a ramp index, would lose the
// distinction between two glyphs whose ink density is within one byte of each
// other. Going forward only costs a table lookup at paint time.
func rampLum(idx int) byte {
	if idx < 0 {
		idx = 0
	}
	if idx >= len(ramp) {
		idx = len(ramp) - 1
	}
	return rampLumTab[idx]
}

// rampLumTab is rampLum for every possible ramp index. The mapping is a pure
// division with only len(ramp) inputs, and rampLum is called once per cell per
// frame, so the division is hoisted to init.
var rampLumTab = func() [256]byte {
	var t [256]byte
	for i := range t {
		t[i] = byte((i*256 + len(ramp) - 1) / len(ramp))
	}
	return t
}()

// MonoFrame expands the grid to the byte-per-cell layout DiffRenderer expects.
//
// Reuses the same scratch buffer as the colour path. At 30fps a 200x60 grid
// would otherwise put 360KB a second of short-lived slices into the heap on a
// machine that is already the reason the source resolution is capped at 360p.
func (g *VizGrid) MonoFrame() []byte {
	n := g.cols * g.rows
	if len(g.mono) < n {
		g.mono = make([]byte, n)
	}
	out := g.mono[:n]
	for i, idx := range g.gray {
		out[i] = rampLum(int(idx))
	}
	return out
}

// vizPixPerCell maps the glyph layout to the renderer's pixel count, so the grid
// writes the frame in the layout the renderer is going to read.
func vizPixPerCell(glyph GlyphMode) int {
	if glyph == GlyphHalf {
		return 2
	}
	return 1
}

// ColorFrame expands the grid to the rgb24 layout ColorDiffRenderer expects.
//
// The layout is the one startVideoTap's filter produces, which is what the
// renderer is written against: for each cell the top pixel, then (in half-block
// mode) the bottom pixel one row down.
//
// fg == bg on purpose. The half-block glyph paints the foreground over the top
// half of the cell and the background over the bottom half, so a cell with two
// different colours would render as a hard horizontal split rather than as the
// solid colour the style asked for.
//
// A cleared cell is colour 0, black, not a space: a space would let the
// previous frame's background show through the diff, which is the stale-cell bug
// in a new place.
func (g *VizGrid) ColorFrame() []byte {
	n := g.cols * g.rows
	need := n * g.pixPerCell * 3
	if len(g.frame) < need {
		g.frame = make([]byte, need)
	}
	f := g.frame[:need]
	stride := g.cols * g.pixPerCell * 3
	for y := 0; y < g.rows; y++ {
		for x := 0; x < g.cols; x++ {
			c := g.rgb[y*g.cols+x]
			if !g.color {
				// Mono requested: drive luminance through the grey ramp so a
				// colour-blind terminal still shows the shape.
				c = monoLuma(rampLum(int(g.gray[y*g.cols+x])))
			}
			top := y*stride + x*3
			f[top], f[top+1], f[top+2] = byte(c>>16), byte(c>>8), byte(c)
			if g.pixPerCell == 2 {
				bot := top + g.cols*3
				f[bot], f[bot+1], f[bot+2] = byte(c>>16), byte(c>>8), byte(c)
			}
		}
	}
	return f
}

// monoLuma expands a grey byte to 0xRRGGBB so the colour renderer can draw the
// mono path without a second code path.
//
// One multiply rather than three assignments: 0x010101 * 255 is exactly 0xFFFFFF,
// so a grey byte becomes a neutral colour without a branch and without touching
// the components individually.
func monoLuma(b byte) uint32 { return uint32(b) * 0x010101 }

// musicFPS is how often the spectrum redraws.
//
// Budgeted in cells per second for the same reason fpsForGrid is: a 200x60 grid is
// 12000 cells against 80x22's 1760, and repainting the whole grid at 30fps is
// 360k cells/sec on a terminal that cannot absorb it.
//
// The rates are higher than video's because a visualizer frame is far cheaper to
// produce -- there is no ffmpeg decode behind it, just arithmetic over 48 bands
// -- so the budget is reached later.
func musicFPS(cols, rows int) int {
	cells := cols * rows
	switch {
	case cells <= 2500:
		return 30
	case cells <= 6000:
		return 24
	case cells <= 12000:
		return 20
	case cells <= 25000:
		return 15
	case cells <= 50000:
		return 10
	default:
		return 8
	}
}

// musicPollTimeout bounds the mpv position read in music mode.
//
// Shorter than the 400ms the drift corrector uses, because this one runs on the
// render tick: a 400ms stall here is more than a tenth of a frame budget and
// would be felt as a hitch every time mpv was slow to answer. A dropped read is
// harmless -- the clock holds its last value -- so a tight timeout costs nothing
// when it fires.
const musicPollTimeout = 60 * time.Millisecond

// vizRegistry is the style list, in cycling order.
//
// Order is cheap-first. Pressing `v` repeatedly should walk through the visual
// styles and end somewhere surprising, not run into the particle field on the
// second press.
var vizRegistry = []func() Viz{
	func() Viz { return &barsViz{} },
	func() Viz { return &scopeViz{} },
	func() Viz { return &mirrorViz{} },
	func() Viz { return &waterfallViz{} },
	func() Viz { return &radialViz{} },
	func() Viz { return &particlesViz{} },
}

// VizNames lists the registry in cycling order, for --help and the style line.
func VizNames() []string {
	out := make([]string, 0, len(vizRegistry))
	for _, mk := range vizRegistry {
		out = append(out, mk().Name())
	}
	return out
}

// VizByName finds a style, for --viz.
func VizByName(name string) (Viz, bool) {
	for _, mk := range vizRegistry {
		v := mk()
		if strings.EqualFold(v.Name(), name) {
			return v, true
		}
	}
	return nil, false
}

// vizPrefs is the style and palette chosen by the user.
//
// A pointer, and that is the whole mechanism for "remembered across tracks": the
// value outlives one trackSession and is handed to the next one, so pressing `v`
// once does not have to be repeated per song. It is not keyed by track ID. A
// cache with no evictor, keyed on a value that changes between runs, would make
// the second play of a track look different from the first, which is the kind of
// thing people report as a bug.
type vizPrefs struct {
	style   int
	palette int
}

func newVizPrefs() *vizPrefs { return &vizPrefs{} }

// clamp keeps both indices inside the registry after the list changes.
func (p *vizPrefs) clamp() {
	if p.style < 0 || p.style >= len(vizRegistry) {
		p.style = 0
	}
	if p.palette < 0 || p.palette >= len(palettes) {
		p.palette = 0
	}
}

// nextStyle advances and returns the new style index, wrapping.
func (p *vizPrefs) nextStyle() int {
	p.style = (p.style + 1) % len(vizRegistry)
	return p.style
}

// prevStyle steps back, wrapping.
func (p *vizPrefs) prevStyle() int {
	p.style = (p.style - 1 + len(vizRegistry)) % len(vizRegistry)
	return p.style
}

// nextPalette advances and returns the new palette index, wrapping.
func (p *vizPrefs) nextPalette() int {
	p.palette = (p.palette + 1) % len(palettes)
	return p.palette
}

// StyleName and PaletteName are what the HUD shows after a switch.
func (p *vizPrefs) StyleName() string {
	p.clamp()
	return vizRegistry[p.style]().Name()
}

func (p *vizPrefs) PaletteName() string {
	p.clamp()
	return palettes[p.palette].name
}

// makeViz builds the currently selected style.
func (p *vizPrefs) makeViz() Viz {
	p.clamp()
	return vizRegistry[p.style]()
}

// describeStyle is the one-line status after a switch, e.g. "viz waterfall".
//
// Carries the palette too, because after `c` it is the palette that changed and
// saying only the style would be actively misleading -- the user pressed a colour
// key and was told about a visualizer.
func describeStyle(p *vizPrefs) string {
	return fmt.Sprintf("viz %s  ·  %s", p.StyleName(), p.PaletteName())
}
