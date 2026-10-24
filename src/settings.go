package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/vspcoderz/rasterbar/internal/term"
)

// settings.go is the click-only settings store.
//
// The file is `~/.config/rasterbar/config.json`. The program's stated position
// on config files was "there isn't one — a file for a single string would be a
// worse trade" (see ytdlpauth.go), and that position holds for *one* string: a
// cookies value has the environment for it. Settings earn a file the day there
// is more than one string and a UI to choose them, which the download cache and
// the persisted visualizer choice are.
//
// Precedence stays the codebase's rule: an explicit flag beats the settings,
// which beat the built-in default. Settings are defaults the user typed once,
// never an override of something the user typed now.
//
// The UI is deliberately click-only: every value is a cycle, chosen with
// left/right or enter, no text inputs. That rules out settings whose values
// need typing — a custom cache path goes through `--cache-dir` or an edit of
// the file, and by design.

// settings is the persisted shape. Fields are omitempty so a hand-written
// partial file round-trips without growing empty keys.
type settings struct {
	// Viz and Palette name the starting style/scheme; empty means the
	// built-in default. Validated against the registries on apply and on save.
	Viz     string `json:"viz,omitempty"`
	Palette string `json:"palette,omitempty"`
	// Quality is 0 for "auto from terminal size", else one of the -q values.
	Quality int `json:"quality,omitempty"`
	// Mode is the default playback mode: "", "music" or "video".
	Mode string `json:"mode,omitempty"`
	// CacheDir is the download destination, stored unparsed (tilde intact).
	CacheDir string `json:"cacheDir,omitempty"`
	// CookiesFrom mirrors --cookies-from-browser, for a machine under a
	// permanent bot check that wants it set once.
	CookiesFrom string `json:"cookiesFromBrowser,omitempty"`
}

// settingsPath resolves the config file location, empty when there is no home.
func settingsPath() (string, error) {
	base, err := os.UserConfigDir()
	if err != nil || base == "" {
		// XDG UsersConfigDir is $XDG_CONFIG_HOME, else $HOME/.config. There is
		// no meaningful second place for a per-user file, so "no home" means
		// "no settings", not a desperate fallback to cwd.
		return "", fmt.Errorf("no user config directory: %w", err)
	}
	return filepath.Join(base, "rasterbar", "config.json"), nil
}

// loadSettings reads the file. A missing file is a normal state with zero
// values; a corrupt one is reported but never fatal — settings are defaults,
// and defaults that can brick the program would be the opposite of a default.
func loadSettings() (settings, error) {
	var s settings
	path, err := settingsPath()
	if err != nil {
		return s, err
	}
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return s, err
	}
	if err := json.Unmarshal(data, &s); err != nil {
		return s, fmt.Errorf("%s: %w", path, err)
	}
	return s, nil
}

// saveSettings writes the file atomically: temp in the same directory, then
// rename. A partial write reads as corrupt settings forever, and the same
// apply-then-swap discipline the build itself follows settles this too.
func saveSettings(s settings) error {
	path, err := settingsPath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", tmp, err)
	}
	return os.Rename(tmp, path)
}

// validate keeps a saved file honest: names resolve through the same
// registries `--viz`/`--palette` reject against, quality is a known value, and
// mode is one of two strings. A fresh binary that dropped a style must not
// brick its own settings file — the whole point of saving valid values is that
// loadSettings exists only to be trusted.
func (s settings) validate() (settings, error) {
	var errs []string
	if s.Viz != "" {
		if _, ok := VizByName(s.Viz); !ok {
			errs = append(errs, fmt.Sprintf("viz %q", s.Viz))
		}
	}
	if s.Palette != "" && paletteIndexByName(s.Palette) < 0 {
		errs = append(errs, fmt.Sprintf("palette %q", s.Palette))
	}
	switch s.Quality {
	case 0, 240, 360, 480, 720, 1080:
	default:
		errs = append(errs, fmt.Sprintf("quality %d", s.Quality))
	}
	switch s.Mode {
	case "", "music", "video":
	default:
		errs = append(errs, fmt.Sprintf("mode %q", s.Mode))
	}
	if len(errs) != 0 {
		return s, fmt.Errorf("%s", strings.Join(errs, "; "))
	}
	return s, nil
}

// applyTo folds the settings into parsed options. Explicit flag state is
// detected by non-zero values in the parsed options, which is why every
// flag-affected field here is compared against its zero value.
func (s settings) applyTo(o *options) {
	if o.viz == "" && s.Viz != "" {
		o.viz = s.Viz
	}
	if o.palette == "" && s.Palette != "" {
		o.palette = s.Palette
	}
	if o.quality == 0 && s.Quality != 0 {
		o.quality = Quality(s.Quality)
	}
	// Mode: only when neither -a nor -M was typed. "video" enables ascii;
	// "music" is leaving the built-in default explicit, which costs nothing
	// and makes the saved value visible in behavior rather than in a file.
	if !o.ascii && !o.music && s.Mode != "" {
		o.ascii = s.Mode == "video"
		o.music = s.Mode == "music"
	}
	if o.cookies == "" && o.cookiesFrom == "" && s.CookiesFrom != "" {
		o.cookiesFrom = s.CookiesFrom
	}
	if o.cacheDir == "" && s.CacheDir != "" {
		o.cacheDir = s.CacheDir
	}
}

