# Phase 7 — music mode, visualizers, and a spectrum that is a real player

## Goal

Music mode stops being a one-key demo and becomes the other half of the player.
Right now the spectrum view (`runVisualAudio`) has no transport, no HUD, no
duration, no chapters, no volume, no seek and no queue. It is the *default* mode
and the least finished thing in the repo. This phase makes it a peer of video
mode, then puts six visualizers behind a key.

Everything lands as character-grid renderers. There is no GPU, no shader and no
third-party Go module, because that is what this project is.

## The bug this phase is built on top of

**The level tap and mpv are handed the same URL.** `runVisualAudio` resolves one
`bestaudio` googlevideo URL and passes it to *both* `StartLevelTap`'s ffmpeg and
mpv. AGENT.MD says this exact thing is forbidden:

> Never pass an audio URL to ffmpeg as an input. It is never mapped, but ffmpeg
> opens it anyway, and googlevideo URLs are effectively single-use — mpv's own
> request then fails with 403.

So music mode today is one 403 away from silence, and the failure is
intermittent because the grant is a cache. This is not a new bug to work around;
it is a violation of the project's own hard rule, and phase 7 removes it by
construction rather than by luck.

## The enabler: mpv becomes the clock

Video mode has two children and a frame counter to derive a position from.
Music mode has one child and mpv can just be asked.

So `SyncPlayer` splits along the seam that already exists:

| Piece | Today | After |
|---|---|---|
| ffmpeg | started inline, frames only | `startVideoTap` — mpv and the level tap, no ffmpeg |
| mpv | started inline, after the first frame | `startMpv` — owns position, volume, seek, track end |
| `Seconds()` | `startAt + frames/fps` | mpv IPC position; frame count when video is on |
| `checkSync()` | measures and nudges | no-op in music mode: one clock cannot drift from itself |

Position from mpv IPC is *more* accurate than the frame counter, not less: it is
the sound card's own clock, reported by the process making the sound.

### The tap gets its own grant, and that is legal

Fixing the double-open means the tap and mpv each need their own URL. Two
options:

| Option | Cost | Verdict |
|---|---|---|
| One URL, one consumer | free | impossible — mpv owns audio and will not emit PCM |
| `yt-dlp -g` twice, two grants | ~0.5s per **track**, not per seek | **chosen** |

The rule in AGENT.MD is one input, one consumer, and it is a rule about *URLs*,
not about *streams*. Two separate `-g` calls produce two independent single-use
grants, so each consumer genuinely holds its own. The URL is then reused across
rebuilds within a track, exactly as `s.pair.videoURL` already is for video.

`resolveMedia` grows an audio-only sibling that reuses the **same single `yt-dlp
-J` request**, because duration and chapters ride along in that response for
free. Picking the video stream in a mode that never plays video is the only
waste, and it costs nothing to skip.

### Seek is a rebuild, and that is already the design

Both children restart on a seek in video mode already. Music mode is identical:
ffmpeg-tap from `-ss <t>`, mpv from `--start=<t>`. The alternative — leave the
tap running and accept that the visualizer drifts from the audio after every
seek — means a spectrum showing the wrong 30 seconds of the song, which is worse
than a 200ms hitch.

## Music mode is a `trackSession` flag, not a second player

Adding `musicOnly` to `trackSession` gets the render loop, the prompt, the
resize path, the stall handler and the queue for free. The alternative is a
parallel `runVisualMusic` with its own transport, and phase 6 is what that
duplication costs: it took a byte pump, a key router and a prompt state machine
to make one mode good. The flag costs one branch in `start()` and one in
`Seconds()`.

`runVisualAudio` is deleted. `playQueue` grows a `music bool`; both modes are
then the same code path with a different frame source.

## The visualizer framework

```go
type Viz interface {
    Name() string
    Resize(cols, rows int)      // cheap; called on SIGWINCH
    Push(*AudioFrame)           // bands, waveform, beat envelope, bpm
    Paint(*VizGrid)             // writes cells
    Heavy() bool                // capped particle count / lower fps
}
```

`VizGrid` is `cols*rows` cells, each holding a ramp index and an optional packed
RGB. Two painters convert it to the frame layouts the existing renderers already
speak — `DiffRenderer.Draw` for mono, `ColorDiffRenderer.Draw` for colour.

**This is the load-bearing decision.** Both renderers already implement diff
caching, SGR elision and the bandwidth maths, and `screen_test.go` already
proves they leave no stale cells. Reusing them verbatim means the pixel-leak
regression suite covers visualizer output too, for free. Writing a third painter
would mean re-proving all of that from scratch.

Colour cells set fg == bg so the half-block glyph renders solid. A cell with
nothing in it is near-black, not a space — a space would let the previous frame's
background bleed through the diff.

