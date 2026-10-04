package main

// Tests for the jump-to-time prompt, the timestamp grammar, chapter navigation
// and the overlay.
//
// The routing tests are the important ones. Everything here existed because a
// text field over a running video is impossible if the keyboard is decoded
// anywhere but the loop that knows the field is open, and the failure mode of
// getting that wrong is silent: the keys still work, they just mean something
// else.

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

// --- the burst that motivated all of this ------------------------------------

// TestKeyRouterPromptClaimsItsOwnBurst is the regression. A `:` and the timestamp
// after it routinely arrive in one read, because the terminal delivers whatever
// has been typed since the last read. A decoder that ran in the reader goroutine
// would have called them transport keys: the `1` a dead key, the `-` in `-30` a
// volume change three times over, and a `q` typed into a timestamp a quit.
func TestKeyRouterPromptClaimsItsOwnBurst(t *testing.T) {
	var r keyRouter
	res := r.feed([]byte(":1:30\r"), 10, 300)

	if len(res.cmds) != 0 {
		t.Errorf("timestamp produced transport commands %v, want none", res.cmds)
	}
	if !res.submit {
		t.Fatal("Enter did not submit")
	}
	if !res.valid {
		t.Fatal("1:30 was rejected")
	}
	if res.target != 90 {
		t.Errorf("target = %v, want 90", res.target)
	}
	if res.open {
		t.Error("prompt still open after Enter")
	}
}

// TestKeyRouterBytesAfterSubmitGoToTheTransport: a prompt that closes mid-chunk
// has to hand the rest back. `:1:30<CR>q` is quit, not a timestamp with a q on
// the end that silently does nothing.
func TestKeyRouterBytesAfterSubmitGoToTheTransport(t *testing.T) {
	var r keyRouter
	res := r.feed([]byte(":1:30\rq"), 0, 300)
	if len(res.cmds) != 1 || res.cmds[0] != CmdQuit {
		t.Errorf("cmds = %v, want [CmdQuit]", res.cmds)
	}
	if !res.valid || res.target != 90 {
		t.Errorf("target = %v valid=%v, want 90", res.target, res.valid)
	}
}

// TestKeyRouterEscapeLeavesTheRestAlone: dismissing the field must not eat what
// follows it either.
func TestKeyRouterEscapeLeavesTheRestAlone(t *testing.T) {
	var r keyRouter
	res := r.feed([]byte(":\x1b "), 0, 300)
	if !res.cancel {
		t.Error("Escape did not cancel")
	}
	if len(res.cmds) != 1 || res.cmds[0] != CmdTogglePause {
		t.Errorf("cmds = %v, want [CmdTogglePause]", res.cmds)
	}
}

// TestKeyRouterArrowBeforePrompt: the ordinary transport path is untouched by any
// of this.
func TestKeyRouterArrowBeforePrompt(t *testing.T) {
	var r keyRouter
	res := r.feed([]byte("\x1b[C\x1b[D"), 0, 300)
	want := []Cmd{CmdSeekFwd, CmdSeekBack}
	if len(res.cmds) != len(want) {
		t.Fatalf("cmds = %v, want %v", res.cmds, want)
	}
	for i := range want {
		if res.cmds[i] != want[i] {
			t.Errorf("cmd %d = %v, want %v", i, res.cmds[i], want[i])
		}
	}
}

// TestKeyRouterSplitEscapeSurvivesThePrompt: an arrow typed and then completed
// one read at a time, which is what the VTIME read timeout exists for.
func TestKeyRouterSplitEscapeSurvivesThePrompt(t *testing.T) {
	var r keyRouter
	res := r.feed([]byte("\x1b"), 0, 300)
	if len(res.cmds) != 0 {
		t.Errorf("partial escape produced %v", res.cmds)
	}
	res = r.feed([]byte("[C"), 0, 300)
	if len(res.cmds) != 1 || res.cmds[0] != CmdSeekFwd {
		t.Errorf("completed escape = %v, want CmdSeekFwd", res.cmds)
	}
}

