# AGENTS.md — RasterBar

Terminal YouTube player: ASCII/colour video or a music visualiser, in one static
Go binary. Module `github.com/vspcoderz/rasterbar`; **all code is in `src/`**, so
the installable path is the module path plus `/src`.

`POSTMORTEMS.md` holds the measured history behind every rule here. The rules
below are the load-bearing subset; add new rules here, not there.

## Commands

Run the full check **once**, at the end, chained — not after every edit.

```sh
go vet ./... && go test ./... && go build -ldflags="-s -w" -o rasterbar ./src
```

Single test: `go test -run TestComputeLayout ./...`. Tests run offline, in ~0.6s,
with no ffmpeg/mpv/network.

## Zero third-party Go modules

Defining constraint, not a preference. FFT, PTY, raw mode and termios are all
hand-rolled. Before adding an import, check the stdlib, then check whether the
"dependency" is really 30 lines.

## Dead ends — verified failures, do not retry

| Dead end | What happens |
|---|---|
| `mpv --vo=aa` | `Video output aa not found!` — not in Arch's mpv |
| ffmpeg on a youtube.com URL | `Invalid data found when processing input` |
| `yt-dlp -f worst` | no progressive `18`; formats are 130-399 |
| one ffmpeg writing video pipe + audio FIFO | deadlocks, 0 bytes |
| probing a resolved googlevideo URL | burns the grant → `403` on the real request |

## Rules whose failure is silent

These bugs do not crash — they misbehave somewhere else, so each is pinned by a
test or an explicit comment.

**Media / streams**
- **Ask what streams a local file has, and act on both answers.** `hasAudio` and
  `hasVideo`. An audio-only file handed to the video tap gets `-map 0:v:0`, which
  is a hard ffmpeg error, then EOF, then "read first frame: EOF" — and because
  `-l DIR --play` defaulted to video mode, a music folder died on track 1 and
  took the queue with it. A track with no video stream plays as a visualiser;
  `silent` stays fatal in music mode, because the spectrum is the content there.
- **An episode marker is not a requirement for being playable.** `classifyMedia`
  returning false used to drop `nocturne.opus` and scan a music folder to zero
  tracks. Unnumbered files are included and sorted by natural filename order.
- **Never pass an audio URL to ffmpeg as an input.** It is unused but ffmpeg
  opens it anyway; googlevideo grants are single-use, so mpv then gets `403`.
  One input, one consumer. Music mode resolving the audio stream *twice* is
  deliberate: two `yt-dlp -J` calls mint two independent grants.
- **Every ffmpeg that feeds a clock needs `-re`.** Without it ffmpeg consumes the
  whole track while mpv is on second one. Symptoms look like a broken detector/
  analyser/renderer, not a missing flag.
- **The level tap needs `-ss` too** — it is its own ffmpeg and cannot share mpv's
  stream position. See `levelTapArgs`.
- **Never set `mpv --idle`.** It survives EOF, so track end is unobservable and
  the queue hangs on every last frame.
- **Never route a local path through yt-dlp.** `resolveURL`/`resolveMedia` branch
  on `track.IsLocal()`. A local file is already a progressive container.
- **ffprobe is safe on local paths, unsafe on resolved URLs** (single-use grant).
  `probeMedia` must pass `-show_streams` or `hasAudio` is always false.
- **ffprobe JSON does not match the struct you would guess:** `start_time` /
  `end_time` are strings, a chapter title nests under `tags`. Pinned by
  `TestFfprobeOutputShape`.
- **A test that re-implements the parse proves nothing.** The `-show_streams`
  tests unmarshalled the fixture and looped over `Streams` themselves, so
  dropping `-show_streams` again — the exact regression they were written for —
  left them green. They call `parseProbeOutput` now, which is the entry point
  production uses.
- **A failed probe is an error, not `hasAudio == false`.** Folding ffprobe's
  failure into "no audio" reported a corrupt download as "no audio stream in
  <title>", which is a confident wrong answer to the only question the user can
  act on.

