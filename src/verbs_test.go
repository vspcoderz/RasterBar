package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The verbs are a front door onto flags that already worked. Two properties
// matter and neither is obvious:
//
//  1. A verb changes what its arguments MEAN, and nothing else. `rasterbar play
//     song.mp3` plays the file; `rasterbar search song.mp3` searches for the
//     words. Same argument, opposite outcome.
//  2. Nothing that worked stops working. Bare arguments, and every flag, must
//     behave exactly as before — otherwise a "discoverable front door" is just a
//     breaking change with a nicer help screen.

// TestVerbsConsumeThemselves checks that each verb is recognised, consumed, and
// leaves its arguments behind for the file/query decision.
func TestVerbsConsumeThemselves(t *testing.T) {
	cases := []struct {
		argv     []string
		wantArgs string
		check    func(o options) error
	}{
		{
			argv:     []string{"play", "lofi", "hip", "hop"},
			wantArgs: "lofi hip hop",
		},
		{
			argv: []string{"search", "lofi"},
			// The whole point of `search`: the argument is a query even though
			// it could be a path.
			wantArgs: "lofi",
			check:    func(o options) error { return mustTrue(o.forceSearch, "search did not force a search") },
		},
		{
			argv:     []string{"find", "lofi"},
			wantArgs: "lofi",
			check:    func(o options) error { return mustTrue(o.forceSearch, "find did not force a search") },
		},
		{
			argv:     []string{"viz", "radial"},
			wantArgs: "",
			check:    func(o options) error { return mustEqual(o.viz, "radial", "viz not applied") },
		},
		{
			argv:     []string{"palette", "ocean"},
			wantArgs: "",
			check:    func(o options) error { return mustEqual(o.palette, "ocean", "palette not applied") },
		},
		{
			argv:     []string{"list", "/tmp"},
			wantArgs: "",
			check:    func(o options) error { return mustEqual(o.library, "/tmp", "list did not set the directory") },
		},
	}
	for _, c := range cases {
		t.Run(strings.Join(c.argv, " "), func(t *testing.T) {
			o, err := parseArgs(c.argv)
			if err != nil {
				t.Fatalf("parseArgs(%v): %v", c.argv, err)
			}
			if got := strings.Join(o.args, " "); got != c.wantArgs {
				t.Errorf("args = %q, want %q", got, c.wantArgs)
			}
			if c.check != nil {
				if err := c.check(o); err != nil {
					t.Error(err)
				}
			}
		})
	}
}

// TestVerbsDoNotBreakBareArguments is the compatibility half. Every one of these
// worked before the verbs existed and must behave identically now.
func TestVerbsDoNotBreakBareArguments(t *testing.T) {
	for _, c := range []struct {
		argv     []string
		wantArgs string
	}{
		{[]string{"lofi", "hip", "hop"}, "lofi hip hop"},
		// A song called "play" must still be findable. The verb only counts when
		// something follows it.
		{[]string{"play"}, "play"},
		{[]string{"search"}, "search"},
		{[]string{"something", "play", "with"}, "something play with"},
		// `list` is the exception: its argument is optional, so a bare `list`
		// means the current directory rather than a search for the word.
		{[]string{"list"}, ""},
	} {
		o, err := parseArgs(c.argv)
		if err != nil {
			t.Fatalf("parseArgs(%v): %v", c.argv, err)
		}
		if got := strings.Join(o.args, " "); got != c.wantArgs {
			t.Errorf("parseArgs(%v): args = %q, want %q", c.argv, got, c.wantArgs)
		}
		if o.forceSearch {
			t.Errorf("parseArgs(%v) forced a search", c.argv)
		}
	}
}

// TestFlagsStillWork: a verb layer that quietly changes a flag's meaning is
// worse than no verb layer, and -l/--viz/--palette are the documented spelling.
func TestFlagsStillWork(t *testing.T) {
	o, err := parseArgs([]string{"-l", "/tmp", "--viz", "radial", "--palette", "ocean", "-a", "-q", "480"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if o.library != "/tmp" {
		t.Errorf("-l gave library %q, want /tmp", o.library)
	}
	if o.viz != "radial" || o.palette != "ocean" {
		t.Errorf("--viz/--palette gave %q/%q, want radial/ocean", o.viz, o.palette)
	}
	if !o.ascii {
		t.Error("-a not set")
	}
	if o.quality != Quality(480) {
		t.Errorf("-q gave %v, want 480", o.quality)
	}

	// And the flag/verb forms must accept exactly the same names.
	for _, name := range VizNames() {
		ov, err := parseArgs([]string{"viz", name, "q"})
		if err != nil {
			t.Errorf("viz %q: %v", name, err)
			continue
		}
		if !strings.EqualFold(ov.viz, name) {
			t.Errorf("viz %q gave %q", name, ov.viz)
		}
	}
	for _, name := range paletteNames() {
		op, err := parseArgs([]string{"palette", name, "q"})
		if err != nil {
			t.Errorf("palette %q: %v", name, err)
			continue
		}
		if !strings.EqualFold(op.palette, name) {
			t.Errorf("palette %q gave %q", name, op.palette)
		}
	}
}

// TestVerbsRejectBadValues: a verb that accepts a name the flag rejects is two
// lists to keep in step, and oneNamed exists so there is only one.
func TestVerbsRejectBadValues(t *testing.T) {
	for _, argv := range [][]string{
		{"viz", "nonsense", "q"},
		{"palette", "nonsense", "q"},
		{"viz"},
		{"palette"},
		{"list", "/no/such/directory/here"},
	} {
		if _, err := parseArgs(argv); err == nil {
			t.Errorf("parseArgs(%v) should have errored", argv)
		}
	}
}

// TestListVerbWithNoArgumentIsTheCurrentDirectory: `rasterbar list` with nothing
// after it must not slice past the end of an empty slice. It did, and it took
// the process down with a panic before the scan ever started.
func TestListVerbWithNoArgumentIsTheCurrentDirectory(t *testing.T) {
	o, err := parseArgs([]string{"list"})
	if err != nil {
		t.Fatalf("parseArgs(list): %v", err)
	}
	if o.library != "." {
		t.Errorf("library = %q, want %q", o.library, ".")
	}
	if len(o.args) != 0 {
		t.Errorf("args = %v, want none", o.args)
	}

	dir := t.TempDir()
	song := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(song, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	o2, err := parseArgs([]string{"list", dir})
	if err != nil {
		t.Fatalf("parseArgs(list DIR): %v", err)
	}
	if o2.library != dir {
		t.Errorf("library = %q, want %q", o2.library, dir)
	}
	if st, err := os.Stat(o2.library); err != nil || !st.IsDir() {
		t.Errorf("library %q is not a directory", o2.library)
	}
}

func mustTrue(b bool, msg string) error {
	if !b {
		return errStr(msg)
	}
	return nil
}

func mustEqual(got, want, msg string) error {
	if got != want {
		return errStr(msg + ": got " + got + ", want " + want)
	}
	return nil
}

type errStr string

func (e errStr) Error() string { return string(e) }