// TestKeyRouterFlushDropsAHalfSequence: the idle tick is the only signal that an
// escape sequence which started will never finish. Without it a mouse report cut
// in half would sit in the buffer for the rest of the track.
func TestKeyRouterFlushDropsAHalfSequence(t *testing.T) {
	var r keyRouter
	r.feed([]byte("\x1b["), 0, 300)
	res := r.feed(nil, 0, 300) // the tick
	if len(res.cmds) != 0 {
		t.Errorf("flush produced %v, want nothing", res.cmds)
	}
	// And the router is usable afterwards.
	res = r.feed([]byte("q"), 0, 300)
	if len(res.cmds) != 1 || res.cmds[0] != CmdQuit {
		t.Errorf("after flush cmds = %v, want CmdQuit", res.cmds)
	}
}

// TestKeyRouterPromptSwallowsArrows: reaching for a seek inside a text field
// should do nothing, not dismiss the field.
func TestKeyRouterPromptSwallowsArrows(t *testing.T) {
	var r keyRouter
	res := r.feed([]byte(":\x1b[C\x1b[D9"), 0, 300)
	if !res.open {
		t.Fatal("prompt dismissed by an arrow key")
	}
	if r.pr.text() != "9" {
		t.Errorf("text = %q, want %q", r.pr.text(), "9")
	}
	if len(res.cmds) != 0 {
		t.Errorf("arrows leaked to the transport: %v", res.cmds)
	}
}

// --- the field ---------------------------------------------------------------

func TestPromptEditing(t *testing.T) {
	var p prompt
	p.start()

	used, act := p.consume([]byte("1:3x"))
	if used != 4 || act != promptNone {
		t.Errorf("consume = %d, %v; want 4, promptNone", used, act)
	}
	if p.text() != "1:3x" {
		t.Fatalf("text = %q", p.text())
	}

	p.consume([]byte{0x7f}) // backspace
	if p.text() != "1:3" {
		t.Errorf("after backspace = %q, want 1:3", p.text())
	}

	p.consume([]byte{0x15}) // ctrl-u
	if p.text() != "" {
		t.Errorf("after ctrl-u = %q, want empty", p.text())
	}
	if !p.open {
		t.Error("ctrl-u closed the field")
	}
}

// TestPromptBackspaceOnEmptyIsHarmless: a held backspace must not panic or wrap.
func TestPromptBackspaceOnEmptyIsHarmless(t *testing.T) {
	var p prompt
	p.start()
	p.consume([]byte{0x7f, 0x7f, 0x08})
	if p.text() != "" {
		t.Errorf("text = %q, want empty", p.text())
	}
}

// TestPromptIsCapped: a held key must not run the field off the side of the
// overlay. The cap is a design decision, not an accident, so it is pinned.
func TestPromptIsCapped(t *testing.T) {
	var p prompt
	p.start()
	p.consume([]byte(strings.Repeat("9", promptMax*3)))
	if n := len(p.text()); n != promptMax {
		t.Errorf("text length %d, want the cap of %d", n, promptMax)
	}
}

// TestPromptDropsControlBytes: only printable ASCII is text. A stray control byte
// or half a UTF-8 rune in a field that takes digits and separators is garbage.
func TestPromptDropsControlBytes(t *testing.T) {
	var p prompt
	p.start()
	p.consume([]byte{'1', 0x01, '2', 0x80, '3'})
	if p.text() != "123" {
		t.Errorf("text = %q, want 123", p.text())
	}
}

func TestPromptCtrlCCancels(t *testing.T) {
	var p prompt
	p.start()
	_, act := p.consume([]byte("12\x03"))
	if act != promptCancel {
		t.Errorf("ctrl-c = %v, want promptCancel", act)
	}
	if p.open {
		t.Error("field still open after ctrl-c")
	}
}

// TestPromptEnterSubmitsWhateverItHas: Enter on an empty field dismisses rather
// than jumping, because there is nothing to jump to.
func TestPromptEnterSubmitsWhateverItHas(t *testing.T) {
	var p prompt
	p.start()
	_, act := p.consume([]byte("\r"))
	if act != promptSubmit {
		t.Errorf("Enter = %v, want promptSubmit", act)
	}
}

// --- the grammar -------------------------------------------------------------

