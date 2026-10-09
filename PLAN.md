# PLAN — file support, renderer fidelity, and the crashes under them

Status: done

Delivered: three crash-class fixes (a panic in the key router, a data race on the
mpv socket, a panic waiting in `fdWritable`), `-l` working for music folders,
direct file / glob / playlist arguments, and the renderer letterboxing video
instead of stretching it. Plus: Ctrl-C restores the terminal, synchronized
output, and the strip decayed at analysis rate.

## What was broken, and how each was proved

Nothing below was inferred from reading. Every claim was reproduced first.

| bug | reproduction |
|---|---|
| split arrow key through the `:` prompt **panicked** | a test driving `keyRouter.feed` with `"\x1b["` then `"C"` → `panic: slice bounds out of range [3:1]` at `player.go:806` |
| `mpvIPC` raced in music mode | `go test -race` with a real unix socket and two goroutines → `DATA RACE`, write at `ipc.go:246` vs read at `ipc.go:115` |
| `fdWritable` could panic | `len(syscall.FdSet.Bits) == 16` → 1024 max fd; the guard allowed 4096 |
| `-l` on a music folder found nothing | `rasterbar -l ~/Projects/music/songs` → `no playable files with an episode number` |
| an audio file in video mode killed playback | pty run on an opus file → `Stream map '' matches no streams` ×3, then `cannot play E01 Long: read first frame: EOF` |
| a file path went to YouTube search | `rasterbar ~/…/celestial.wav` → `searching: …`, 10 unrelated results |
| video was squashed by ~43% | a square in a 16:9 frame rendered 0.57:1 through `scale=100:50`; 1.00:1 through `scale=100:28` |
| the letterbox filter broke the split pane | pty run, `W` then `{` → `video pane: split pane: read first frame: EOF`. **This was a regression I introduced**, caught only by driving the real thing |
| Ctrl-C left the tty broken | pty run, `\x03` mid-playback → no `\x1b[?25h` and no `\x1b[2J` in the remaining output |
| the peak caps never drew | `peak - level` measured peaking at 0.317 against a threshold of 0.5 |

Each fix has a test that fails without it. Verified by planting the defect: the
router panic and the `fdWritable` panic both reproduce on a reverted edit, and
`-race` fires on the reverted IPC lock.

## Part 1 — the crashes

**`keyRouter.feed` index panic.** `prompt.consume` prepends a held `escTail` to
the chunk and returned `len(chunk)` of the *combined* slice; the caller applied
that count to the un-combined `r.buf`. `ESC [` arriving alone held a 2-byte
`escTail` and returned 2 against a 2-byte buffer — fine. The next read saw
`"\x1b[C"` (3 bytes from a 1-byte buffer) and returned 3. Fixed by counting
against the caller's slice, with a clamp in `feed` as a second guard: this
slicing happens with children running, and the cost of a wrong count is a panic
rather than a dropped key.

