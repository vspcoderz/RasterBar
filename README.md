# vspz-yt-cli

A terminal YouTube player with two modes. **Video mode** renders video as ASCII or
colour art, locked in sync to the music. **Music mode** replaces the picture with a
visualizer driven by a real FFT — bars, an oscilloscope, a waterfall, a radial
spectrum, or a particle field — and it is a full player, not a demo: transport
keys, HUD, chapters, seek-to-timestamp and an auto-advancing queue in both modes.

Single static Go binary. **Zero third-party Go modules.**

```
vspz-yt-cli "lofi hip hop"                  # music mode (default)
vspz-yt-cli -M --viz radial "synthwave"     # music mode, radial spectrum
vspz-yt-cli -a "synthwave"                  # video mode
vspz-yt-cli -a -q 720 "synthwave"           # force a higher-resolution source
vspz-yt-cli -m "jazz"                       # muted; the visualizer still animates
```

### The two modes

| | Video (`-a`) | Music (`-M`, default) |
|---|---|---|
| Picture | ASCII or colour video | a visualizer |
| Children | ffmpeg (video) + mpv (audio) | mpv (audio) + a second ffmpeg for the spectrum tap |
| Clock | frame count, drift-corrected against mpv | mpv's own position, asked per frame |
| Extra keys | `s` toggles the spectrum strip | `v`/`V` style, `c` palette, `s` strip |

Every playback key works in both. The strip costs a row of video, so it is off by
default in video mode and on by default in music mode.

### Visualizers

`v` cycles the style, `c` the palette. Both carry over to the next track, so you
press them once per session rather than once per song.

| Style | What it draws |
|---|---|
| `bars` | log-spaced bands with peak-hold caps |
| `scope` | the raw waveform, decimated across the width |
| `mirror` | bands mirrored about an axis, kaleidoscope-folded when wide enough |
| `waterfall` | spectrum history scrolling down — song structure you can see |
| `radial` | spokes from a centre point, aspect-corrected |
| `particles` | dots launched on transients |

Palettes: `spectrum` (bass red → treble violet), `height`, `ocean`, `ember`,
`mono`. In mono every palette draws the same grey ramp, so `c` says so rather than
cycling invisibly.

The two expensive styles are capped rather than disabled: particles never exceed
400, and neither costs more on a 300x120 terminal than on an 80x24 one. A style
that only works on a big terminal is a style most people never see.

### Beat detection

Spectral flux with an adaptive threshold, feeding an onset envelope and a tempo
estimate. Both are honest about their limits: the tempo is a median of recent
intervals clamped to 60–180 and it is an *estimate*, and the envelope is what the
strobe and the particles draw rather than the raw onset flag, because a flag true
for one 93ms window is invisible at 30fps.

Two decisions in here were got wrong first and are worth knowing about before
changing either:

- The detector reads the analyser's **raw** magnitudes, not its smoothed output. A
  bar wants a slow release; an onset detector wants the opposite, and gets weaker
  every bar otherwise.
- The adaptive threshold is a **median**, not a mean. Onsets are rare, so a mean is
  dragged up by the very events it should detect, and on a periodic signal the
  mean *is* the event — nothing can cross `mean * 1.6`.

### Resolution follows your terminal

The ASCII grid and the source video resolution are both derived from your
terminal size (`TIOCGWINSZ`), so you never pay decode CPU for pixels you cannot
see:

| Terminal | Character grid | Source |
|---|---|---|
| 80x24 | 80 x ~21 | 360p |
| 120x40 | 120 x ~40 | 480p |
| 200x60 | 200 x ~57 | 720p |

Overrides, because auto-detection cannot be perfect:

```
-q 240|360|480|720|1080   hard cap/floor on source resolution
--aspect R                cell height/width ratio (default 2.0)
--cols N / --rows N       force the character grid
-c / --mono               force colour on, or off
```

`--aspect` exists because the 2:1 cell ratio is an assumption about your font,
not a measured value. Raise it if the image looks squashed, lower it if it
looks stretched.

## Features

- **Search** via `ytfzf -c yt -I J` under a built-in PTY, falling back to
  `yt-dlp ytsearch10` if the ytfzf scrape comes back empty.
- **Browse** in a dependency-free TUI: `j`/`k`/arrows, `g`/`G`, `1`-`9` jump.
- **A real player in both modes**: play/pause, ±10s/±1s/±60s seek, chapter
  navigation, `:1:30` jump-to-timestamp with a preview, volume, `n`/`p` through
  the queue, and auto-advance when a track ends.
