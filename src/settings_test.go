package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// settings_test.go: every decision behind the settings screen is a pure value
// transformation, so the screen itself never needs a tty to be believed. The
// raw-mode halves that touch the terminal are the manual tier.

func TestCycleValueWrapsBothWays(t *testing.T) {
	values := []string{"a", "b", "c"}
	if got := cycleValue(values, "a", -1); got != "c" {
		t.Errorf("wrap down = %q, want c", got)
	}
	if got := cycleValue(values, "c", 1); got != "a" {
		t.Errorf("wrap up = %q, want a", got)
	}
	if got := cycleValue(values, "b", 1); got != "c" {
		t.Errorf("step up = %q, want c", got)
	}
	// An unknown current value (a name a newer binary dropped) steps to the
	// nearest end in the direction pressed, never vanishing.
	if got := cycleValue(values, "?", 1); got != "a" {
		t.Errorf("unknown+1 = %q, want a", got)
	}
	if got := cycleValue(values, "?", -1); got != "c" {
		t.Errorf("unknown-1 = %q, want c", got)
	}
	if got := cycleValue(values, "b", 0); got != "b" {
		t.Error("zero step moved")
	}
}

func TestSettingsCyclesLandWhereTheySay(t *testing.T) {
	// Row 0 is default mode: music -> video, and one step is one change.
	m := newSettingsModel(settings{})
	if got := m.cycle(1); !strings.Contains(got, "video") {
		t.Errorf("mode cycle = %q, want video", got)
	}

	// Quality: auto -> 240, and 1080 wraps back to auto. The wrap is the
	// behavior that keeps the screen click-only: there is no wrong direction.
	m.cur = 1
	if got := m.cycle(1); !strings.Contains(got, "240") {
		t.Errorf("quality cycle = %q, want 240", got)
	}
	m.s.Quality = 1080
	if got := m.cycle(1); !strings.Contains(got, "auto") {
		t.Errorf("quality wrap = %q, want auto", got)
	}

	// A dropped style name must not strand the row: an unknown viz steps to a
	// real one rather than being unreachable.
	m2 := newSettingsModel(settings{Viz: "style-that-was-removed"})
	m2.cur = 2
	if got := m2.cycle(1); strings.Contains(got, "style-that-was-removed") {
		t.Errorf("unknown viz stuck where it was: %q", got)
	}

	// The default cache dir is stored absent, not set-to-itself, so the file
	// keeps saying "default" instead of freezing the preset in place.
	// The default cache dir is stored absent, not set-to-itself, so the file
	// keeps saying "default" instead of freezing the preset in place. Cycling
	// the full ring (from the default, around, back) must land on absent.
	m3 := newSettingsModel(settings{})
	m3.cur = 4
	for i := 0; i < len(cacheDirPresets); i++ {
		m3.cycle(1)
	}
	if m3.s.CacheDir != "" {
		t.Errorf("full cache-dir ring did not return the default: %q", m3.s.CacheDir)
	}

	// "none" is the sentinel for cookies, and never stored as a value. From
	// brave the ring reaches none after 4 steps (brave-item index arithmetic),
	// and proving the mapping once is the point, not the arithmetic.
	m4 := newSettingsModel(settings{CookiesFrom: "brave"})
	m4.cur = 5
	for i := 0; i < 4; i++ {
		m4.cycle(1)
	}
	if m4.s.CookiesFrom != "" {
		t.Errorf("none was stored raw: %q", m4.s.CookiesFrom)
	}
	if got := m4.cycle(1); !strings.Contains(got, "firefox") {
		t.Errorf("cycling away from none = %q", got)
	}
}

func TestSettingsValidateDropsTheUnreadableRow(t *testing.T) {
	s, err := (settings{Viz: "not-a-style", Palette: "ocean", Quality: 360, Mode: "music"}).validate()
	if err == nil {
		t.Fatal("an invalid viz validated")
	}
	if s.Palette != "ocean" || s.Quality != 360 {
		t.Errorf("valid fields were lost: %+v", s)
	}
	if _, err := (settings{}).validate(); err != nil {
		t.Errorf("defaults do not validate: %v", err)
	}
}