// qualityNames maps the cycle vocabulary to the stored value.
var qualityNames = []struct {
	name  string
	value int
}{
	{"auto", 0}, {"240", 240}, {"360", 360},
	{"480", 480}, {"720", 720}, {"1080", 1080},
}

// browserChoices is the cookies-from-browser row. "none" is the sentinel for
// empty; the browser list is yt-dlp's, and the same one the flag's error
// message names.
var browserChoices = []string{"none", "firefox", "chrome", "chromium", "brave", "edge", "safari", "opera"}

// settingsRow is one cycled setting: a label, the current value's display
// text, and what cycling by delta does to it. Values are cycle vocabulary
// rather than stored values, so `auto` and the empty string meet in one place.
type settingsRow struct {
	name    string
	display func(s settings) string
	cycle   func(*settings, int)
}

// cycleValue steps through a closed vocabulary, wrapping at both ends. An
// unknown current value (a saved name a newer binary dropped, a typed custom
// path) steps towards the end the direction points at, so a dropped value is
// never unreachable — it lands on the nearest edge and rejoins the ring.
func cycleValue(values []string, cur string, delta int) string {
	if len(values) == 0 {
		return cur
	}
	at := -1
	for i, v := range values {
		if v == cur {
			at = i
			break
		}
	}
	if at == -1 {
		if delta < 0 {
			return values[len(values)-1]
		}
		if delta > 0 {
			return values[0]
		}
		return cur
	}
	return values[(at+delta+len(values))%len(values)]
}

// settingsRows is the click-only screen, in display order. Order is a choice
// the user sees, but it is also ranked: the two most-pressed rows are nearest
// the top.
func settingsRows() []settingsRow {
	return []settingsRow{
		{
			name:    "default mode",
			display: func(s settings) string { return modeDisplay(s.Mode) },
			cycle: func(s *settings, d int) {
				s.Mode = cycleValue([]string{"music", "video"}, modeDisplay(s.Mode), d)
			},
		},
		{
			name: "quality cap",
			display: func(s settings) string {
				for _, q := range qualityNames {
					if int(s.Quality) == q.value {
						return q.name
					}
				}
				return "auto"
			},
			cycle: func(s *settings, d int) {
				cur := qualityNameFor(int(s.Quality))
				next := cycleValue(qualityNameList(), cur, d)
				s.Quality = qualityValueFor(next)
			},
		},
		{
			name: "visualizer",
			display: func(s settings) string {
				if s.Viz == "" {
					return "default"
				}
				return s.Viz
			},
			cycle: func(s *settings, d int) {
				cur := s.Viz
				if cur == "" {
					// Outside the vocabulary so the first step lands on a real
					// name; cycleValue sends an unknown value to index 0/delta.
					cur = ":"
				}
				s.Viz = cycleValue(VizNames(), cur, d)
			},
		},
		{
			name: "palette",
			display: func(s settings) string {
				if s.Palette == "" {
					return "default"
				}
				return s.Palette
			},
			cycle: func(s *settings, d int) {
				cur := s.Palette
				if cur == "" {
					cur = ":"
				}
				s.Palette = cycleValue(paletteNames(), cur, d)
			},
		},
		{
			name: "download to",
			display: func(s settings) string {
				if s.CacheDir == "" {
					return "~/" + defaultCacheDirName
				}
				if !inSlice(cacheDirPresets, s.CacheDir) {
					return s.CacheDir + " (custom)"
				}
				return s.CacheDir
			},
			cycle: func(s *settings, d int) {
				cur := s.CacheDir
				if cur == "" {
					cur = cacheDirPresets[0] // and cycle from there
				}
				if !inSlice(cacheDirPresets, cur) {
					cur = ":" // away from the custom value, to a preset
				}
				s.CacheDir = cycleValue(cacheDirPresets, cur, d)
				// Stepping onto the default preset clears the field, so the
				// default stays legible in the file ("absent" beats "set to
				// itself").
				if s.CacheDir == cacheDirPresets[0] {
					s.CacheDir = ""
				}
			},
		},
		{
			name: "cookies-from-browser",
			display: func(s settings) string {
				if s.CookiesFrom == "" {
					return "none"
				}
				return s.CookiesFrom
			},
			cycle: func(s *settings, d int) {
				cur := s.CookiesFrom
				if cur == "" {
					cur = "none"
				}
				next := cycleValue(browserChoices, cur, d)
				if next == "none" {
					next = ""
				}
				s.CookiesFrom = next
			},
		},
	}
}

// settingsModel is the pure half of the settings screen: cursor, current
// settings, and what the keys do. All of it is testable without a tty —
// TestSettingsCycles and TestSettingsApplyToDate exist because the alternative
// (a raw-mode loop that races the terminal) is not.
type settingsModel struct {
	s    settings
	rows []settingsRow
	cur  int
}

