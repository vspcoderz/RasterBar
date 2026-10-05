package main

import (
	"math"
	"testing"
)

// The visualizers are tested at the seams that can be wrong without a terminal:
// the band resampling, the ramp round-trip, the onset arithmetic, and one paint
// per style against a hand-worked grid.
//
// What is deliberately not tested here: anything about how the bytes reach the
// screen. VizGrid produces exactly the frame layouts the existing renderers
// already speak, so screen_test.go's no-stale-cells suite covers that path for
// free -- re-proving it for visualizers would be testing the renderers twice.

// --- ramp round-trip ---------------------------------------------------------

// TestRampLumRoundTripsThroughLevelFor pins the trick that lets a style choose an
// exact glyph and still paint through DiffRenderer unchanged.
//
// levelFor is what DiffRenderer calls on every cell. rampLum has to produce a
// luminance that maps back to the same ramp index, or every visualizer cell shows
// the neighbouring glyph's ink density and the whole grid is subtly wrong in a way
// no amount of staring at it would explain.
func TestRampLumRoundTripsThroughLevelFor(t *testing.T) {
	for idx := 0; idx < len(ramp); idx++ {
		got := levelFor(rampLum(idx))
		if got != ramp[idx] {
			t.Errorf("levelFor(rampLum(%d)) = %q, want %q", idx, got, ramp[idx])
		}
	}
}

func TestRampLumClampsOutOfRange(t *testing.T) {
	if got := rampFor(-1); got != 0 {
		t.Errorf("rampFor(-1) = %d, want the darkest glyph index 0", got)
	}
	if got := rampFor(2); got != byte(len(ramp)-1) {
		t.Errorf("rampFor(2) = %d, want the brightest index %d", got, len(ramp)-1)
	}
}

// TestRampForIsNotInverted guards the direction of the mapping, which is the one
// thing about it that is genuinely counter-intuitive.
//
// The ramp runs from ' ' to '@' in order of ink, so a HIGH index is a cell that
// lays down a lot of ink. Loud therefore means a high index. Getting this backwards
// renders a quiet passage as a wall of solid blocks and a loud one as silence.

// TestRampForIsNotInverted guards the direction of the mapping, which is the one
// thing about it that is genuinely counter-intuitive.
//
// The ramp runs from ' ' to '@' in order of ink, so a HIGH index is a cell that
// lays down a lot of ink. Loud therefore means a high index. Getting this backwards
// renders a quiet passage as a wall of solid blocks and a loud one as silence.
func TestRampForIsNotInverted(t *testing.T) {
	quiet := rampFor(0.0)
	loud := rampFor(1.0)
	if quiet >= loud {
		t.Errorf("rampFor is inverted: quiet=%d loud=%d", quiet, loud)
	}
	if quiet != 0 {
		t.Errorf("rampFor(0) = %d, want 0 (a blank cell)", quiet)
	}
	if loud != byte(len(ramp)-1) {
		t.Errorf("rampFor(1) = %d, want %d", loud, len(ramp)-1)
	}
}

// --- band resampling ---------------------------------------------------------

// --- band resampling ---------------------------------------------------------

func TestResampleBandsShrinksByAveraging(t *testing.T) {
	// 4 bands down to 2: each output is the mean of its pair, so no energy is lost.
	// Taking every other band instead would drop half the spectrum, which is what
	// makes a hi-hat vanish at low band counts.
	src := []float64{0, 1, 0, 1}
	out := make([]float64, 2)
	resampleBands(src, 2, out)
	if out[0] != 0.5 || out[1] != 0.5 {
		t.Errorf("shrink = %v, want [0.5 0.5]", out)
	}
}

func TestResampleBandsGrowsByRepeating(t *testing.T) {
	// Growing must NOT interpolate. An invented peak between two real ones is a
	// lie about the music, and the spectrum is the one thing here that is supposed
	// to be a measurement.
	src := []float64{0, 0, 1, 0}
	out := make([]float64, 8)
	resampleBands(src, 8, out)
	for i, v := range out {
		if v != 0 && v != 1 {
			t.Errorf("out[%d] = %v, invented an intermediate value", i, v)
		}
	}
	if out[4] != 1 || out[5] != 1 {
		t.Errorf("peak not repeated onto its columns: %v", out)
	}
}

func TestResampleBandsEmptySourceIsFlat(t *testing.T) {
	out := make([]float64, 4)
	resampleBands(nil, 4, out)
	for i, v := range out {
		if v != 0 {
			t.Errorf("out[%d] = %v, want 0 from an empty source", i, v)
		}
	}
}

// TestResampleBandsIdentity proves the analyser runs at a pass-through when the
// grid asks for exactly the bands it produced. 48 bands on a 48-column terminal is
// the common case and must not be altered.

// TestResampleBandsIdentity proves the analyser runs at a pass-through when the
// grid asks for exactly the bands it produced. 48 bands on a 48-column terminal is
// the common case and must not be altered.
func TestResampleBandsIdentity(t *testing.T) {
	src := make([]float64, bands)
	for i := range src {
		src[i] = float64(i) / float64(bands-1)
	}
	out := make([]float64, bands)
	resampleBands(src, bands, out)
	for i := range src {
		if out[i] != src[i] {
			t.Fatalf("band %d: got %v want %v", i, out[i], src[i])
		}
	}
}