### The six styles

| Style | Grid | Needs | Cost |
|---|---|---|---|
| `bars` | baseline, up from the bottom | bands | cheap |
| `scope` | polyline across the full width | waveform window | cheap |
| `mirror` | bands mirrored about a centre axis | bands | cheap |
| `waterfall` | one history row per frame | bands | cheap, O(rows) copy |
| `radial` | bands from a centre point | bands | **heavy** |
| `particles` | dots on transients | bands + beat | **heavy** |

`scope` is the one that changes the tap: an FFT throws the time domain away, so
the tap has to keep a window of the last N samples alongside the bands. One extra
ring buffer in `LevelTap`, filled in the same loop that already decodes them.

`radial` has to correct for the 2:1 cell aspect. A circle drawn on an
uncorrected grid is an ellipse stretched to twice its height, which reads as a
rendering bug rather than as a circle. The radius maths multiplies y by the
aspect ratio — same `defaultAspect` the video path uses.

### Heavy styles are capped, not disabled

The user asked for low-end support. `Heavy()` styles get a particle budget and a
lower frame rate, and both are derived from the grid so a 300x120 terminal and a
80x24 one cost the same order of magnitude:

- particles: budget = `min(cols*rows/8, 400)`
- heavy fps: half of `fpsForGrid`

They still run everywhere. A style that only works on a big terminal is a style
most people never see.

## Palettes

| Palette | Hue from | Reads as |
|---|---|---|
| `mono` | — | ramp density only, no SGR |
| `spectrum` | band index | bass red → treble violet, the obvious one |
| `height` | cell height | every bar is a gradient of its own length |
| `ocean` | beat envelope | cool, low contrast, good on a dark terminal |

Hue maths is HSV→RGB, ~15 lines, and `quant256` already handles the 256-colour
fallback. Mono ignores all of it — `colorSupported` is the gate.

## Live switching

| Key | Action |
|---|---|
| `v` | next style |
| `V` | previous style |
| `c` | next palette |

`v` and `V` are new; `c` is free in playback (`CmdChapPrev` is `[`, `CmdChapNext`
is `]`). Style switching forces a repaint and clears per-style state — a
particle field carrying its old velocities into a waterfall would be garbage.

## Per-track memory

Read as: **the choice carries forward to the next song.** Not a lookup table keyed
by track ID — that is a cache with no evictor, and it would mean the second play
of a track looks different from the first.

`playQueue` owns a `*vizPrefs` and passes the pointer through `playOpts`. It
survives a queue advance and dies with the process. Default on first launch is
`bars` + `auto palette`, because bars at mono ramp density is the most legible
thing on an unknown terminal.

## Spectrum strip in video mode

A compact band row under the video, so watching a video still gives you visuals.

**It takes rows from the grid, not from inside the grid.** Overlaying the video's
bottom rows means fighting the diff cache for those cells and forcing a full
repaint every frame, which throws away the entire point of the diff renderer.
Instead `chromeRows` goes from 3 to 5 in strip mode, `l.rows` shrinks by two,
and toggling the strip is a layout recompute plus the rebuild that SIGWINCH
already does. Costs one rebuild per toggle, which is the right price for not
giving up diffing.

The strip itself is `miniBars(bands, width) string` — a pure function, the same
ramp chars the bars style already emits, plus SGR runs when colour is on. Tested
for its width invariant and nothing else, because it is a formatter.

## Beat detection

Spectral flux over the band array, thresholded against a rolling mean:

```
flux = Σ max(0, bands[i] - prev[i])     positive difference only
onset when flux > mean(flux window) * 1.6
```

Positive difference only, because a decaying spectrum is a release and firing on
it would produce a second onset per note. The threshold is adaptive rather than
fixed because absolute band levels vary enormously between a lofi track and a
mastered one, and a fixed threshold works on exactly one of them.

`bpm` comes from the median interval between the last eight onsets, clamped to
60-180. Median, not mean, because one missed onset between two beats should not
move the answer. **It is an estimate and the HUD says so** — no tempo detector
that fits in a render loop is right, and one that claims to be right is worse
than one that admits it is guessing.

The envelope decays at `0.88` per frame, which at 24fps is about 200ms of glow.
The strobe is that envelope, never a raw threshold, because a hard threshold
strobes on the subdivisions of a beat.

## Files touched