**Concurrency / lifecycle**
- **A track has three clocks and pause must freeze all of them, not the ones
  that happen to be inside `SyncPlayer`.** `pauseChildren` SIGSTOPs `ff` and
  `audio`, which is all of them in video mode. Music mode has a nil `ff` and two
  `-re`-paced ffmpegs of its own — the `LevelTap` and the split pane's
  `videoTap` — so freezing mpv alone left the spectrum and the split video
  animating against a stopped sound. Measured with `/proc/<pid>/stat`: paused on
  the old code, `mpv=T` and both `ffmpeg=S`; after the fix, all three `T`.
  The half that could not be undone was the tap: it kept consuming audio, so it
  ended every pause permanently ahead of mpv by the pause's length, and
  `checkSync` cannot fix it — music mode has no frame counter to compare against.
  `TestPauseReachesEveryChildClock` fails if either call is dropped.
- **A SIGSTOPped process still listens; it just stops answering.** So a paused
  mpv makes `pollPosition` block for the full `musicPollTimeout` on *every*
  frame — a 60ms tax on a 33ms budget. `checkSync` and `SetVolume` already
  bailed on a paused player; the music pump was the third caller that forgot.
  `SyncPlayer.paused` is an `atomic.Bool` because the pump reads it from another
  goroutine.
- **Never close the video pipe before killing ffmpeg, and never `Wait` on mpv.**
  Both hang the render loop (which then ignores `q`). `Close` signals `SIGCONT`
  before `SIGKILL`; ffmpeg wait is bounded by `childReapTimeout`. `mpv` is reaped
  by the goroutine `start` launches. `LevelTap.Close` had it backwards (pipe
  first) and hung on every seek, resize and track advance.
- **Decode keys in the render loop, never in the reader goroutine.** `readKeys`
  is a byte pump; `keyRouter` owns routing so a `:` prompt is testable.
- **`used` is a count against the caller's slice, never a longer one built
  internally.** `prompt.consume` prepends a held `escTail`; returning
  `len(chunk)` of the combined slice made `:` + an arrow split across two reads
  panic with `slice bounds out of range` inside the render loop.
- **`mpvIPC` has a mutex because music mode really has two goroutines on it.**
  `pumpMusic` polls `timePos` every tick while `+`/`-` calls `setVolume`; a lost
  `next++` hands one `request_id` to two calls.
- **A terminal stops reading before you notice, so check the fd before the
  write.** `fdWritable`'s guard is `len(set.Bits)*64`, not a round number:
  `FdSet` holds 1024 fds.
- **Ctrl-C is SIGINT, so handle it.** Raw mode here keeps `ISIG`, meaning the
  `0x03` case in `cmdForByte` is unreachable and the default action kills the
  process with the tty still raw and the children unreaped. `playTrack` turns it
  into `OutcomeQuit`.
- **Print a failure after the terminal is restored, not before.** The deferred
  clear erases it (defer is LIFO).
- **Nil a channel once you have consumed its close.** A closed channel stays
  ready and spins a `select` forever; nil blocks forever.

**Analysis / spectrum**
- **Band levels are not dB.** They are unnormalised FFT magnitudes carrying a
  measured +44.2dB offset (full-scale ≈ +42). To mean a physical level, measure
  the input samples (`measureLevel`), never the bands.
- **Never measure a signal property on the display** — normalisation applies a
  floor and a clamp, and a clamp invents contrast. `stdDb`/`levelDbfs` are
  computed before it.
- **The onset detector reads the analyser's raw magnitudes, not `Analyze`'s
  smoothed return.** A slow release kills transients geometrically.
- **The onset threshold is a median, never a mean** — onsets are rare, and a mean
  is dragged up by the events it should detect.
- **The noise gate is shape AND level**, measured in dB on raw bands before any
  normalisation. Tuned on synthesised tones it lies; real music reads mean/max
  0.42-0.80 where tones read 0.04-0.11. Calibrate against real material.
- **The dB window adapts per spectrum, not per band.** Per band makes an empty
  band read full scale.
- **`analysisHz` needs its `float64` conversion.** `spectrumHz/fftSize` with both
  untyped int constants is *integer division evaluated at compile time*: 11025/1024
  is 10, not 10.766. A tempo-driven style advancing one beat per analysis is then
  7.7% slow — a wrong tempo that reads as a rounding mistake somewhere else. `go
  vet` catches the `%f`-with-int case; it caught this one only because a test
  printed the value.
- **`bandHz` is a second copy of the analyser's band layout**, in closed form,
  because `NewSpectrumAnalyzer` keeps its `edges` private. Nothing but
  `TestBandHzAgreesWithTheAnalyzersEdges` connects them — change the log spacing
  in the constructor and every scope silently stops zooming.

