# PLAN — pause freezes every clock, not just the audio

Status: done

Result: all three clocks SIGSTOP together, measured. Paused on a music-mode track
with the split pane up — old: `mpv=T ffmpeg=S ffmpeg=S`; new: `mpv=T ffmpeg=T
ffmpeg=T`. Presented bytes per frame while paused fell from 4470 (indistinguishable
from playing at 4377) to 157, the one large frame being the PAUSED banner repaint.

## The bug

Music mode, space bar: the sound stops, the visualizer keeps dancing, and if the
split pane is up the video keeps playing. Resume is then permanently out of sync
by the length of the pause.

`trackSession.SetPaused` (session.go:604) delegates to `SyncPlayer.pauseChildren`
(flow.go:53), which SIGSTOPs exactly two fields:

```go
if s.ff != nil     { s.ff.Process.Signal(syscall.SIGSTOP) }
if s.audio != nil  { s.audio.Process.Signal(syscall.SIGSTOP) }
```

Music mode has **three** children, and only one of them is in that struct:

| child | owner | stopped by pause? |
|---|---|---|
| mpv | `SyncPlayer.audio` | yes |
| the level tap's ffmpeg | `trackSession.tap` | **no** |
| the split pane's ffmpeg | `videoPane.live` | **no** |

So AGENTS.md's "pause is `SIGSTOP` on both children in the same instant" was true
of video mode and false of music mode, which has three. Nothing raised, nothing
logged — `ff` is legitimately nil in music mode, so the nil check did its job.

Three consequences, in increasing order of how much they matter:

1. The tap keeps running at `-re` pace, so `LevelTap.gen` keeps incrementing,
   `TryFrame` keeps returning true, and `viz.Push` keeps folding new analysis
   into the styles. The picture animates against a stopped sound.
2. `pumpLive` keeps draining the pane's pipe, so `Latest()` keeps changing and
   the split video plays on.
3. **The tap is now ahead of mpv by the length of the pause.** On resume mpv
   continues from where it was while the tap continues from where *it* was, and
   no correction can close that gap — `checkSync` compares mpv against the video
   *frame counter*, and in music mode there is no frame counter. This is the
   "does not sync correctly" part, and it accumulates: every pause adds its
   duration to a permanent offset.

The same bug is quieter in video mode with `s`: the strip has its own tap
(`session.go:359`, same `s.tap` field), so it keeps scrolling under a frozen
picture.

## Secondary

`pumpMusic` calls `p.pollPosition(musicPollTimeout)` on every tick. A SIGSTOPped
mpv does not service its IPC socket, so each frame costs a full 60ms blocking
read — the pump ticks at 16/s of stalled work for as long as the pause lasts.
`checkSync` already returns early on `s.paused` and `SetVolume` already defers
while paused; the pump was the third place that forgot.

## The fix

Freeze the whole set, not a subset of it. Freezing is not a UI choice here: the
tap is a *clock*, and a clock that keeps running while the one it is drawn from
stops is the desync.

| file | change |
|---|---|
| `src/visual.go` | `LevelTap.SetPaused(bool)` — SIGSTOP/SIGCONT the tap's ffmpeg |
| `src/split.go` | `videoPane.SetPaused(bool)` — SIGSTOP/SIGCONT the pane's ffmpeg |
| `src/session.go` | `SetPaused` signals all three; `pollPosition` skips a frozen mpv |
| `src/flow.go`, `src/sync.go` | `SyncPlayer.paused` → `atomic.Bool` |
| `src/pause_test.go` | new |

### `SyncPlayer.paused` becomes atomic

`pauseChildren`/`resumeChildren` write it from the render loop; `pollPosition`
would now read it from `pumpMusic`, a different goroutine. `checkSync` reads it
from the render loop today, so it is currently race-free and the new reader
would not be. `atomic.Bool` rather than a mutex: it is one flag, set on a
keypress and read on a frame.

### `pollPosition` guards itself

The check goes where the socket is, not in the caller, for the same reason
`checkSync` guards itself: every caller of a paused player's socket wants the
same answer, and there are three of them now.

```go
if s.ipc == nil || s.paused.Load() {
    return s.clock.now(), false
}
```

