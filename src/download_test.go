package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// download_test.go covers the offline half of the cache: the ID parser, the
// lookup, the yt-dlp argument list, and the precedence that picks the folder.
// The real download needs network and ffmpeg and is the manual tier, like the
// SIGSTOP/SIGINT cases AGENTS.md exempts from the suite.

func TestYoutubeID(t *testing.T) {
	cases := []struct {
		link string
		want string
	}{
		{"https://www.youtube.com/watch?v=ahx2kdqMWYE", "ahx2kdqMWYE"},
		{"https://youtube.com/watch?v=ahx2kdqMWYE", "ahx2kdqMWYE"},
		{"https://music.youtube.com/watch?v=ahx2kdqMWYE", "ahx2kdqMWYE"},
		{"https://m.youtube.com/watch?v=ahx2kdqMWYE&t=90", "ahx2kdqMWYE"},
		{"https://youtu.be/ahx2kdqMWYE", "ahx2kdqMWYE"},
		{"https://youtu.be/ahx2kdqMWYE?t=30", "ahx2kdqMWYE"},
		{"https://www.youtube.com/shorts/ahx2kdqMWYE", "ahx2kdqMWYE"},
		{"https://www.youtube.com/embed/ahx2kdqMWYE", "ahx2kdqMWYE"},
		{"https://www.youtube.com/live/ahx2kdqMWYE", "ahx2kdqMWYE"},
		// A URL is never a file and never a glob — but it is also not always
		// YouTube, and a non-YouTube ID would silently match nothing forever
		// rather than resolving as a plain link.
		{"https://vimeo.com/12345", ""},
		{"not a url at all", ""},
		{"https://www.youtube.com/", ""},
		{"https://youtu.be/segmented/into/two", ""},
	}
	for _, c := range cases {
		if got := youtubeID(c.link); got != c.want {
			t.Errorf("youtubeID(%q) = %q, want %q", c.link, got, c.want)
		}
	}
}

func TestCacheLookupMatchesByID(t *testing.T) {
	dir := t.TempDir()
	for _, f := range []string{
		"Some Long Title [abc123].mp4",
		"Other [xyz].mkv",
		"no-id-file.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	p, ok := cacheLookup(dir, "abc123")
	if !ok || filepath.Base(p) != "Some Long Title [abc123].mp4" {
		t.Errorf("abc123 lookup = %q, %v; want the mp4", p, ok)
	}
	if _, ok := cacheLookup(dir, "abc1"); ok {
		t.Error("a partial ID matched a full one")
	}
	// The needle is " [<id>]", the exact form yt-dlp's template writes, so the
	// brackets protect against a prefix of another ID rather than substring
	// luck: "abc1234" must not match "[abc123]".
	if _, ok := cacheLookup(dir, "abc1234"); ok {
		t.Error("abc1234 matched [abc123]")
	}
	if _, ok := cacheLookup(dir, "xyz"); !ok {
		t.Error("the mkv was skipped")
	}
	if _, ok := cacheLookup(dir, "notafile"); ok {
		t.Error("a txt file matched a media lookup")
	}
	// A missing directory is a miss, not an error: the play path resolves the
	// cache dir but deliberately never creates it.
	if _, ok := cacheLookup(filepath.Join(dir, "absent"), "abc123"); ok {
		t.Error("an absent directory claimed a hit")
	}
	if _, ok := cacheLookup(dir, ""); ok {
		t.Error("an empty ID claimed a hit")
	}

	// A subdirectory named like a track is not a file.
	sub := filepath.Join(dir, "Dir [sub1].mp4")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if p, ok := cacheLookup(dir, "sub1"); ok {
		t.Errorf("a directory was returned as a cached track: %q", p)
	}
}

func TestCacheTitleRecoversTheFilename(t *testing.T) {
	cases := []struct {
		path string
		want string
	}{
		{"/cache/Some Title [abc123].mp4", "Some Title"},
		{"/cache/Song (remix) [abc].mkv", "Song (remix)"},
		{"/cache/plain.mp3", "plain"},
		{"[brackets only].mp4", "[brackets only]"},
	}
	for _, c := range cases {
		if got := cacheTitle(c.path); got != c.want {
			t.Errorf("cacheTitle(%q) = %q, want %q", c.path, got, c.want)
		}
	}
}

func TestDownloadTrackArgsAreTheOneDownload(t *testing.T) {
	dir := "/cache-placeholder"
	args := downloadTrackArgs("https://youtu.be/abc", dir)
	joined := strings.Join(args, " ")
	for _, want := range []string{
		// Archive + no-overwrites is the skip-if-done pair; without them a
		// cached re-download burns bandwidth, and yt-dlp's "[download] ...
		// has already been recorded" exit-zero is then indistinguishable
		// from success.
		"--merge-output-format mp4",
		"-f " + downloadFormat,
		"--download-archive " + dir,
		"--no-overwrites",
		"--no-playlist",
		"--newline",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("download args missing %q: %s", want, joined)
		}
	}
	// The output name embeds the ID: that is the whole reason cacheLookup is
	// a directory scan instead of an index file.
	if !strings.Contains(joined, "%(title)s [%(id)s].%(ext)s") {
		t.Errorf("output template lost the ID: %s", joined)
	}
	// The cap exists because the renderer cannot show the difference
	// (AGENTS: never pay decode CPU for pixels you cannot see), and the
	// fallbacks exist because YouTube distributes no progressive format any
	// more.
	if !strings.Contains(downloadFormat, "1080") {
		t.Errorf("unbounded download format: %s", downloadFormat)
	}

	// One yt-dlp argument list rule: cookies land on the download path on the
	// same terms as every other call site, because YouTube bot-checks by IP.
	full := strings.Join(ytdlpAuthConfig{FromFile: "c.txt"}.withArgs(args...), " ")
	if !strings.Contains(full, "--cookies c.txt") {
		t.Errorf("download dropped the cookies: %s", full)
	}
}

