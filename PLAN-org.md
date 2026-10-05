# PLAN-org — reorganisation

## What the layout actually looks like now

Measured, not assumed:

| file | lines | problem |
|---|---:|---|
| `session.go` | 1186 | **`playTrack` is 654 of them** (`session.go:501-1154`) — one function holding the render loop, HUD painter, overlay, key routing, resize and stall handling |
| `main_test.go` | 2124 | junk drawer: **48 unrelated test groups** across ~10 seams — `TestParse*`×15, `TestPlayer*`×12, `TestHUD*`×11, `TestDecode*`×8, `TestColor*`×7, `TestPick*`×5, `TestCompute*`×5 |
| `viz_test.go` | 1664 | same: palette, HUD lines, style paint, grid and layout tests mixed |
| `styles.go` | 775 | six independent styles, 54 funcs, 6 types |
| `prompt_test.go` | 905 | seam is fine, file is just big |

Totals: 9085 source, 6609 test, all `package main`, all flat.

## Decision: files, not packages

Asked, answered "all" — so everything that adds value, with one exception stated
plainly rather than buried.

**Deferred: `internal/` packages.** Going from one package to `internal/viz`,
`internal/player`, `internal/transport`, `internal/hud` means touching all 9085
source lines and 6609 test lines, and it forces exporting state that is
deliberately unexported today — `stdDb` and `levelDbfs` exist as unexported fields
*specifically so tests can assert the noise gate's decision*, and `haveLast`,
`c.first`, `g.bgRamp` are asserted directly in the leak suite. That churn buys
navigability the file split already delivers, and it is precisely the reshuffle
that silently reopens the three rendering bugs just closed in `91cd3c0`,
`9111ac9` and `a0e4687`.

This is a separate, deliberate step, not part of tidy-up. Say the word and it gets
its own plan and its own commit.

## Plan

### A — housekeeping
- `git remote` → `https://github.com/vspcoderz/RasterBar.git` (still pointed at the
  old name; every push currently relies on GitHub's redirect)
- verify whether `go install github.com/vspcoderz/rasterbar@latest` resolves with
  the repo capitalised — keep the lowercase module path only if it actually does
- `doc.go` package documentation

### B — test files by seam (biggest win, pure file movement)
`main_test.go` → per-seam files, routed by test-name prefix, matching the seven
seams `AGENT.MD` already documents. Every non-`Test` helper in the file moves to
the seam it belongs to rather than being duplicated or dropped.

`viz_test.go` → palette, hud, style, grid split.

Rule: no test is added, renamed or deleted. The count is asserted before and after.

### C — styles by file
`styles.go` → `styles_common.go` (ramp, resample, registry) + one file per style.
Pure moves.

### D — `playTrack`
654 lines is the one genuine code problem, not just a layout one. Extract the
cohesive closures — HUD painter, overlay, key handling — into methods. Assessed
only after A-C land green, because it is the one step that can change behaviour.

### E — docs
- README architecture section
- `AGENT.MD` architecture map updated to the new file names

## Verification

After **every** step, not just at the end:

```sh
gofmt -l . && go vet ./... && go test -count=1 ./... && go build -ldflags="-s -w" -o rasterbar .
```

Plus, for B specifically: `grep -c '^func Test'` before and after must be equal.
A refactor that quietly drops a test is worse than one that never ran.

## Status

starting with A