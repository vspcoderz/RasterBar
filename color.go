package main

import (
	"fmt"
	"io"
	"os"
	"strings"
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
func quant256(r, g, b byte) int {
	// Grey decision first. Pure black and pure white exist in the cube (16 and
	// 231) but the 232-255 ramp is neutral, and mapping black to cube-black
	// leaves greys with a colour cast on some palettes.
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

	prevTop []uint32 // packed 0xRRGGBB per cell
	prevBot []uint32
	first   bool

	lastFG, lastBG uint32
	lastX, lastY   int
	haveLast       bool
}

func NewColorDiffRenderer(w io.Writer, cols, rows int, mode ColorMode) *ColorDiffRenderer {
	n := cols * rows
	return &ColorDiffRenderer{
		w:         w,
		cols:      cols,
		rows:      rows,
		mode:      mode,
		truecolor: mode == ColorTrue,
		prevTop:   make([]uint32, n),
		prevBot:   make([]uint32, n),
		first:     true,
	}
}

func packRGB(r, g, b byte) uint32 {
	return uint32(r)<<16 | uint32(g)<<8 | uint32(b)
}

// cellBytes is the frame size the colour path needs.
func (c *ColorDiffRenderer) cellBytes() int { return c.cols * c.rows * 2 * 3 }

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
		// Seed the previous buffers with an impossible value. They start as
		// zeros, which is a legitimate colour, so a first frame that happened to
		// be all-black would diff as "unchanged" and never be drawn.
		for i := range c.prevTop {
			c.prevTop[i] = ^uint32(0)
			c.prevBot[i] = ^uint32(0)
		}
	} else {
		// Reset SGR so a stale background colour cannot bleed into a cell whose
		// previous frame painted one.
		if _, err := io.WriteString(c.w, "\x1b[0m"); err != nil {
			return err
		}
	}

	stride := c.cols * 2 * 3 // two pixels per row of cells, 3 bytes each
	for y := 0; y < c.rows; y++ {
		for x := 0; x < c.cols; x++ {
			idx := y*c.cols + x

			topOff := y*stride + x*3
			botOff := topOff + c.cols*3
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
				if _, err := fmt.Fprintf(c.w, "\x1b[%d;%dH", y+1, x+1); err != nil {
					return err
				}
			}

			if err := c.emitColor(top, bot); err != nil {
				return err
			}
			if _, err := io.WriteString(c.w, string(halfBlock)); err != nil {
				return err
			}

			c.lastX, c.lastY = x, y
			c.haveLast = true
		}
		// Each glyph is one column wide, but some terminals render U+2580 as
		// ambiguous-width. A newline after the last cell avoids leaving the
		// cursor mid-row; the cursor is repositioned explicitly anyway.
		if _, err := io.WriteString(c.w, "\r\n"); err != nil {
			return err
		}
		c.haveLast = false
	}
	return nil
}

// emitColor writes the SGR sequence for a cell, skipping it when neither colour
// changed since the previous cell. This is the main bandwidth saving.
func (c *ColorDiffRenderer) emitColor(top, bot uint32) error {
	if c.haveLast && top == c.lastFG && bot == c.lastBG {
		return nil
	}
	var seq string
	if c.truecolor {
		seq = fmt.Sprintf("\x1b[38;2;%d;%d;%d;48;2;%d;%d;%dm",
			top>>16&0xff, top>>8&0xff, top&0xff,
			bot>>16&0xff, bot>>8&0xff, bot&0xff)
	} else {
		seq = fmt.Sprintf("\x1b[38;5;%d;48;5;%dm",
			quant256(byte(top>>16), byte(top>>8), byte(top)),
			quant256(byte(bot>>16), byte(bot>>8), byte(bot)))
	}
	if _, err := io.WriteString(c.w, seq); err != nil {
		return err
	}
	c.lastFG, c.lastBG = top, bot
	return nil
}

// ForceNext makes the next Draw repaint everything.
func (c *ColorDiffRenderer) ForceNext() { c.first = true; c.haveLast = false }

// FrameRenderer is the interface runASCII draws through, so the mono and colour
// paths are interchangeable.
type FrameRenderer interface {
	Draw(frame []byte) error
	ForceNext()
}

// colorSupported reports whether any colour mode is active.
func colorSupported(m ColorMode) bool { return m != ColorNone }

// envSlice builds a lookup-friendly copy of the environment for detection.
func envSlice() []string { return os.Environ() }
