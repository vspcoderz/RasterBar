<div align="center">

<pre>
▛▀▖      ▐        ▛▀▖      
▙▄▘▝▀▖▞▀▘▜▀ ▞▀▖▙▀▖▙▄▘▝▀▖▙▀▖
▌▚ ▞▀▌▝▀▖▐ ▖▛▀ ▌  ▌ ▌▞▀▌▌  
▘ ▘▝▀▘▀▀  ▀ ▝▀▘▘  ▀▀ ▝▀▘▘  
</pre>

# RasterBar

**A YouTube player that lives in your terminal.**
ASCII video, or a music visualiser with eighteen styles and ten gradient palettes
— in one static binary with **zero third-party Go modules**.

</div>

---

<p align="center">
  <img src="docs/mirror.png" width="49%" alt="mirror visualiser at 435x372">
  <img src="docs/mirror-wide.png" width="49%" alt="mirror visualiser at 715x445">
</p>

<p align="center"><sub>
  <code>mirror</code> visualiser, two terminal widths. The strip under the progress
  bar is the live spectrum; the chrome is the HUD.
</sub></p>

---

<p align="center">
  <img src="docs/video-mode.png" width="86%" alt="video mode: colour ASCII of Never Gonna Give You Up">
</p>

<p align="center"><sub>
  <strong>Video mode</strong> (<code>-a</code>) — the picture rendered into the cell grid in
  colour, with the HUD and key hints below it. The visualiser screenshots above are
  <strong>music mode</strong>, which has no video at all.
</sub></p>

---

## What it is

You point it at a search, a URL, or a folder. It plays the audio through `mpv`
and paints the picture itself, straight into the terminal's cell grid — colour,
half-blocks, diff-rendered so only the cells that changed get written.

Two modes, one player:

| | |
|---|---|
| **`-M` music mode** *(default)* | no video. Eighteen audio-reacting visualisers, ten palettes, beat detection. |
| **`-a` video mode** | ASCII or truecolour video, with the spectrum strip under the picture. |

## Visualisers

`v` and `V` walk them; `--viz NAME` picks one at startup.

Eighteen, in the order `v` walks them — the readable ones first, the strange ones
last.

**From the waveform** (amplitude against time)

| name | what it does |
|---|---|
| `scope` | a triggered oscilloscope. ~2.5 cycles of the dominant pitch, standing still. |
| `lissajous` | the waveform against itself, quarter-period delayed. A tone is an ellipse. |
| `ribbon` | the waveform's per-column min and max, filled. Loudness as thickness. |
| `swell` | a waterfall of the waveform rather than the spectrum. Tremolos and fades are shapes here. |

**From the spectrum** (frequency against level)

| name | what it does |
|---|---|
| `bars` | the classic. Bars grow from the baseline, peak caps hold the top. |
| `mirror` | mirrored around the centreline — the wings in the screenshots. |
| `matrix` | a hardware LED meter. Eight quantised dots per band, dark ones drawn. |
| `peaks` | only the local maxima, as needles at interpolated frequency. A triad is three needles. |
| `rays` | light shafts rising from the bottom. Heads pop on transients. |
| `wheel` | a polar spectrum: every band its own radius band, loudness is *thickness*. |
| `helix` | the spectrum wound onto a rotating coil, bass at the outside. |
| `terrain` | the spectrum's history in perspective, receding to a horizon. |
| `waterfall` | a scrolling spectrogram. Newest row on top, fading with age. |
| `radial` | one spoke per frequency band, fanning out from the centre. |

**From the beat and the tempo**

| name | what it does |
|---|---|
| `bloom` | an expanding ring per onset, sized by the spectrum's brightness. |
| `metro` | wavefronts travelling outward at the estimated BPM. The only style that uses the tempo. |
| `particles` | dots launched on transients and integrated with momentum. |

**A field, not a reading**

| name | what it does |
|---|---|
| `aurora` | a drifting noise field lit by the bass. Capped, not disabled, on a big grid. |

## Palettes

`c` cycles. `1`–`9` and `0` pick one outright.

| key | palette | gradient runs on |
|:---:|---|---|
| 1 | `spectrum` | frequency — bass red → treble violet |
| 2 | `height` | cell value, so every bar is a gradient of its own length |
| 3 | `ocean` | frequency, narrow cyan→blue. Quiet on a dark terminal. |
| 4 | `ember` | frequency, warm red→orange. Transients read as heat. |
| 5 | `graphite` | cell value, **pure greyscale** |
| 6 | `ink` | frequency, **pure greyscale** — bass black, treble white |
| 7 | `ice` | cell value, deep navy → near-white |
| 8 | `magma` | cell value, black → red → orange → yellow |
| 9 | `viridis` | both, dark purple → teal → yellow |
| 0 | `mono` | none — no colour at all. Deliberately last. |

Every one is a gradient. The two greyscale ones are *not* `--mono`: they return
`r==g==b` from the same colour path, which is what lets them carry a real
luminance ramp instead of a flat fill.

## Install

All the code lives in `src/`, so the installable package path is the module path
plus `src` — and Go names the binary after the last element, so this one calls
itself `src`:

```sh
go install github.com/vspcoderz/rasterbar/src@latest
```

Building it yourself is the better route if you want the binary called
`rasterbar`, and it is what the release notes use:

```sh
git clone https://github.com/vspcoderz/RasterBar
cd RasterBar
go build -ldflags="-s -w" -o rasterbar ./src
```

**Requires** `ffmpeg` and `mpv` on `$PATH`. Searching YouTube additionally wants
`yt-dlp` and `ytfzf`.

