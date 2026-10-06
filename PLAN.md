# PLAN — video pane in music mode

Status: done

Delivered: `W` toggles a video pane beside the visualiser in music mode (off by
default), `a`/`d` set the side, `T` swaps the still thumbnail for live video,
`{`/`}` move the divider (clamped to a quarter either side). One renderer, two
composited panes.

## What the design turned out to require

Compositing in Go rather than running a renderer per pane. Two renderers would
break the colour renderer's SGR elision — it assumes nothing else changed the
terminal's colour state since its last cell, and pane B repainting in between
makes that false. It would produce wrong colours and no error.

## Bugs found and fixed while building it

- **Half-block doubling applied in mono.** `paneFrameBytes` doubled for GlyphHalf
  even with colour off, where ffmpeg sends one byte per cell. Caught by
  `TestVideoTapGeometryMatchesPaneFrames`, which compares the bytes ffmpeg is
  asked for against the bytes the compositor copies.
- **The divider keys did nothing.** `divider` started at 0, and from 0 both
  `+1` and `-1` clamp to the same quarter-width floor. Worse, the state lived as
  locals inside `playTrack`, so no test could reach it. Extracted `splitState`,
  which is why `TestSplitDividerMovesInBothDirections` exists now.
- **The default split was 25%, not even.** `initDivider` now starts at half.
- **`divider` meant two different things** depending on which side the video was
  on. It is now the width of the left pane, so the second pane always starts at
  exactly `divider`.
- Three copies of the `yt-dlp -J` exec-plus-unmarshal became one `ytdlpInfo`.

## Verified

`gofmt -l src/` clean, `go vet` clean, `go test ./...` ok, `go test -race ./...`
ok, release build ok. 14 new subtests, including the no-stale-cells suite for
two panes and the case where moving the divider abandons cells.

Manually, in a real pty: both panes draw, **0 colours shared** between the pane
regions (no bleed), zero scroll events. The exact boundary column from that ad-hoc
harness is not quoted because the heuristic that found it is too rough to trust;
the geometry is pinned deterministically by unit tests instead.

## Goal

Music mode optionally shows video in a second pane beside the visualiser.
Off by default. Keys: `W` toggle, `a`/`d` video left/right, `T` still/live,
`{`/`}` nudge the divider.

## Measured, not assumed

Everything below was checked against the real tools before planning.

- `ffmpeg -i <https thumbnail url> -vf scale=W:H:flags=area,format=gray -frames:v 1
  -pix_fmt gray -f rawvideo -` emits **exactly W*H** bytes; rgb24 emits **W*H*3**.
  Both verified. So a thumbnail is *one frame from the existing filter chain,
  held static* — no `image/jpeg`, no new import, zero-dep constraint intact.
- ffmpeg reads an https image URL directly (it is a direct media URL, so the
  dead-end "ffmpeg on a youtube.com URL" does not apply).
- yt-dlp hands back a **`.webp`** thumbnail, and this ffmpeg decodes webp
  (verified: correct byte count). `search.go` already has a
  `Thumbnails []struct{URL,Width,Height}` to pick the best size from.
- `thumbnails` rides along in the `-J` response music mode already makes, so the
  thumbnail URL costs **zero extra requests**.

## Cost the user should know

- One extra ffmpeg process while the pane is on, and roughly half the cells to
  fill per frame. Hence off by default.
- The video URL needs **its own grant** — one URL, one consumer. Resolved
  **lazily on the first `W`**, so default music mode stays at exactly two
  `yt-dlp -J` calls and does not get slower.

## Design

**Reuse, do not rewrite.** `startVideoTap` already does ffmpeg → rawvideo at an
arbitrary cols×rows with the right filter, so the live pane *is* a `videoTap`
built at pane geometry. The thumbnail is the same thing with `-frames:v 1` and no
`-re`.

The pane is therefore "a frame buffer whose source is a static file instead of
the pipe", which is already the shape both painters speak. That is why this is
much smaller than it looks.

### Files

- `src/split.go` (new) — `splitLayout` (two pane rects + divider), thumbnail
  and live pane producers, divider clamping.
- `src/session.go` — lazy video resolve, pane lifecycle alongside the existing
  per-generation `start`/`stopDecoder`, a `paneFrames` channel in the music
  loop's select, the five new key cases.
- `src/player.go` — `CmdSplit`, `CmdPaneLeft`, `CmdPaneRight`, `CmdThumb`,
  `CmdDividerL`, `CmdDividerR` + key table entries.
- `src/color.go`, `src/render.go` — **pane interleave invalidation** (below).
- `src/ascii.go` — `mediaPair.thumbURL`; capture from the existing `-J`.

### The architectural crux: two grids, one screen

`FrameRenderer` already emits absolute CUP per run, so two renderer instances
(one per pane) can write to disjoint screen regions with independent diff
caches. The trap is SGR state:

> Renderer A's dedup keys off "colour unchanged since my last cell". Pane B
> writes between A's frames and changes the terminal's actual SGR. Next frame A
> skips an SGR that is no longer active.

Fix: a pane must invalidate its SGR dedup state at the start of each of its
Draws, since it cannot know what the other pane did in between. Small explicit
method rather than making the dedup global.

## Risk I want on the record

`screen_test.go`'s emulator is the only thing that catches a stale cell, and the
repo already records that **a test double kinder than reality agrees with your
bug**. Two renderers interleaving into one screen is exactly the case where a
too-forgiving emulator hides damage. So:

- Extend the no-stale-cells suite to drive two panes over moving content with
  the real interleave order.
- Verify by running the real thing, not only the tests — per AGENTS.md, SIGSTOP
  pause, live resize and anything writing to a terminal are not unit tested.

## Files touched

new `src/split.go`, `src/split_test.go`; edits to `session.go`, `player.go`,
`color.go`, `render.go`, `ascii.go`, `screen_test.go`, `hud.go`;
`AGENTS.md` rules; `POSTMORTEMS.md` if anything new breaks.

## Verification

- `go vet ./... && go test ./... && go test -race ./... && go build -ldflags="-s -w"`
- New tests: split layout + divider clamping; the six new keys in `keys_test.go`;
  two-pane no-stale-cells in `screen_test.go`.
- Benchmark before/after: the pane must not regress `BenchmarkColorDrawWorstCase`.
- Manual: music mode `W`, `a`/`d`, `T`, `{`/`}`, live SIGWINCH while split,
  and a track change while the pane is on.

## Open decisions I'd flag if you disagree

- Default side: left (video left, visualiser right).
- Divider range: 25%..75% of terminal width, so neither pane can collapse.
- Thumbnail before the video stream exists: allowed, it needs no stream at all —
  which means `T` can work even if video resolution fails.