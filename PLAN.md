# rasterbar — make it a real media player

## Goal

Today it searches, plays one track, exits. A *player* has a queue, transport
controls, and tells you where you are. This plan adds those three, in that order,
because they are not independent: transport controls are **blocked** by the
current architecture until the render loop stops blocking on decode.

## Verified constraints (carried forward — do not retry)

| Fact | Evidence |
|---|---|
| `ytfzf -c yt` is the working scraper | prints "Scaping Youtube", fzf launches under PTY |
| `ytfzf` needs a TTY | non-interactive shell → `inappropriate ioctl for device` |
| `mpv --vo=aa` NOT available | `Video output aa not found!` — not compiled in |
| ffmpeg CANNOT open YouTube URLs | `Invalid data found when processing input` |
| yt-dlp must resolve URL first | `yt-dlp -f 136 -g` → googlevideo URL |
| googlevideo URLs are effectively single-use | probing one burned the grant → `403 Forbidden` on ffmpeg's later request (`ascii.go:236`) |
| one ffmpeg writing video pipe + audio FIFO deadlocks | 0 bytes produced (`sync.go:98`) |
| mpv CREATES+BINDS its IPC socket | we are the client (`ipc.go:25`) |
| mpv sets `"error":"success"` on good replies | testing non-empty error rejects every answer (`ipc.go:89`) |
| ASCII pipe works | 106704/106704 bytes, 0 ffmpeg errors |
| Format IDs 130-399 only | no progressive `18`; `worst` selector fails |

Dead ends: `mpv --vo=aa`, ffmpeg-on-YouTube-URL, `yt-dlp -f worst`, single-ffmpeg
dual-output.

## What is actually missing

| Gap | Evidence |
|---|---|
| **No transport at all.** No pause, seek, next/prev, volume. | `watchQuit` (`visual.go:225`) reads one byte and discards everything except `q`/`0x03`. The keyboard is not even wired to a handler. |
| **No position UI.** No time, no duration, no progress bar. | `computeLayout` reserves `chromeRows = 3` for "title bar and key hints" (`ascii.go:72`) and **nothing ever draws them**. Three blank rows. |
| **One track, then exit.** A 10-result search uses 1 result. | `main()` plays a single track and returns (`main.go:201`); `WaitAudioEnd` ends the process. |
| **The render loop cannot accept input.** | `SyncPlayer.Next()` blocks in `io.ReadFull` on an `-re`-paced pipe (`sync.go:273`), inside one monolithic `select` with no key case (`ascii.go:556`). |

That last row is the whole problem. Pause needs the loop to keep running while
decode is suspended; seek needs to restart ffmpeg at an offset from inside the
loop. Neither is possible while the loop's only exit is "frame arrived, EOF, or
quit".

## Approach

### Phase 1 — split decode from render (the enabler)

Move `io.ReadFull` into a decoder goroutine feeding a buffered channel (cap 2).
The render loop then selects over `{frame, key, resize, quit, audio-end}` and
never blocks. Everything after this is additive.

- Backpressure is unchanged: cap 2 means ffmpeg blocks on write when we render
  slowly, which is the existing natural throttle. No unbounded memory.
- **Pause = the code we already have.** `pauseChildren`/`resumeChildren`
  (`flow.go:47`) SIGSTOP both children in the same instant, so both clocks freeze
  and sync is preserved exactly. Pause just stops the loop drawing and paints a
  `PAUSED` overlay. Zero new mechanism — this is the stall handler promoted to a
  feature.
- **Seek = the code we already have.** `restartAt(pos)` (`ascii.go:529`) already
  rebuilds both children at a media offset for resize. Seek reuses it.

### Phase 2 — transport keys → mpv IPC

`mpvIPC` gains `setProperty(name, value)` — pause and volume are one property
each. Seek on video needs the ffmpeg rebuild; seek on audio is `ipc.seek`, which
exists.

`space` pause · `←`/`→` seek ∓10s · `n`/`p` next/prev · `+/-` volume · `q` quit

**Seek cost is the real risk.** `restartAt` re-runs `resolveMedia`, which is a
`yt-dlp -J` network round trip (~1s) — unusable on a held arrow key. Resolve once
per track, reuse the URLs for seeks, and re-resolve only if ffmpeg fails. Must be
measured, not assumed.

### Phase 3 — the HUD that was always reserved

Fill the 3 rows `computeLayout` already budgets. No renderer change:
`l.rows` already excludes chrome, so the player owns the lines below it.

- row 1: title + channel
- row 2: progress bar `▐▓▓▓░░░░░▌ 1:23 / 4:56`
- row 3: key hints, fading out after a few seconds of no input

Duration comes free from the existing `yt-dlp -J` call — one field on `ytInfo`.
No extra request, no ffprobe (which would burn the URL, see constraints).

