# Post-mortems

Every bug this project hit, and what it cost to find. Numbering restarts per
phase, as it did in the plans these were lifted from.

The plans themselves — goals, phases, file lists, verification checklists — are
gone. They were working documents, they had drifted, and `git log` keeps them
permanently anyway. What was *not* reproducible from the code or from
`AGENT.MD` is here: the measurement behind each decision, and the wrong turns
that led to the right one.

The rules that came out of these live in `AGENT.MD`. Do not add a rule here —
add it there, and cross-reference.

---

## Phase 8 — ten gradient palettes

### One bug this introduced, and what caused it

Binding digits broke `TestDecodeKeysIgnoresUnknownBytes`, and the cause was not the
key table. `decodeOne` consumed exactly **three bytes** of any CSI sequence, so a
bracketed paste marker — `\x1b[200~`, six bytes — left `00~` to be decoded as
ordinary keystrokes. Every unmapped byte meant `CmdNone`, so nothing showed. With a
digit meaning palette-select, pasting anything at all would have silently switched
the palette to mono.

The fix is in the decoder, not the table: scan to the CSI final byte (the first in
`0x40-0x7E`) and consume the whole sequence, bounded by `csiLimit` so a truncated
one cannot wedge it. Pinned by `TestDigitsDoNotLeakFromUnknownSequences` and
`TestCsiSequenceIsConsumedWhole`.

This is the same lesson as bug 19 in `PLAN-phase7.md` and it is now in `AGENT.MD`:
the decoder's guarantee that an unhandled sequence cannot be mistaken for a command

---

## Phase 7 — music mode, visualizers, the spectrum

### Bugs

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

18. **`ColorDiffRenderer.Draw` ended every row with an unconditional `\r\n`.**

    LF while the cursor sits on the terminal's last row is a *scroll*: the whole
    screen shifts up one row. The diff cache is untouched by that, so from the
    next frame the cache and the terminal disagree permanently — every cell the
    cache thinks is unchanged gets skipped, and it keeps showing whatever row
    scrolled into its place. New frame draws, old frame slides up underneath,
    ghost that no later frame can clear. Exactly the reported symptom.

    Normal geometry should not reach it: `computeLayout` reserves
    `chromeRows >= 3`, so the grid's last row sits three rows above the bottom.
    It becomes reachable when the renderer's row count is stale — a resize-down
    is handled in its own `select` arm, so frames drawn before SIGWINCH is
    processed still CUP past the new bottom, clamp to the last row, and then hit
    that `\r\n`.

    It is removed rather than guarded, because it had no job: `haveLast` is
    cleared at the end of each row, so the first cell of the next row emits an
    absolute CUP, and a mid-row run is repositioned by the same check. The old
    comment even admitted "the cursor is repositioned explicitly anyway".

    **Why it survived every test:** `screen_test.go`'s emulator did
    `case '\n': s.curY++` and let `put` drop off-screen writes. The cursor
    wandered past the last row, the rest of the frame was silently discarded, and
    the assertion still compared the *pre-scroll* cells against the frame. The
    emulator now scrolls — `scrollUp()` — and with it honest,
    `TestColorRendererScreenMatchesLastFrame` fails on all four colour/glyph
    combinations (`cell (17,0): bg ffffff want 000000` — row 0 showing row 1's
    content) and passes only after the `\r\n` is gone. Mono never failed, because
    `DiffRenderer` never emitted a newline.

