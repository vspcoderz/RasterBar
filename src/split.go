package main

// The split view: video and visualiser side by side in music mode.
//
// Off by default. It costs a second ffmpeg and roughly half the cells per frame,
// so it is something you turn on rather than something you get.
//
// # Why one renderer and not two
//
// The obvious shape is a FrameRenderer per pane, each diffing its own region.
// That has a trap in it that is easy to miss and produces no wrong output, only
// wrong *colours*: the colour renderer elides an SGR when the cell it is about to
// draw has the same colour as the one it drew last time. That assumption is
// "nothing changed the terminal's colour state since my last cell", and with two
// panes interleaving, the other pane breaks it constantly. Pane A decides it can
// skip an SGR, pane B repaints the screen in between, and A's next frame inherits
// B's colours.
//
// So the two panes are composited in Go into a single full-width frame, and the
// one existing renderer diffs that. It keeps one diff cache covering the whole
// screen, which is also what makes the no-stale-cells suite cover the split view
// for free.
//
// # Why the compositor is a row-wise copy
//
// Both producers already emit the layout the renderers speak, which is what makes
// this cheap rather than a rewrite:
//
//	mono:    rows of cols bytes
//	colour:  per cell row, perCell rows of cols*3 bytes (cell-row-major)
//
// The video pane is ffmpeg's rawvideo at the pane's own width, and the visualiser
// pane is VizGrid's MonoFrame/ColorFrame at the pane's own width. Both are
// "rows of (width * bytesPerPixel) bytes", so placing one next to the other is a
// memcpy per row with no per-cell work and no conversion.

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
)

// splitMinFrac and splitMaxFrac bound the divider.
//
// A quarter each side, not a minimum pane width alone: a 40-column terminal would
// otherwise allow a 1-column pane, and the video pane is the one that breaks
// visibly when starved (it loses its aspect before the visualiser notices).
const (
	splitMinFrac = 0.25
	splitMaxFrac = 0.75
)

// paneRect is a rectangle of the grid in cells.
type paneRect struct {
	x, y, cols, rows int
}

// splitLayout is the two panes of the split view, in cells.
//
// Both panes always span the full height of the grid. The split is horizontal
// only, which is deliberate: it means chromeRows, the HUD, and the renderer are
// all untouched by the split. Only the grid is divided.
type splitLayout struct {
	viz     paneRect
	video   paneRect
	divider int // first column of the video pane
}

// clampDivider keeps the divider inside the allowed range for a grid this wide.
//
// Exposed as a function of (cols, want) so the key handler and the layout
// builder cannot disagree about where the divider is allowed to sit.
func clampDivider(cols, want int) int {
	lo := int(float64(cols) * splitMinFrac)
	hi := int(float64(cols) * splitMaxFrac)
	if hi < lo+1 {
		hi = lo + 1
	}
	if hi > cols {
		hi = cols
	}
	if want < lo {
		return lo
	}
	if want > hi {
		return hi
	}
	return want
}

// splitState is the split view's user-facing state.
//
// Its own type, not four locals in the render loop, and that is a bug fix rather
// than tidiness: as locals they were unreachable from a test, so the first version
// shipped with the divider initialised to 0 -- where BOTH nudge directions clamp
// to the same floor and the { and } keys silently did nothing. Nothing could catch
// it, because nothing outside playTrack could read it.
//
// The render loop still owns the *geometry* (it is the only place that knows what
// the grid currently is); this owns what the user asked for.
type splitState struct {
	on        bool
	videoLeft bool
	thumb     bool
	// divider is the width of the LEFT pane, so the second pane always begins at
	// exactly this column whichever side it is on.
	divider int
}

// initDivider puts the divider at an even split.
//
// Half, not "unset". Starting at 0 looked harmless -- it clamps to the quarter
// floor -- but it is also where both nudge directions resolve to the same value,
// so the divider keys did nothing until the user had already moved it past the
// floor by hand. Even is also what the split is supposed to look like.
func (s *splitState) initDivider(cols int) {
	s.divider = clampDivider(cols, cols/2)
}

func (s *splitState) toggle()           { s.on = !s.on }
func (s *splitState) setSide(left bool) { s.videoLeft = left }
func (s *splitState) toggleThumb()      { s.thumb = !s.thumb }

// nudge moves the divider by delta columns, clamped to the allowed range.
//
// Clamping both ends is the point: the floor exists so the video pane cannot be
// starved to nothing, and hitting it should stop rather than wrap.
func (s *splitState) nudge(cols, delta int) {
	s.divider = clampDivider(cols, s.divider+delta)
}

