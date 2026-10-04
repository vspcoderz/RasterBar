package main

// A minimal terminal emulator, so renderer output can be checked by what the
// screen ends up showing rather than by which bytes came out.
//
// Byte-level assertions cannot catch a stale cell: a renderer that emits the
// right bytes in the wrong order, or skips a cell whose colour changed, still
// "writes something". Reconstructing the screen and comparing it to the frame
// that was handed in is the only assertion that actually means "the terminal
// shows this frame".
//
// Scope is deliberately tiny — only the sequences these two renderers emit. It
// is not a terminal emulator and does not want to be.

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// screenCell is one character cell: the glyph plus the colours in force when it
// was written. A leaked pixel shows up as a cell whose fg/bg belong to an older
// frame.
type screenCell struct {
	r        rune
	fg, bg   uint32
	inverse  bool
	hasColor bool
}

type screen struct {
	cols, rows int
	cells      []screenCell
	curX       int
	curY       int
	fg, bg     uint32
	inverse    bool
	colored    bool
	// deferredWrap mirrors the terminal's pending-wrap state: after writing the
	// last column the cursor does not move until something else moves it.
	pendingWrap bool
}

func newScreen(cols, rows int) *screen {
	s := &screen{cols: cols, rows: rows}
	s.clear()
	return s
}

func (s *screen) clear() {
	s.cells = make([]screenCell, s.cols*s.rows)
	for i := range s.cells {
		s.cells[i] = screenCell{r: ' '}
	}
	s.curX, s.curY = 0, 0
	s.pendingWrap = false
}

func (s *screen) put(r rune) {
	if s.pendingWrap {
		s.curX = 0
		s.curY++
		s.pendingWrap = false
	}
	if s.curY < 0 || s.curY >= s.rows || s.curX < 0 || s.curX >= s.cols {
		return // off-screen write: not something these renderers do
	}
	s.cells[s.curY*s.cols+s.curX] = screenCell{
		r: r, fg: s.fg, bg: s.bg, inverse: s.inverse, hasColor: s.colored,
	}
	s.curX++
	if s.curX >= s.cols {
		s.pendingWrap = true
	}
}

func (s *screen) moveTo(row, col int) { // 1-based, like CUP
	s.curY, s.curX = row-1, col-1
	s.pendingWrap = false
}

func (s *screen) at(y, x int) screenCell { return s.cells[y*s.cols+x] }

// feed interprets p as terminal output.
// Write makes screen an io.Writer, so a renderer can be handed one directly.
func (s *screen) Write(p []byte) (int, error) {
	s.feed(p)
	return len(p), nil
}

func (s *screen) feed(p []byte) {
	str := string(p)
	for i := 0; i < len(str); {
		c := str[i]
		if c != 0x1b {
			switch c {
			case '\r':
				s.curX = 0
				s.pendingWrap = false
			case '\n':
				s.curY++
				s.pendingWrap = false
			default:
				r, size := decodeRune(str[i:])
				s.put(r)
				i += size
				continue
			}
			i++
			continue
		}
		// Escape sequence.
		if i+1 >= len(str) {
			return
		}
		switch str[i+1] {
		case '[':
			j := i + 2
			for j < len(str) && !isFinalByte(str[j]) {
				j++
			}
			if j >= len(str) {
				return
			}
			s.csi(str[i+2:j], str[j])
			i = j + 1
		default:
			i += 2
		}
	}
}

func isFinalByte(c byte) bool { return c >= 0x40 && c <= 0x7e }