| File | Change |
|---|---|
| `sync.go` | `startMpv` / `startVideoTap` split; `SyncPlayer.musicOnly`; `Seconds()` via IPC; `checkSync` no-op |
| `ascii.go` | `resolveAudioPair` (same `-J` request, audio stream only); `musicGrid` layout |
| `session.go` | `trackSession.musicOnly`; PCM goroutine feeding the same render loop; layout for music mode |
| `player.go` | `CmdVizNext` / `CmdVizPrev` / `CmdPalette` + key table |
| `visual.go` | delete `runVisualAudio`; keep `LevelTap` + `Visualizer`, add waveform ring |
| `viz.go` | **new** — `Viz`, `VizGrid`, `AudioFrame`, painters, registry |
| `styles.go` | **new** — the six styles |
| `palette.go` | **new** — HSV→RGB, the four palettes |
| `beat.go` | **new** — flux, onsets, bpm, envelope |
| `hud.go` | `miniBars`; `strip` field; style + palette + bpm in the footer |
| `main.go` | `--music` / `-M`; prefs plumbing; usage |
| `main_test.go` | style switching, prefs carry-over, strip invariant, beat on a synthetic flux train |
| `viz_test.go` | **new** — each style paints a known grid from a known band array; cell/ramp coverage |

## Bugs

The first six were predicted. The last four were not, and each of those took real
time to find — which is the argument for writing this section down at all.

### Predicted, and confirmed

1. **The strip costs a grid row, and `computeLayout` clamps `availRows` to 4.** At
   `--rows 8` the video is what has to shrink.
2. **`Seconds()` returning 0 on the first tick.** mpv's IPC reports 0 until it has
   probed, so the clock showed `0:00` for the first half second of every seek.
3. **A seek in music mode rebuilds ffmpeg *and* mpv**, so the tap's FFT window
   crosses the boundary. `viz.Reset()` on rebuild.
4. **Stale cells when switching style mid-frame.** Each `Viz` keeps its own history,
   so `Paint` for style B does not cover every cell style A wrote.
5. **The colour path's cursor/SGR bookkeeping.** Predicted as a risk, then dissolved
   by the design: `VizGrid` emits no SGR of its own, it only fills the byte buffer
   the renderer already reads. Prediction 5 was wrong because prediction 0 (reuse
   the renderers) was right.
6. **Particle state at 300x120.** Capped at construction, so the slice is never
   `cols*rows` long.

### Not predicted

7. **The transport error line was overwritten before it was ever visible.** The
   existing code wrote it straight to the footer row; `feedKeys` then called
   `paintHUD(true)` a few lines later and wiped it, so a failed seek looked like no
   keypress at all. Fixed by routing it *and* the new status messages through
   `hud.status`, so the HUD's own skip cache treats them as content.

8. **`ForceNext` blanks the chrome.** Predicted for style switching, and correct —
   but the trap has a second half that is easy to miss: the forced repaint clears
   the *whole screen*, so `paintHUD`'s "unchanged, skip" cache then decides there is
   nothing to repaint and the title, clock and footer stay blank until something
   else changes them. Every `ForceNext` caller has to clear `hudText`.

9. **The library could not see audio files.** `.m4a`/`.mp3`/`.flac` were not in
   `mediaExts`, so a music folder scanned to nothing at all.

10. **`probeMedia` never asked for streams.** No `-show_streams`, so `hasAudio` was
    *always* false and every local file was classified silent. In video mode that
    silently downgraded track-end detection to the grace timer; in music mode it
    refused to play the audio-only file the mode exists for. Found because the first
    music-mode pty run refused to start.

11. **The level tap had no `-re`.** The big one. ffmpeg analysed the entire track in
    under a second, exited, and the visualizer showed its final frame for the rest
    of the playback. Every symptom pointed somewhere else: the spectrum looked
    frozen, the onset envelope stalled at 0.193, the particle field fired once, the
    bars stopped moving. It cost two wrong diagnoses before a trace showed the
    envelope *freezing* rather than the detector failing.

12. **The onset detector read the smoothed magnitudes.** Slow release means each
    transient's rise shrinks by ~18% against the next, so onsets decayed to nothing
    over a few bars.

13. **The onset threshold was a mean.** Onsets are rare, so a mean is dragged up by
    the events it should detect. On a periodic signal the mean *is* the transient,
    and `mean * 1.6` is a threshold nothing can cross.

12 and 13 are why `testdata/beat-12s.pcm` exists. Every unit test passed while the
feature was broken, because each piece was individually correct and the bug was in
the seam between them. The assertion that actually catches it is "did the field
still have particles at the *end* of the track" — not "did it ever have any", and
not "how many are alive": 25 were alive and all 25 had drifted off the grid.

### The automatic gain

Four more, all in `fft.go`, and three of the four were only findable by building
audio designed to break them. The fixtures in play before this were one 60-second
file with no dynamic range at all, which cannot exercise a gain that only misbehaves
when the signal *changes character*. `genstress.py` produces eight segments — loud,
silence, quiet, flat noise at two levels — and `musicraw/` holds real tracks for the
other direction.