// layout is the two panes for a grid this size.
func (s *splitState) layout(cols, rows int) splitLayout {
	return computeSplitLayout(cols, rows, s.divider, s.videoLeft)
}

// vizRect is the visualiser's rectangle: the whole grid with the split off, and
// its pane with it on.
func (s *splitState) vizRect(cols, rows int) paneRect {
	if !s.on {
		return paneRect{cols: cols, rows: rows}
	}
	return s.layout(cols, rows).viz
}

// computeSplitLayout divides a grid into a visualiser pane and a video pane.
//
// divider is the width of the LEFT pane, which is what makes "move it one column
// right" the same operation in both orientations and leaves the second pane
// always starting at exactly `divider`. Defining it any other way means the key
// handler has to know which side is which.
func computeSplitLayout(cols, rows int, divider int, videoLeft bool) splitLayout {
	d := clampDivider(cols, divider)
	rest := cols - d
	var vz, vd paneRect
	if videoLeft {
		vd = paneRect{x: 0, y: 0, cols: d, rows: rows}
		vz = paneRect{x: d, y: 0, cols: rest, rows: rows}
	} else {
		vz = paneRect{x: 0, y: 0, cols: d, rows: rows}
		vd = paneRect{x: d, y: 0, cols: rest, rows: rows}
	}
	return splitLayout{viz: vz, video: vd, divider: d}
}

// bytesPerCell is how many bytes one cell contributes to a frame buffer.
//
// 1 for mono (one ramp level) and 3 for colour (rgb24). perCell is the separate
// question of how many stacked pixels a cell holds: 2 for the half-block layout,
// 1 otherwise.
func bytesPerCell(mode ColorMode) int {
	if mode == ColorNone {
		return 1
	}
	return 3
}

// perCellFor is how many stacked pixels a cell holds in this mode and glyph.
//
// The half-block layout doubles the height, but only in colour: the mono renderer
// wants one ramp level per cell and never sees a pixel, so doubling there made the
// compositor expect twice the bytes ffmpeg produces. It read as noise on screen
// rather than as an error, which is why TestVideoTapGeometryMatchesPaneFrames
// exists.
func perCellFor(mode ColorMode, glyph GlyphMode) int {
	if mode == ColorNone {
		return 1
	}
	if glyph == GlyphHalf {
		return 2
	}
	return 1
}

// frameBytesFor is the size of a full frame buffer for a grid of this geometry.
//
// This is the size ColorDiffRenderer.Draw validates its input against, so the
// compositor's output must match it exactly or the draw fails on a size check.
func frameBytesFor(cols, rows int, mode ColorMode, glyph GlyphMode) int {
	return cols * rows * perCellFor(mode, glyph) * bytesPerCell(mode)
}

// paneFrameBytes is the same figure for one pane.
func paneFrameBytes(cols, rows int, mode ColorMode, glyph GlyphMode) int {
	return frameBytesFor(cols, rows, mode, glyph)
}

// composeSplitFrame writes both panes into one full-width frame.
//
// vizFrame and vidFrame must be the producers' own buffers at their own pane
// widths. dst must be frameBytesFor(fullCols, fullRows, ...).
//
// Returns false if a buffer is the wrong size for its pane, which would mean
// walking off the end of dst. That check is the reason this is not just three
// copies: a pane resized on the terminal but not rebuilt would otherwise corrupt
// memory, and a panic inside a painter is the failure mode the whole safeDiv
// family exists to avoid.
func composeSplitFrame(dst []byte, split splitLayout, fullCols, fullRows int, mode ColorMode, glyph GlyphMode, vizFrame, vidFrame []byte) bool {
	perCell := perCellFor(mode, glyph)
	bpc := bytesPerCell(mode)
	if perCell < 1 {
		perCell = 1
	}
	if bpc < 1 {
		bpc = 1
	}
	if len(dst) < frameBytesFor(fullCols, fullRows, mode, glyph) {
		return false
	}
	if len(vizFrame) < paneFrameBytes(split.viz.cols, split.viz.rows, mode, glyph) {
		return false
	}
	if len(vidFrame) < paneFrameBytes(split.video.cols, split.video.rows, mode, glyph) {
		return false
	}

	// Column-major-within-a-row offsets: one full row of pixels is
	// fullCols*bpc bytes, and a pane starting at column x begins x*bpc into it.
	rowStride := fullCols * bpc

	for y := 0; y < fullRows; y++ {
		for k := 0; k < perCell; k++ {
			// The full row this pixel row lives in.
			rowOff := y*perCell*rowStride + k*rowStride

			// Visualiser pane. Its own buffer is cell-row-major at its own
			// width, so its pixel row (y, k) is at y*perCell*vzStride +
			// k*vzStride.
			vzStride := split.viz.cols * bpc
			if split.viz.cols > 0 && split.viz.y <= y && y < split.viz.y+split.viz.rows {
				from := y*perCell*vzStride + k*vzStride
				to := rowOff + split.viz.x*bpc
				copy(dst[to:to+split.viz.cols*bpc], vizFrame[from:from+split.viz.cols*bpc])
			}

			// Video pane. ffmpeg's rawvideo is plain row-major at the pane's
			// width, so its pixel row (y, k) is at (y*perCell+k)*vdStride.
			vdStride := split.video.cols * bpc
			if split.video.cols > 0 && split.video.y <= y && y < split.video.y+split.video.rows {
				from := (y*perCell + k) * vdStride
				to := rowOff + split.video.x*bpc
				copy(dst[to:to+split.video.cols*bpc], vidFrame[from:from+split.video.cols*bpc])
			}
		}
	}
	return true
}

