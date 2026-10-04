# AGENT.MD — vspz-yt-cli

Terminal YouTube player: ASCII video with live audio, in one static Go binary.

## Commands

Run the full check **once**, at the end, as one chained command. Do not re-run
build/vet/test after every small edit — it is slow and it is not the bottleneck.

```sh
go vet ./... && go test ./... && go build -ldflags="-s -w" -o vspz-yt-cli .
```

Single test file: `go test -run TestComputeLayout ./...`

## Hard rules

**Zero third-party Go modules.** This is the project's defining constraint, not a
preference. The FFT, the PTY, raw mode, and termios are each hand-rolled because
each dependency would have been a few dozen lines of syscall or arithmetic. Before
adding any import, check whether it is already in the standard library, and
whether the "dependency" is really 30 lines.

**Do not retry the dead ends below.** They were each verified against a real
failure. Re-testing them wastes a session.

| Dead end | What actually happens |
|---|---|
| `mpv --vo=aa` | `Video output aa not found!` — not compiled into Arch's mpv |
| ffmpeg on a youtube.com URL | `Invalid data found when processing input` |
| `yt-dlp -f worst` | no progressive `18`; formats are 130-399 |
| one ffmpeg writing video pipe + audio FIFO | deadlocks, 0 bytes produced |
| probing a resolved googlevideo URL | burns the grant → `403 Forbidden` on the real request |

**Never pass an audio URL to ffmpeg as an input.** It is never mapped, but
ffmpeg opens it anyway, and googlevideo URLs are effectively single-use — mpv's
own request then fails with 403. One input, one consumer.

**Never set `mpv --idle`.** It survives end-of-file, so mpv never exits and track
end is unobservable. The queue hangs on the last frame of every track.

## Testing

Behaviour is tested at four seams, all runnable without ffmpeg, mpv, or network:

1. `Player` — transport state machine, against a `fakeMedia` backend
2. `hud.go` — pure formatting (`formatClock`, `progressBar`, `hud.lines`)
3. `Outcome` — why playback stopped
4. `mpvIPC` — request encoding and reply parsing

Deliberately **not** unit tested, because they need real processes: the SIGSTOP
pause, the ffmpeg rebuild on seek/resize, the decoder goroutine against a live
pipe, and anything that writes to a terminal. Verify those by running it.

Expected values in tests must be worked out by hand or taken from a known-good
literal. Recomputing them the way the code does makes the test pass by
construction and proves nothing.

### Driving it without a TTY

Pipe-driven testing of this program gives misleading results: a reopened
`/dev/stdin` and the original fd 0 compete for the same pipe and bytes go missing.
Use a real pty. `/tmp/opencode/ptycheck.py` (cases: `seek`, `back`, `pause`,
`next`, `vol`, `auto`) and `verify.py` were built for this; recreate them if gone.

- `auto` picks the shortest result in the list and waits — the only way to prove
  a track ending advances the queue.
- Note that the browse list has a line-mode fallback for non-TTY stdin, so a
  piped run selects with a **line index**, not a bare Enter.

## Architecture

```
main.go        arg parsing, then playQueue (owns the queue index)
search.go      ytfzf/yt-dlp search -> []Track
tui.go         browse list; line-mode fallback when stdin is not a TTY
session.go     trackSession (media backend), decoder goroutine, render loop
player.go      Player: transport rules, key decoding
sync.go        SyncPlayer: ffmpeg video pipe + mpv audio, A/V sync
ipc.go         mpv JSON IPC: time position, seek, properties
hud.go         title / progress bar / hints
color.go       colour renderer, glyph probe, terminal detection
flow.go        covered-terminal stall handling
```

`playQueue` owns the queue index. `playTrack` plays exactly one track and returns
an `Outcome`; it never mutates the queue.

### Sync

ffmpeg paces video on the system clock, mpv paces audio on the sound card's
clock. They drift, so `checkSync` asks mpv for its real position every 2s and
seeks the audio when the gap passes 250ms. Measured steady-state drift is
~40-100ms. `VSPZ_YT_CLI_DEBUG_SYNC=1` shows the readout.

`SyncPlayer.Seconds()` adds `startAt`. A rebuilt player decodes from `-ss
<offset>`, so its frame counter starts at zero while the media is minutes in. Get
this wrong and every resize rewinds the audio to the start, because the corrector
reads the error as drift.

### Pause

`SIGSTOP` on both children in the same instant. This is the same mechanism
`flow.go` uses for a covered terminal, promoted to a feature: freezing both clocks
together is what preserves sync exactly. Do not route pause through mpv's `pause`
property — mpv would stop on the sound card's schedule while the video pipe kept
filling, and that backlog would surface as a burst on resume.

## Style

- Match the surrounding code's density: comments explain *why*, especially why a
  rejected alternative lost. Do not narrate what the code does.
- Every non-obvious constraint gets a comment saying what was measured, not what
  is assumed.
- Errors: report what failed and the underlying reason. Never swallow one to keep
  a background task quiet — except the drift corrector, which stays silent
  because a missed nudge is not worth interrupting playback for.