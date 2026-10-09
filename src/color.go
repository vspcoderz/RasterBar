package main

import (
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"
)

// Colour support for the ASCII renderer.
//
// Two things make this hard, and both were learned the hard way:
//
//  1. Bandwidth. A naive per-cell 24-bit sequence (\x1b[38;2;R;G;Bm) is ~19 bytes
//     per cell. At a 200x57 grid that is ~136KB per frame, which would bring
//     back the exact lag the diff renderer fixed. So: quantise to the xterm
//     256-colour palette when truecolor is unavailable, skip SGR when the colour
//     is unchanged from the previous cell, and reposition the cursor only when
//     the write is not contiguous with the last one.
//
//  2. Cell shape. A character cell is about twice as tall as it is wide, so
//     square pixels look stretched. The half-block character (U+2580) solves
//     this for free: set the foreground to the top pixel's colour and the
//     background to the bottom pixel's, then print one glyph. Two pixels per
//     cell, so vertical resolution doubles at no extra cost.
const halfBlock = '▀'

// ColorMode says how much colour the terminal can take.
type ColorMode int

const (
	ColorNone ColorMode = iota
	Color256
	ColorTrue
)

// detectColor inspects the environment. Deliberately conservative: emitting
// 24-bit escapes to a terminal that does not understand them produces garbage,
// so unknown terminals get no colour rather than broken output.
//
// COLORTERM=truecolor (or 24bit) is the standard signal; a 256colour TERM gets
// the 256-colour palette.
func detectColor(env []string) ColorMode {
	term, colorterm := "", ""
	for _, kv := range env {
		switch {
		case strings.HasPrefix(kv, "TERM="):
			term = kv[5:]
		case strings.HasPrefix(kv, "COLORTERM="):
			colorterm = kv[10:]
		}
	}
	ct := strings.ToLower(colorterm)
	if ct == "truecolor" || ct == "24bit" {
		return ColorTrue
	}
	// Only advertise on TERM values that actually mean it. Guessing truecolor
	// for an unrecognised terminal produces garbage escapes, so the fallback is
	// no colour at all.
	t := strings.ToLower(term)
	if strings.Contains(t, "truecolor") || strings.Contains(t, "24bit") {
		return ColorTrue
	}
	if strings.Contains(t, "256color") || strings.Contains(t, "kitty") {
		return Color256
	}
	return ColorNone
}

// quant256 maps 24-bit RGB to an xterm-256 palette index using the standard
// 6x6x6 colour cube with a 24-step grey ramp.
//
// The greys matter more than they look: ASCII video is full of near-neutral
// content, and the colour cube's darkest entry (16) is not actually black, so
// without the grey ramp mid-greys get a visible colour cast.
//
// But pure black and pure white are not greys and must not go to the ramp. The
// ramp starts at index 232, which is #080808 — not black. Sending true black there
// means every "black" cell on a 256-colour terminal is a faint grey box, which
// against the terminal's own black background reads as a grid of smudges rather
// than as empty cells. Index 16 is #000000 and 231 is #ffffff in the standard
// palette, so the exact endpoints go there and everything between takes the ramp.
//
// This was invisible for the entire life of the colour leak tests: the emulator in
// screen_test.go ignored SGR 38/48 outright, so every colour compared as 0, and
// leakFrames' final frame is entirely black — black matched black, 216 cells a
// time, and the suite went green without ever reading a colour. See sgr.
func quant256(r, g, b byte) int {
	if r == 0 && g == 0 && b == 0 {
		return 16 // #000000, not the ramp's #080808
	}
	if r == 255 && g == 255 && b == 255 {
		return 231 // #ffffff, not the ramp's #eeeeee
	}
	if absDiff(r, g) < 12 && absDiff(g, b) < 12 && absDiff(r, b) < 12 {
		avg := (int(r) + int(g) + int(b)) / 3
		return 232 + avg*23/255
	}
	ri := int(r) * 5 / 255
	gi := int(g) * 5 / 255
	bi := int(b) * 5 / 255
	return 16 + 36*ri + 6*gi + bi
}

func absDiff(a, b byte) int {
	if a > b {
		return int(a - b)
	}
	return int(b - a)
}

