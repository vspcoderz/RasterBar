// Command rasterbar searches YouTube and plays results in a terminal UI:
// audio-first with a live FFT spectrum, synced ASCII video on toggle.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/vspcoderz/rasterbar/internal/term"
)

const usage = `rasterbar - terminal YouTube player: ASCII video or a music visualizer

usage:
  rasterbar [options] <query>

modes:
  -M, --music       music mode: the visualizer replaces the video (default)
  -a, --ascii       video mode: ASCII/colour video (the strip is off; press s)

options:
  -m, --mute         play audio muted (visualizer still animates)
      --viz NAME     visualizer style: bars, scope, mirror, waterfall,
                     radial, particles (default: bars)
      --palette NAME colour scheme: spectrum, height, ocean, ember, graphite,
                     ink, ice, magma, viridis, mono (default: spectrum)
  -c, --color        force colour (default: auto-detected from the terminal)
      --mono         force plain monochrome ASCII, no colour
      --glyph MODE   cell layout: auto (probe the terminal), half (2 pixels
                     per cell, needs narrow U+2580), cell (1 pixel per cell,
                     always aligns correctly)
  -q, --quality H    cap source resolution: 240, 360, 480, 720, 1080
                     (default: chosen from your terminal size)
      --aspect R     cell height/width ratio, default 2.0. Raise it if the
                     image looks squashed, lower it if it looks stretched
      --cols N       force ASCII grid width (default: terminal width)
      --rows N       force ASCII grid height (default: from terminal height)
  -h, --help         this help

playing files (as well as searching):
  rasterbar song.mp3 b.opus        these files, in this order
  rasterbar ~/Music/*.flac         a glob, quoted or not
  rasterbar roadtrip.m3u           an m3u/m3u8 playlist, entries relative to it

  An argument becomes a file when it exists, contains a wildcard, or names a
  playlist. Anything else is a YouTube search, so "lofi hip hop radio" still
  searches. A file with no video stream plays as a visualiser whichever mode you
  asked for, because there is no picture to show.

library mode (browse a directory instead of searching):
  -l, --library DIR  scan DIR for media files
      --no-probe     skip the ffprobe pass (instant scan, "--:--" durations)
      --play         start the first track without the browse list

  Filenames are parsed for series, season and episode, so "Show S01E02.mkv",
  "Show - 01x02.mkv", "Show - 07 [1080p].mkv" and "Show Episode 7.mkv" all
  land in a watchable order. Files with no episode marker are still included,
  ordered by natural filename order -- so "track 2" precedes "track 10", which
  is the whole reason a music folder works here at all. Audio containers count:
  .mp3 .m4a .flac .opus .ogg .oga .wav .aac .wma and the video ones.

  requires: ffmpeg, mpv  (yt-dlp and ytfzf are needed only for search)

The grid and the source resolution both follow your terminal size automatically:
a small window requests a small video, a large one a larger video, so you never
pay decode CPU for pixels you cannot see.

keys (browse):
  j / k / arrows   move
  g / G            top / bottom
  1-9              jump to row
  enter            play music mode (visualizer)
  a                play video mode
  q                quit

keys (playback, both modes):
  space            pause / resume
  left / right     seek -10s / +10s
  , / .            seek -1s / +1s
  < / >            seek -60s / +60s
  [ / ]            previous / next chapter
  :                jump to a timestamp
  n / p            next / previous track
  + / -            volume up / down
  q / ctrl-c       quit

  s                toggle the spectrum strip (video mode: off by default.
                   In music mode the strip is part of the visualiser, and s
                   gives its row back to the grid)

keys (playback, music mode only):
  v / V            next / previous visualizer style
  c                next colour palette
  1 - 0            pick a palette directly: 1 spectrum, 2 height, 3 ocean,
                   4 ember, 5 graphite, 6 ink, 7 ice, 8 magma, 9 viridis,
                   0 mono
  W                split view: show video beside the visualiser (off by
                   default; needs a source with a video stream)
  a / d            move the video pane to the left / right
  T                video pane: still thumbnail / live video
  { / }            move the split divider

  The jump prompt takes 1:30, 1:02:03, a bare 90 (seconds), 90s / 2m / 1h2m3s,
  +30 / -1:30 (relative to now), and 50% (of the track). It previews where the
  jump will land before you commit to it. enter jumps, esc cancels.

  The style and palette you pick carry over to the next track, so v is pressed
  once per session rather than once per song.

  The split view (W) is music mode only, and off until asked for: it starts a
  second ffmpeg and gives the picture half the grid. T switches the pane between
  live video and the still thumbnail from yt-dlp, which needs no video stream at
  all. { and } move the divider, which stops at a quarter each way so neither
  pane can be squeezed out.

  When a track finishes the next result starts on its own; playback stops at the
  end of the list.

tuned for low-end machines: video mode requests a 360p h264 source because
ffmpeg decode cost scales with resolution, and an 80x22 character grid cannot
show the difference between 360p and 1080p. Video is letterboxed rather than
stretched, so the picture keeps its own shape whatever shape your window is. The
spectrum is computed in-process with a hand-rolled FFT, so there are no
third-party Go modules. The expensive styles (radial, particles) are capped
rather than disabled: particles never exceed 400, and on a large grid radial
draws at a fraction of the frame rate rather than at none.

requires: yt-dlp, ffmpeg, mpv  (ytfzf optional, used as the primary scraper)
`