**Visualisers**
- **The scope's window was the bug, not its line style.** The tap hands over
  `waveWindow` = 1024 samples at 11025Hz — 92.9ms — and the old scope drew all of
  it across the width: 40.8 cycles of a 440Hz note on an 80-column screen, two
  columns per cycle, aliased into a band where every note looked identical. It now
  shows ~2.5 cycles of the dominant band (62 samples at 440Hz) and triggers on a
  rising zero crossing. Measured, not aesthetic.
- **Animate in `Push`, not `Paint`.** `Push` is the analysis rate (~10.77Hz);
  `Paint` is up to 30Hz *and* `CapScale` of that. A phase advanced in `Paint` runs
  in slow motion the moment a style is capped — metro's wavefront, wheel's spin,
  helix's coil, bloom's rings, aurora's drift.
- **A phosphor trail decays in `Push` for the same reason**: per `Paint` it is
  three times longer on a capped style, so the same track looks different on two
  terminals.
- **A style's picture must be a function of the music, not of a generator.** Value
  noise is 0..1 *by construction*, so aurora drawn straight from it lit 654 of 672
  cells in silence — a bright screen reacting to nothing. It is gated by the
  smoothed level.
- **Gate a field, do not gate nothing.** The same argument is why barsViz draws an
  axis under every band and matrixViz draws its unlit LEDs: a silent band drawn as
  literally nothing is indistinguishable from a column the renderer never reached.
- **Spatial constants need the grid, not a per-beat unit.** metro's first version
  moved one cell per beat, which at 128bpm is a 1-cell spacing — every cell inside
  the front, the whole screen lit every frame, 1.2ms/op and a uniform glow instead
  of rings. Spacing is a fraction of the radius; the *speed* carries the tempo.
- **Per-row phase skew has to stay under a fraction of the spacing.** 0.35
  cells/row over 60 rows is 21 cells against a 25-cell spacing: the rings interfere
  with each other instead of waving, and 61% of the grid lights. 0.04 is a tenth
  of a spacing and still visible.
- **`math.Hypot`, `math.Mod` and `math.Exp` in a per-cell path are 3.8x.** metro
  went 2.67ms → 697µs by using `math.Sqrt`, floor-and-subtract, and `smoothstep`.
  Same picture to the eye. `Hypot`'s overflow protection is for inputs that cannot
  overflow, like grid coordinates.
- **The heavy set is a set, not a count.** `TestHeavyStylesAreMarkedAndCapped`
  used to assert `heavy == 2`, which a new expensive style could satisfy by
  claiming to be cheap. It now checks `heavyStyles` against what the styles report,
  in both directions.
- **A style's exemptions are sets too, and they are checked for typos.**
  `waveOnlyStyles` and `tempoStyles` replace a string compare on `"scope"`; a name
  in either must resolve through `VizByName` or it would silently exempt nothing.
- **`TestResetDropsEveryStyleState` compares against a control**, not against zero
  lit cells. matrix draws unlit LEDs and metro draws a tempo front on purpose, so a
  "must be dark" assertion fails them for drawing what they should. A fresh
  instance fed only silence is the right baseline; what it isolates is history that
  survived a reset.

**Rendering / layout**
- **Letterbox, never stretch.** `videoTapGeometry` emitted a bare `scale=W:H`,
  which stretches the source to the grid's shape. Measured: a square in a 16:9
  frame rendered 0.57:1 on screen — 43% out. `force_original_aspect_ratio=decrease`
  plus `pad` fixes it and still emits exactly `cols*rows` bytes, so the compositor
  contract is untouched. `--aspect` is a guess about the *cell* ratio; the filter
  is the one that knows the *source's*.
- **The letterbox's filter ORDER is load-bearing, and the failure is silent.**
  `scale=…:force_original_aspect_ratio=decrease,setsar=1,format=X,pad=W:H` works;
  moving `format` after `pad` makes ffmpeg fail with "Padded dimensions cannot be
  smaller than input dimensions" on most geometries, which the video tap reports
  as "read first frame: EOF" and the split pane just never appears. `setsar=1` is
  equally required — the fit leaves `sar 64/63` and pad compares *display*
  dimensions.
- **Test a filter across geometries, not one.** 100×50 was the single geometry
  the first version of this test used, and it is the one that worked.
  `TestVideoTapFilterByteCountAcrossGeometries` sweeps 13 shapes × 4 modes.
