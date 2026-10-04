package main

import "testing"

// TestClassifyMedia covers the filename shapes an anime library actually
// contains. Expected values are hand-derived from each name, not recomputed the
// way the parser does them.
//
// The cases that matter most are the ambiguous ones: a title year that must not
// be read as an episode number, a resolution tag that must not outrank the real
// episode number, and absolute numbering that must not be mistaken for a season.
func TestClassifyMedia(t *testing.T) {
	cases := []struct {
		name string
		file string
		// wantSeries "" means only the episode number is asserted.
		wantSeries  string
		wantSeason  int
		wantEpisode int
	}{
		{"sxxexx spaced", "Cowboy Bebop S01E02.mkv", "Cowboy Bebop", 1, 2},
		{"sxxexx dotted", "cowboy.bebop.s01e02.mkv", "cowboy bebop", 1, 2},
		{"sxxexx underscored", "Cowboy_Bebop_S01E12.mkv", "Cowboy Bebop", 1, 12},
		{"x notation", "Cowboy Bebop - 01x05.mkv", "Cowboy Bebop", 1, 5},
		{"episode word", "Cowboy Bebop Episode 7.mkv", "Cowboy Bebop", 0, 7},
		{"ep dot", "Cowboy Bebop Ep.7.mkv", "Cowboy Bebop", 0, 7},
		{"bare dash number", "Cowboy Bebop - 07 -.mkv", "Cowboy Bebop", 0, 7},
		{"bare underscore", "Cowboy_Bebop_07.mkv", "Cowboy Bebop", 0, 7},
		{"bracketed", "Cowboy Bebop [07].mkv", "Cowboy Bebop", 0, 7},
		{"resolution tag must not win", "Cowboy Bebop - 07 - Title 1080p.mkv", "Cowboy Bebop", 0, 7},
		{"version suffix must not win", "Cowboy Bebop 07v2.mkv", "Cowboy Bebop", 0, 7},
		{"trailing quality does not become series", "Cowboy Bebop S01E02 1080p HEVC.mkv", "Cowboy Bebop", 1, 2},
		{"release group stripped", "Cowboy Bebop - 07 [SubsPlease].mkv", "Cowboy Bebop", 0, 7},
		{"bracket quality stripped", "Cowboy Bebop S01E02 [BD 1080p].mkv", "Cowboy Bebop", 1, 2},
		{"parens quality stripped", "Cowboy Bebop S01E02 (BD x264).mkv", "Cowboy Bebop", 1, 2},
		{"title year is not an episode", "Some Show (2019) - 03.mkv", "Some Show", 0, 3},
		{"double digit episode", "Some Show - 10.mkv", "Some Show", 0, 10},
		{"three digit episode", "Some Show - 100.mkv", "Some Show", 0, 100},
		{"no episode marker is skipped", "Some Show.mkv", "", 0, 0},
		{"trailer is not an episode", "Some Show - Trailer.mkv", "", 0, 0},
		{"zero is not an episode", "Some Show - 00.mkv", "", 0, 0},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, ok := classifyMedia(c.file)
			if c.wantEpisode == 0 {
				if ok {
					t.Fatalf("classifyMedia(%q) = %+v, want skipped", c.file, got)
				}
				return
			}
			if !ok {
				t.Fatalf("classifyMedia(%q) skipped, want episode %d", c.file, c.wantEpisode)
			}
			if got.Episode != c.wantEpisode {
				t.Errorf("episode = %d, want %d", got.Episode, c.wantEpisode)
			}
			if got.Season != c.wantSeason {
				t.Errorf("season = %d, want %d", got.Season, c.wantSeason)
			}
			if got.Series != c.wantSeries {
				t.Errorf("series = %q, want %q", got.Series, c.wantSeries)
			}
		})
	}
}

