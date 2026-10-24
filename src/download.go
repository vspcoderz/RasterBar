package main

import (
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

// download.go turns the cache into the argument for a download verb.
//
// The shape of the feature is decided by two facts the codebase already paid
// for: youtube googlevideo grants are single-use (one URL, one consumer — see
// ascii.go), and YouTube stopped distributing progressive formats entirely
// (AGENTS.md dead-end: `yt-dlp -f worst` finds no progressive `18` any more,
// 130-399 only). So:
//
//   - Playback of a *cache hit* plays the local file, which needs no grant at
//     all and gets seeking, chapters and pause for free through the
//     `Track.IsLocal()` machinery.
//   - A download is split-stream capture + one ffmpeg mux, which is what
//     yt-dlp itself does for `bestvideo+bestaudio`.
//
// One merged mp4 per video is the stored format, always — a music-mode user
// gets the same mp4, because the alternative (an extracted audio file for
// music, a video for `a`) is two files and two names for one video ID, and
// cache lookup keys on the ID.

// defaultCacheDirName is the flat directory everything downloads to. One dir,
// not an audio/ and video/ split: lookup is by video ID and the ID is in the
// filename, so one scan serves both kinds of media.
const defaultCacheDirName = "RasterStuff"

// cacheDirPresets are the values the settings screen cycles through. A cache
// directory needs a filesystem path, and the settings screen is deliberately
// click-only (no text inputs), so the row offers presets; `--cache-dir` and an
// edit of config.json cover anything else.
var cacheDirPresets = []string{
	"~/" + defaultCacheDirName,
	"~/.cache/rasterbar",
	"~/Music/rasterbar",
}

// resolveCacheDir applies the precedence: the flag stated most precisely wins
// over the settings value, which wins over the default. Tilde expansion happens
// here rather than in every caller, because the settings and default values
// both store the `~/` form.
//
// It never creates anything: playback calls it to *look* in the cache, and
// looking is not a reason to make folders. prepareCacheDir is the writing form.
func resolveCacheDir(flagDir, settingsDir string) (string, error) {
	dir := flagDir
	if dir == "" {
		dir = settingsDir
	}
	if dir == "" {
		dir = "~/" + defaultCacheDirName
	}
	dir = expandTilde(dir)
	if st, err := os.Stat(dir); err == nil && !st.IsDir() {
		return "", fmt.Errorf("cache dir %s exists but is a file", dir)
	}
	return dir, nil
}

// prepareCacheDir resolves the directory and makes sure it exists, for the
// paths that are about to write into it.
func prepareCacheDir(flagDir, settingsDir string) (string, error) {
	dir, err := resolveCacheDir(flagDir, settingsDir)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create cache dir %s: %w", dir, err)
	}
	return dir, nil
}

// youtubeID extracts a video ID from a YouTube link, empty for anything else.
//
// Only YouTube gets ID-based cache lookup. yt-dlp supports other sites, and
// downloads from one would still land in the cache named by *their* ID — but a
// future `rasterbar <url>` cannot find the file without running yt-dlp, which
// defeats the point of the cache. An unparsed URL means a cache miss, quietly.
func youtubeID(link string) string {
	u, err := url.Parse(link)
	if err != nil {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	host = strings.TrimPrefix(host, "www.")
	switch {
	case host == "youtu.be":
		if id := strings.Trim(u.Path, "/"); id != "" && strings.Count(id, "/") == 0 {
			return id
		}
	case host == "youtube.com" || host == "music.youtube.com" || host == "m.youtube.com":
		if v := u.Query().Get("v"); v != "" {
			return v
		}
		for _, seg := range []string{"shorts", "embed", "v", "live"} {
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) == 2 && parts[0] == seg {
				return parts[1]
			}
		}
	}
	return ""
}

// idOf is the cache key for a track: an explicit ID from search results, else
// one parsed from the URL.
func idOf(t Track) string {
	if t.ID != "" {
		return t.ID
	}
	return youtubeID(t.URL)
}

// cacheLookup finds a downloaded file by video ID.
//
// Directory scan and substring, not filepath.Glob: `Title [ID].mp4` contains
// glob metacharacters (`[`) and every name would need escaping. Titles also
// legitimately contain brackets, so the match is on " [<id>]" — the literal
// form the output template writes — anchored on both sides being the separator
// convention of yt-dlp's %(title)s [%(id)s].
func cacheLookup(dir, id string) (string, bool) {
	if id == "" {
		return "", false
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", false
	}
	needle := "[" + id + "]"
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.Contains(name, needle) {
			continue
		}
		if !mediaExts[strings.ToLower(filepath.Ext(name))] {
			continue
		}
		return filepath.Join(dir, name), true
	}
	return "", false
}

// cacheTitle recovers a display title from a cached filename: everything before
// the final " [<id>]". A file written by the template always has the bracket
// suffix; one placed by hand may not, and then the whole name (without its
// extension) is the best title available.
func cacheTitle(path string) string {
	name := filepath.Base(path)
	if i := strings.LastIndex(name, " ["); i > 0 {
		return name[:i]
	}
	return strings.TrimSuffix(name, filepath.Ext(name))
}

