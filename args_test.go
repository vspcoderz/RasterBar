package main

import (
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
	if o.query != "lofi" {
		t.Errorf("query = %q", o.query)
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
	if o.query != "lofi hip hop" {
		t.Errorf("query = %q", o.query)
	}
	if _, err := parseArgs([]string{"-x", "q"}); err == nil {
		t.Error("want error on unknown flag")
	}
	if _, err := parseArgs([]string{"-h"}); err == nil {
		t.Error("-h should signal help")
	}
	o2, _ := parseArgs([]string{"-a", "song"})
	if !o2.ascii || o2.query != "song" {
		t.Errorf("got %+v", o2)
	}
}
