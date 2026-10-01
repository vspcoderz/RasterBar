package main

import (
	"encoding/binary"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Spectrum visualizer.
//
// Levels come from a real audio tap (ffmpeg -> raw s16le PCM), analysed with a
// hand-rolled FFT (fft.go). Nothing here is driven by a timer.
// The shared `ramp` lives in ascii.go.
//
// Rejected: mpv's astats/ebur128 metadata properties. Four attempts to read
// ${af-metadata/...} and ${metadata/...} through --term-playing-msg returned
// "(error)" on mpv as built here, so that property path is not portable enough
// to build on. Rejected: gonum/a DSP package — zero module dependencies is a
// hard requirement of this project (see PLAN.md).

// Visualizer draws bars with peak-hold caps.
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

func (v *Visualizer) Render() string {
	v.mu.Lock()
	defer v.mu.Unlock()
	var sb strings.Builder
	sb.Grow(len(v.level) * 2)
	for i, lv := range v.level {
		li := int(lv * float64(len(ramp)-1))
		if li < 0 {
			li = 0
		}
		if li >= len(ramp) {
			li = len(ramp) - 1
		}
		sb.WriteByte(ramp[li])
		// peak cap: one char above the bar, only if there is headroom
		pi := int(v.peak[i] * float64(len(ramp)-1))
		if pi > li && pi < len(ramp) {
			sb.WriteByte(ramp[pi])
		}
	}
	return sb.String()
}

// LevelTap decodes a media URL's audio to raw PCM and exposes spectrum bands.
type LevelTap struct {
	cmd *exec.Cmd
	r   io.ReadCloser

	mu    sync.Mutex
	bands []float64
	an    *SpectrumAnalyzer
	once  sync.Once
}

// spectrumRate is higher than the old RMS tap on purpose: an FFT needs
// bandwidth above the audible range to resolve bass properly. 11025Hz mono is
// 22KB/s of pipe traffic, still trivial.
const spectrumRate = 11025

func StartLevelTap(mediaURL string) (*LevelTap, error) {
	cmd := exec.Command("ffmpeg",
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", mediaURL,
		"-vn", "-sn", "-dn",
		"-ac", "1", "-ar", fmt.Sprint(spectrumRate),
		"-f", "s16le", "-",
	)
	r, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("level tap start: %w", err)
	}
	lt := &LevelTap{
		cmd:   cmd,
		r:     r,
		an:    NewSpectrumAnalyzer(spectrumRate, bands),
		bands: make([]float64, bands),
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
			l.mu.Lock()
			copy(l.bands, mags)
			l.mu.Unlock()
		}
		if err != nil {
			return
		}
	}
}

// Bands returns the latest magnitudes. Caller must not retain the slice.
func (l *LevelTap) Bands() []float64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.bands
}

func (l *LevelTap) Close() {
	l.once.Do(func() {
		if l.r != nil {
			l.r.Close()
		}
		if l.cmd != nil && l.cmd.Process != nil {
			l.cmd.Process.Kill()
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

// watchQuit closes the returned channel on q or ctrl-c. Raw mode required.
func watchQuit(in *os.File) <-chan struct{} {
	ch := make(chan struct{})
	go func() {
		b := make([]byte, 1)
		for {
			n, err := in.Read(b)
			if err != nil || n == 0 {
				return
			}
			if b[0] == 'q' || b[0] == 'Q' || b[0] == 0x03 {
				close(ch)
				return
			}
		}
	}()
	return ch
}

// runVisualAudio plays audio and draws the spectrum. Audio-only mode used to
// be a blank terminal for the entire track.
func runVisualAudio(track Track, in, out *os.File, audioURL string, mute bool) error {
	restore, err := makeRaw(in)
	if err == nil {
		defer restore()
	}
	fmt.Fprint(out, "\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h\x1b[2J\x1b[H")

	tap, err := StartLevelTap(audioURL)
	if err != nil {
		fmt.Fprintf(out, "level tap unavailable: %v\r\n", err)
	}
	if tap != nil {
		defer tap.Close()
	}

	args := []string{"--no-config", "--no-video"}
	if mute {
		// Muted is a real, tested path: the spectrum is analysed from the ffmpeg
		// tap, which is independent of mpv's audio output, so the visualizer
		// still animates with no sound.
		args = append(args, "--mute=yes")
	}
	audio := exec.Command("mpv", append(args, audioURL)...)
	// mpv prints a status line to stdout many times a second. Inheriting our
	// terminal means it paints over every frame (verified: 0 frames survived).
	audio.Stdout = io.Discard
	audio.Stderr = os.Stderr
	if err := audio.Start(); err != nil {
		return fmt.Errorf("mpv start: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- audio.Wait() }()
	defer func() {
		if audio.Process != nil {
			audio.Process.Kill()
		}
	}()

	quit := watchQuit(in)
	viz := NewVisualizer(bands)

	const fps = 24
	tick := time.NewTicker(time.Second / fps)
	defer tick.Stop()

	for {
		select {
		case <-quit:
			return nil
		case err := <-exited:
			return err
		case <-tick.C:
			if tap != nil {
				viz.Push(tap.Bands())
			}
			// Home + fixed lines, no full clear: less traffic, no flicker.
			fmt.Fprintf(out, "\x1b[H\r%s\r\n\r%s\r\n\rq quit\r",
				viz.Render(), trackSummary(track))
		}
	}
}

func trackSummary(t Track) string {
	s := fmt.Sprintf("%s  —  %s  [%s]", t.Title, t.ChannelText(), t.DurationText())
	if len(s) > 76 {
		s = s[:75] + "…"
	}
	return s
}
