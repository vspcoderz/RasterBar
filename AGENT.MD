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

Music mode resolves the audio stream **twice** on purpose, once for the level tap
and once for mpv. Two `yt-dlp -J` calls mint two independent grants, so each
consumer genuinely holds its own. That is not a contradiction of the rule above:
the rule is about URLs, not streams. The single-URL version it replaced is what
made music mode one 403 away from silence, intermittently, because a grant is a
cache and whichever consumer opened it first won.

**Every ffmpeg that feeds a *clock* needs `-re`.** The video pipe and the level tap
both do. Without it ffmpeg pushes as fast as the CPU allows, so the whole track is
consumed while mpv is still on the first second — and every symptom points
somewhere else. Measured: the spectrum drew one frame and froze for the rest of a
60s track, the onset envelope stopped at 0.193, the particle field fired once, and
the bars stopped moving. None of that looks like a missing `-re`; it looks like a
broken detector, a broken analyser and a broken renderer.

**The level tap is its own ffmpeg, so it needs `-ss` too.** It cannot share mpv's
copy of the stream, so after a seek it has to be told where playback is or it
analyses from the top of the file. `levelTapArgs` is a function with a test because
both flags fail silently.

**The onset detector reads the analyser's *raw* magnitudes, not `Analyze`'s return
value.** A bar wants a slow release; an onset detector needs the opposite and gets
geometrically weaker every bar otherwise, because smoothed values only fall 18% per
analysis.

**The onset threshold is a median, never a mean.** Onsets are rare by definition, so
a mean is dragged up by the very events it should detect. On a periodic signal the
mean *is* the transient, and `mean * 1.6` is a threshold nothing can cross.

**Never set `mpv --idle`.** It survives end-of-file, so mpv never exits and track
end is unobservable. The queue hangs on the last frame of every track.

**Never resolve a local path through yt-dlp.** `resolveURL` and `resolveMedia`
branch on `track.IsLocal()` and hand back the path unchanged. yt-dlp finds no
extractor for a filesystem path, so every seek would pay a subprocess to be told
it failed. A local file is also already a progressive container: both refs are the
same path, which is the same shape `pickStreams` returns for a progressive
YouTube format, so there is nothing to select.

**ffprobe is safe on a local path and unsafe on a resolved URL.** The reason this
project avoids probing a googlevideo URL is the single-use grant, not the
subprocess. `library.go` probes freely, once per file, capped at 8 concurrent.

**The spectrum's dB window adapts, per spectrum and not per band.** A fixed
`-70..-10dB` mapping is calibrated for a quiet home recording; a commercial master
sits 10-15dB hotter and pins every band at 1.0, so the visualiser draws a solid
block. Measured on a T-Series pop track: `band[0]=1.000 band[last]=0.950`.

Per band is the tempting version and it is **wrong**: normalising each band against
its own peak makes a loud band and an empty one both read as full scale, so an 86Hz
tone lit the lowest band and the highest equally. The floor is one value for the
whole spectrum. `TestSpectrumAnalyzerBands` catches the per-band version, and its
assertion is about *which* band is loudest rather than comparing band 0 to band 31
-- the latter only passed by accident of the old calibration.

**Resample bands to the drawn column count on the way in, not on the way out.** The
smoother is sized to the grid and `Push` writes as many entries as the analyser
produced, so without this the tail columns keep their zero initial value forever: on
an 80-column terminal the analyser's 48 bands fill 48 columns and the right-hand
third is permanently blank.

`probeMedia` must pass `-show_streams`. Without it ffprobe returns format and
chapters but no `streams` key, so `hasAudio` is always false and every local file is
classified silent. In video mode that silently downgrades track-end detection to the
grace timer after the video pipe closes; in music mode it refuses to play the
audio-only file that mode exists for.

**Never close the video pipe before killing ffmpeg, and never `Wait` on mpv.**
Both hang the render loop rather than crashing it, and a hung loop ignores `q`
because it never reaches a `select` again. Measured at roughly 1 seek run in 6.
- `vidOut` is an `*os.File` from `StdoutPipe`, and Go's poller makes `Close` on a
  file with a read in flight *wait* for that read. The read waits on a writer, the
  writer is ffmpeg blocked on a full pipe, and the only thing draining that pipe
  is the read being waited on.
