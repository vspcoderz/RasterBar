# Phase 6 — jump-to-time, fine seek, chapters, pixel leak

## Goal

`←`/`→` move 10s. On a 40-minute video that is 240 keypresses to reach the
credits, and there is no way to reach a position you can only describe as
"around 12 minutes in" — the one you actually want when a video has a
chapters list. This phase adds a `:` prompt that takes a timestamp, plus the
seek granularity and chapter navigation that makes it usable.

Everything here lands in ASCII playback. The spectrum view still has no
transport at all (see "Not done" above) and is not touched.

## The enabler: the key reader must stop decoding

`readKeys` currently decodes bytes into `Cmd` **in its own goroutine**
(`player.go:408`). A text prompt is impossible on top of that: the reader has no
idea a prompt is open, so the bytes of `1:30\r` get decoded as transport keys.
`q` would quit mid-prompt, `-30` would change the volume three times, and the
fix cannot be "tell the reader a prompt is open" — by the time the reader learns,
the rest of the burst is already queued.

So: `readKeys` becomes a **byte pump** (`chan []byte`) and decoding moves into
the render loop, where prompt state is a plain local. One decoder, one owner of
what the bytes mean, no ordering race. Touches four call sites: `playOpts.keys`,
`playTrack`, `awaitDrain` (`flow.go:79`), `playQueue` (`main.go:312`).

`decodeStream` is refactored into `decodeOne(pending) (Cmd, used int, ok bool)`
plus a loop over it, so the six existing decode tests keep testing the same
thing.

## Approach

| Piece | Where | Notes |
|---|---|---|
| Byte pump | `player.go` | emits `nil` on an empty read — that is the VTIME tick, and it is what flushes a stale partial escape sequence |
| `decodeOne` | `player.go` | one command from the front of `pending`; `ok=false` means "wait for more bytes" |
| `parseTimestamp` | `hud.go` next to `formatClock` | pure; the inverse of `formatDuration`, so it reads the same as the HUD clock |
| `prompt` | `player.go` | byte-at-a-time consumption, returns how much of a chunk it ate and what the user did |
| `Overlay` | `render.go` + `color.go` | both renderers, plus one shared centring helper |
| `Player.JumpTo` | `player.go` | transport rule with the clamp, tested against `fakeMedia` |
| `Player.MarkClear` | `player.go` | the ghost marker's lifetime |
| Chapters | `ascii.go` (`ytInfo`), `library.go` (ffprobe), `player.go` | one extra JSON field, no extra request |

### Timestamp grammar

Accepted, all in one function:

```
90          bare seconds
1:30        minutes:seconds      1:02:03   hours:minutes:seconds
90s 2m 1h2m3s                  explicit units
+30 -1:30                      relative to the current position
50%          fraction of the duration
```

Sign applies to the whole value. Bare `50` is 50 seconds — unambiguous, since
no other reading of a lone number is plausible.

### Overlay, and the stale-cell trap it walks into

The prompt is reverse video, centred, on the **last grid row plus the row above
it**: the input line, and a second line showing the resolved target
(`→ 0:01:30 / 3:20`) or why it is not yet valid.

The trap: the overlay paints over cells the diff renderers think they already
drew. When the prompt closes, those cells are *unchanged* in the diff cache, so
**the next frame skips them and the prompt stays on screen forever**. The fix is
one `ForceNext()` on close, which costs exactly one full repaint. This is the
same class of bug as the black-cell one in `ba35b10`.

The colour renderer additionally tracks cursor position and last-emitted SGR
(`haveLast`/`lastX`/`lastFG`). An overlay write invalidates all of it, so the
colour path clears those after painting — otherwise the next frame assumes the
cursor is where the overlay left it and the grid shears.

### Ghost marker

`progressBar` grows a marked variant: the cell before the seek is drawn as `┃`.
Fades after `markLinger`, on the same clock as the hint fade.

### Chapters

`yt-dlp -J` already returns `chapters`; reading it costs zero extra requests.
Local files need `ffprobe -show_chapters`, folded into the **existing** probe
call rather than added as a second one — a 2000-file library scan cannot afford
to double its subprocess count. `[`/`]` for prev/next.

Prev is the usual rule: the start of the current chapter if you are more than a
second into it, otherwise the previous chapter's start, otherwise 0.

## Files touched

| File | Change |
|---|---|
| `player.go` | byte pump, `decodeOne`, `parseTimestamp`, `prompt`, `JumpTo`, chapter + fine-seek commands, marker state |
| `session.go` | prompt lifecycle in the render loop, overlay calls, channel type |
| `hud.go` | `parseTimestamp`, marked progress bar, chapter in the footer |
| `render.go` | `Overlay` on `DiffRenderer` + shared centring |
| `color.go` | `Overlay` on `ColorDiffRenderer`, cursor/SGR invalidation |
| `ascii.go` | `ytInfo.Chapters`, `Chapter` type |
| `library.go` | one ffprobe call returning duration **and** chapters |
| `flow.go` | `awaitDrain` takes the byte channel |
| `main.go` | byte channel, usage text |
| `main_test.go` | timestamp grammar, prompt state machine, `JumpTo`, chapters, fine seek |
| `screen_test.go` | terminal emulator; leak regression + overlay-does-not-leak |

## Bugs found while building this

1. **The transport error line has no SGR reset** (`session.go:472`). It is
   written with whatever colour the last video cell set. Real, found by
   reading, fixed with a `\x1b[0m` and a test.
2. **Pixel leak — cause not yet identified.** Two hypotheses measured and
   eliminated, both now regression tests:
   - *Stale cell from the diff renderer.* Eliminated. Built a terminal emulator
     (`screen_test.go`) that reconstructs the screen and compares it to the
     frame handed in, over high-contrast moving edges, with the HUD interleaved
     between frames exactly as the render loop does it, plus the stall-recovery
     `ForceNext`. Clean in both mono and colour, both glyph modes, both 24-bit
     and 256.
   - *`unsharp` ringing on hard edges.* Eliminated. Measured: ffmpeg's
     `scale=flags=area,unsharp=5:5:0.7:5:5:0.0` on a synthetic black/white edge
     produces no overshoot — no threshold means no halo. Pixel-for-pixel
     identical output with and without the filter on that input.

   What is left is not a stale cell, so it needs the symptom described before it
   gets a fix. Guessing here would be worse than asking.

## Verification

1. `go vet ./...`
2. `go test ./...`
3. `go build -ldflags="-s -w" -o vspz-yt-cli .`
4. Real pty, real track: `:` opens, `1:30` previews, Enter lands where the
   preview said, Esc cancels and leaves **no** overlay behind, `,`/`.` nudge one
   second, `<`/`>` jump a minute, `[`/`]` walk chapters, `q` typed inside a
   prompt is text and not a quit.

## Status

in progress
