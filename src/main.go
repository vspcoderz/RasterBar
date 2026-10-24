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
  download <url|query>   save the video to your download folder as mp4; with a
                         query, opens the picker and enter/d downloads
  update [--check]       replace this binary with the newest release on GitHub
  settings               click-only screen: default mode, quality cap,
                         visualizer, palette, download folder, cookies
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
  rasterbar https://youtu.be/ID           play a link, from cache when it is
                                          already downloaded
  rasterbar download https://youtu.be/ID  same link, into the download folder

  An argument becomes a file when it exists, contains a wildcard, or names a
  playlist. A link becomes a player of that link, not a search for its text.
  Anything else is a YouTube search. A file with no video stream plays as a
  visualiser whichever mode you asked for, because there is no picture.

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
      --cache-dir DIR     the folder "download" saves videos into
                          (default ~/RasterStuff, then
                          VSPZ_YT_CLI_CACHE_DIR, then the settings file)
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
      --no-discord   do not publish what is playing to Discord (on by default)
      --discord      presence is on anyway; report if it cannot connect
      --discord-app-id ID
                      a different Discord application id (a snowflake: digits)
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
  d                download (in the download picker; enter there too)
  q                quit

download/save environment variables:
   VSPZ_YT_CLI_CACHE_DIR    the download folder, once per shell
   VSPZ_YT_CLI_UPDATE=auto  check GitHub in the background at launch and
                            mention a newer release before playback starts

downloads and the cache:
  Playing a YouTube link or a search result saves that video into the cache
  while it plays — the HUD status row shows "saving — 42.3% of ..." until
  it finishes, and the next play of the same video starts from the local
  file with no network at all (and free seek, pause and chapters). The
  download verb streams yt-dlp's own progress lines. --cache-dir picks the
  folder, the settings screen stores it, VSPZ_YT_CLI_CACHE_DIR keeps it
  for a shell.

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

  Discord: what is playing is published to your profile by default — the track
  title, the channel, and a progress bar — and it clears when you quit. The
  bar comes from start and end timestamps, so nothing is sent per frame; a
  track costs a few updates rather than thirty a second. --no-discord turns it
  off, and VSPZ_YT_CLI_NO_DISCORD=1 does the same for a whole shell.

      rasterbar --discord "lofi hip hop"  same, but say why if it cannot connect
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

	// downloadMode comes from the `download` verb: the arguments are a URL to
	// save to the cache, or a search whose results pick what to download.
	downloadMode bool
	// cacheDir is the `--cache-dir` value, stored unparsed (tilde intact).
	cacheDir string
	// updateMode from the `update` verb; updateCheck is its --check form,
	// compare-only.
	updateMode  bool
	updateCheck bool
	// settingsMode from the `settings` verb: open the click-only screen and
	// exit. Takes no position on anything else.
	settingsMode bool

	// viz and palette name a starting style, resolved here rather than at first
	// use so a typo is an argument error instead of a silently ignored flag.
	viz     string
	palette string

	// cookies hand yt-dlp a signed-in session. Needed on any machine YouTube has
	// decided is a scraper, which is most machines eventually — see ytdlpAuth.
	cookies     string
	cookiesFrom string

	// Discord presence. On by default because the app ID is ours and the whole
	// point of the feature is that it needs no flag; noDiscord turns it off, and
	// discord says "I asked for this, so tell me why it is not working" — the
	// difference between a program that is quiet because nothing happened and
	// one that is quiet because it failed.
	noDiscord    bool
	discord      bool
	discordAppID string

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
		case "--cache-dir":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--cache-dir needs a directory")
			}
			i++
			o.cacheDir = args[i]
		case "--check":
			// update's compare-only form. Refused later when no update verb
			// outranks it, so a stray --check has an owner or an error.
			o.updateCheck = true
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
		case "--no-discord":
			o.noDiscord = true
		case "--discord":
			o.discord = true
		case "--discord-app-id":
			if i+1 >= len(args) {
				return o, fmt.Errorf("--discord-app-id needs a numeric application id")
			}
			i++
			if err := validDiscordAppID(args[i]); err != nil {
				return o, err
			}
			o.discordAppID = args[i]
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

