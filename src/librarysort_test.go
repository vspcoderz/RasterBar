package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestNaturalLessOrdersNumbersAsNumbers is the reason this file exists.
//
// sort.Strings puts "track 10.mp3" before "track 2.mp3". Episode numbers solve
// that by parsing them; a music library has no episode numbers to parse, so the
// comparison has to find the digits itself. These expectations are the behaviour
// a person would describe out loud, not the implementation's own arithmetic.
func TestNaturalLessOrdersNumbersAsNumbers(t *testing.T) {
	cases := []struct {
		a, b string
		want bool
	}{
		{"track 2.mp3", "track 10.mp3", true},
		{"track 10.mp3", "track 2.mp3", false},
		// Leading zeros are a value, not a position: "02 - prelude" is track 2
		// and sorts after track 1. Read as text the leading "0" would win.
		{"1 - Overture", "02 - prelude", true},
		{"02 - prelude", "1 - Overture", false},
		{"celestial.wav", "checkmate.mp3", true},
		{"checkmate.mp3", "celestial.wav", false},
		{"a.mp3", "b.mp3", true},
		// Case-folded, so an album directory does not sort by capitalisation.
		{"Album/track 2.mp3", "album/Track 10.mp3", true},
		// Equal in value, so the shorter run wins to keep the order stable.
		{"1.mp3", "01.mp3", true},
		{"01.mp3", "1.mp3", false},
		{"x", "x", false}, // equal
		{"Track 9", "Track 10", true},
	}
	for _, c := range cases {
		if got := naturalLess(c.a, c.b); got != c.want {
			t.Errorf("naturalLess(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestNaturalLessIsATotalOrder checks the property sort.SliceStable depends on.
// A comparator that reports both a<b and b<a for some pair makes the sort's
// output depend on the input order, which is the "reordering a directory scan
// changes nothing" property sortLibrary is supposed to have.
func TestNaturalLessIsATotalOrder(t *testing.T) {
	names := []string{
		"track 2.mp3", "track 10.mp3", "Track 9.mp3", "01.mp3", "1.mp3",
		"celestial.wav", "checkmate.mp3", "a.mp3", "b.mp3", "02 - prelude",
		"1 - Overture", "x", "x ", "10 - ten", "9 - nine",
	}
	for _, a := range names {
		for _, b := range names {
			if naturalLess(a, b) && naturalLess(b, a) {
				t.Errorf("both naturalLess(%q,%q) and naturalLess(%q,%q) are true", a, b, b, a)
			}
		}
	}
}

// TestSortLibraryPutsUnnumberedFilesInFilenameOrder is the change to library
// scanning: a music folder used to scan to zero tracks, because classifyMedia
// required an episode marker.
func TestSortLibraryPutsUnnumberedFilesInFilenameOrder(t *testing.T) {
	tracks := []Track{
		{LocalPath: "/m/song10.mp3", Title: "song10"},
		{LocalPath: "/m/song2.mp3", Title: "song2"},
		{LocalPath: "/m/Nocturne.opus", Title: "Nocturne"},
		{LocalPath: "/m/celestial.wav", Title: "celestial"},
	}
	sortLibrary(tracks)
	want := []string{"celestial", "Nocturne", "song2", "song10"}
	for i, w := range want {
		if tracks[i].Title != w {
			t.Errorf("position %d = %q, want %q (full order: %v)", i, tracks[i].Title, w, titles(tracks))
		}
	}
}

// TestSortLibraryStillOrdersEpisodes is the other half: numbered files must keep
// exactly the order they had, because that is a TV library's whole reason to
// exist and the change must not have touched it.
func TestSortLibraryStillOrdersEpisodes(t *testing.T) {
	tracks := []Track{
		{LocalPath: "/l/Show S01E10.mkv", Series: "Show", Season: 1, Episode: 10},
		{LocalPath: "/l/Show S01E02.mkv", Series: "Show", Season: 1, Episode: 2},
		{LocalPath: "/l/Other S01E01.mkv", Series: "Other", Season: 1, Episode: 1},
		{LocalPath: "/l/Show S02E01.mkv", Series: "Show", Season: 2, Episode: 1},
		{LocalPath: "/l/extra.mkv", Episode: 0},
	}
	sortLibrary(tracks)
	// Numbered first, then the unnumbered extra by name.
	want := []string{
		"Other S01E01.mkv", "Show S01E02.mkv", "Show S01E10.mkv",
		"Show S02E01.mkv", "extra.mkv",
	}
	for i, w := range want {
		if tracks[i].LocalPath != "/l/"+w {
			t.Errorf("position %d = %q, want %q", i, tracks[i].LocalPath, "/l/"+w)
		}
	}
}

// TestDisplayTitleOmitsThePrefixForUnnumberedFiles: "E00  Song" is not a worse
// version of "Song", it is a claim about a number the file never had, and it is
// what made a music folder look like a broken television library.
func TestDisplayTitleOmitsThePrefixForUnnumberedFiles(t *testing.T) {
	got := displayTitle("nocturne.mp3", mediaKey{Series: "songs", Title: "nocturne"})
	if got != "nocturne" {
		t.Errorf("displayTitle for an unnumbered file = %q, want just the title", got)
	}
	if got := displayTitle("x.mp3", mediaKey{Series: "", Title: "x"}); got != "x" {
		t.Errorf("displayTitle with no series = %q, want %q", got, "x")
	}
}

// TestReadPlaylistResolvesRelativeEntries is the whole point of .m3u support: a
// playlist is portable, so its entries are relative to itself.
func TestReadPlaylistResolvesRelativeEntries(t *testing.T) {
	dir := t.TempDir()
	write := func(name string) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		return p
	}
	write("one.mp3")
	write("two.opus")
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	write(filepath.Join("sub", "three.wav"))

	pl := filepath.Join(dir, "list.m3u")
	// A BOM (see bom below), comments, a blank line, a nested relative path, an absolute path, a
	// missing entry and a URL — every shape a real playlist file contains.
	body := bom + "#EXTM3U\n" +
		"#EXTINF:123,Artist - One\n" +
		"one.mp3\n" +
		"\n" +
		"./two.opus\n" +
		"sub/three.wav\n" +
		"missing.mp3\n" +
		"https://example.com/remote.mp3\n" +
		"# a trailing comment\n"
	if err := os.WriteFile(pl, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	got, err := readPlaylist(pl, 0)
	if err != nil {
		t.Fatalf("readPlaylist: %v", err)
	}
	want := []string{
		filepath.Join(dir, "one.mp3"),
		filepath.Join(dir, "two.opus"),
		filepath.Join(dir, "sub", "three.wav"),
	}
	if len(got) != len(want) {
		t.Fatalf("got %d entries %v, want %d %v", len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// TestReadPlaylistFollowsOneLevelAndStops pins the recursion bound. A playlist
// that lists itself must terminate; that is the only property that matters here,
// and maxPlaylistDepth is what provides it.
func TestReadPlaylistFollowsOneLevelAndStops(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "a.mp3")
	if err := os.WriteFile(song, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(dir, "nested.m3u")
	self := filepath.Join(dir, "self.m3u")
	for _, p := range []string{nested, self} {
		body := "a.mp3\nnested.m3u\n"
		if p == self {
			body = "a.mp3\nself.m3u\nnested.m3u\n"
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	done := make(chan []string, 1)
	go func() {
		got, err := readPlaylist(self, 0)
		if err != nil {
			done <- nil
			return
		}
		done <- got
	}()

	select {
	case got := <-done:
		if len(got) == 0 {
			t.Fatal("no entries resolved")
		}
		if got[0] != song {
			t.Errorf("first entry = %q, want %q", got[0], song)
		}
	case <-timeoutAfter():
		t.Fatal("readPlaylist did not terminate on a self-referencing playlist")
	}
}

// TestLooksLikeFileKeepsSearchQueriesAsQueries is the regression that motivated
// the whole file-argument path: `rasterbar ~/Music/song.mp3` used to search YouTube
// for that string and return ten unrelated videos.
func TestLooksLikeFileKeepsSearchQueriesAsQueries(t *testing.T) {
	dir := t.TempDir()
	song := filepath.Join(dir, "song.mp3")
	if err := os.WriteFile(song, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if !looksLikeFile(song) {
		t.Errorf("looksLikeFile(%q) = false, want true", song)
	}
	if looksLikeFile("lofi hip hop radio") {
		t.Error("a search query was taken for a file")
	}
	if !looksLikeFile(filepath.Join(dir, "*.mp3")) {
		t.Error("a quoted glob was not recognised as a file intent")
	}
	// A directory is a path the user typed, so it is a file *intent* — and
	// loadPathArg turns that into "use -l <dir>", which is the useful answer.
	// Reporting false sent a bare directory to YouTube search.
	if !looksLikeFile(dir) {
		t.Error("a directory was treated as a search query")
	}
	if _, _, err := loadPathArg(dir, 0); err == nil {
		t.Error("a bare directory was accepted without pointing at the list verb")
	}
}

func titles(ts []Track) []string {
	out := make([]string, len(ts))
	for i, t := range ts {
		out[i] = t.Title
	}
	return out
}

// bom is the UTF-8 byte order mark a Windows-authored playlist starts with.
var bom = "\ufeff"

func timeoutAfter() <-chan time.Time { return time.After(2 * time.Second) }

// TestExpandTilde covers the quoted form of every path input.
//
// A quoted `~/Music/*.flac` is what someone types when they want the program
// rather than the shell to do the expanding, and Go has no tilde expansion — not
// in os.Stat, not in filepath.Glob. So the documented example worked (unquoted,
// the shell expands it first) and the other obvious one silently matched nothing.
// The bug hides in exactly the shape that "works fine when I try it".
func TestExpandTilde(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		t.Skip("no home directory")
	}
	cases := []struct{ in, want string }{
		{"~", home},
		{"~/Music", filepath.Join(home, "Music")},
		{"~/a/b/c.flac", filepath.Join(home, "a/b/c.flac")},
		{"~notauser/x", "~notauser/x"}, // not resolved: reading passwd is not ours
		{"/absolute/~/path", "/absolute/~/path"},
		{"plain", "plain"},
		{"", ""},
	}
	for _, c := range cases {
		if got := expandTilde(c.in); got != c.want {
			t.Errorf("expandTilde(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// The whole point: a glob under a quoted ~ now resolves.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "a.mp3"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	rel, err := filepath.Rel(home, dir)
	if err != nil || !strings.HasPrefix(rel, "..") {
		// The temp dir is not under $HOME, which is the normal case and cannot be
		// exercised with a ~ path. Skip rather than assert something vacuous.
		t.Skipf("temp dir %q is not under %q", dir, home)
	}
	if !looksLikeFile("~/" + filepath.Join(rel, "*.mp3")) {
		t.Error("a quoted ~/ glob was not recognised as a file intent")
	}
	paths, isFile, err := loadPathArg("~/"+filepath.Join(rel, "*.mp3"), 0)
	if err != nil {
		t.Fatalf("loadPathArg: %v", err)
	}
	if !isFile || len(paths) != 1 {
		t.Errorf("quoted ~/ glob gave %d paths (isFile=%v), want 1", len(paths), isFile)
	}
}

// TestMusicModeForFallsBackOnlyForVideoLessFiles pins the one place the two
// modes are chosen.
//
// It is a function rather than a line inside newTrackSession because the answer
// changes the grid, the chrome height, the tap and every `if o.music` in the
// render loop. When it lived in the session, a local mp3 in video mode produced
// nil-buffer frames that the loop handed to the *video* renderer, which rejects
// them as "frame too small" and ends the track with no message: `-a` on an mp3
// painted nothing at all.
//
// The non-local case is asserted without a network: a YouTube result is never
// videoLess, so the decision must be a pure pass-through there and the function
// must not shell out.
func TestMusicModeForFallsBackOnlyForVideoLessFiles(t *testing.T) {
	// Returns true for "plays as a visualiser".
	remote := Track{URL: "https://youtu.be/x"}
	if musicModeFor(remote, false) {
		t.Error("a remote track asked for video fell back to music")
	}
	if !musicModeFor(remote, true) {
		t.Error("a remote track asked for music left music mode")
	}
	if musicModeFor(Track{}, false) {
		t.Error("an empty track fell back to music without a probe")
	}
	if !musicModeFor(Track{}, true) {
		t.Error("an empty track asked for music left music mode")
	}
}