func TestSeasonFromDir(t *testing.T) {
	cases := map[string]int{
		"Season 2":      2,
		"Season 02":     2,
		"S02":           2,
		"S02 [BD]":      2,
		"Show Season 3": 3,
		"Season One":    0,
		"Cowboy Bebop":  0,
	}
	for dir, want := range cases {
		if got := seasonFromDir(dir); got != want {
			t.Errorf("seasonFromDir(%q) = %d, want %d", dir, got, want)
		}
	}
}

func TestDisplayTitle(t *testing.T) {
	cases := []struct {
		file string
		want string
	}{
		{"Cowboy Bebop S01E02.mkv", "S01E02  Cowboy Bebop"},
		{"Cowboy Bebop - 07.mkv", "E07  Cowboy Bebop"},
		{"Show S02E10 - The Test.mkv", "S02E10  Show - The Test"},
		// A release with no episode name leaves the title off entirely rather
		// than trailing a dangling separator.
		{"Cowboy Bebop - 07 [SubsPlease].mkv", "E07  Cowboy Bebop"},
	}
	for _, c := range cases {
		key, ok := classifyMedia(c.file)
		if !ok {
			t.Fatalf("classifyMedia(%q) skipped", c.file)
		}
		if got := displayTitle(c.file, key); got != c.want {
			t.Errorf("displayTitle(%q) = %q, want %q", c.file, got, c.want)
		}
	}
}

// TestIsLocal pins the branch the whole library mode hangs on: a library track
// must never reach yt-dlp.
func TestIsLocal(t *testing.T) {
	local := Track{LocalPath: "/tmp/show/e01.mkv"}
	if !local.IsLocal() {
		t.Error("library track not detected as local")
	}
	remote := Track{URL: "https://youtube.com/watch?v=abc"}
	if remote.IsLocal() {
		t.Error("youtube track wrongly detected as local")
	}
}

// TestLibraryChannelText covers the channel column for local files. "unknown"
// there reads like a scraping failure, and this is the assertion that stops it
// coming back.
func TestLibraryChannelText(t *testing.T) {
	withSeries := Track{LocalPath: "/tmp/bebop/s01e01.mkv", Series: "Cowboy Bebop"}
	if got := withSeries.ChannelText(); got != "Cowboy Bebop" {
		t.Errorf("channel = %q, want the series name", got)
	}
	noSeries := Track{LocalPath: "/tmp/loose/e01.mkv"}
	if got := noSeries.ChannelText(); got != "local" {
		t.Errorf("channel = %q, want %q", got, "local")
	}
	// A network track must keep reporting its channel, and "unknown" when there
	// is none — that path is unchanged.
	yt := Track{URL: "x", Channel: "Art Is Sound"}
	if got := yt.ChannelText(); got != "Art Is Sound" {
		t.Errorf("channel = %q, want %q", got, "Art Is Sound")
	}
	if got := (Track{URL: "x"}).ChannelText(); got != "unknown" {
		t.Errorf("channel = %q, want %q", got, "unknown")
	}
}

// TestSortLibrary pins episode ordering numerically, which is the whole reason
// sorting exists. A lexical sort puts episode 10 before episode 2.
func TestSortLibrary(t *testing.T) {
	tracks := []Track{
		{Series: "B", Episode: 2, LocalPath: "b02"},
		{Series: "A", Episode: 10, LocalPath: "a10"},
		{Series: "A", Episode: 2, LocalPath: "a02"},
		{Series: "A", Season: 2, Episode: 1, LocalPath: "aS02e01"},
		{Series: "A", Season: 1, Episode: 3, LocalPath: "aS01e03"},
		{Series: "A", Season: 1, Episode: 1, LocalPath: "aS01e01"},
	}
	sortLibrary(tracks)

	// Episode 2 before episode 10 is the whole reason this function exists, and
	// season 1 before season 2 is the other half.
	want := []string{"a02", "a10", "aS01e01", "aS01e03", "aS02e01", "b02"}
	for i, tr := range tracks {
		if tr.LocalPath != want[i] {
			t.Errorf("position %d = %q, want %q (full order %v)", i, tr.LocalPath, want[i], tracks)
		}
	}
}