- **Build test fixtures nobody can actually get and they will agree with your
  bug.** The same test passed against a broken filter because the fixture encoded
  as `yuv444p`, which pad accepts; every real source is `yuv420p`, which it
  refuses. Pin the pixel format, and check the fixture is what production sees.
- **Recreate the renderer whenever the grid's shape changes.** It is built once
  and rebuilt only in the `sess.dirty` path, so the music branch of `CmdStrip`
  left it sized to the old row count and a later smaller frame failed the
  `frame too small` check — `OutcomeError`, silent exit.
- **Recompute from the live window, not the size captured at entry.**
  `CmdStrip` used the entry-time `termCols`/`termRows`, so a strip toggle after a
  resize snapped the grid back to the pre-resize geometry.
- **Wrap frames in DEC 2026, per frame.** `\x1b[?2026h` before the frame's
  first write, `\x1b[?2026l` after the last one, in the *same* loop iteration.
  It is a begin/end pair, not a mode you switch on: sending `h` at startup and
  `l` at exit makes a terminal that implements it buffer the whole track and
  present nothing until the program exits — a black screen for the entire video,
  on exactly the terminals it was written for. A pty ignores the mode, so every
  test passes. `tools/ptysync.py` is the only harness that catches it.
- **A threshold has to sit inside the range the code around it can produce.**
  `miniBars`'s peak cap tested `peak > level+0.5`, but `Visualizer.Push` decays
  level by 0.82 and peak by 0.93, which caps the achievable gap at ~0.36
  (measured 0.317). The cap never drew.
- **A ramp index is not a character.** `rampFor` returns an index; string builders
  must do `ramp[rampFor(...)]`. An index below 32 is a control char, and LF in
  the chrome is a scroll. `TestHudLinesCarryNoControlCharacters` catches it.
- **Closing an overlay must be followed by `ForceNext`, and every `ForceNext`
  caller must clear `hudText`** — the forced repaint clears the whole screen, then
  `paintHUD`'s skip cache leaves the chrome blank.
- **Resample bands to the drawn column count on the way in**, or tail columns
  stay zero forever.
- **`computeLayout` already subtracts chrome rows** (`chromeRowsFor(strip)`).
  Never override `--rows` in a test or it double-subtracts.
- **A track ends when *both* streams end**, not when the first does. And note the
  `hasAudio` bug masked this for the project's whole life — fix a bug in the same
  commit that removes what hid it.
- **Decide the mode before anything that depends on it.** `musicModeFor` is
  called in `playTrack` *before* `computeLayout`, the glyph probe and the strip,
  because a video-less local file plays as a visualiser whatever was asked for
  and that changes the grid, the chrome height and every `if o.music` in the
  loop. Deciding it inside `newTrackSession` looked tidier and left the session
  pushing nil-buffer frames into the *video* renderer, which rejected them as
  "frame too small" and ended the track silently.
- **SGR dedup state is separate from cursor state, on purpose.** `haveLast` is
  about cursor contiguity and is cleared at the end of every row; the emitted SGR
  survives that. `emitColor` keys off `sgrValid`/`sgrFG`/`sgrBG` instead, so a
  still-active colour is not re-emitted at the top of every row. Merging them back
  costs one SGR per row per frame and nothing catches it.
- **`Push` happens at analysis rate, not render rate.** Use `LevelTap.TryFrame`,
  which reports false until a new FFT window lands. `Frame()` plus an
  unconditional `Push` makes the waterfall scroll, particles age and the smoother
  release ~3x faster than the music, because the render tick is up to 30Hz and the
  analysis is ~11Hz. The HUD strip broke this one layer up: `paintHUD` runs on
  every keypress too, so its `Push` decayed the strip 3x faster when you mashed
  keys. Two consumers need **two cursors** — `TryFrameAt` — or one starves.
- **Two consumers of one tap starve each other.** `readGen` is per-consumer, not
  per-tap. Reset the cursor when the tap is replaced, or a stale one reads as
  "nothing new yet" until the count climbs past the old generation.
- **A heavy style is capped, so call `CapScale`.** All six styles implemented
  `Heavy`/`CapScale` and nothing called them, so `usage`'s claim that expensive
  styles are capped described an intention. The pump reads the cap each tick
  rather than baking it into the ticker, because a ticker cannot change rate and
  `v` must take effect without restarting mpv.
