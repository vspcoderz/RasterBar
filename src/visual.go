package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Spectrum analysis: the audio tap and the display smoothing on top of it.
//
// Levels come from a real audio tap (ffmpeg -> raw s16le PCM), analysed with a
// hand-rolled FFT (fft.go). Nothing here is driven by a timer -- the tap's clock
// is the audio's own.
//
// Rejected: mpv's astats/ebur128 metadata properties. Four attempts to read
// ${af-metadata/...} and ${metadata/...} through --term-playing-msg returned
// "(error)" on mpv as built here, so that property path is not portable enough
// to build on. Rejected: gonum/a DSP package -- zero module dependencies is a
// hard requirement of this project (see PLAN.md).

// Visualizer holds a smoothed level and a held peak per band.
//
// This is display smoothing, not analysis: the level has a fast attack and a slow
// release so a transient reads as a spike rather than as a blur, and the peak is
// held so the eye can see how loud a passage got between two frames. A bar with
// neither is unreadable at any frame rate you would actually watch.
type Visualizer struct {
	mu    sync.Mutex
	level []float64
	peak  []float64
}

func NewVisualizer(bars int) *Visualizer {
	if bars <= 0 {
		bars = bands
	}
	return &Visualizer{level: make([]float64, bars), peak: make([]float64, bars)}
}

// Resize changes the band count, keeping the levels it can.
//
// Levels already computed are kept rather than zeroed, because a terminal resize
// is not a new piece of music: dropping the bars to zero for a frame on every
// drag of the window edge is worse than carrying them over. The tails are left at
// whatever the old bands had, which is close enough and costs nothing.
func (v *Visualizer) Resize(n int) {
	if n <= 0 || n == len(v.level) {
		return
	}
	level := make([]float64, n)
	copy(level, v.level)
	peak := make([]float64, n)
	copy(peak, v.peak)
	v.level, v.peak = level, peak
}

// Push feeds one set of band magnitudes (0..1 each).
func (v *Visualizer) Push(mags []float64) {
	v.mu.Lock()
	defer v.mu.Unlock()
	n := len(v.level)
	for i := range v.level {
		v.level[i] *= 0.82
		v.peak[i] *= 0.93
	}
	for i := 0; i < n && i < len(mags); i++ {
		m := mags[i]
		switch {
		case m < 0:
			m = 0
		case m > 1:
			m = 1
		}
		if m > v.level[i] {
			v.level[i] = m
		}
		if m > v.peak[i] {
			v.peak[i] = m
		}
	}
}

// PushScalar feeds a single broadband amplitude (used by the RMS fallback).
func (v *Visualizer) PushScalar(amp float64) {
	v.mu.Lock()
	idx := 0
	switch {
	case amp < 0:
		amp = 0
	case amp > 1:
		amp = 1
	}
	idx = int(amp * float64(len(v.level)-1))
	for i := range v.level {
		v.level[i] *= 0.86
		v.peak[i] *= 0.94
	}
	for i := idx; i < len(v.level); i++ {
		if v.level[i] < amp {
			v.level[i] = amp
		}
		if v.peak[i] < amp {
			v.peak[i] = amp
		}
	}
	v.mu.Unlock()
}

// Level returns the smoothed band levels. Caller must not retain the slice.
func (v *Visualizer) Level() []float64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.level
}

// Peak returns the held band peaks. Caller must not retain the slice.
func (v *Visualizer) Peak() []float64 {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.peak
}

// Render draws the levels as one line of ramp characters with peak caps.
//
// Kept as a string because it is what the HUD's spectrum strip needs, and the
// strip is plain text in a fixed-width row rather than a grid.
func (v *Visualizer) Render() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var sb []byte
	sb = append(sb, make([]byte, len(v.level))...)
	for i, lv := range v.level {
		li := int(lv * float64(len(ramp)-1))
		if li < 0 {
			li = 0
		}
		if li >= len(ramp) {
			li = len(ramp) - 1
		}
		sb[i] = ramp[li]
	}
	return string(sb)
}

// levelTapArgs is the ffmpeg command line for the tap, split out so it can be
// asserted on.
//
// Every flag here is load-bearing and the absence of one of them is silent, which
// is why this is a function with a test rather than a literal buried in an
// exec.Command call.
//
// -re is the one that bit. Without it ffmpeg decodes and pushes as fast as the
// CPU allows, so the whole track is analysed in the first fraction of a second,
// ffmpeg exits, and the visualizer is then showing the last frame it will ever get
// while mpv is still playing. Observed exactly that: the spectrum drew one spectrum
// and then froze for the rest of a 60s track, the onset envelope stopped at 0.193,
// and the particle field spawned a burst and stopped.
//
// Every one of those symptoms points at the onset detector. The detector was fine --
// it had run out of audio a minute early. Same reason sync.go puts -re on the video
// pipe: without it the output races ahead of the music.
//
// -ss is the other one. The tap is its own ffmpeg reading its own copy of the
// stream (see resolveAudioPair on why it cannot share mpv's), so after a seek it
// has to be told where playback is or it analyses from the top of the file.
func levelTapArgs(mediaURL string, startAt float64) []string {
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
	}
	if startAt > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", startAt))
	}
	args = append(args,
		"-re",
		"-i", mediaURL,
		"-vn", "-sn", "-dn",
		"-ac", "1", "-ar", fmt.Sprint(spectrumRate),
		"-f", "s16le", "-",
	)
	return args
}