// verbDownload marks the download picker. Bare is an error rather than a
// search for the word "download": the verb's whole point is an argument that
// says what to save, and guessing would be uploading someone's weather query
// to a shop they never asked about. neverBareVerbs routes the bare case here.
func verbDownload(o *options, args []string) ([]string, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("download needs a URL or a search term")
	}
	o.downloadMode = true
	return args, nil
}

// verbUpdate is `update` or `update check`. Extra words are refused rather
// than ignored: an update that pretends it understood "update nightly arm"
// would install the wrong asset if one matched. The --check form is parsed as
// a flag (below), the spoken form as a word, both spelling one option.
func verbUpdate(o *options, args []string) ([]string, error) {
	if len(args) > 0 {
		if args[0] != "check" {
			return nil, fmt.Errorf("update takes --check or check, or nothing (got %q)", args[0])
		}
		o.updateCheck = true
	}
	o.updateMode = true
	return nil, nil
}

// verbSettings opens the click-only screen and exits; it does not play, and
// splitting the session there would be two fronts for one verb.
func verbSettings(o *options, args []string) ([]string, error) {
	if len(args) > 0 {
		return nil, fmt.Errorf("settings takes no arguments (got %s)", strings.Join(args, " "))
	}
	o.settingsMode = true
	return nil, nil
}

func verbList(o *options, args []string) ([]string, error) { return listDirArg(o, args) }