**`mpvIPC` had no mutex.** Two goroutines incremented `next` (a lost update hands
one `request_id` to two calls, and each reads the other's reply) and two read one
`bufio.Reader`. The lock is held across the round trip; there was only ever one
caller wanting the socket at a time, so it costs no contention. `seek` also
stopped returning nil on a read error, which had `SyncReport.Corrected` reporting
a correction that never happened.

**`fdWritable` indexed past `FdSet`.** The guard read `64*64`; the bitmap holds
1024. Now `len(set.Bits)*64`, and the test opens real descriptors until it is
past the bitmap — the only honest way to reach the dead zone.

**Ctrl-C.** Raw mode here clears `ECHO` and `ICANON` but not `ISIG`, so `\x03`
is delivered as SIGINT — which makes the `0x03` case in `cmdForByte` unreachable
and kills the process with the tty still raw and the children unreaped. Now
`playTrack` returns `OutcomeQuit`. Verified in a pty: `echo` and `icanon` are
back to `True` afterwards.

**`LevelTap.Close` closed the pipe before killing ffmpeg** — the exact ordering
AGENTS.md forbids for the other two children — and hung on every seek, resize and
track advance.

**`cannot play …` was erased by the deferred screen clear**, microseconds after
being printed. `restoreTerminal` is now both deferred and callable, because a
deferred print runs *before* an earlier deferred restore (LIFO).

## Part 2 — `-l` and files

**Unnumbered files are no longer dropped.** `classifyMedia` required an episode
marker, so `nocturne.opus` produced nothing. Every extension was already in
`mediaExts`; the parser was the gate, and it is a rule about television. They are
included now, ordered by `naturalLess` on the path, which is what puts `track 2`
before `track 10`. Numbered files keep their series/season/episode order exactly.

**Audio-only files no longer kill playback.** `probeMedia` learned `HasVideo`.
`newTrackSession` probes a local file once and falls back to music mode when there
is no video stream — falling back rather than refusing, because "no picture" is
not a mode the user can act on. `-l DIR --play` also stopped defaulting to video
for an explicit file list. `silent` stays fatal in music mode; that is the mirror
case and the spectrum is the content there.

**Files, globs and playlists.** `parseArgs` collects positional arguments into
`o.args` and `main` decides, because deciding needs the filesystem. An argument
is a file when it exists, has a wildcard, or names a playlist; anything else is a
YouTube search, which is what every argument was before. `.m3u`/`.m3u8` resolve
entries against the playlist's own directory, skip URLs, strip a BOM, and follow
nested playlists to a depth of 2 so a self-referencing file terminates.

**`probeMedia` reports failures.** It used to fold ffprobe's error into
`hasAudio == false`, so a corrupt download became "no audio stream in <title>".

## Part 3 — renderer

**Letterboxed.** `scale=W:H:force_original_aspect_ratio=decrease,setsar=1,
format=X,pad=W:H`. The measured 43% stretch is gone and the frame is still
exactly `cols*rows` bytes, so the compositor contract and every existing geometry
test are untouched.

Getting there took three attempts, and the first two are the interesting part:

1. `scale,pad` — the frame came out the right *shape* on a PGM and the wrong
   size everywhere else, and only at one geometry.
2. `scale,setsar=1,pad` — still failing. The fit leaves `sar 64/63`, so pad
   compares display dimensions (49 × 64/63 = 49.78 → 50) and refuses to pad a
   50-wide image into 49 columns.
3. `scale,setsar=1,format,pad` — correct everywhere.

The reason it survived the first round of testing is the part worth keeping:
**the test used one geometry, and it was the one that worked.** 100×50 is the
geometry the video grid actually gets, which is exactly why it behaved and every
other shape did not. `TestVideoTapFilterByteCountAcrossGeometries` now sweeps 13
shapes × 4 colour/glyph modes and demands an exact byte count from each.

And the test *still* passed against the broken filter after that, because the
fixture encoded as `yuv444p` — which pad accepts — while every real source is
`yuv420p`, which it refuses. A fixture nobody can produce is a fixture that
agrees with your bug. The fixture now pins `-pix_fmt yuv420p`, and reverting the
filter order makes the test fail on 9 of 13 geometries.

**Synchronized output.** `\x1b[?2026h` / `\x1b[?2026l` around the session.
Unconditional: an unknown private mode is ignored, and the terminals that
implement it are the ones fast enough to tear.

**The colour path got `unsharp`.** The mono branch has had it all along; the
colour branch had none, and nothing caught the asymmetry.

**`appendCUP` in the colour per-cell path.** `fmt.Fprintf` per changed cell, which
AGENTS.md forbids; `render.go` already had the scratch that does it. Measured 0
allocs either way, so this is consistency and headroom, not a leak.

## Smaller things, in the same commit

- **The strip now decays at analysis rate.** `paintHUD` runs on every keypress as
  well as the 10 Hz tick, so its `Push` ran the strip ~3× faster than the music
  when you mashed keys. Uses `TryFrameAt` with its own cursor, because the
  visualiser and the strip are two consumers of one tap and a shared `readGen`
  makes one of them starve.
- **Peak caps draw.** `peak > level+0.5` sat above the ~0.36 ceiling the decay
  rates can produce. Now `peakCapGap = 0.12`, with a test that measures the
  ceiling rather than trusting the comment.
- **`heavy` styles are actually capped.** All six styles implemented
  `Heavy`/`CapScale` and nothing called them, so `usage`'s claim described an
  intention. The pump reads the cap each tick rather than baking it into the
  ticker, because a ticker cannot change rate and `v` must work without
  restarting mpv.
- **`s` in music mode** recreates the renderer and recomputes from the live
  window. It did neither: the renderer kept its old row count, so a later smaller
  frame failed `frame too small` → `OutcomeError` → silent exit, and the layout
  was computed from the terminal size captured at entry, so a toggle after a
  resize snapped the grid back.
- **`startThumbnail` bounds its `Wait`** by `childReapTimeout`, like every other
  child. It was the only unbounded one, on the render-loop goroutine, so a hung
  fetch took `q` with it.
- **Dead code removed**: the entire `resolveURL`/`resolveVideoURL`/
  `resolveAudioURL`/`runYtdlp` `-g` path (unreachable), and `vizPixPerCell` (a
  copy of `perCellFor` — the two-copies-of-one-rule hazard the rulebook calls out).
- **`usage` fixed in three places** where it described something other than what
  the code does.

## Files

| file | change |
|---|---|
| `src/player.go` | `consume`'s `used` accounting, clamped in `feed` |
| `src/ipc.go` | mutex; `seek` no longer reports a correction that failed |
| `src/flow.go` | `FdSet` bound |
| `src/session.go` | signals, error visibility, DEC 2026, strip rate, renderer rebuild, `vizCap` |
| `src/visual.go` | `Close` ordering, `Dead`, `TryFrameAt` |
| `src/library.go` | unnumbered files, `mediaInfo`, `parseProbeOutput`, probe errors |
| `src/ascii.go` | `hasVideo` → `mediaPair.videoLess`; dead `-g` path removed |
| `src/main.go` | file/glob/m3u args, `--play` default, `usage` fixes |
| `src/hud.go` | `peakCapGap` |
| `src/split.go` | letterbox, colour `unsharp`, bounded thumbnail reap |
| `src/color.go` | `appendCUP` |
| `src/playlist.go`, `src/naturalsort.go` | new |
| `src/ipcrace_test.go`, `src/letterbox_test.go`, `src/librarysort_test.go`, `src/flow_test.go` | new |

## Verification

`go vet ./... && go test ./... && go test -race ./... && go build -ldflags="-s -w" -o rasterbar ./src` — all pass. `gofmt -l src/` clean.

Benchmarks, min of 5 at 200×57, against the table in AGENTS.md: every path still
**0 allocs/op**. `ColorDrawWorstCase` 1.65ms (was 1.47ms — noise on this box;
`ns/op` has always been unreliable here, which is why allocations are the hard
signal). `MonoDraw` 25µs, `OnsetPush` 238ns, `MiniBars` 12µs / 4 allocs. The
letterbox changes no Go code on the hot path.

Manually, in a real pty, one at a time:

- `-l ~/Projects/music/songs` → a browsable list in filename order
- `rasterbar ~/Projects/music/songs/*.opus` and a `.m3u` → a queue
- an mp3 with `a` in the browse list → a visualiser, no ffmpeg spew
- `:` then an arrow split across reads → no panic
- music mode, `+`/`-` mashed → clean under `-race`
- `s` then a seek then `s` in music mode → no silent exit
- ctrl-c mid-playback → `echo`/`icanon` restored, cursor visible, no orphans
- split view: `W`, `{`, `}`, `d`, `a`, `T` in sequence → no `read first frame`,
  both panes painting, 40 rows addressed, ~5.8k distinct colours in one frame
- a 16:9 clip → the letterbox is visible and the picture is not stretched

## Left alone, and why

- **`LevelTap` death is reported, not acted on.** `Dead()` exists and the pump
  records the error; wiring it to the status line is a render-loop change with no
  test seam behind it, and it is better than nothing only if it is finished. Worth
  a follow-up rather than a half-wired panic path.
- **`fdWritable` still runs after `bw.Flush()`**, which means the flush can be
  what blocks. Reordering means deciding what to do with a partially-flushed
  frame, which is a design question about frame atomicity — and DEC 2026 now makes
  that a *terminal* problem for terminals that support it. Flagged, not guessed.
- **The `-l` message** still says "N tracks" rather than episodes, which is right
  now that both kinds are in the list.