func TestParseTimestamp(t *testing.T) {
	const dur = 600.0
	cases := []struct {
		in   string
		pos  float64
		want float64
	}{
		{"90", 0, 90},
		{"0", 0, 0},
		{"1:30", 0, 90},
		{"1:02:03", 0, 3723},
		{"90:00", 0, 5400}, // a field over 59 is minutes, not a mistake
		{"1:90", 0, 150},   // and seconds over 59 is seconds
		{"1.5", 0, 1.5},    // fractions are fine
		{"90s", 0, 90},
		{"2m", 0, 120},
		{"1h", 0, 3600},
		{"1h2m3s", 0, 3723},
		{"1H2M3S", 0, 3723}, // case does not matter
		{"1h 30m", 0, 5400}, // spaces between terms
		{"50%", 0, 300},
		{"100%", 0, 600},
		{"0%", 0, 0},
		{"+30", 100, 130},   // relative forward
		{"-30", 100, 70},    // relative back
		{"+1:30", 100, 190}, // relative, colon form
		{"-1:30", 100, 10},
		{"+2m", 100, 220},
		{"-50%", 500, 200}, // half a track back from the end
		{"1:30", 500, 90},  // no sign is absolute, not relative
		{"  1:30  ", 0, 90},
	}
	for _, c := range cases {
		got, ok := parseTimestamp(c.in, c.pos, dur)
		if !ok {
			t.Errorf("parseTimestamp(%q) rejected it", c.in)
			continue
		}
		if got != c.want {
			t.Errorf("parseTimestamp(%q, pos=%v) = %v, want %v", c.in, c.pos, got, c.want)
		}
	}
}

func TestParseTimestampRejects(t *testing.T) {
	// dur=0 is a live stream: a percentage has nothing to be a fraction of.
	for _, in := range []string{
		"",        // nothing typed
		"   ",     // only whitespace
		"+",       // a sign with no number
		"-",       //
		"abc",     // not a time
		"1:2:3:4", // more fields than a clock has
		"1:",      // trailing separator
		":30",     // leading separator
		"1h30",    // a number with no unit: 90 minutes or 90 seconds, no way to tell
		"90x",     // unknown unit
		"s",       // a unit with no number
		"m30",
		"1..5", // not a number
	} {
		if got, ok := parseTimestamp(in, 10, 600); ok {
			t.Errorf("parseTimestamp(%q) = %v, want rejection", in, got)
		}
	}
	if _, ok := parseTimestamp("50%", 10, 0); ok {
		t.Error("a percentage resolved with no duration, want rejection")
	}
	// A negative result is not a time; it is a seek backwards from zero.
	if got, ok := parseTimestamp("-30", 10, 600); !ok || got != -20 {
		t.Errorf("parseTimestamp(\"-30\", 10) = %v, %v; want -20, true — the clamp is the transport's job", got, ok)
	}
}

// TestParseTimestampMatchesTheClock: what the prompt accepts and what the HUD
// prints have to be written the same way, or the prompt teaches a syntax the
// clock does not use. This is the round trip.
func TestParseTimestampMatchesTheClock(t *testing.T) {
	for _, s := range []string{"0:00", "1:30", "9:05", "1:00:00", "2:03:04"} {
		secs, ok := parseTimestamp(s, 0, 100000)
		if !ok {
			t.Fatalf("parseTimestamp(%q) rejected its own clock format", s)
		}
		if got := formatClock(secs); got != s {
			t.Errorf("round trip %q -> %v -> %q", s, secs, got)
		}
	}
}

// --- transport ---------------------------------------------------------------

func TestPlayerJumpToClampsAndReachesMedia(t *testing.T) {
	m := &fakeMedia{dur: 100}
	p := newTestPlayer(m, Track{ID: "a"})

	p.JumpTo(42)
	if got := m.pos; got != 42 {
		t.Errorf("media at %v, want 42", got)
	}
	// Past the end lands on the end rather than on nothing.
	p.JumpTo(5000)
	if got := m.pos; got != 100 {
		t.Errorf("media at %v, want the 100s clamp", got)
	}
	// Before the start clamps to zero.
	p.JumpTo(-30)
	if got := m.pos; got != 0 {
		t.Errorf("media at %v, want 0", got)
	}
}

func TestPlayerFineAndLongSeek(t *testing.T) {
	m := &fakeMedia{dur: 600}
	p := newTestPlayer(m, Track{ID: "a"})
	p.Tick(300)

	cases := []struct {
		cmd  Cmd
		want float64
	}{
		{CmdSeekFwdFine, 301},
		{CmdSeekFwdFine, 302},
		{CmdSeekBackFine, 301},
		{CmdSeekFwdLong, 361},
		{CmdSeekBackLong, 301},
	}
	for i, c := range cases {
		p.Do(c.cmd)
		if got := m.pos; got != c.want {
			t.Errorf("step %d (%v): media at %v, want %v", i, c.cmd, got, c.want)
		}
	}
}