type options struct {
	// args holds the positional arguments in the order given. Resolved into
	// either a file queue or a search query in main.
	args []string
	// query is the joined positional text, set only once main has established
	// that none of the arguments names a file.
	query    string
	mute     bool
	ascii    bool
	music    bool
	quality  Quality
	aspect   float64
	cols     int // explicit override, 0 = auto
	rows     int
	color    ColorMode // colorAuto unless a flag says otherwise
	glyph    GlyphMode // GlyphAuto unless a flag says otherwise
	library  string    // directory to browse instead of searching
	noProbe  bool      // skip ffprobe during a library scan
	playOnly bool      // play immediately, skip the browse list

	// viz and palette name a starting style, resolved here rather than at first
	// use so a typo is an argument error instead of a silently ignored flag.
	viz     string
	palette string

	// vizPrefs caches the resolved style/palette across the queue. Built by
	// prefs() on demand and shared, so the `v` and `c` keys mutate one value.
	vizPrefs *vizPrefs
}

// prefs returns the shared visualizer preferences, resolving the named style and
// palette the first time.
//
// Resolution is deferred rather than done in parseArgs because a style needs the
// registry, and the registry is a package-level table -- but an *invalid* name is
// rejected in parseArgs, so this cannot silently fall back.
func (o *options) prefs() *vizPrefs {
	if o.vizPrefs != nil {
		return o.vizPrefs
	}
	p := newVizPrefs()
	for i, mk := range vizRegistry {
		if o.viz != "" && strings.EqualFold(mk().Name(), o.viz) {
			p.style = i
		}
	}
	if i := paletteIndexByName(o.palette); i >= 0 {
		p.palette = i
	}
	o.vizPrefs = p
	return p
}

// colorAuto requests terminal capability detection.
const colorAuto ColorMode = -1