- **Music mode** with a real 48-band spectrum — hand-rolled radix-2 FFT with a
  Hann window and log-spaced band edges (30 Hz → Nyquist) — behind six visualizers.
- **ASCII video** rendered from raw frames, with **A/V sync**.
- **Colour** via half-block cells (`▀`), auto-detected: 24-bit when the terminal
  advertises it, xterm-256 palette otherwise, monochrome if not. Two pixels per
  cell means colour *doubles* vertical resolution instead of costing it.
- **Diff-based rendering** that repaints only changed cells, which is what keeps
  large grids smooth. The visualizers paint through the same renderer, so they
  inherit its diffing instead of needing their own.
- **Tuned for low-end machines**: resolution, frame rate, particle count and
  visualizer rate all scale with your terminal and grid size.
- **Library mode** — point it at a directory and your own anime, series or music
  browse as a catalog, ordered by series/season/episode, with the queue
  auto-advancing. Audio containers (`.mp3`, `.flac`, `.m4a`, …) are recognised
  too. See below.

## Library mode

```
vspz-yt-cli --library ~/Videos/Anime
vspz-yt-cli -l ~/Videos/Anime --play          # start the first episode, no list
vspz-yt-cli -l ~/Videos/Anime --no-probe       # instant scan, durations shown as --:--
```

Filenames are parsed into series, season and episode, so a messy tree lands in a
watchable order instead of a lexical one:

| Filename | Becomes |
|---|---|
| `Cowboy Bebop S01E02 - Stray Dog Strut.mkv` | `S01E02` Cowboy Bebop — Stray Dog Strut |
| `Cowboy Bebop - 07 - Session 3 [SubsPlease].mkv` | `E07` Cowboy Bebop — Session 3 |
| `Show Name 01x05.mkv` | `S01E05` Show Name |
| `Show Name - 100.mkv` | `E100` Show Name |
| `Show Name (2019) - 03.mkv` | `E03` Show Name — the year is not an episode |
| `Random Home Video.mkv` | skipped, no episode marker |

Rules that matter:

- **Season comes from the directory** when the filename has none, so
  `Show/Season 2/Show - 01.mkv` files as `S02E01`.
- **Episode order is numeric.** A lexical sort plays episode 10 before episode 2.
- **A title year is never an episode number.** Four digits cannot match the
  one-to-three digit rule, so `Show (2019)` files as `Show`.
- **A file with no episode marker is skipped**, not placed arbitrarily.
- **An unreadable subdirectory is skipped**, not fatal.
- Episode 1 playing through advances the queue to episode 2 on its own.

Local files need no resolving: `ffmpeg` and `mpv` both open a path directly, so
`yt-dlp` is never invoked in library mode. `--no-probe` skips the `ffprobe` pass
that fills in durations — worth it on a large library over a network share.

`ffmpeg`, `ffprobe` and `mpv` are required for library mode. `yt-dlp` and `ytfzf`
are only needed for search.

## Requirements

External binaries, all in the Arch repos:

| Tool | Why |
|---|---|
| `yt-dlp` | search + resolving media URLs |
| `ffmpeg` / `ffprobe` | decode, scale, format conversion |
| `mpv` | audio output |
| `ytfzf` *(optional)* | preferred scraper |

## Install

```sh
git clone https://github.com/vspcoderz/vspz-yt-cli
cd vspz-yt-cli
go build -ldflags="-s -w" -o vspz-yt-cli .
```

## Keys

**Browse:** `j`/`k`/arrows move · `g`/`G` top/bottom · `1`-`9` jump · `enter` play
audio · `a` ASCII video · `q` quit

**ASCII playback:**

| Key | Action |
|---|---|
| `space` | pause / resume |
| `←` `→` | seek -10s / +10s |
| `,` `.` | seek -1s / +1s |
| `<` `>` | seek -60s / +60s |
| `[` `]` | previous / next chapter |
| `:` | jump to a timestamp |
| `n` `p` | next / previous result |
| `+` `-` | volume up / down |
| `q` `ctrl-c` | quit |

A track that finishes starts the next result on its own, so a search is a
playlist for as long as you want it. Playback stops at the end of the list
rather than wrapping — nothing in a search result set is ordered like a
playlist.

The bar shows position and duration; the top line shows the track and its
`04/19` place in the queue. After a seek the bar keeps a `┃` where playback was,
for a few seconds, so a jump across forty minutes and a nudge of one second do
not look the same. When a track has chapters, the one playing takes the hint row
once the hints have faded.