- **`LevelTap` hands out copies, deliberately.** `Bands`/`Wave`/`Frame` copy under
  the mutex. The lock protects the *slice header*, not the contents, so returning
  the header and letting the caller read it let the pump overwrite the spectrum
  mid-read. A handful of bytes at 11Hz is the price of not needing `-race` to see
  it.
- **Handle both arrow introducers.** CSI is `ESC [ <final>`, SS3 is `ESC O
  <final>` — xterm's application cursor mode, which nothing here turns off, so a
  terminal started in that mode sends SS3 for the whole session. Also hold an
  incomplete sequence rather than treating a bare `ESC` as a cancel: a read
  boundary lands inside one often enough to lose a typed timestamp.
- **The video-mode strip needs its own grant.** `resolveMedia` mints a *third*
  URL (its own `yt-dlp -J`) when the strip is on, because `audioURL` belongs to
  mpv and one URL has one consumer. It is resolved lazily on `s` rather than on
  every video track, since it is a whole extra round trip.
- **The split view composites in Go; there is deliberately ONE renderer.** Two
  renderers would break the colour renderer's SGR elision, which assumes nothing
  else changed the terminal's colour state since its last cell — pane B repainting
  in between makes that false, and it produces wrong *colours* with no error.
  `composeSplitFrame` lays both panes into one frame and the existing whole-screen
  diff cache covers it, which is also why the no-stale-cells suite covers the split
  for free. See `splitleak_test.go`.
- **`videoTapGeometry` is the single source of pane layout.** The live pane and the
  thumbnail must agree byte-for-byte; two copies of the filter is how they drift and
  the compositor starts reading noise. `TestVideoTapGeometryMatchesPaneFrames` pins
  it.
- **Half-block doubling is colour-only.** `perCellFor` returns 1 in mono regardless
  of glyph mode. Getting that wrong made `paneFrameBytes` expect twice the bytes
  ffmpeg sends — noise on screen, not an error.
- **Split state lives in `splitState`, not in the render loop's locals.** As locals
  it was unreachable from a test, and the first version shipped with `divider` at 0,
  where `+1` and `-1` both clamp to the same floor and `{`/`}` silently did nothing.
  If you add split state, put it in the type so a test can reach it.
- **The pane's pixel width is ffmpeg's scale target.** Any change to it — resize,
  divider, side — is a restart, exactly like SIGWINCH restarting the video decoder.
  Resizing the buffer alone leaves ffmpeg sending frames of the old size.
- **The pane's URL is resolved lazily on the first `W`** and reused. Music mode
  already spends two `yt-dlp -J` calls; a third on every track would slow down the
  common path for a feature that is off by default.
- **Frame buffers are owned by exactly one goroutine at a time:** decoder fills →
  channel → render loop `Draw`s → `frameBufs.Put`. Neither renderer retains the
  slice (both copy into their caches), so returning it after `Draw` is safe. Any
  new renderer must keep that property or the pool aliases.
- **Never pin an RGB channel in a palette.** `_, g, b := hsvToRGB(...); rgb(1, g, b)`
  threw away the red and made every hue past the yellow arc full red — `ocean`
  rendered pink. Use the converted component; `TestHuePalettesUseTheConvertedRedComponent`
  pins it.

## Performance

`src/perf_bench_test.go`. Baseline vs current, min of 5, 200x57 grid:

```
go test -run XXX -bench . -benchtime 500ms -count 5 ./src
```

| bench | before | after | allocs before -> after |
|---|---|---|---|
| ColorDrawWorstCase | 9.36ms | 1.47ms | 11400 -> 0 |
| ColorDrawPartial | 179us | 71us | 0 -> 0 |
| BarsPaint | 610us | 237us | 0 -> 0 |
| RadialPaint | 448us | 58us | 0 -> 0 |
| WaterfallPaint | 1.17ms | 690us | 0 -> 0 |
| ColorFrame | 145us | 80us | 0 -> 0 |
| SpectrumAnalyze | 73us | 41us | 0 -> 0 |
| OnsetPush | 1694ns | 246ns | 2 -> 0 |
| MonoDraw | 52us | 24us | 57 -> 0 |
| MiniBars | 41us | 10us | 4 -> 4 (unchanged) |

`BenchmarkEveryStylePaint` walks `vizRegistry`, so the nineteenth style is
measured the day it lands rather than never. 200x60, min of 3:

