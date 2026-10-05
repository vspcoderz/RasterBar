package main

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// library.go turns a directory of files into a []Track, so a local collection
// browses, queues and plays through the exact same TUI as a search result.
//
// This is a source, not a special case. Nothing downstream cares where a Track
// came from except the resolver, which asks track.IsLocal(): playQueue, the
// browse list, the HUD and the A/V corrector are all untouched by it.

// mediaExts are the containers ffmpeg reads directly. Anything else in the tree
// is skipped silently — a media folder also holds artwork, .nfo sidecars,
// subtitles and checksum files, and warning about each one turns a library
// listing into noise.
//
// The audio containers are here because music mode needs them. A music folder is
// .mp3 and .flac, not .mkv, and a library that could only see video containers
// would have nothing to show the spectrum.
//
// Ordering is unaffected: libraryTrack still requires an episode marker, so
// "17 - Artist - Song.mp3" lands in position 17 while a bare "Song.mp3" is skipped
// rather than placed arbitrarily. What order a music library should use instead is
// not decided here.
var mediaExts = map[string]bool{
	".mp4": true, ".m4v": true, ".mkv": true, ".webm": true,
	".avi": true, ".mov": true, ".mpg": true, ".mpeg": true, ".wmv": true,
	".flv": true, ".ts": true, ".m2ts": true, ".ogv": true,
	// Audio-only containers. `-vn` and the level tap both read these fine.
	".mp3": true, ".m4a": true, ".flac": true, ".opus": true,
	".ogg": true, ".oga": true, ".wav": true, ".aac": true, ".wma": true,
}

var (
	// "Show Name S01E02", "show.s01.e02", "Show - 01x02"
	//
	// Group 1 is the whole "S01E02" token so the split consumes the literal S:
	// cutting at the season digits alone leaves "Cowboy Bebop S". Group 0 is the
	// delimiter in front and stays on the series side.
	//
	// No \b before the S: underscore is a word character to the regexp engine, so
	// "Show_S01E02" has no boundary there and \b would never fire.
	reSxxExx = regexp.MustCompile(`(?i)(?:^|[^0-9A-Za-z])(S(\d{1,3})[\s._-]?E(\d{1,4})\b)`)
	// "01x02" with no S — the form most anime rips use.
	reXxx = regexp.MustCompile(`(?i)(?:^|[^\d])(\d{1,2})x(\d{2,3})(?:[^\d]|$)`)
	// "Episode 7", "Ep. 7", "E07 - Title"
	reEpisodeWord = regexp.MustCompile(`(?i)\b(?:episode|ep\.?|e)\s*(\d{1,4})\b`)
	// "- 07 -", "_07_", "[07]" — the absolute numbering anime releases use.
	//
	// The delimiter classes are spelled out rather than written as [^\w.] because
	// underscore IS a word character to Go's regexp engine, and "_07_" is the most
	// common anime separator there is. A run of four digits cannot match, which is
	// what keeps the year in "Show_2019" out of the episode slot.
	reBareNumber = regexp.MustCompile(`(^|[^0-9A-Za-z.])(\d{1,3})(?:v\d)?(?:[^0-9]|$)`)
	// "Show Name (2019)", "Show Name [2019]"
	reYearParen = regexp.MustCompile(`[\(\[](?:19|20)\d{2}[\)\]]`)
	// Bracketed release tags: "[1080p]", "(BD)", "[SubsPlease]", "[Group]".
	reReleaseTag = regexp.MustCompile(`(?i)[\(\[](?:[0-9]{3,4}x?[0-9]{0,4}p?|hd|sd|bd|bdrip|dvdrip|web-?dl|webrip|web|raw|remux|dual-?audio|multi-?subs?|subs?|dub|subbed|10-?bit|8-?bit|hi10p?)[^)\]]*[\)\]]`)
	// The same tags appearing bare: "x264", "HEVC".
	reLooseTag = regexp.MustCompile(`(?i)\b(?:1080p|720p|480p|x264|x265|hevc|h\.?264|h\.?265|aac|flac|opus)\b`)
	// "Season 2", "Season 02" anywhere in a directory name.
	reSeasonDir = regexp.MustCompile(`(?i)\b(?:season|seasons|s)\s*(\d{1,3})\b`)
	// A directory that is only a season marker: "S02", "Season 02".
	reSeasonDirTight = regexp.MustCompile(`(?i)^(?:season|s)[\s._-]*(\d{1,3})$`)
)

// probeConcurrency bounds the ffprobe fan-out. One file per worker, capped well
// below the core count: each probe is a process spawn waiting on I/O, not a
// CPU-bound decode, so 8 concurrent reads beat 64.
const probeConcurrency = 8

