package main

import (
	"bytes"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestRenderFrameTo(t *testing.T) {
	const cols, rows = 4, 2
	raw := make([]byte, cols*rows)
	for i := range raw {
		raw[i] = 255
	}
	var buf bytes.Buffer
	if err := renderFrameTo(&buf, raw, cols, rows); err != nil {
		t.Fatalf("renderFrameTo: %v", err)
	}
	want := strings.Repeat(string(ramp[len(ramp)-1])+strings.Repeat(string(ramp[len(ramp)-1]), 0), 0) +
		strings.Repeat(string(ramp[len(ramp)-1]), cols) + "\n" +
		strings.Repeat(string(ramp[len(ramp)-1]), cols) + "\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderFrameToDarkIsSpaces(t *testing.T) {
	const cols, rows = 4, 2
	var buf bytes.Buffer
	if err := renderFrameTo(&buf, make([]byte, cols*rows), cols, rows); err != nil {
		t.Fatalf("renderFrameTo: %v", err)
	}
	want := strings.Repeat(" ", cols) + "\n" + strings.Repeat(" ", cols) + "\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRampCoversFullByteRange(t *testing.T) {
	// every possible byte must map to a valid ramp index
	for b := 0; b < 256; b++ {
		idx := b * len(ramp) / 256
		if idx < 0 || idx >= len(ramp) {
			t.Fatalf("byte %d maps to out-of-range index %d", b, idx)
		}
	}
	if idx := 255 * len(ramp) / 256; idx != len(ramp)-1 {
		t.Errorf("byte 255 maps to %d, want %d (brightest char)", idx, len(ramp)-1)
	}
}

func TestDiffRendererFirstFramePaintsAll(t *testing.T) {
	const cols, rows = 8, 3
	var buf bytes.Buffer
	d := NewDiffRenderer(&buf, cols, rows)
	frame := make([]byte, cols*rows)
	for i := range frame {
		frame[i] = byte(i * 8)
	}
	if err := d.Draw(frame); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "\x1b[2J") {
		t.Error("first frame should clear the screen")
	}
	// three cursor-positioned rows
	if strings.Count(out, "\x1b[") < rows {
		t.Errorf("expected at least %d cursor moves, got %d", rows, strings.Count(out, "\x1b["))
	}
}

func TestDiffRendererSkipsUnchanged(t *testing.T) {
	const cols, rows = 16, 4
	var buf bytes.Buffer
	d := NewDiffRenderer(&buf, cols, rows)
	frame := make([]byte, cols*rows)
	for i := range frame {
		frame[i] = 128
	}
	if err := d.Draw(frame); err != nil {
		t.Fatal(err)
	}
	full := buf.Len()
	buf.Reset()

	// Identical frame: almost nothing should be written.
	if err := d.Draw(frame); err != nil {
		t.Fatal(err)
	}
	quiet := buf.Len()
	if quiet > full/10 {
		t.Errorf("unchanged frame wrote %d bytes vs %d for the first frame; diffing is not working", quiet, full)
	}

	// One changed cell should cost far less than a full repaint.
	frame[0] = 255
	buf.Reset()
	if err := d.Draw(frame); err != nil {
		t.Fatal(err)
	}
	oneCell := buf.Len()
	if oneCell > full/2 {
		t.Errorf("single-cell change wrote %d bytes vs %d full; runs are not limiting output", oneCell, full)
	}
}

func TestDiffRendererRunWidening(t *testing.T) {
	// A one-cell change must still emit output (cursor move + the cell),
	// even though the run-expansion widens the painted span.
	const cols, rows = 10, 2
	var buf bytes.Buffer
	d := NewDiffRenderer(&buf, cols, rows)
	frame := make([]byte, cols*rows)
	for i := range frame {
		frame[i] = 40
	}
	if err := d.Draw(frame); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	frame[5] = 200
	if err := d.Draw(frame); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("a changed cell produced no output")
	}
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Error("changed cell should be positioned with a cursor move")
	}
}

func TestDiffRendererRejectsShortFrame(t *testing.T) {
	d := NewDiffRenderer(&bytes.Buffer{}, 8, 4)
	if err := d.Draw(make([]byte, 4)); err == nil {
		t.Error("want an error for a frame smaller than the grid")
	}
}

