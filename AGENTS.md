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

**Concurrency / lifecycle**
- **Never close the video pipe before killing ffmpeg, and never `Wait` on mpv.**
  Both hang the render loop (which then ignores `q`). `Close` signals `SIGCONT`
  before `SIGKILL`; ffmpeg wait is bounded by `childReapTimeout`. `mpv` is reaped
  by the goroutine `start` launches.
- **Decode keys in the render loop, never in the reader goroutine.** `readKeys`
  is a byte pump; `keyRouter` owns routing so a `:` prompt is testable.
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

**Rendering / layout**
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
- **SGR dedup state is separate from cursor state, on purpose.** `haveLast` is
  about cursor contiguity and is cleared at the end of every row; the emitted SGR
  survives that. `emitColor` keys off `sgrValid`/`sgrFG`/`sgrBG` instead, so a
  still-active colour is not re-emitted at the top of every row. Merging them back
  costs one SGR per row per frame and nothing catches it.
- **`Push` happens at analysis rate, not render rate.** Use `LevelTap.TryFrame`,
  which reports false until a new FFT window lands. `Frame()` plus an
  unconditional `Push` makes the waterfall scroll, particles age and the smoother
  release ~3x faster than the music, because the render tick is up to 30Hz and the
  analysis is ~11Hz.
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
main.go      arg parsing, playQueue (owns the queue index; playTrack never mutates it)
search.go    ytfzf/yt-dlp search -> []Track       library.go  dir scan -> []Track
tui.go       browse list (+ line-mode fallback)   session.go  trackSession, decoder goroutine, render loop
player.go    Player: transport, byte routing, : prompt   sync.go  SyncPlayer: ffmpeg video + mpv audio
ipc.go       mpv JSON IPC                         hud.go    title / progress / parseTimestamp / miniBars
render.go    mono diff renderer + overlay          color.go  colour renderer, glyph probe
flow.go      covered-terminal stall handling      fft.go    FFT, log-spaced bands
visual.go    LevelTap + display smoother          viz.go    VizGrid, style registry, prefs
styles_*.go  the six visualisers                  palette.go HSV -> RGB, ten schemes
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
Use a pty: `/tmp/opencode/{ptydrive,termscreen,musiccheck,leakdecide,videockeck,
silentcheck,hanghunt,leakhunt}.py` (recreate if wiped). Hard-won rules:

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