Returning `false` rather than a value keeps the caller's existing "a failed read
is harmless, the clock holds" path.

### Ordering inside `SetPaused`

`cur` first, then the tap, then the pane — not because the order is load-bearing
(the signals land microseconds apart) but so the reading of the code matches the
reading of the rule: the audio is the reference clock, freeze it, then freeze
everything drawn against it. Stated in the comment so nobody "fixes" the order.

The rebuild path needs no change: `session.go:1481` already re-applies
`SetPaused(true)` after a seek or resize, and the new tap and pane are reached
through `s.tap`/`s.pane` at call time.

## Tests

No ffmpeg, no mpv, no network — the three seams this bug lives in are reachable
without any of them.

- `TestPauseReachesEveryChildClock` — `trackSession` with a `SyncPlayer`, a
  `LevelTap` and a `videoPane`; `SetPaused(true)` must SIGSTOP all three and
  `SetPaused(false)` must SIGCONT all three. **Delete the tap and pane calls from
  `SetPaused` and this goes red**, which is the whole point.
- `TestLevelTapSetPaused` / `TestVideoPaneSetPaused` — the signal itself, plus
  nil-tap and closed-pane cases (a `LevelTap` with no process, a pane after
  `Close`) must not panic. `Close` already SIGCONTs before killing for exactly
  this reason and that invariant gets pinned here.
- `TestPollPositionSkipsAFrozenPlayer` — a paused player with a socket that would
  otherwise answer must return `(held, false)` without reading it.

Signals go through a `sig func(os.Signal) error` field defaulted in the
constructors, so the tests observe the freeze instead of spawning ffmpeg. Same
reason `player_test.go` has a `fakeMedia`.

## Verification

```
go vet ./... && go test ./... && go test -count=1 -race ./src
go build -ldflags="-s -w" -o rasterbar ./src
```

All pass; `gofmt -l src/` clean. (`go test ./...` now takes ~35s, not the ~0.6s
AGENTS.md claims — `TestVideoTapFilterByteCountAcrossGeometries` shells out to
ffmpeg across 13 geometries. Pre-existing, not from this change.)

The planted-defect check: deleting the two `SetPaused` calls from
`trackSession.SetPaused` makes `TestPauseReachesEveryChildClock` **and**
`TestVideoModeStripTapPausesToo` fail, against binaries built from both the fixed
and the reverted source.

### Driven, in a pty, one at a time

Bytes presented per frame, DEC 2026 honoured the way Ghostty does
(`/tmp/opencode/freezeframes.py`), music mode on a 60s fixture, pause at 4s held
to 10s:

| | playing 1–4s | **paused 4–10s** | resumed 10–14s |
|---|---|---|---|
| old | 4377 B/frame | **4470 B/frame** | 4280 |
| new | 5156 B/frame | **157 B/frame** | 4511 |

The old build paints as much while paused as while playing, which is the bug
stated as a number. The new build emits one repaint — the PAUSED banner — and
then only what the diff renderer cannot elide.

Process states from `/proc/<pid>/stat`, music mode with the split pane open:

```
                old                          new
split open   ffmpeg=S ffmpeg=S mpv=S     ffmpeg=S ffmpeg=S mpv=S
PAUSED       ffmpeg=S ffmpeg=S mpv=T     ffmpeg=T ffmpeg=T mpv=T
PAUSED +2s   ffmpeg=S ffmpeg=S mpv=T     ffmpeg=T ffmpeg=T mpv=T
resumed      ffmpeg=S ffmpeg=S mpv=S     ffmpeg=S ffmpeg=S mpv=S
```

Also driven, all clean:

- **`q` while paused with the split open** — all three `T`, no leaked ffmpeg or
  mpv afterwards. SIGKILL works on a stopped process; `LevelTap.Close`'s
  SIGCONT-before-Kill is belt and braces here.
- **Seek while paused** (`:1:20⏎`) and **resize while paused** — every child
  stays `T` across the rebuild, which is the `sess.dirty` path re-applying
  `SetPaused` to freshly started children.
- **Video mode with `s`** — while paused the loop emits nothing at all, because
  the video pipe is frozen and there is no frame to drive a HUD repaint. The
  strip's tap was still running behind that; now it is not.