// parseArgs turns argv into options.
//
// The positional arguments land in o.args and are NOT joined into a query. That
// split is the whole reason `rasterbar song.mp3 b.opus` can be a queue: deciding
// between "three search terms" and "two files" needs the filesystem, which this
// function has no business touching. main joins them into a query only when none
// of them names a file.
func parseArgs(args []string) (options, error) {
	o := options{color: colorAuto}
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-h", "--help":
			return o, fmt.Errorf("help")
		case "-m", "--mute":
			o.mute = true
		case "--glyph":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--glyph needs a value: auto, half, cell")
			}
			i++
			switch args[i] {
			case "auto":
				o.glyph = GlyphAuto
			case "half", "halfblock":
				o.glyph = GlyphHalf
			case "cell", "single":
				o.glyph = GlyphCell
			default:
				return o, fmt.Errorf("--glyph must be auto, half or cell (got %s)", args[i])
			}
		case "-c", "--color", "--colour":
			o.color = ColorTrue
		case "--mono", "--no-color", "--no-colour":
			o.color = ColorNone
		case "-a", "--ascii":
			o.ascii = true
		case "-M", "--music":
			o.music = true
		case "--viz", "--visualizer":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--viz needs a value: %s", strings.Join(VizNames(), ", "))
			}
			i++
			if _, ok := VizByName(args[i]); !ok {
				return o, fmt.Errorf("--viz must be one of %s (got %s)",
					strings.Join(VizNames(), ", "), args[i])
			}
			o.viz = args[i]
		case "--palette":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--palette needs a value")
			}
			i++
			if paletteIndexByName(args[i]) < 0 {
				names := make([]string, 0, len(palettes))
				for _, p := range palettes {
					names = append(names, p.name)
				}
				return o, fmt.Errorf("--palette must be one of %s (got %s)",
					strings.Join(names, ", "), args[i])
			}
			o.palette = args[i]
		case "-q", "--quality":
			if i+1 >= len(args) {
				return o, fmt.Errorf("-q needs a value (240, 360, 480, 720, 1080)")
			}
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil {
				return o, fmt.Errorf("bad -q value: %s", args[i])
			}
			switch v {
			case 240, 360, 480, 720, 1080:
				o.quality = Quality(v)
			default:
				return o, fmt.Errorf("-q must be one of 240, 360, 480, 720, 1080 (got %d)", v)
			}
		case "--aspect":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--aspect needs a value (e.g. 2.0)")
			}
			i++
			f, err := strconv.ParseFloat(args[i], 64)
			if err != nil || f <= 0 || f > 10 {
				return o, fmt.Errorf("bad --aspect value: %s", args[i])
			}
			o.aspect = f
		case "--cols":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--cols needs a value")
			}
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil || v < minCols {
				return o, fmt.Errorf("bad --cols value: %s", args[i])
			}
			o.cols = v
		case "--rows":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--rows needs a value")
			}
			i++
			v, err := strconv.Atoi(args[i])
			if err != nil || v < 4 {
				return o, fmt.Errorf("bad --rows value: %s", args[i])
			}
			o.rows = v
		case "--library", "-l":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--library needs a directory")
			}
			i++
			o.library = args[i]
		case "--no-probe":
			o.noProbe = true
		case "--play", "--now":
			o.playOnly = true
		default:
			if strings.HasPrefix(a, "-") {
				return o, fmt.Errorf("unknown flag: %s", a)
			}
			// Positional arguments are collected, not joined. Whether they are a
			// search query or a list of files is decided in main, because it needs
			// the filesystem: joining them here would turn `song.mp3 b.opus` into
			// the single string "song.mp3 b.opus" and then into a YouTube search
			// for that string, which is what used to happen.
			o.args = append(o.args, a)
		}
	}
	return o, nil
}

// expandFileArgs turns positional arguments that name files into a queue of
// paths, in order.
//
// The decision is "does at least one argument look like a file", not "does every
// argument" — `rasterbar b.opus lofi hip hop` is a mixed intent, and taking the
// file and searching for the rest would be two players. When any argument looks
// like a file, all of them are treated as paths and the ones that are not get an
// error naming them, which is honest. When none does, it is a search.
func expandFileArgs(args []string) ([]string, error) {
	any := false
	for _, a := range args {
		if looksLikeFile(a) {
			any = true
			break
		}
	}
	if !any {
		return nil, nil
	}

	var out []string
	for _, a := range args {
		paths, isFile, err := loadPathArg(a, 0)
		if err != nil {
			return nil, err
		}
		if !isFile {
			return nil, fmt.Errorf("%q is not a file, a glob or a playlist", a)
		}
		out = append(out, paths...)
	}
	return out, nil
}