func TestResolveCacheDirPrecedence(t *testing.T) {
	// The flag beats the settings; both beat the default.
	dir, err := resolveCacheDir("/flag", "~/settings")
	if err != nil || dir != "/flag" {
		t.Errorf("flag lost: %q %v", dir, err)
	}
	dir, err = resolveCacheDir("", "~/settings")
	if err != nil || dir == "" || strings.Contains(dir, "~") {
		t.Errorf("settings lost or unexpanded: %q %v", dir, err)
	}
	def, err := resolveCacheDir("", "")
	if err != nil || !strings.HasSuffix(def, defaultCacheDirName) {
		t.Errorf("default lost: %q %v", def, err)
	}
	// A file where a folder belongs is reported, not worked around.
	filePath := t.TempDir() + "/file"
	if err := os.WriteFile(filePath, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveCacheDir(filePath, ""); err == nil {
		t.Error("a file as cache dir did not error")
	}
}

func TestPrepareCacheDirCreates(t *testing.T) {
	flagDir := t.TempDir() + "/nested/deeper"
	dir, err := prepareCacheDir(flagDir, "")
	if err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(dir)
	if err != nil || !st.IsDir() {
		t.Errorf("prepare left no directory at %s: %v", dir, err)
	}
}

func TestServeFromCacheRewritesRemoteTracksOnly(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/Known Track [kid1].mp4", []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracks := []Track{
		{URL: "https://youtu.be/kid1"}, // hit
		{URL: "https://youtu.be/kid2"}, // miss
		{URL: "https://youtu.be/kid1", Title: "kept", // already local
			LocalPath: "/already/local.mp4"},
		{URL: "https://other.site/x", LocalPath: "/somewhere/file.mp4"}, // local, no ID
	}
	serveFromCache(dir, tracks)
	if !tracks[0].IsLocal() || cacheTitle(tracks[0].LocalPath) != "Known Track" {
		t.Errorf("hit was not served from cache: %+v", tracks[0])
	}
	if tracks[1].IsLocal() {
		t.Errorf("a miss was turned local: %+v", tracks[1])
	}
	if tracks[2].Title != "kept" || tracks[2].LocalPath != "/already/local.mp4" {
		t.Errorf("a pre-existing local track was rewritten: %+v", tracks[2])
	}
	if tracks[3].LocalPath != "/somewhere/file.mp4" {
		t.Errorf("untouched track changed: %+v", tracks[3])
	}
}
