package main

import (
	"io"
	"math"
	"testing"
)

// Benchmarks for the per-frame hot paths.
//
// The two that matter most are the colour renderer and the style Paint: both run
// once per changed cell, and both were dominated by work that was repeated per
// cell when it was really per row, per spoke, or once.
//
// BenchmarkColorDrawWorstCase is the honest worst case -- every cell changes
// every frame -- because that is when one SGR per cell is unavoidable and the
// cost of *building* it is fully exposed.

func benchColorFrame(cols, rows, pix int) []byte {
	f := make([]byte, cols*rows*pix*3)
	for i := range f {
		f[i] = byte(i*7 + i/3)
	}
	return f
}

func benchBands(n int) []float64 {
	b := make([]float64, n)
	for i := range b {
		b[i] = math.Abs(math.Sin(float64(i)*0.7)) * 0.9
	}
	return b
}

func BenchmarkColorDrawWorstCase(b *testing.B) {
	const cols, rows = 200, 57
	f := benchColorFrame(cols, rows, 2)
	r := NewColorDiffRenderer(io.Discard, cols, rows, ColorTrue, GlyphHalf)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.ForceNext() // full repaint, so every cell emits an SGR
		if err := r.Draw(f); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkColorDrawPartial(b *testing.B) {
	const cols, rows = 200, 57
	f := benchColorFrame(cols, rows, 2)
	g := benchColorFrame(cols, rows, 2)
	r := NewColorDiffRenderer(io.Discard, cols, rows, ColorTrue, GlyphHalf)
	if err := r.Draw(g); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := r.Draw(f); err != nil {
			b.Fatal(err)
		}
		g, f = f, g // alternate so every frame differs from the last
	}
}

func BenchmarkMonoDraw(b *testing.B) {
	const cols, rows = 200, 57
	f := make([]byte, cols*rows)
	for i := range f {
		f[i] = byte(i * 3)
	}
	r := NewDiffRenderer(io.Discard, cols, rows)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		r.ForceNext()
		if err := r.Draw(f); err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkBarsPaint(b *testing.B) {
	const cols, rows = 200, 60
	v := &barsViz{}
	v.Resize(cols, rows)
	v.Push(&AudioFrame{Bands: benchBands(bands), Beat: 1})
	g := NewVizGrid(cols, rows, true, 2)
	g.SetPalette(palettes[0])
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Clear()
		v.Paint(g)
	}
}

func BenchmarkRadialPaint(b *testing.B) {
	const cols, rows = 200, 60
	v := &radialViz{}
	v.Resize(cols, rows)
	v.Push(&AudioFrame{Bands: benchBands(bands), Beat: 1})
	g := NewVizGrid(cols, rows, true, 2)
	g.SetPalette(palettes[0])
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Clear()
		v.Paint(g)
	}
}

func BenchmarkWaterfallPaint(b *testing.B) {
	const cols, rows = 200, 60
	v := &waterfallViz{}
	v.Resize(cols, rows)
	for i := 0; i < 20; i++ {
		v.Push(&AudioFrame{Bands: benchBands(bands), Beat: 1})
	}
	g := NewVizGrid(cols, rows, true, 2)
	g.SetPalette(palettes[0])
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		g.Clear()
		v.Paint(g)
	}
}

func BenchmarkColorFrame(b *testing.B) {
	const cols, rows = 200, 60
	g := NewVizGrid(cols, rows, true, 2)
	g.SetPalette(palettes[0])
	for i := range g.gray {
		g.gray[i] = byte(i)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = g.ColorFrame()
	}
}

func BenchmarkSpectrumAnalyze(b *testing.B) {
	s := NewSpectrumAnalyzer(spectrumRate, bands)
	samples := make([]float64, fftSize)
	for i := range samples {
		samples[i] = math.Sin(float64(i) * 0.05)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s.Analyze(samples)
	}
}

func BenchmarkOnsetPush(b *testing.B) {
	d := NewOnsetDetector(bands)
	mags := benchBands(bands)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		d.Push(mags)
	}
}

func BenchmarkMiniBars(b *testing.B) {
	lev := benchBands(bands)
	pk := benchBands(bands)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = miniBars(lev, 200, pk)
	}
}

// BenchmarkEveryStylePaint is the registry-wide paint cost, and it is the one
// that matters for a new style.
//
// Per-style benchmarks have to be written by hand when a style is added, which is
// to say they are written once and never again, and the rule that painting
// allocates nothing goes unchecked for every style added after them. This one
// cannot be forgotten: it walks vizRegistry, so the nineteenth style is measured
// the day it lands.
//
// Allocations are the hard signal, not ns/op: on this box the timing is noisy
// (a 2.5GHz i5 under load) but an allocation in a per-cell path is a fact rather
// than a measurement. Every sub-benchmark here must report 0 allocs/op.
func BenchmarkEveryStylePaint(b *testing.B) {
	const cols, rows = 200, 60
	wave := make([]float64, fftSize)
	for i := range wave {
		wave[i] = math.Sin(float64(i) * 0.21)
	}
	for _, mk := range vizRegistry {
		v := mk()
		b.Run(v.Name(), func(b *testing.B) {
			v.Resize(cols, rows)
			// Fed a full history before timing, so the styles that own one (the
			// phosphor trails, the scroll buffers) are measured on a full frame
			// rather than on an empty one that skips every cell.
			for i := 0; i < 30; i++ {
				v.Push(&AudioFrame{Bands: benchBands(bands), Wave: wave, Beat: 1, BPM: 128})
			}
			g := NewVizGrid(cols, rows, true, 2)
			g.SetPalette(palettes[0])
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				g.Clear()
				v.Paint(g)
			}
		})
	}
}
