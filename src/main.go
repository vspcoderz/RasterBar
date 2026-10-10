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

const usageTemplate = `rasterbar - terminal YouTube player: ASCII video or a music visualizer

usage:
  rasterbar <query>                    search YouTube
  rasterbar <file>...                  play files
  rasterbar <verb> [options] [args...] see verbs below

verbs:
  play <query|file...>   search YouTube, or play files — the default
  search <query>         force a search, even if an argument looks like a path
  list [DIR]             browse a directory (was -l DIR); defaults to "."
  viz NAME               %s
  palette NAME           spectrum height ocean ember graphite ink ice magma
                         viridis mono
  help                   this text
  version                print the version

  rasterbar "lofi hip hop radio"          a search
  rasterbar play "lofi hip hop radio"     the same, said out loud
  rasterbar play track.mp3 prelude.opus   files, in this order
  rasterbar play "~/Music/*.flac"          a glob, quoted or not
  rasterbar play roadtrip.m3u             an m3u/m3u8 playlist
  rasterbar list ~/Shows --play           browse a directory
  rasterbar play -a "tesseract"           video mode

  An argument becomes a file when it exists, contains a wildcard, or names a
  playlist. Anything else is a YouTube search. A file with no video stream plays
  as a visualiser whichever mode you asked for, because there is no picture.

modes:
  -M, --music       music mode: the visualizer replaces the video (default)
  -a, --ascii       video mode: ASCII/colour video (the strip is off; press s)

options:
  -m, --mute         play audio muted (visualizer still animates)
      --cookies FILE      a Netscape cookies.txt for yt-dlp
      --cookies-from-browser NAME
                          firefox, chrome, chromium, brave, edge, safari,
                          opera. YouTube bot-checks by IP, and a signed-in
                          browser is the only cure; see "yt-dlp and YouTube"
                          below.
      --viz NAME     visualizer style (same as the viz verb)
      --palette NAME colour scheme (same as the palette verb)
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
      --no-probe     skip the ffprobe pass on a directory scan
      --play         with list, start the first track without the browse list
  -h, --help         this text

  Every flag still works and every verb is a flag spelled out. The flags are
  short and several are muscle memory; the verbs are the discoverable front door.

  list orders a directory two ways at once: files with an episode marker sort
  series -> season -> episode, and files without one sort by natural filename
  order, so "track 2" precedes "track 10". Audio containers count too:
  .mp3 .m4a .flac .opus .ogg .oga .wav .aac .wma as well as the video ones.

  yt-dlp and YouTube: YouTube decides by IP whether you are a person. When it
  decides you are not, every request — search and playback alike — comes back
  "Sign in to confirm you're not a bot". The cure is yt-dlp's cookies:

      rasterbar --cookies-from-browser brave "lofi"
      export VSPZ_YT_CLI_COOKIES_FROM_BROWSER=brave   # once per shell

  A cookies.txt also works, with --cookies FILE. Name a browser you are actually
  signed into YouTube in — a profile with no session makes things worse rather
  than better, and the error says so when that is what happened.

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
third-party Go modules. The expensive styles (radial, particles, aurora) are
capped rather than disabled: particles never exceed 400, and on a large grid
radial and aurora draw at a fraction of the frame rate rather than at none.

requires: yt-dlp, ffmpeg, mpv  (ytfzf optional, used as the primary scraper)
`

// usage is the help text with the style list filled in from the registry.
//
// Generated rather than written out, for the reason paletteCount generates the
// digit table: a hardcoded list of style names is right exactly until the next
// style lands, and nothing fails when it goes stale -- `--viz` still resolves, the
// error message still lists the real names, and the help quietly lies about what
// exists. Found by driving the binary after adding twelve styles: the help still
// said six.
//
// strings.Replace rather than fmt.Sprintf: the template already contains a `%` in
// its percentage-related prose, and Sprintf would reject it as an unknown verb.
// One substitution site does not need a format string.
var usage = strings.Replace(usageTemplate, "%s", strings.Join(VizNames(), " "), 1)

type options struct {
	// args holds the positional arguments in the order given, after any leading
	// verb has been consumed. Resolved into either a file queue or a search query
	// in main.
	args []string
	// forceSearch comes from the `search` verb: the arguments are a query even if
	// one of them happens to name a file. Without it a directory called
	// "Discovery" turns `search discovery` into a browse.
	forceSearch bool
	// verbErr holds a verb's own argument error. takeVerb has no error return,
	// so parseArgs hands it over here rather than changing its signature.
	verbErr error
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

	// cookies hand yt-dlp a signed-in session. Needed on any machine YouTube has
	// decided is a scraper, which is most machines eventually — see ytdlpAuth.
	cookies     string
	cookiesFrom string

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
		case "--cookies":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--cookies needs a file (a Netscape cookies.txt)")
			}
			i++
			o.cookies = expandTilde(args[i])
		case "--cookies-from-browser", "--from-browser":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--cookies-from-browser needs a name: firefox, chrome, chromium, brave, edge, safari, opera")
			}
			i++
			o.cookiesFrom = args[i]
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
	// A leading verb is consumed here so the rest of the program never sees one.
	// Doing it after the flag loop rather than during it means `play` and
	// `--play` cannot be confused: the flag is still parsed as a flag, and only
	// a bare leading word is a verb.
	takeVerb(&o)
	if o.verbErr != nil {
		return o, o.verbErr
	}
	return o, nil
}

