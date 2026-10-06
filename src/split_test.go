package main

import (
	"bytes"
	"testing"
)

// splitTestFrame fills a frame buffer with a recognisable per-pixel pattern, so a
// copy landing in the wrong place is visible rather than plausible.
func splitTestFrame(cols, rows, perCell, bpc int, seed byte) []byte {
	f := make([]byte, cols*rows*perCell*bpc)
	for i := range f {
		f[i] = byte(i)*7 + seed
	}
	return f
}

// TestVideoTapGeometryMatchesPaneFrames is the invariant the whole split rests on:
// the number of bytes ffmpeg is asked for has to equal the number the compositor
// expects to copy. If these drift, the compositor walks off the end of a buffer
// and reads garbage, which looks like noise on screen rather than an error.
func TestVideoTapGeometryMatchesPaneFrames(t *testing.T) {
	for _, mode := range []ColorMode{ColorNone, Color256, ColorTrue} {
		for _, glyph := range []GlyphMode{GlyphHalf, GlyphCell} {
			for _, cols := range []int{2, 7, 40} {
				for _, rows := range []int{1, 5, 24} {
					_, _, size := videoTapGeometry(cols, rows, 12, mode, glyph)
					want := paneFrameBytes(cols, rows, mode, glyph)
					if size != want {
						t.Errorf("mode=%v glyph=%v %dx%d: ffmpeg frame %d bytes, compositor wants %d",
							mode, glyph, cols, rows, size, want)
					}
				}
			}
		}
	}
}

// TestClampDividerKeepsBothPanesUsable pins the bounds.
func TestClampDividerKeepsBothPanesUsable(t *testing.T) {
	for _, cols := range []int{10, 20, 80, 200} {
		lo := int(float64(cols) * splitMinFrac)
		hi := int(float64(cols) * splitMaxFrac)
		if got := clampDivider(cols, -5); got != lo {
			t.Errorf("cols=%d: clampDivider(-5) = %d, want %d", cols, got, lo)
		}
		if got := clampDivider(cols, cols+50); got != hi {
			t.Errorf("cols=%d: clampDivider(%d) = %d, want %d", cols, cols+50, got, hi)
		}
		// A value already inside the range must pass through untouched. Picked
		// from the range rather than hardcoded, because a fixed number like 7 is
		// below the floor at 200 columns and the clamp is then correct -- which is
		// exactly the mistake the first version of this test made.
		mid := lo + (hi-lo)/2
		if got := clampDivider(cols, mid); got != mid {
			t.Errorf("cols=%d: clampDivider(%d) = %d, want it in range untouched", cols, mid, got)
		}
		// And the result is always a usable width for both panes.
		got := clampDivider(cols, mid)
		if got <= 0 || got >= cols {
			t.Errorf("cols=%d: divider %d leaves a pane with no columns", cols, got)
		}
	}
}

// TestSplitLayoutPanesTileTheGrid: the two panes must exactly cover the grid, with
// no gap and no overlap, in both orientations. An off-by-one here is a column
// that never gets painted, which the diff cache then holds forever.
func TestSplitLayoutPanesTileTheGrid(t *testing.T) {
	const cols, rows = 37, 11
	for _, videoLeft := range []bool{false, true} {
		for _, want := range []int{5, 18, 30} {
			s := computeSplitLayout(cols, rows, want, videoLeft)
			if s.viz.cols+s.video.cols != cols {
				t.Errorf("videoLeft=%v want=%d: panes are %d+%d cols, want %d",
					videoLeft, want, s.viz.cols, s.video.cols, cols)
			}
			if s.viz.rows != rows || s.video.rows != rows {
				t.Errorf("videoLeft=%v want=%d: pane rows %d/%d, want %d/%d",
					videoLeft, want, s.viz.rows, s.video.rows, rows, rows)
			}
			// Whichever side the video is on, its x must line up with the divider
			// and the visualiser must take the rest.
			if videoLeft {
				if s.video.x != 0 || s.viz.x != s.divider {
					t.Errorf("video left: video.x=%d viz.x=%d divider=%d", s.video.x, s.viz.x, s.divider)
				}
			} else {
				if s.viz.x != 0 || s.video.x != s.divider {
					t.Errorf("video right: viz.x=%d video.x=%d divider=%d", s.viz.x, s.video.x, s.divider)
				}
			}
		}
	}
}