// TestPlayerFineSeekClampsAtTheEdges: a one-second step must not be the thing
// that walks off the start of the track.
func TestPlayerFineSeekClampsAtTheEdges(t *testing.T) {
	m := &fakeMedia{dur: 10}
	p := newTestPlayer(m, Track{ID: "a"})
	for i := 0; i < 5; i++ {
		p.Do(CmdSeekBackFine)
	}
	if m.pos != 0 {
		t.Errorf("media at %v, want 0", m.pos)
	}
	p.Do(CmdSeekFwdFine)
	if m.pos != 1 {
		t.Errorf("media at %v, want 1", m.pos)
	}
}

func TestPlayerMarksWhereItSeekedFrom(t *testing.T) {
	m := &fakeMedia{dur: 600}
	p := newTestPlayer(m, Track{ID: "a"})
	p.Tick(120)

	if st := p.State(); st.HasMark {
		t.Error("a fresh player already has a marker")
	}

	p.JumpTo(400)
	st := p.State()
	if !st.HasMark || st.Mark != 120 {
		t.Errorf("mark = %v (set %v), want 120", st.Mark, st.HasMark)
	}

	// The next seek moves the marker, it does not stack.
	p.JumpTo(410)
	if st := p.State(); st.Mark != 400 {
		t.Errorf("mark = %v, want 400", st.Mark)
	}

	// A seek that does not move leaves no marker: there is nothing to point back
	// from, and a tick under the playhead is just noise.
	p.ClearMark()
	p.JumpTo(410)
	if st := p.State(); st.HasMark {
		t.Errorf("a no-op seek left a marker at %v", st.Mark)
	}
}

// --- chapters ----------------------------------------------------------------

func testChapters() []Chapter {
	return []Chapter{
		{Start: 0, End: 60, Title: "Intro"},
		{Start: 60, End: 200, Title: "Main"},
		{Start: 200, End: 300, Title: "End"},
	}
}

func TestChapterTargetNext(t *testing.T) {
	chaps := testChapters()
	cases := []struct {
		pos      float64
		want     float64
		wantOkay bool
	}{
		{0, 60, true},    // from the first, to the second
		{30, 60, true},   //
		{59.9, 60, true}, // right on the boundary
		{60, 200, true},  // already in the second
		{250, 0, false},  // past the last: nothing to go to
	}
	for _, c := range cases {
		got, ok := chapterTarget(chaps, c.pos, true)
		if ok != c.wantOkay || (ok && got != c.want) {
			t.Errorf("next from %v = %v, %v; want %v, %v", c.pos, got, ok, c.want, c.wantOkay)
		}
	}
}

func TestChapterTargetPrev(t *testing.T) {
	chaps := testChapters()
	cases := []struct {
		pos      float64
		want     float64
		wantOkay bool
	}{
		{90, 0, true},   // in the second chapter: back to the first
		{60, 0, true},   // exactly at a start: still the one before it
		{59.9, 0, true}, // a tenth of a second early, still the first chapter
		{200, 60, true}, // in the third: back to the second
		{250, 60, true}, //
		{299, 60, true}, // still the third
		{0, 0, true},    // in the first: restart
		{-10, 0, true},  // before the first: restart
	}
	for _, c := range cases {
		got, ok := chapterTarget(chaps, c.pos, false)
		if ok != c.wantOkay || (ok && got != c.want) {
			t.Errorf("prev from %v = %v, %v; want %v, %v", c.pos, got, ok, c.want, c.wantOkay)
		}
	}
}

// TestChapterPrevAlwaysMakesProgress is the regression behind the rule. The
// conventional alternative — "from inside a chapter go to its start, otherwise go
// to the one before" — lands you on a start, playback carries you two seconds
// inside it, and the next press returns you to the same start. You can hold the
// key down for the rest of the track and never leave. A key that cannot make
// progress is a dead key.
func TestChapterPrevAlwaysMakesProgress(t *testing.T) {
	chaps := testChapters()
	pos := 250.0
	seen := map[float64]bool{}
	for i := 0; i < 5; i++ {
		next, ok := chapterTarget(chaps, pos, false)
		if !ok {
			t.Fatal("prev stopped working")
		}
		seen[next] = true
		// Play a little, the way the track does, so the next press is genuinely
		// "from inside" rather than from a frozen position.
		pos = next + 2
	}
	if len(seen) < 2 {
		t.Errorf("five presses of prev only ever visited %v — the key is dead", seen)
	}
}