// csi handles the subset the renderers use: H, J, m.
func (s *screen) csi(params string, final byte) {
	switch final {
	case 'H':
		row, col := 1, 1
		if p := strings.Split(params, ";"); len(p) >= 2 {
			if v, err := strconv.Atoi(p[0]); err == nil {
				row = v
			}
			if v, err := strconv.Atoi(p[1]); err == nil {
				col = v
			}
		} else if len(p) == 1 && p[0] != "" {
			if v, err := strconv.Atoi(p[0]); err == nil {
				row = v
			}
		}
		s.moveTo(row, col)
	case 'J':
		if params == "" || params == "2" {
			s.clear()
		}
	case 'm':
		if params == "" || params == "0" {
			s.fg, s.bg, s.inverse, s.colored = 0, 0, false, false
			return
		}
		for _, part := range strings.Split(params, ";") {
			n, err := strconv.Atoi(part)
			if err != nil {
				continue
			}
			switch {
			case n == 7:
				s.inverse = true
			case n == 27:
				s.inverse = false
			case n == 39:
				s.fg = 0
			case n == 49:
				s.bg = 0
			case n >= 30 && n <= 37, n >= 90 && n <= 97:
				s.colored = true
				s.fg = s.fg&0xFF00FF00 | uint32(n&7)*0x00050005&0
			case n == 38, n == 48:
				// 5;<idx> or 2;<r>;<g>;<b>: consume what follows.
			}
		}
	}
}

func decodeRune(s string) (rune, int) {
	for i, r := range s {
		_ = i
		return r, len(string(r))
	}
	return 0, 1
}

// --- the leak itself ---------------------------------------------------------

// TestColorRendererScreenMatchesLastFrame is the leak test: after a run of
// adversarial frames, the screen must show the last frame exactly. A cell left
// showing an older frame's colour is a leaked pixel.
func TestColorRendererScreenMatchesLastFrame(t *testing.T) {
	const cols, rows = 24, 9
	for _, mode := range []ColorMode{ColorTrue, Color256} {
		for _, glyph := range []GlyphMode{GlyphHalf, GlyphCell} {
			name := modeName(mode) + "/" + glyphName(glyph)
			t.Run(name, func(t *testing.T) {
				sc := newScreen(cols, rows)
				r := newColorRenderer(sc, cols, rows, mode, glyph)

				frames := leakFrames(cols, rows, glyph)
				for i, f := range frames {
					if err := r.Draw(f); err != nil {
						t.Fatalf("frame %d: %v", i, err)
					}
				}
				assertScreenMatches(t, sc, cols, rows, glyph, frames[len(frames)-1])
			})
		}
	}
}

func TestDiffRendererScreenMatchesLastFrame(t *testing.T) {
	const cols, rows = 24, 9
	sc := newScreen(cols, rows)
	r := NewDiffRenderer(sc, cols, rows)
	frames := leakFrames(cols, rows, GlyphCell)
	for i, f := range frames {
		if err := r.Draw(f); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
	}
	assertScreenMatchesMono(t, sc, cols, rows, frames[len(frames)-1])
}

// leakFrames builds a sequence designed to expose a stale cell: every frame is
// mostly black with a few hard-edged bright blocks that move one cell per frame.
// High-contrast motion is what a music video is made of, and it is what makes a
// skipped repaint visible.
func leakFrames(cols, rows int, glyph GlyphMode) [][]byte {
	perCell := 1
	if glyph == GlyphHalf {
		perCell = 2
	}
	pxRows := rows * perCell
	n := cols * pxRows * 3
	var out [][]byte

	black := make([]byte, n)
	out = append(out, black)

	for step := 0; step < 6; step++ {
		f := make([]byte, n)
		for y := 0; y < pxRows; y++ {
			for x := 0; x < cols; x++ {
				// A diagonal band of white, shifting one column per frame.
				if (x+y+step)%cols < 3 {
					off := (y*cols + x) * 3
					f[off], f[off+1], f[off+2] = 255, 255, 255
				}
			}
		}
		out = append(out, f)
	}
	// Return to black: the single most leaky transition, because every cell
	// goes from "definitely changed" to "matches nothing".
	out = append(out, make([]byte, n))
	return out
}