// mediaKey is the parsed identity of one file.
type mediaKey struct {
	Series  string
	Season  int
	Episode int
	// Title is the episode's own name, the part after the number in
	// "Show S02E10 - The Test". Empty for a bare numbered release.
	Title string
}

// ScanLibrary walks root and returns every playable file as a Track, ordered
// series → season → episode.
//
// The sort is the point. os.ReadDir already returns entries in filename order,
// but that is a lexical order: "Show - 1" then "Show - 10" is a playlist nobody
// can watch, so ordering is done on parsed numbers instead.
func ScanLibrary(root string, probe bool) ([]Track, error) {
	info, err := os.Stat(root)
	if err != nil {
		return nil, fmt.Errorf("library %q: %w", root, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("library %q is not a directory", root)
	}

	var tracks []Track
	// A WalkDir over an unbounded tree on a network share can take a while and
	// print nothing, so this path returns rather than looking hung.
	err = filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory should not abort the scan.
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() || !mediaExts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		tr, ok := libraryTrack(path, root)
		if !ok {
			return nil
		}
		tracks = append(tracks, tr)
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk %q: %w", root, err)
	}

	sortLibrary(tracks)
	if probe {
		probeDurations(tracks)
	}
	return tracks, nil
}

// libraryTrack builds a Track from one path.
//
// It returns ok=false for a video whose name carries no episode marker at all.
// Such a file has no meaningful position in a playlist, and inventing one from
// readdir order would put it somewhere arbitrary rather than honestly nowhere.
func libraryTrack(path, root string) (Track, bool) {
	// The path relative to root is the identity. An absolute path in the HUD
	// eats the whole width, and the leaf is what distinguishes one episode from
	// the next anyway.
	rel, err := filepath.Rel(root, path)
	if err != nil {
		rel = filepath.Base(path)
	}
	rel = filepath.ToSlash(rel)

	name := filepath.Base(rel)
	key, ok := classifyMedia(name)
	if !ok {
		return Track{}, false
	}

	dir := filepath.Base(filepath.Dir(rel))
	if key.Season == 0 {
		if s := seasonFromDir(dir); s > 0 {
			key.Season = s
		}
	}
	if key.Series == "" {
		key.Series = cleanTitle(dir)
	}

	dur := probeDuration(path)
	tr := Track{
		Scraper:   "library",
		ID:        rel,
		URL:       path,
		LocalPath: path,
		Title:     displayTitle(name, key),
		Series:    key.Series,
		Season:    key.Season,
		Episode:   key.Episode,
	}
	if dur > 0 {
		d := formatDuration(dur)
		tr.Duration = &d
	}
	return tr, true
}

// classifyMedia extracts series/season/episode from a filename, accepting the
// most explicit pattern that matches.
//
// The split point is the important part: series is the text BEFORE the episode
// number and the episode title is the text AFTER it. Substituting the number out
// of the middle cannot tell "Cowboy Bebop - 07 - Session 3" from a series called
// "Cowboy Bebop Session 3", and it leaves the year in "Show (2019) - 03" glued
// to the name. Splitting instead gives "Cowboy Bebop" + "Session 3".
//
// Ordering carries more weight than any individual pattern. "Show Name - 07" and
// "Show Name S01E07" both contain a 07 but only one has a season, and a show
// released as per-season absolute numbering is indistinguishable from a
// seasonless one except by the directory above it. Trying SxxExx first and bare
// number last is what stops a title year from being filed as an episode.
func classifyMedia(name string) (mediaKey, bool) {
	base := stripExt(filepath.Base(name))

	// Splits run on the number groups rather than the whole match: these
	// patterns capture their leading delimiter as group 0, and that delimiter
	// belongs to the series side of the cut.
	if loc := reSxxExx.FindStringSubmatchIndex(base); loc != nil {
		return splitMedia(base, loc[2], loc[3],
			atoi(base[loc[4]:loc[5]]), atoi(base[loc[6]:loc[7]])), true
	}
	if loc := reXxx.FindStringSubmatchIndex(base); loc != nil {
		return splitMedia(base, loc[2], loc[5],
			atoi(base[loc[2]:loc[3]]), atoi(base[loc[4]:loc[5]])), true
	}
	// "Episode 7" is explicit about being an episode even with no season, so it
	// outranks the bare-number rule. The whole phrase is the marker here.
	if loc := reEpisodeWord.FindStringSubmatchIndex(base); loc != nil {
		if e := atoi(base[loc[2]:loc[3]]); e > 0 {
			return splitMedia(base, loc[0], loc[1], 0, e), true
		}
	}

	// Bare number: last match wins, not first. In "Show - 07 - Title 1080p" the
	// trailing 1080p is not an episode, and in "Show 07 v2" the version is the
	// thing that should lose.
	if loc := lastBareNumber(base); loc != nil {
		// Group 2 is the number; group 1 is the delimiter before it.
		if e := atoi(base[loc[4]:loc[5]]); e > 0 {
			return splitMedia(base, loc[4], loc[5], 0, e), true
		}
	}
	return mediaKey{}, false
}