func TestSettingsApplyNeverOverridesExplicitFlags(t *testing.T) {
	// The flag state the user typed is preserved; settings only fill gaps.
	o := options{viz: "radial", palette: "ember", quality: Quality(480), ascii: true, cookies: "c.txt", cacheDir: "/typed"}
	(settings{Viz: "bloom", Palette: "ice", Quality: 720, Mode: "music", CookiesFrom: "firefox", CacheDir: "~/RasterStuff"}).applyTo(&o)
	if o.viz != "radial" || o.palette != "ember" || o.quality != Quality(480) {
		t.Errorf("settings overrode explicit flags: %+v", o)
	}
	if o.ascii != true {
		t.Errorf("settings overrode -a: %+v", o)
	}
	if o.cookies != "c.txt" || o.cacheDir != "/typed" {
		t.Errorf("settings overrode explicit cookies/cache: %+v", o)
	}

	// Nothing typed: settings fill, and a "video" default is ASCII.
	o2 := options{}
	(settings{Viz: "bloom", Palette: "ice", Quality: 720, Mode: "video", CookiesFrom: "firefox", CacheDir: "~/RasterStuff"}).applyTo(&o2)
	if o2.viz != "bloom" || o2.palette != "ice" || o2.quality != Quality(720) {
		t.Errorf("settings did not fill the gaps: %+v", o2)
	}
	if !o2.ascii || o2.music {
		t.Errorf("a video default did not enable ascii: %+v", o2)
	}
	if o2.cookiesFrom != "firefox" {
		t.Errorf("cookies setting lost: %+v", o2)
	}
	if o2.cacheDir != "~/RasterStuff" {
		t.Errorf("cache dir setting lost: %+v", o2)
	}
}

func TestSettingsRoundTripThroughTheFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	s := settings{Viz: "bloom", Palette: "ember", Quality: 480, Mode: "video", CacheDir: "~/RasterStuff"}
	if err := saveSettings(s); err != nil {
		t.Fatal(err)
	}
	// The file is human-visible JSON, not a binary cache; it is where a user
	// types the custom cache path the click-only screen cannot carry.
	data, err := os.ReadFile(filepath.Join(dir, "rasterbar", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "bloom") {
		t.Errorf("settings file unreadable: %s", data)
	}
	back, err := loadSettings()
	if err != nil {
		t.Fatal(err)
	}
	if back != s {
		t.Errorf("round trip drift: %+v -> %+v", s, back)
	}
}

func TestLoadMissingSettingsAreNotAnError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	s, err := loadSettings()
	if err != nil {
		t.Fatalf("no config file: %v", err)
	}
	if s != (settings{}) {
		t.Errorf("missing file yielded values: %+v", s)
	}
}

func TestSaveSettingsIsAtomic(t *testing.T) {
	// The write goes through a temp file and a rename; a crash between writes
	// must leave the previous file, never a truncated one. Held by checking
	// nothing is left over: the temp name's whole point is to disappear.
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	if err := saveSettings(settings{Viz: "radial"}); err != nil {
		t.Fatal(err)
	}
	path, _ := settingsPath()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("a temp file survived the save: %s", e.Name())
		}
	}
}

func TestVersionlessVerbsStillParse(t *testing.T) {
	// settings and update must accept a bare invocation — a verb whose word is
	// not a plausible search must never fall through to a YouTube search for
	// "settings" or "update".
	for _, argv := range [][]string{{"settings"}, {"update"}, {"update", "--check"}, {"update", "check"}} {
		o, err := parseArgs(argv)
		if err != nil {
			t.Fatalf("parseArgs(%v): %v", argv, err)
		}
		if !o.settingsMode && !o.updateMode {
			t.Errorf("parseArgs(%v) did not route a verb", argv)
		}
	}
	if _, err := parseArgs([]string{"settings", "nope"}); err == nil {
		t.Error("settings with arguments should refuse")
	}
	if _, err := parseArgs([]string{"update", "nightly"}); err == nil {
		t.Error("update with a nonsense argument should refuse")
	}
	if o, err := parseArgs([]string{"download", "https://youtu.be/x"}); err != nil || !o.downloadMode {
		t.Error("download with a link did not take the verb")
	}
	if _, err := parseArgs([]string{"download"}); err == nil {
		t.Error("bare download should refuse, not search for the word")
	}
	// Worst case: a search genuinely about settings, played via the verb.
	o, err := parseArgs([]string{"play", "design", "settings", "tutorial"})
	if err != nil || strings.Join(o.args, " ") != "design settings tutorial" {
		t.Errorf("usage covered: %+v %v", o, err)
	}
}

func TestCacheDirFlagParses(t *testing.T) {
	o, err := parseArgs([]string{"download", "--cache-dir", "/tmp/c", "https://youtu.be/x"})
	if err != nil {
		t.Fatal(err)
	}
	if o.cacheDir != "/tmp/c" {
		t.Errorf("--cache-dir lost: %q", o.cacheDir)
	}
	if _, err := parseArgs([]string{"--cache-dir"}); err == nil {
		t.Error("--cache-dir with no value should error")
	}
}