// ColorDiffRenderer paints RGB frames as half-block cells, repainting only
// what changed.
//
// frame layout: cols * rows * 2 pixels, 3 bytes each (rgb24), row-major, top
// pixel of a cell stored immediately before its bottom pixel. That ordering is
// what the sync player's ffmpeg filter produces.
type ColorDiffRenderer struct {
	w         io.Writer
	cols      int
	rows      int
	mode      ColorMode
	truecolor bool
	half      bool // GlyphHalf: two pixels per cell, else one

	prevTop []uint32 // packed 0xRRGGBB per cell
	prevBot []uint32
	first   bool

	lastX, lastY int
	haveLast     bool

	// SGR dedup state, deliberately separate from haveLast. haveLast is about
	// cursor contiguity and is cleared at the end of every row; the emitted SGR
	// survives that reset, so the dedup key is kept separately or the first
	// changed cell of each row re-emits a sequence that is still active.
	sgrFG, sgrBG uint32
	sgrValid     bool

	// esc is the reusable escape-sequence scratch for emitColor, so the hot path
	// writes no garbage. fmt.Sprintf per changed cell was the dominant allocator
	// in a full-frame repaint.
	esc []byte
}

// newColorRenderer picks the colour renderer with the resolved glyph layout.
func newColorRenderer(w io.Writer, cols, rows int, mode ColorMode, glyph GlyphMode) *ColorDiffRenderer {
	return NewColorDiffRenderer(w, cols, rows, mode, glyph)
}

func NewColorDiffRenderer(w io.Writer, cols, rows int, mode ColorMode, glyph GlyphMode) *ColorDiffRenderer {
	n := cols * rows
	return &ColorDiffRenderer{
		w:         w,
		cols:      cols,
		rows:      rows,
		mode:      mode,
		truecolor: mode == ColorTrue,
		half:      glyph == GlyphHalf,
		prevTop:   make([]uint32, n),
		prevBot:   make([]uint32, n),
		first:     true,
		esc:       make([]byte, 0, 64),
	}
}

func packRGB(r, g, b byte) uint32 {
	return uint32(r)<<16 | uint32(g)<<8 | uint32(b)
}

// pixPerCell is the vertical pixel count each cell holds.
func (c *ColorDiffRenderer) pixPerCell() int {
	if c.half {
		return 2
	}
	return 1
}

// cellBytes is the frame size the colour path needs.
func (c *ColorDiffRenderer) cellBytes() int {
	return c.cols * c.rows * c.pixPerCell() * 3
}

func (c *ColorDiffRenderer) Draw(frame []byte) error {
	if len(frame) < c.cellBytes() {
		return fmt.Errorf("frame too small: %d bytes, want %d", len(frame), c.cellBytes())
	}

	if c.first {
		if _, err := io.WriteString(c.w, "\x1b[H\x1b[2J"); err != nil {
			return err
		}
		c.first = false
		c.haveLast = false
		c.sgrValid = false
		// Seed the previous buffers with an impossible value. They start as
		// zeros, which is a legitimate colour, so a first frame that happened to
		// be all-black would diff as "unchanged" and never be drawn.
		for i := range c.prevTop {
			c.prevTop[i] = ^uint32(0)
			c.prevBot[i] = ^uint32(0)
		}
	} else {
		// Reset SGR so a stale background colour cannot bleed into a cell whose
		// previous frame painted one. That reset invalidates the dedup key.
		if _, err := io.WriteString(c.w, "\x1b[0m"); err != nil {
			return err
		}
		c.sgrValid = false
	}

	// In one-pixel mode each cell is a space with a background colour: U+0020
	// is unambiguously one column wide, so the grid cannot drift.
	perCell := c.pixPerCell()
	stride := c.cols * perCell * 3
	glyph := string(halfBlock)
	if !c.half {
		glyph = " "
	}

	for y := 0; y < c.rows; y++ {
		for x := 0; x < c.cols; x++ {
			idx := y*c.cols + x

			topOff := y*stride + x*3
			botOff := topOff
			if perCell == 2 {
				botOff = topOff + c.cols*3
			}
			top := packRGB(frame[topOff], frame[topOff+1], frame[topOff+2])
			bot := packRGB(frame[botOff], frame[botOff+1], frame[botOff+2])

			if top == c.prevTop[idx] && bot == c.prevBot[idx] {
				continue
			}
			c.prevTop[idx] = top
			c.prevBot[idx] = bot

			// Move the cursor only when this cell does not follow the last one
			// written. Contiguous runs need no positioning at all.
			if !c.haveLast || y != c.lastY || x != c.lastX+1 {
				// appendCUP, not fmt.Fprintf. Both are zero-alloc for this shape
				// (measured), so this is not a leak fix — it is that AGENTS.md
				// forbids fmt in a per-cell path and render.go already grew the
				// scratch that does it properly. Two ways to build the same escape
				// is how the mono path ends up with an optimisation the colour path
				// never got.
				c.esc = appendCUP(c.esc[:0], y+1, x+1)
				if _, err := c.w.Write(c.esc); err != nil {
					return err
				}
			}

			if err := c.emitColor(top, bot); err != nil {
				return err
			}
			if _, err := io.WriteString(c.w, glyph); err != nil {
				return err
			}

			c.lastX, c.lastY = x, y
			c.haveLast = true
		}
		// No newline here, deliberately. A terminal on the last row turns LF
		// into a scroll, and a scroll moves every cell the diff cache believes
		// is still where it left it -- after that the cache and the terminal
		// disagree permanently, so every unchanged cell is skipped and keeps
		// showing what scrolled into its place. That is a ghost that no later
		// frame can clear.
		//
		// The cursor does not need the newline: haveLast is cleared below, so
		// the first cell of the next row emits an absolute CUP, and a run that
		// ends mid-row is repositioned by the same check. The old comment
		// claimed the newline avoided leaving the cursor mid-row, but it also
		// admitted the cursor is repositioned explicitly anyway -- it was risk
		// with no owner.
		c.haveLast = false
	}
	return nil
}