// splitMedia builds a mediaKey by cutting base around the [start,end) span of
// the episode marker.
func splitMedia(base string, start, end int, season, episode int) mediaKey {
	series := stripTags(base[:start])
	title := stripTags(base[end:])
	// A title that is only tags ("[SubsPlease]") cleans to empty, which is the
	// wanted outcome: no episode name, not a placeholder one.
	return mediaKey{Series: series, Season: season, Episode: episode, Title: title}
}

// lastBareNumber returns the submatch indices of the last plausible episode
// number in a name.
//
// A year cannot match: reBareNumber accepts one to three digits, and a
// four-digit year either overflows the count or fails the trailing-delimiter
// requirement on the fourth digit. That is what keeps "Show (2019)" from
// becoming episode 2019.
func lastBareNumber(name string) []int {
	ms := reBareNumber.FindAllStringSubmatchIndex(name, -1)
	for i := len(ms) - 1; i >= 0; i-- {
		// ms[i] is [fullStart,fullEnd, g1Start,g1End, ...]; group 2 is the number.
		if atoi(name[ms[i][4]:ms[i][5]]) > 0 {
			return ms[i]
		}
	}
	return nil
}

// stripTags removes release-group, quality and year noise from one half of a
// filename. A year goes too: "(2019)" is metadata, and "Some Show (2019)" in a
// browse list is just a longer way of writing "Some Show".
func stripTags(s string) string {
	s = reReleaseTag.ReplaceAllString(s, " ")
	s = reYearParen.ReplaceAllString(s, " ")
	s = reLooseTag.ReplaceAllString(s, " ")
	return cleanTitle(s)
}

// atoi is strconv.Atoi with the error dropped, for a substring already known to
// be digits by the pattern that matched it. Zero is the failure value, and every
// caller treats zero as "no season" or "no episode".
func atoi(s string) int {
	n, _ := strconv.Atoi(s)
	return n
}

// stripExt removes the container suffix. Called on names that came from the
// walk, so the extension is known to be one of ours.
func stripExt(name string) string {
	if ext := filepath.Ext(name); mediaExts[strings.ToLower(ext)] {
		return name[:len(name)-len(ext)]
	}
	return name
}

// seasonFromDir reads a season number off a directory name: "Season 2",
// "S02", "Show Name Season 02".
func seasonFromDir(dir string) int {
	if m := reSeasonDirTight.FindStringSubmatch(dir); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	if m := reSeasonDir.FindStringSubmatch(dir); m != nil {
		n, _ := strconv.Atoi(m[1])
		return n
	}
	return 0
}

// cleanTitle collapses separators and trims what tag stripping left behind.
func cleanTitle(s string) string {
	s = strings.NewReplacer("_", " ", ".", " ", "-", " ").Replace(s)
	var b strings.Builder
	var space bool
	for _, r := range s {
		if r == ' ' || r == '\t' {
			if !space {
				b.WriteByte(' ')
			}
			space = true
			continue
		}
		b.WriteRune(r)
		space = false
	}
	// Brackets survive because the cut lands just after one: the series side of
	// "Show [07]" is "Show [". Trimming the edges is what makes that "Show".
	return strings.Trim(b.String(), " \t[](){}")
}

// displayTitle is what the browse list and HUD show. The episode name is
// appended when the release had one, which is the difference between a list of
// "S01E07 Cowboy Bebop" rows and a list you can actually scan for the one you
// want.
func displayTitle(name string, key mediaKey) string {
	var prefix string
	if key.Season > 0 {
		prefix = fmt.Sprintf("S%02dE%02d", key.Season, key.Episode)
	} else {
		prefix = fmt.Sprintf("E%02d", key.Episode)
	}
	switch {
	case key.Series == "":
		return prefix
	case key.Title == "":
		return prefix + "  " + key.Series
	default:
		return prefix + "  " + key.Series + " - " + key.Title
	}
}

// sortLibrary orders tracks the way a watcher expects to press next.
//
// Season and episode numerically, series alphabetically, then full path as the
// final tiebreak: two copies of one episode at different qualities parse to
// identical numbers, and a fixed order beats filesystem readdir order.
func sortLibrary(ts []Track) {
	sort.SliceStable(ts, func(i, j int) bool {
		a, b := ts[i], ts[j]
		if a.Series != b.Series {
			return a.Series < b.Series
		}
		if a.Season != b.Season {
			return a.Season < b.Season
		}
		if a.Episode != b.Episode {
			return a.Episode < b.Episode
		}
		return a.LocalPath < b.LocalPath
	})
}

