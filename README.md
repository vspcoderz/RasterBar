<div align="center">

# vspz-yt-cli

**A YouTube player that lives in your terminal.**
ASCII video, or a music visualiser with ten gradient palettes — in one static
binary with **zero third-party Go modules**.

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

## What it is

You point it at a search, a URL, or a folder. It plays the audio through `mpv`
and paints the picture itself, straight into the terminal's cell grid — colour,
half-blocks, diff-rendered so only the cells that changed get written.

Two modes, one player:

| | |
|---|---|
| **`-M` music mode** *(default)* | no video. Six audio-reacting visualisers, ten palettes, beat detection. |
| **`-a` video mode** | ASCII or truecolour video, with the spectrum strip under the picture. |

## Visualisers

`v` and `V` walk them; `--viz NAME` picks one at startup.

| name | what it does |
|---|---|
| `bars` | the classic. Bars grow from the baseline, peak caps hold the top. |
| `scope` | a waveform trace, aspect-corrected so it's a line and not a smear. |
| `mirror` | mirrored around the centreline — the wings in the screenshots. |
| `waterfall` | a scrolling spectrogram. Newest row on top, fading with age. |
| `radial` | one spoke per frequency band, fanning out from the centre. |
| `particles` | dots launched on transients and integrated with momentum. |

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

```sh
go install github.com/vspcoderz/vspz-yt-cli@latest
```

Or build it yourself:

```sh
git clone https://github.com/vspcoderz/vspz-yt-cli
cd vspz-yt-cli
go build -ldflags="-s -w" -o vspz-yt-cli .
```

**Requires** `ffmpeg` and `mpv` on `$PATH`. Searching YouTube additionally wants
`yt-dlp` and `ytfzf`.

## Use

```sh
vspz-yt-cli "lofi hip hop radio"     # search and play
vspz-yt-cli https://youtu.be/...    # a URL
vspz-yt-cli -l ~/Music -M            # browse a folder as a visualiser
vspz-yt-cli -l ~/Music --play       # start playing immediately
vspz-yt-cli -a -c "tesseract"        # ASCII/colour video mode
```

### Keys while playing

| key | |
|---|---|
| `space` | pause / resume |
| `←` `→` | seek ∓10s |
| `,` `.` | seek ∓1s |
| `<` `>` | seek ∓60s |
| `[` `]` | previous / next chapter |
| `:` | jump to a timestamp |
| `n` `p` | next / previous result |
| `+` `-` | volume |
| `v` `V` | next / previous visualiser |
| `c` | next palette |
| `1`–`9` `0` | pick a palette directly |
| `s` | toggle the spectrum strip |
| `q` | quit |

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

## License

MIT — see [LICENSE](LICENSE).