```
go test -run XXX -bench EveryStylePaint -benchtime 200ms -count 3 ./src
```

| style | ns/op | style | ns/op |
|---|---|---|---|
| bloom | 23us | terrain | 224us |
| particles | 21us | helix | 263us |
| peaks | 53us | rays | 322us |
| radial | 75us | mirror | 357us |
| matrix | 84us | ribbon | 427us |
| scope | 83us | lissajous | 595us |
| swell | 113us | metro | 697us |
| wheel | 193us | waterfall | 942us |
| **aurora** | **1.51ms** | | |

All 18 at **0 allocs/op** — that is the hard signal, not `ns/op`. `aurora` is the
worst case and is the one style that is `Heavy()`, capped to 10-20fps by
`CapScale`. `waterfall` is the slowest *cheap* style and predates this batch.

Rules of thumb, each of which was a bug first:
- **No `fmt` in a per-cell path.** `fmt.Sprintf`/`Fprintf` per changed cell was
  11400 allocations per full repaint. Both renderers keep an `esc []byte` scratch
  and use `strconv.AppendInt` / `appendCUP`.
- **Hoist anything that does not depend on the inner loop index.** The gradient
  along a bar is per *row* (bars, mirror), the band position is per *column*
  (waterfall), and the colour along a radial spoke is per *spoke*. Redoing them
  per cell was 6x on radial alone.
- **`sync.Pool` for anything per-frame.** Frames and the onset detector's
  `baseline`/`tempo` scratch; the smoother already had this.
- `ns/op` on this box is noisy (a 2.5GHz i5 under load) — compare **allocations**
  for a hard signal and take the min of several runs for timing.

## Architecture

```
main.go      verbs + arg parsing, playQueue (owns the queue index; playTrack never mutates it)
search.go    ytfzf/yt-dlp search -> []Track       library.go  dir scan -> []Track
ytdlpauth.go yt-dlp cookies + actionable errors
playlist.go  file args, globs, .m3u -> []Track    naturalsort.go  filename order
tui.go       browse list (+ line-mode fallback)   session.go  trackSession, decoder goroutine, render loop
player.go    Player: transport, byte routing, : prompt   sync.go  SyncPlayer: ffmpeg video + mpv audio
ipc.go       mpv JSON IPC                         hud.go    title / progress / parseTimestamp / miniBars
render.go    mono diff renderer + overlay          color.go  colour renderer, glyph probe
flow.go      covered-terminal stall handling      fft.go    FFT, log-spaced bands
visual.go    LevelTap + display smoother          viz.go    VizGrid, style registry, prefs
styles_*.go  the eighteen visualisers              palette.go HSV -> RGB, ten schemes
split.go     the split view: two panes, one composed frame, splitState
beat.go      spectral flux, onsets, tempo         doc.go    package docs
internal/term  termios, TIOCGWINSZ, pty — the one extracted package
```

Everything else is one package on purpose: pieces are coupled through
deliberately unexported state so tests can assert decisions directly. Splitting
would force exporting it all.

- **Music mode is a flag (`trackSession.music`), not a second player.** Both modes
  share the loop, prompt, resize, stall handler and queue; music pushes a
  `videoFrame` with a nil buffer. The one structural difference is **where the
  position comes from**: video counts frames on two clocks, music asks its one
  child (`SyncPlayer.Seconds()` branches; `checkSync` is a no-op).
- **A positional argument is a file only if it looks like one, and that check
  needs the filesystem — so `parseArgs` collects and `main` decides.** Joining
  them in `parseArgs` is what made `rasterbar ~/Music/song.mp3` a YouTube search
  for that string. `-l`/`list` beats file args outright, since a directory scan
  has its own walk and parser.
- **A URL is never a path, whatever it contains.** `hasGlobMeta` looks for
  `*?[`, and `https://www.youtube.com/watch?v=X` is full of question marks, so it
  read as a glob that matched nothing. `https://youtu.be/x` has no query string
  and happened to work, which is why the documented example was fine and the real
  one never was.
- **One yt-dlp argument list, or a setting that only reaches some paths.** There
  are three yt-dlp invocations (search, metadata, playback) and they all build
  their args through `ytdlpAuth.run`. YouTube bot-checks by IP, so a flagged
  machine needs cookies *everywhere*; a flag added to one call site is a bug
  report that depends on which path the user was on.