// LevelTap decodes a media URL's audio to raw PCM and exposes spectrum bands, the
// time-domain waveform and an onset envelope.
type LevelTap struct {
	cmd *exec.Cmd
	r   io.ReadCloser

	mu      sync.Mutex
	bands   []float64
	wave    []float64 // retained for the next read, so the hot loop allocates nothing
	waveBuf []float64
	beat    float64
	bpm     float64
	// gen increments on every new analysis window. The render loop reads it via
	// TryFrame so a style is folded at the audio's ~11Hz, not the renderer's up
	// to 30Hz -- otherwise a waterfall scrolls and particles age at the frame
	// rate and the state runs ~3x faster than the music.
	gen     uint64
	readGen uint64
	// dead is the pump's exit error, or nil while it is running. See Dead.
	dead   error
	an     *SpectrumAnalyzer
	onsets *onsetDetector
	once   sync.Once
}

// spectrumRate is higher than the old RMS tap on purpose: an FFT needs
// bandwidth above the audible range to resolve bass properly. 11025Hz mono is
// 22KB/s of pipe traffic, still trivial.
const spectrumRate = 11025

// waveWindow is how many raw samples the tap keeps for the oscilloscope.
//
// fftSize, so it is exactly one analysis window: the scope then draws the same
// span of time the FFT just looked at, which means the waveform and the bars
// always describe the same moment. A different length would be a different
// moment and the two styles would disagree.
const waveWindow = fftSize

func StartLevelTap(mediaURL string) (*LevelTap, error) {
	return StartLevelTapAt(mediaURL, 0)
}

// StartLevelTapAt is StartLevelTap with a media offset, so the analysis stays
// aligned with playback across a seek.
//
// The offset is not an optimisation. The tap is its own ffmpeg reading its own
// copy of the stream (see resolveAudioPair on why it cannot share mpv's), so it
// has to be told where playback is or it analyses from the top of the file while
// the music plays from the middle.
func StartLevelTapAt(mediaURL string, startAt float64) (*LevelTap, error) {
	cmd := exec.Command("ffmpeg", levelTapArgs(mediaURL, startAt)...)
	r, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("level tap start: %w", err)
	}
	lt := &LevelTap{
		cmd:     cmd,
		r:       r,
		an:      NewSpectrumAnalyzer(spectrumRate, bands),
		onsets:  NewOnsetDetector(bands),
		bands:   make([]float64, bands),
		wave:    make([]float64, 0, waveWindow),
		waveBuf: make([]float64, fftSize),
	}
	go lt.pump()
	return lt, nil
}

func (l *LevelTap) pump() {
	// Read in FFT-sized blocks so every analysis window is full; a short read
	// would zero-pad and read as a volume drop.
	buf := make([]byte, fftSize*2)
	samples := make([]float64, fftSize)
	for {
		n, err := io.ReadFull(l.r, buf)
		if n > 0 {
			ns := n / 2
			for i := 0; i < ns; i++ {
				samples[i] = float64(int16(binary.LittleEndian.Uint16(buf[i*2:]))) / 32768.0
			}
			mags := l.an.Analyze(samples[:ns])
			// Wave window: the same samples the FFT just consumed, so scope and
			// bars always describe the same moment. Copied rather than aliased
			// because samples is reused by the next read, and the render loop
			// reads this slice from another goroutine.
			// Reused rather than reallocated: this runs ~11 times a second for
			// the life of the track, and a fresh slice each time is garbage the
			// renderer never asks for. Only the filled prefix is handed on.
			wave := l.waveBuf[:ns]
			copy(wave, samples[:ns])
			// Raw, not the smoothed mags. See SpectrumAnalyzer.Raw for why the
			// detector must not see the display's smoothing.
			beat, _ := l.onsets.Push(l.an.Raw())

			l.mu.Lock()
			copy(l.bands, mags)
			l.wave = wave
			l.beat = beat
			l.bpm = l.onsets.BPM()
			l.gen++
			l.mu.Unlock()
		}
		if err != nil {
			// A tap that dies silently is a frozen spectrum: the styles keep
			// drawing the last frame for the rest of the track, which reads as a
			// hung process with no way to tell it from one. Record the exit so
			// TryFrame can report it.
			l.mu.Lock()
			l.dead = err
			l.mu.Unlock()
			return
		}
	}
}

// Bands returns a copy of the latest magnitudes.
//
// A copy, and that is not a habit. The pump goroutine writes into l.bands under
// this same mutex ~11 times a second, so handing back the header under the lock
// and letting the caller read it afterwards protects only the assignment -- the
// pump is free to overwrite the contents while the render loop is still reading
// them. A half-updated spectrum reads as a glitch in the bars and nothing else
// would catch it. Copying is a few hundred bytes at 11Hz.
func (l *LevelTap) Bands() []float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]float64, len(l.bands))
	copy(out, l.bands)
	return out
}