func TestLevelForCoversRange(t *testing.T) {
	// The ramp is ordered by perceived ink density, not codepoint, so the
	// meaningful checks are coverage and endpoints.
	if got := levelFor(0); got != ramp[0] {
		t.Errorf("levelFor(0) = %q, want darkest %q", got, ramp[0])
	}
	if got := levelFor(255); got != ramp[len(ramp)-1] {
		t.Errorf("levelFor(255) = %q, want brightest %q", got, ramp[len(ramp)-1])
	}
	// Every byte must map to a character that exists in the ramp, and the
	// distinct level count must show real tonal resolution (not a tiny ramp).
	seen := map[byte]bool{}
	for b := 0; b < 256; b++ {
		g := levelFor(byte(b))
		if !strings.ContainsRune(ramp, rune(g)) {
			t.Fatalf("byte %d mapped to %q which is not in the ramp", b, g)
		}
		seen[g] = true
	}
	if len(seen) < 32 {
		t.Errorf("only %d distinct levels used; ramp is too coarse for smooth gradients", len(seen))
	}
	// Denser input must never map to a *lower ramp index*.
	prevIdx := -1
	for b := 0; b < 256; b++ {
		idx := strings.IndexByte(ramp, levelFor(byte(b)))
		if idx < prevIdx {
			t.Fatalf("ramp index went backwards at byte %d: %d < %d", b, idx, prevIdx)
		}
		prevIdx = idx
	}
}

func TestDetectColor(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want ColorMode
	}{
		{"truecolor via COLORTERM", []string{"TERM=xterm-256color", "COLORTERM=truecolor"}, ColorTrue},
		{"24bit", []string{"COLORTERM=24bit"}, ColorTrue},
		{"term advertises truecolor", []string{"TERM=xterm-truecolor"}, ColorTrue},
		{"plain 256", []string{"TERM=xterm-256color"}, Color256},
		{"kitty", []string{"TERM=xterm-kitty"}, Color256},
		{"dumb", []string{"TERM=dumb"}, ColorNone},
		{"plain xterm", []string{"TERM=xterm"}, ColorNone},
		{"empty env", nil, ColorNone},
	}
	for _, tc := range cases {
		if got := detectColor(tc.env); got != tc.want {
			t.Errorf("%s: detectColor(%v) = %v, want %v", tc.name, tc.env, got, tc.want)
		}
	}
}

func TestQuant256GreyRamp(t *testing.T) {
	// Mid greys must land in the 232-255 grey ramp, not the colour cube, or
	// monochrome content picks up a colour cast.
	//
	// 0 and 255 are excluded and asserted separately: they are the two endpoints
	// where the ramp is *wrong*, because the ramp starts at #080808 and ends at
	// #eeeeee. See TestQuant256EndpointsAreActuallyBlackAndWhite.
	for _, v := range []byte{10, 64, 128, 200} {
		idx := quant256(v, v, v)
		if idx < 232 {
			t.Errorf("grey %d mapped to %d, want the grey ramp (>=232)", v, idx)
		}
	}
	// Saturated colours must land in the cube (16-231).
	for _, c := range [][3]byte{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 0}} {
		idx := quant256(c[0], c[1], c[2])
		if idx < 16 || idx > 231 {
			t.Errorf("colour %v mapped to %d, want the colour cube (16-231)", c, idx)
		}
	}
	// Output must be a valid palette index.
	for r := 0; r < 256; r += 17 {
		for g := 0; g < 256; g += 17 {
			for b := 0; b < 256; b += 17 {
				idx := quant256(byte(r), byte(g), byte(b))
				if idx < 0 || idx > 255 {
					t.Fatalf("quant256(%d,%d,%d) = %d, out of palette", r, g, b, idx)
				}
			}
		}
	}
}

func TestColorRendererEmitsHalfBlocks(t *testing.T) {
	const cols, rows = 4, 2
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, ColorTrue, GlyphHalf)
	frame := make([]byte, r.cellBytes())
	for i := 0; i < len(frame); i++ {
		frame[i] = byte(i * 7 % 256)
	}
	if err := r.Draw(frame); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, string(halfBlock)) {
		t.Errorf("expected half-block glyphs in output, got %q", truncateForLog(out))
	}
	if !strings.Contains(out, "38;2;") {
		t.Error("truecolor mode should emit 24-bit SGR sequences")
	}
	if !strings.Contains(out, "48;2;") {
		t.Error("half-block mode needs a background colour for the lower half")
	}
}

