package main

import (
	"strings"
	"testing"
)

// --- the HUD strip -----------------------------------------------------------

// TestMiniBarsIsExactlyWidth is the fixed-width invariant every HUD row depends on.
//
// The renderers diff against the previous paint, so a row that changes length
// leaves the tail of the longer previous row on screen forever. This is the same
// invariant fit() enforces, and the strip has to hold it too.
func TestMiniBarsIsExactlyWidth(t *testing.T) {
	for _, width := range []int{1, 5, 20, 80, 200} {
		for _, n := range []int{0, 1, 48, 200} {
			b := make([]float64, n)
			for i := range b {
				b[i] = float64(i) / float64(maxInt(n, 1))
			}
			got := miniBars(b, width, nil)
			if len([]rune(got)) != width {
				t.Errorf("miniBars(%d bands, width %d) is %d cells, want %d",
					n, width, len([]rune(got)), width)
			}
		}
	}
}

func TestMiniBarsEmptySourceIsBlankNotPanicking(t *testing.T) {
	got := miniBars(nil, 20, nil)
	if len(got) != 20 {
		t.Fatalf("length %d, want 20", len(got))
	}
	if strings.TrimSpace(got) != "" {
		t.Errorf("no bands gave %q, want blanks", got)
	}
}

func TestMiniBarsIsMonotonic(t *testing.T) {
	// Quiet must lay down less ink than loud, or the strip is noise.
	//
	// Compared as ramp indices, never as bytes. The ramp is deliberately not
	// monotonic in codepoint order ('^' is 0x5E, '`' is 0x60) because ink density
	// is a property of the font, not the encoding -- see the note on `ramp` in
	// render.go. Comparing bytes therefore compares nothing.
	//
	// This test passed for years by accident: miniBars emitted ramp *indices*,
	// and indices are ordered numbers, so the byte comparison was really
	// comparing two integers and agreed with itself. The moment miniBars emitted
	// real glyphs it failed on '$' < 'i', which is the ramp working as designed
	// and the test measuring the wrong property. It is here because the fix for
	// the newline bug is what exposed it.
	idx := func(g byte) int { return strings.IndexByte(ramp, g) }
	quiet := []float64{0.05, 0.05, 0.05, 0.05}
	loud := []float64{0.95, 0.95, 0.95, 0.95}
	q := miniBars(quiet, 4, nil)
	l := miniBars(loud, 4, nil)
	for i := range q {
		qi, li := idx(q[i]), idx(l[i])
		if qi < 0 || li < 0 {
			t.Fatalf("cell %d: %q / %q is not in the ramp", i, rune(q[i]), rune(l[i]))
		}
		if qi >= li {
			t.Errorf("cell %d: quiet %q (ramp %d) is not lighter than loud %q (ramp %d)",
				i, rune(q[i]), qi, rune(l[i]), li)
		}
	}
}

// TestHudStripAddsARow pins the layout coupling.
//
// Non-empty strip means four rows, and empty means three. The render loop indexes
// the returned lines and writes them below the grid, so a count that did not match
// the layout's chrome budget would either overwrite the video or leave a stale row.

// TestHudStripAddsARow pins the layout coupling.
//
// Non-empty strip means four rows, and empty means three. The render loop indexes
// the returned lines and writes them below the grid, so a count that did not match
// the layout's chrome budget would either overwrite the video or leave a stale row.
func TestHudStripAddsARow(t *testing.T) {
	base := hud{title: "t", pos: 10, dur: 100, queue: 1, total: 3}
	if got := len(base.lines(80)); got != hudStripRows {
		t.Errorf("without a strip: %d rows, want %d", got, hudStripRows)
	}
	withStrip := base
	withStrip.strip = strings.Repeat("=", 80)
	if got := len(withStrip.lines(80)); got != hudStripRows+1 {
		t.Errorf("with a strip: %d rows, want %d", got, hudStripRows+1)
	}
}