// newColorRenderer is the shared constructor used by runASCII.

// emitColor writes the SGR sequence for a cell, skipping it when neither colour
// changed since the previous cell. This is the main bandwidth saving.
func (c *ColorDiffRenderer) emitColor(top, bot uint32) error {
	if c.sgrValid && top == c.sgrFG && bot == c.sgrBG {
		return nil
	}
	b := c.esc[:0]
	if c.truecolor {
		b = append(b, "\x1b[38;2;"...)
		b = strconv.AppendInt(b, int64(top>>16&0xff), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(top>>8&0xff), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(top&0xff), 10)
		b = append(b, ";48;2;"...)
		b = strconv.AppendInt(b, int64(bot>>16&0xff), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(bot>>8&0xff), 10)
		b = append(b, ';')
		b = strconv.AppendInt(b, int64(bot&0xff), 10)
		b = append(b, 'm')
	} else {
		b = append(b, "\x1b[38;5;"...)
		b = strconv.AppendInt(b, int64(quant256(byte(top>>16), byte(top>>8), byte(top))), 10)
		b = append(b, ";48;5;"...)
		b = strconv.AppendInt(b, int64(quant256(byte(bot>>16), byte(bot>>8), byte(bot))), 10)
		b = append(b, 'm')
	}
	c.esc = b
	if _, err := c.w.Write(b); err != nil {
		return err
	}
	c.sgrFG, c.sgrBG, c.sgrValid = top, bot, true
	return nil
}

// ForceNext makes the next Draw repaint everything.
func (c *ColorDiffRenderer) ForceNext() { c.first = true; c.haveLast = false }

// Overlay paints lines over the bottom of the grid in reverse video.
//
// Reverse video rather than an explicit background because the pixels underneath
// are unknown and moving: the video's own colours are whatever the shot is, and
// any fixed pair would be unreadable over part of it. Swapping fg and bg is
// legible over anything, and it costs no colour of our own.
//
// The cursor and colour bookkeeping is invalidated afterwards, and it has to be.
// Draw skips the cursor move when a cell follows the last one written and skips
// the SGR when the colour is unchanged; after an overlay the cursor is in the
// middle of the grid with a background colour no cell has. Leaving the state
// alone makes the next frame resume mid-row and carry one overlay cell's colour
// into the rest of the line.
func (c *ColorDiffRenderer) Overlay(lines []string) error {
	if _, err := io.WriteString(c.w, "\x1b[0m\x1b[7m"); err != nil {
		return err
	}
	for _, ol := range layoutOverlay(c.cols, c.rows, lines) {
		if _, err := fmt.Fprintf(c.w, "\x1b[%d;1H%s", ol.row, ol.text); err != nil {
			return err
		}
	}
	if _, err := io.WriteString(c.w, "\x1b[0m"); err != nil {
		return err
	}
	c.haveLast = false
	c.sgrValid = false
	return nil
}