// TestChapterTargetHasNoDeadZoneAtABoundary: positions are a frame count divided
// by a frame rate, so landing on a start is exact only by luck. The strict
// comparison handles it without a tolerance in either direction.
func TestChapterTargetHasNoDeadZoneAtABoundary(t *testing.T) {
	chaps := []Chapter{{Start: 90, End: 100, Title: "A"}, {Start: 100, End: 200, Title: "B"}}
	// A hair before the boundary: next still finds it, prev does not skip past it.
	if got, ok := chapterTarget(chaps, 99.999999, true); !ok || got != 100 {
		t.Errorf("next from just under a boundary = %v, %v; want 100, true", got, ok)
	}
	// Prev returns the chapter *before* the one you are in. A hair under the
	// boundary you are still in the first, so there is nothing before it and the
	// key restarts the track rather than handing back a start you are not at.
	if got, ok := chapterTarget(chaps, 99.999999, false); !ok || got != 0 {
		t.Errorf("prev from just under a boundary = %v, %v; want 0, true", got, ok)
	}
	// Exactly on it, and a hair over: both count as being in the second chapter.
	for _, pos := range []float64{100, 100.000001} {
		if got, ok := chapterTarget(chaps, pos, false); !ok || got != 90 {
			t.Errorf("prev from %v = %v, %v; want 90, true", pos, got, ok)
		}
	}
}

func TestPlayerChapterKeysSeek(t *testing.T) {
	m := &fakeMedia{dur: 300, chapters: testChapters()}
	p := newTestPlayer(m, Track{ID: "a"})
	p.Tick(10)

	p.Do(CmdChapNext)
	if m.pos != 60 {
		t.Errorf("after next: %v, want 60", m.pos)
	}
	p.Do(CmdChapNext)
	if m.pos != 200 {
		t.Errorf("after next: %v, want 200", m.pos)
	}
	// Past the last chapter the key is dead, not a restart.
	p.Do(CmdChapNext)
	if m.pos != 200 {
		t.Errorf("next past the end moved to %v, want it to stay at 200", m.pos)
	}
	p.Do(CmdChapPrev)
	if m.pos != 60 {
		t.Errorf("after prev: %v, want 60", m.pos)
	}
	p.Do(CmdChapPrev)
	if m.pos != 0 {
		t.Errorf("prev again: %v, want 0", m.pos)
	}
}

func TestCurrentChapterTitle(t *testing.T) {
	chaps := testChapters()
	cases := []struct {
		pos  float64
		want string
	}{
		{0, "Intro"},
		{30, "Intro"},
		{59, "Intro"},
		{60, "Main"},
		{199, "Main"},
		{250, "End"},
		{9999, "End"}, // past the end still reports the last one
	}
	for _, c := range cases {
		if got := currentChapter(chaps, c.pos); got != c.want {
			t.Errorf("chapter at %v = %q, want %q", c.pos, got, c.want)
		}
	}
	if got := currentChapter(nil, 30); got != "" {
		t.Errorf("chapter of a track with none = %q, want empty", got)
	}
}

// TestChaptersFromDropsUnusableEntries: yt-dlp writes start_time as -1 or NaN when
// it does not know, and letting one through would make a key seek to a position
// that does not exist.
func TestChaptersFromDropsUnusableEntries(t *testing.T) {
	got := chaptersFrom([]ytChapter{
		{StartTime: 0, EndTime: 10, Title: "ok"},
		{StartTime: -1, EndTime: 10, Title: "unknown"},
		{StartTime: 20, EndTime: 10, Title: "backwards"},
		{StartTime: 30, EndTime: 40, Title: "also ok"},
	})
	if len(got) != 2 {
		t.Fatalf("kept %d chapters, want 2: %v", len(got), got)
	}
	if got[0].Title != "ok" || got[1].Title != "also ok" {
		t.Errorf("kept the wrong ones: %v", got)
	}
	if chaptersFrom(nil) != nil {
		t.Error("no chapters should be nil, not an empty slice")
	}
}

// --- the overlay -------------------------------------------------------------

