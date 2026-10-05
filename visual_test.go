package main

import (
	"strings"
	"testing"
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
