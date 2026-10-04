// Command vspz-yt-cli searches YouTube and plays results in a terminal UI:
// audio-first with a live FFT spectrum, synced ASCII video on toggle.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

const usage = `vspz-yt-cli - terminal YouTube player with ASCII video and audio spectrum

usage:
  vspz-yt-cli [options] <query>

options:
  -m, --mute         play audio muted (visualizer still animates)
  -a, --ascii        start ASCII video mode instead of audio
      --glyph MODE   auto (probe terminal), half (2px/cell), cell (1px/cell)
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

library mode (browse a directory instead of searching):
  -l, --library DIR  scan DIR for video files, ordered series/season/episode
      --no-probe     skip the ffprobe pass (instant scan, "--:--" durations)
      --play         start the first episode without the browse list

  Filenames are parsed for series, season and episode, so "Show S01E02.mkv",
  "Show - 01x02.mkv", "Show - 07 [1080p].mkv" and "Show Episode 7.mkv" all
  land in a watchable order. A file with no episode marker is skipped rather
  than placed arbitrarily.

  requires: ffmpeg, mpv  (yt-dlp and ytfzf are needed only for search)

The ASCII grid and the source resolution both follow your terminal size
automatically: a small window requests a small video, a large one a larger
video, so you never pay decode CPU for pixels you cannot see.

keys (browse):
  j / k / arrows   move
  g / G            top / bottom
  1-9              jump to row
  enter            play audio + spectrum
  a                play ASCII video
  q                quit

keys (ascii playback):
  space            pause / resume
  left / right     seek -10s / +10s
  , / .            seek -1s / +1s
  < / >            seek -60s / +60s
  [ / ]            previous / next chapter
  :                jump to a timestamp
  n / p            next / previous result
  + / -            volume up / down
  q / ctrl-c       quit

  The jump prompt takes 1:30, 1:02:03, a bare 90 (seconds), 90s / 2m / 1h2m3s,
  +30 / -1:30 (relative to now), and 50% (of the track). It previews where the
  jump will land before you commit to it. enter jumps, esc cancels.

  When a track finishes the next result starts on its own; playback stops at the
  end of the list.

tuned for low-end machines: ASCII mode requests a 360p h264 source because
ffmpeg decode cost scales with resolution, and an 80x22 character grid cannot
show the difference between 360p and 1080p. The spectrum is computed in-process
with a hand-rolled FFT, so there are no third-party Go modules.

requires: yt-dlp, ffmpeg, mpv  (ytfzf optional, used as the primary scraper)
`

type options struct {
	query    string
	mute     bool
	ascii    bool
	quality  Quality
	aspect   float64
	cols     int // explicit override, 0 = auto
	rows     int
	color    ColorMode // colorAuto unless a flag says otherwise
	glyph    GlyphMode // GlyphAuto unless a flag says otherwise
	library  string    // directory to browse instead of searching
	noProbe  bool      // skip ffprobe during a library scan
	playOnly bool      // play immediately, skip the browse list
}

// colorAuto requests terminal capability detection.
const colorAuto ColorMode = -1

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
			if o.query != "" {
				o.query += " " + a
			} else {
				o.query = a
			}
		}
	}
	return o, nil
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
	if opts.query == "" && opts.library == "" {
		fmt.Print(usage)
		return
	}

	// Resolve colour mode: explicit flag wins, otherwise ask the terminal.
	colorMode := opts.color
	if colorMode == colorAuto {
		colorMode = detectColor(envSlice())
	}

	// Two sources, one Track shape: a network search or a directory scan. The
	// library branch deliberately skips Search entirely rather than both
	// filling a slice, because there is no query to hand a scraper.
	var tracks []Track
	label := opts.query
	if opts.library != "" {
		label = opts.library
		fmt.Fprintf(os.Stderr, "scanning library: %s\n", opts.library)
		tracks, err = ScanLibrary(opts.library, !opts.noProbe)
		if err != nil {
			fmt.Fprintf(os.Stderr, "library scan failed: %v\n", err)
			os.Exit(1)
		}
		if len(tracks) == 0 {
			fmt.Fprintf(os.Stderr, "no playable files with an episode number under %s\n", opts.library)
			os.Exit(1)
		}
		fmt.Fprintf(os.Stderr, "%d episodes\n", len(tracks))
	} else {
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
	track := tracks[idx]

	// --play skips the browse list, so there is no keypress to request ASCII
	// mode. Default it on: asking for a library episode and getting a spectrum
	// alone would not be what anyone wanted.
	wantASCII := opts.ascii || (opts.playOnly && opts.library != "")
	if action == ActionASCII {
		wantASCII = true
	}

	if wantASCII {
		playQueue(opts, colorMode, tracks, idx)
		return
	}

	fmt.Fprintf(os.Stderr, "playing: %s — %s\n", track.Title, track.ChannelText())
	audioURL, err := resolveAudioURL(track)
	if err != nil {
		fmt.Fprintf(os.Stderr, "resolve audio failed: %v\n", err)
		os.Exit(1)
	}
	if err := runVisualAudio(track, os.Stdin, os.Stdout, audioURL, opts.mute); err != nil {
		fmt.Fprintf(os.Stderr, "playback failed: %v\n", err)
		os.Exit(1)
	}
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
func playQueue(opts options, mode ColorMode, tracks []Track, index int) {
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
	}
	if po.aspect <= 0 {
		po.aspect = defaultAspect
	}

	// One key reader for the whole queue. Starting one per track left the
	// previous goroutine blocked in Read after a queue advance, and the two then
	// raced for stdin — which showed up as the quit key doing nothing.
	keys := make(chan []byte, 16)
	go readKeys(po.in, keys)
	po.keys = keys

	for index >= 0 && index < len(tracks) {
		fmt.Fprintf(os.Stderr, "playing %d/%d: %s — %s\n",
			index+1, len(tracks), tracks[index].Title, tracks[index].ChannelText())

		outcome := playTrack(po, tracks, index)
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