func TestColorRenderer256UsesPalette(t *testing.T) {
	const cols, rows = 4, 2
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, Color256, GlyphHalf)
	if err := r.Draw(make([]byte, r.cellBytes())); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "38;5;") {
		t.Errorf("256 mode should emit palette indices, got %q", truncateForLog(out))
	}
	if strings.Contains(out, "38;2;") {
		t.Error("256 mode must not emit 24-bit sequences")
	}
}

func TestColorRendererSkipsUnchangedCells(t *testing.T) {
	const cols, rows = 12, 4
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, Color256, GlyphHalf)
	frame := make([]byte, r.cellBytes())
	for i := range frame {
		frame[i] = 128
	}
	if err := r.Draw(frame); err != nil {
		t.Fatal(err)
	}
	first := buf.Len()
	buf.Reset()

	// Identical frame: only the per-row reset sequences should remain, not a
	// full repaint of every cell.
	if err := r.Draw(frame); err != nil {
		t.Fatal(err)
	}
	second := buf.Len()
	if second > first/2 {
		t.Errorf("unchanged colour frame wrote %d bytes vs %d for the first frame; diffing is not working",
			second, first)
	}
}

func TestColorRendererRejectsShortFrame(t *testing.T) {
	r := NewColorDiffRenderer(&bytes.Buffer{}, 8, 4, ColorTrue, GlyphHalf)
	if err := r.Draw(make([]byte, 10)); err == nil {
		t.Error("want an error for a frame smaller than the colour grid")
	}
}

func TestColorCellBytesDoublesHeight(t *testing.T) {
	const cols, rows = 10, 5
	r := NewColorDiffRenderer(&bytes.Buffer{}, cols, rows, ColorTrue, GlyphHalf)
	// Two pixels per cell, three bytes each: the half-block layout.
	if got, want := r.cellBytes(), cols*rows*2*3; got != want {
		t.Errorf("cellBytes = %d, want %d", got, want)
	}
}

