# vspz-yt-cli

A terminal YouTube player: search with `ytfzf`, browse results in a custom TUI,
play audio with a live FFT spectrum, or play video as ASCII art — with the ASCII
locked in sync to the music.

Single static Go binary. **Zero third-party Go modules.**

```
vspz-yt-cli "lofi hip hop"
vspz-yt-cli -m "jazz"      # muted, spectrum still animates
vspz-yt-cli -a "synthwave" # start in ASCII mode
vspz-yt-cli -a -q 720 "synthwave"   # force a higher-resolution source
```

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
- **Audio playback** with a real 48-band spectrum — hand-rolled radix-2 FFT with
  a Hann window and log-spaced band edges (30 Hz → Nyquist).
- **ASCII video** rendered from raw frames, with **A/V sync**.
- **Colour** via half-block cells (`▀`), auto-detected: 24-bit when the terminal
  advertises it, xterm-256 palette otherwise, monochrome if not. Two pixels per
  cell means colour *doubles* vertical resolution instead of costing it.
- **Diff-based rendering** that repaints only changed cells, which is what keeps
  large grids smooth.
- **Tuned for low-end machines**: resolution and frame rate both scale with your
  terminal and grid size.

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

**Playback:** `q` or `ctrl-c` stop

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
