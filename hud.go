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
	// barMark is the tick left where playback was before the last seek. A heavy
	// box-drawing vertical, so it reads as a distinct event rather than as more
	// progress — which is the one thing it must never be mistaken for.
	barMark = "┃"
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
//
// markAt, when >= 0, is a position to leave a tick at: where playback was before
// the last seek. Without it, a jump across forty minutes and a nudge of one second
// look identical, and the whole point of jumping is not being able to tell where
// you were before.
func progressBar(pos, dur float64, width int, markAt float64) string {
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
	cells := []rune(strings.Repeat(barFilled, filled) + strings.Repeat(barEmpty, width-filled))
	if markAt >= 0 && !math.IsNaN(markAt) {
		if i := int(markAt / dur * float64(width)); i >= 0 && i < len(cells) {
			cells[i] = []rune(barMark)[0]
		}
	}
	return string(cells)
}

// parseTimestamp resolves what the user typed into a media position.
//
// It is the inverse of formatDuration and deliberately shares its shape: a
// timestamp the player accepts and a timestamp it displays should be written the
// same way, or the prompt teaches a syntax the clock does not use.
//
// Accepted, all of them:
//
//	90           bare seconds
//	1:30         minutes:seconds       1:02:03   hours:minutes:seconds
//	90s 2m 1h2m3s                      explicit units
//	+30  -1:30                          relative to where playback is now
//	50%          fraction of the duration
//
// A leading sign means relative, and applies to a percentage too: -50% is "go
// back half a track", which is a thing you want at the end of one.
//
// Fields are deliberately not range-checked. `1:90` is 150 seconds and `90:00` is
// 90 minutes; both are what the person typing them meant, and clamping to the
// shape of a wall clock would silently do something else.
func parseTimestamp(s string, pos, dur float64) (float64, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, false
	}
	rel := false
	sign := 1.0
	switch s[0] {
	case '+':
		rel, s = true, s[1:]
	case '-':
		rel, sign, s = true, -1, s[1:]
	}
	if s == "" {
		return 0, false
	}

	// A percentage is a fraction of the track, so it needs a duration to mean
	// anything. On a live stream there is none, and the entry is refused rather
	// than resolved against zero.
	if pct, isPct := strings.CutSuffix(s, "%"); isPct {
		f, err := strconv.ParseFloat(strings.TrimSpace(pct), 64)
		if err != nil || dur <= 0 || f < 0 {
			return 0, false
		}
		if rel {
			return pos + sign*f/100*dur, true
		}
		return f / 100 * dur, true
	}

	var (
		secs float64
		ok   bool
	)
	switch {
	case strings.Contains(s, ":"):
		secs, ok = parseColonTime(s)
	case hasTimeUnit(s):
		secs, ok = parseUnitTime(s)
	default:
		var f float64
		f, err := strconv.ParseFloat(s, 64)
		secs, ok = f, err == nil
	}
	if !ok || secs < 0 || math.IsNaN(secs) || math.IsInf(secs, 0) {
		return 0, false
	}
	if rel {
		return pos + sign*secs, true
	}
	return secs, true
}

// parseColonTime reads `m:s` or `h:m:s`.
func parseColonTime(s string) (float64, bool) {
	parts := strings.Split(s, ":")
	if len(parts) < 2 || len(parts) > 3 {
		return 0, false
	}
	// Right to left: the rightmost field is always seconds, and the scale of each
	// field to its left depends on how many fields there are, not on its index.
	var total, scale = 0.0, 1.0
	for i := len(parts) - 1; i >= 0; i-- {
		f, err := strconv.ParseFloat(strings.TrimSpace(parts[i]), 64)
		if err != nil || f < 0 {
			return 0, false
		}
		total += f * scale
		scale *= 60
	}
	return total, true
}

// hasTimeUnit reports whether s contains a letter, which is what separates `90s`
// from a bare `90`. Guessing is not an option: `2m` is two minutes and `2` is two
// seconds, and only the letter tells them apart.
func hasTimeUnit(s string) bool {
	for i := 0; i < len(s); i++ {
		if c := s[i]; (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			return true
		}
	}
	return false
}

// parseUnitTime reads `1h2m3s`, tolerating spaces between the terms.
//
// Every number must carry a unit. `1h30` is refused rather than guessed at: the
// two readings are ninety minutes and ninety seconds, and nothing in the input
// says which was meant.
func parseUnitTime(s string) (float64, bool) {
	s = strings.ReplaceAll(s, " ", "")
	var total float64
	i := 0
	for i < len(s) {
		start := i
		for i < len(s) && (s[i] == '.' || (s[i] >= '0' && s[i] <= '9')) {
			i++
		}
		if start == i {
			return 0, false // a unit with no number in front of it
		}
		f, err := strconv.ParseFloat(s[start:i], 64)
		if err != nil {
			return 0, false
		}
		if i >= len(s) {
			return 0, false // a number with no unit after it
		}
		var scale float64
		switch lowerASCII(s[i]) {
		case 'h':
			scale = 3600
		case 'm':
			scale = 60
		case 's':
			scale = 1
		default:
			return 0, false
		}
		total += f * scale
		i++
	}
	return total, i > 0
}

func lowerASCII(c byte) byte {
	if c >= 'A' && c <= 'Z' {
		return c + ('a' - 'A')
	}
	return c
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
	// chapter is the title of the chapter being played, if the track has any.
	chapter string
	// mark is where playback was before the last seek. Paired with hasMark
	// rather than using a negative sentinel, because the zero value of a struct
	// literal has to mean "no marker" — a bare hud{...} in a test would otherwise
	// silently grow a tick at the start of the bar.
	mark    float64
	hasMark bool
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
	mark := -1.0
	if h.hasMark {
		mark = h.mark
	}
	bar := progressBar(h.pos, h.dur, barWidth, mark)

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
// otherwise the chapter being played, otherwise the key hints until they fade.
//
// The hints fade because a permanent hint row is furniture the eye learns to
// skip; they come back on the next keypress, which is exactly when someone
// needs them. Paused is exempt — a banner that vanishes on its own is worse than
// one that stays.
//
// A chapter title takes the row the hints have vacated. It is the one piece of
// information that is worth a permanent home in the chrome: it is what tells you
// where you are in something with structure, and it costs nothing while the hints
// are up because those are the moments you are pressing keys anyway.
func (h hud) footer() string {
	if h.paused {
		return "PAUSED  space play  ←/→ seek  n/p next  +/- vol  : jump  q quit"
	}
	if h.chapter != "" {
		return "▸ " + h.chapter
	}
	if h.muted {
		return "muted   ←/→ seek  space pause  : jump  q quit"
	}
	if h.volume != 100 {
		return "vol " + strconv.Itoa(h.volume) + "%   ←/→ seek  space pause  : jump  q quit"
	}
	if !h.showHints {
		return ""
	}
	return "space pause  ←/→ seek  ,/. fine  </> 60s  : jump  n/p next  q quit"
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