- **"No paths" and "not a file intent" are different answers.** `expandFileArgs`
  returning an empty slice used to be read as "nothing playable", which killed
  every YouTube search with "no playable files in lofi hip hop radio". It returns
  the intent as a second value for exactly this reason: three answers, not two.
- **A test that only covers new paths will not catch a new path breaking the
  old one.** The whole suite was green with search completely dead, because
  nothing asked for a path that was *not* a file. `TestSearchStillSearches`.
- **A verb layer is a front door, not a replacement.** Bare arguments and every
  flag keep working; a verb only counts when it is the first positional argument
  AND something follows it, so `rasterbar play` still searches for a song called
  "play". The verbs that take no value (`help`, `version`) or an optional one
  (`list`) are the exception, or they would not be verbs.
- **A fake that disagrees with the protocol fails the test for the wrong
  reason.** The mpv IPC double replied with a canned sweep of ids instead of the
  one requested, which went flaky the moment the mutex serialised the calls.
- **Sync:** ffmpeg (video) and mpv (audio) drift; `checkSync` asks mpv's real
  position every 2s and corrects past 250ms. `VSPZ_YT_CLI_DEBUG_SYNC=1` shows it.
  `Seconds()` must add `startAt` or a resize rewinds audio to the start.
- **Pause is `SIGSTOP` on both children in the same instant** (same mechanism as
  the covered-terminal handler). Never use mpv's `pause` property.
- **The `s` spectrum strip takes a row from the video grid, not an overlay** — an
  overlay would fight the diff cache and need a full repaint every frame. Hence
  `chromeRows` is a parameter to `computeLayout`.

## Testing

Behaviour is tested at seven seams, all runnable with no ffmpeg/mpv/network:
`Player`+`fakeMedia`, `hud.go` formatting, `Outcome`, `mpvIPC`, `keyRouter`/
`prompt`, `VizGrid`+each style, and **the whole tap pipeline** (`testdata/beat-12s.pcm`
→ `SpectrumAnalyzer` → `onsetDetector` → `AudioFrame` → a style). Seam 7 exists
because seams 1-6 all passed while the feature was broken — every piece right,
wired wrong.

- Expected values must be hand-worked or taken from a known-good literal, never
  recomputed the way the code does.
- `screen_test.go` carries a terminal emulator — the only way to catch a stale
  cell. Assert on the reconstructed screen, not on bytes. It must be told the
  colour mode; quantisation is not a leak. A test double kinder than reality
  agrees with your bug.
- Deliberately **not** unit tested (need real processes): SIGSTOP pause, ffmpeg
  rebuild on seek/resize, live decoder, anything writing to a terminal. Run it.

### Driving it without a real TTY

Piped stdin gives misleading results (reopened `/dev/stdin` competes with fd 0).
Use a pty: `tools/ptysync.py` is in the repo, the rest live in
`/tmp/opencode/{ptydrive,termscreen,musiccheck,leakdecide,videockheck,
silentcheck,hanghunt,leakhunt}.py` (recreate if wiped).

**`tools/ptysync.py` implements DEC 2026 and nothing else does.** It holds every
byte written between `?2026h` and `?2026l` and only presents it at the `l`,
which is what Ghostty does. A plain pty ignores the mode, so a program that opens
it and never closes it looks *perfect* in every test and shows a black screen for
the entire track in real use — which is exactly what happened. It prints
`frames presented: N` and `still buffering at exit:` on stderr; N == 1 means the
picture only appeared at exit.

Never leave 2026 open across a select. Emit `syncOn` before the frame's first
write and `syncOff` after the last one in the same iteration, then flush.

- **Never run two at once** — they spawn/kill ffmpeg+mpv and reap each other's kids.
- **A synthetic clip needs an audio stream** or mpv exits instantly.
- **Locate HUD rows by content, never index** — chrome height changes with the strip.
- **A steady tone has no transients**; use `testdata/beat-12s.pcm` for onset work.
- **A copied screenshot shows nothing in colour mode** — ink is in the background.
  Dump cell attributes (`termscreen.py`'s `lit()`).

## Style

- Comments explain **why**, especially why a rejected alternative lost — never
  narrate what the code does.
- Every non-obvious constraint states what was **measured**, not assumed.
- Errors report what failed and the underlying reason. Never swallow one to keep a
  background task quiet — except the drift corrector, which stays silent by design.
