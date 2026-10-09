package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseArgsQualityAndLayout(t *testing.T) {
	o, err := parseArgs([]string{"-q", "720", "--aspect", "2.5", "--cols", "120", "lofi"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if o.quality != Quality(720) {
		t.Errorf("quality = %v, want 720", o.quality)
	}
	if o.aspect != 2.5 {
		t.Errorf("aspect = %v, want 2.5", o.aspect)
	}
	if o.cols != 120 {
		t.Errorf("cols = %d, want 120", o.cols)
	}
	// Positional arguments are NOT joined here any more. Whether they are a search
	// query or a list of files needs the filesystem, so parseArgs collects them and
	// main decides. TestParseArgsFileArgsAndQuery pins the joining.
	if len(o.args) != 1 || o.args[0] != "lofi" {
		t.Errorf("args = %v, want [lofi]", o.args)
	}

	// Reject nonsense values instead of silently ignoring them.
	for _, bad := range [][]string{
		{"-q", "999"}, {"-q", "abc"}, {"-q"}, {"--aspect", "0"},
		{"--aspect", "-1"}, {"--cols", "0"}, {"--cols", "5"},
	} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("parseArgs(%v) should have errored", bad)
		}
	}
}

func TestParseArgsColorFlags(t *testing.T) {
	o, err := parseArgs([]string{"-c", "lofi"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if o.color != ColorTrue {
		t.Errorf("-c gave colour %v, want ColorTrue", o.color)
	}

	m, err := parseArgs([]string{"--mono", "lofi"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if m.color != ColorNone {
		t.Errorf("--mono gave colour %v, want ColorNone", m.color)
	}

	// No flag must mean "detect", not "off".
	a, err := parseArgs([]string{"lofi"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if a.color != colorAuto {
		t.Errorf("default colour = %v, want colorAuto (detect)", a.color)
	}
}

func TestParseArgsGlyphFlag(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want GlyphMode
	}{
		{"auto", GlyphAuto},
		{"half", GlyphHalf},
		{"cell", GlyphCell},
	} {
		o, err := parseArgs([]string{"--glyph", tc.arg, "q"})
		if err != nil {
			t.Fatalf("--glyph %s: %v", tc.arg, err)
		}
		if o.glyph != tc.want {
			t.Errorf("--glyph %s = %v, want %v", tc.arg, o.glyph, tc.want)
		}
	}
	if _, err := parseArgs([]string{"--glyph", "bogus"}); err == nil {
		t.Error("want an error for an unknown --glyph value")
	}
	if _, err := parseArgs([]string{"--glyph"}); err == nil {
		t.Error("--glyph with no value should error")
	}
	// Default must stay "detect".
	o, _ := parseArgs([]string{"q"})
	if o.glyph != GlyphAuto {
		t.Errorf("default glyph = %v, want GlyphAuto", o.glyph)
	}
}

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"-m", "lofi", "hip", "hop"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !o.mute {
		t.Error("-m not set")
	}
	if got := strings.Join(o.args, " "); got != "lofi hip hop" {
		t.Errorf("args joined = %q", got)
	}
	if _, err := parseArgs([]string{"-x", "q"}); err == nil {
		t.Error("want error on unknown flag")
	}
	if _, err := parseArgs([]string{"-h"}); err == nil {
		t.Error("-h should signal help")
	}
	o2, _ := parseArgs([]string{"-a", "song"})
	if !o2.ascii || len(o2.args) != 1 || o2.args[0] != "song" {
		t.Errorf("got %+v", o2)
	}
}

// TestParseArgsFileArgsAndQuery pins the decision main makes with the collected
// arguments: a name that exists on disk becomes a file, and anything else stays
// text to search YouTube for.
//
// This is the behaviour that regressed before. The arguments used to be joined
// inside parseArgs, so `rasterbar ~/Music/song.mp3` searched YouTube for that
// string and returned ten unrelated videos.
func TestParseArgsFileArgsAndQuery(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "nocturne.opus")
	if err := os.WriteFile(song, []byte("not really audio"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A real file: files, not a query.
	o, err := parseArgs([]string{song})
	if err != nil {
		t.Fatal(err)
	}
	paths, ferr := expandFileArgs(o.args)
	if ferr != nil {
		t.Fatalf("expandFileArgs: %v", ferr)
	}
	if len(paths) != 1 || paths[0] != song {
		t.Errorf("paths = %v, want [%s]", paths, song)
	}

	// Plain words: no file, so the query path stays intact.
	o2, err := parseArgs([]string{"lofi", "hip", "hop"})
	if err != nil {
		t.Fatal(err)
	}
	p2, err2 := expandFileArgs(o2.args)
	if err2 != nil {
		t.Fatalf("expandFileArgs: %v", err2)
	}
	if len(p2) != 0 {
		t.Errorf("a search query resolved to files: %v", p2)
	}
	if q := strings.Join(o2.args, " "); q != "lofi hip hop" {
		t.Errorf("query = %q", q)
	}
}

// TestExpandFileArgsRejectsMixedInput pins the mixed case: once any argument
// names a file, every argument is a path, and the one that is not gets named in
// the error rather than silently becoming a search.
func TestExpandFileArgsRejectsMixedInput(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(song, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := expandFileArgs([]string{song, "lofi"}); err == nil {
		t.Fatal("want an error for a mixed file/search argument list")
	}
}