- mpv is already reaped by the goroutine `start` launches to close `s.audio`. A
  second `Wait` on the same `*exec.Cmd` queues behind a process whose exit we do
  not control — the observed freeze was `mpv` still in state `T`, SIGSTOPped by
  the covered-terminal handler. `Close` signals `SIGCONT` before `SIGKILL` and
  bounds the ffmpeg wait; see `childReapTimeout`.

**Decode keys in the render loop, never in the reader goroutine.** `readKeys` is a
byte pump. It used to emit `Cmd`, which made the `:` prompt impossible: a `:` and
the timestamp after it arrive in one read, and the reader had already decided that
`q` was a quit and `-30` was three volume changes before anything could object.
`keyRouter` owns the routing so the burst behaviour is unit-testable.

**ffprobe's JSON is not shaped the way you would guess.** `start_time` and
`end_time` are JSON *strings*, and a chapter title is nested under `tags`.
Structuring them as numbers and as a top-level field compiles, runs, and fails at
unmarshal — which `probeMedia` cannot distinguish from "no duration", so the
library's length column silently goes blank. `TestFfprobeOutputShape` pins the
real response.

## Two rules that look like bugs and are not

**A track ends when *both* streams end, not when the first one does.** mpv exiting
is the end of the *audio*; the video pipe closing is the end of the *picture*. Ending
on mpv's exit alone means any file whose soundtrack is shorter than its video jumps
to the next track with half the film unwatched.

This was masked for the entire life of the project by the `probeMedia` bug above:
`hasAudio` was always false, every local file was classified silent, `sess.audio`
stayed nil, and video mode ended tracks on the grace timer after the video pipe
closed instead. Fixing the probe turned a latent ordering bug into a live one, which
is the argument for fixing a bug in the same commit that removes whatever hid it.

**Nil a channel once you have consumed its close.** A closed channel is permanently
ready, so leaving it in a `select` makes that arm fire on every iteration — a busy
loop that spins a core and floods stderr if anything is logging. `sess.audio` and
`sess.frames` are both set to nil as they are consumed. A nil channel blocks forever
in a select, which is exactly the "never again" needed.

## Testing

Behaviour is tested at seven seams, all runnable without ffmpeg, mpv, or network:

1. `Player` — transport state machine, against a `fakeMedia` backend
2. `hud.go` — pure formatting (`formatClock`, `progressBar`, `parseTimestamp`,
   `hud.lines`, `miniBars`)
3. `Outcome` — why playback stopped
4. `mpvIPC` — request encoding and reply parsing
5. `keyRouter` / `prompt` — byte routing, so a `:` and its timestamp in one read
   is a unit test rather than something you have to press keys to find
6. `VizGrid` + each style — band resampling, the ramp round-trip through
   `levelFor`, and one paint per style against a hand-worked grid
7. **the whole tap pipeline** — `testdata/beat-12s.pcm` through
   `SpectrumAnalyzer` → `onsetDetector` → `AudioFrame` → a style

Seam 7 exists because seams 1–6 all passed while the feature was broken, and they
were structurally incapable of catching it. Every piece was right; they were wired
together wrongly. The fixture is checked in as raw PCM rather than synthesised in
the test because the bug was a *calibration* problem — absolute band levels decide
the thresholds — so a signal that happened to be 3dB quieter would have hidden it.

Deliberately **not** unit tested, because they need real processes: the SIGSTOP
pause, the ffmpeg rebuild on seek/resize, the decoder goroutine against a live
pipe, and anything that writes to a terminal. Verify those by running it.

Expected values in tests must be worked out by hand or taken from a known-good
literal. Recomputing them the way the code does makes the test pass by
construction and proves nothing.

### Asserting on the screen, not on bytes

`screen_test.go` carries a small terminal emulator, and it is the only way to
catch a stale cell: a renderer that emits the right bytes in the wrong order still
"writes something". Reconstructing the grid and comparing it to the frame that was
handed in is the assertion that means anything.

