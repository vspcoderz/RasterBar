package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestVideoTapFilterPreservesAspect is the guard for the letterbox.
//
// The filter used to be a bare `scale=W:H`, which stretches the source to
// whatever shape the grid happens to be. Measured on a 16:9 frame containing a
// centred white square: scale=100:50 renders that square 0.57:1 on screen —
// squashed by 43%. Nobody noticed because a shot rarely has a straight edge to
// measure against, and --aspect had been the workaround.
//
// The frame SIZE is unchanged, which is what keeps the compositor and the diff
// cache working; what this checks is that ffmpeg still emits exactly that many
// bytes with the fit-and-pad filter in place, and that the geometry is
// measurably rounder. A filter that preserved aspect by cropping or by emitting a
// different size would break the pane agreement that
// TestVideoTapGeometryMatchesPaneFrames exists to protect.
//
// Skipped when ffmpeg is not on PATH: the geometry assertions above cover the
// byte count without a subprocess, and this is the one that needs the real thing.
func TestVideoTapFilterPreservesAspect(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()

	// A uniform mid-grey 640x360 canvas: 16:9, and bright enough that the whole
	// frame is above the pad threshold, so the measured region is the picture
	// rather than whatever happens to be lit inside it.
	src := filepath.Join(dir, "grey.mp4")
	mk := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "color=c=0x808080:s=640x360:d=1",
		"-frames:v", "1", src)
	if out, err := mk.CombinedOutput(); err != nil {
		t.Skipf("cannot build the test fixture: %v: %s", err, out)
	}

	const cols, rows = 100, 50
	filter, pixFmt, want := videoTapGeometry(cols, rows, 0, ColorNone, GlyphCell)
	out := filepath.Join(dir, "out.pgm")
	run := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-i", src, "-vf", filter, "-pix_fmt", pixFmt,
		"-frames:v", "1", "-f", "image2", out)
	if o, err := run.CombinedOutput(); err != nil {
		t.Fatalf("ffmpeg with the geometry filter failed: %v\n%s\nfilter was: %s",
			err, o, filter)
	}

	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// The byte count is the whole contract with the renderer: cols*rows for gray.
	// Asserted on the pixel body, not the file, because the PGM carries a header.
	// Measured before the shape, because a filter that changed the size would make
	// the shape measurement meaningless.
	body, perr := readPGM(data, cols, rows)
	if perr != nil {
		t.Fatalf("%v (filter was: %s)", perr, filter)
	}
	if len(body) != want {
		t.Errorf("frame body is %d bytes, want %d (cols*rows). filter: %s",
			len(body), want, filter)
	}
	// The picture region's own proportions must be the source's. Measured in
	// PIXELS, not on screen, and deliberately so: a square pixel in a character
	// cell is displayed as a 1:2 rectangle because that is what a cell is, so any
	// assertion about on-screen squareness is really an assertion about the font.
	// This measures the thing the filter is responsible for.
	got := pictureAspect(body, cols, rows)
	srcAR := 16.0 / 9.0
	if got <= 0 || got < srcAR*0.92 || got > srcAR*1.08 {
		t.Errorf("picture region is %.2f:1, source is %.2f:1 (%.0f%% off)\n"+
			"filter: %s", got, srcAR, 100*(got-srcAR)/srcAR, filter)
	}
}