// videoPane holds the latest frame for the video pane of a split view.
//
// The pane is a decoration, so frames are dropped rather than queued: the
// decoder goroutine overwrites `frame` in place and the render loop takes
// whatever the most recent one was. Blocking the render loop on a second ffmpeg
// would mean the visualiser's frame rate becomes the video's, which is the
// opposite of the intent.
//
// Latest copies its return value, because the writer is mid-overwrite the
// instant it returns.
type videoPane struct {
	mu     sync.Mutex
	frame  []byte
	cols   int
	rows   int
	live   *videoTap
	stop   chan struct{}
	closed bool
}

// newVideoPane allocates the pane's buffer at a geometry.
func newVideoPane(cols, rows int, mode ColorMode, glyph GlyphMode) *videoPane {
	n := paneFrameBytes(cols, rows, mode, glyph)
	if n < 1 {
		n = 1
	}
	return &videoPane{cols: cols, rows: rows, frame: make([]byte, n)}
}

// Resize reallocates for a new geometry. The contents are dropped: the producer
// will refill on its next frame, and preserving a misaligned buffer would be the
// exact aliasing bug this pane is careful about.
func (p *videoPane) Resize(cols, rows int, mode ColorMode, glyph GlyphMode) {
	p.mu.Lock()
	p.cols, p.rows = cols, rows
	p.frame = make([]byte, paneFrameBytes(cols, rows, mode, glyph))
	p.mu.Unlock()
}

// Set replaces the contents wholesale, for a source that produces one frame and
// is done (the thumbnail).
func (p *videoPane) Set(frame []byte) {
	p.mu.Lock()
	p.frame = frame
	p.mu.Unlock()
}

// Latest returns a copy of the current frame, or nil if there is nothing yet.
func (p *videoPane) Latest() []byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.frame) == 0 {
		return nil
	}
	out := make([]byte, len(p.frame))
	copy(out, p.frame)
	return out
}

// startLive pipes real video into the pane.
//
// ffmpeg is started at the pane's own width and frame rate, so a half-width pane
// asks for a smaller stream rather than decoding a full one and throwing most of
// it away. The first frame is read synchronously (that is what startVideoTap
// does) so the pane has something to show immediately.
func (p *videoPane) startLive(videoURL string, fps int, startAt float64, mode ColorMode, glyph GlyphMode) error {
	vt, err := startVideoTap(videoURL, p.cols, p.rows, fps, startAt, mode, glyph)
	if err != nil {
		return err
	}
	p.mu.Lock()
	p.live = vt
	p.cols, p.rows = vt.cols, vt.rows
	stop := make(chan struct{})
	p.stop = stop
	// Frame one is already in hand; show it before the goroutine starts.
	first := make([]byte, len(vt.first))
	copy(first, vt.first)
	if len(first) <= len(p.frame) {
		copy(p.frame, first)
	}
	p.mu.Unlock()

	go p.pumpLive(vt, stop)
	return nil
}

// pumpLive keeps the newest frame and drops whatever arrives while the render
// loop is busy. The exit condition is the read failing, which happens when ffmpeg
// ends or is killed on Close.
func (p *videoPane) pumpLive(vt *videoTap, stop chan struct{}) {
	frame := make([]byte, vt.size)
	for {
		select {
		case <-stop:
			return
		default:
		}
		if _, err := io.ReadFull(vt.out, frame); err != nil {
			return
		}
		p.mu.Lock()
		if p.frame == nil || len(p.frame) < len(frame) {
			p.frame = make([]byte, len(frame))
		}
		copy(p.frame, frame)
		p.mu.Unlock()
	}
}