Redraw the HUD at ~10Hz, not per frame: the position only changes once a second.

### Phase 4 — the queue

The search result list *is* the queue. On track end, advance to the next one and
keep playing. Stop at the end of the list (no wrap, no invented loop mode).

This needs `runASCII` to report *why* it stopped — a small outcome enum
(`ended` / `quit` / `next` / `prev` / `error`) instead of `error`. Real signature
change, and the reason it goes in its own phase: it touches every return path.

### Phase 5 — local files (optional, needs a decision)

Play a path or `file://` URL instead of searching. Skips `Search` and
`resolveMediaPair` entirely — ffmpeg reads the file — and duration comes from
ffprobe, which is safe for local files. This is the difference between a YouTube
player and a media player, and it changes what the binary *is*.

## Files touched

| File | Change |
|---|---|
| `player.go` *(new)* | transport state machine, `Outcome`, key decoding (`decodeStream`), `readKeys` |
| `session.go` *(new)* | `trackSession` media backend, decoder goroutine + frame channel, the render loop |
| `hud.go` *(new)* | title, progress bar, hints — pure formatting |
| `ipc.go` | `setProperty`, `marshalCommand`, `parsePropReply`; volume |
| `ascii.go` | dropped `runASCII`; `resolveMedia` now returns duration too |
| `sync.go` | `startAt` in `Seconds()`; audio URL no longer passed to ffmpeg; no `--idle` |
| `flow.go` | `awaitDrain` drains keys while suspended |
| `raw.go` | `makeRawVT` — VTIME reads so escape sequences can be parsed |
| `main.go` | `playQueue` owns the queue index; one key reader per session |
| `main_test.go` | tests at all four seams |

Deviation from the original plan: the render loop went into a new `session.go`
rather than `ascii.go`, which was already 665 lines and would have passed 900.

## Rejected alternatives

- **bubbletea / lipgloss.** Rejected before this plan and still rejected — but
  this is the phase where a state-machine library would normally earn its keep,
  so the reasoning is worth stating: the player has ~6 states and one `select`
  loop, and the UI it draws is three lines of chrome plus a progress bar. A
  framework would add a dependency to render a string. Stdlib stays.
- **Drop `-re` and pace frames in Go.** This would make pause and seek much
  cleaner — we would own the clock outright. Rejected: it destroys the structural
  A/V sync this repo already fights for, and hands back the drift problem the
  `checkSync` corrector exists to solve. Drift correction stays.
- **Let mpv render the video.** No ASCII video output in mpv (`--vo=aa` verified
  absent). mpv owns audio, ffmpeg owns video. That split is the design.
- **A `FrameRenderer` per frame mode.** Already exists (`newRenderer`), keep it.

## Bugs found while building this

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

## Verification

- `go vet ./...`, `go test ./...`, static build — all pass.
- Driven through a real pty, against live YouTube streams: `seek` (`0:10 → 0:20 →
  0:30`), `back` (jumps back, clamps at 0), `pause` (overlay, resumes), `next`
  (advances the queue), `vol`, and `auto` (a track ends and the next one starts on
  its own). A/V drift measured steady at ~40-100ms with no corrections.

## Not done

Transport keys and queue advance are **ASCII-only**. The spectrum/audio view
(`runVisualAudio`) still plays one track and exits — it has no key handling at
all, and making it a transport means applying the same decoder split there.

`f` to cycle the grid size was in the original Phase 2 key list and was cut: it
is a convenience, not part of being a player, and it would have meant a new
command plus a preset table late in the change. Everything else in Phases 1-4
shipped.

## Status

phases 1-4 done and verified; phase 5 (local files) not started, not agreed

## Risks

| Risk | Mitigation |
|---|---|
| Seek re-hits the network per press | resolve once per track; re-resolve only on ffmpeg failure. Measure. |
| Seek while paused leaves the player paused-but-rebuilt | make pause a player state, not a signal side effect; assert it in a test |
| Live streams have no duration | HUD degrades to elapsed-only, no bar |
| Per-frame key polling costs CPU | reuse the existing stdin reader goroutine; bytes are already read |
| Chrome rows collide with a full-screen grid | `computeLayout` already subtracts them |

## Verification

Run once at the end, as one chained command — not after every edit.

1. `go vet ./...`
2. `go test ./...` — 63 tests exist today at 41.9%; new units (frame channel,
   outcome enum, HUD bar formatting, seek position arithmetic) get direct tests
3. `go build -ldflags="-s -w" -o rasterbar .`
4. Manual, against a real track: pause holds sync · seek lands where the bar
   says · volume responds · track end advances to the next result · `q` leaves a
   clean terminal

## Status

planned — not started