func TestParseCPRColumn(t *testing.T) {
	cases := map[string]int{
		"\x1b[1;9R":  9,
		"\x1b[1;17R": 17,
		"\x1b[5;1R":  1,
		"garbage":    0,
		"":           0,
		"\x1b[?R":    0,
	}
	for in, want := range cases {
		if got := parseCPRColumn(in); got != want {
			t.Errorf("parseCPRColumn(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestProbeHalfBlockNarrowDetectsWideTerminal(t *testing.T) {
	// Simulate a terminal that reports the cursor advancing 2 columns per
	// half-block: it must be identified as wide.
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	go func() {
		outR.Read(make([]byte, 512)) // drain the probe output
	}()
	go func() {
		inW.Write([]byte("\x1b[1;17R")) // 1 + 2*8
		inW.Close()
	}()
	narrow, ok := probeHalfBlockNarrow(inR, outW)
	outW.Close()
	if !ok {
		t.Error("a terminal that answered the report should count as ok")
	}
	if narrow {
		t.Error("terminal reported 2 columns per glyph; it must not be treated as narrow")
	}
	inR.Close()
}

func TestProbeHalfBlockNarrowDetectsNarrowTerminal(t *testing.T) {
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	go func() {
		outR.Read(make([]byte, 512))
	}()
	go func() {
		inW.Write([]byte("\x1b[1;9R")) // 1 + 1*8
		inW.Close()
	}()
	narrow, ok := probeHalfBlockNarrow(inR, outW)
	outW.Close()
	if !ok || !narrow {
		t.Errorf("got narrow=%v ok=%v, want true/true for a 1-column report", narrow, ok)
	}
	inR.Close()
}

func TestResolveGlyphFallsBackToCell(t *testing.T) {
	// A non-answering terminal must get the always-correct one-pixel layout.
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	go func() {
		outR.Read(make([]byte, 2048))
		inW.Close() // EOF: no reply
	}()
	if got := resolveGlyph(GlyphAuto, inR, outW); got != GlyphCell {
		t.Errorf("resolveGlyph with no reply = %v, want GlyphCell", got)
	}
	outW.Close()
	inR.Close()

	// Explicit flags must override detection entirely.
	if got := resolveGlyph(GlyphHalf, nil, nil); got != GlyphHalf {
		t.Errorf("--glyph half must be honoured, got %v", got)
	}
	if got := resolveGlyph(GlyphCell, nil, nil); got != GlyphCell {
		t.Errorf("--glyph cell must be honoured, got %v", got)
	}
}

func TestColorCellModeUsesSpaces(t *testing.T) {
	const cols, rows = 4, 2
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, ColorTrue, GlyphCell)
	frame := make([]byte, r.cellBytes())
	for i := range frame {
		frame[i] = byte(120 + i%40)
	}
	if err := r.Draw(frame); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, string(halfBlock)) {
		t.Error("cell mode must not emit the ambiguous-width half-block")
	}
	if !strings.Contains(out, "48;2;") {
		t.Error("cell mode should paint pixels as a background colour")
	}
	if strings.Count(out, " ") < cols*rows {
		t.Errorf("expected at least %d space glyphs, got %d", cols*rows, strings.Count(out, " "))
	}
	// One pixel per cell means a frame exactly the size of the grid.
	if got, want := r.cellBytes(), cols*rows*3; got != want {
		t.Errorf("cell-mode frame = %d bytes, want %d", got, want)
	}
}

func TestColorHalfModeFrameDoubles(t *testing.T) {
	const cols, rows = 4, 2
	half := NewColorDiffRenderer(&bytes.Buffer{}, cols, rows, ColorTrue, GlyphHalf)
	single := NewColorDiffRenderer(&bytes.Buffer{}, cols, rows, ColorTrue, GlyphCell)
	if half.cellBytes() != single.cellBytes()*2 {
		t.Errorf("half-block frame %d bytes, want double the cell-mode %d",
			half.cellBytes(), single.cellBytes())
	}
}

func TestTermSizeOnNonTTY(t *testing.T) {
	// Reading a non-TTY must return an error, not a fake size: the callers fall
	// back to defaults on error.
	f, err := os.CreateTemp("", "notatty")
	if err != nil {
		t.Skipf("temp file: %v", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, _, err := termSize(f); err == nil {
		t.Error("termSize on a regular file should error")
	}
}

// --- black cells and the colour diff ----------------------------------------
//
// A pixel that is true black must still be painted. Skipping it would leave
// whatever the terminal had in that cell showing through, which reads as "black
// pixels leak" — so these pin that the diff treats black as a real colour.

func TestBlackCellsArePaintedOnFirstFrame(t *testing.T) {
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, 4, 2, ColorTrue, GlyphCell)
	if err := r.Draw(solidRGB(4, 2, 0, 0, 0)); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "48;2;0;0;0") {
		t.Errorf("no black background emitted; output=%q", out)
	}
	if n := strings.Count(out, " "); n < 8 {
		t.Errorf("emitted %d glyphs for 8 cells: %q", n, out)
	}
}

func TestCellTurningBlackIsRepainted(t *testing.T) {
	// Four grey cells, then the middle two go black. The two changed cells must
	// be painted; the two unchanged ones must not be.
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, 4, 1, ColorTrue, GlyphCell)
	if err := r.Draw(solidRGB(4, 1, 200, 200, 200)); err != nil {
		t.Fatalf("Draw grey: %v", err)
	}
	buf.Reset()

	f := solidRGB(4, 1, 200, 200, 200)
	for x := 1; x < 3; x++ {
		f[x*3], f[x*3+1], f[x*3+2] = 0, 0, 0
	}
	if err := r.Draw(f); err != nil {
		t.Fatalf("Draw black: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "48;2;0;0;0") {
		t.Errorf("cells that turned black were not painted; output=%q", out)
	}
	if strings.Contains(out, "\x1b[1;1H") {
		t.Errorf("unchanged cell was repainted; output=%q", out)
	}
	// The changed pair is contiguous, so it is one cursor move and two glyphs.
	if !strings.Contains(out, "\x1b[1;2H") {
		t.Errorf("changed cells not positioned; output=%q", out)
	}
	if n := strings.Count(out, " "); n != 2 {
		t.Errorf("emitted %d glyphs, want exactly the 2 changed cells: %q", n, out)
	}
}

func TestBlackTopPixelInHalfModeIsPainted(t *testing.T) {
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, 2, 1, ColorTrue, GlyphHalf)
	f := make([]byte, 2*1*2*3)
	for i := range f {
		f[i] = 180
	}
	if err := r.Draw(f); err != nil {
		t.Fatalf("Draw grey: %v", err)
	}
	buf.Reset()
	for x := 0; x < 2; x++ { // top row black, bottom stays grey
		f[x*3], f[x*3+1], f[x*3+2] = 0, 0, 0
	}
	if err := r.Draw(f); err != nil {
		t.Fatalf("Draw black top: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "38;2;0;0;0") {
		t.Errorf("black top pixel not emitted as foreground; output=%q", out)
	}
	if !strings.Contains(out, "▀") {
		t.Errorf("half-block glyph missing; output=%q", out)
	}
}

// TestQuant256EndpointsAreActuallyBlackAndWhite pins the two colours the grey
// ramp cannot represent.
//
// The grey ramp runs 232..255, which is #080808 through #eeeeee. It is the right
// home for mid greys — that is the colour-cast fix, and TestQuant256GreyRamp still
// pins it — but its endpoints are not black and white. Mapping true black to 232
// means every "black" cell on a 256-colour terminal is #080808, a faint grey box
// that reads as a smudge against the terminal's own black background.
//
// This was previously asserted the other way round, with a note not to "fix" it
// without measuring the cast. Measured now, with a working emulator to read the
// result back: the cast concern is real for mid greys and unaffected by sending
// the exact endpoints to 16 and 231, which are #000000 and #ffffff. So both
// properties hold at once, and the earlier version only had one of them.

// TestQuant256EndpointsAreActuallyBlackAndWhite pins the two colours the grey
// ramp cannot represent.
//
// The grey ramp runs 232..255, which is #080808 through #eeeeee. It is the right
// home for mid greys — that is the colour-cast fix, and TestQuant256GreyRamp still
// pins it — but its endpoints are not black and white. Mapping true black to 232
// means every "black" cell on a 256-colour terminal is #080808, a faint grey box
// that reads as a smudge against the terminal's own black background.
//
// This was previously asserted the other way round, with a note not to "fix" it
// without measuring the cast. Measured now, with a working emulator to read the
// result back: the cast concern is real for mid greys and unaffected by sending
// the exact endpoints to 16 and 231, which are #000000 and #ffffff. So both
// properties hold at once, and the earlier version only had one of them.
func TestQuant256EndpointsAreActuallyBlackAndWhite(t *testing.T) {
	if got := quant256(0, 0, 0); got != 16 {
		t.Errorf("quant256(0,0,0) = %d, want 16 (#000000); the ramp's 232 is #080808", got)
	}
	if got := quant256(255, 255, 255); got != 231 {
		t.Errorf("quant256(255,255,255) = %d, want 231 (#ffffff); the ramp's 255 is #eeeeee", got)
	}
	// And the property that actually matters: round-tripping through the palette
	// gives back the colour asked for. Before the fix this was 080808.
	if got := xterm256(quant256(0, 0, 0)); got != 0x000000 {
		t.Errorf("black round-trips to %06x, want 000000", got)
	}
	if got := xterm256(quant256(255, 255, 255)); got != 0xffffff {
		t.Errorf("white round-trips to %06x, want ffffff", got)
	}
}

// solidRGB builds a cols*rows rgb24 frame (one pixel per cell) of one colour.

// solidRGB builds a cols*rows rgb24 frame (one pixel per cell) of one colour.
func solidRGB(cols, rows int, r, g, b byte) []byte {
	f := make([]byte, cols*rows*3)
	for i := 0; i < cols*rows; i++ {
		f[i*3], f[i*3+1], f[i*3+2] = r, g, b
	}
	return f
}

// The renderer's offset arithmetic, checked by replaying its output the way a
// terminal does: maintain the current colours, apply an SGR when one appears, and
// otherwise carry the previous colour forward. A cell whose SGR is skipped is not
// a bug — skipping it is the whole point of the bandwidth optimisation — so the
// check has to model that rather than counting sequences.
//
// ffmpeg is told scale=cols:rows*2, so the frame is INTERLEAVED: per cell row, a
// full row of top pixels then a full row of bottom pixels. Every pixel encodes
// its own (cell row, column) so a mis-mapped offset cannot go unnoticed.

// The renderer's offset arithmetic, checked by replaying its output the way a
// terminal does: maintain the current colours, apply an SGR when one appears, and
// otherwise carry the previous colour forward. A cell whose SGR is skipped is not
// a bug — skipping it is the whole point of the bandwidth optimisation — so the
// check has to model that rather than counting sequences.
//
// ffmpeg is told scale=cols:rows*2, so the frame is INTERLEAVED: per cell row, a
// full row of top pixels then a full row of bottom pixels. Every pixel encodes
// its own (cell row, column) so a mis-mapped offset cannot go unnoticed.

var sgrRe = regexp.MustCompile(`\x1b\[38;2;(\d+);(\d+);(\d+);48;2;(\d+);(\d+);(\d+)m`)

// cell paints from the renderer output: the i-th glyph and the colour in force
// when it was written.

// cell paints from the renderer output: the i-th glyph and the colour in force
// when it was written.
type paint struct {
	fgR, fgG int
	bgR, bgG int
}

// replay walks the stream, tracking cursor moves, SGR changes and glyphs, and
// returns the colour in force for each cell in write order.

// replay walks the stream, tracking cursor moves, SGR changes and glyphs, and
// returns the colour in force for each cell in write order.
func replay(t *testing.T, out string, cells int) []paint {
	t.Helper()
	res := make([]paint, 0, cells)
	i := 0
	var cur paint
	have := false
	for i < len(out) {
		switch {
		case out[i] == 0x1b:
			m := sgrRe.FindStringSubmatchIndex(out[i:])
			if m != nil {
				g := sgrRe.FindStringSubmatch(out[i:])
				cur.fgR, _ = strconv.Atoi(g[1])
				cur.fgG, _ = strconv.Atoi(g[2])
				cur.bgR, _ = strconv.Atoi(g[4])
				cur.bgG, _ = strconv.Atoi(g[5])
				have = true
				i += m[1]
				continue
			}
			// Any other sequence (cursor move, reset) — skip it.
			j := i + 1
			for j < len(out) && !(out[j] >= 0x40 && out[j] <= 0x7e) {
				j++
			}
			if j < len(out) {
				if out[j] == 'm' {
					cur = paint{}
				}
				i = j + 1
				continue
			}
			i = len(out)
		case out[i] == '\r' || out[i] == '\n':
			i++
		case out[i] >= ' ':
			if have {
				res = append(res, cur)
			}
			// Advance by the whole rune: U+2580 is three bytes and counting bytes
			// would report three glyphs per cell.
			_, size := utf8.DecodeRuneInString(out[i:])
			i += size
		default:
			i++
		}
	}
	return res
}

func coordFrameHalf(cols, rows int) []byte {
	f := make([]byte, cols*rows*2*3)
	set := func(px int, r, g byte) {
		f[px*3], f[px*3+1], f[px*3+2] = r, g, 0
	}
	// Interleaved, because that is what `scale=cols:rows*2` produces: frame row
	// 2y is the top of cell row y, row 2y+1 is its bottom. Encoding it planar
	// instead (both halves adjacent) makes consecutive cell rows overwrite each
	// other, which looks like an off-by-N in the renderer when the test is wrong.
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			set((2*y)*cols+x, byte(10+y), byte(x))
			set((2*y+1)*cols+x, byte(200+y), byte(x))
		}
	}
	return f
}