The emulator in `screen_test.go` used to **ignore SGR 38/48 entirely**, which made
every colour assertion in that file vacuous: it recorded each cell's colours as 0 no
matter what was emitted, and `leakFrames` finishes on an all-black frame, so "screen
is black" matched "frame is black" for all 216 cells. The colour leak tests had never
once read a colour. It is implemented now, and it immediately exposed that
`quant256` was mapping pure black onto grey-ramp index 232 — which is `#080808`, not
black — so every "black" cell on a 256-colour terminal was a faint grey smudge.

`assertScreenMatches` also has to know the colour mode, or a 256-colour screen is
compared against a 24-bit expectation and every non-exact colour reads as a failure.
Quantisation is not a leak, and treating the two as the same thing is how a real leak
gets dismissed as a test bug.

`DiffRenderer` and `ColorDiffRenderer` both keep a diff cache the overlay is
invisible to, so closing an overlay **must** be followed by `ForceNext` or the
prompt stays on screen for the rest of the track. The colour path additionally
tracks cursor position and last-emitted SGR; painting over the grid invalidates
both, or the next frame resumes mid-row.

`ForceNext` also invalidates the **HUD's** skip cache, and every caller has to know
that. A forced repaint clears the whole screen including the chrome, so
`paintHUD`'s "unchanged, skip" then decides there is nothing to repaint and the
title, clock and footer stay blank. Three separate places reach a `ForceNext` —
the stall recovery, a seek rebuild, and a visualizer style switch — and all three
must clear `hudText` with it.

The visualizers paint into `VizGrid` and then hand the result to those same two
renderers, so everything above applies to them for free. That reuse is why
`screen_test.go`'s no-stale-cells suite covers visualizer output without a single
new test, and why a third bespoke painter would have meant re-proving all of it.

### Driving it without a TTY

Pipe-driven testing of this program gives misleading results: a reopened
`/dev/stdin` and the original fd 0 compete for the same pipe and bytes go missing.
Use a real pty. `/tmp/opencode/ptycheck.py` (cases: `seek`, `back`, `pause`,
`next`, `vol`, `auto`) and `verify.py` were built for this; recreate them if gone.

- `auto` picks the shortest result in the list and waits — the only way to prove
  a track ending advances the queue.
- Note that the browse list has a line-mode fallback for non-TTY stdin, so a
  piped run selects with a **line index**, not a bare Enter.

`/tmp/opencode/` holds the current generation, recreate if wiped:

| Script | What it proves |
|---|---|
| `termscreen.py` | the terminal emulator every other script asserts with; mirrors `screen_test.go` deliberately, because two implementations of the same idea would disagree |
| `ptydrive.py` | shared pty plumbing: pump, send keys, locate HUD rows by content |
| `musiccheck.py` | music mode end to end — every style cycles, palettes, transport, prompt, strip |
| `leakdecide.py` | dense→sparse style switch must actually clear cells; "every row has ink" is not a valid invariant, because bars fills the bottom and scope draws a line |
| `videockeck.py` | video mode has not regressed, including **live SIGWINCH resizes** |
| `silentcheck.py` | a video-only local file plays its full length |
| `hanghunt.py` | repeated seeks never freeze the loop (the `Close` deadlock) |
| `leakhunt.py` | hunts stale cells in a running player |

Four rules for these, all learned the hard way:

- **Never run two of them at once.** They all spawn and kill ffmpeg and mpv, and
  one script's cleanup will reap another script's children mid-run.
- **A synthetic test clip needs an audio stream.** `mpv` exits instantly on a
  video-only file, which reads as track end and ends the run in about two seconds.
- **Never override `--rows`.** `computeLayout` already subtracts the chrome rows,
  so passing a value double-subtracts and every assertion lands on the wrong row.
- **A copied terminal screenshot shows you nothing.** In colour mode the glyph is a
  space or a half-block and all the ink is in the background, so selecting and
  copying 1600 cells of visualiser yields a wall of blank lines. It looks exactly
  like "the visualiser is not drawing". Dump cell attributes instead — see
  `termscreen.py`'s `lit()` — and remember the fix may not be about drawing at all.
- **Locate HUD rows by content, never by index.** The chrome height changes when
  the spectrum strip is toggled, so every fixed index is wrong the moment `s` is
  pressed. That reads as "the strip toggle broke the HUD".
