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

keys (playback):
  q / ctrl-c       stop and exit

tuned for low-end machines: ASCII mode requests a 360p h264 source because
ffmpeg decode cost scales with resolution, and an 80x22 character grid cannot
show the difference between 360p and 1080p. The spectrum is computed in-process
with a hand-rolled FFT, so there are no third-party Go modules.

requires: yt-dlp, ffmpeg, mpv  (ytfzf optional, used as the primary scraper)
`

type options struct {
	query   string
	mute    bool
	ascii   bool
	quality Quality
	aspect  float64
	cols    int // explicit override, 0 = auto
	rows    int
	color   ColorMode // colorAuto unless a flag says otherwise
	glyph   GlyphMode // GlyphAuto unless a flag says otherwise
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
	if opts.query == "" {
		fmt.Print(usage)
		return
	}

	// Resolve colour mode: explicit flag wins, otherwise ask the terminal.
	colorMode := opts.color
	if colorMode == colorAuto {
		colorMode = detectColor(envSlice())
	}

	fmt.Fprintf(os.Stderr, "searching: %s\n", opts.query)
	tracks, err := Search(opts.query)
	if err != nil {
		fmt.Fprintf(os.Stderr, "search failed: %v\n", err)
		os.Exit(1)
	}

	tui := NewTUI(os.Stdin, os.Stdout, opts.query, tracks)
	action, idx := tui.Run()
	if action == ActionQuit || idx < 0 {
		return
	}
	track := tracks[idx]

	wantASCII := opts.ascii
	if action == ActionASCII {
		wantASCII = true
	}

	if wantASCII {
		fmt.Fprintf(os.Stderr, "ascii: %s — %s\n", track.Title, track.ChannelText())
		if err := runASCII(track, os.Stdin, os.Stdout, opts.mute, opts.quality, opts.aspect, opts.cols, opts.rows, colorMode, opts.glyph); err != nil {
			fmt.Fprintf(os.Stderr, "ascii playback failed: %v\n", err)
			os.Exit(1)
		}
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

// debugSync enables the A/V drift readout on stderr. Off by default because it
// writes to the same stream the renderer uses.
var debugSync = os.Getenv("VSPZ_YT_CLI_DEBUG_SYNC") != ""