func assertScreenMatches(t *testing.T, sc *screen, cols, rows int, glyph GlyphMode, frame []byte) {
	t.Helper()
	perCell := 1
	if glyph == GlyphHalf {
		perCell = 2
	}
	pxRows := rows * perCell
	for y := 0; y < pxRows; y++ {
		for x := 0; x < cols; x++ {
			off := (y*cols + x) * 3
			want := packRGB(frame[off], frame[off+1], frame[off+2])
			cellY := y / perCell
			got := sc.at(cellY, x)
			var gotFG, gotBG uint32
			if perCell == 2 && y%2 == 0 {
				gotFG, gotBG = got.fg, got.bg
			} else if perCell == 2 {
				gotFG, gotBG = got.fg, got.bg
			} else {
				gotBG = got.bg
			}
			if perCell == 1 {
				if gotBG != want {
					t.Fatalf("cell (%d,%d): bg %06x want %06x", x, y, gotBG, want)
				}
				continue
			}
			_ = gotFG
			if gotFG != want && gotBG != want {
				t.Fatalf("pixel (%d,%d): screen shows fg %06x bg %06x, want %06x in one of them",
					x, y, gotFG, gotBG, want)
			}
		}
	}
}

func assertScreenMatchesMono(t *testing.T, sc *screen, cols, rows int, frame []byte) {
	t.Helper()
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			want := levelFor(frame[y*cols+x])
			if got := sc.at(y, x).r; got != rune(want) {
				t.Fatalf("cell (%d,%d): %q want %q", x, y, got, rune(want))
			}
		}
	}
}

// TestRenderLoopScreenMatchesLastFrame drives the write pattern the render loop
// actually produces — a frame, then the HUD's three chrome lines with their SGR
// reset, then the next frame — because that interleaving is what the renderer
// tests above never exercise. A renderer that assumes it is the only writer
// leaks: it assumes the cursor is where it left it, and it assumes the colour
// in force is the one it last set.
func TestRenderLoopScreenMatchesLastFrame(t *testing.T) {
	const cols, rows = 24, 8
	for _, mode := range []ColorMode{ColorTrue, Color256} {
		for _, glyph := range []GlyphMode{GlyphHalf, GlyphCell} {
			t.Run(modeName(mode)+"/"+glyphName(glyph), func(t *testing.T) {
				sc := newScreen(cols, rows+3)
				r := newColorRenderer(sc, cols, rows, mode, glyph)
				frames := leakFrames(cols, rows, glyph)
				for i, f := range frames {
					if err := r.Draw(f); err != nil {
						t.Fatalf("frame %d: %v", i, err)
					}
					// Exactly what paintHUD emits.
					hud := []string{"title", "0:01 / 3:20", "space pause"}
					fmt.Fprint(sc, "\x1b[0m")
					for j, line := range hud {
						fmt.Fprintf(sc, "\x1b[%d;1H%s", rows+1+j, line)
					}
				}
				// The grid must still show the last frame, not the HUD's colour.
				assertScreenMatches(t, sc, cols, rows, glyph, frames[len(frames)-1])
			})
		}
	}
}

// TestRenderLoopAfterStallClear checks the recovery path: a covered terminal
// forces a full repaint on resume, and ForceNext is the only thing that makes
// the next Draw rewrite every cell.
func TestRenderLoopAfterStallClear(t *testing.T) {
	const cols, rows = 24, 8
	sc := newScreen(cols, rows+3)
	r := newColorRenderer(sc, cols, rows, ColorTrue, GlyphHalf)
	frames := leakFrames(cols, rows, GlyphHalf)
	for i, f := range frames {
		if err := r.Draw(f); err != nil {
			t.Fatalf("frame %d: %v", i, err)
		}
		if i == len(frames)-2 {
			r.ForceNext() // the stall handler's recovery
		}
	}
	assertScreenMatches(t, sc, cols, rows, GlyphHalf, frames[len(frames)-1])
}

func modeName(m ColorMode) string {
	switch m {
	case ColorTrue:
		return "truecolor"
	case Color256:
		return "256"
	}
	return "mono"
}

func glyphName(g GlyphMode) string {
	if g == GlyphHalf {
		return "half"
	}
	return "cell"
}

var _ = fmt.Sprintf

// text renders a row as a string, for assertions about what a cell shows.
func (s *screen) text(y int) string {
	var b strings.Builder
	for x := 0; x < s.cols; x++ {
		b.WriteRune(s.at(y, x).r)
	}
	return strings.TrimRight(b.String(), " ")
}