19. **The actual bug: `miniBars` wrote ramp *indices* as *bytes*.**

    `rampFor` returns a ramp index, not a glyph, because `VizGrid.Set` takes an
    index and converts it via `rampLum` itself. Every style is therefore written
    that way and none of them can be wrong this way. `miniBars` builds a *string*
    for the HUD strip, and it assigned the index straight into the bytes:

    ```go
    cells[i] = rampFor(0.15 + 0.85*v)   // index, used as a character
    ```

    Every index below 32 is a control character. **Index 10 is LF**, 13 is CR, 9
    is TAB. Measured on a 192x47 terminal, music mode, strip on:

    ```
    h.strip = "\n\n\n\n...192 of them..."
    ```

    The strip row is row 46 of 47, and `paintHUD` writes each line with an
    absolute CUP. So the strip wrote ~192 newlines at the bottom of the terminal
    and **scrolled the entire screen away** — 4224 CRLF in six seconds, on every
    HUD repaint, which the clock drives several times a second.

    The diff cache cannot see a scroll. After one, it still believed every cell
    was where it left it, so it skipped them and the grid lost both its content
    and its palette background. Sampled painted-cell counts over ten seconds went
    `2526 1769 1596 623 0 2573 1723 937 1807 0 ...` — the screen fully blank
    roughly a third of the time.

    **How this survived, and how it was finally caught.** Nothing in the unit
    suite touched `miniBars`' output as *text*: `TestHudStripAddsARow` counts
    rows, and the strip row was the right length (192) with the right rune count
    (192). It was wrong characters. The screen emulator could not see it either,
    because the damage was a scroll rather than a cell. What finally showed it
    was reading the program's **raw pty output** instead of the screen:
    `CRLF x144` attributed to `CUP 46;1H`, and `h.strip` printing as 192
    newlines.

    Fixed with `ramp[rampFor(...)]`, plus `TestHudLinesCarryNoControlCharacters`
    and `TestMiniBarsWritesGlyphsNotIndices`. CRLF in six seconds: **4227 -> 3**.
    Palette-background cells on screen: **145 -> 1708**. All 47 rows populated,
    previously only 5-41 plus the footer.

    Bug 18 in this list was real and worth fixing, but it was not this. The
    per-style differences (particles least affected, waterfall and radial worst)
    were never style differences at all — they were how much visual damage a
    full-screen scroll does to a style that happens to be dense or sparse.


---

## Phase 6 — jump-to-time, fine seek, chapters, pixel leak

### Bugs found while building this

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


---

## Phases 1–5 — the player itself

### Bugs found while building this

Found by running the thing, not by reading it. All are fixed with tests.

1. **`Seconds()` ignored the resume offset** (`sync.go`). A player rebuilt at
   `-ss 300` reported position 0, so `checkSync` saw 300s of drift and seeked the
   audio back to the start — every terminal resize rewound the track.
2. **The audio URL was passed to ffmpeg as an unused input.** ffmpeg opened it,
   spent the single-use grant, and mpv got `403 Forbidden` on its own request.
   There was no audio at all.
3. **Escape sequences split across reads were dropped.** `decodeKeys` was
   stateless per read, so `ESC` in one read and `[C` in the next produced two
   `CmdNone` and the arrow was lost. Now `decodeStream` carries the incomplete
   tail — which is the entire reason `VTIME` exists.
4. **`readKeys` blocked forever after the browse list read stdin.** The TUI's
   abandoned `bufio.Reader` left `os.Stdin` in a state where a later direct read
   never returned; the bytes sat unread and every transport key was silently
   dead. Reopening `/dev/stdin` reads them. Root cause not fully explained — see
   the comment on `readKeys`.
5. **One key reader per track.** After a queue advance the previous goroutine was
   still blocked in `Read` and raced the new one for stdin, so `q` did nothing.
   The reader now belongs to the session (`playQueue`), not the track.
6. **`mpv --idle=yes` meant track end was unobservable.** mpv survives end-of-file,
   so it never exited and `WaitAudioEnd` never returned. Harmless in the old loop
   (ffmpeg's EOF ended playback) but fatal once the queue listens for the audio to
   end — it hung on the last frame of every track.
7. **Volume while paused blocked for 2s.** mpv is SIGSTOPped and cannot answer an
   IPC call. Volume is now recorded and applied on resume.