- **A steady test tone has no transients**, so the particle field is *correctly*
  empty on one. Use a gated pulse train (`testdata/beat-12s.pcm` is 12s of 50ms
  bursts every 500ms) when checking anything that reacts to onsets, or you will
  chase a bug that is not there.

## Architecture

```
main.go        arg parsing, then playQueue (owns the queue index)
search.go      ytfzf/yt-dlp search -> []Track
library.go     directory scan -> []Track; filename -> series/season/episode
tui.go         browse list; line-mode fallback when stdin is not a TTY
session.go     trackSession (media backend), decoder goroutine, render loop
player.go      Player: transport rules, byte routing, the `:` prompt
sync.go        SyncPlayer: ffmpeg video pipe + mpv audio, A/V sync
ipc.go         mpv JSON IPC: time position, seek, properties
hud.go         title / progress bar / hints / parseTimestamp / miniBars
render.go      mono diff renderer + overlay layout
color.go       colour renderer, glyph probe, terminal detection
flow.go        covered-terminal stall handling
fft.go         radix-2 FFT, log-spaced band edges, raw vs smoothed magnitudes
visual.go      LevelTap (PCM tap), Visualizer (display smoothing)
viz.go         VizGrid, the two painters, style registry, prefs
styles.go      the six visualizers
palette.go     HSV -> RGB and the four colour schemes
beat.go        spectral flux, onsets, tempo estimate
```

`playQueue` owns the queue index. `playTrack` plays exactly one track and returns
an `Outcome`; it never mutates the queue.

### Music mode is a flag, not a second player

`trackSession.music` is a bool. Video mode and music mode share the render loop,
the prompt, the resize path, the stall handler and the queue, and they feed the
same channel: music mode's `pumpMusic` pushes a `videoFrame` with a **nil** buffer
and a position, and the loop branches on that once.

This is not tidiness. Phase 6 is what making one mode good cost — a byte pump, a
key router and a prompt state machine. A parallel `runVisualMusic` would have
rebuilt all three. The old `runVisualAudio` was exactly that mistake: its own loop,
one key (`q`), no HUD, no duration, no seek, no queue advance, and it was the
*default* mode.

The two modes differ in one structural way worth remembering: **where the position
comes from**. Video mode has two children on two clocks and counts frames; music
mode has one child and asks it. `SyncPlayer.Seconds()` branches on it, and
`checkSync` is a no-op in music mode because there is nothing to drift from.

### Seeking and the prompt

Every position change goes through `Player.seekTo`, which clamps and leaves the
pre-seek marker for the bar. `keyRouter` turns terminal bytes into `Cmd`s and
routes them to the `prompt` first while it is open; the render loop owns the
router and is the only thing that decodes.

The overlay is drawn after the frame, on the bottom two grid rows, and closing it
forces one full repaint — see Testing above for why both halves of that matter.

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

In music mode there is only one child to stop, and `pauseChildren` already guards
the nil ffmpeg. The pause *banner* is drawn on the keypress by `feedKeys` before the
loop can notice the children are frozen — so it appears even though no further
frames arrive to repaint it.

### The spectrum strip costs a grid row

`s` toggles it. It takes its space out of the **video grid**, not by overlaying the
bottom rows, because overlaying means fighting the diff cache for those cells and
forcing a full repaint every frame — giving up the reason the diff renderer exists.
So `chromeRows` is a parameter to `computeLayout`, and toggling is a layout recompute
plus the rebuild that SIGWINCH already does. In music mode there is no video, so the
same key is free.

The HUD therefore has two shapes, three rows and four. Which is why `hud.lines`
returns a slice the caller indexes by position rather than three named strings, and
why `chromeRowsFor(strip)` exists rather than a constant: three places have to agree
on the chrome height.

## Style

- Match the surrounding code's density: comments explain *why*, especially why a
  rejected alternative lost. Do not narrate what the code does.
- Every non-obvious constraint gets a comment saying what was measured, not what
  is assumed.
- Errors: report what failed and the underlying reason. Never swallow one to keep
  a background task quiet — except the drift corrector, which stays silent
  because a missed nudge is not worth interrupting playback for.