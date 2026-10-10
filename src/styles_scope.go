package main

import "math"

// --- scope -------------------------------------------------------------------

// scopeViz draws the waveform as a triggered, zoomed oscilloscope trace.
//
// The previous version was thin and looked it, and the reason was not its line
// style. The tap hands it `waveWindow` = 1024 samples at 11025Hz, which is 92.9ms
// of audio, and the old Paint drew all of it across the terminal's width:
//
//	note     cycles in 92.9ms    columns per cycle at 80 cols
//	55Hz     5.1                 16
//	220Hz    20.4                3.9
//	440Hz    40.8                2.0
//
// Past a few hundred Hz there are more cycles than there are columns, so the
// trace aliases into a solid band and every note looks like every other note. No
// amount of phosphor fixes that; the window itself is wrong.
//
// So: show about scopeCycles cycles of whatever pitch is loudest, and trigger on
// a rising zero crossing so the trace stands still instead of sliding sideways
// with the note's phase. Everything else here -- the min/max beam, the trail --
// is detail on top of those two.
//
// scopeViz does read Bands, but only to choose the zoom. It still draws nothing
// at all from bands alone, which is what TestStylesOnlyDrawFromTheirOwnInput
// asserts and what the doc comment on that test means.
type scopeViz struct {
	cols, rows int
	wave       []float64
	// hz is the dominant frequency the zoom is computed from, slewed rather than
	// taken raw: the loudest band flickers between neighbours on almost every
	// analysis, and an unsmoothed zoom makes the trace pump in and out.
	hz float64

	trail phosphor
}

const (
	// scopeCycles is how much of the signal's period to show.
	//
	// 2.5 rather than 1: one cycle is enough to see a shape but not enough to
	// see that the shape repeats, and a real scope is set to a small integer
	// number of cycles for the same reason. Two and a half is the smallest
	// count where the trace both holds still and shows a period.
	scopeCycles = 2.5
	// scopeMinSamples floors the window. Below this the arithmetic per column
	// stops being the problem and aliasing becomes it: at 5kHz, 2.5 cycles is
	// 5 samples, and 5 samples across 200 columns is a line interpolating
	// between five points, which is a drawing of nothing.
	scopeMinSamples = 48
	// scopeDefaultHz is the zoom used before any band has been loud enough to
	// pick a pitch, and whenever the window is silent. 220Hz is a middle of the
	// range guess: it only decides how many samples are drawn, and a silent
	// window draws a flat line either way.
	scopeDefaultHz = 220.0
	// scopeHzSlew is how far the zoom's pitch follows a new estimate per
	// analysis. 0.25 converges in about four analyses (~0.4s), which is fast
	// enough to follow a melody change and slow enough to ignore a single
	// transient's worth of flicker.
	scopeHzSlew = 0.25
	// scopeTriggerFloor is the peak amplitude below which no trigger is
	// attempted.
	//
	// A trigger on a near-silent window locks onto a single noise crossing and
	// holds the trace at one point, which reads as a frozen renderer. Silence is
	// the one case where untriggered is the correct answer, because there is
	// nothing periodic to stabilise.
	scopeTriggerFloor = 0.02
)

func (s *scopeViz) Name() string { return "scope" }
func (s *scopeViz) Heavy() bool  { return false }
func (s *scopeViz) CapScale(int, int) float64 {
	return 1
}

func (s *scopeViz) Resize(cols, rows int) {
	s.cols, s.rows = cols, rows
	s.hz = scopeDefaultHz
	// A short trail, because a scope's persistence is set to make the beam
	// readable rather than to smear: 0.55 leaves about three previous analyses
	// visible, which is enough to see the trace retrace itself and not enough
	// to hide where it is now.
	s.trail.Resize(cols, rows, 0.55)
}

// Reset drops the waveform and the trail. Both are "the last 93ms of the old
// position", and drawing either after a seek shows a fragment of the wrong part
// of the song.
func (s *scopeViz) Reset() {
	s.wave = s.wave[:0]
	s.hz = scopeDefaultHz
	s.trail.Clear()
}