func TestHalfBlockMapsTopAndBottomRowsCorrectly(t *testing.T) {
	const cols, rows = 6, 4
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, ColorTrue, GlyphHalf)
	if err := r.Draw(coordFrameHalf(cols, rows)); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	got := replay(t, buf.String(), cols*rows)
	if len(got) != cols*rows {
		t.Fatalf("got %d painted cells, want %d", len(got), cols*rows)
	}
	for i, p := range got {
		y, x := i/cols, i%cols
		if p.fgR != 10+y || p.fgG != x {
			t.Errorf("cell (x=%d,y=%d) fg = %d,%d want %d,%d",
				x, y, p.fgR, p.fgG, 10+y, x)
		}
		if p.bgR != 200+y || p.bgG != x {
			t.Errorf("cell (x=%d,y=%d) bg = %d,%d want %d,%d",
				x, y, p.bgR, p.bgG, 200+y, x)
		}
	}
}

func TestCellModeMapsRowsCorrectly(t *testing.T) {
	const cols, rows = 6, 4
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, ColorTrue, GlyphCell)
	f := make([]byte, cols*rows*3)
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			i := y*cols + x
			f[i*3], f[i*3+1], f[i*3+2] = byte(10+y), byte(x), 0
		}
	}
	if err := r.Draw(f); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	got := replay(t, buf.String(), cols*rows)
	if len(got) != cols*rows {
		t.Fatalf("got %d painted cells, want %d", len(got), cols*rows)
	}
	for i, p := range got {
		y, x := i/cols, i%cols
		if p.fgR != 10+y || p.fgG != x {
			t.Errorf("cell (x=%d,y=%d) = %d,%d want %d,%d",
				x, y, p.fgR, p.fgG, 10+y, x)
		}
	}
}