// TestHudLinesCarryNoControlCharacters is the regression test for the bug that
// shipped the whole phase as "the visualizer ghosts".
//
// miniBars writes a string rather than a grid, so it has to index the ramp
// itself -- rampFor returns an index. It did not, and every ramp index below 32
// went out as a control character: index 10 is LF. The strip row then wrote ~192
// newlines at row 46, the bottom of the terminal, which scrolled the whole screen
// away several times a second. The diff cache cannot see a scroll, so it skipped
// every cell that had moved and the grid lost both its content and its background.
//
// Widths are swept because the ramp index that a given level maps to depends on
// the value, so a single level would only ever expose one control character.

// TestHudLinesCarryNoControlCharacters is the regression test for the bug that
// shipped the whole phase as "the visualizer ghosts".
//
// miniBars writes a string rather than a grid, so it has to index the ramp
// itself -- rampFor returns an index. It did not, and every ramp index below 32
// went out as a control character: index 10 is LF. The strip row then wrote ~192
// newlines at row 46, the bottom of the terminal, which scrolled the whole screen
// away several times a second. The diff cache cannot see a scroll, so it skipped
// every cell that had moved and the grid lost both its content and its background.
//
// Widths are swept because the ramp index that a given level maps to depends on
// the value, so a single level would only ever expose one control character.
func TestHudLinesCarryNoControlCharacters(t *testing.T) {
	for _, width := range []int{20, 80, 192} {
		for _, level := range []float64{0, 0.15, 0.5, 0.85, 1} {
			for _, peak := range []float64{0, 1} {
				h := hud{
					title: "Track", channel: "Ch", pos: 10, dur: 100,
					queue: 1, total: 3,
					strip: miniBars([]float64{level}, width, []float64{peak}),
				}
				for i, line := range h.lines(width) {
					for j, r := range []rune(line) {
						if r < 0x20 || r == 0x7f {
							t.Fatalf("width %d level %.2f peak %.1f: line %d rune %d is control character %q (want a glyph)",
								width, level, peak, i, j, r)
						}
					}
				}
			}
		}
	}
}

// TestMiniBarsWritesGlyphsNotIndices pins the same boundary from the other side.
//
// A ramp index that happens to be printable is still the wrong character: index
// 11 is '!' whether or not that is the glyph the ramp wanted there. So the
// assertion is that every cell is a member of the ramp, not merely visible.

// TestMiniBarsWritesGlyphsNotIndices pins the same boundary from the other side.
//
// A ramp index that happens to be printable is still the wrong character: index
// 11 is '!' whether or not that is the glyph the ramp wanted there. So the
// assertion is that every cell is a member of the ramp, not merely visible.
func TestMiniBarsWritesGlyphsNotIndices(t *testing.T) {
	bands := make([]float64, 48)
	for i := range bands {
		bands[i] = float64(i) / float64(len(bands))
	}
	got := miniBars(bands, 64, bands)
	if len([]rune(got)) != 64 {
		t.Fatalf("miniBars returned %d runes, want 64", len([]rune(got)))
	}
	for i, r := range []rune(got) {
		if r == ' ' {
			continue
		}
		if !strings.ContainsRune(ramp, r) {
			t.Fatalf("cell %d is %q, which is not in the ramp; miniBars appears to be "+
				"emitting ramp indices as bytes", i, r)
		}
	}
}

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

// TestChromeRowsForMatchesTheHud is the coupling that would otherwise be a pair of
// constants drifting apart.
func TestChromeRowsForMatchesTheHud(t *testing.T) {
	if chromeRowsFor(false) != hudStripRows {
		t.Errorf("chromeRowsFor(false) = %d, want %d", chromeRowsFor(false), hudStripRows)
	}
	if chromeRowsFor(true) != hudStripRows+1 {
		t.Errorf("chromeRowsFor(true) = %d, want %d", chromeRowsFor(true), hudStripRows+1)
	}
}

