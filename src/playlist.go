package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// playlist.go turns command-line arguments that name files into a queue.
//
// Three sources, one Track shape:
//
//	rasterbar song.mp3 b.opus        explicit files, in the order given
//	rasterbar ~/Music/*.flac          a shell glob, or one we expand ourselves
//	rasterbar roadtrip.m3u            a playlist
//
// The rule that keeps `rasterbar "lofi hip hop radio"` working unchanged: a
// positional argument becomes a file only if it *is* a file (or a glob that
// matched, or a playlist that exists). Anything else is a search query, which is
// what every argument was before this existed.
//
// No dependency. m3u is three lines of format and a `#` comment convention, and a
// playlist parser is exactly the sort of thing that pulls in a library for the
// convenience of handling formats nobody uses.

// maxPlaylistDepth stops a playlist that includes itself, directly or through a
// chain, from recursing forever. One level is what real playlists use; two is
// enough for a folder-of-playlists layout and still terminates.
const maxPlaylistDepth = 2

// isPlaylistExt reports whether a name is a playlist rather than media.
func isPlaylistExt(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".m3u", ".m3u8":
		return true
	}
	return false
}

// looksLikeFile reports whether an argument should be treated as a file input
// rather than a search query.
//
// A glob is included: `~/Music/*.flac` typed with the quotes still means the
// user's intent, and a shell that already expanded it produced plain paths that
// take the same path through here. isLocalArg exists so the check is in one place.
func looksLikeFile(arg string) bool {
	if arg == "" {
		return false
	}
	if _, err := os.Stat(arg); err == nil {
		// A directory counts, deliberately: it is a path the user typed, so it is
		// not a search query. loadPathArg then says "-l <dir>", which is the
		// useful answer. Returning false here sent a bare directory to YouTube
		// search, and the user got ten unrelated videos for a folder path.
		return true
	}
	return hasGlobMeta(arg)
}

// hasGlobMeta reports whether a path contains a shell wildcard.
//
// Checked rather than assumed because the shell has usually already expanded it
// — unquoted globs arrive as literal paths — but a quoted one reaches us intact,
// and expanding it here is the difference between "played nothing" and "played
// the album".
func hasGlobMeta(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// expandGlob resolves a wildcard argument, returning nil when it matched nothing.
//
// A no-match glob is *not* an error and *not* a search query: the user asked for
// files, got none, and silently searching YouTube for "~/Music/*.flac" is worse
// than saying the glob matched nothing. The caller reports it.
func expandGlob(pattern string) ([]string, error) {
	matches, err := filepath.Glob(pattern)
	if err != nil {
		return nil, fmt.Errorf("bad pattern %q: %w", pattern, err)
	}
	var out []string
	for _, m := range matches {
		if st, err := os.Stat(m); err == nil && !st.IsDir() {
			out = append(out, m)
		}
	}
	return out, nil
}

// loadPathArg expands one command-line argument into zero or more paths.
//
// Zero means "not a file input at all" — which is how a search query stays a
// search query. More than one means a glob matched, and a playlist means its
// entries.
func loadPathArg(arg string, depth int) (paths []string, isFileInput bool, err error) {
	st, serr := os.Stat(arg)
	switch {
	case serr == nil && !st.IsDir():
		if isPlaylistExt(arg) {
			entries, perr := readPlaylist(arg, depth)
			if perr != nil {
				return nil, true, perr
			}
			return entries, true, nil
		}
		if !mediaExts[strings.ToLower(filepath.Ext(arg))] {
			// A real file we do not play. Treated as a file input so the caller
			// can say "no playable files" rather than searching YouTube for the
			// user's own filename.
			return nil, true, nil
		}
		return []string{arg}, true, nil

	case serr == nil && st.IsDir():
		// A bare directory is what -l is for: handing it here would duplicate the
		// walk and the episode parser. Reported as a file intent so the message
		// names the fix — falling through to a YouTube search for a path the user
		// typed is the least useful of the three possible answers.
		return nil, true, fmt.Errorf("%s is a directory: use -l %s", arg, arg)

	case hasGlobMeta(arg):
		matches, gerr := expandGlob(arg)
		if gerr != nil {
			return nil, true, gerr
		}
		if len(matches) == 0 {
			return nil, true, nil
		}
		for _, m := range matches {
			paths = append(paths, m)
		}
		return paths, true, nil
	}
	return nil, false, nil
}

// readPlaylist parses an .m3u/.m3u8 into absolute-or-relative media paths.
//
// Entries are resolved against the playlist's own directory, because a playlist is
// portable and relative entries are the norm — an absolute path in an .m3u means
// it only works on the machine that made it. A nested playlist is followed to
// maxPlaylistDepth, which is what stops a playlist that lists itself.
//
// Lines starting with '#' are comments, which in practice also covers the
// '#EXTM3U' and '#EXTINF' headers.
func readPlaylist(path string, depth int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("playlist %s: %w", path, err)
	}
	base := filepath.Dir(path)

	var out []string
	seen := map[string]bool{}
	add := func(p string) {
		p = strings.TrimSpace(p)
		if p == "" || seen[p] {
			return
		}
		seen[p] = true
		out = append(out, p)
	}

	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		// A Windows-authored playlist carries a UTF-8 BOM on the first line,
		// which would otherwise become part of the first path and produce an
		// entry that matches nothing. Trimmed per line rather than off the whole
		// file so a BOM elsewhere costs nothing.
		line = strings.TrimPrefix(line, "\ufeff")
		// An absolute path, a URL, or relative to the playlist.
		if strings.Contains(line, "://") {
			// A remote entry. Not supported: the whole local-file path assumes a
			// container ffmpeg and mpv can open directly, and silently dropping it
			// would make a playlist look shorter than it is. Skipped here, counted
			// by the caller's report.
			continue
		}
		full := line
		if !filepath.IsAbs(full) {
			full = filepath.Join(base, full)
		}
		full = filepath.Clean(full)

		if isPlaylistExt(full) {
			if depth >= maxPlaylistDepth {
				continue
			}
			nested, nerr := readPlaylist(full, depth+1)
			if nerr != nil {
				continue
			}
			for _, p := range nested {
				add(p)
			}
			continue
		}
		if !mediaExts[strings.ToLower(filepath.Ext(full))] {
			continue
		}
		if st, serr := os.Stat(full); serr != nil || st.IsDir() {
			continue
		}
		add(full)
	}
	return out, nil
}