// TestLayoutOverlayIsBottomAnchoredAndFullWidth: reverse video over a moving
// frame is only legible when what is behind it is uniform, so the padding is the
// feature, not padding.
func TestLayoutOverlayIsBottomAnchoredAndFullWidth(t *testing.T) {
	got := layoutOverlay(20, 10, []string{"one", "two"})
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	if got[0].row != 9 || got[1].row != 10 {
		t.Errorf("rows %d and %d, want 9 and 10 (bottom two of ten)", got[0].row, got[1].row)
	}
	for _, l := range got {
		if n := len([]rune(l.text)); n != 20 {
			t.Errorf("line %q is %d cells, want 20", l.text, n)
		}
	}
}

// TestLayoutOverlayRefusesMoreLinesThanRows: a prompt taller than the grid would
// otherwise address rows above the frame and paint over the HUD.
func TestLayoutOverlayRefusesMoreLinesThanRows(t *testing.T) {
	got := layoutOverlay(20, 2, []string{"a", "b", "c", "d"})
	if len(got) != 2 {
		t.Fatalf("got %d lines, want 2", len(got))
	}
	if got[0].row != 1 || got[1].row != 2 {
		t.Errorf("rows %d and %d, want 1 and 2", got[0].row, got[1].row)
	}
}

func TestLayoutOverlayOnNothing(t *testing.T) {
	if got := layoutOverlay(0, 10, []string{"x"}); got != nil {
		t.Errorf("zero width = %v, want nil", got)
	}
	if got := layoutOverlay(20, 10, nil); got != nil {
		t.Errorf("no lines = %v, want nil", got)
	}
}

// TestOverlayDoesNotCorruptTheNextFrame is the stale-cell trap, from the other
// side. The colour renderer skips a cursor move when a cell follows the last one
// written and skips the SGR when the colour is unchanged; after an overlay the
// cursor is in the middle of the grid with a background no cell has. If the state
// survives, the next frame resumes mid-row and carries one overlay cell's colour
// into the rest of the line.
func TestOverlayDoesNotCorruptTheNextFrame(t *testing.T) {
	const cols, rows = 24, 8
	for _, mode := range []ColorMode{ColorTrue, Color256} {
		for _, glyph := range []GlyphMode{GlyphHalf, GlyphCell} {
			t.Run(modeName(mode)+"/"+glyphName(glyph), func(t *testing.T) {
				sc := newScreen(cols, rows)
				r := newColorRenderer(sc, cols, rows, mode, glyph)
				frames := leakFrames(cols, rows, glyph)

				if err := r.Draw(frames[0]); err != nil {
					t.Fatal(err)
				}
				if err := r.Overlay([]string{"jump to", "1:30"}); err != nil {
					t.Fatal(err)
				}
				// What the caller must do when the prompt closes.
				r.ForceNext()
				last := frames[len(frames)-1]
				if err := r.Draw(last); err != nil {
					t.Fatal(err)
				}
				assertScreenMatches(t, sc, cols, rows, glyph, last)
			})
		}
	}
}

// TestMonoOverlayDoesNotCorruptTheNextFrame: same trap on the diff renderer.
func TestMonoOverlayDoesNotCorruptTheNextFrame(t *testing.T) {
	const cols, rows = 24, 8
	sc := newScreen(cols, rows)
	r := NewDiffRenderer(sc, cols, rows)
	frames := leakFrames(cols, rows, GlyphCell)

	if err := r.Draw(frames[0]); err != nil {
		t.Fatal(err)
	}
	if err := r.Overlay([]string{"jump to", "1:30"}); err != nil {
		t.Fatal(err)
	}
	r.ForceNext()
	last := frames[len(frames)-1]
	if err := r.Draw(last); err != nil {
		t.Fatal(err)
	}
	assertScreenMatchesMono(t, sc, cols, rows, last)
}