func main() {
	opts, err := parseArgs(os.Args[1:])
	if err != nil {
		if err.Error() == "help" {
			fmt.Print(usage)
			return
		}
		fmt.Fprintf(os.Stderr, "%v\n\n%s", err, usage)
		os.Exit(2)
	}

	// Decide what the positional arguments are before anything else needs to know:
	// a list of files, or text to search YouTube for.
	//
	// -l wins outright when both are given, because a directory scan has its own
	// walk and its own episode parser and a file list would only duplicate them.
	var filePaths []string
	if opts.library == "" && len(opts.args) > 0 {
		filePaths, err = expandFileArgs(opts.args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		if len(filePaths) == 0 {
			// Every argument looked like a path and none of them resolved to a
			// playable file. Saying so beats falling back to a YouTube search for
			// the user's own filename, which is what used to happen.
			fmt.Fprintf(os.Stderr, "no playable files in %s\n", strings.Join(opts.args, " "))
			os.Exit(1)
		}
	} else {
		opts.query = strings.Join(opts.args, " ")
	}

	if len(filePaths) == 0 && opts.library == "" && opts.query == "" {
		fmt.Print(usage)
		return
	}

	// Resolve colour mode: explicit flag wins, otherwise ask the terminal.
	colorMode := opts.color
	if colorMode == colorAuto {
		colorMode = detectColor(envSlice())
	}

	// Three sources, one Track shape: a network search, a directory scan, or a
	// list of files the user named. The library branch deliberately skips Search
	// entirely rather than both filling a slice, because there is no query to hand
	// a scraper; the same goes for explicit files.
	var tracks []Track
	label := opts.query
	switch {
	case opts.library != "":
		label = opts.library
		fmt.Fprintf(os.Stderr, "scanning library: %s\n", opts.library)
		tracks, err = ScanLibrary(opts.library, !opts.noProbe)
		if err != nil {
			fmt.Fprintf(os.Stderr, "library scan failed: %v\n", err)
			os.Exit(1)
		}
		if len(tracks) == 0 {
			fmt.Fprintf(os.Stderr, "no playable files under %s\n", opts.library)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "%d tracks\n", len(tracks))

	case len(filePaths) > 0:
		tracks = tracksFromPaths(filePaths)
		label = fmt.Sprintf("%d files", len(tracks))
		fmt.Fprintf(os.Stderr, "%d files: %s\n", len(tracks), strings.Join(opts.args, " "))

	default:
		fmt.Fprintf(os.Stderr, "searching: %s\n", opts.query)
		tracks, err = Search(opts.query)
		if err != nil {
			fmt.Fprintf(os.Stderr, "search failed: %v\n", err)
			os.Exit(1)
		}
	}

	// A library scan is already an ordered queue, so --play skips the browse
	// list: picking one episode by hand is the only thing between the command
	// and playback, and for a known show you want the first episode.
	idx := 0
	action := ActionPlay
	if !opts.playOnly {
		tui := NewTUI(os.Stdin, os.Stdout, label, tracks)
		var sel int
		action, sel = tui.Run()
		if action == ActionQuit || sel < 0 {
			return
		}
		idx = sel
	}

	// --play skips the browse list, so there is no keypress to request video mode.
	// Default it on for a library: asking for a library episode and getting a
	// spectrum alone would not be what anyone wanted.
	//
	// NOT for an explicit file list. A file named on the command line is a
	// deliberate act, and the common case is a song: `rasterbar track.mp3` asking
	// for ASCII video of a file with no video stream used to kill playback on the
	// first track and take the rest of the queue with it. newTrackSession also
	// falls back per-track on a probe, so this is belt and braces — but the
	// default should be right for the folder it is applied to, not repaired later.
	wantVideo := opts.ascii || (opts.playOnly && opts.library != "")
	if action == ActionASCII {
		wantVideo = true
	}
	if opts.music {
		// An explicit --music overrides the browse list's choice, including the
		// library default above. A flag that silently lost to a heuristic would be
		// worse than the heuristic not existing.
		wantVideo = false
	}

	// One queue, two modes. Music mode used to be runVisualAudio: a separate loop
	// with one key, no HUD, no duration, no seek and no queue advance. Sharing
	// playQueue is what makes it a player rather than a demo, and it is why the
	// prompt, the resize path and the stall handler work there for free.
	playQueue(opts, colorMode, tracks, idx, !wantVideo)
}

// playQueue plays the selected result and then keeps going through the rest of
// the search results.
//
// This is what makes it a queue rather than a single track: a search returns ten
// results and previously one was played and the process exited, throwing the
// other nine away. Stopping at the end of the list is deliberate — wrapping back
// to the top of a search result set is disorienting, and nothing in a search
// result is ordered as a playlist.
//
// Navigation keys (n/p) return an outcome rather than changing the queue here, so
// the loop stays the single owner of the index.
func playQueue(opts options, mode ColorMode, tracks []Track, index int, music bool) {
	po := playOpts{
		in:        os.Stdin,
		out:       os.Stdout,
		mute:      opts.mute,
		quality:   opts.quality,
		aspect:    opts.aspect,
		cols:      opts.cols,
		rows:      opts.rows,
		mode:      mode,
		glyphPref: opts.glyph,
		music:     music,
		// One prefs value for the whole queue, shared by every track: pressing `v`
		// once should not have to be repeated per song.
		prefs: opts.prefs(),
	}
	if po.aspect <= 0 {
		po.aspect = defaultAspect
	}

	// One key reader for the whole queue. Starting one per track left the
	// previous goroutine blocked in Read after a queue advance, and the two then
	// raced for stdin — which showed up as the quit key doing nothing.
	// Resolve the glyph layout before the key reader starts.
	//
	// The probe round-trips with the terminal: it prints the half-block glyph and
	// reads the Device Status Report back off the same fd the keys come from.
	// Starting readKeys first put two readers on one tty, and the report went to
	// whichever won — either a 700ms stall that silently downgraded the frame to
	// one pixel per cell, or a keystroke the user pressed during startup eaten by
	// the probe. Probing first makes the race impossible rather than unlikely.
	//
	// Raw mode has to be on for the reply to come back cleanly, and playTrack sets
	// it for the whole render loop, so it is turned on and off just for the probe.
	if po.glyphPref == GlyphAuto && po.in != nil && po.out != nil {
		if restore, err := term.MakeRawVT(po.in, 0, 1); err == nil {
			po.glyphPref = resolveGlyph(GlyphAuto, po.in, po.out)
			restore()
		}
	}

	keys := make(chan []byte, 16)
	go readKeys(po.in, keys)
	po.keys = keys

	for index >= 0 && index < len(tracks) {
		fmt.Fprintf(os.Stderr, "playing %d/%d: %s — %s\n",
			index+1, len(tracks), tracks[index].Title, tracks[index].ChannelText())

		outcome := playTrack(po, tracks, index)
		if debugQueue {
			fmt.Fprintf(os.Stderr, "queue: track %d/%d %q -> %v\n",
				index+1, len(tracks), tracks[index].Title, outcome)
		}
		switch outcome {
		case OutcomeQuit, OutcomeError:
			return
		case OutcomeEnded:
			// End of the queue.
			return
		case OutcomeNext:
			index++
		case OutcomePrev:
			index--
		default:
			return
		}
	}
}

// debugSync enables the A/V drift readout on stderr. Off by default because it
// writes to the same stream the renderer uses.
var debugSync = os.Getenv("VSPZ_YT_CLI_DEBUG_SYNC") != ""

// debugViz traces the spectrum values reaching the styles. Same reasoning as
// debugSync: off by default, because stderr is a stream the renderer uses.
var debugViz = os.Getenv("VSPZ_YT_CLI_DEBUG_VIZ") != ""

// debugQueue traces every queue transition. The only way to tell a track that
// genuinely ended from one whose mpv exited for some other reason, which look
// identical from the outside: both return an Outcome and both move the queue on.
var debugQueue = os.Getenv("VSPZ_YT_CLI_DEBUG_QUEUE") != ""