### `:` — jump to a timestamp

Ten seconds per keypress is 240 presses to reach the credits of a long video, and
no way at all to reach the part you can only describe as "around twelve minutes
in". `:` opens a field over the video:

```
 jump to █ 1:30
 → 1:30 / 3:20   ·   enter jump  ·   esc cancel
```

| You type | It means |
|---|---|
| `90` | 90 seconds |
| `1:30` | minutes:seconds |
| `1:02:03` | hours:minutes:seconds |
| `90s` `2m` `1h2m3s` | explicit units |
| `+30` `-1:30` | relative to where you are now |
| `50%` | that fraction of the track |

The second line previews where the jump will land, from the same parser the HUD
clock is built from, so what you type and what you see are the same format.
`enter` jumps, `esc` cancels, and `ctrl-u` clears the line. Everything typed while
the field is open is text — including `q`, `n` and `-`.

Fields are not range-checked: `1:90` is 150 seconds and `90:00` is 90 minutes,
because both are what the person typing them meant.


## Design notes

Why these choices, and what was rejected:

**Zero Go dependencies, deliberately.** The spectrum analyser would normally
pull in gonum or a DSP package; it is a radix-2 Cooley–Tukey FFT in ~80 lines
instead (`fft.go`). The PTY needed to drive `ytfzf`/`fzf` would normally pull in
`creack/pty`; it is two `ioctl`s (`pty.go`). Raw terminal mode would normally
pull in `x/term`; it is `TCGETS`/`TCSETS` directly (`raw.go`). Each dependency was
a handful of lines of syscall or arithmetic, and a zero-dependency static binary
was the better trade.

**A/V sync cannot be structural, so it is measured.** The obvious approach —
one `ffmpeg` emitting both a video pipe and an audio FIFO — deadlocks: ffmpeg
blocks on the second output while the first waits on a reader. So `ffmpeg` owns
video (`-re` paced) and `mpv` owns audio, on separate clocks: the system clock
versus the sound card's. Those drift apart over a long track no matter how
carefully they are started, so the player asks `mpv` for its real position over
its IPC socket every 2s and seeks the audio back onto the video when they
diverge by more than 250ms. Measured steady-state drift is ~70ms, below the
perceptual lip-sync threshold. Run with `VSPZ_YT_CLI_DEBUG_SYNC=1` to watch it.

Two protocol details cost real time here: `mpv` **creates and binds** its IPC
socket rather than connecting to it, and it sets `"error":"success"` on
*successful* replies — testing for a non-empty error field rejects every good
answer, which silently disabled the corrector entirely.

**Backgrounding the window suspends both children.** An unread terminal stops
accepting writes, so `ffmpeg` blocks; `mpv` writes straight to PipeWire and would
keep playing, and the streams would drift apart. `SIGSTOP` on both in the same
instant freezes both clocks — no desync, no respawn, and nothing painted over
whatever is on screen.

**Resolution, not a fixed size.** `ffmpeg` decodes every source frame regardless
of output size, so decode cost scales with source resolution. The grid, the
source height, and the frame rate are all derived from your terminal, so a small
window never pays for pixels it cannot show.

**mpv's stdout is discarded.** `mpv` prints a status line many times a second;
inheriting the terminal paints over every rendered frame. This cost a real
debugging session (0 frames visible).

## Known limitations

- **Transport keys are ASCII-only.** The spectrum/audio view still plays one
  track and exits; no pause, seek, or queue advance there.
- **Seek rebuilds both processes**, so it costs a frame or two of latency. Media
  URLs are resolved once per track and reused across seeks; they are re-resolved
  only if ffmpeg rejects one.
- ASCII is luminance-only: colour is lost, and dark or low-contrast video
  renders mostly blank. (`--mono` is the fallback path; colour is default.)
- The half-block glyph `▀` is East Asian Ambiguous width, so a terminal that
  renders it double-width will misalign the grid.
- Colour mode depends on the terminal: terminals without `COLORTERM=truecolor`
  get the 256-colour palette, and unknown terminals get no colour at all.
- Spectrum bands are derived from an FFT over the audio tap; they track the music
  but are not isolated per-bin equalisers.
- `ytfzf`'s scrape depends on a reachable path and can be flaky; `yt-dlp` is the
  fallback.

## License

MIT — see [LICENSE](LICENSE).