// probeDurations fills in every missing duration with a bounded ffprobe fan-out.
//
// The browse list shows a length per row and the seek bar needs a total, so a
// library without durations looks broken. Probing is one subprocess per file,
// which is why it is capped and why the flag can skip it: on a 2000-file
// library it is minutes of waiting for a column of numbers that reads "--:--"
// perfectly legibly.
func probeDurations(ts []Track) {
	var wg sync.WaitGroup
	sem := make(chan struct{}, probeConcurrency)

	for i := range ts {
		if ts[i].Duration != nil {
			continue
		}
		wg.Add(1)
		go func(tr *Track) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if d := probeDuration(tr.LocalPath); d > 0 {
				s := formatDuration(d)
				tr.Duration = &s
			}
		}(&ts[i])
	}
	wg.Wait()
}

// ffprobeOutput is the subset of `ffprobe -show_format -show_chapters` we read.
//
// Both are asked for in one call on purpose. Probing is a subprocess per file and
// the library scan caps it at 8 concurrent, so a second call for chapters would
// double the wall clock of a 2000-file scan to obtain data the first call can
// return for free.
//
// The chapter fields are strings and the title is nested under tags because that
// is what ffprobe actually emits. Getting this wrong is not a compile error: the
// unmarshal fails, and the caller sees a zero duration — which looks exactly like
// an unknown one and silently blanks the length column. See TestFfprobeOutputShape
// for the fixture that pins it.
type ffprobeOutput struct {
	Format struct {
		Duration string `json:"duration"`
	} `json:"format"`
	Streams []struct {
		CodecType string `json:"codec_type"`
	} `json:"streams"`
	Chapters []struct {
		StartTime string `json:"start_time"`
		EndTime   string `json:"end_time"`
		Tags      struct {
			Title string `json:"title"`
		} `json:"tags"`
	} `json:"chapters"`
}

// probeMedia returns a local file's length in seconds and its chapters.
//
// Probing a local path is safe in a way probing a resolved googlevideo URL is
// not: a local file has no single-use grant to burn, so the usual reason this
// project avoids ffprobe (see resolveMedia) does not apply here.
// probeMedia returns a file's length in seconds, its chapters, and whether it
// has an audio stream.
//
// The third answer is not cosmetic. A video-only file handed to
// `mpv --no-video` exits instantly with nothing played, and the render loop reads
// that exit as "the track finished" — so a silent video stopped about two seconds
// in and looked like a broken player rather than a file with no sound.
func probeMedia(path string) (dur int, chapters []Chapter, hasAudio bool) {
	// -show_streams is not optional and its absence was invisible for a long time.
	//
	// Without it ffprobe returns format and chapters but no `streams` key, so the
	// loop below never ran, hasAudio was always false, and *every* local file was
	// classified silent. In video mode that was survivable and hard to notice: the
	// nil audio channel blocks forever by design and the track ended on the
	// grace timer after the video pipe closed instead of on mpv exiting. In music
	// mode it is fatal, because refusing a source with no audio stream is exactly
	// the check this flag was supposed to inform -- and it refused the audio-only
	// file that music mode exists to play.
	out, err := exec.Command("ffprobe",
		"-v", "quiet",
		"-print_format", "json",
		"-show_format",
		"-show_streams",
		"-show_chapters",
		path).Output()
	if err != nil {
		return 0, nil, false
	}
	var p ffprobeOutput
	if err := json.Unmarshal(out, &p); err != nil {
		return 0, nil, false
	}
	for _, st := range p.Streams {
		if st.CodecType == "audio" {
			hasAudio = true
			break
		}
	}
	chaps := make([]Chapter, 0, len(p.Chapters))
	for _, c := range p.Chapters {
		start, err1 := strconv.ParseFloat(strings.TrimSpace(c.StartTime), 64)
		end, err2 := strconv.ParseFloat(strings.TrimSpace(c.EndTime), 64)
		// One unreadable entry drops that chapter and nothing else. Bailing out of
		// the whole list would cost chapter navigation over a single bad row.
		if err1 != nil || err2 != nil || end < start {
			continue
		}
		chaps = append(chaps, Chapter{Start: start, End: end, Title: c.Tags.Title})
	}
	if len(chaps) == 0 {
		chaps = nil
	}
	// ffprobe reports fractional seconds ("1437.218000").
	f, err := strconv.ParseFloat(strings.TrimSpace(p.Format.Duration), 64)
	if err != nil || f <= 0 {
		return 0, chaps, hasAudio
	}
	return int(f), chaps, hasAudio
}

// probeDuration returns a file's length in seconds, or 0 if unreadable.
func probeDuration(path string) int {
	d, _, _ := probeMedia(path)
	return d
}
