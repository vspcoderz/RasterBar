package main

import (
	"fmt"
	"io"
)

// Rendering: tonal ramp and diff-based painting.
//
// The ramp spans black to white in *perceived ink density*, following the
// classic 70-character ASCII ramp. It is deliberately not monotonic in
// codepoint order ('^' is 0x5E, '`' is 0x60): what matters is how much ink each
// glyph lays down on screen, which is a property of the font, not of the
// encoding. Hand-reordering the string to satisfy a codepoint test would break
// the visual ramp, so the tests check coverage and endpoints instead.
//
// 70 levels instead of the original 10 removes banding on smooth gradients at
// no runtime cost.
const ramp = " .'`^\",:;Il!i~+_-?][}{1)(|\\/tfjrxnuvczXYUJCLQ0OZmwqpdbkhao*#MW&8%B@$"

// DiffRenderer repaints only the cells that changed since the last frame.
//
// This is the fix for lag. Writing the whole grid every frame cost ~11KB per
// frame at 200x57, so the terminal, not the CPU, became the bottleneck. ASCII
// video is mostly static background between frames, so diffing cuts the byte
// count by roughly an order of magnitude.
//
// Writes are grouped into per-row runs so a changed cell costs a cursor
// position plus a few characters, not a whole line.
type DiffRenderer struct {
	w    io.Writer
	cols int
	rows int

	prev  []byte // previous frame, one byte per cell
	cur   []byte // scratch for partial row writes
	first bool
}

func NewDiffRenderer(w io.Writer, cols, rows int) *DiffRenderer {
	return &DiffRenderer{
		w:     w,
		cols:  cols,
		rows:  rows,
		prev:  make([]byte, cols*rows),
		cur:   make([]byte, cols),
		first: true,
	}
}

// levelFor maps a 0-255 grayscale byte to a ramp index.
func levelFor(b byte) byte {
	i := int(b) * len(ramp) / 256
	if i >= len(ramp) {
		i = len(ramp) - 1
	}
	return ramp[i]
}

// Draw paints a frame, emitting only what changed. frame must be cols*rows
// bytes, one grayscale sample per cell.
func (d *DiffRenderer) Draw(frame []byte) error {
	if len(frame) < d.cols*d.rows {
		return fmt.Errorf("frame too small: %d bytes, want %d", len(frame), d.cols*d.rows)
	}

	if d.first {
		// First frame: home and paint everything.
		if _, err := io.WriteString(d.w, "\x1b[H\x1b[2J"); err != nil {
			return err
		}
		for y := 0; y < d.rows; y++ {
			row := frame[y*d.cols : (y+1)*d.cols]
			for x, b := range row {
				d.cur[x] = levelFor(b)
			}
			if _, err := fmt.Fprintf(d.w, "\x1b[%d;1H%s", y+1, d.cur); err != nil {
				return err
			}
		}
		copy(d.prev, frame)
		d.first = false
		return nil
	}

	for y := 0; y < d.rows; y++ {
		off := y * d.cols
		row := frame[off : off+d.cols]
		old := d.prev[off : off+d.cols]

		// Find runs of changed cells in this row.
		x := 0
		for x < d.cols {
			if row[x] == old[x] {
				x++
				continue
			}
			start := x
			for x < d.cols && row[x] != old[x] {
				x++
			}
			// Expand slightly: a run one cell wide still costs a cursor
			// move, so paint a couple of neighbours with it.
			if end := x + 2; end <= d.cols {
				x = end
			}
			for i := start; i < x; i++ {
				d.cur[i] = levelFor(row[i])
			}
			if _, err := fmt.Fprintf(d.w, "\x1b[%d;%dH%s", y+1, start+1, d.cur[start:x]); err != nil {
				return err
			}
		}
		copy(old, row)
	}
	return nil
}

// ForceNext makes the next Draw repaint everything. Used when the terminal may
// have been resized or cleared underneath us.
func (d *DiffRenderer) ForceNext() { d.first = true }