// Close retires the pane's process.
//
// Kill before Wait, and never close the read end first: Close on the read side of
// a pipe with a read in flight waits for that read, which is waiting on ffmpeg,
// which is blocked on a full pipe. That is the same deadlock SyncPlayer.Close
// documents, and it is why kill() exists rather than a bare out.Close().
func (p *videoPane) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	stop := p.stop
	vt := p.live
	p.stop = nil
	p.live = nil
	p.mu.Unlock()

	if stop != nil {
		close(stop)
	}
	if vt != nil {
		vt.kill()
	}
}

// startThumbnail fetches the still image once and holds it.
//
// The whole thumbnail design rests on this producing exactly one frame of
// paneFrameBytes bytes, which is why it reuses videoTapGeometry rather than
// writing its own filter: the geometry has to agree with the live pane's or the
// compositor would be handed a misaligned buffer.
//
// ffmpeg reads the thumbnail URL directly. That is not the "ffmpeg on a youtube.com
// URL" dead end -- that is ffmpeg being handed an HTML page, and a thumbnail is a
// real image file over https. Verified against a live thumbnail: exactly
// paneFrameBytes bytes for both gray and rgb24.
func startThumbnail(thumbURL string, cols, rows int, mode ColorMode, glyph GlyphMode) ([]byte, error) {
	filter, pixFmt, size := videoTapGeometry(cols, rows, 0, mode, glyph)
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", thumbURL,
		"-map", "0:v:0",
		"-vf", filter,
		"-frames:v", "1",
		"-pix_fmt", pixFmt,
		"-f", "rawvideo", "pipe:1",
	}
	cmd := exec.Command("ffmpeg", args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("thumbnail start: %w", err)
	}
	frame := make([]byte, size)
	// One frame only, so this cannot block indefinitely; the read is bounded by
	// the image size rather than by a duration.
	_, readErr := io.ReadFull(out, frame)
	_ = out.Close()
	_ = cmd.Wait()
	if readErr != nil {
		return nil, fmt.Errorf("thumbnail read: %w", readErr)
	}
	return frame, nil
}

// fillPaneBackground fills (or grows) a pane-sized buffer with the palette's
// background colour, and returns it.
//
// The placeholder for a pane that has not produced a frame yet. The palette
// background rather than zero bytes, for the reason VizGrid.Clear uses it: pure
// black on a dark terminal makes the pane read as a hole in the picture rather
// than as a panel that has not filled in yet.
func fillPaneBackground(buf []byte, cols, rows int, mode ColorMode, glyph GlyphMode, p palette) []byte {
	n := paneFrameBytes(cols, rows, mode, glyph)
	if cols <= 0 || rows <= 0 || n <= 0 {
		return buf
	}
	if len(buf) < n {
		buf = make([]byte, n)
	}
	buf = buf[:n]
	if mode == ColorNone {
		// One ramp level per cell. Index 1 is what VizGrid.Clear writes, so the
		// placeholder matches what the visualiser pane would show if nothing had
		// been drawn there.
		for i := range buf {
			buf[i] = 1
		}
		return buf
	}
	bg := p.bgColor()
	r, g, b := byte(bg>>16), byte(bg>>8), byte(bg)
	for i := 0; i+2 < len(buf); i += 3 {
		buf[i], buf[i+1], buf[i+2] = r, g, b
	}
	return buf
}

// videoTapGeometry is the ffmpeg filter, pixel format and frame size for a grid or
// pane of this geometry. Shared with startVideoTap so the live pane and the
// thumbnail cannot disagree about layout.
func videoTapGeometry(cols, rows, fps int, mode ColorMode, glyph GlyphMode) (filter, pixFmt string, frameBytes int) {
	var parts []string
	if fps > 0 {
		parts = append(parts, fmt.Sprintf("fps=%d", fps))
	}
	if mode == ColorNone {
		parts = append(parts,
			fmt.Sprintf("scale=%d:%d:flags=area", cols, rows),
			"unsharp=5:5:0.7:5:5:0.0",
			"format=gray")
		return strings.Join(parts, ","), "gray", cols * rows
	}
	outRows := rows
	if glyph == GlyphHalf {
		outRows = rows * 2
	}
	parts = append(parts,
		fmt.Sprintf("scale=%d:%d:flags=area", cols, outRows),
		"format=rgb24")
	return strings.Join(parts, ","), "rgb24", cols * outRows * 3
}