// TestHudStatusOutranksTheFooter pins the priority order.
//
// A status line is the answer to a keypress the user just made, and the four
// seconds it lives for are when they are still looking for it.

// TestHudStatusOutranksTheFooter pins the priority order.
//
// A status line is the answer to a keypress the user just made, and the four
// seconds it lives for are when they are still looking for it.
func TestHudStatusOutranksTheFooter(t *testing.T) {
	h := hud{paused: true, volume: 50, showHints: true, status: "viz waterfall"}
	if got := h.footer(); !strings.Contains(got, "viz waterfall") {
		t.Errorf("footer = %q, want the status line to win", got)
	}
	h.status = ""
	if got := h.footer(); !strings.Contains(got, "PAUSED") {
		t.Errorf("footer = %q, want the pause banner once the status expires", got)
	}
}

// TestStripCostsTheVideoAGridRow is the whole argument for putting the strip in the
// chrome rather than over the grid.
//
// If the strip were free the video would keep all its rows and there would be no
// reason for the layout to care. It is not free: it takes a row, and that is what
// makes the trade visible.

// TestStripCostsTheVideoAGridRow is the whole argument for putting the strip in the
// chrome rather than over the grid.
//
// If the strip were free the video would keep all its rows and there would be no
// reason for the layout to care. It is not free: it takes a row, and that is what
// makes the trade visible.
func TestStripCostsTheVideoAGridRow(t *testing.T) {
	plain := computeLayout(80, 24, 0, 0, chromeRowsFor(false))
	withStrip := computeLayout(80, 24, 0, 0, chromeRowsFor(true))
	if withStrip.rows >= plain.rows {
		t.Errorf("strip did not cost a row: plain %d, with strip %d", plain.rows, withStrip.rows)
	}
	if withStrip.cols != plain.cols {
		t.Errorf("strip changed the width: plain %d, with strip %d", plain.cols, withStrip.cols)
	}
}

// TestMusicFPSFallsWithTheGrid is the low-end budget.
//
// Repainting a whole grid 30 times a second is fine at 80x22 and hopeless at
// 300x120, so the rate has to track the cell count rather than being a constant.

func TestFormatDuration(t *testing.T) {
	for in, want := range map[int]string{3674: "1:01:14", 65: "1:05", 3600: "1:00:00", 59: "0:59"} {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short string altered: %q", got)
	}
	if got := truncate("abcdefghij", 5); got != "abcd…" {
		t.Errorf("got %q, want abcd…", got)
	}
}

// --- HUD (S2) ---------------------------------------------------------------
//
// The clock delegates to formatDuration, which the browse list already uses, so
// a duration reads identically in the list and on the progress bar.

