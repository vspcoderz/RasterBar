# PLAN — more visualisers, and a scope worth looking at

Status: done

Result: 6 → 18 styles, all at **0 allocs/op**, all painting under DEC 2026 in a
pty. `scope` rewritten: 40.8 cycles of a 440Hz note across an 80-column screen
becomes 2.5, triggered and standing still. `BPM` finally has a consumer.

## The scope was not "meh" because of its line style

Measured, from the constants: `waveWindow = fftSize = 1024` samples at
`spectrumHz = 11025`, so the scope was handed **92.9ms of audio** and asked to
draw it across the terminal's width.

| note | cycles in 92.9ms | columns per cycle at 80 cols |
|---|---|---|
| 55Hz bass | 5.1 | 16 |
| 220Hz mid | 20.4 | 3.9 |
| 440Hz treble | 40.8 | **2.0** |

Past a few hundred Hz there are more cycles than columns, so the trace aliased
into a solid band and every note looked the same. Three smaller faults sat on top
of it: no trigger (the trace slid with the note's phase), a column **mean** (a
lowpass, which cancels an asymmetric waveform toward zero), and no persistence.

Fixed by zoom (~2.5 cycles of the dominant band, 62 samples at 440Hz), a rising
zero-crossing trigger, a min/max beam, and a phosphor trail. Zoom needed a
band→Hz mapping no style had, so `bandHz` went into fft.go next to the
constructor that builds the same layout privately, with
`TestBandHzAgreesWithTheAnalyzersEdges` holding the two copies together.

## What shipped

| axis | styles |
|---|---|
| waveform | `lissajous` `ribbon` `swell` (+ the rewritten `scope`) |
| spectrum | `matrix` `peaks` `rays` `wheel` `helix` `terrain` |
| beat / tempo | `bloom` `metro` |
| field | `aurora` (the only `Heavy()`) |

Shared: `phosphor` (the trail buffer), `lerp`/`smoothstep`/`vnoise`,
`analysisHz`, `bandHz`, `dominantBand`, `maxFloat`.

## Bugs this batch found, which is the point of the plan

| found by | bug |
|---|---|
| `TestResetDropsEveryStyleState` (new) | aurora lit **654 of 672 cells in silence** — value noise is 0..1 by construction, so the picture had stopped reacting to anything. Now gated by level. |
| `TestResetDropsEveryStyleState` (new) | ribbon drew a lit hairline from bands alone. Needed a `haveWave` flag: a zero envelope is not the same as no signal. |
| `TestStylesOnlyDrawFromTheirOwnInput` | ribbon again, same cause. |
| `TestMetroFrontsAreLegible` (new) | one cell of travel per beat = a 1-cell spacing at 128bpm, so **every cell lit every frame**: 1.2ms/op and a uniform glow instead of rings. |
| `go vet` | `analysisHz = spectrumHz/fftSize` is **integer division** — 10, not 10.766. A tempo 7.7% slow. |
| `BenchmarkEveryStylePaint` (new) | metro at 2.67ms/op was the worst style in the registry while declaring itself cheap. `math.Hypot`/`Mod`/`Exp` per cell → `sqrt`/floor-subtract/`smoothstep`: **697µs**. |
| driving the binary | `help` still listed six styles. The list is now generated from `VizNames()`. |
| my own test | matrix and metro draw *on purpose* on silence, so the Reset test compares against a **control instance** rather than against zero lit cells. |

The Reset test caught two real bugs on its first run. It is now the most valuable
test in the file for a future nineteenth style.

## Verification

```
go vet ./... && go test ./... && go build -ldflags="-s -w" -o rasterbar ./src
```
All pass, `gofmt` clean. Full suite ~9s.

Paint cost, 200x60, `BenchmarkEveryStylePaint` (registry-driven, so the next
style is measured on arrival): **all 18 at 0 allocs/op**, 23µs (bloom) to 1.51ms
(aurora, capped). Slowest cheap style is `waterfall` at 942µs, pre-existing.

Driven, one at a time, through `tools/ptysync.py` — DEC 2026 honoured the way
Ghostty does, 12s fixture, `--viz NAME` per style:

```
bars 5086  scope 8799  lissajous 36102  ribbon 7711  swell 17839  mirror 4666
matrix 2754  peaks 1040  rays 32113  wheel 9931  helix 6401  terrain 9880
waterfall 35315  radial 2810  bloom 563  metro 37380  particles 463  aurora 62901
```

(bytes/frame.) All 18 presented 55–67 frames with **nothing withheld at exit** —
a program that left 2026 open would show 1 frame and a black screen for the whole
track, which a bare pty cannot catch.

## Not done, deliberately

- **No direct-select keys for styles.** 18 makes `v` a longer walk, but `--viz
  NAME` and `V` cover it and adding keys is a change nobody asked for.
- **`peaksFloor` (0.12) wants a check against real music.** It is the one constant
  in the batch tuned on a fixture. 40 needles is the spectrum again.
- **Screenshots.** The pty drive measures frames and bytes, not appearance. Worth
  running `termscreen.py` over the new styles by hand before trusting the tuning.