// Push keeps the latest window, slews the zoom's pitch, and decays the trail.
//
// Copied rather than aliased: the tap recycles its sample buffer on the next
// read, ~11 times a second, so holding the slice would draw a half-overwritten
// waveform.
//
// The trail decays here, on the analysis clock, rather than in Paint. Paint runs
// at up to 30Hz and at CapScale of that for a heavy style, so a trail that
// decayed per paint would be twice as long on one terminal as on another for
// the same music. The trail is a record of the audio, so it ages on the audio's
// clock -- the same rule waterfallViz's scroll follows.
func (s *scopeViz) Push(f *AudioFrame) {
	if len(f.Wave) > 0 {
		s.wave = append(s.wave[:0], f.Wave...)
	}
	_, hz := dominantBand(f.Bands)
	if hz > 0 {
		if s.hz <= 0 {
			s.hz = hz
		} else {
			s.hz = lerp(s.hz, hz, scopeHzSlew)
		}
	}
	s.trail.Decay()
}

// scopeSamples is how many of the tap's samples this trace shows: about
// scopeCycles of a period of the dominant pitch, floored so a bright treble
// still gets enough samples to interpolate between.
func (s *scopeViz) scopeSamples() int {
	n := int(scopeCycles * spectrumHz / s.hz)
	if n < scopeMinSamples {
		n = scopeMinSamples
	}
	if n > len(s.wave) {
		n = len(s.wave)
	}
	return n
}

// triggerStart returns the index of a rising zero crossing at or after the
// beginning of the window, or 0 when the window is too quiet or too noisy to
// have one worth locking to.
//
// Only the first half of the window is searched. A crossing found at the very
// end would leave fewer than one cycle to draw, which is the frozen-trace bug in
// a different costume.
func (s *scopeViz) triggerStart(n int) int {
	limit := n / 2
	peak := 0.0
	for i := 0; i < limit; i++ {
		a := clampSigned(s.wave[i])
		if a < 0 {
			a = -a
		}
		if a > peak {
			peak = a
		}
	}
	if peak < scopeTriggerFloor {
		return 0
	}
	for i := 1; i < limit; i++ {
		if s.wave[i-1] <= 0 && s.wave[i] > 0 {
			return i
		}
	}
	return 0
}

// Paint draws the trace, then lays it into the trail and blits the trail.
func (s *scopeViz) Paint(g *VizGrid) {
	n := s.scopeSamples()
	if s.cols == 0 || s.rows == 0 {
		s.trail.Blit(g)
		return
	}
	if n <= 1 || s.rows < 2 {
		// Nothing to draw this frame, but the trail is still on screen and the
		// caller cleared the grid, so it has to be blitted to avoid a flash of
		// background between two silent analyses.
		s.trail.Blit(g)
		return
	}
	start := s.triggerStart(n)
	mid := float64(s.rows-1) / 2
	prevTop, prevBot := -1, -1
	for x := 0; x < s.cols; x++ {
		lo := start + x*n/s.cols
		hi := start + (x+1)*n/s.cols
		if hi <= lo {
			hi = lo + 1
		}
		top, bot := s.beamRow(lo, hi, mid)
		if prevTop >= 0 {
			// Join this column's beam to the last one across the gap, so the
			// trace is a line rather than a column of strokes. A scope that skips
			// rows between columns reads as separate bars, which is the original
			// complaint about the old line, not a fix for it.
			joinTop := minInt(prevTop, top)
			joinBot := maxInt(prevBot, bot)
			for y := joinTop; y <= joinBot; y++ {
				s.trail.Lay(x, y, scopeBeamInk)
			}
		}
		s.trail.Lay(x, top, scopeBeamInk)
		s.trail.Lay(x, bot, scopeBeamInk)
		prevTop, prevBot = top, bot
	}
	s.trail.Blit(g)
}

// scopeBeamInk is the intensity the live trace is laid down at. Full: the trail
// *is* the history, so the newest pass has to be the brightest thing on screen
// or the scope has no beam, only a smear.
const scopeBeamInk = 1.0

// beamRow finds the screen rows one column's slice of samples covers.
//
// Min and max, not the mean. Averaging a column cancels an asymmetric waveform
// toward zero, so the quietest and most transient things -- a kick, a plucked
// string's attack -- came out as the flattest, which is the opposite of what the
// scope is for. The beam's *thickness* carrying the transient is also what a
// real scope's beam does.
func (s *scopeViz) beamRow(lo, hi int, mid float64) (top, bot int) {
	top, bot = int(mid), int(mid)
	first := true
	for i := lo; i < hi && i < len(s.wave); i++ {
		v := clampSigned(s.wave[i])
		y := int(math.Round(mid - v*mid))
		if y < 0 {
			y = 0
		}
		if y >= s.rows {
			y = s.rows - 1
		}
		if first {
			top, bot = y, y
			first = false
			continue
		}
		if y < top {
			top = y
		}
		if y > bot {
			bot = y
		}
	}
	return top, bot
}