// tracksFromPaths builds the queue for explicit file arguments, in order.
//
// Duration probing is deliberately off. A command line is explicit — the user
// listed the tracks they want — so paying one ffprobe per file before the first
// frame is a cost with no visible benefit, and the resolver probes the track that
// actually plays anyway (resolveAudioPair / resolveMedia both call probeMedia on
// the local path). The queue is the same shape whatever filled it, which is the
// point of library.go's "this is a source, not a special case".
func tracksFromPaths(paths []string) []Track {
	tracks := make([]Track, 0, len(paths))
	for _, p := range paths {
		name := filepath.Base(p)
		dir := filepath.Base(filepath.Dir(p))
		album := cleanTitle(dir)
		key := mediaKey{Title: stripTags(stripExt(name))}
		// A leading number in the name is a position, not an episode: "03
		// prelude.mp3" is the third track of an album and there is no series.
		// Parsing it keeps the natural order honest for a numbered album, and
		// sortLibrary leaves unnumbered files in filename order anyway.
		if loc := lastBareNumber(name); loc != nil {
			if n := atoi(name[loc[4]:loc[5]]); n > 0 {
				key.Episode = n
			}
		}
		if key.Title == "" {
			key.Title = stripTags(stripExt(name))
		}
		// The album goes in Series so the browse list's channel column and the
		// sort have something to group on, but it is kept OUT of the title. A
		// queue the user typed is not browsing a library: "songs - celestial"
		// is the filename plus the folder it was already in, and the folder name
		// is the least interesting thing about the track.
		tracks = append(tracks, Track{
			Scraper:   "file",
			ID:        p,
			URL:       p,
			LocalPath: p,
			Title:     displayTitle(name, key),
			Series:    album,
			Season:    key.Season,
			Episode:   key.Episode,
		})
	}
	// Only sort when every track carries a number, which means the user handed
	// over a numbered set and expects it played in that order. A mixed or
	// unnumbered list keeps the order they typed, because that is the only
	// ordering they expressed.
	if allNumbered(tracks) {
		sortLibrary(tracks)
	}
	return tracks
}

func allNumbered(ts []Track) bool {
	if len(ts) == 0 {
		return false
	}
	for _, t := range ts {
		if t.Episode == 0 {
			return false
		}
	}
	return true
}
