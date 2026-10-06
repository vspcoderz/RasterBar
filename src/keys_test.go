package main

import (
	"testing"
)

// --- Key decoding -----------------------------------------------------------
//
// Pure function so escape sequences can be tested without a terminal. Arrows
// arrive as ESC [ <final>, which is the only multi-byte sequence handled.

func TestDecodeKeysMapsTransportKeys(t *testing.T) {
	cases := []struct {
		in   string
		want []Cmd
	}{
		{" ", []Cmd{CmdTogglePause}},
		{"q", []Cmd{CmdQuit}},
		{"Q", []Cmd{CmdQuit}},
		{"\x03", []Cmd{CmdQuit}}, // ctrl-c, still delivered as a byte in raw mode
		{"n", []Cmd{CmdNext}},
		{"p", []Cmd{CmdPrev}},
		{"+", []Cmd{CmdVolUp}},
		{"=", []Cmd{CmdVolUp}}, // the unshifted key on most layouts
		{"-", []Cmd{CmdVolDown}},
		{"_", []Cmd{CmdVolDown}},
	}
	for _, c := range cases {
		got := decodeKeys([]byte(c.in))
		if len(got) != len(c.want) || (len(got) == 1 && got[0] != c.want[0]) {
			t.Errorf("decodeKeys(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// TestDecodeKeysMapsPaletteDigits pins the digit table for the direct palette
// keys.
//
// It has to catch two things. A digit wired to the wrong palette is invisible
// until someone presses it, and a palette with no digit is unreachable without
// walking `c`. The completeness check is the one that matters: adding an
// eleventh palette with no key would otherwise fail silently.

// TestDecodeKeysMapsPaletteDigits pins the digit table for the direct palette
// keys.
//
// It has to catch two things. A digit wired to the wrong palette is invisible
// until someone presses it, and a palette with no digit is unreachable without
// walking `c`. The completeness check is the one that matters: adding an
// eleventh palette with no key would otherwise fail silently.
func TestDecodeKeysMapsPaletteDigits(t *testing.T) {
	if paletteCount != len(palettes) {
		t.Fatalf("paletteCount = %d, len(palettes) = %d; the digit table would desync",
			paletteCount, len(palettes))
	}
	digits := []byte{'1', '2', '3', '4', '5', '6', '7', '8', '9', '0'}
	seen := make(map[int]bool, len(digits))
	for _, c := range digits {
		n := paletteDigit(c)
		if n < 0 {
			t.Fatalf("paletteDigit(%q) = -1, want a palette index", c)
		}
		want := CmdPaletteSelect + Cmd(n)
		got := decodeKeys([]byte{c})
		if len(got) != 1 || got[0] != want {
			t.Errorf("decodeKeys(%q) = %v, want [%d] (palette %d, %q)",
				c, got, want, n, palettes[n].name)
		}
		seen[n] = true
	}
	for i, p := range palettes {
		if !seen[i] {
			t.Errorf("palette %q (index %d) has no digit key", p.name, i)
		}
	}
}

// TestDigitsDoNotLeakFromUnknownSequences is the regression test for a bug this
// feature introduced rather than revealed.
//
// The decoder used to consume exactly three bytes of any CSI sequence, so a
// bracketed paste marker -- ESC [ 200 ~, six bytes -- left "00~" to be decoded as
// ordinary keystrokes. Every unmapped byte meant CmdNone so nothing showed. Once a
// digit became a palette select, pasting anything at all would have changed the
// palette to mono.

// TestDigitsDoNotLeakFromUnknownSequences is the regression test for a bug this
// feature introduced rather than revealed.
//
// The decoder used to consume exactly three bytes of any CSI sequence, so a
// bracketed paste marker -- ESC [ 200 ~, six bytes -- left "00~" to be decoded as
// ordinary keystrokes. Every unmapped byte meant CmdNone so nothing showed. Once a
// digit became a palette select, pasting anything at all would have changed the
// palette to mono.
func TestDigitsDoNotLeakFromUnknownSequences(t *testing.T) {
	for _, seq := range []string{
		"\x1b[200~",     // bracketed paste start
		"\x1b[201~",     // bracketed paste end
		"\x1b[<0;10;5M", // mouse press, digits in its parameters
		"\x1b[1;5A",     // modified cursor up
		"\x1b[3~",       // delete key
	} {
		for _, c := range decodeKeys([]byte(seq)) {
			if c >= CmdPaletteSelect {
				t.Errorf("decodeKeys(%q) produced palette select %d; digits inside "+
					"an escape sequence must never reach the key table", seq, c-CmdPaletteSelect)
			}
		}
	}
}

// TestCsiSequenceIsConsumedWhole checks the decoder leaves nothing behind.
//
// Not about the commands it returns -- CmdNone for all of these is correct -- but
// about `used`, because a short read is how leaked bytes reach the key table.

// TestCsiSequenceIsConsumedWhole checks the decoder leaves nothing behind.
//
// Not about the commands it returns -- CmdNone for all of these is correct -- but
// about `used`, because a short read is how leaked bytes reach the key table.
func TestCsiSequenceIsConsumedWhole(t *testing.T) {
	for _, seq := range []string{"\x1b[C", "\x1b[D", "\x1b[200~", "\x1b[<0;10;5M", "\x1b[1;5A"} {
		_, used, ok := decodeOne([]byte(seq))
		if !ok {
			t.Errorf("decodeOne(%q) asked for more bytes; it should be complete", seq)
			continue
		}
		if used != len(seq) {
			t.Errorf("decodeOne(%q) consumed %d of %d bytes, leaking %q into the key table",
				seq, used, len(seq), seq[used:])
		}
	}
}

func TestDecodeKeysArrows(t *testing.T) {
	cases := []struct {
		in   string
		want Cmd
	}{
		{"\x1b[C", CmdSeekFwd},  // right
		{"\x1b[D", CmdSeekBack}, // left
		{"\x1b[D", CmdSeekBack},
		{"\x1b[A", CmdNone}, // up: not a transport key
		{"\x1b[B", CmdNone}, // down
	}
	for _, c := range cases {
		got := decodeKeys([]byte(c.in))
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("decodeKeys(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDecodeKeysHandlesBursts(t *testing.T) {
	// Held arrow key: the terminal sends the sequence repeatedly, and several
	// can land in one read. All of them must be queued, not just the first.
	got := decodeKeys([]byte("\x1b[C\x1b[C\x1b[C"))
	if len(got) != 3 {
		t.Fatalf("burst produced %v, want 3 seeks", got)
	}
	for i, c := range got {
		if c != CmdSeekFwd {
			t.Errorf("cmd %d = %v, want CmdSeekFwd", i, c)
		}
	}
}

func TestDecodeKeysIgnoresUnknownBytes(t *testing.T) {
	// A mouse report, a bracketed-paste marker, a function key: all arrive as
	// bytes we do not handle. They must be swallowed, not turned into seeks.
	for _, in := range []string{"\x1b[200~", "\x1bOP", "z", "\x00", "\x1b"} {
		for _, c := range decodeKeys([]byte(in)) {
			if c != CmdNone {
				t.Errorf("decodeKeys(%q) produced %v, want CmdNone only", in, c)
			}
		}
	}
}

func TestDecodeStreamKeepsIncompleteSequences(t *testing.T) {
	// The bug this pins: a terminal can split ESC [ C across reads. Decoding each
	// read independently threw the arrow away, so seek did nothing and it looked
	// like keys were not being delivered at all.
	cmds, used := decodeStream([]byte("\x1b"))
	if len(cmds) != 0 || used != 0 {
		t.Errorf("lone ESC consumed %d bytes -> %v, want it held", used, cmds)
	}
	cmds, used = decodeStream([]byte("\x1b["))
	if len(cmds) != 0 || used != 0 {
		t.Errorf("ESC [ consumed %d bytes -> %v, want it held", used, cmds)
	}
	// The rest arrives: now the sequence completes.
	cmds, used = decodeStream([]byte("\x1b[C"))
	if len(cmds) != 1 || cmds[0] != CmdSeekFwd {
		t.Errorf("completed split = %v, want CmdSeekFwd", cmds)
	}
	if used != 3 {
		t.Errorf("consumed %d bytes, want 3", used)
	}
}

// TestDecodeStreamHandlesApplicationModeArrows is the regression test for SS3.
//
// xterm's application cursor mode sends ESC O A rather than ESC [ A. Nothing in
// this program changes the keypad mode, so whichever mode the terminal is in is
// whichever it keeps using — and a terminal *started* in application mode sends
// SS3 for arrows for the whole session. The decoder dropped the ESC and then
// read O, A as ordinary letters, so the arrows silently did nothing.
func TestDecodeStreamHandlesApplicationModeArrows(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want Cmd
	}{
		{"\x1bOC", CmdSeekFwd},
		{"\x1bOD", CmdSeekBack},
		{"\x1b[C", CmdSeekFwd},
		{"\x1b[D", CmdSeekBack},
	} {
		cmds, used := decodeStream([]byte(tc.in))
		if len(cmds) != 1 || cmds[0] != tc.want {
			t.Errorf("%q decoded to %v, want [%v]", tc.in, cmds, tc.want)
		}
		if used != 3 {
			t.Errorf("%q consumed %d bytes, want 3", tc.in, used)
		}
	}
}

// A partial SS3 must be held, exactly like a partial CSI: dropping the ESC
// would turn the introducer into an ordinary letter press.
func TestDecodeStreamHoldsPartialSS3(t *testing.T) {
	for _, in := range []string{"\x1b", "\x1bO"} {
		cmds, used := decodeStream([]byte(in))
		if len(cmds) != 0 || used != 0 {
			t.Errorf("%q consumed %d bytes -> %v, want it held", in, used, cmds)
		}
	}
}

// Up and down are unbound, and an unmapped SS3 final must still be swallowed
// whole rather than decoded as a literal letter.
func TestDecodeStreamSwallowsUnmappedSS3(t *testing.T) {
	cmds, used := decodeStream([]byte("\x1bOA"))
	if used != 3 {
		t.Errorf("consumed %d bytes, want 3", used)
	}
	if len(cmds) != 1 || cmds[0] != CmdNone {
		t.Errorf("ESC O A decoded to %v, want a single CmdNone", cmds)
	}
}

func TestDecodeStreamHandlesPartialThenMore(t *testing.T) {
	// A real burst: keys before the sequence, the sequence itself, keys after.
	pending := []byte("n\x1b[D ")
	cmds, used := decodeStream(pending)
	want := []Cmd{CmdNext, CmdSeekBack, CmdTogglePause}
	if len(cmds) != len(want) {
		t.Fatalf("got %v, want %v", cmds, want)
	}
	for i := range want {
		if cmds[i] != want[i] {
			t.Errorf("cmd %d = %v, want %v", i, cmds[i], want[i])
		}
	}
	if used != len(pending) {
		t.Errorf("consumed %d of %d bytes", used, len(pending))
	}
}

func TestDecodeStreamDropsEscapeNotFollowedByBracket(t *testing.T) {
	// ESC then a normal key: the ESC is not a sequence, so it is discarded and
	// the key after it still works. Holding it would swallow the key.
	cmds, used := decodeStream([]byte("\x1bq"))
	if len(cmds) != 1 || cmds[0] != CmdQuit {
		t.Errorf("got %v, want CmdQuit", cmds)
	}
	if used != 2 {
		t.Errorf("consumed %d bytes, want 2", used)
	}
}