// verbs are the subcommands. A first positional argument that matches one is a
// verb and is consumed; everything after it means what that verb says it means.
//
// Bare arguments still work exactly as they did — `rasterbar lofi hip hop radio`
// searches, `rasterbar song.mp3` plays the file — because a verb layer that
// breaks the thing people already type is worse than no verb layer. The verbs
// exist to say what used to need a flag, and to be explicit when the argument
// alone is ambiguous.
//
//	play            search or play files (the default; stated out loud)
//	search          force a YouTube search, even if an argument looks like a path
//	list DIR        browse a directory           (was -l DIR)
//	viz NAME        pick a visualizer            (was --viz NAME)
//	palette NAME    pick a palette               (was --palette NAME)
//	help            this text                    (was --help)
//	version         print the version
//
// The flags all still work. They are short, several are muscle memory, and
// `-a -q 480` is not worse than `play --video --quality 480`. The verbs are the
// discoverable front door, not a replacement.
// Named rather than closures aliasing each other: `verbs["viz"]` inside its own
// initialiser is an initialization cycle, and Go is right to refuse it.
func verbViz(o *options, args []string) ([]string, error) {
	_, rest, err := oneNamed(o, args, "--viz", &o.viz, VizNames())
	return rest, err
}

func verbPalette(o *options, args []string) ([]string, error) {
	_, rest, err := oneNamed(o, args, "--palette", &o.palette, paletteNames())
	return rest, err
}

func verbSearch(o *options, args []string) ([]string, error) {
	o.forceSearch = true
	return args, nil
}

func verbPlay(o *options, args []string) ([]string, error) { return args, nil }

func verbList(o *options, args []string) ([]string, error) { return listDirArg(o, args) }

var verbs = map[string]func(o *options, args []string) ([]string, error){
	"play":       verbPlay,
	"search":     verbSearch,
	"find":       verbSearch,
	"list":       verbList,
	"library":    verbList,
	"viz":        verbViz,
	"visual":     verbViz,
	"visualize":  verbViz,
	"visualizer": verbViz,
	"palette":    verbPalette,
	"color":      verbPalette,
	"help":       verbPrintHelp,
	"version":    verbPrintVersion,
}

// version is stamped at build time:
//
//	go build -ldflags="-X main.version=v1.2.3"
//
// Empty when built by hand, and `rasterbar version` says so rather than printing
// an empty string as though it meant something.
var version = ""

// helpRequested and versionRequested are set by their verbs and acted on in main.
// parseArgs cannot print and exit: it returns options.
var helpRequested, versionRequested bool

func verbPrintHelp(o *options, args []string) ([]string, error) {
	helpRequested = true
	return nil, nil
}

func verbPrintVersion(o *options, args []string) ([]string, error) {
	versionRequested = true
	return nil, nil
}

// oneNamed consumes the value a verb like `viz` or `palette` requires.
//
// Shared with the flag form so the two cannot drift: the flag has been the only
// way to say this for the life of the project, and a verb that accepts a name the
// flag rejects is two lists to keep in step.
func oneNamed(o *options, args []string, flagName string, dst *string, valid []string) (string, []string, error) {
	if len(args) == 0 {
		return "", nil, fmt.Errorf("needs a value: %s", strings.Join(valid, ", "))
	}
	v := args[0]
	for _, ok := range valid {
		if strings.EqualFold(ok, v) {
			*dst = v
			return v, args[1:], nil
		}
	}
	return "", nil, fmt.Errorf("must be one of %s (got %s)", strings.Join(valid, ", "), v)
}

func listDirArg(o *options, args []string) ([]string, error) {
	dir := "."
	rest := args
	if len(args) > 0 {
		dir = expandTilde(args[0])
		rest = args[1:]
	}
	if st, err := os.Stat(dir); err != nil || !st.IsDir() {
		return nil, fmt.Errorf("%s is not a directory", dir)
	}
	o.library = dir
	return rest, nil
}

func paletteNames() []string {
	names := make([]string, 0, len(palettes))
	for _, p := range palettes {
		names = append(names, p.name)
	}
	return names
}