var verbs = map[string]func(o *options, args []string) ([]string, error){
	"play":       verbPlay,
	"download":   verbDownload,
	"dl":         verbDownload,
	"save":       verbDownload,
	"update":     verbUpdate,
	"settings":   verbSettings,
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
	// A bare `download` must error ("needs a URL or a search term"), a bare
	// `update` must actually update, and a bare `settings` must open the
	// screen: for all three the word is not something anyone searches for,
	// and the search fallback would swallow the whole verb.
	"download": true, "dl": true, "save": true,
	"update": true, "settings": true,
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

// expandArgs turns positional arguments into a queue of paths plus links.
//
// The decision is "does at least one argument look like a file or a link", not
// "does every argument" — `rasterbar b.opus lofi hip hop` is a mixed intent,
// and taking the file and searching for the rest would be two players. When any
// argument looks like a file or a link, the whole set is treated as such and
// anything unresolvable gets an error naming it, which is honest. When none
// does, it is a search.
//
// The second return value is whether this was a file/link intent at all, and it
// is load-bearing. There are three answers, not two:
//
//	(nil, nil, false, nil)  no argument names a file or link -> a YouTube search
//	(p, l,   true,  nil)  resolved -> play/download them
//	(_, _,   true,  err)  asked for, none usable -> report why
//
// Links live in their own slice, appended after the local files, because the
// order inside each kind is what the user typed and the two kinds differ in
// what they cost to resolve (a link needs yt-dlp, a file does not). A track
// from a link keeps its URL for the player; a file keeps its path.
//
// The first version of this (as expandFileArgs) returned only (paths, error)
// and the caller treated "no paths" as "nothing playable", which made every
// search die with "no playable files in lofi hip hop radio". The primary
// feature of the program, gone, and nothing in the suite noticed because no
// test asked for a path that is NOT a file end to end.
func expandArgs(args []string) (paths []string, links []string, isIntent bool, err error) {
	any := false
	for _, a := range args {
		if looksLikeFile(a) || looksLikeURL(a) {
			any = true
			break
		}
	}
	if !any {
		return nil, nil, false, nil
	}

	var out []string
	for _, a := range args {
		if looksLikeURL(a) {
			links = append(links, a)
			continue
		}
		paths, isFile, err := loadPathArg(a, 0)
		if err != nil {
			return nil, nil, true, err
		}
		if !isFile {
			return nil, nil, true, fmt.Errorf("%q is not a file, a glob, a playlist or a link", a)
		}
		out = append(out, paths...)
	}
	return out, links, true, nil
}

// validDiscordAppID checks an application id before it is used.
//
// Discord application ids are snowflakes: decimal digits, nothing else. A
// name, a UUID or a stray space all fail the handshake server-side with
// "Invalid Client ID", which reads like a networking problem and is not one.
// Catching it here turns a confusing failure at playback time into an argument
// error, which is the same treatment --viz and --palette already get.
func validDiscordAppID(id string) error {
	if id == "" {
		return fmt.Errorf("--discord-app-id needs a value")
	}
	for _, r := range id {
		if r < '0' || r > '9' {
			return fmt.Errorf("--discord-app-id must be the numeric application id (got %q)", id)
		}
	}
	return nil
}

// discordConfig resolves presence settings from the flag and the environment.
//
// The flag wins over the environment, for the same reason --cookies does: the
// one stated most precisely. Presence is on unless something says otherwise,
// so the only two ways to change it are --no-discord and the environment
// equivalent.
//
// Returned rather than applied so parseArgs stays a parser and main stays the
// thing that decides.
func discordConfig(o options, env []string) (enabled bool, appID string, err error) {
	for _, kv := range env {
		key, val, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch key {
		case "VSPZ_YT_CLI_DISCORD_APP_ID":
			if o.discordAppID == "" {
				o.discordAppID = val
			}
		case "VSPZ_YT_CLI_NO_DISCORD":
			if val != "" && val != "0" && val != "false" {
				o.noDiscord = true
			}
		}
	}
	appID = o.discordAppID
	if appID == "" {
		appID = discordAppID
	}
	return !o.noDiscord, appID, validDiscordAppID(appID)
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
	// a list of files, a list of links, or text to search YouTube for.
	//
	// -l wins outright when both are given, because a directory scan has its own
	// walk and its own episode parser and a file list would only duplicate them.
	// The download verb is skipped here: its arguments carry the same meaning
	// (what to save), not what to play, and resolving them twice would run
	// yt-dlp on a URL twice.
	var filePaths, linkArgs []string
	if opts.library == "" && len(opts.args) > 0 && !opts.forceSearch && !opts.downloadMode {
		var isIntent bool
		filePaths, linkArgs, isIntent, err = expandArgs(opts.args)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(1)
		}
		if isIntent && len(filePaths) == 0 && len(linkArgs) == 0 {
			// Every argument looked like a path and none of them resolved to a
			// playable file. Saying so beats falling back to a YouTube search for
			// the user's own filename, which is what used to happen.
			fmt.Fprintf(os.Stderr, "no playable files in %s\n", strings.Join(opts.args, " "))
			os.Exit(1)
		}
	}
	if len(filePaths) == 0 && len(linkArgs) == 0 && opts.library == "" {
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

	// The settings file is read before anything that consumes it. A load error
	// is a warning, not an exit: the settings are defaults, and a corrupt file
	// must never stop a song from playing.
	cfg, serr := loadSettings()
	if serr != nil {
		fmt.Fprintf(os.Stderr, "settings: %v (using defaults)\n", serr)
	}
	cfg.applyTo(&opts)

	// update and settings both end the session: no playback, no cache, no
	// presence. They run before anything that needs cookies, the terminal or
	// the audio stack.
	if opts.updateCheck && !opts.updateMode {
		fmt.Fprintln(os.Stderr, "--check belongs to `update`")
		os.Exit(2)
	}
	if opts.updateMode {
		msg, uerr := runUpdate(opts.updateCheck)
		if uerr != nil {
			fmt.Fprintln(os.Stderr, uerr)
			os.Exit(1)
		}
		fmt.Println(msg)
		return
	}
	if opts.settingsMode {
		out, saved, rerr := runSettings(cfg, os.Stdin, os.Stdout)
		if rerr != nil {
			fmt.Fprintf(os.Stderr, "settings: %v\n", rerr)
			os.Exit(1)
		}
		cfg = out
		if saved {
			path, _ := settingsPath()
			fmt.Fprintf(os.Stderr, "settings saved: %s\n", path)
		}
		return
	}

	// Background update check, VSPZ_YT_CLI_UPDATE=auto only. Started before the
	// network-heavy work so it has time to finish; the notice is consumed just
	// before the UI takes the terminal, non-blockingly.
	updCh := updateNotice()

	if len(filePaths) == 0 && len(linkArgs) == 0 && opts.library == "" && opts.query == "" {
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

	// The download verb. Runs after settings (folder) and cookies (yt-dlp needs
	// them on exactly the same terms as playback: cookies here, missed there,
	// is a bug report that depends on which path the user was on).
	if opts.downloadMode {
		drainUpdateNotice(updCh)
		if code := runDownload(opts, cfg, colorMode); code != 0 {
			os.Exit(code)
		}
		return
	}

	// Three sources, one Track shape: a network search, a directory scan, or
	// arguments the user named (files, or links). The library branch deliberately
	// skips Search entirely rather than both filling a slice, because there is no
	// query to hand a scraper; the same goes for explicit files.
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

	case len(filePaths) > 0 || len(linkArgs) > 0:
		// Files resolve before links; a link costs yt-dlp round trips and a
		// file does not, and each kind keeps the order it was typed in.
		tracks = tracksFromPaths(filePaths)
		for _, link := range linkArgs {
			tracks = append(tracks, trackForURL(link))
		}
		label = fmt.Sprintf("%d tracks", len(tracks))
		fmt.Fprintf(os.Stderr, "%s: %s\n", label, strings.Join(opts.args, " "))

	default:
		fmt.Fprintf(os.Stderr, "searching: %s\n", opts.query)
		tracks, err = Search(opts.query)
		if err != nil {
			fmt.Fprintf(os.Stderr, "search failed: %v\n", err)
			os.Exit(1)
		}
	}

	// Any remote track already in the download cache plays from the file
	// instead of a minted grant — free seeks, chapters and pause, and none of
	// the single-use URL machinery. The swap happens BEFORE the metadata fetch:
	// a cached link must not pay one yt-dlp round trip per play, which is
	// exactly the wait the cache exists to remove. The cache directory is
	// resolved but not created: playing a track is not a reason to make folders
	// on disk, and a failed ReadDir in cacheLookup just means "not cached".
	if len(tracks) > 0 {
		if dir, derr := resolveCacheDir(opts.cacheDir, cfg.CacheDir); derr == nil {
			serveFromCache(dir, tracks)
		}
	}

	// A bare URL is not a search result, so its Title/ID/duration are whatever
	// yt-dlp says rather than nothing. One -J per track that lacks them, and
	// only when the field is missing: a search result already carries all of
	// this, a library track never reaches here, and a cache hit above resolved
	// to a local file with no network at all. A failure is a warning, not an
	// exit — the track plays the same either way.
	for i := range tracks {
		if tracks[i].IsLocal() || tracks[i].Title != "" || tracks[i].URL == "" {
			continue
		}
		if merr := fetchTrackMeta(&tracks[i]); merr != nil {
			fmt.Fprintf(os.Stderr, "track %d: %v (playing anyway)\n", i+1, merr)
		}
		if tracks[i].Title == "" {
			// The display has to show something, and a bare link is the truth
			// about what the user asked for even when the metadata lied.
			tracks[i].Title = strings.TrimSpace(tracks[i].URL)
		}
	}

	// A library scan is already an ordered queue, so --play skips the browse
	// list: picking one episode by hand is the only thing between the command
	// and playback, and for a known show you want the first episode.
	idx := 0
	action := ActionPlay
	if !opts.playOnly {
		drainUpdateNotice(updCh)
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
	//
	// The cache dir is resolved here (empty on failure: no cache, no background
	// saves, playback unaffected) rather than past Opts through, because the
	// session wants a concrete path and the option is a maybe-tilde string.
	playDir, pderr := resolveCacheDir(opts.cacheDir, cfg.CacheDir)
	if pderr != nil {
		playDir = ""
	}
	playQueue(opts, colorMode, tracks, idx, !wantVideo, playDir)
}

// runDownload is the whole download verb: URL arguments are saved directly,
// anything else is joined into one search whose picker downloads on enter.
//
// Playing stays available from the picker ('a', video mode), and the picker is
// given back after every download or playback ends — a download session is
// "what do I want from this query", not one item and out.
func runDownload(opts options, cfg settings, mode ColorMode) int {
	paths, links, _, err := expandArgs(opts.args)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if len(links) == 0 {
		// No links means nothing here is a URL. With file intent that is "you
		// already have this file, on disk"; otherwise it is the picker flow.
		if len(paths) > 0 {
			fmt.Fprintf(os.Stderr, "download takes a URL or a search term; %q is already on disk\n",
				strings.Join(paths, " "))
			return 1
		}
		if strings.TrimSpace(opts.query) == "" {
			// Unreachable through the verb (bare `download` errors in
			// verbDownload), but runDownload is callable directly in tests.
			fmt.Fprintln(os.Stderr, "download needs a URL or a search term")
			return 1
		}
		tracks, serr := Search(opts.query)
		if serr != nil {
			fmt.Fprintf(os.Stderr, "search failed: %v\n", serr)
			return 1
		}
		for {
			tui := NewDownloadTUI(os.Stdin, os.Stdout, opts.query, tracks)
			action, sel := tui.Run()
			if action == ActionQuit || sel < 0 {
				return 0
			}
			if action == ActionASCII || action == ActionPlay {
				// Playing from the picker uses the same queue machinery as the
				// play verb, so seek, pause, chapters and the HUD all work in
				// a session that opened as a download. The cache rides along:
				// a download-mode play saves too, so "watch it and have it
				// next time" costs one keypress.
				playDir, pderr := resolveCacheDir(opts.cacheDir, cfg.CacheDir)
				if pderr != nil {
					playDir = ""
				}
				playQueue(opts, mode, tracks, sel, action != ActionASCII, playDir)
				continue
			}
			dir, derr := resolveCacheDir(opts.cacheDir, cfg.CacheDir)
			if derr != nil {
				fmt.Fprintln(os.Stderr, derr)
				return 1
			}
			// A picker entry already carries Title and ID from the search, so
			// downloadTrack goes straight to the cache check. os.Stdout is the
			// sink: raw mode is off, the terminal is plain, and yt-dlp's own
			// progress lines ARE the loading display ("[download] 42.3% ...").
			path, already, derr := downloadTrack(tracks[sel], dir, os.Stdout)
			if derr != nil {
				fmt.Fprintf(os.Stderr, "download: %v\n", derr)
				continue
			}
			state := "saved"
			if already {
				state = "already cached"
			}
			fmt.Printf("%s: %s\n", state, path)
		}
	}

	// URL arguments: save each one. A failure names its link and stops the
	// batch — `download url1 url2` with url1 offline must not half-report
	// success by continuing.
	dir, derr := resolveCacheDir(opts.cacheDir, cfg.CacheDir)
	if derr != nil {
		fmt.Fprintln(os.Stderr, derr)
		return 1
	}
	for i, link := range links {
		tr := trackForURL(link)
		if merr := fetchTrackMeta(&tr); merr != nil {
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: %v\n", i+1, len(links), link, merr)
			return 1
		}
		path, already, derr := downloadTrack(tr, dir, os.Stdout)
		if derr != nil {
			fmt.Fprintf(os.Stderr, "[%d/%d] %s: %v\n", i+1, len(links), link, derr)
			return 1
		}
		state := "saved"
		if already {
			state = "already cached"
		}
		fmt.Printf("[%d/%d] %s: %s\n", i+1, len(links), state, path)
	}
	return 0
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
//
// cacheDir is the resolved download cache, ""-able. It is a parameter rather
// than a field on options because the resolved value (tilde expanded, runtime
// config applied) is what the session needs, and two resolution sites in this
// file already exist.
func playQueue(opts options, mode ColorMode, tracks []Track, index int, music bool, cacheDir string) {
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
		cacheDir:  cacheDir,
		// One prefs value for the whole queue, shared by every track: pressing `v`
		// once should not have to be repeated per song.
		prefs: opts.prefs(),
	}
	if po.aspect <= 0 {
		po.aspect = defaultAspect
	}

	// Presence. Opened once for the whole queue, not per track: the handshake is
	// a round trip to Discord and a socket we would otherwise open and close
	// once per song.
	//
	// A failure here is reported only when the user asked for presence by name.
	// Presence is on by default, and the overwhelmingly common reason it fails
	// is that Discord is not running — which is not an error worth a line of
	// stderr on every launch. --discord means "I asked for this", so then the
	// reason is worth saying.
	var pres *presence
	if presenceOn, appID, err := discordConfig(opts, os.Environ()); err != nil {
		fmt.Fprintf(os.Stderr, "discord presence: %v\n", err)
	} else if presenceOn {
		p, perr := newPresence(appID)
		if perr != nil {
			if opts.discord {
				fmt.Fprintf(os.Stderr, "discord presence: %v\n", perr)
			}
		} else {
			pres = p
		}
	}
	if pres != nil {
		// Deferred so every exit from the queue clears the activity: quitting,
		// the end of the list, and a track that failed all land here. Without
		// it a crash leaves the profile claiming to listen to a track that
		// stopped minutes ago.
		defer pres.Close()
	}
	po.presence = pres

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