func TestBandCountForClampsToTheAnalyserCeiling(t *testing.T) {
	// Wider than the ceiling: capped, or a 300-column terminal asks for 300 bands
	// from an analyser that produces 48.
	if got := bandCountFor(300); got != maxDrawnBands {
		t.Errorf("bandCountFor(300) = %d, want %d", got, maxDrawnBands)
	}
	if got := bandCountFor(40); got != 40 {
		t.Errorf("bandCountFor(40) = %d, want 40 (one per column)", got)
	}
	// Degenerate width must not produce a zero or a negative.
	if got := bandCountFor(0); got < 2 {
		t.Errorf("bandCountFor(0) = %d, want at least 2", got)
	}
}

// --- onset detection ---------------------------------------------------------

// TestOnsetDetectorNeverFiresOnSilence covers the floor.
//
// Without onsetFloor, flux is zero, the mean is zero, and the comparison is
// 0 > 0*1.6 -- which is false, but only just. A single sample of dither in a
// digital-silent passage is many times the mean and reads as a beat, so the whole
// envelope becomes a strobe during the quiet parts of a track.

// --- audio format selection --------------------------------------------------

// TestPickAudioPrefersAStreamWithNoVideo is the low-end decision in one assertion.
//
// The level tap's ffmpeg is given -vn, so it never *outputs* video -- but ffmpeg
// still decodes what it is handed before discarding it. Pointed at a progressive
// format that means decoding a 360p h264 video purely to throw it away, on every
// track, on the machine that can least afford it.
func TestPickAudioPrefersAStreamWithNoVideo(t *testing.T) {
	formats := []ytFormat{
		// A progressive format with the highest bitrate, which is exactly what a
		// bitrate-sorted preference would pick.
		{FormatID: "18", URL: "https://progressive", TBR: 2000, VCodec: "avc1", ACodec: "mp4a"},
		// An audio-only DASH format at a much lower bitrate.
		{FormatID: "140", URL: "https://audio-only", TBR: 130, VCodec: "none", ACodec: "mp4a"},
	}
	got, err := pickAudio(formats)
	if err != nil {
		t.Fatalf("pickAudio: %v", err)
	}
	if got != "https://audio-only" {
		t.Errorf("pickAudio chose %q, want the audio-only stream", got)
	}
}

func TestPickAudioFallsBackToProgressive(t *testing.T) {
	// Some uploads only have progressive. Refusing would mean the track is
	// unplayable in music mode, which is worse than decoding video we then discard.
	formats := []ytFormat{
		{FormatID: "18", URL: "https://progressive", TBR: 800, VCodec: "avc1", ACodec: "mp4a"},
	}
	got, err := pickAudio(formats)
	if err != nil {
		t.Fatalf("pickAudio: %v", err)
	}
	if got != "https://progressive" {
		t.Errorf("pickAudio = %q, want the progressive fallback", got)
	}
}

func TestPickAudioSkipsM3u8(t *testing.T) {
	// m3u8 has to be re-resolved by ffmpeg, which loses control of the clock and
	// makes the tap's pacing a guess.
	formats := []ytFormat{
		{FormatID: "hls", URL: "https://m3u8", TBR: 900, VCodec: "none", ACodec: "mp4a", Protocol: "m3u8"},
	}
	if _, err := pickAudio(formats); err == nil {
		t.Error("pickAudio accepted an m3u8 format")
	}
}

func TestPickAudioRejectsVideoOnly(t *testing.T) {
	formats := []ytFormat{
		{FormatID: "137", URL: "https://video", TBR: 4000, VCodec: "avc1", ACodec: "none"},
	}
	if _, err := pickAudio(formats); err == nil {
		t.Error("pickAudio accepted a video-only format as audio")
	}
}

// TestLevelTapArgsAreRealtimeAndSeeked pins the two flags whose absence is silent.
//
// This is not the kind of test that "proves nothing" by recomputing the code's own
// answer. It pins a decision that was already got wrong: the tap shipped without
// -re, analysed the entire track in under a second, and every visible symptom --
// a frozen spectrum, a stalled onset envelope, a particle field that fired once --
// pointed somewhere else entirely. Only the arg list can catch it.
//
// -re: without it ffmpeg pushes as fast as the CPU allows, so the whole track is
// consumed while mpv is still on the first second.
//
// -ss: the tap is a separate ffmpeg on a separate copy of the stream, so after a
// seek it would otherwise analyse from the top of the file.

// --- helpers -----------------------------------------------------------------

// rampBands is a plausible spectrum: quiet at the bottom, loud in the middle,
// rolling off at the top. Flat arrays make every style's output degenerate.
func rampBands(n int, scale float64) []float64 {
	out := make([]float64, n)
	for i := range out {
		t := float64(i) / float64(maxInt(n-1, 1))
		out[i] = clamp01((0.3 + 0.7*math.Sin(t*math.Pi)) * scale)
	}
	return out
}

func ones(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = 1
	}
	return out
}

// rampWave is a deterministic waveform, so a scope test is reproducible rather
// than dependent on a random source.

// rampWave is a deterministic waveform, so a scope test is reproducible rather
// than dependent on a random source.
func rampWave(n int) []float64 {
	out := make([]float64, n)
	for i := range out {
		out[i] = math.Sin(float64(i) * 0.05)
	}
	return out
}

// cellsAboveBg counts cells carrying ink *beyond the background*.
//
// litCells counts anything non-zero, which used to be the same thing and now is
// not: the grid paints a background into every cell on Clear, so litCells is the
// full grid on every frame and every "did this style draw anything" assertion built
// on it became vacuous.
