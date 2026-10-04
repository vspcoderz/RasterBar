package main

import (
	"math"
	"strconv"
	"strings"
)

// The playback HUD: the three rows computeLayout already budgets as
// `chromeRows` and that nothing ever drew (see ascii.go).
//
// Everything here is pure formatting. The HUD is repainted on a timer rather
// than per frame, and the position only changes once a second, so there is no
// reason for any of this to touch a frame, a process, or the clock.

// Bar glyphs. Box-drawing rather than ASCII so the bar reads as a solid line,
// and distinct enough to stay legible on a dark background.
//
// Both are East Asian Ambiguous width, which is the same class of problem
// resolveGlyph probes for with U+2580. The difference is that a mis-sized bar
// shifts the clock text along one line instead of corrupting every row of the
// grid, so it is not worth a second terminal round trip to detect.
const (
	barFilled = "━"
	barEmpty  = "─"
)

// formatClock renders a position as m:ss, or h:mm:ss past an hour.
//
// Delegates to formatDuration instead of reimplementing the shape: the browse
// list already prints durations that way, and one format means a track reads
// the same before and during playback.
func formatClock(sec float64) string {
	if sec < 0 || math.IsNaN(sec) {
		sec = 0
	}
	return formatDuration(int(math.Round(sec)))
}

// progressBar renders playback position as exactly width cells.
//
// Returns "" when there is nothing meaningful to draw — an unknown duration (a
// live stream) or no space — so the caller can fall back to elapsed-only rather
// than paint a bar stuck at zero.
//
// Filled cells are floored, not rounded, so the bar never claims to be ahead of
// the audio. A player that shows more progress than has played is worse than one
// that lags by a pixel.
func progressBar(pos, dur float64, width int) string {
	if width <= 0 || dur <= 0 || math.IsNaN(dur) || math.IsNaN(pos) {
		return ""
	}
	filled := int(pos / dur * float64(width))
	if filled < 0 {
		filled = 0
	}
	if filled > width {
		filled = width
	}
	return strings.Repeat(barFilled, filled) + strings.Repeat(barEmpty, width-filled)
}

// hudRows renders the chrome block: title, progress, key hints.
//
// Kept as one function returning the lines rather than three, because the caller
// has to repaint them as a unit and knows the terminal height; splitting them
// would mean passing that budget around to three call sites.
type hud struct {
	title   string
	channel string
	pos     float64
	dur     float64
	volume  int  // 0-100
	muted   bool // the session was started muted; volume is not in effect
	paused  bool
	queue   int // position in the queue, 1-based; 0 = single track
	total   int
	// showHints is false once the key hints have faded. They come back on the
	// next keypress, so a player does not permanently spend a row reminding you
	// what space does.
	showHints bool
}

// lines renders the HUD to exactly `width` columns per row.
func (h hud) lines(width int) []string {
	if width < 8 {
		width = 8
	}

	// Row 1: title, the channel, and the queue position when there is a queue.
	head := h.title
	if h.channel != "" {
		head += " — " + h.channel
	}
	switch {
	case h.queue > 0 && h.total > 1:
		head = pad2(h.queue) + "/" + strconv.Itoa(h.total) + "  " + head
	case h.total > 1:
		head = strconv.Itoa(h.total) + " tracks  " + head
	}

	// Row 2: the bar and the clock. The clock is fixed-width ("h:mm:ss" or
	// "m:ss" plus " / ") so the bar can claim the rest without the line ever
	// changing length, which would make the whole HUD jitter.
	clock := formatClock(h.pos)
	if h.dur > 0 {
		clock += " / " + formatClock(h.dur)
	}
	// "  " separates the bar from the clock, so the bar gets the width minus the
	// clock and those two spaces. Getting this off by one makes the row a cell
	// short and strands a stale character at the end of the line.
	clockWidth := len([]rune(clock)) + 2
	barWidth := width - clockWidth
	if barWidth < 4 {
		// Too narrow for a usable bar: the clock matters more than progress.
		return []string{
			fit(head, width),
			fit(clock, width),
			fit(h.footer(), width),
		}
	}
	bar := progressBar(h.pos, h.dur, barWidth)

	return []string{
		fit(head, width),
		// fit() matters on this row: progressBar returns "" when the duration is
		// unknown, so the bar contributes zero cells and the row would otherwise
		// be short — leaving a stale tail and breaking the fixed-width invariant
		// both renderers depend on.
		fit(bar+"  "+clock, width),
		fit(h.footer(), width),
	}
}

// footer is row 3: the pause banner while paused, otherwise the volume state,
// otherwise the key hints until they fade.
//
// The hints fade because a permanent hint row is furniture the eye learns to
// skip; they come back on the next keypress, which is exactly when someone
// needs them. Paused is exempt — a banner that vanishes on its own is worse than
// one that stays.
func (h hud) footer() string {
	if h.paused {
		return "PAUSED  space play  ←/→ seek  n/p next  +/- vol  q quit"
	}
	if h.muted {
		return "muted   ←/→ seek  space pause  q quit"
	}
	if h.volume != 100 {
		return "vol " + strconv.Itoa(h.volume) + "%   ←/→ seek  space pause  q quit"
	}
	if !h.showHints {
		return ""
	}
	return "space pause  ←/→ seek  n/p next  q quit"
}

// pad2 zero-pads to two digits, for the queue counter.
func pad2(n int) string {
	if n < 10 {
		return "0" + strconv.Itoa(n)
	}
	return strconv.Itoa(n)
}

// fit truncates to width and then pads with spaces, so every HUD row is exactly
// width columns.
//
// Padding is not cosmetic. The renderers diff against what they last painted, so
// a row that shrinks leaves the tail of the previous, longer row on screen —
// stale characters that nothing ever clears. Rows are fixed-width by
// construction so that cannot happen.
func fit(s string, width int) string {
	s = truncate(s, width)
	if pad := width - len([]rune(s)); pad > 0 {
		s += strings.Repeat(" ", pad)
	}
	return s
}