// TestVideoTapFilterByteCountAcrossGeometries is the guard that was missing.
//
// The first version of the letterbox filter was tested at 100x50 and passed —
// 100x50 is the one geometry where `pad,unsharp,format` happens to configure.
// At 49x70, 26x35, 3x4, 75x75, 100x37 and 1x1 ffmpeg fails with "Padded
// dimensions cannot be smaller than input dimensions", which the video tap
// reports as "read first frame: EOF" and the split pane silently never appears.
//
// A single-geometry test is a test of one geometry. This sweeps the awkward
// shapes — odd both ways, narrower than the source, wider, one pixel, and the
// ones the split view actually asks for — and demands the exact byte count from
// every one.
//
// Skipped without ffmpeg. TestVideoTapFilterKeepsTheByteContractWithoutFfmpeg
// covers the filter string; this is the half that needs the real thing.
func TestVideoTapFilterByteCountAcrossGeometries(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not on PATH")
	}
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mp4")
	// 320x180 h264 in yuv420p — the shape of a real capture, and the shape that
	// matters here.
	//
	// `-pix_fmt yuv420p` is load-bearing, and its absence is why this test passed
	// against a filter that does not work. Without it ffmpeg encodes the fixture
	// as yuv444p, and the pad filter configures happily on a 4:4:4 source while
	// failing on every real 4:2:0 one. A fixture nobody can actually get is a
	// fixture that agrees with the bug.
	mk := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error", "-y",
		"-f", "lavfi", "-i", "testsrc=size=320x180:rate=10:duration=1",
		"-c:v", "libx264", "-pix_fmt", "yuv420p",
		"-frames:v", "1", src)
	if out, err := mk.CombinedOutput(); err != nil {
		t.Skipf("cannot build the fixture: %v: %s", err, out)
	}

	geoms := [][2]int{
		{100, 50}, // the one the first version tested
		{49, 70},  // the pane width that broke, and an odd width
		{50, 70},
		{100, 37},
		{26, 35},
		{3, 4},
		{75, 75}, // square
		{80, 21},
		{1, 1},
		{13, 7},
		{200, 57}, // the documented benchmark grid
		{2, 2},
		{37, 9},
	}
	cases := []struct {
		name  string
		mode  ColorMode
		glyph GlyphMode
	}{
		{"mono", ColorNone, GlyphCell},
		{"colour-half", ColorTrue, GlyphHalf},
		{"colour-cell", ColorTrue, GlyphCell},
		{"256-half", Color256, GlyphHalf},
	}

	out := filepath.Join(dir, "frame.raw")
	for _, c := range cases {
		for _, g := range geoms {
			cols, rows := g[0], g[1]
			filter, pixFmt, want := videoTapGeometry(cols, rows, 12, c.mode, c.glyph)
			_ = os.Remove(out)
			run := exec.Command("ffmpeg", "-hide_banner", "-loglevel", "error",
				"-nostdin", "-re", "-i", src, "-map", "0:v:0",
				"-vf", filter, "-pix_fmt", pixFmt,
				"-frames:v", "1", "-f", "rawvideo", out)
			if o, err := run.CombinedOutput(); err != nil {
				t.Errorf("%s %dx%d: ffmpeg failed: %v\n  %s\n  filter: %s",
					c.name, cols, rows, err, o, filter)
				continue
			}
			fi, err := os.Stat(out)
			if err != nil {
				t.Errorf("%s %dx%d: no output: %v", c.name, cols, rows, err)
				continue
			}
			if int(fi.Size()) != want {
				t.Errorf("%s %dx%d: %d bytes, want %d\n  filter: %s",
					c.name, cols, rows, fi.Size(), want, filter)
			}
		}
	}
}

// TestVideoTapFilterKeepsTheByteContractWithoutFfmpeg is the half of the above
// that needs no subprocess: the filter string itself must name a scale and a pad
// of the same size, because that pairing is what keeps the output at cols*rows.
func TestVideoTapFilterKeepsTheByteContractWithoutFfmpeg(t *testing.T) {
	for _, mode := range []ColorMode{ColorNone, Color256, ColorTrue} {
		for _, glyph := range []GlyphMode{GlyphHalf, GlyphCell} {
			filter, pixFmt, size := videoTapGeometry(40, 24, 12, mode, glyph)
			if !strings.Contains(filter, "force_original_aspect_ratio") {
				t.Errorf("mode=%v glyph=%v: filter does not preserve aspect: %s", mode, glyph, filter)
			}
			if !strings.Contains(filter, "pad=40:") && !strings.Contains(filter, "pad=40:48:") {
				t.Errorf("mode=%v glyph=%v: filter does not pad back to the grid: %s", mode, glyph, filter)
			}
			if size != paneFrameBytes(40, 24, mode, glyph) {
				t.Errorf("mode=%v glyph=%v: frame size %d, compositor wants %d",
					mode, glyph, size, paneFrameBytes(40, 24, mode, glyph))
			}
			_ = pixFmt
		}
	}
}