// Wave returns a copy of the most recent time-domain window, for the same reason
// as Bands: the pump owns the backing array.
func (l *LevelTap) Wave() []float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]float64, len(l.wave))
	copy(out, l.wave)
	return out
}

// Beat returns the onset envelope and tempo estimate.
func (l *LevelTap) Beat() (float64, float64) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.beat, l.bpm
}

// Frame collects everything the visualizers draw from, in one read of the tap.
//
// One call rather than three because the values have to be consistent with each
// other: a style that read bands on one tick and the beat on the next would be
// drawing a waveform from one moment and an envelope from another, and on a
// transient the mismatch is visible as the reaction lagging the beat.
//
// Caller may retain Bands and Wave: both are copies, because the pump owns the
// buffers the styles would otherwise be reading.
func (l *LevelTap) Frame() AudioFrame {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.frameLocked()
}

// TryFrame is Frame, but reports false when no new analysis has landed since the
// previous successful call. Callers that mutate per-analysis state on every Push
// use this so the state advances at the audio's rate, not the renderer's.
func (l *LevelTap) TryFrame() (AudioFrame, bool) {
	return l.TryFrameAt(&l.readGen)
}

// TryFrameAt is TryFrame against a caller-owned cursor.
//
// Two consumers share one tap: the visualiser and the spectrum strip under the
// HUD. They need the same "has anything new landed" gate but they must not share
// one cursor — a single cursor means whichever asks first takes the frame and the
// other is told there is nothing new, so a consumer silently stops updating
// depending on draw order. Each one holds its own uint64.
func (l *LevelTap) TryFrameAt(readGen *uint64) (AudioFrame, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.gen == *readGen {
		return AudioFrame{}, false
	}
	*readGen = l.gen
	return l.frameLocked(), true
}

// Dead reports why the tap's ffmpeg stopped producing audio, if it has.
//
// StartLevelTapAt only fails when ffmpeg cannot be *started*. A tap that starts
// and then dies — an expired googlevideo grant, a container it cannot demux —
// gives pump an EOF on the first ReadFull and the goroutine simply returns.
// Nothing observed that, so music mode kept drawing the last spectrum it had for
// the rest of the track, which is indistinguishable from a hung process. The
// render loop reports this through the status line once per tap.
func (l *LevelTap) Dead() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.dead
}

// frameLocked copies Bands and Wave; see Bands for why they are not aliased.
func (l *LevelTap) frameLocked() AudioFrame {
	bands := make([]float64, len(l.bands))
	copy(bands, l.bands)
	wave := make([]float64, len(l.wave))
	copy(wave, l.wave)
	return AudioFrame{
		Bands: bands,
		Wave:  wave,
		Beat:  l.beat,
		BPM:   l.bpm,
	}
}

func (l *LevelTap) Close() {
	l.once.Do(func() {
		// Kill first, close second — the ordering SyncPlayer.Close documents and
		// the one this got wrong. Closing a pipe with a read in flight waits for
		// that read to finish, and the read is waiting on ffmpeg, which is
		// blocked writing into a pipe nobody is draining. That is a hang on every
		// seek, resize and track advance for the life of the process, not a
		// theoretical one: pump is in ReadFull on exactly this pipe.
		if l.cmd != nil && l.cmd.Process != nil {
			_ = l.cmd.Process.Signal(syscall.SIGCONT)
			_ = l.cmd.Process.Kill()
			// Reap it. Killing without waiting leaves a zombie per seek, resize
			// and track advance for the life of the process. Bounded like
			// SyncPlayer.Close, because the pump goroutine may still be in Read.
			reaped := make(chan struct{})
			go func() {
				_ = l.cmd.Wait()
				close(reaped)
			}()
			select {
			case <-reaped:
			case <-time.After(childReapTimeout):
			}
		}
		// Now that ffmpeg is gone, nothing is writing to the pipe, so closing the
		// read end releases pump's ReadFull immediately instead of waiting for a
		// writer that will never come.
		if l.r != nil {
			l.r.Close()
		}
	})
}

// rmsLevel computes broadband RMS of s16le PCM mapped from -55..0 dBFS to
// 0..1. Kept as a fallback and for tests; the FFT path is preferred.
func rmsLevel(pcm []byte) float64 {
	if len(pcm) < 2 {
		return 0
	}
	n := len(pcm) / 2
	var sum float64
	for i := 0; i < n; i++ {
		f := float64(int16(binary.LittleEndian.Uint16(pcm[i*2:]))) / 32768.0
		sum += f * f
	}
	rms := math.Sqrt(sum / float64(n))
	if rms <= 0 {
		return 0
	}
	level := (20*math.Log10(rms) + 55.0) / 55.0
	switch {
	case level < 0:
		return 0
	case level > 1:
		return 1
	}
	return level
}