func newSettingsModel(s settings) *settingsModel {
	return &settingsModel{s: s, rows: settingsRows()}
}

func (m *settingsModel) move(d int) {
	m.cur = (m.cur + d + len(m.rows)) % len(m.rows)
}

// cycle steps the selected row. Returns the row's new display, for the render.
func (m *settingsModel) cycle(d int) string {
	m.rows[m.cur].cycle(&m.s, d)
	return m.line(m.cur)
}

func (m *settingsModel) line(i int) string {
	draw := " "
	if i == m.cur {
		draw = ">"
	}
	return fmt.Sprintf("%s %-22s %s\n", draw, m.rows[i].name, m.rows[i].display(m.s))
}

func (m *settingsModel) render(w int) string {
	var b strings.Builder
	rule := strings.Repeat("─", max(w, 20)) + "\n"
	fmt.Fprintf(&b, "rasterbar settings\n%s", rule)
	for i := range m.rows {
		b.WriteString(m.line(i))
	}
	b.WriteString(rule)
	b.WriteString(" ↑/↓ or j/k move   ←/→ or enter change   q save+quit\n   use --cache-dir for a custom download path\n")
	return b.String()
}

func modeDisplay(mode string) string {
	if mode == "" {
		return "music"
	}
	return mode
}

// qualityNameList is the quality cycle vocabulary, derived from qualityNames
// so the two cannot disagree.
func qualityNameList() []string {
	names := make([]string, len(qualityNames))
	for i, q := range qualityNames {
		names[i] = q.name
	}
	return names
}

func qualityNameFor(value int) string {
	for _, q := range qualityNames {
		if q.value == value {
			return q.name
		}
	}
	return "auto"
}

func qualityValueFor(name string) int {
	for _, q := range qualityNames {
		if q.name == name {
			return q.value
		}
	}
	return 0
}

func inSlice(haystack []string, needle string) bool {
	for _, h := range haystack {
		if h == needle {
			return true
		}
	}
	return false
}

// runSettings opens the settings screen, edits in place, and saves on exit.
//
// The model does all deciding; this loop only converts bytes to actions, the
// same split keyRouter has. Raw mode has to be on for the escape reads to
// arrive as bytes; restore happens before anything else touches the terminal.
//
// The mixed result list is deliberate in shape but must be *fully* named:
// `(settings, saved bool, err error)` parses `settings` as a NAME of type
// bool (named lists group by type), which compiled-time confused everyone
// including the compiler.
func runSettings(s settings, in, outF *os.File) (out settings, saved bool, err error) {
	m := newSettingsModel(s)
	restore, rerr := term.MakeRaw(in)
	if rerr != nil {
		// Not a tty: report the current values rather than pretending they
		// changed. The same line-mode fallback the browse list has would accept
		// a silent no-op; a printout is the honest version.
		fmt.Fprint(outF, "(stdin is not a tty; current settings)\n"+m.render(80))
		return s, false, nil
	}
	defer restore()

	redraw := func() { fmt.Fprint(outF, "\x1b[2J\x1b[H"+m.render(80)) }
	redraw()

	r := bufio.NewReader(in)
	for {
		b, rerr := r.ReadByte()
		if rerr != nil {
			return m.s, false, nil
		}
		if b == 0x1b {
			buf := make([]byte, 0, 8)
			esc := false
			for i := 0; i < promptEscLimit; i++ {
				nb, e2 := r.ReadByte()
				if e2 != nil {
					return m.s, false, nil
				}
				buf = append(buf, nb)
				if i == 0 && nb != '[' && nb != 'O' {
					break // a lone Escape opens and dismisses nothing here
				}
				if i >= 1 && nb >= 0x40 && nb <= 0x7e {
					esc = true
					break
				}
			}
			if esc {
				// One render after the action: rendering before AND after the
				// cycle is two full clears per arrow key for no reason.
				switch buf[len(buf)-1] {
				case 'A':
					m.move(-1)
				case 'B':
					m.move(1)
				case 'D': // left
					m.cycle(-1)
				case 'C': // right
					m.cycle(1)
				}
				redraw()
			}
			continue
		}
		switch b {
		case 'q', 'Q', 0x03:
			outS, verr := m.s.validate()
			if verr != nil {
				// Drop the row the registry rejected rather than refusing to
				// exit: a stuck settings screen is worse than a lost row, and
				// validate names which row so the printout is actionable.
				fmt.Fprintf(os.Stderr, "settings: dropped unreadable %v\n", verr)
			}
			return outS, true, saveSettings(outS)
		case 'j':
			m.move(1)
			redraw()
		case 'k':
			m.move(-1)
			redraw()
		case '\r', '\n', 'a', 'A', 'd', 'D':
			// Any select-but-quit key means "change": the screen is click-only,
			// so enter is simply the left-arrow of people without arrow keys.
			m.cycle(1)
			redraw()
		}
	}
}
