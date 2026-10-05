package main

import (
	"testing"
)

func TestComputeLayoutFollowsTerminal(t *testing.T) {
	// A small terminal must not request a big video.
	small := computeLayout(80, 24, 0, 0, 3)
	if small.cols != 80 {
		t.Errorf("small cols = %d, want 80", small.cols)
	}
	if small.sourceH > 360 {
		t.Errorf("80x24 requested %dp, want <= 360p", small.sourceH)
	}

	// A large terminal should scale up.
	big := computeLayout(240, 60, 0, 0, 3)
	if big.cols != 240 {
		t.Errorf("big cols = %d, want 240", big.cols)
	}
	if big.sourceH <= small.sourceH {
		t.Errorf("big terminal sourceH %d not greater than small %d", big.sourceH, small.sourceH)
	}

	// Monotonic: more columns must never lower the requested source.
	prev := 0
	for _, c := range []int{40, 80, 120, 160, 200, 240, 300} {
		l := computeLayout(c, 50, 0, 0, 3)
		if l.sourceH < prev {
			t.Errorf("sourceH dropped from %d to %d at %d cols", prev, l.sourceH, c)
		}
		prev = l.sourceH
	}
}

func TestComputeLayoutRespectsRows(t *testing.T) {
	// A short terminal must clamp rows, and the grid must still fit.
	l := computeLayout(200, 12, 0, 0, 3)
	if l.rows >= 12 {
		t.Errorf("rows = %d, must leave room for chrome in a 12-row window", l.rows)
	}
	if l.rows < 4 {
		t.Errorf("rows = %d, want a usable minimum", l.rows)
	}

	// Very tall window: rows follow cols/aspect, not the whole window.
	tall := computeLayout(80, 200, 0, 0, 3)
	if tall.rows >= 200 {
		t.Errorf("rows = %d, want width/aspect rather than full height", tall.rows)
	}
}

func TestComputeLayoutAspect(t *testing.T) {
	// A taller aspect ratio (cells taller than wide) needs fewer rows.
	normal := computeLayout(100, 50, 2.0, 0, 3)
	tall := computeLayout(100, 50, 4.0, 0, 3)
	if tall.rows >= normal.rows {
		t.Errorf("aspect 4.0 gave rows=%d, expected fewer than aspect 2.0 rows=%d",
			tall.rows, normal.rows)
	}

	// Zero/invalid aspect must fall back to the default, not divide by zero.
	zero := computeLayout(100, 50, 0, 0, 3)
	if zero.rows <= 0 {
		t.Error("zero aspect produced no rows")
	}
}

func TestComputeLayoutQualityOverride(t *testing.T) {
	// An explicit floor raises the source above what the grid implies.
	small := computeLayout(80, 24, 0, 0, 3)
	raised := computeLayout(80, 24, 0, Quality(720), 3)
	if raised.sourceH < 720 {
		t.Errorf("quality 720 gave sourceH %d", raised.sourceH)
	}
	if raised.sourceH <= small.sourceH {
		t.Errorf("quality override did not raise resolution: %d vs %d",
			raised.sourceH, small.sourceH)
	}

	// A cap must not raise it.
	capped := computeLayout(300, 60, 0, Quality(240), 3)
	if capped.sourceH > 720 {
		t.Errorf("cap 240 gave %dp, want it capped", capped.sourceH)
	}
}

func TestComputeLayoutFallbacks(t *testing.T) {
	// Non-TTY (zero) sizes must not produce a zero-sized grid.
	l := computeLayout(0, 0, 0, 0, 3)
	if l.cols <= 0 || l.rows <= 0 {
		t.Errorf("fallback grid is %dx%d, want positive", l.cols, l.rows)
	}

	// Absurd terminal sizes are clamped.
	huge := computeLayout(10000, 10000, 0, 0, 3)
	if huge.cols > maxCols {
		t.Errorf("cols = %d, want clamped to %d", huge.cols, maxCols)
	}
	if huge.rows > maxRows {
		t.Errorf("rows = %d, want clamped to %d", huge.rows, maxRows)
	}
}

func TestFPSForGridScalesDown(t *testing.T) {
	small := fpsForGrid(80, 21)
	big := fpsForGrid(200, 57)
	if big > small {
		t.Errorf("big grid fps %d should not exceed small grid fps %d", big, small)
	}
	if small < 6 {
		t.Errorf("small grid fps = %d, want at least 6", small)
	}
	// Cells-per-second must stay in a band a terminal can absorb. Test the
	// clamped layout, not the raw helper: computeLayout is what actually runs.
	for _, tc := range [][2]int{{80, 21}, {120, 40}, {200, 57}, {300, 120}, {400, 200}} {
		l := computeLayout(tc[0], tc[1], 0, 0, 3)
		if cps := l.cols * l.rows * l.fps; cps > 150000 {
			t.Errorf("%dx%d -> grid %dx%d @%dfps = %d cells/sec, too much",
				tc[0], tc[1], l.cols, l.rows, l.fps, cps)
		}
	}
}

func TestTMoveClamps(t *testing.T) {
	tracks := make([]Track, 5)
	tui := NewTUI(nil, nil, "q", tracks)
	tui.move(-1)
	if tui.cursor != 0 {
		t.Errorf("cursor = %d, want 0", tui.cursor)
	}
	tui.move(100)
	if tui.cursor != 4 {
		t.Errorf("cursor = %d, want 4", tui.cursor)
	}
}