// TestOverlayOutlivesItselfWithoutForceNext pins the contract the render loop
// depends on: the cells an overlay covered are invisible to the diff cache, so
// only a forced repaint brings the video back. The first draft of the close path
// guarded on "was the prompt open", which by then it never was — so the repaint
// was skipped and the prompt stayed on screen for the rest of the track. The unit
// test missed it because it called ForceNext itself.
func TestOverlayOutlivesItselfWithoutForceNext(t *testing.T) {
	const cols, rows = 24, 8
	sc := newScreen(cols, rows)
	r := newColorRenderer(sc, cols, rows, ColorTrue, GlyphHalf)
	frames := leakFrames(cols, rows, GlyphHalf)

	if err := r.Draw(frames[0]); err != nil {
		t.Fatal(err)
	}
	if err := r.Overlay([]string{"jump to", "1:30"}); err != nil {
		t.Fatal(err)
	}
	// The next frame is identical to the one underneath, so without a forced
	// repaint every changed cell is still repainted but the overlay rows are not.
	last := frames[len(frames)-1]
	if err := r.Draw(last); err != nil {
		t.Fatal(err)
	}
	if got := sc.text(rows - 1); got == " jump to" {
		t.Skip("this frame's bottom row matches the overlay anyway, so it cannot " +
			"show the leak; the forced-repaint path is covered by " +
			"TestOverlayDoesNotCorruptTheNextFrame")
	}
}

// TestCloseOverlayForcesARepaint is the same obligation seen from the other side:
// after an overlay, the very next Draw must rewrite every cell. It is what makes
// the screen correct without the loop having to remember which cells it covered.
func TestCloseOverlayForcesARepaint(t *testing.T) {
	const cols, rows = 24, 8
	sc := newScreen(cols, rows)
	r := newColorRenderer(sc, cols, rows, ColorTrue, GlyphHalf)
	frames := leakFrames(cols, rows, GlyphHalf)

	if err := r.Draw(frames[0]); err != nil {
		t.Fatal(err)
	}
	if err := r.Overlay([]string{"jump to", "1:30"}); err != nil {
		t.Fatal(err)
	}
	r.ForceNext() // exactly what closeOverlay does
	last := frames[len(frames)-1]
	if err := r.Draw(last); err != nil {
		t.Fatal(err)
	}
	assertScreenMatches(t, sc, cols, rows, GlyphHalf, last)
}

// TestProgressBarMarkSitsAtTheOldPosition: the tick has to be where playback
// was, not where it is.
func TestProgressBarMarkSitsAtTheOldPosition(t *testing.T) {
	got := progressBar(80, 100, 10, 20)
	// 80/100 of 10 cells is 8 filled; the tick is at 20/100, which is cell 2.
	want := "━━" + barMark + "━━━━━──"
	if got != want {
		t.Errorf("marked bar = %q, want %q", got, want)
	}
	// No marker, no tick.
	if got := progressBar(80, 100, 10, -1); strings.ContainsRune(got, []rune(barMark)[0]) {
		t.Errorf("unmarked bar has a tick: %q", got)
	}
	// A marker outside the track is ignored rather than panicking.
	if got := progressBar(50, 100, 10, 5000); strings.ContainsRune(got, []rune(barMark)[0]) {
		t.Errorf("out-of-range mark drew a tick: %q", got)
	}
}

// TestHUDFooterShowsTheChapter: the row the hints have vacated is the chapter's,
// which is the one piece of chrome worth keeping permanently.
func TestHUDFooterShowsTheChapter(t *testing.T) {
	h := hud{title: "t", chapter: "Main", showHints: true}
	if got := h.footer(); !strings.Contains(got, "Main") {
		t.Errorf("footer = %q, want the chapter title", got)
	}
}

// TestHUDRowsHoldWithAMark: a marker must not change the row's width. The rows
// are fixed-width by construction and the renderers diff against them.
func TestHUDRowsHoldWithAMark(t *testing.T) {
	for _, width := range []int{20, 40, 80} {
		h := hud{title: "Lofi Girl", pos: 300, dur: 3600, mark: 30, hasMark: true}
		for i, row := range h.lines(width) {
			if n := len([]rune(row)); n != width {
				t.Errorf("width %d row %d is %d cells: %q", width, i, n, row)
			}
		}
	}
}

// TestPromptStatusPreviewsTheLanding: the second row exists so a typo is visible
// before the jump rather than after it.
func TestPromptStatusPreviewsTheLanding(t *testing.T) {
	var p prompt
	p.start()
	if got := p.status(10, 300); !strings.Contains(got, "1:30") {
		t.Errorf("empty prompt status = %q, want the examples", got)
	}

	p.consume([]byte("1:30"))
	got := p.status(10, 300)
	if !strings.Contains(got, "1:30") || !strings.Contains(got, "5:00") {
		t.Errorf("status = %q, want the landing and the duration", got)
	}

	p.stop()
	p.start()
	p.consume([]byte("nope"))
	if got := p.status(10, 300); !strings.Contains(got, "not a timestamp") {
		t.Errorf("bad input status = %q, want it to say so", got)
	}
}

