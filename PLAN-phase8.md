# PLAN-phase8 — ten gradient palettes, direct select

## Goal

Ten colour palettes selectable by number key during playback (`1`–`9`, `0`),
every one of them a gradient. Includes black-and-white entries that still
gradient — which is *not* the existing `mono` flag, since `mono: true` means
"draw no colour at all" and is flat by definition.

## Palette set

| key | index | name | gradient axis | status |
|-----|-------|------|---------------|--------|
| 1 | 0 | spectrum | band (hue sweep 0–0.78) | existing |
| 2 | 1 | height | value | existing |
| 3 | 2 | ocean | band (narrow, 0.50–0.65) | existing |
| 4 | 3 | ember | band (warm, 0.02–0.12) | existing |
| 5 | 4 | **graphite** | value, pure grey | new |
| 6 | 5 | **ink** | band, pure grey | new |
| 7 | 6 | **ice** | value, navy→white | new |
| 8 | 7 | **magma** | value, black→red→orange→yellow | new |
| 9 | 8 | **viridis** | band+value, purple→teal→yellow | new |
| 0 | 9 | mono | none (no colour) | existing, last |

`graphite` and `ink` are the black-and-white pair, and they differ the way the
existing coloured pairs do: `graphite` gradients by value so every bar is a
gradient of its own length, `ink` gradients by band so bass is dark and treble is
light. Same split as `height` vs `spectrum`.

`mono` stays last on purpose. The ordering is load-bearing — index 0 must be a
real colour, because a `mono` default drew a black grid on a black terminal.

## Approach

- `palette` gains no fields. Each new palette is `fn(band, val, beat) uint32`
  arithmetic, matching the file's existing "no tables" rationale (the 256-colour
  path quantises whatever we produce; interpolated RGB degrades gracefully).
- Digit keys are free during playback: browse-mode `1-9` is a different router
  (`tui.go`), and `decodeStream` binds no digits.
- `Cmd` is an int enum with no payload, so the digit rides as an offset from a
  named base rather than ten constants:

  ```go
  CmdPaletteSelect   // CmdPaletteSelect+n selects palette n
  ```

  One case in the decoder, one range check in the render loop, instead of ten of
  each. The base is a named constant both sides derive from, so they cannot drift.
- The render-loop handler is reached as a range guard *before* the switch, because
  Go cannot spell a range in a `case`.
- Same guard rails `c` already has: music mode only, and a message rather than a
  dead key when colour is off.

## Files touched

- `palette.go` — five `fn`s, five `palettes` entries, their `bg` tints
- `player.go` — `CmdPaletteSelect` constant; digit cases in the key table
- `session.go` — range guard, select, `ForceNext`, `hudText = ""`, `reportLine`
- `main.go` — `--help` keys line
- `viz_test.go`, `player_test`-style tests in `main_test.go` — regression tests

## Verification

1. `gofmt -l . && go vet ./... && go test -count=1 ./... && go build ...`
2. New tests:
   - digits `1`–`0` decode to indices 0–9 and nothing else does
   - **every** non-`mono` palette varies with `val` — the user's explicit
     "keep the gradient in every palette", asserted rather than assumed
   - `palettes[0].mono == false`, and `len(palettes) == 10`
   - every palette's cells contain no control character (the lesson of bug 19 —
     `miniBars` shipped 192 of them)
3. Raw pty: press digits, confirm zero CRLF and no CUP past the last row
4. `go version -m` on the installed binary for the revision stamp

## Status

**done.**

Verified: `gofmt -l`, `go vet`, `go test -count=1`, `go test -race`, build — all clean.
On a real 192x47 terminal, digits `5 6 7 8 9 0 1` put
`graphite · ink · ice · magma · viridis · mono · spectrum` in the footer, and each
palette's unlit-cell background came back as its designed tint:

| key | palette | bg observed | designed |
|-----|---------|-------------|----------|
| 5 | graphite | `18;18;18` | 0.07,0.07,0.07 |
| 6 | ink | `15;15;18` | 0.06,0.06,0.07 |
| 7 | ice | `10;18;31` | 0.04,0.07,0.12 |
| 8 | magma | `28;8;13` | 0.11,0.03,0.05 |
| 9 | viridis | `13;18;20` | 0.05,0.07,0.08 |
| 1 | spectrum | `26;8;31` | 0.10,0.03,0.12 |

Distinct colours per palette ranged 35 (magma) to 1924 (viridis), so the ramps are
real rather than a constant fill.

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
was only true because nothing mappable appeared in sequence parameters before.