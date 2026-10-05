package main

import (
	"encoding/binary"
	"os"
	"testing"
)

// End-to-end over real audio: PCM -> SpectrumAnalyzer -> onsetDetector ->
// AudioFrame -> a style.
//
// This is the only seam that could not be reached by the unit tests above, and
// it is where the bug actually was. Every piece tested clean in isolation: the
// analyser produced the right magnitudes, the detector fired on every transient,
// the particle style spawned and retired correctly. Wired together the particle
// field spawned eight dots at the start of a track and then never again, for the
// whole track.
//
// testdata/beat-12s.pcm is 12 seconds of s16le mono at spectrumRate: a 440Hz
// burst of 50ms every 500ms plus a 110Hz burst offset by 250ms. Checked in as raw
// PCM rather than generated in the test because the bug is a calibration problem --
// the absolute band levels matter to the thresholds -- and a synthesised signal
// that happened to be 3dB quieter would hide it.
//
// It is a fixture, not a dependency: the test needs no ffmpeg, no mpv and no
// network. The file was produced once by
//   ffmpeg -i <src> -f s16le -ac 1 -ar 11025 -t 12 testdata/beat-12s.pcm

// readPCM loads s16le mono and returns it as float64 in [-1,1].
func readPCM(t *testing.T, path string) []float64 {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("fixture missing: %v", err)
	}
	if len(raw)%2 != 0 {
		t.Fatalf("fixture is not whole s16 samples: %d bytes", len(raw))
	}
	out := make([]float64, len(raw)/2)
	for i := range out {
		out[i] = float64(int16(binary.LittleEndian.Uint16(raw[i*2:]))) / 32768.0
	}
	return out
}

// analysePCM runs the real tap pipeline over the samples and returns one
// AudioFrame per fftSize block, which is exactly what LevelTap.pump produces.
func analysePCM(t *testing.T, pcm []float64) []AudioFrame {
	t.Helper()
	an := NewSpectrumAnalyzer(spectrumRate, bands)
	d := NewOnsetDetector(bands)
	var frames []AudioFrame
	for off := 0; off+fftSize <= len(pcm); off += fftSize {
		mags := an.Analyze(pcm[off : off+fftSize])
		smoothed := make([]float64, len(mags))
		copy(smoothed, mags)
		beat, onset := d.Push(an.Raw())
		frames = append(frames, AudioFrame{
			Bands: smoothed,
			Wave:  pcm[off : off+fftSize],
			Beat:  beat,
			BPM:   d.BPM(),
		})
		_ = onset
	}
	return frames
}

// TestTapPipelineProducesSustainedOnsets is the direct end-to-end assertion.
//
// 12s of a 500ms pulse train is 24 beats. The baseline needs 32 analyses -- about
// 3s, or 6 beats -- before it judges anything, so roughly 18 onsets are expected.
// One onset means the detector gave up; more than 24 means it is firing between
// beats.
func TestTapPipelineProducesSustainedOnsets(t *testing.T) {
	pcm := readPCM(t, "testdata/beat-12s.pcm")
	frames := analysePCM(t, pcm)
	if len(frames) < 100 {
		t.Fatalf("only %d frames from the fixture", len(frames))
	}

	onsets := 0
	first, last := -1, -1
	for i, f := range frames {
		// The envelope jumps to 1.0 on an onset, so count those rather than
		// threading the raw flag through: this is what a style actually sees.
		if f.Beat >= beatRise-1e-9 {
			onsets++
			if first < 0 {
				first = i
			}
			last = i
		}
	}
	if onsets < 12 {
		t.Errorf("%d onsets in 12s of a 500ms pulse train; want 12 or more", onsets)
	}
	if onsets > 24 {
		t.Errorf("%d onsets in 24 beats; firing between beats", onsets)
	}
	t.Logf("%d onsets, first at frame %d, last at frame %d, bpm=%.1f",
		onsets, first, last, frames[len(frames)-1].BPM)
}

// TestParticleFieldStaysAliveAcrossAWholeTrack is the regression test that was
// missing, and it is the one that matches the symptom: the field must still have
// particles at the *end* of the track, not just near the start.
//
// Push is driven at the render rate (musicFPS), because that is what the render
// loop does -- the tap analyses at ~11Hz and the renderer pushes whatever frames
// it has, so the particle lifetimes decay per render frame and the envelope the
// style reacts to is a held value between analyses.
func TestParticleFieldStaysAliveAcrossAWholeTrack(t *testing.T) {
	pcm := readPCM(t, "testdata/beat-12s.pcm")
	frames := analysePCM(t, pcm)
	if len(frames) == 0 {
		t.Fatal("no frames")
	}

	v := &particlesViz{}
	v.Resize(80, 24)

	// Render rate is ~4x the analysis rate, so hold each analysed frame for a few
	// pushes, which is what the real render loop does.
	const pushesPerFrame = 3
	alive := make([]int, 0, len(frames))
	drawn := make([]int, 0, len(frames))
	g := NewVizGrid(80, 24, false, 1)
	for _, f := range frames {
		for i := 0; i < pushesPerFrame; i++ {
			fr := f
			v.Push(&fr)
		}
		alive = append(alive, v.alive)
		// What is actually on screen, which is not the same question as how many
		// particles exist. Asserting on `alive` alone is what let the real bug
		// through: 25 particles were alive and every one of them had drifted off
		// the grid, so the field rendered as an empty screen for the whole track.
		g.Clear()
		g.SetPalette(paletteAt(0))
		v.Paint(g)
		drawn = append(drawn, cellsAboveBg(g))
	}

	t.Logf("alive:  %v", alive)
	t.Logf("drawn:  %v", drawn)
	if alive[len(alive)-1] == 0 {
		t.Errorf("no particles alive at the end of the track; the field died")
	}
	if drawn[len(drawn)-1] < 5 {
		t.Errorf("%d particles alive but only %d on screen at the end: they are "+
			"drifting off the grid. drawn=%v", alive[len(alive)-1],
			drawn[len(drawn)-1], drawn)
	}
	// A field that only exists early is the original symptom, so check the whole
	// second half rather than the last frame alone.
	half := len(drawn) / 2
	for i := half; i < len(drawn); i++ {
		if drawn[i] == 0 {
			t.Errorf("nothing drawn at frame %d of %d (mid-track); the field dies "+
				"and does not come back. drawn=%v", i, len(drawn), drawn)
			break
		}
	}
}

// TestEveryStyleSurvivesRealAudio pushes every style through the whole fixture, so
// a style that panics or stops drawing on real content is caught here rather than
// by someone pressing v during playback.
func TestEveryStyleSurvivesRealAudio(t *testing.T) {
	pcm := readPCM(t, "testdata/beat-12s.pcm")
	frames := analysePCM(t, pcm)
	if len(frames) == 0 {
		t.Fatal("no frames")
	}

	for _, mk := range vizRegistry {
		v := mk()
		v.Resize(80, 24)
		g := NewVizGrid(80, 24, false, 1)
		for i, f := range frames {
			fr := f
			v.Push(&fr)
			// Paint on the analysis rate for this test: the point is coverage of
			// every style against real content, not frame-rate realism.
			if i%3 == 0 {
				g.Clear()
				v.Paint(g)
			}
		}
		g.Clear()
		v.Paint(g)
		if lit := cellsAboveBg(g); lit == 0 {
			t.Errorf("%s drew nothing at the end of real audio", v.Name())
		}
	}
}
