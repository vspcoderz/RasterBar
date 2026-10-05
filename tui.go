package main

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// TUI is a plain-stdlib list view. No bubbletea: this is a scrollable list and
// a key handler, which is a few dozen lines, and a state-machine library would
// be the heaviest dependency in a program whose whole point is lightness.
type TUI struct {
	out    *os.File
	in     *os.File
	tracks []Track
	cursor int
	top    int
	query  string
	rows   int
}

func NewTUI(in, out *os.File, query string, tracks []Track) *TUI {
	rows := 12
	if _, r, err := termSize(out); err == nil && r > 6 {
		// A third of the window, so a tall terminal shows more results.
		rows = r / 3
		if rows < 4 {
			rows = 4
		}
		if len(tracks) > 0 && rows > len(tracks) {
			rows = len(tracks)
		}
	}
	return &TUI{in: in, out: out, query: query, tracks: tracks, rows: rows}
}

// Action is what the user chose in the list.
type Action int

const (
	ActionQuit Action = iota
	ActionPlay
	ActionASCII
)

func (t *TUI) clear() {
	fmt.Fprint(t.out, "\x1b[2J\x1b[H")
}

// width returns the terminal width, falling back to 80 when stdout is not a TTY.
func (t *TUI) width() int {
	if c, _, err := termSize(t.out); err == nil && c > 20 {
		if c > maxCols {
			return maxCols
		}
		return c
	}
	return defaultCols
}

func (t *TUI) render() {
	t.clear()
	w := t.width()
	var sb strings.Builder
	fmt.Fprintf(&sb, "vspz-yt-cli · %d results · %s\n", len(t.tracks), t.query)
	sb.WriteString(strings.Repeat("─", w) + "\n")

	// Budget the row: marker + title + [duration] + channel, with the title
	// taking whatever is left so it uses the full width instead of a fixed 42.
	const durW = 9 // "[1:01:14] "
	const chanW = 17
	titleW := w - 2 - durW - chanW
	if titleW < 10 {
		titleW = 10
	}

	for i := t.top; i < len(t.tracks) && i < t.top+t.rows; i++ {
		marker := "  "
		if i == t.cursor {
			marker = "> "
		}
		tr := t.tracks[i]
		fmt.Fprintf(&sb, "%s%s [%8s] %s\n", marker,
			truncate(tr.Title, titleW), tr.DurationText(),
			truncate(tr.ChannelText(), chanW))
	}
	sb.WriteString(strings.Repeat("─", w) + "\n")
	fmt.Fprint(t.out, sb.String())
	fmt.Fprint(t.out, "j/k move  enter music  a video  q quit\n")
}

// Run reads single keypresses until the user acts. Reads raw bytes, not lines:
// in raw mode the terminal sends \r for Enter and never \n, so a line-based
// reader (bufio.Scanner) never fires. That bug shipped in the first draft.
func (t *TUI) Run() (Action, int) {
	restore, err := makeRaw(t.in)
	if err != nil {
		// Not a TTY (piped input). Fall back to line mode so the binary is
		// still scriptable: read one decision from stdin.
		fmt.Fprintln(t.out, "(stdin is not a TTY; enter a line index or 'q')")
		t.render()
		br := bufio.NewReader(t.in)
		line, _ := br.ReadString('\n')
		return parseDecision(strings.TrimSpace(line), len(t.tracks))
	}
	defer restore()

	t.render()
	r := bufio.NewReader(t.in)
	buf := make([]byte, 0, 8)
	for {
		b, err := r.ReadByte()
		if err != nil {
			return ActionQuit, -1
		}
		if b == 0x1b { // escape sequence: arrow keys
			buf = buf[:0]
			for i := 0; i < 2; i++ {
				nb, err := r.ReadByte()
				if err != nil {
					return ActionQuit, -1
				}
				buf = append(buf, nb)
			}
			switch buf[1] {
			case 'A':
				t.move(-1)
			case 'B':
				t.move(1)
			}
			t.render()
			continue
		}
		switch b {
		case 'q', 'Q', 0x03: // ctrl-c
			return ActionQuit, -1
		case 'j':
			t.move(1)
			t.render()
		case 'k':
			t.move(-1)
			t.render()
		case 'g', 0x48: // home
			t.cursor = 0
			t.clampTop()
			t.render()
		case 'G', 0x46: // end
			t.cursor = len(t.tracks) - 1
			t.clampTop()
			t.render()
		case '\r', '\n': // enter — both forms, raw or cooked
			if len(t.tracks) == 0 {
				return ActionQuit, -1
			}
			return ActionPlay, t.cursor
		case 'a', 'A':
			if len(t.tracks) == 0 {
				return ActionQuit, -1
			}
			return ActionASCII, t.cursor
		case '1', '2', '3', '4', '5', '6', '7', '8', '9':
			// jump to a numbered row
			n := int(b - '0')
			if n-1 < len(t.tracks) {
				t.cursor = n - 1
				t.clampTop()
				t.render()
			}
		}
	}
}

func (t *TUI) move(d int) {
	t.cursor += d
	if t.cursor < 0 {
		t.cursor = 0
	}
	if t.cursor > len(t.tracks)-1 {
		t.cursor = len(t.tracks) - 1
	}
	t.clampTop()
}

func (t *TUI) clampTop() {
	if t.cursor < t.top {
		t.top = t.cursor
	}
	if t.cursor >= t.top+t.rows {
		t.top = t.cursor - t.rows + 1
	}
	if t.top < 0 {
		t.top = 0
	}
}

func parseDecision(line string, n int) (Action, int) {
	if line == "" || line == "q" || line == "Q" {
		return ActionQuit, -1
	}
	idx := -1
	fmt.Sscanf(line, "%d", &idx)
	if idx >= 0 && idx < n {
		return ActionPlay, idx
	}
	return ActionQuit, -1
}

func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