// TestComposeSplitFramePlacesBothPanes walks the composed buffer and checks each
// pane's bytes arrived at the right offset and nowhere else.
func TestComposeSplitFramePlacesBothPanes(t *testing.T) {
	const fullCols, fullRows = 21, 6
	for _, mode := range []ColorMode{ColorNone, ColorTrue} {
		for _, glyph := range []GlyphMode{GlyphHalf, GlyphCell} {
			for _, videoLeft := range []bool{false, true} {
				s := computeSplitLayout(fullCols, fullRows, 9, videoLeft)
				dst := make([]byte, frameBytesFor(fullCols, fullRows, mode, glyph))
				vz := splitTestFrame(s.viz.cols, fullRows, perCellFor(mode, glyph), bytesPerCell(mode), 0)
				vd := splitTestFrame(s.video.cols, fullRows, perCellFor(mode, glyph), bytesPerCell(mode), 0)

				if !composeSplitFrame(dst, s, fullCols, fullRows, mode, glyph, vz, vd) {
					t.Fatalf("compose refused: mode=%v glyph=%v left=%v", mode, glyph, videoLeft)
				}

				perCell := perCellFor(mode, glyph)
				bpc := bytesPerCell(mode)
				// Spot-check one pixel row per pane against each source.
				for _, pane := range []paneRect{s.viz, s.video} {
					var src []byte
					if pane == s.viz {
						src = vz
					} else {
						src = vd
					}
					for _, y := range []int{0, fullRows / 2, fullRows - 1} {
						from := y * perCell * pane.cols * bpc
						rowOff := y*perCell*fullCols*bpc + pane.x*bpc
						if !bytes.Equal(dst[rowOff:rowOff+pane.cols*bpc], src[from:from+pane.cols*bpc]) {
							t.Errorf("mode=%v glyph=%v left=%v: pane x=%d row %d mismatch",
								mode, glyph, videoLeft, pane.x, y)
						}
					}
				}

				// Every byte of dst must belong to exactly one pane. Rebuild the
				// expectation from scratch and compare whole: that catches a copy
				// landing a row off, which a spot check would miss.
				want := make([]byte, len(dst))
				for y := 0; y < fullRows; y++ {
					for k := 0; k < perCell; k++ {
						rowOff := y*perCell*fullCols*bpc + k*fullCols*bpc
						copy(want[rowOff+s.viz.x*bpc:rowOff+s.viz.x*bpc+s.viz.cols*bpc],
							vz[y*perCell*s.viz.cols*bpc+k*s.viz.cols*bpc:])
						copy(want[rowOff+s.video.x*bpc:rowOff+s.video.x*bpc+s.video.cols*bpc],
							vd[(y*perCell+k)*s.video.cols*bpc:])
					}
				}
				if !bytes.Equal(dst, want) {
					t.Errorf("mode=%v glyph=%v left=%v: composed frame does not match the independent rebuild",
						mode, glyph, videoLeft)
				}
			}
		}
	}
}

// TestComposeSplitFrameRejectsMismatchedBuffers: a pane resized without being
// rebuilt must be refused, not allowed to walk off the end.
func TestComposeSplitFrameRejectsMismatchedBuffers(t *testing.T) {
	const cols, rows = 16, 4
	s := computeSplitLayout(cols, rows, 8, false)
	tooSmall := make([]byte, 1)

	if composeSplitFrame(make([]byte, frameBytesFor(cols, rows, ColorNone, GlyphCell)), s,
		cols, rows, ColorNone, GlyphCell, tooSmall, tooSmall) {
		t.Error("a short visualiser buffer was accepted")
	}
	if composeSplitFrame(make([]byte, frameBytesFor(cols, rows, ColorNone, GlyphCell)), s,
		cols, rows, ColorNone, GlyphCell, make([]byte, s.viz.cols*rows), tooSmall) {
		t.Error("a short video buffer was accepted")
	}
	if composeSplitFrame(tooSmall, s, cols, rows, ColorNone, GlyphCell,
		make([]byte, s.viz.cols*rows), make([]byte, s.video.cols*rows)) {
		t.Error("a short destination was accepted")
	}
}