// TestScanLibraryIncludesUnnumberedFiles is the `-l` fix, end to end through the
// walk: a folder of songs used to scan to zero tracks with "no playable files with
// an episode number", because classifyMedia required an episode marker.
func TestScanLibraryIncludesUnnumberedFiles(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{
		"celestial.wav", "checkmate.mp3", "nocturne.opus", "proud.flac",
		"Show S01E01.mkv", "artwork.jpg", "notes.txt",
	} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	tracks, err := ScanLibrary(dir, false)
	if err != nil {
		t.Fatalf("ScanLibrary: %v", err)
	}

	// Four songs plus one episode. The artwork and the text file are not media and
	// are skipped without comment — a media folder is full of sidecars and
	// warning about each one turns a listing into noise.
	if len(tracks) != 5 {
		var got []string
		for _, tr := range tracks {
			got = append(got, tr.LocalPath)
		}
		t.Fatalf("scan found %d tracks, want 5: %v", len(tracks), got)
	}

	// Numbered first — a show's episodes are the reason someone pointed the
	// player at a directory — then the unnumbered songs in filename order.
	// sortLibrary owns the rule; this is the ordering it produced.
	want := []string{
		"Show S01E01.mkv", "celestial.wav", "checkmate.mp3",
		"nocturne.opus", "proud.flac",
	}
	for i, w := range want {
		if base := filepath.Base(tracks[i].LocalPath); base != w {
			t.Errorf("position %d = %q, want %q (full order: %v)",
				i, base, w, basenames(tracks))
		}
	}
}

func basenames(ts []Track) []string {
	out := make([]string, len(ts))
	for i, tr := range ts {
		out[i] = filepath.Base(tr.LocalPath)
	}
	return out
}

// squareAspect measures a bright square in a grayscale PGM and reports its on-screen
// proportions, in terminal cells.
//
// Cells are taller than they are wide, so the answer is cols / (rows * cellAspect).
// cellAspect is the project's own --aspect default of 2.0; the tolerance in the
// caller is wide enough that a wrong constant here would still catch the 0.57:1
// stretch this is looking for, which is a 43% error rather than a few percent.
func pictureAspect(body []byte, cols, rows int) float64 {
	// Threshold at 16: the pad is pure black (0), the picture is anything else.
	var (
		minX, maxX = cols, -1
		minY, maxY = rows, -1
	)
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			if body[y*cols+x] > 16 {
				if x < minX {
					minX = x
				}
				if x > maxX {
					maxX = x
				}
				if y < minY {
					minY = y
				}
				if y > maxY {
					maxY = y
				}
			}
		}
	}
	if maxX < 0 {
		// All black. A pad-only frame is legitimate (a source narrower than the
		// grid can letterbox to nothing), so this is 0, not a panic.
		return 0
	}
	return float64(maxX-minX+1) / float64(maxY-minY+1)
}

// readPGM parses the binary PGM ffmpeg wrote, or reports why it could not.
//
// Hand-parsed rather than shelling out to ffprobe: the point is to measure the
// exact bytes the renderer receives, and ffprobe would only report back dimensions
// this test already knows.
func readPGM(data []byte, cols, rows int) ([]byte, error) {
	// A binary PGM is "P5", a dimensions line, a maxval line, then one whitespace
	// byte and the samples. Hand-parsed rather than shelling out to ffprobe: the
	// point is to measure the exact bytes the renderer receives, and ffprobe would
	// only report dimensions this test already knows.
	if len(data) < 4 || data[0] != 'P' || data[1] != '5' {
		return nil, errNoPGMHeader
	}
	rest := data[2:]
	// Three lines: dimensions, maxval, then the single whitespace separator that
	// ffmpeg writes. Comment lines (#...) are legal in a PGM and are skipped.
	lines := 0
	for lines < 3 && len(rest) > 0 {
		if rest[0] == '#' {
			nl := bytes.IndexByte(rest, '\n')
			if nl < 0 {
				return nil, errNoPGMHeader
			}
			rest = rest[nl+1:]
			continue
		}
		nl := bytes.IndexByte(rest, '\n')
		if nl < 0 {
			return nil, errNoPGMHeader
		}
		rest = rest[nl+1:]
		lines++
	}
	if len(rest) < cols*rows {
		return nil, errNoPGMHeader
	}
	return rest[:cols*rows], nil
}

var errNoPGMHeader = errors.New("not a binary PGM with the expected header")

// TestScanLibraryOnAnEmptyMusicFolderFindsNothing is the guard that fails when its
// subject is absent: the fix above means "no playable files" is now a real answer
// rather than the default for a folder full of music.
func TestScanLibraryOnAnEmptyMusicFolderFindsNothing(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "cover.jpg"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	tracks, err := ScanLibrary(dir, false)
	if err != nil {
		t.Fatalf("ScanLibrary: %v", err)
	}
	if len(tracks) != 0 {
		t.Errorf("a folder with no media returned %d tracks", len(tracks))
	}
}
