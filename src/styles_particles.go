package main

import "math"

// --- particles ---------------------------------------------------------------

// particlesViz is the heaviest style: dots launched on transients.
//
// Heavy because it carries per-particle state and integrates it every frame.
//
// The budget is applied at *construction*, not in the paint loop. A grid of
// 300x120 is 36000 cells, and clearing that much particle state on every style
// switch and every seek is a GC pause on the machine that can least afford one --
// the fix has to be not allocating it in the first place.
type particlesViz struct {
	cols, rows int
	budget     int
	px, py     []float64
	pvx, pvy   []float64
	life       []float64
	alive      int
	beat       float64
	prevBeat   float64
	aspect     float64
}

// particleBudget is the cap, derived from the grid so a big terminal does not
// cost proportionally more. 400 is where this stops reading as a field and starts
// reading as static.
func particleBudget(cols, rows int) int {
	n := cols * rows / 8
	if n > 400 {
		n = 400
	}
	if n < 24 {
		n = 24
	}
	return n
}

func (p *particlesViz) Name() string { return "particles" }
func (p *particlesViz) Heavy() bool  { return true }

func (p *particlesViz) CapScale(cols, rows int) float64 {
	return float64(particleBudget(cols, rows)) / 400
}

func (p *particlesViz) Resize(cols, rows int) {
	p.cols, p.rows = cols, rows
	p.aspect = defaultAspect
	p.budget = particleBudget(cols, rows)
	// Allocated once per resize, sized to the cap. This is the whole reason the
	// style is capped: no slice here is ever cols*rows long.
	p.px = make([]float64, p.budget)
	p.py = make([]float64, p.budget)
	p.pvx = make([]float64, p.budget)
	p.pvy = make([]float64, p.budget)
	p.life = make([]float64, p.budget)
	p.alive = 0
	p.beat, p.prevBeat = 0, 0
}

func (p *particlesViz) Reset() {
	for i := range p.life {
		p.life[i] = 0
	}
	p.alive = 0
	p.beat, p.prevBeat = 0, 0
}

func (p *particlesViz) Push(f *AudioFrame) {
	p.prevBeat = p.beat
	p.beat = clamp01(f.Beat)
	// Spawn on the *rise* of the envelope, not on its level. Level would spawn on
	// every frame of a loud passage and spend the whole budget in the first tenth
	// of a bar, leaving nothing for the rest of the track. A rise is one event per
	// onset.
	if rise := p.beat - p.prevBeat; rise > 0.25 {
		p.spawn(rise)
	}
	p.integrate()
}

// spawn adds particles, up to the budget.
func (p *particlesViz) spawn(rise float64) {
	// The golden angle spreads consecutive spawn directions evenly around a
	// circle without a random source, so the field is reproducible: the same
	// music gives the same picture, which makes a bug report actionable.
	n := int(rise * 12)
	if n < 1 {
		n = 1
	}
	for i := 0; i < n && p.alive < p.budget; i++ {
		k := p.alive
		p.px[k] = float64(p.cols) / 2
		p.py[k] = float64(p.rows) / 2
		ang := float64(k) * 2.399963
		speed := 0.5 + rise*0.8
		p.pvx[k] = math.Cos(ang) * speed
		// Flattened by the cell aspect, so a circle of particles is round on
		// screen rather than twice as tall.
		p.pvy[k] = math.Sin(ang) * speed / p.aspect
		p.life[k] = 1
		p.alive++
	}
}

// integrate advances every particle and retires the dead ones.
func (p *particlesViz) integrate() {
	// Retire by compacting in place rather than by freeing slots: the alive set
	// stays contiguous, so Paint is a single pass with no gaps to skip.
	w := 0
	for i := 0; i < p.alive; i++ {
		p.life[i] -= 0.02
		if p.life[i] <= 0 {
			continue
		}
		p.px[i] += p.pvx[i]
		p.py[i] += p.pvy[i]
		if w != i {
			p.px[w], p.px[i] = p.px[i], p.px[w]
			p.py[w], p.py[i] = p.py[i], p.py[w]
			p.pvx[w], p.pvx[i] = p.pvx[i], p.pvx[w]
			p.pvy[w], p.pvy[i] = p.pvy[i], p.pvy[w]
			p.life[w] = p.life[i]
		}
		w++
	}
	p.alive = w
}

func (p *particlesViz) Paint(g *VizGrid) {
	for i := 0; i < p.alive; i++ {
		val := clamp01(p.life[i])
		// Fade with life, so a particle dims as it dies rather than vanishing.
		g.Set(int(p.px[i]), int(p.py[i]), rampFor(0.3+0.6*val), g.Color(0.5, val, p.beat))
	}
	// A core on the beat, so there is something to see at the centre even when
	// every particle has already drifted off. Brightness tracks the envelope, so
	// it is a glow rather than a strobe.
	if p.beat > 0.3 {
		g.Set(p.cols/2, p.rows/2, rampBright, g.Color(0, 1, p.beat))
	}
}