// FrameRenderer is the interface the render loop draws through, so the mono and
// colour paths are interchangeable.
type FrameRenderer interface {
	Draw(frame []byte) error
	ForceNext()
	// Overlay draws text over the grid. See layoutOverlay for where it lands.
	Overlay(lines []string) error
}

// colorSupported reports whether any colour mode is active.
func colorSupported(m ColorMode) bool { return m != ColorNone }

// envSlice builds a lookup-friendly copy of the environment for detection.
func envSlice() []string { return os.Environ() }

// GlyphMode decides how many pixels each character cell represents.
type GlyphMode int

const (
	// GlyphAuto probes the terminal and picks the best layout it supports.
	GlyphAuto GlyphMode = iota
	// GlyphHalf uses the half-block (2 vertical pixels per cell).
	GlyphHalf
	// GlyphCell uses one pixel per cell, rendered as a space with a background
	// colour. U+0020 is unambiguously one column wide, so this always lays out
	// correctly -- at the cost of half the vertical resolution.
	GlyphCell
)

// probeHalfBlockNarrow asks the terminal whether U+2580 advances the cursor by
// one column or two.
//
// It prints the glyph, then issues a Device Status Report (CPR) and reads the
// reported column. A terminal with narrow ambiguous-width reports col = 1+n; one
// that renders it wide reports 1+2n.
//
// This is the only reliable way to find out: U+2580 is East Asian Ambiguous
// width, and the two behaviours both look completely normal in isolation. The
// bug is only visible as cascading horizontal drift, because every row overruns
// the terminal width and wraps (this is exactly what the reported ghosting was).
//
// Returns ok=false when the terminal does not answer the report, so the caller
// can fall back to the always-correct layout.
func probeHalfBlockNarrow(in *os.File, out *os.File) (narrow bool, ok bool) {
	const n = 8

	// Home to 1;1 so the reported column is relative to a known origin.
	fmt.Fprint(out, "\x1b[1;1H")
	for i := 0; i < n; i++ {
		fmt.Fprint(out, string(halfBlock))
	}
	fmt.Fprint(out, "\x1b[6n")

	type result struct {
		s   string
		err error
	}
	ch := make(chan result, 1)
	go func() {
		b := make([]byte, 1)
		var acc []byte
		for i := 0; i < 32; i++ {
			_, err := in.Read(b)
			if err != nil {
				ch <- result{err: err}
				return
			}
			acc = append(acc, b[0])
			if b[0] == 'R' { // end of a CPR reply
				break
			}
		}
		ch <- result{s: string(acc)}
	}()

	select {
	case r := <-ch:
		if r.err != nil {
			return false, false
		}
		col := parseCPRColumn(r.s)
		if col <= 0 {
			return false, false
		}
		switch col {
		case 1 + n:
			return true, true
		case 1 + 2*n:
			return false, true
		default:
			return false, false
		}
	case <-time.After(700 * time.Millisecond):
		// Terminal ignored the report. Assume the safe layout.
		return false, false
	}
}

// parseCPRColumn extracts the column from an "\x1b[<row>;<col>R" reply.
func parseCPRColumn(s string) int {
	i := strings.IndexByte(s, ';')
	if i < 0 {
		return 0
	}
	j := strings.IndexByte(s[i:], 'R')
	if j < 0 {
		return 0
	}
	col := strings.TrimSpace(s[i+1 : i+j])
	n, err := strconv.Atoi(col)
	if err != nil {
		return 0
	}
	return n
}

// resolveGlyph decides how many pixels per cell to use.
// half is only chosen when the terminal explicitly proved it is narrow.
func resolveGlyph(mode GlyphMode, in, out *os.File) GlyphMode {
	switch mode {
	case GlyphHalf:
		return GlyphHalf
	case GlyphCell:
		return GlyphCell
	}
	if out == nil || in == nil {
		return GlyphCell
	}
	narrow, ok := probeHalfBlockNarrow(in, out)
	// Clean up the probe output either way.
	fmt.Fprint(out, "\x1b[2J\x1b[H")
	if ok && narrow {
		return GlyphHalf
	}
	return GlyphCell
}