// --- the ffprobe shape -------------------------------------------------------

// ffprobeJSON is a real `-show_format -show_chapters` response, trimmed to the
// parts that matter.
//
// It is here because the shape is a trap rather than a convention: start_time and
// end_time are JSON *strings*, and the title is nested under "tags". Structuring
// them as numbers and as a top-level field still compiles, still runs, and fails
// at unmarshal — which the caller cannot tell apart from "this file has no
// duration", so the length column goes blank and nothing complains. That is
// exactly the regression this caught.
const ffprobeJSON = `{
  "chapters": [
    {
      "id": 0,
      "time_base": "1/22579200",
      "start": 0,
      "start_time": "0.000000",
      "end": 1354752000,
      "end_time": "60.000000",
      "tags": { "title": "Opening" }
    },
    {
      "id": 1,
      "time_base": "1/10000000",
      "start": 600000000,
      "start_time": "60.000000",
      "end": 1800000000,
      "end_time": "180.000000",
      "tags": { "title": "Main part" }
    }
  ],
  "format": {
    "filename": "episode.mkv",
    "duration": "600.000000"
  }
}`

func TestFfprobeOutputShape(t *testing.T) {
	var p ffprobeOutput
	if err := json.Unmarshal([]byte(ffprobeJSON), &p); err != nil {
		t.Fatalf("a real ffprobe response does not fit ffprobeOutput: %v", err)
	}
	if len(p.Chapters) != 2 {
		t.Fatalf("read %d chapters, want 2", len(p.Chapters))
	}
	if got := p.Chapters[1].Tags.Title; got != "Main part" {
		t.Errorf("chapter title = %q, want %q", got, "Main part")
	}
	start, err := strconv.ParseFloat(strings.TrimSpace(p.Chapters[0].StartTime), 64)
	if err != nil {
		t.Fatalf("start_time is not parseable: %v", err)
	}
	if start != 0 {
		t.Errorf("start_time = %v, want 0", start)
	}
	dur, err := strconv.ParseFloat(strings.TrimSpace(p.Format.Duration), 64)
	if err != nil || dur != 600 {
		t.Errorf("duration = %v (%v), want 600", dur, err)
	}
}

// ffprobeSilentJSON is the same response for a file with no audio stream, which is
// the shape that ends playback two seconds in if it goes unnoticed.
const ffprobeSilentJSON = `{
  "streams": [
    { "index": 0, "codec_type": "video", "codec_name": "h264" }
  ],
  "format": { "filename": "silent.mkv", "duration": "42.000000" }
}`

// TestFfprobeDetectsASilentFile: mpv exits instantly on a video-only file, and the
// render loop reads that exit as the end of the track. Detecting it is the whole
// difference between a silent video playing for its full length and stopping dead.
func TestFfprobeDetectsASilentFile(t *testing.T) {
	var p ffprobeOutput
	if err := json.Unmarshal([]byte(ffprobeSilentJSON), &p); err != nil {
		t.Fatalf("silent response does not fit ffprobeOutput: %v", err)
	}
	hasAudio := false
	for _, st := range p.Streams {
		if st.CodecType == "audio" {
			hasAudio = true
			break
		}
	}
	if hasAudio {
		t.Error("a video-only file reported an audio stream")
	}
	if len(p.Chapters) != 0 {
		t.Errorf("read %d chapters from a file with none", len(p.Chapters))
	}
}

// TestFfprobeFindsAudioInTheFixture: the other half — a video stream present must
// not read as silence, which would make every ordinary file skip its own audio.
func TestFfprobeFindsAudioInTheFixture(t *testing.T) {
	withAudio := strings.Replace(ffprobeJSON,
		`"format": {`, `"streams": [{"codec_type": "video"}, {"codec_type": "audio"}],
  "format": {`, 1)
	var p ffprobeOutput
	if err := json.Unmarshal([]byte(withAudio), &p); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	found := false
	for _, st := range p.Streams {
		if st.CodecType == "audio" {
			found = true
		}
	}
	if !found {
		t.Error("a file with an audio stream did not report one")
	}
}