### If YouTube says "Sign in to confirm you're not a bot"

YouTube decides by IP whether you are a person, and a flagged machine gets that
error on *every* request — search and playback alike. Nothing in this program can
work around it; `yt-dlp` needs the cookies of a browser you are signed into.

```sh
rasterbar --cookies-from-browser chromium "lofi hip hop"
export VSPZ_YT_CLI_COOKIES_FROM_BROWSER=chromium   # once per shell, done
```

A `cookies.txt` also works: `rasterbar --cookies FILE`.

Name a browser you are actually logged into YouTube in. A profile with no
session — Brave and Chrome on this machine have none, for instance — fails with
"could not find cookies database", and the program says so rather than sending
you round in circles.

## Use

```sh
rasterbar "lofi hip hop radio"          # search and play
rasterbar https://youtu.be/...         # a URL
rasterbar -a -c "tesseract"            # ASCII/colour video mode
```

Or point it at files instead of a search:

```sh
rasterbar play track.mp3 prelude.opus  # these files, in this order
rasterbar play "~/Music/*.flac"         # a glob
rasterbar play roadtrip.m3u            # an m3u/m3u8 playlist
rasterbar list ~/Shows --play          # browse a folder
```

An argument is treated as a file when it exists, contains a wildcard, or names a
playlist — anything else is a YouTube search, so `"lofi hip hop radio"` still
does what it looks like. A file with no video stream plays as a visualiser no
matter which mode you asked for, since there is no picture to show.

### Verbs

Every flag still works. The verbs are the same flags spelled out, and exist so
you can find them:

| verb | does |
|---|---|
| `play <query\|file...>` | search YouTube, or play files — the default |
| `search <query>` | force a search, even if an argument looks like a path |
| `list [DIR]` | browse a directory (was `-l DIR`); defaults to `.` |
| `viz NAME` | pick a visualizer (was `--viz`) |
| `palette NAME` | pick a palette (was `--palette`) |
| `help` | the full usage text |
| `version` | the version |

`rasterbar play` with nothing after it searches for the word "play", because a
search is a harmless answer to a mistyped command and "no arguments" is not an
answer at all.

`list` orders a directory two ways at once: files with an episode marker sort
series → season → episode, and files without one sort by natural filename order,
so `track 2` comes before `track 10`. That is what makes a music folder browsable
here rather than invisible.

### Keys while playing

| key | |
|---|---|
| `space` | pause / resume |
| `←` `→` | seek ∓10s |
| `,` `.` | seek ∓1s |
| `<` `>` | seek ∓60s |
| `[` `]` | previous / next chapter |
| `:` | jump to a timestamp |
| `n` `p` | next / previous track |
| `+` `-` | volume |
| `s` | toggle the spectrum strip |
| `q` / `ctrl-c` | quit |

Music mode only — there is no visualiser in video mode:

| key | |
|---|---|
| `v` `V` | next / previous visualiser (18 of them; `--viz NAME` picks one outright) |
| `c` | next palette |
| `1`–`9` `0` | pick a palette directly |
| `W` | split view: video beside the visualiser (off by default) |
| `a` `d` | move the video pane left / right |
| `T` | video pane: still thumbnail / live video |
| `{` `}` | move the split divider |

### Keys while browsing

| key | |
|---|---|
| `j` `k` / arrows | move |
| `g` `G` | top / bottom |
| `1`–`9` | jump to row |
| `enter` | play in music mode |
| `a` | play in video mode |

The `:` prompt takes `1:30`, `1:02:03`, a bare `90` (seconds), `90s` / `2m` /
`1h2m3s`, `+30` / `-1:30` relative to now, and `50%` of the track. It previews
where the jump will land before you commit.

### Split view

In music mode, `W` puts the video beside the visualiser instead of replacing it —
a second pane, on whichever side you want, with `{` and `}` moving the divider.
It is off by default because it costs a second ffmpeg and half the grid.

`T` swaps the pane between live video and the still thumbnail that `yt-dlp` already
returned in a response the player had already made, so the still costs no extra
request and no video stream at all.

## Notable

**Zero third-party Go modules.** The FFT, the PTY, raw mode and termios are all
hand-rolled. That's the project's defining constraint, not a preference — each
dependency would have been a few dozen lines of syscall or arithmetic, and a
player you can `go install` and trust is worth more than one with a module graph.

**The renderer diffs.** Repainting the whole grid every frame cost ~11KB at
200×57 and made the terminal, not the CPU, the bottleneck. Writes are grouped into
per-row runs so a changed cell costs a cursor move and a few characters.

**The spectrum is calibrated against real music.** The band levels are
unnormalised FFT magnitudes carrying a measured +44.2dB offset, and the noise
gate tests shape *and* level on the raw bands before any normalisation. Both
earlier versions of that gate passed on synthesised tones and were wrong on music
— the tones read mean/max 0.04–0.11 where real tracks read 0.42–0.80.

**It fits your terminal.** Grid width, height, source resolution and frame rate
are all derived from the window size, so a small window doesn't ask ffmpeg for a
4K stream. `TIOCGWINSZ` is watched, and a resize rebuilds the decoder.

**Video is letterboxed, not stretched.** The picture keeps its own proportions
whatever shape your window is. It didn't used to: the filter scaled straight to
the cell grid, so a square in a 16:9 frame rendered 0.57:1 on screen — squashed
by 43%, which is invisible in a shot and glaring in a title card.

**Frames are presented atomically.** Every frame is wrapped in DEC mode 2026, so
the terminal shows a finished picture rather than a half-painted one. Terminals
that don't implement it ignore it.

## License

MIT — see [LICENSE](LICENSE).