// takeVerb consumes a leading verb, if the first argument is one.
//
// Only ever looks at the FIRST positional argument. `rasterbar play something`
// is a verb followed by a query; `rasterbar lofi hip hop` is a query that happens
// to start with a word, and someone searching for a song called "Play" should
// still find it.
//
// The exception is a bare verb with nothing after it: `rasterbar play` searches
// for the word "play" rather than printing usage, because a search is a harmless
// answer to a mistyped invocation and "no arguments" is not an answer at all.
//
// The verbs excluded from that fallback are the ones where the word is not
// something anyone searches for: `viz` and `palette` need a value and should say
// which are valid, and `help`/`version` must work with no arguments at all or
// they are not verbs, they are aliases for a flag that already existed.
var neverBareVerbs = map[string]bool{
	"viz": true, "visual": true, "visualize": true, "visualizer": true,
	"palette": true, "color": true,
	"help": true, "version": true,
	"list": true, "library": true, // these take an optional DIR, defaulting to "."
}

func takeVerb(o *options) (string, bool) {
	if len(o.args) == 0 {
		return "", false
	}
	fn, ok := verbs[o.args[0]]
	if !ok {
		return "", false
	}
	if len(o.args) == 1 && !neverBareVerbs[o.args[0]] {
		return "", false
	}
	verb := o.args[0]
	rest, err := fn(o, o.args[1:])
	if err != nil {
		// Surfaced by the caller's error path; stored because takeVerb has no
		// error return and parseArgs already has one to fill.
		o.verbErr = err
		return verb, true
	}
	o.args = rest
	return verb, true
}

// expandFileArgs turns positional arguments that name files into a queue of
// paths, in order.
//
// The decision is "does at least one argument look like a file", not "does every
// argument" — `rasterbar b.opus lofi hip hop` is a mixed intent, and taking the
// file and searching for the rest would be two players. When any argument looks
// like a file, all of them are treated as paths and the ones that are not get an
// error naming them, which is honest. When none does, it is a search.
//
// The second return value is whether this was a file intent at all, and it is
// load-bearing. There are three answers, not two:
//
//	(nil, false, nil)  no argument names a file -> a YouTube search
//	(p,  true,  nil)  files resolved          -> play them
//	(_,  true,  err)  files asked for, none usable -> report why
//
// The first version returned only (paths, error) and the caller treated "no
// paths" as "nothing playable", which made every search die with "no playable
// files in lofi hip hop radio". The primary feature of the program, gone, and
// nothing in the suite noticed because no test asked for a path that is NOT a
// file end to end.
func expandFileArgs(args []string) (paths []string, isFileIntent bool, err error) {
	any := false
	for _, a := range args {
		if looksLikeFile(a) {
			any = true
			break
		}
	}
	if !any {
		return nil, false, nil
	}

	var out []string
	for _, a := range args {
		paths, isFile, err := loadPathArg(a, 0)
		if err != nil {
			return nil, true, err
		}
		if !isFile {
			return nil, true, fmt.Errorf("%q is not a file, a glob or a playlist", a)
		}
		out = append(out, paths...)
	}
	return out, true, nil
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
	if opts.library == "" && len(opts.args) > 0 && !opts.forceSearch {
		var isFileIntent bool
		filePaths, isFileIntent, err = expandFileArgs(opts.args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		if isFileIntent && len(filePaths) == 0 {
			// Every argument looked like a path and none of them resolved to a
			// playable file. Saying so beats falling back to a YouTube search for
			// the user's own filename, which is what used to happen.
			fmt.Fprintf(os.Stderr, "no playable files in %s\n", strings.Join(opts.args, " "))
			os.Exit(1)
		}
	}
	if len(filePaths) == 0 && opts.library == "" {
		opts.query = strings.Join(opts.args, " ")
	}

	// Before the empty-input check below, not after: `rasterbar help` and
	// `rasterbar version` legitimately carry nothing else on the command line,
	// and falling through to "no arguments, here is the usage text" made
	// `version` print the help and look like it worked.
	if helpRequested {
		fmt.Print(usage)
		return
	}
	if versionRequested {
		if version == "" {
			fmt.Println("rasterbar (development build, no version stamped)")
			return
		}
		fmt.Println("rasterbar", version)
		return
	}

	if len(filePaths) == 0 && opts.library == "" && opts.query == "" {
		fmt.Print(usage)
		return
	}

	// The flag wins over the environment, and the file wins over the browser: the
	// one that was stated most precisely. The environment is what makes this
	// usable at all, because a machine under a bot check needs it on every single
	// invocation and typing a flag each time is how people end up not doing it.
	auth := ytdlpAuthFromEnv(os.Environ())
	if opts.cookies != "" {
		auth = ytdlpAuthConfig{FromFile: opts.cookies}
	} else if opts.cookiesFrom != "" {
		auth = ytdlpAuthConfig{FromBrowser: opts.cookiesFrom}
	}
	ytdlpAuth = auth

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