func TestFormatClockUsesTUIShape(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0:00"},
		{83, "1:23"},
		{83.4, "1:23"},
		{3723, "1:02:03"},
		{-5, "0:00"}, // a negative position is meaningless; show zero
	}
	for _, c := range cases {
		if got := formatClock(c.in); got != c.want {
			t.Errorf("formatClock(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestProgressBarFillsProportionally(t *testing.T) {
	cases := []struct {
		pos, dur float64
		width    int
		want     string
	}{
		{0, 100, 10, "──────────"},
		{100, 100, 10, "━━━━━━━━━━"},
		{50, 100, 10, "━━━━━─────"},
		{25, 100, 10, "━━────────"}, // floor, not round
	}
	for _, c := range cases {
		got := progressBar(c.pos, c.dur, c.width, -1)
		if got != c.want {
			t.Errorf("progressBar(%v,%v,%d) = %q, want %q", c.pos, c.dur, c.width, got, c.want)
		}
	}
}

func TestProgressBarClampsOutOfRange(t *testing.T) {
	// A position past the end (drift, or a bad duration) must not overflow the
	// bar: the line has a fixed cell budget and would wrap.
	if got := progressBar(150, 100, 4, -1); got != "━━━━" {
		t.Errorf("pos>dur = %q, want full bar", got)
	}
	if got := progressBar(-5, 100, 4, -1); got != "────" {
		t.Errorf("negative pos = %q, want empty bar", got)
	}
}

func TestProgressBarWidthIsExact(t *testing.T) {
	for _, width := range []int{1, 3, 20, 80} {
		got := progressBar(37, 90, width, -1)
		if n := len([]rune(got)); n != width {
			t.Errorf("progressBar width %d produced %d cells: %q", width, n, got)
		}
	}
}

func TestProgressBarNoDurationDrawsNothing(t *testing.T) {
	// Live streams have no duration. An empty string is what makes the caller
	// fall back to elapsed-only instead of painting a permanently empty bar.
	if got := progressBar(30, 0, 10, -1); got != "" {
		t.Errorf("dur=0 = %q, want empty", got)
	}
	if got := progressBar(30, 90, 0, -1); got != "" {
		t.Errorf("width=0 = %q, want empty", got)
	}
}

func TestHUDRowsAreExactlyWidth(t *testing.T) {
	// Every row must fill the line. A short row leaves the tail of the previous,
	// longer row on screen, because both renderers diff against what they last
	// painted and nothing clears the remainder.
	for _, width := range []int{20, 40, 80, 120} {
		h := hud{title: "Lofi Girl - 1 A.M. Study Session", pos: 83, dur: 3674, volume: 80}
		for i, row := range h.lines(width) {
			if n := len([]rune(row)); n != width {
				t.Errorf("width %d row %d = %d cells: %q", width, i, n, row)
			}
		}
	}
}

func TestHUDShowsBarAndClock(t *testing.T) {
	rows := hud{title: "T", pos: 50, dur: 100}.lines(40)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	// 40 - (len("0:50 / 1:40") + 2) = 27 bar cells, half filled, then "  0:50 / 1:40".
	if !strings.Contains(rows[1], "0:50 / 1:40") {
		t.Errorf("row 1 = %q, want the clock", rows[1])
	}
	bar := strings.TrimRight(strings.SplitN(rows[1], "  0", 2)[0], " ")
	if got := len([]rune(bar)); got != 27 {
		t.Errorf("bar = %d cells (%q), want 27", got, bar)
	}
	if strings.Count(bar, barFilled) != 13 { // floor(0.5 * 27)
		t.Errorf("bar filled = %d cells, want 13", strings.Count(bar, barFilled))
	}
}

func TestHUDLiveStreamHasNoBar(t *testing.T) {
	// A live stream has no duration, so there is no progress to show. The row
	// falls back to elapsed only rather than a bar stuck at zero.
	rows := hud{title: "T", pos: 30, dur: 0}.lines(40)
	if strings.ContainsRune(rows[1], '━') || strings.ContainsRune(rows[1], '─') {
		t.Errorf("row 1 = %q, want no bar glyphs for unknown duration", rows[1])
	}
	if !strings.Contains(rows[1], "0:30") {
		t.Errorf("row 1 = %q, want elapsed clock", rows[1])
	}
}

func TestHUDQueueCounterAndPausedHints(t *testing.T) {
	rows := hud{title: "T", pos: 0, dur: 10, queue: 3, total: 12, paused: true}.lines(60)
	if !strings.HasPrefix(rows[0], "03/12") {
		t.Errorf("row 0 = %q, want the 03/12 queue counter", rows[0])
	}
	if !strings.HasPrefix(rows[2], "PAUSED") {
		t.Errorf("row 2 = %q, want pause hints", rows[2])
	}
}

func TestHUDSingleTrackHasNoQueueCounter(t *testing.T) {
	// One track is not a queue. A "1/1" prefix is noise.
	rows := hud{title: "T", pos: 0, dur: 10, queue: 1, total: 1}.lines(60)
	if strings.HasPrefix(rows[0], "01/01") {
		t.Errorf("row 0 = %q, want no queue counter for a single track", rows[0])
	}
}

func TestHUDSurvivesATinyTerminal(t *testing.T) {
	// Below the bar's minimum the clock still has to render. An empty or
	// panicking HUD on a 20-column window is worse than no bar.
	for _, width := range []int{1, 8, 12} {
		rows := hud{title: "Long Title Here", pos: 83, dur: 3674, volume: 100}.lines(width)
		if len(rows) != 3 {
			t.Fatalf("width %d: got %d rows, want 3", width, len(rows))
		}
	}
}

// --- Transport state machine (S1) -------------------------------------------
//
// The player is tested against a fake media backend, so every case here runs
// with no ffmpeg, no mpv and no network. These are transport semantics — the
// rules a user feels — not process plumbing.

func TestHUDRowWidthHoldsWithUnknownDuration(t *testing.T) {
	// The bar row skips fit(), so its width has to come out of the arithmetic.
	// Checked for the unknown-duration case (no bar at all) and for an hour-plus
	// clock, where the timestamp is two characters wider than at the start.
	for _, width := range []int{20, 40, 80} {
		for _, dur := range []float64{0, 12, 3674, 36000, 45296} {
			rows := hud{title: "Title", pos: 3, dur: dur, showHints: true}.lines(width)
			for i, row := range rows {
				if n := len([]rune(row)); n != width {
					t.Errorf("width %d dur %v: row %d = %d cells: %q",
						width, dur, i, n, row)
				}
			}
		}
	}
}

func TestHUDFooterShowsMutedNotVolume(t *testing.T) {
	// Under -m the volume is not in effect, so printing "vol 100%" is a lie.
	rows := hud{title: "T", pos: 0, dur: 10, volume: 100, muted: true, showHints: true}.lines(60)
	if !strings.Contains(rows[2], "muted") {
		t.Errorf("row 2 = %q, want the muted label", rows[2])
	}
	if strings.Contains(rows[2], "100%") {
		t.Errorf("row 2 = %q, must not claim a volume while muted", rows[2])
	}
}

func TestHUDFooterShowsVolumeAfterChange(t *testing.T) {
	rows := hud{title: "T", pos: 0, dur: 10, volume: 40, showHints: true}.lines(60)
	if !strings.Contains(rows[2], "40%") {
		t.Errorf("row 2 = %q, want the volume", rows[2])
	}
}

func TestHUDFooterFadesHints(t *testing.T) {
	// A permanent hint row is furniture the eye learns to skip.
	faded := hud{title: "T", pos: 0, dur: 10, volume: 100, showHints: false}.lines(60)
	if strings.TrimSpace(faded[2]) != "" {
		t.Errorf("row 2 = %q, want it empty once hints have faded", faded[2])
	}
	// Paused is exempt: a banner that vanishes on its own is worse than one that
	// stays, because it is the only thing telling you playback is stopped.
	paused := hud{title: "T", pos: 0, dur: 10, paused: true, showHints: false}.lines(60)
	if !strings.HasPrefix(paused[2], "PAUSED") {
		t.Errorf("row 2 = %q, want the pause banner to persist", paused[2])
	}
}

func TestHUDTitleShowsChannel(t *testing.T) {
	rows := hud{title: "Song", channel: "Some Channel", pos: 0, dur: 10,
		queue: 1, total: 10}.lines(60)
	if !strings.HasPrefix(rows[0], "01/10  Song — Some Channel") {
		t.Errorf("row 0 = %q, want title and channel", rows[0])
	}
}

// --- black cells and the colour diff ----------------------------------------
//
// A pixel that is true black must still be painted. Skipping it would leave
// whatever the terminal had in that cell showing through, which reads as "black
// pixels leak" — so these pin that the diff treats black as a real colour.
