package main

import "math"

// --- bloom -------------------------------------------------------------------

// bloomViz fires an expanding ring on every onset, sized by the spectrum's
// brightness.
//
// The only style driven by the beat *envelope* rather than by a level, and the
// distinction is load-bearing: `bars` shows where the music is, this shows when
// it hits. A kick is one event, so a kick gets one ring -- a bar chart cannot say
// that, because a kick occupies two analyses of the same loudness and reads as
// one fat bar.
//
// Rings are kept in a fixed-size list and retired by age, so the cost is bounded
// by the list rather than by how fast the music is. A drum-and-bass break at
// 180bpm is 9 onsets a second; a list that grew per onset would be 500 rings
// after a minute, each drawing a perimeter, which is the failure mode that makes
// this style the first thing anyone notices on a laptop.
type bloomViz struct {
	cols, rows int
	n          int
	viz        Visualizer
	scratch    []float64
	// rings is the fixed-size ring list: radius, brightness and age per entry.
	// Sized at bloomRings in the constructor path and never grown, which is the
	// entire cost bound.
	rings []bloomRing
	// next is the write cursor, and live is how many entries are in use.
	next, live int
	prevBeat   float64
	beat       float64
	// energy is the mean level across the spectrum, which is what sizes a new
	// ring. A ring's radius from the *peak* band would make every kick the same
	// size and every hi-hat tiny, when the thing you want to see is how hard the
	// hit was.
	energy   float64
	centroid float64
	aspect   float64
}

// bloomRing is one expanding ring.
type bloomRing struct {
	r   float64 // current radius in cells
	ink float64
	// bright is the brightness at birth, so a ring fades along its own history
	// rather than along the current beat envelope -- otherwise every ring on
	// screen brightens and fades together with the music and the ages stop being
	// distinguishable.
	bright float64
	band   float64 // the spectrum's brightness at birth, for the colour
}

const (
	// bloomRings is how many rings can be alive at once.
	//
	// 24 at ~10.8 analyses a second is about two seconds of onsets, which is
	// longer than most phrases and short enough that the list cannot fill on
	// anything a person would play. Past that the oldest is overwritten, which
	// drops a ring mid-flight -- visible, but better than the alternative.
	bloomRings = 24
	// bloomRise is the onset threshold, on the *rise* of the envelope.
	//
	// A rise and not a level, for particlesViz's reason: level spawns on every
	// analysis of a loud passage and spends the whole list in the first tenth of a
	// bar. A rise is one event per onset.
	bloomRise = 0.18
	// bloomSpeed is how far a ring grows per analysis, in cells.
	//
	// 1.1 at ~10.8 analyses a second is about a ring every second, so a ring
	// lives long enough to read as an event and dies before the next bar.
	bloomSpeed = 1.1
	// bloomLife is the fade per analysis, applied per ring.
	bloomLife = 0.955
	// bloomFloor is the brightness below which a ring is not drawn.
	bloomFloor = 0.05
)

func (b *bloomViz) Name() string { return "bloom" }
func (b *bloomViz) Heavy() bool  { return false }
func (b *bloomViz) CapScale(int, int) float64 {
	return 1
}

func (b *bloomViz) Resize(cols, rows int) {
	b.cols, b.rows = cols, rows
	b.n = bandCountFor(cols)
	b.viz.Resize(b.n)
	if len(b.scratch) != b.n {
		b.scratch = make([]float64, b.n)
	}
	if len(b.rings) != bloomRings {
		b.rings = make([]bloomRing, bloomRings)
	}
	b.next, b.live = 0, 0
	b.aspect = defaultAspect
}

func (b *bloomViz) Reset() {
	b.viz = Visualizer{level: make([]float64, b.n), peak: make([]float64, b.n)}
	for i := range b.rings {
		b.rings[i] = bloomRing{}
	}
	b.next, b.live, b.energy = 0, 0, 0
	b.beat, b.prevBeat = 0, 0
}

// Push folds in the analysis, fires a ring on a rise, and ages every live ring.
//
// All of it here, at the analysis rate. A ring's radius advanced in Paint would
// grow three times slower on a capped style, so the rings would expand at one
// speed on a small terminal and another on a large one -- and the ring's radius
// *is* the picture, unlike a bar's height which is re-derived every frame.
func (b *bloomViz) Push(f *AudioFrame) {
	resampleBands(f.Bands, b.n, b.scratch)
	b.viz.Push(b.scratch)
	b.prevBeat = b.beat
	b.beat = clamp01(f.Beat)

	// Mean level, for the ring's size.
	var sum float64
	for _, v := range b.viz.Level() {
		sum += clamp01(v)
	}
	b.energy = sum / float64(maxInt(len(b.viz.Level()), 1))
	b.recomputeCentroid()

	if rise := b.beat - b.prevBeat; rise > bloomRise {
		b.fire(rise)
	}
	for i := range b.live {
		// `live` is the count of entries in use; the cursor may not be at the
		// end, so the whole list is walked rather than a prefix.
		r := &b.rings[i]
		r.r += bloomSpeed
		r.ink *= bloomLife
	}
	// Count what is still worth drawing, so a quiet passage's rings are dropped
	// instead of being walked forever at zero cost.
	live := 0
	for i := range b.rings {
		if b.rings[i].ink > bloomFloor {
			live++
		}
	}
	b.live = live
}

// fire spawns a ring at the centre.
func (b *bloomViz) fire(rise float64) {
	r := &b.rings[b.next]
	b.next = (b.next + 1) % len(b.rings)
	// Brightness from the spectrum's mean level, not from the rise. The rise is
	// how *sudden* the hit was and two hits can have the same rise at very
	// different loudnesses; the energy is how hard it was.
	ink := clamp01(b.energy*1.6 + rise*0.5)
	r.r = bloomSpeed
	r.ink = ink
	r.bright = ink
	r.band = b.centroid
}

// centroid is where the spectrum's energy is, as a 0..1 band position.
//
// Mean of the band positions weighted by level, over the analyser's own bands --
// the same arithmetic as a spectral centroid, and the reason two rings from the
// same track are not the same colour. It is computed in Push, where the levels
// already are, rather than per ring at fire time.
func (b *bloomViz) recomputeCentroid() {
	lv := b.viz.Level()
	var num, den float64
	for i, v := range lv {
		w := clamp01(v)
		num += bandPos(i, len(lv)) * w
		den += w
	}
	if den <= 0 {
		b.centroid = 0.5
		return
	}
	b.centroid = clamp01(num / den)
}

func (b *bloomViz) Paint(g *VizGrid) {
	if b.cols == 0 || b.rows == 0 || b.live == 0 {
		return
	}
	cx := float64(b.cols) / 2
	cy := float64(b.rows) / 2
	aspect := b.aspect
	for i := range b.rings {
		r := &b.rings[i]
		if r.ink <= bloomFloor {
			continue
		}
		// A circle of the ring's radius, aspect-corrected, drawn a step at a time
		// around rather than tested per cell: a per-cell distance test is
		// rows*cols per ring, and with 24 rings that is the whole budget of the
		// program on a large grid.
		steps := int(r.r * 6)
		if steps < 8 {
			steps = 8
		}
		if steps > 512 {
			steps = 512
		}
		for k := 0; k <= steps; k++ {
			ang := float64(k) / float64(steps) * 2 * math.Pi
			x := int(cx + math.Cos(ang)*r.r)
			y := int(cy + math.Sin(ang)*r.r/aspect)
			g.Set(x, y, rampFor(r.ink), g.Color(r.band, r.ink, b.beat))
		}
	}
}
