package main

import (
	"strings"
	"testing"
	"time"
)

func TestVisualizerReactsAndClamps(t *testing.T) {
	v := NewVisualizer(8)
	if got := len(v.Render()); got < 8 {
		t.Errorf("width = %d, want at least 8", got)
	}
	before := v.Render()
	v.Push([]float64{1, 1, 1, 1, 1, 1, 1, 1})
	if v.Render() == before {
		t.Error("visualizer did not react to Push")
	}
	// must clamp, not panic or invert
	v.Push([]float64{-5, -5, -5, -5, -5, -5, -5, -5})
	v.Push([]float64{99, 99, 99, 99, 99, 99, 99, 99})
	if strings.ContainsAny(v.Render(), "xyz") {
		t.Errorf("render contains out-of-ramp chars: %q", v.Render())
	}
}

func TestVisualizerPushScalar(t *testing.T) {
	v := NewVisualizer(16)
	before := v.Render()
	v.PushScalar(1.0)
	if v.Render() == before {
		t.Error("PushScalar had no effect")
	}
	v.PushScalar(-1)
	v.PushScalar(2)
}

// TestLevelTapReportsItsOwnDeath is the fix for a silent empty visualiser.
//
// Observed on a live YouTube track: mpv held a googlevideo URL and played fine,
// and the *level tap's* ffmpeg exited within seconds -- visible only as
// `[ffmpeg] <defunct>` under a running process. Nothing noticed. The session
// stayed up, the HUD showed the track advancing, and all eighteen styles drew an
// empty grid for the rest of the song with no message anywhere.
//
// Dead() existed the whole time and nothing called it, which is the same shape as
// the Viz.CapScale bug already in AGENTS.md: both halves of the interface, and
// the consumer never written. The doc comment even claimed "the render loop
// reports this through the status line once per tap" -- it did not.
//
// Pointed at a file that does not exist, ffmpeg exits immediately, so this needs
// no network and no real stream: local ffmpeg only.
func TestLevelTapReportsItsOwnDeath(t *testing.T) {
	lt, err := StartLevelTapAt("/nonexistent/definitely-not-here.m4a", 0)
	if err != nil {
		t.Skipf("cannot start a local ffmpeg: %v", err)
	}
	defer lt.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if d := lt.Dead(); d != nil {
			// It has to say *why*, not just that the pipe closed. A bare EOF is the
			// symptom, and reporting the symptom is what this whole bug was.
			if !strings.Contains(strings.ToLower(d.Error()), "no such file") &&
				!strings.Contains(strings.ToLower(d.Error()), "error") {
				t.Errorf("the tap's death is reported as %q, which does not say why", d)
			}
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("the tap's ffmpeg exited and Dead() never reported it")
}

// TestLevelTapCapturesStderr pins the other half. With no Stderr set the child
// inherits this process's stderr, which is the terminal -- in raw mode, being
// drawn on. So ffmpeg's own explanation of its failure was interleaved with the
// visualiser and could not be read.
func TestLevelTapCapturesStderr(t *testing.T) {
	lt, err := StartLevelTapAt("/nonexistent/definitely-not-here.m4a", 0)
	if err != nil {
		t.Skipf("cannot start a local ffmpeg: %v", err)
	}
	defer lt.Close()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if s := lt.StderrTail(); s != "" {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Error("ffmpeg wrote nothing to the tap's captured stderr")
}
