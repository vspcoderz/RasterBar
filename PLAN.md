# YtCLI — ASCII/music TUI for YouTube

## Goal

A single static Go binary. Search YouTube via `ytfzf -c yt`, browse results in a
custom TUI, play audio. Audio-first with a visualizer; ASCII video is a toggle.

## Verified constraints (tested on this machine, 2026-10-01)

| Fact | Evidence |
|---|---|
| `ytfzf -c yt` is the working scraper | prints "Scaping Youtube", fzf launches under PTY |
| ytfzf needs a TTY | non-interactive shell → `inappropriate ioctl for device` |
| `-I J` returns JSON | `ytfzf -I J` dumps video JSON instead of opening fzf |
| `mpv` streams YouTube | exit 0, opus 48000 Hz |
| `mpv --vo=aa` NOT available | `Video output aa not found!` — not compiled in |
| ffmpeg CANNOT open YouTube URLs | `Invalid data found when processing input` |
| yt-dlp must resolve URL first | `yt-dlp -f 136 -g` → googlevideo URL |
| ASCII pipe works | 106704/106704 bytes, 0 ffmpeg errors |
| Format IDs 130-399 only | no progressive `18`; `worst` selector fails |

Dead ends (do not retry): `mpv --vo=aa`, ffmpeg-on-YouTube-URL, `yt-dlp -f worst`.

## Architecture

```
ytfzf -c yt -I J "query"      → JSON results (fzf never opens)
        ↓
Go TUI                       → list, selection, keys
        ↓
yt-dlp -f <id> -g URL        → direct media URL
        ↓
ffmpeg → gray rawvideo pipe  → bytes → char ramp → stdout
```

`ffmpeg` cannot take a YouTube URL. Always resolve through yt-dlp first.

## Dependencies

Go standard library only. No bubbletea/lipgloss/image libs.
Rationale: ASCII rendering is a byte→char map, not an imaging problem. Raw-mode
stdin + ANSI escapes is a few hundred lines and one static binary. Rejected:
bubbletea (state machine for a 2-pane list), any image lib (we never decode an
image, only read gray bytes).

External binaries (all already installed, not Go deps):
`ytfzf`, `yt-dlp`, `ffmpeg`, `mpv`.

## Files

- `main.go` — arg parsing, mode dispatch
- `search.go` — ytfzf JSON invocation + parsing
- `tui.go` — list view, raw-mode stdin, key handling
- `play.go` — yt-dlp resolve, audio playback, visualizer
- `ascii.go` — ffmpeg gray pipe → char ramp

## Behaviour

- Audio is the default and always plays.
- ASCII video is opt-in via key toggle — it burns CPU and destroys color.
- ASCII maps luminance to a ramp; aspect corrected 2:1 for terminal cells.
- Dark video renders mostly blank. Ramp is expected to look poor on dark
  sources; that is a property of luminance-only ASCII, not a bug.

## Verification

1. `ytfzf -c yt -I J` returns parseable JSON for a real query
2. TUI list renders and responds to keys
3. Audio plays through mpv/ffmpeg
4. ASCII toggle produces non-blank frames
5. `go build` produces a static binary

## Status

in progress