// trackForURL builds a queue entry for a bare link. Title, duration and ID are
// filled in later by fetchTrackMeta (and the queue line falls back to the URL
// itself when that fails), because the player needs them and the queue does
// not — but an empty Title is the marker the fill pass keys on, so it stays
// empty here rather than carrying a placeholder lie.
func trackForURL(link string) Track {
	return Track{
		Scraper: "url",
		URL:     link,
		ID:      youtubeID(link),
	}
}

// fetchTrackMeta fills Title, ID and Duration from one `yt-dlp -J`. Metadata is
// not a grant consumer, so this does not two-step with playback's resolving —
// the same response shape resolveMedia already reads.
func fetchTrackMeta(t *Track) error {
	out, err := ytdlpAuth.run("-J", "--no-warnings", "--no-playlist", t.URL)
	if err != nil {
		return fmt.Errorf("yt-dlp -J: %w", err)
	}
	var info ytInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return fmt.Errorf("parse yt-dlp -J: %w", err)
	}
	if info.Title != "" {
		t.Title = info.Title
	}
	if info.VideoID != "" {
		t.ID = info.VideoID
	}
	if info.Duration > 0 {
		d := formatDuration(int(info.Duration))
		t.Duration = &d
	}
	return nil
}

// downloadFormat is the format selector. Explained piecewise in
// downloadTrackArgs's comment; kept as a const so a test can pin it and so the
// string lives once.
const downloadFormat = "bv*[height<=1080]+ba/b[height<=1080]/b"

// serveFromCache rewrites every remote track whose video ID is already
// downloaded into a local one. Title comes from the cached filename, which is
// what the template put there, and there is no network in this path to ask.
//
// It mutates the slice, because every alternative (returning a copy, a second
// slice) invites a caller to forget which half reached the player. Only called
// on tracks the player is about to own.
func serveFromCache(dir string, tracks []Track) {
	for i := range tracks {
		if tracks[i].IsLocal() || tracks[i].URL == "" {
			continue
		}
		p, ok := cacheLookup(dir, idOf(tracks[i]))
		if !ok {
			continue
		}
		tracks[i].LocalPath = p
		if title := cacheTitle(p); title != "" {
			tracks[i].Title = title
		}
	}
}

// downloadTrack ensures the track exists in the cache and reports where.
//
// The returned bool is "was already cached", which is worth publishing because
// otherwise a download of an already-downloaded video looks broken — a long
// silent pause followed by a file that did not change. The ID comes from
// anywhere it is stable (search result, metadata, URL parse); without one the
// downloaded file cannot be found afterwards, so the download fetches it first.
func downloadTrack(t Track, dir string) (path string, already bool, err error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", false, fmt.Errorf("create cache dir %s: %w", dir, err)
	}
	if t.ID == "" {
		if err := fetchTrackMeta(&t); err != nil {
			return "", false, err
		}
	}
	if p, ok := cacheLookup(dir, t.ID); ok {
		return p, true, nil
	}
	if _, derr := ytdlpAuth.runStream(downloadTrackArgs(t.URL, dir)...); derr != nil {
		return "", false, derr
	}
	p, ok := cacheLookup(dir, t.ID)
	if !ok {
		return "", false, fmt.Errorf(
			"yt-dlp reported success but no cached file matches [%s]", t.ID)
	}
	return p, false, nil
}

// downloadTrackArgs is one yt-dlp invocation as an argument list.
//
// Format selector: `bv*[height<=1080]+ba` merges a best video (any codec, at
// most the terminal-size answer this program already uses) with best audio;
// `b[height<=1080]` and finally `b` fall back to progressive containers when
// YouTube offers none, which it mostly does not (AGENTS.md dead-end table).
// 1080 is the cap because the renderer cannot show the difference on a terminal
// ("never pay decode CPU for pixels you cannot see") and neither can a cache.
//
// `--download-archive` is the skip-if-done index, keyed by video ID; a
// re-invocation of an already-downloaded URL prints "[download] ... has already
// been recorded" and exits zero — which is why downloadTrack checks the cache
// *before* the call: the silent exit is indistinguishable from success.
//
// `--newline` makes yt-dlp emit progress as lines rather than CR-updated bars,
// because the streaming variant of run hands stderr to the terminal, and a CR
// bar is exactly what a terminal does not want while the player is quiet.
func downloadTrackArgs(link, dir string) []string {
	return []string{
		link,
		"-f", downloadFormat,
		"--merge-output-format", "mp4",
		"-o", filepath.Join(dir, "%(title)s [%(id)s].%(ext)s"),
		"--download-archive", filepath.Join(dir, "downloaded.txt"),
		"--no-overwrites",
		"--no-playlist",
		"--no-warnings",
		"--newline",
	}
}