14. **The gain's high-water mark never fell.** The guard read `peakDb > -infDb`,
    i.e. `> +140`, so the subtraction never ran. `peakDb` was a pure all-time
    maximum. Every downstream claim about the gain adapting was therefore false,
    including a comment describing exactly that behaviour — and nothing failed
    loudly, because a gain that never falls only fails to adapt. Fixed to
    `peakDb > infDb`; 400 silent analyses now walk it down 480dB instead of leaving
    it at 41.5.

15. **The gain's floor was unbounded below, so broadband noise normalised against
    itself.** `agcGateDb` was added as the fix: a -120dBFS dither tone had lit 48 of
    48 bands with a mean of 0.825 — a full-scale block where the signal was
    inaudible.

    The gate is only good for what is *sub-audible*. A -74dBFS noise floor measures
    -46dB per band, **34dB above the gate**, so it still drew 48/48 bands with a
    mean of 0.738. No absolute threshold fixes this: a -60dBFS tone measures
    -18.8dB per band, so level alone cannot separate audible noise from quiet music.

16. **Two wrong answers to bug 15, both worth recording.**

    *Attempt one — gate on shape, measured on the display.* Using mean/max of the
    normalised bands: noise read 0.75-0.88, tonal fixtures read 0.04-0.11, so it
    looked decisive. **Measured against three real tracks it read 0.42-0.80** —
    fully overlapping the noise — and the ramp as set would have dimmed real music
    by up to 99%. The synthetic tones lied about what music looks like.

    *Attempt two — same metric, still on the display, worse.* Normalisation clamps
    at the floor, and a clamp manufactures contrast: a quiet frame's bands land at
    exactly zero except a few just above, which looks like structure. The gate then
    **inverted** — -58dBFS noise passed as *less* flat and drew brighter than
    -78dBFS noise. Quieter material rendered on top of louder material.

    The fix for both: measure shape in dB, on the raw band levels, *before* the
    floor and the clamp — so it describes the signal rather than the display. On its
    own that still fails, because music's flattest frames (p01 5.27dB) touch noise's
    (p95 5.18dB).

17. **What does separate them is that they differ for different reasons.** Music's
    flattest moments are *loud* — its quiet passages are sparse, hence
    high-contrast — and noise is quiet at every moment. Measured over 1452 music
    frames and 1072 noise frames:

    ```
                     p05        p50        p95
    music level   -28.4 dBFS  -12.8 dBFS  -9.1 dBFS
    noise level   -98.9 dBFS  -78.8 dBFS  -58.6 dBFS

    stdDb <= 6 AND level <= -50 dBFS  ->  music gated 0.0%   noise gated 97.3%
    ```

    The gate is now two conditions ANDed, so "flat" means quiet *and* structureless.
    Verified end to end: real music renders 43.4% of the panel against the old
    build's 43.5% (identical min/max), while the stress track's flat-noise segment
    went from 41% of the grid to essentially zero.

    Note the band levels are **not** dBFS — they are unnormalised FFT magnitudes
    carrying a +44.2dB offset — which is why `agcGateDb`'s "dBFS" claim never meant
    what it said, and why `levelDbfs` is measured off the input samples instead.

## Verification

1. `go vet ./...`
2. `go test ./...`
3. `go build -ldflags="-s -w" -o vspz-yt-cli .`
4. Real pty, real track, in **both** modes: transport keys, seek, volume,
   chapters, `:` jump, queue advance on track end.
5. `v` through all six styles at 80x24 and at 200x60, mono and colour, and
   **after a resize** — 5 is the bug that only a real terminal shows.
6. `time` the music-mode binary on a track with the terminal covered vs open, and
   confirm no CPU spin when the screen is static.
7. `--rows 8` in both modes, strip on and off. Nothing may panic.

## Status

**done**, with one decision deliberately left open.

Verified: `go vet`, `go test` (also under `-race`) and the build all clean. In a
real pty: music mode across all six styles, colour and mono, all four palettes,
transport keys, the `:` prompt, the strip toggle and a clean exit; video mode with
the strip on and off, and live SIGWINCH resizes down to 60x18 and back up.

### Left open

**Ordering for an audio library.** Audio containers are now recognised, but
`libraryTrack` still requires an episode marker, so `17 - Artist - Song.mp3` lands
in position 17 while a bare `Song.mp3` is skipped. That is the right default — an
arbitrary position is worse than none — but a music folder almost certainly wants
something else, and what it wants is a question rather than an assumption. Disk
number prefixes, a `trackNN` convention and a sidecar ordering file are all
plausible.