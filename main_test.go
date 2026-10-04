package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestParseYtdlpFlatPlaylist(t *testing.T) {
	raw := []byte(`{"_type":"url","id":"lTRiuFIWV54","url":"https://www.youtube.com/watch?v=lTRiuFIWV54","title":"1 A.M Study Session","channel":"Lofi Girl","uploader":"Lofi Girl","duration":3674,"thumbnails":[{"url":"small.jpg","height":202,"width":360},{"url":"big.jpg","height":720,"width":1280}]}
{"_type":"url","id":"abc123","url":"https://www.youtube.com/watch?v=abc123","title":"Second","channel":"Chan2","duration":0}`)
	tracks, err := parseYtdlp(raw)
	if err != nil {
		t.Fatalf("parseYtdlp: %v", err)
	}
	if len(tracks) != 2 {
		t.Fatalf("want 2 tracks, got %d", len(tracks))
	}
	got := tracks[0]
	if got.ID != "lTRiuFIWV54" {
		t.Errorf("ID = %q", got.ID)
	}
	if got.DurationText() != "1:01:14" {
		t.Errorf("Duration = %q, want 1:01:14", got.DurationText())
	}
	if !strings.HasSuffix(got.Thumbs, "big.jpg") {
		t.Errorf("Thumbs = %q, want largest", got.Thumbs)
	}
	if got2 := tracks[1].DurationText(); got2 != "--:--" {
		t.Errorf("zero duration = %q, want --:--", got2)
	}
}

func TestParseYtdlpSkipsMalformed(t *testing.T) {
	tracks, err := parseYtdlp([]byte("not json\n{\"id\":\"ok\",\"title\":\"t\"}\n"))
	if err != nil {
		t.Fatalf("parseYtdlp: %v", err)
	}
	if len(tracks) != 1 || tracks[0].ID != "ok" {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
}

func TestParseYtdlpEmptyFails(t *testing.T) {
	if _, err := parseYtdlp([]byte("")); err == nil {
		t.Fatal("want error on empty input")
	}
}

func TestParseYtfzfJSONWithTerminalNoise(t *testing.T) {
	// Shape captured from a real `ytfzf -c yt -I J` run, wrapped in the escape
	// sequences fzf leaves behind (see PLAN.md).
	raw := []byte("\x1b[?1049h\x1bScraping Youtube (with https://www.youtube.com) (lofi)\n" +
		`[{"scraper":"youtube_search","url":"https://youtube.com/watch?v=BCxTQq0UiFs","title":"Chill Lofi","channel":"Art Is Sound","duration":"1:42:55","views":"6M","date":"1y ago","ID":"BCxTQq0UiFs","thumbs":"https://i.ytimg.com/x.jpg"}]` +
		"\x1b[?25h\x1b[?1049l\n")
	tracks, err := parseYtfzfJSON(raw)
	if err != nil {
		t.Fatalf("parseYtfzfJSON: %v", err)
	}
	if len(tracks) != 1 {
		t.Fatalf("got %d tracks, want 1", len(tracks))
	}
	if tracks[0].ID != "BCxTQq0UiFs" {
		t.Errorf("ID = %q", tracks[0].ID)
	}
	if tracks[0].ChannelText() != "Art Is Sound" {
		t.Errorf("channel = %q", tracks[0].ChannelText())
	}
	if tracks[0].DurationText() != "1:42:55" {
		t.Errorf("duration = %q", tracks[0].DurationText())
	}
}

func TestParseYtfzfJSONNoArrayFails(t *testing.T) {
	if _, err := parseYtfzfJSON([]byte("Nothing was scraped")); err == nil {
		t.Fatal("want error when no array present")
	}
}

func TestFormatDuration(t *testing.T) {
	for in, want := range map[int]string{3674: "1:01:14", 65: "1:05", 3600: "1:00:00", 59: "0:59"} {
		if got := formatDuration(in); got != want {
			t.Errorf("formatDuration(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderFrameTo(t *testing.T) {
	const cols, rows = 4, 2
	raw := make([]byte, cols*rows)
	for i := range raw {
		raw[i] = 255
	}
	var buf bytes.Buffer
	if err := renderFrameTo(&buf, raw, cols, rows); err != nil {
		t.Fatalf("renderFrameTo: %v", err)
	}
	want := strings.Repeat(string(ramp[len(ramp)-1])+strings.Repeat(string(ramp[len(ramp)-1]), 0), 0) +
		strings.Repeat(string(ramp[len(ramp)-1]), cols) + "\n" +
		strings.Repeat(string(ramp[len(ramp)-1]), cols) + "\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRenderFrameToDarkIsSpaces(t *testing.T) {
	const cols, rows = 4, 2
	var buf bytes.Buffer
	if err := renderFrameTo(&buf, make([]byte, cols*rows), cols, rows); err != nil {
		t.Fatalf("renderFrameTo: %v", err)
	}
	want := strings.Repeat(" ", cols) + "\n" + strings.Repeat(" ", cols) + "\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestRampCoversFullByteRange(t *testing.T) {
	// every possible byte must map to a valid ramp index
	for b := 0; b < 256; b++ {
		idx := b * len(ramp) / 256
		if idx < 0 || idx >= len(ramp) {
			t.Fatalf("byte %d maps to out-of-range index %d", b, idx)
		}
	}
	if idx := 255 * len(ramp) / 256; idx != len(ramp)-1 {
		t.Errorf("byte 255 maps to %d, want %d (brightest char)", idx, len(ramp)-1)
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short string altered: %q", got)
	}
	if got := truncate("abcdefghij", 5); got != "abcd…" {
		t.Errorf("got %q, want abcd…", got)
	}
}

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

func TestFFTIsPowerOfTwoSized(t *testing.T) {
	for _, n := range []int{0, 1, 3, 100} {
		f := NewFFT(n)
		size := len(f.rev)
		if size&(size-1) != 0 {
			t.Errorf("NewFFT(%d) gave non-power-of-two size %d", n, size)
		}
	}
}

// A pure tone must put its energy in the matching FFT bin. This is the test
// that proves the hand-rolled FFT is actually correct and not just fast.
func TestFFTFindsPureTone(t *testing.T) {
	const rate = 11025
	const toneBin = 64 // 64 * rate/fftSize = 689 Hz
	f := NewFFT(fftSize)
	re := make([]float64, fftSize)
	im := make([]float64, fftSize)
	for i := 0; i < fftSize; i++ {
		ang := 2 * math.Pi * float64(toneBin) * float64(i) / float64(fftSize)
		re[i] = math.Cos(ang)
	}
	f.Forward(re, im)

	mag := func(bin int) float64 {
		return math.Hypot(re[bin], im[bin])
	}
	peak := mag(toneBin)
	if peak < 1e-6 {
		t.Fatalf("no energy at expected bin %d (mag=%g)", toneBin, peak)
	}
	// the peak must dominate its neighbours, or the transform is wrong
	for _, nb := range []int{toneBin - 8, toneBin - 2, toneBin + 2, toneBin + 8} {
		if mag(nb) > peak*0.05 {
			t.Errorf("leak into bin %d: %g vs peak %g", nb, mag(nb), peak)
		}
	}
	// DC and the far end should be near zero for a mid-band tone
	if mag(1) > peak*0.05 {
		t.Errorf("unexpected DC energy: %g", mag(1))
	}
}

func TestSpectrumAnalyzerBands(t *testing.T) {
	s := NewSpectrumAnalyzer(11025, 32)
	samples := make([]float64, fftSize)
	mags := s.Analyze(samples)
	if len(mags) != 32 {
		t.Fatalf("got %d bands, want 32", len(mags))
	}
	for i, m := range mags {
		if m < 0 || m > 1 {
			t.Errorf("band %d = %v, want 0..1", i, m)
		}
	}
	// silence must not light up the meter
	sum := 0.0
	for _, m := range mags {
		sum += m
	}
	if sum > 0.5 {
		t.Errorf("silence produced energy: sum=%v", sum)
	}

	// a loud low tone must move the lowest band more than the highest
	low := make([]float64, fftSize)
	for i := range low {
		ang := 2 * math.Pi * 8 * float64(i) / float64(fftSize) // ~86 Hz
		low[i] = math.Cos(ang)
	}
	m2 := NewSpectrumAnalyzer(11025, 32).Analyze(low)
	if m2[0] <= m2[len(m2)-1] {
		t.Errorf("low tone did not favour low band: first=%v last=%v", m2[0], m2[len(m2)-1])
	}
}

func TestComputeLayoutFollowsTerminal(t *testing.T) {
	// A small terminal must not request a big video.
	small := computeLayout(80, 24, 0, 0)
	if small.cols != 80 {
		t.Errorf("small cols = %d, want 80", small.cols)
	}
	if small.sourceH > 360 {
		t.Errorf("80x24 requested %dp, want <= 360p", small.sourceH)
	}

	// A large terminal should scale up.
	big := computeLayout(240, 60, 0, 0)
	if big.cols != 240 {
		t.Errorf("big cols = %d, want 240", big.cols)
	}
	if big.sourceH <= small.sourceH {
		t.Errorf("big terminal sourceH %d not greater than small %d", big.sourceH, small.sourceH)
	}

	// Monotonic: more columns must never lower the requested source.
	prev := 0
	for _, c := range []int{40, 80, 120, 160, 200, 240, 300} {
		l := computeLayout(c, 50, 0, 0)
		if l.sourceH < prev {
			t.Errorf("sourceH dropped from %d to %d at %d cols", prev, l.sourceH, c)
		}
		prev = l.sourceH
	}
}

func TestComputeLayoutRespectsRows(t *testing.T) {
	// A short terminal must clamp rows, and the grid must still fit.
	l := computeLayout(200, 12, 0, 0)
	if l.rows >= 12 {
		t.Errorf("rows = %d, must leave room for chrome in a 12-row window", l.rows)
	}
	if l.rows < 4 {
		t.Errorf("rows = %d, want a usable minimum", l.rows)
	}

	// Very tall window: rows follow cols/aspect, not the whole window.
	tall := computeLayout(80, 200, 0, 0)
	if tall.rows >= 200 {
		t.Errorf("rows = %d, want width/aspect rather than full height", tall.rows)
	}
}

func TestComputeLayoutAspect(t *testing.T) {
	// A taller aspect ratio (cells taller than wide) needs fewer rows.
	normal := computeLayout(100, 50, 2.0, 0)
	tall := computeLayout(100, 50, 4.0, 0)
	if tall.rows >= normal.rows {
		t.Errorf("aspect 4.0 gave rows=%d, expected fewer than aspect 2.0 rows=%d",
			tall.rows, normal.rows)
	}

	// Zero/invalid aspect must fall back to the default, not divide by zero.
	zero := computeLayout(100, 50, 0, 0)
	if zero.rows <= 0 {
		t.Error("zero aspect produced no rows")
	}
}

func TestComputeLayoutQualityOverride(t *testing.T) {
	// An explicit floor raises the source above what the grid implies.
	small := computeLayout(80, 24, 0, 0)
	raised := computeLayout(80, 24, 0, Quality(720))
	if raised.sourceH < 720 {
		t.Errorf("quality 720 gave sourceH %d", raised.sourceH)
	}
	if raised.sourceH <= small.sourceH {
		t.Errorf("quality override did not raise resolution: %d vs %d",
			raised.sourceH, small.sourceH)
	}

	// A cap must not raise it.
	capped := computeLayout(300, 60, 0, Quality(240))
	if capped.sourceH > 720 {
		t.Errorf("cap 240 gave %dp, want it capped", capped.sourceH)
	}
}

func TestComputeLayoutFallbacks(t *testing.T) {
	// Non-TTY (zero) sizes must not produce a zero-sized grid.
	l := computeLayout(0, 0, 0, 0)
	if l.cols <= 0 || l.rows <= 0 {
		t.Errorf("fallback grid is %dx%d, want positive", l.cols, l.rows)
	}

	// Absurd terminal sizes are clamped.
	huge := computeLayout(10000, 10000, 0, 0)
	if huge.cols > maxCols {
		t.Errorf("cols = %d, want clamped to %d", huge.cols, maxCols)
	}
	if huge.rows > maxRows {
		t.Errorf("rows = %d, want clamped to %d", huge.rows, maxRows)
	}
}

func TestParseArgsQualityAndLayout(t *testing.T) {
	o, err := parseArgs([]string{"-q", "720", "--aspect", "2.5", "--cols", "120", "lofi"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if o.quality != Quality(720) {
		t.Errorf("quality = %v, want 720", o.quality)
	}
	if o.aspect != 2.5 {
		t.Errorf("aspect = %v, want 2.5", o.aspect)
	}
	if o.cols != 120 {
		t.Errorf("cols = %d, want 120", o.cols)
	}
	if o.query != "lofi" {
		t.Errorf("query = %q", o.query)
	}

	// Reject nonsense values instead of silently ignoring them.
	for _, bad := range [][]string{
		{"-q", "999"}, {"-q", "abc"}, {"-q"}, {"--aspect", "0"},
		{"--aspect", "-1"}, {"--cols", "0"}, {"--cols", "5"},
	} {
		if _, err := parseArgs(bad); err == nil {
			t.Errorf("parseArgs(%v) should have errored", bad)
		}
	}
}

func TestPickStreamsPrefersProgressive(t *testing.T) {
	formats := []ytFormat{
		{URL: "v720", Height: 720, VCodec: "av01", ACodec: "none", Protocol: "https", TBR: 100},
		{URL: "a251", ACodec: "opus", VCodec: "none", Protocol: "https", TBR: 140},
		{URL: "prog", Height: 360, VCodec: "avc1", ACodec: "mp4a", Protocol: "https"},
	}
	v, a, err := pickStreams(formats, 480)
	if err != nil {
		t.Fatalf("pickStreams: %v", err)
	}
	if v != "prog" || a != "prog" {
		t.Errorf("got (%q,%q), want the progressive stream used for both", v, a)
	}
}

func TestPickStreamsPicksSeparateWhenNoProgressive(t *testing.T) {
	formats := []ytFormat{
		{URL: "v1080-av1", Height: 1080, VCodec: "av01", ACodec: "none", Protocol: "https", TBR: 200},
		{URL: "v360-h264", Height: 360, VCodec: "avc1", ACodec: "none", Protocol: "https", TBR: 40},
		{URL: "v720-vp9", Height: 720, VCodec: "vp09", ACodec: "none", Protocol: "https", TBR: 90},
		{URL: "a140", ACodec: "mp4a", VCodec: "none", Protocol: "https", TBR: 129},
		{URL: "a139", ACodec: "mp4a", VCodec: "none", Protocol: "https", TBR: 49},
	}
	v, a, err := pickStreams(formats, 720)
	if err != nil {
		t.Fatalf("pickStreams: %v", err)
	}
	// h264 must win over vp9 and av1 at equal-or-better height, and the 1080p
	// AV1 stream must be excluded by the 720p cap.
	if v != "v360-h264" {
		t.Errorf("video = %q, want the h264 stream (cheapest to decode)", v)
	}
	if a != "a140" {
		t.Errorf("audio = %q, want the highest-bitrate audio", a)
	}
}

func TestPickStreamsHonoursHeightCap(t *testing.T) {
	formats := []ytFormat{
		{URL: "v1080", Height: 1080, VCodec: "avc1", ACodec: "none", Protocol: "https"},
		{URL: "v480", Height: 480, VCodec: "avc1", ACodec: "none", Protocol: "https"},
		{URL: "a", ACodec: "opus", VCodec: "none", Protocol: "https"},
	}
	v, _, err := pickStreams(formats, 480)
	if err != nil {
		t.Fatalf("pickStreams: %v", err)
	}
	if v != "v480" {
		t.Errorf("video = %q, want v480 (1080p exceeds the cap)", v)
	}
}

func TestPickStreamsSkipsM3u8(t *testing.T) {
	formats := []ytFormat{
		{URL: "v360-m3u8", Height: 360, VCodec: "avc1", ACodec: "none", Protocol: "m3u8"},
		{URL: "v240", Height: 240, VCodec: "avc1", ACodec: "none", Protocol: "https"},
		{URL: "a", ACodec: "opus", VCodec: "none", Protocol: "https"},
	}
	v, _, err := pickStreams(formats, 480)
	if err != nil {
		t.Fatalf("pickStreams: %v", err)
	}
	if v != "v240" {
		t.Errorf("video = %q, want v240; m3u8 must be skipped", v)
	}
}

func TestPickStreamsNoAudioFails(t *testing.T) {
	formats := []ytFormat{
		{URL: "v360", Height: 360, VCodec: "avc1", ACodec: "none", Protocol: "https"},
	}
	if _, _, err := pickStreams(formats, 480); err == nil {
		t.Error("want an error when there is no audio format")
	}
}

func TestIsH264(t *testing.T) {
	// "av01" is AV1, not h264, despite the shared prefix.
	for _, tc := range []struct {
		vc   string
		want bool
	}{
		{"avc1.640028", true},
		{"avc1.4d401e", true},
		{"av01.0.08M.08", false},
		{"vp09.00.40.08", false},
		{"vp8", false},
		{"", false},
	} {
		if got := isH264(tc.vc); got != tc.want {
			t.Errorf("isH264(%q) = %v, want %v", tc.vc, got, tc.want)
		}
	}
}

func TestDiffRendererFirstFramePaintsAll(t *testing.T) {
	const cols, rows = 8, 3
	var buf bytes.Buffer
	d := NewDiffRenderer(&buf, cols, rows)
	frame := make([]byte, cols*rows)
	for i := range frame {
		frame[i] = byte(i * 8)
	}
	if err := d.Draw(frame); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "\x1b[2J") {
		t.Error("first frame should clear the screen")
	}
	// three cursor-positioned rows
	if strings.Count(out, "\x1b[") < rows {
		t.Errorf("expected at least %d cursor moves, got %d", rows, strings.Count(out, "\x1b["))
	}
}

func TestDiffRendererSkipsUnchanged(t *testing.T) {
	const cols, rows = 16, 4
	var buf bytes.Buffer
	d := NewDiffRenderer(&buf, cols, rows)
	frame := make([]byte, cols*rows)
	for i := range frame {
		frame[i] = 128
	}
	if err := d.Draw(frame); err != nil {
		t.Fatal(err)
	}
	full := buf.Len()
	buf.Reset()

	// Identical frame: almost nothing should be written.
	if err := d.Draw(frame); err != nil {
		t.Fatal(err)
	}
	quiet := buf.Len()
	if quiet > full/10 {
		t.Errorf("unchanged frame wrote %d bytes vs %d for the first frame; diffing is not working", quiet, full)
	}

	// One changed cell should cost far less than a full repaint.
	frame[0] = 255
	buf.Reset()
	if err := d.Draw(frame); err != nil {
		t.Fatal(err)
	}
	oneCell := buf.Len()
	if oneCell > full/2 {
		t.Errorf("single-cell change wrote %d bytes vs %d full; runs are not limiting output", oneCell, full)
	}
}

func TestDiffRendererRunWidening(t *testing.T) {
	// A one-cell change must still emit output (cursor move + the cell),
	// even though the run-expansion widens the painted span.
	const cols, rows = 10, 2
	var buf bytes.Buffer
	d := NewDiffRenderer(&buf, cols, rows)
	frame := make([]byte, cols*rows)
	for i := range frame {
		frame[i] = 40
	}
	if err := d.Draw(frame); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	frame[5] = 200
	if err := d.Draw(frame); err != nil {
		t.Fatal(err)
	}
	if buf.Len() == 0 {
		t.Error("a changed cell produced no output")
	}
	if !strings.Contains(buf.String(), "\x1b[") {
		t.Error("changed cell should be positioned with a cursor move")
	}
}

func TestDiffRendererRejectsShortFrame(t *testing.T) {
	d := NewDiffRenderer(&bytes.Buffer{}, 8, 4)
	if err := d.Draw(make([]byte, 4)); err == nil {
		t.Error("want an error for a frame smaller than the grid")
	}
}

func TestLevelForCoversRange(t *testing.T) {
	// The ramp is ordered by perceived ink density, not codepoint, so the
	// meaningful checks are coverage and endpoints.
	if got := levelFor(0); got != ramp[0] {
		t.Errorf("levelFor(0) = %q, want darkest %q", got, ramp[0])
	}
	if got := levelFor(255); got != ramp[len(ramp)-1] {
		t.Errorf("levelFor(255) = %q, want brightest %q", got, ramp[len(ramp)-1])
	}
	// Every byte must map to a character that exists in the ramp, and the
	// distinct level count must show real tonal resolution (not a tiny ramp).
	seen := map[byte]bool{}
	for b := 0; b < 256; b++ {
		g := levelFor(byte(b))
		if !strings.ContainsRune(ramp, rune(g)) {
			t.Fatalf("byte %d mapped to %q which is not in the ramp", b, g)
		}
		seen[g] = true
	}
	if len(seen) < 32 {
		t.Errorf("only %d distinct levels used; ramp is too coarse for smooth gradients", len(seen))
	}
	// Denser input must never map to a *lower ramp index*.
	prevIdx := -1
	for b := 0; b < 256; b++ {
		idx := strings.IndexByte(ramp, levelFor(byte(b)))
		if idx < prevIdx {
			t.Fatalf("ramp index went backwards at byte %d: %d < %d", b, idx, prevIdx)
		}
		prevIdx = idx
	}
}

func TestFPSForGridScalesDown(t *testing.T) {
	small := fpsForGrid(80, 21)
	big := fpsForGrid(200, 57)
	if big > small {
		t.Errorf("big grid fps %d should not exceed small grid fps %d", big, small)
	}
	if small < 6 {
		t.Errorf("small grid fps = %d, want at least 6", small)
	}
	// Cells-per-second must stay in a band a terminal can absorb. Test the
	// clamped layout, not the raw helper: computeLayout is what actually runs.
	for _, tc := range [][2]int{{80, 21}, {120, 40}, {200, 57}, {300, 120}, {400, 200}} {
		l := computeLayout(tc[0], tc[1], 0, 0)
		if cps := l.cols * l.rows * l.fps; cps > 150000 {
			t.Errorf("%dx%d -> grid %dx%d @%dfps = %d cells/sec, too much",
				tc[0], tc[1], l.cols, l.rows, l.fps, cps)
		}
	}
}

func TestDetectColor(t *testing.T) {
	cases := []struct {
		name string
		env  []string
		want ColorMode
	}{
		{"truecolor via COLORTERM", []string{"TERM=xterm-256color", "COLORTERM=truecolor"}, ColorTrue},
		{"24bit", []string{"COLORTERM=24bit"}, ColorTrue},
		{"term advertises truecolor", []string{"TERM=xterm-truecolor"}, ColorTrue},
		{"plain 256", []string{"TERM=xterm-256color"}, Color256},
		{"kitty", []string{"TERM=xterm-kitty"}, Color256},
		{"dumb", []string{"TERM=dumb"}, ColorNone},
		{"plain xterm", []string{"TERM=xterm"}, ColorNone},
		{"empty env", nil, ColorNone},
	}
	for _, tc := range cases {
		if got := detectColor(tc.env); got != tc.want {
			t.Errorf("%s: detectColor(%v) = %v, want %v", tc.name, tc.env, got, tc.want)
		}
	}
}

func TestQuant256GreyRamp(t *testing.T) {
	// Pure greys must land in the 232-255 grey ramp, not the colour cube, or
	// monochrome content picks up a colour cast.
	for _, v := range []byte{0, 10, 64, 128, 200, 255} {
		idx := quant256(v, v, v)
		if idx < 232 {
			t.Errorf("grey %d mapped to %d, want the grey ramp (>=232)", v, idx)
		}
	}
	// Saturated colours must land in the cube (16-231).
	for _, c := range [][3]byte{{255, 0, 0}, {0, 255, 0}, {0, 0, 255}, {255, 255, 0}} {
		idx := quant256(c[0], c[1], c[2])
		if idx < 16 || idx > 231 {
			t.Errorf("colour %v mapped to %d, want the colour cube (16-231)", c, idx)
		}
	}
	// Output must be a valid palette index.
	for r := 0; r < 256; r += 17 {
		for g := 0; g < 256; g += 17 {
			for b := 0; b < 256; b += 17 {
				idx := quant256(byte(r), byte(g), byte(b))
				if idx < 0 || idx > 255 {
					t.Fatalf("quant256(%d,%d,%d) = %d, out of palette", r, g, b, idx)
				}
			}
		}
	}
}

func TestColorRendererEmitsHalfBlocks(t *testing.T) {
	const cols, rows = 4, 2
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, ColorTrue, GlyphHalf)
	frame := make([]byte, r.cellBytes())
	for i := 0; i < len(frame); i++ {
		frame[i] = byte(i * 7 % 256)
	}
	if err := r.Draw(frame); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, string(halfBlock)) {
		t.Errorf("expected half-block glyphs in output, got %q", truncateForLog(out))
	}
	if !strings.Contains(out, "38;2;") {
		t.Error("truecolor mode should emit 24-bit SGR sequences")
	}
	if !strings.Contains(out, "48;2;") {
		t.Error("half-block mode needs a background colour for the lower half")
	}
}

func TestColorRenderer256UsesPalette(t *testing.T) {
	const cols, rows = 4, 2
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, Color256, GlyphHalf)
	if err := r.Draw(make([]byte, r.cellBytes())); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "38;5;") {
		t.Errorf("256 mode should emit palette indices, got %q", truncateForLog(out))
	}
	if strings.Contains(out, "38;2;") {
		t.Error("256 mode must not emit 24-bit sequences")
	}
}

func TestColorRendererSkipsUnchangedCells(t *testing.T) {
	const cols, rows = 12, 4
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, Color256, GlyphHalf)
	frame := make([]byte, r.cellBytes())
	for i := range frame {
		frame[i] = 128
	}
	if err := r.Draw(frame); err != nil {
		t.Fatal(err)
	}
	first := buf.Len()
	buf.Reset()

	// Identical frame: only the per-row reset sequences should remain, not a
	// full repaint of every cell.
	if err := r.Draw(frame); err != nil {
		t.Fatal(err)
	}
	second := buf.Len()
	if second > first/2 {
		t.Errorf("unchanged colour frame wrote %d bytes vs %d for the first frame; diffing is not working",
			second, first)
	}
}

func TestColorRendererRejectsShortFrame(t *testing.T) {
	r := NewColorDiffRenderer(&bytes.Buffer{}, 8, 4, ColorTrue, GlyphHalf)
	if err := r.Draw(make([]byte, 10)); err == nil {
		t.Error("want an error for a frame smaller than the colour grid")
	}
}

func TestColorCellBytesDoublesHeight(t *testing.T) {
	const cols, rows = 10, 5
	r := NewColorDiffRenderer(&bytes.Buffer{}, cols, rows, ColorTrue, GlyphHalf)
	// Two pixels per cell, three bytes each: the half-block layout.
	if got, want := r.cellBytes(), cols*rows*2*3; got != want {
		t.Errorf("cellBytes = %d, want %d", got, want)
	}
}

func TestParseArgsColorFlags(t *testing.T) {
	o, err := parseArgs([]string{"-c", "lofi"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if o.color != ColorTrue {
		t.Errorf("-c gave colour %v, want ColorTrue", o.color)
	}

	m, err := parseArgs([]string{"--mono", "lofi"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if m.color != ColorNone {
		t.Errorf("--mono gave colour %v, want ColorNone", m.color)
	}

	// No flag must mean "detect", not "off".
	a, err := parseArgs([]string{"lofi"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if a.color != colorAuto {
		t.Errorf("default colour = %v, want colorAuto (detect)", a.color)
	}
}

func truncateForLog(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

func TestParseCPRColumn(t *testing.T) {
	cases := map[string]int{
		"\x1b[1;9R":  9,
		"\x1b[1;17R": 17,
		"\x1b[5;1R":  1,
		"garbage":    0,
		"":           0,
		"\x1b[?R":    0,
	}
	for in, want := range cases {
		if got := parseCPRColumn(in); got != want {
			t.Errorf("parseCPRColumn(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestProbeHalfBlockNarrowDetectsWideTerminal(t *testing.T) {
	// Simulate a terminal that reports the cursor advancing 2 columns per
	// half-block: it must be identified as wide.
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	go func() {
		outR.Read(make([]byte, 512)) // drain the probe output
	}()
	go func() {
		inW.Write([]byte("\x1b[1;17R")) // 1 + 2*8
		inW.Close()
	}()
	narrow, ok := probeHalfBlockNarrow(inR, outW)
	outW.Close()
	if !ok {
		t.Error("a terminal that answered the report should count as ok")
	}
	if narrow {
		t.Error("terminal reported 2 columns per glyph; it must not be treated as narrow")
	}
	inR.Close()
}

func TestProbeHalfBlockNarrowDetectsNarrowTerminal(t *testing.T) {
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	go func() {
		outR.Read(make([]byte, 512))
	}()
	go func() {
		inW.Write([]byte("\x1b[1;9R")) // 1 + 1*8
		inW.Close()
	}()
	narrow, ok := probeHalfBlockNarrow(inR, outW)
	outW.Close()
	if !ok || !narrow {
		t.Errorf("got narrow=%v ok=%v, want true/true for a 1-column report", narrow, ok)
	}
	inR.Close()
}

func TestResolveGlyphFallsBackToCell(t *testing.T) {
	// A non-answering terminal must get the always-correct one-pixel layout.
	inR, inW, _ := os.Pipe()
	outR, outW, _ := os.Pipe()
	go func() {
		outR.Read(make([]byte, 2048))
		inW.Close() // EOF: no reply
	}()
	if got := resolveGlyph(GlyphAuto, inR, outW); got != GlyphCell {
		t.Errorf("resolveGlyph with no reply = %v, want GlyphCell", got)
	}
	outW.Close()
	inR.Close()

	// Explicit flags must override detection entirely.
	if got := resolveGlyph(GlyphHalf, nil, nil); got != GlyphHalf {
		t.Errorf("--glyph half must be honoured, got %v", got)
	}
	if got := resolveGlyph(GlyphCell, nil, nil); got != GlyphCell {
		t.Errorf("--glyph cell must be honoured, got %v", got)
	}
}

func TestColorCellModeUsesSpaces(t *testing.T) {
	const cols, rows = 4, 2
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, ColorTrue, GlyphCell)
	frame := make([]byte, r.cellBytes())
	for i := range frame {
		frame[i] = byte(120 + i%40)
	}
	if err := r.Draw(frame); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	out := buf.String()
	if strings.Contains(out, string(halfBlock)) {
		t.Error("cell mode must not emit the ambiguous-width half-block")
	}
	if !strings.Contains(out, "48;2;") {
		t.Error("cell mode should paint pixels as a background colour")
	}
	if strings.Count(out, " ") < cols*rows {
		t.Errorf("expected at least %d space glyphs, got %d", cols*rows, strings.Count(out, " "))
	}
	// One pixel per cell means a frame exactly the size of the grid.
	if got, want := r.cellBytes(), cols*rows*3; got != want {
		t.Errorf("cell-mode frame = %d bytes, want %d", got, want)
	}
}

func TestColorHalfModeFrameDoubles(t *testing.T) {
	const cols, rows = 4, 2
	half := NewColorDiffRenderer(&bytes.Buffer{}, cols, rows, ColorTrue, GlyphHalf)
	single := NewColorDiffRenderer(&bytes.Buffer{}, cols, rows, ColorTrue, GlyphCell)
	if half.cellBytes() != single.cellBytes()*2 {
		t.Errorf("half-block frame %d bytes, want double the cell-mode %d",
			half.cellBytes(), single.cellBytes())
	}
}

func TestParseArgsGlyphFlag(t *testing.T) {
	for _, tc := range []struct {
		arg  string
		want GlyphMode
	}{
		{"auto", GlyphAuto},
		{"half", GlyphHalf},
		{"cell", GlyphCell},
	} {
		o, err := parseArgs([]string{"--glyph", tc.arg, "q"})
		if err != nil {
			t.Fatalf("--glyph %s: %v", tc.arg, err)
		}
		if o.glyph != tc.want {
			t.Errorf("--glyph %s = %v, want %v", tc.arg, o.glyph, tc.want)
		}
	}
	if _, err := parseArgs([]string{"--glyph", "bogus"}); err == nil {
		t.Error("want an error for an unknown --glyph value")
	}
	if _, err := parseArgs([]string{"--glyph"}); err == nil {
		t.Error("--glyph with no value should error")
	}
	// Default must stay "detect".
	o, _ := parseArgs([]string{"q"})
	if o.glyph != GlyphAuto {
		t.Errorf("default glyph = %v, want GlyphAuto", o.glyph)
	}
}

func TestParseTimePosReply(t *testing.T) {
	// mpv sets "error":"success" on a SUCCESSFUL reply. Treating any non-empty
	// error as a failure silently disabled the drift corrector, which showed up
	// as a permanent desync. This case must be accepted.
	got, ok, done := parseTimePosReply(
		[]byte(`{"data":12.5,"error":"success","request_id":7}`), 7)
	if !done || !ok {
		t.Fatalf(`error:"success" must be treated as success (ok=%v done=%v)`, ok, done)
	}
	if got != 12.5 {
		t.Errorf("position = %v, want 12.5", got)
	}

	// A real error must be rejected.
	_, ok, done = parseTimePosReply(
		[]byte(`{"error":"property unavailable","request_id":7}`), 7)
	if !done || ok {
		t.Errorf("a real error must be rejected (ok=%v done=%v)", ok, done)
	}

	// An event for another request must be skipped, not treated as our answer.
	_, ok, done = parseTimePosReply([]byte(`{"event":"playback-restart"}`), 7)
	if done {
		t.Error("an event is not a reply to our request")
	}

	// Null data with success is still not a position.
	_, ok, done = parseTimePosReply([]byte(`{"data":null,"error":"success","request_id":7}`), 7)
	if !done || ok {
		t.Errorf("null data must be rejected (ok=%v done=%v)", ok, done)
	}

	// Garbage is skipped, not fatal.
	_, ok, done = parseTimePosReply([]byte(`not json`), 7)
	if done {
		t.Error("garbage should not be treated as a completed reply")
	}
}

func TestSyncPlayerSeconds(t *testing.T) {
	s := &SyncPlayer{fps: 10, frames: 50}
	if got := s.Seconds(); got != 5.0 {
		t.Errorf("Seconds = %v, want 5", got)
	}
	zero := &SyncPlayer{}
	if got := zero.Seconds(); got != 0 {
		t.Errorf("zero-fps Seconds = %v, want 0", got)
	}
}

func TestAbsFloat(t *testing.T) {
	if absFloat(-2.5) != 2.5 || absFloat(2.5) != 2.5 || absFloat(0) != 0 {
		t.Error("absFloat is wrong")
	}
}

func TestSyncReportIgnoresEarlyPositions(t *testing.T) {
	// Before both sides are really running, positions are meaningless and a seek
	// there would jump the track.
	s := &SyncPlayer{fps: 10, frames: 2, ipc: nil}
	rep, corrected := s.checkSync()
	if corrected {
		t.Error("must not correct without an ipc connection")
	}
	if rep.VideoPos != 0.2 {
		t.Errorf("video pos = %v, want 0.2", rep.VideoPos)
	}
}

func TestDriftThresholdIsSane(t *testing.T) {
	// Too small and ordinary jitter causes constant audible seeking; too large
	// and the desync becomes obvious.
	if driftCorrectThreshold < 0.1 || driftCorrectThreshold > 0.5 {
		t.Errorf("driftCorrectThreshold = %v, want between 0.1 and 0.5 seconds", driftCorrectThreshold)
	}
}

func TestTermSizeOnNonTTY(t *testing.T) {
	// Reading a non-TTY must return an error, not a fake size: the callers fall
	// back to defaults on error.
	f, err := os.CreateTemp("", "notatty")
	if err != nil {
		t.Skipf("temp file: %v", err)
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if _, _, err := termSize(f); err == nil {
		t.Error("termSize on a regular file should error")
	}
}

func TestParseArgs(t *testing.T) {
	o, err := parseArgs([]string{"-m", "lofi", "hip", "hop"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !o.mute {
		t.Error("-m not set")
	}
	if o.query != "lofi hip hop" {
		t.Errorf("query = %q", o.query)
	}
	if _, err := parseArgs([]string{"-x", "q"}); err == nil {
		t.Error("want error on unknown flag")
	}
	if _, err := parseArgs([]string{"-h"}); err == nil {
		t.Error("-h should signal help")
	}
	o2, _ := parseArgs([]string{"-a", "song"})
	if !o2.ascii || o2.query != "song" {
		t.Errorf("got %+v", o2)
	}
}

func TestRMSLevel(t *testing.T) {
	if got := rmsLevel(nil); got != 0 {
		t.Errorf("empty pcm = %v, want 0", got)
	}
	if got := rmsLevel([]byte{1}); got != 0 {
		t.Errorf("odd byte pcm = %v, want 0", got)
	}
	// silence
	silence := make([]byte, 400)
	if got := rmsLevel(silence); got != 0 {
		t.Errorf("silence = %v, want 0", got)
	}
	// full-scale square-ish signal should map near the top
	loud := make([]byte, 400)
	for i := 0; i < len(loud); i += 2 {
		binary.LittleEndian.PutUint16(loud[i:], uint16(int16(30000)))
	}
	if got := rmsLevel(loud); got < 0.8 {
		t.Errorf("loud signal = %v, want > 0.8", got)
	}
	// a mid-level tone must land strictly between silence and full scale,
	// which is the whole reason for the dB mapping
	mid := make([]byte, 400)
	for i := 0; i < len(mid); i += 2 {
		binary.LittleEndian.PutUint16(mid[i:], uint16(int16(3000)))
	}
	got := rmsLevel(mid)
	if got <= 0 || got >= 1 {
		t.Errorf("mid signal = %v, want strictly between 0 and 1", got)
	}
}

func TestParseDecision(t *testing.T) {
	if a, i := parseDecision("q", 10); a != ActionQuit || i != -1 {
		t.Errorf("q -> (%v,%d), want quit/-1", a, i)
	}
	if a, i := parseDecision("3", 10); a != ActionPlay || i != 3 {
		t.Errorf("3 -> (%v,%d), want play/3", a, i)
	}
	if a, _ := parseDecision("", 10); a != ActionQuit {
		t.Errorf("empty -> %v, want quit", a)
	}
	if a, _ := parseDecision("99", 10); a != ActionQuit {
		t.Errorf("out of range -> %v, want quit", a)
	}
}

func TestTMoveClamps(t *testing.T) {
	tracks := make([]Track, 5)
	tui := NewTUI(nil, nil, "q", tracks)
	tui.move(-1)
	if tui.cursor != 0 {
		t.Errorf("cursor = %d, want 0", tui.cursor)
	}
	tui.move(100)
	if tui.cursor != 4 {
		t.Errorf("cursor = %d, want 4", tui.cursor)
	}
}

func TestLastURL(t *testing.T) {
	// Returns the LAST url, not the first: for a merged video+audio selector
	// yt-dlp can print separate DASH URLs, and taking the first gave us a
	// video-only stream with no audio.
	in := "warning line\nhttps://example.com/a\nhttps://example.com/b\n"
	if got := lastURL(in); got != "https://example.com/b" {
		t.Errorf("got %q, want the last url", got)
	}
	if got := lastURL("no urls here"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
	if got := lastURL("https://only.example.com/x\n"); got != "https://only.example.com/x" {
		t.Errorf("single url = %q", got)
	}
}

// --- HUD (S2) ---------------------------------------------------------------
//
// The clock delegates to formatDuration, which the browse list already uses, so
// a duration reads identically in the list and on the progress bar.

func TestFormatClockUsesTUIShape(t *testing.T) {
	cases := []struct {
		in   float64
		want string
	}{
		{0, "0:00"},
		{83, "1:23"},
		{83.4, "1:23"},
		{3723, "1:02:03"},
		{-5, "0:00"}, // a negative position is meaningless; show zero
	}
	for _, c := range cases {
		if got := formatClock(c.in); got != c.want {
			t.Errorf("formatClock(%v) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestProgressBarFillsProportionally(t *testing.T) {
	cases := []struct {
		pos, dur float64
		width    int
		want     string
	}{
		{0, 100, 10, "──────────"},
		{100, 100, 10, "━━━━━━━━━━"},
		{50, 100, 10, "━━━━━─────"},
		{25, 100, 10, "━━────────"}, // floor, not round
	}
	for _, c := range cases {
		got := progressBar(c.pos, c.dur, c.width, -1)
		if got != c.want {
			t.Errorf("progressBar(%v,%v,%d) = %q, want %q", c.pos, c.dur, c.width, got, c.want)
		}
	}
}

func TestProgressBarClampsOutOfRange(t *testing.T) {
	// A position past the end (drift, or a bad duration) must not overflow the
	// bar: the line has a fixed cell budget and would wrap.
	if got := progressBar(150, 100, 4, -1); got != "━━━━" {
		t.Errorf("pos>dur = %q, want full bar", got)
	}
	if got := progressBar(-5, 100, 4, -1); got != "────" {
		t.Errorf("negative pos = %q, want empty bar", got)
	}
}

func TestProgressBarWidthIsExact(t *testing.T) {
	for _, width := range []int{1, 3, 20, 80} {
		got := progressBar(37, 90, width, -1)
		if n := len([]rune(got)); n != width {
			t.Errorf("progressBar width %d produced %d cells: %q", width, n, got)
		}
	}
}

func TestProgressBarNoDurationDrawsNothing(t *testing.T) {
	// Live streams have no duration. An empty string is what makes the caller
	// fall back to elapsed-only instead of painting a permanently empty bar.
	if got := progressBar(30, 0, 10, -1); got != "" {
		t.Errorf("dur=0 = %q, want empty", got)
	}
	if got := progressBar(30, 90, 0, -1); got != "" {
		t.Errorf("width=0 = %q, want empty", got)
	}
}

func TestHUDRowsAreExactlyWidth(t *testing.T) {
	// Every row must fill the line. A short row leaves the tail of the previous,
	// longer row on screen, because both renderers diff against what they last
	// painted and nothing clears the remainder.
	for _, width := range []int{20, 40, 80, 120} {
		h := hud{title: "Lofi Girl - 1 A.M. Study Session", pos: 83, dur: 3674, volume: 80}
		for i, row := range h.lines(width) {
			if n := len([]rune(row)); n != width {
				t.Errorf("width %d row %d = %d cells: %q", width, i, n, row)
			}
		}
	}
}

func TestHUDShowsBarAndClock(t *testing.T) {
	rows := hud{title: "T", pos: 50, dur: 100}.lines(40)
	if len(rows) != 3 {
		t.Fatalf("got %d rows, want 3", len(rows))
	}
	// 40 - (len("0:50 / 1:40") + 2) = 27 bar cells, half filled, then "  0:50 / 1:40".
	if !strings.Contains(rows[1], "0:50 / 1:40") {
		t.Errorf("row 1 = %q, want the clock", rows[1])
	}
	bar := strings.TrimRight(strings.SplitN(rows[1], "  0", 2)[0], " ")
	if got := len([]rune(bar)); got != 27 {
		t.Errorf("bar = %d cells (%q), want 27", got, bar)
	}
	if strings.Count(bar, barFilled) != 13 { // floor(0.5 * 27)
		t.Errorf("bar filled = %d cells, want 13", strings.Count(bar, barFilled))
	}
}

func TestHUDLiveStreamHasNoBar(t *testing.T) {
	// A live stream has no duration, so there is no progress to show. The row
	// falls back to elapsed only rather than a bar stuck at zero.
	rows := hud{title: "T", pos: 30, dur: 0}.lines(40)
	if strings.ContainsRune(rows[1], '━') || strings.ContainsRune(rows[1], '─') {
		t.Errorf("row 1 = %q, want no bar glyphs for unknown duration", rows[1])
	}
	if !strings.Contains(rows[1], "0:30") {
		t.Errorf("row 1 = %q, want elapsed clock", rows[1])
	}
}

func TestHUDQueueCounterAndPausedHints(t *testing.T) {
	rows := hud{title: "T", pos: 0, dur: 10, queue: 3, total: 12, paused: true}.lines(60)
	if !strings.HasPrefix(rows[0], "03/12") {
		t.Errorf("row 0 = %q, want the 03/12 queue counter", rows[0])
	}
	if !strings.HasPrefix(rows[2], "PAUSED") {
		t.Errorf("row 2 = %q, want pause hints", rows[2])
	}
}

func TestHUDSingleTrackHasNoQueueCounter(t *testing.T) {
	// One track is not a queue. A "1/1" prefix is noise.
	rows := hud{title: "T", pos: 0, dur: 10, queue: 1, total: 1}.lines(60)
	if strings.HasPrefix(rows[0], "01/01") {
		t.Errorf("row 0 = %q, want no queue counter for a single track", rows[0])
	}
}

func TestHUDSurvivesATinyTerminal(t *testing.T) {
	// Below the bar's minimum the clock still has to render. An empty or
	// panicking HUD on a 20-column window is worse than no bar.
	for _, width := range []int{1, 8, 12} {
		rows := hud{title: "Long Title Here", pos: 83, dur: 3674, volume: 100}.lines(width)
		if len(rows) != 3 {
			t.Fatalf("width %d: got %d rows, want 3", width, len(rows))
		}
	}
}

// --- Transport state machine (S1) -------------------------------------------
//
// The player is tested against a fake media backend, so every case here runs
// with no ffmpeg, no mpv and no network. These are transport semantics — the
// rules a user feels — not process plumbing.

type fakeMedia struct {
	pos      float64
	dur      float64
	paused   bool
	volume   int
	seeks    []float64
	closed   bool
	pauseErr error
	chapters []Chapter
}

func (f *fakeMedia) Position() float64 { return f.pos }
func (f *fakeMedia) Duration() float64 { return f.dur }
func (f *fakeMedia) SetPaused(p bool) error {
	f.paused = p
	return f.pauseErr
}
func (f *fakeMedia) Seek(sec float64) error {
	f.seeks = append(f.seeks, sec)
	f.pos = sec
	return nil
}
func (f *fakeMedia) SetVolume(v int) error { f.volume = v; return nil }
func (f *fakeMedia) Close() error          { f.closed = true; return nil }
func (f *fakeMedia) Chapters() []Chapter   { return f.chapters }

func newTestPlayer(m *fakeMedia, queue ...Track) *Player {
	return NewPlayer(queue, m)
}

func TestPlayerSeekClampsToTrack(t *testing.T) {
	m := &fakeMedia{dur: 100}
	p := newTestPlayer(m, Track{ID: "a"})

	p.Tick(90)
	p.Do(CmdSeekFwd) // +10 would be 100 exactly
	if got := p.State().Pos; got != 100 {
		t.Errorf("seek to end = %v, want 100", got)
	}
	p.Do(CmdSeekFwd) // past the end must clamp, not wrap or overflow
	if got := p.State().Pos; got != 100 {
		t.Errorf("seek past end = %v, want 100", got)
	}
	// 100 back to 0 is 10 presses; one more must clamp rather than go negative.
	for i := 0; i < 11; i++ {
		p.Do(CmdSeekBack)
	}
	if got := p.State().Pos; got != 0 {
		t.Errorf("seek before start = %v, want 0", got)
	}
	if len(m.seeks) == 0 {
		t.Error("media was never told to seek")
	}
}

func TestPlayerPauseSurvivesSeek(t *testing.T) {
	// The bug this pins: a seek rebuilds ffmpeg, and a naive implementation
	// leaves the player thinking it is playing while nothing is moving. Pause
	// must be a state, not a side effect of a signal.
	m := &fakeMedia{dur: 100}
	p := newTestPlayer(m, Track{ID: "a"})

	p.Do(CmdTogglePause)
	p.Do(CmdSeekFwd)
	if !p.State().Paused {
		t.Error("seek while paused left the player playing")
	}
	p.Do(CmdSeekFwd)
	if !p.State().Paused {
		t.Error("second seek while paused left the player playing")
	}
	p.Do(CmdTogglePause)
	if p.State().Paused {
		t.Error("toggle did not resume")
	}
}

func TestPlayerPauseReachesMedia(t *testing.T) {
	m := &fakeMedia{dur: 100}
	p := newTestPlayer(m, Track{ID: "a"})
	p.Do(CmdTogglePause)
	if !m.paused {
		t.Error("media was not paused")
	}
	p.Do(CmdTogglePause)
	if m.paused {
		t.Error("media was not resumed")
	}
}

func TestPlayerVolumeClamps(t *testing.T) {
	m := &fakeMedia{}
	p := newTestPlayer(m, Track{ID: "a"})
	p.Do(CmdVolUp) // from 100 default
	p.Do(CmdVolUp)
	if got := p.State().Volume; got != 100 {
		t.Errorf("vol up at max = %d, want 100", got)
	}
	for i := 0; i < 40; i++ {
		p.Do(CmdVolDown)
	}
	if got := p.State().Volume; got != 0 {
		t.Errorf("vol down past min = %d, want 0", got)
	}
	if m.volume != 0 {
		t.Errorf("media volume = %d, want 0", m.volume)
	}
}

func TestPlayerNextAdvancesQueue(t *testing.T) {
	m := &fakeMedia{}
	q := []Track{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	p := newTestPlayer(m, q...)

	p.Do(CmdNext)
	if got := p.State().Index; got != 1 {
		t.Errorf("index = %d, want 1", got)
	}
	if got := p.State().Outcome; got != OutcomeNext {
		t.Errorf("outcome = %v, want OutcomeNext", got)
	}
}

func TestPlayerNextOnLastTrackEnds(t *testing.T) {
	// There is nothing after the last result. Stopping is the honest answer;
	// wrapping or silently doing nothing both read as a broken key.
	m := &fakeMedia{}
	q := []Track{{ID: "a"}, {ID: "b"}}
	p := newTestPlayer(m, q...)
	p.Do(CmdNext) // -> index 1, the last track
	p.Do(CmdNext)
	if got := p.State().Outcome; got != OutcomeEnded {
		t.Errorf("outcome = %v, want OutcomeEnded", got)
	}
}

func TestPlayerPrevStepsBackAndRestartsAtStart(t *testing.T) {
	m := &fakeMedia{dur: 100}
	q := []Track{{ID: "a"}, {ID: "b"}}
	p := newTestPlayer(m, q...)

	p.Do(CmdNext) // index 1
	p.Do(CmdPrev)
	if got := p.State().Index; got != 0 {
		t.Errorf("prev index = %d, want 0", got)
	}
	// Already on the first track: prev restarts it rather than doing nothing,
	// which is what every other player does.
	p.Tick(42)
	p.Do(CmdPrev)
	if got := p.State().Index; got != 0 {
		t.Errorf("prev at first track moved to index %d, want 0", got)
	}
	if got := p.State().Pos; got != 0 {
		t.Errorf("prev at first track = %v, want a restart to 0", got)
	}
}

func TestPlayerQuitStops(t *testing.T) {
	p := newTestPlayer(&fakeMedia{}, Track{ID: "a"})
	p.Do(CmdQuit)
	if got := p.State().Outcome; got != OutcomeQuit {
		t.Errorf("outcome = %v, want OutcomeQuit", got)
	}
}

func TestPlayerExposesDurationForHUD(t *testing.T) {
	// The progress bar needs the duration, and the media backend is the only
	// thing that knows it.
	p := newTestPlayer(&fakeMedia{dur: 3674}, Track{ID: "a"})
	if got := p.State().Dur; got != 3674 {
		t.Errorf("dur = %v, want 3674", got)
	}
}

// Seconds must include the resume offset. A player rebuilt at -ss 300 reports
// media time 300, not 0 — and getting this wrong made every terminal resize
// rewind the audio to the start, because checkSync read a 300s "drift" and
// seeked mpv back onto the (wrongly believed) video position.
func TestSyncPlayerSecondsIncludesResumeOffset(t *testing.T) {
	resumed := &SyncPlayer{frames: 30, fps: 10, startAt: 300}
	if got := resumed.Seconds(); got != 303 {
		t.Errorf("resumed Seconds() = %v, want 303", got)
	}
	fresh := &SyncPlayer{frames: 30, fps: 10}
	if got := fresh.Seconds(); got != 3 {
		t.Errorf("fresh Seconds() = %v, want 3", got)
	}
}

func TestPlayerTickFollowsMediaClock(t *testing.T) {
	// The media clock is authoritative: position is reported, not predicted.
	p := newTestPlayer(&fakeMedia{dur: 100}, Track{ID: "a"})
	p.Tick(42)
	if got := p.State().Pos; got != 42 {
		t.Errorf("pos = %v, want 42", got)
	}
}

func TestPlayerTickIsNotConfusedBySeek(t *testing.T) {
	// Frames decoded before a seek cannot arrive after it, because a seek
	// replaces the decoder and its channel outright rather than sharing them.
	// The player therefore takes the clock at face value — and the real
	// guarantee is that SyncPlayer.Seconds() counts from the seek target.
	p := newTestPlayer(&fakeMedia{dur: 100}, Track{ID: "a"})
	p.Do(CmdSeekFwd) // seek to 10 from 0
	p.Tick(10 + 3)   // first tick after the rebuilt decoder starts
	if got := p.State().Pos; got != 13 {
		t.Errorf("pos = %v, want 13", got)
	}
}

// --- mpv IPC properties (S4) -----------------------------------------------

func TestParsePropReplyAcceptsSuccess(t *testing.T) {
	// Same trap as parseTimePosReply: mpv sets "error":"success" on a GOOD
	// reply, so rejecting any non-empty error field rejects every working call.
	err, ok := parsePropReply([]byte(`{"data":null,"error":"success","request_id":7}`), 7)
	if !ok {
		t.Fatal("success reply was not recognised as ours")
	}
	if err != nil {
		t.Errorf("success reply rejected: %v", err)
	}
}

func TestParsePropReplyReportsFailure(t *testing.T) {
	err, ok := parsePropReply([]byte(`{"error":"property not found","request_id":7}`), 7)
	if !ok {
		t.Fatal("failure was not recognised as our reply")
	}
	if err == nil {
		t.Fatal("failing reply accepted")
	}
	if !strings.Contains(err.Error(), "property not found") {
		t.Errorf("error = %v, want mpv's reason", err)
	}
}

func TestParsePropReplyIgnoresOtherIDs(t *testing.T) {
	// Events and replies to earlier requests share the socket; only ours counts.
	if _, ok := parsePropReply([]byte(`{"error":"success","request_id":8}`), 7); ok {
		t.Error("reply for another request treated as ours")
	}
	if _, ok := parsePropReply([]byte(`{"event":"playback-restart"}`), 7); ok {
		t.Error("event treated as a reply")
	}
}

func TestPropertyRequestEncodesNameAndValue(t *testing.T) {
	// Pause and volume are one property each. A malformed command here fails
	// silently at runtime, so the encoding is worth pinning.
	b, err := marshalCommand("set_property", []interface{}{"volume", 40}, 3)
	if err != nil {
		t.Fatalf("marshalCommand: %v", err)
	}
	// Trailing newline included: the newline is the frame delimiter, so a caller
	// that forgot it would leave mpv waiting for the rest of the line.
	want := "{\"command\":[\"set_property\",\"volume\",40],\"request_id\":3}\n"
	if string(b) != want {
		t.Errorf("request = %s, want %s", b, want)
	}
}

func TestVolumeCommandClampsBeforeSending(t *testing.T) {
	// mpv accepts 0-100 for volume. Sending 120 is rejected at the socket with an
	// opaque error, so the clamp belongs on our side of it.
	if got := clampVolume(140); got != 100 {
		t.Errorf("clampVolume(140) = %d, want 100", got)
	}
	if got := clampVolume(-4); got != 0 {
		t.Errorf("clampVolume(-4) = %d, want 0", got)
	}
	if got := clampVolume(65); got != 65 {
		t.Errorf("clampVolume(65) = %d, want 65", got)
	}
}

// --- Key decoding -----------------------------------------------------------
//
// Pure function so escape sequences can be tested without a terminal. Arrows
// arrive as ESC [ <final>, which is the only multi-byte sequence handled.

func TestDecodeKeysMapsTransportKeys(t *testing.T) {
	cases := []struct {
		in   string
		want []Cmd
	}{
		{" ", []Cmd{CmdTogglePause}},
		{"q", []Cmd{CmdQuit}},
		{"Q", []Cmd{CmdQuit}},
		{"\x03", []Cmd{CmdQuit}}, // ctrl-c, still delivered as a byte in raw mode
		{"n", []Cmd{CmdNext}},
		{"p", []Cmd{CmdPrev}},
		{"+", []Cmd{CmdVolUp}},
		{"=", []Cmd{CmdVolUp}}, // the unshifted key on most layouts
		{"-", []Cmd{CmdVolDown}},
		{"_", []Cmd{CmdVolDown}},
	}
	for _, c := range cases {
		got := decodeKeys([]byte(c.in))
		if len(got) != len(c.want) || (len(got) == 1 && got[0] != c.want[0]) {
			t.Errorf("decodeKeys(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDecodeKeysArrows(t *testing.T) {
	cases := []struct {
		in   string
		want Cmd
	}{
		{"\x1b[C", CmdSeekFwd},  // right
		{"\x1b[D", CmdSeekBack}, // left
		{"\x1b[D", CmdSeekBack},
		{"\x1b[A", CmdNone}, // up: not a transport key
		{"\x1b[B", CmdNone}, // down
	}
	for _, c := range cases {
		got := decodeKeys([]byte(c.in))
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("decodeKeys(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestDecodeKeysHandlesBursts(t *testing.T) {
	// Held arrow key: the terminal sends the sequence repeatedly, and several
	// can land in one read. All of them must be queued, not just the first.
	got := decodeKeys([]byte("\x1b[C\x1b[C\x1b[C"))
	if len(got) != 3 {
		t.Fatalf("burst produced %v, want 3 seeks", got)
	}
	for i, c := range got {
		if c != CmdSeekFwd {
			t.Errorf("cmd %d = %v, want CmdSeekFwd", i, c)
		}
	}
}

func TestDecodeKeysIgnoresUnknownBytes(t *testing.T) {
	// A mouse report, a bracketed-paste marker, a function key: all arrive as
	// bytes we do not handle. They must be swallowed, not turned into seeks.
	for _, in := range []string{"\x1b[200~", "\x1bOP", "z", "\x00", "\x1b"} {
		for _, c := range decodeKeys([]byte(in)) {
			if c != CmdNone {
				t.Errorf("decodeKeys(%q) produced %v, want CmdNone only", in, c)
			}
		}
	}
}

func TestDecodeStreamKeepsIncompleteSequences(t *testing.T) {
	// The bug this pins: a terminal can split ESC [ C across reads. Decoding each
	// read independently threw the arrow away, so seek did nothing and it looked
	// like keys were not being delivered at all.
	cmds, used := decodeStream([]byte("\x1b"))
	if len(cmds) != 0 || used != 0 {
		t.Errorf("lone ESC consumed %d bytes -> %v, want it held", used, cmds)
	}
	cmds, used = decodeStream([]byte("\x1b["))
	if len(cmds) != 0 || used != 0 {
		t.Errorf("ESC [ consumed %d bytes -> %v, want it held", used, cmds)
	}
	// The rest arrives: now the sequence completes.
	cmds, used = decodeStream([]byte("\x1b[C"))
	if len(cmds) != 1 || cmds[0] != CmdSeekFwd {
		t.Errorf("completed split = %v, want CmdSeekFwd", cmds)
	}
	if used != 3 {
		t.Errorf("consumed %d bytes, want 3", used)
	}
}

func TestDecodeStreamHandlesPartialThenMore(t *testing.T) {
	// A real burst: keys before the sequence, the sequence itself, keys after.
	pending := []byte("n\x1b[D ")
	cmds, used := decodeStream(pending)
	want := []Cmd{CmdNext, CmdSeekBack, CmdTogglePause}
	if len(cmds) != len(want) {
		t.Fatalf("got %v, want %v", cmds, want)
	}
	for i := range want {
		if cmds[i] != want[i] {
			t.Errorf("cmd %d = %v, want %v", i, cmds[i], want[i])
		}
	}
	if used != len(pending) {
		t.Errorf("consumed %d of %d bytes", used, len(pending))
	}
}

func TestDecodeStreamDropsEscapeNotFollowedByBracket(t *testing.T) {
	// ESC then a normal key: the ESC is not a sequence, so it is discarded and
	// the key after it still works. Holding it would swallow the key.
	cmds, used := decodeStream([]byte("\x1bq"))
	if len(cmds) != 1 || cmds[0] != CmdQuit {
		t.Errorf("got %v, want CmdQuit", cmds)
	}
	if used != 2 {
		t.Errorf("consumed %d bytes, want 2", used)
	}
}

func TestPlayerRecordsTransportFailures(t *testing.T) {
	// A keypress the user just made must not fail silently: a seek that did not
	// happen or a volume change mpv rejected both read as a broken program.
	m := &fakeMedia{dur: 100, pauseErr: fmt.Errorf("mpv: property not found")}
	p := newTestPlayer(m, Track{ID: "a"})
	p.Do(CmdTogglePause)
	err := p.State().LastErr
	if err == nil {
		t.Fatal("failing pause was swallowed")
	}
	if !strings.Contains(err.Error(), "property not found") {
		t.Errorf("error = %v, want mpv's reason", err)
	}
	p.ClearErr()
	if p.State().LastErr != nil {
		t.Error("ClearErr did not clear the error")
	}
}

func TestHUDRowWidthHoldsWithUnknownDuration(t *testing.T) {
	// The bar row skips fit(), so its width has to come out of the arithmetic.
	// Checked for the unknown-duration case (no bar at all) and for an hour-plus
	// clock, where the timestamp is two characters wider than at the start.
	for _, width := range []int{20, 40, 80} {
		for _, dur := range []float64{0, 12, 3674, 36000, 45296} {
			rows := hud{title: "Title", pos: 3, dur: dur, showHints: true}.lines(width)
			for i, row := range rows {
				if n := len([]rune(row)); n != width {
					t.Errorf("width %d dur %v: row %d = %d cells: %q",
						width, dur, i, n, row)
				}
			}
		}
	}
}

func TestHUDFooterShowsMutedNotVolume(t *testing.T) {
	// Under -m the volume is not in effect, so printing "vol 100%" is a lie.
	rows := hud{title: "T", pos: 0, dur: 10, volume: 100, muted: true, showHints: true}.lines(60)
	if !strings.Contains(rows[2], "muted") {
		t.Errorf("row 2 = %q, want the muted label", rows[2])
	}
	if strings.Contains(rows[2], "100%") {
		t.Errorf("row 2 = %q, must not claim a volume while muted", rows[2])
	}
}

func TestHUDFooterShowsVolumeAfterChange(t *testing.T) {
	rows := hud{title: "T", pos: 0, dur: 10, volume: 40, showHints: true}.lines(60)
	if !strings.Contains(rows[2], "40%") {
		t.Errorf("row 2 = %q, want the volume", rows[2])
	}
}

func TestHUDFooterFadesHints(t *testing.T) {
	// A permanent hint row is furniture the eye learns to skip.
	faded := hud{title: "T", pos: 0, dur: 10, volume: 100, showHints: false}.lines(60)
	if strings.TrimSpace(faded[2]) != "" {
		t.Errorf("row 2 = %q, want it empty once hints have faded", faded[2])
	}
	// Paused is exempt: a banner that vanishes on its own is worse than one that
	// stays, because it is the only thing telling you playback is stopped.
	paused := hud{title: "T", pos: 0, dur: 10, paused: true, showHints: false}.lines(60)
	if !strings.HasPrefix(paused[2], "PAUSED") {
		t.Errorf("row 2 = %q, want the pause banner to persist", paused[2])
	}
}

func TestHUDTitleShowsChannel(t *testing.T) {
	rows := hud{title: "Song", channel: "Some Channel", pos: 0, dur: 10,
		queue: 1, total: 10}.lines(60)
	if !strings.HasPrefix(rows[0], "01/10  Song — Some Channel") {
		t.Errorf("row 0 = %q, want title and channel", rows[0])
	}
}

// --- black cells and the colour diff ----------------------------------------
//
// A pixel that is true black must still be painted. Skipping it would leave
// whatever the terminal had in that cell showing through, which reads as "black
// pixels leak" — so these pin that the diff treats black as a real colour.

func TestBlackCellsArePaintedOnFirstFrame(t *testing.T) {
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, 4, 2, ColorTrue, GlyphCell)
	if err := r.Draw(solidRGB(4, 2, 0, 0, 0)); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "48;2;0;0;0") {
		t.Errorf("no black background emitted; output=%q", out)
	}
	if n := strings.Count(out, " "); n < 8 {
		t.Errorf("emitted %d glyphs for 8 cells: %q", n, out)
	}
}

func TestCellTurningBlackIsRepainted(t *testing.T) {
	// Four grey cells, then the middle two go black. The two changed cells must
	// be painted; the two unchanged ones must not be.
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, 4, 1, ColorTrue, GlyphCell)
	if err := r.Draw(solidRGB(4, 1, 200, 200, 200)); err != nil {
		t.Fatalf("Draw grey: %v", err)
	}
	buf.Reset()

	f := solidRGB(4, 1, 200, 200, 200)
	for x := 1; x < 3; x++ {
		f[x*3], f[x*3+1], f[x*3+2] = 0, 0, 0
	}
	if err := r.Draw(f); err != nil {
		t.Fatalf("Draw black: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "48;2;0;0;0") {
		t.Errorf("cells that turned black were not painted; output=%q", out)
	}
	if strings.Contains(out, "\x1b[1;1H") {
		t.Errorf("unchanged cell was repainted; output=%q", out)
	}
	// The changed pair is contiguous, so it is one cursor move and two glyphs.
	if !strings.Contains(out, "\x1b[1;2H") {
		t.Errorf("changed cells not positioned; output=%q", out)
	}
	if n := strings.Count(out, " "); n != 2 {
		t.Errorf("emitted %d glyphs, want exactly the 2 changed cells: %q", n, out)
	}
}

func TestBlackTopPixelInHalfModeIsPainted(t *testing.T) {
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, 2, 1, ColorTrue, GlyphHalf)
	f := make([]byte, 2*1*2*3)
	for i := range f {
		f[i] = 180
	}
	if err := r.Draw(f); err != nil {
		t.Fatalf("Draw grey: %v", err)
	}
	buf.Reset()
	for x := 0; x < 2; x++ { // top row black, bottom stays grey
		f[x*3], f[x*3+1], f[x*3+2] = 0, 0, 0
	}
	if err := r.Draw(f); err != nil {
		t.Fatalf("Draw black top: %v", err)
	}
	out := buf.String()
	if !strings.Contains(out, "38;2;0;0;0") {
		t.Errorf("black top pixel not emitted as foreground; output=%q", out)
	}
	if !strings.Contains(out, "▀") {
		t.Errorf("half-block glyph missing; output=%q", out)
	}
}

func TestQuant256KeepsBlacksOnTheNeutralRamp(t *testing.T) {
	// True black maps to 232 (#080808), not the cube's 16 (#000000). That looks
	// wrong in isolation and is deliberate: keeping every neutral on the same
	// ramp is what stops greys picking up a colour cast on some palettes, and
	// TestQuant256GreyRamp pins the whole ramp. Not to be "fixed" to 16 without
	// measuring the cast it was traded against.
	if got := quant256(0, 0, 0); got != 232 {
		t.Errorf("quant256(0,0,0) = %d, want 232 (the ramp's black)", got)
	}
	if got := quant256(255, 255, 255); got != 255 {
		t.Errorf("quant256(255,255,255) = %d, want 255", got)
	}
}

// solidRGB builds a cols*rows rgb24 frame (one pixel per cell) of one colour.
func solidRGB(cols, rows int, r, g, b byte) []byte {
	f := make([]byte, cols*rows*3)
	for i := 0; i < cols*rows; i++ {
		f[i*3], f[i*3+1], f[i*3+2] = r, g, b
	}
	return f
}

// The renderer's offset arithmetic, checked by replaying its output the way a
// terminal does: maintain the current colours, apply an SGR when one appears, and
// otherwise carry the previous colour forward. A cell whose SGR is skipped is not
// a bug — skipping it is the whole point of the bandwidth optimisation — so the
// check has to model that rather than counting sequences.
//
// ffmpeg is told scale=cols:rows*2, so the frame is INTERLEAVED: per cell row, a
// full row of top pixels then a full row of bottom pixels. Every pixel encodes
// its own (cell row, column) so a mis-mapped offset cannot go unnoticed.

var sgrRe = regexp.MustCompile(`\x1b\[38;2;(\d+);(\d+);(\d+);48;2;(\d+);(\d+);(\d+)m`)

// cell paints from the renderer output: the i-th glyph and the colour in force
// when it was written.
type paint struct {
	fgR, fgG int
	bgR, bgG int
}

// replay walks the stream, tracking cursor moves, SGR changes and glyphs, and
// returns the colour in force for each cell in write order.
func replay(t *testing.T, out string, cells int) []paint {
	t.Helper()
	res := make([]paint, 0, cells)
	i := 0
	var cur paint
	have := false
	for i < len(out) {
		switch {
		case out[i] == 0x1b:
			m := sgrRe.FindStringSubmatchIndex(out[i:])
			if m != nil {
				g := sgrRe.FindStringSubmatch(out[i:])
				cur.fgR, _ = strconv.Atoi(g[1])
				cur.fgG, _ = strconv.Atoi(g[2])
				cur.bgR, _ = strconv.Atoi(g[4])
				cur.bgG, _ = strconv.Atoi(g[5])
				have = true
				i += m[1]
				continue
			}
			// Any other sequence (cursor move, reset) — skip it.
			j := i + 1
			for j < len(out) && !(out[j] >= 0x40 && out[j] <= 0x7e) {
				j++
			}
			if j < len(out) {
				if out[j] == 'm' {
					cur = paint{}
				}
				i = j + 1
				continue
			}
			i = len(out)
		case out[i] == '\r' || out[i] == '\n':
			i++
		case out[i] >= ' ':
			if have {
				res = append(res, cur)
			}
			// Advance by the whole rune: U+2580 is three bytes and counting bytes
			// would report three glyphs per cell.
			_, size := utf8.DecodeRuneInString(out[i:])
			i += size
		default:
			i++
		}
	}
	return res
}

func coordFrameHalf(cols, rows int) []byte {
	f := make([]byte, cols*rows*2*3)
	set := func(px int, r, g byte) {
		f[px*3], f[px*3+1], f[px*3+2] = r, g, 0
	}
	// Interleaved, because that is what `scale=cols:rows*2` produces: frame row
	// 2y is the top of cell row y, row 2y+1 is its bottom. Encoding it planar
	// instead (both halves adjacent) makes consecutive cell rows overwrite each
	// other, which looks like an off-by-N in the renderer when the test is wrong.
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			set((2*y)*cols+x, byte(10+y), byte(x))
			set((2*y+1)*cols+x, byte(200+y), byte(x))
		}
	}
	return f
}

func TestHalfBlockMapsTopAndBottomRowsCorrectly(t *testing.T) {
	const cols, rows = 6, 4
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, ColorTrue, GlyphHalf)
	if err := r.Draw(coordFrameHalf(cols, rows)); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	got := replay(t, buf.String(), cols*rows)
	if len(got) != cols*rows {
		t.Fatalf("got %d painted cells, want %d", len(got), cols*rows)
	}
	for i, p := range got {
		y, x := i/cols, i%cols
		if p.fgR != 10+y || p.fgG != x {
			t.Errorf("cell (x=%d,y=%d) fg = %d,%d want %d,%d",
				x, y, p.fgR, p.fgG, 10+y, x)
		}
		if p.bgR != 200+y || p.bgG != x {
			t.Errorf("cell (x=%d,y=%d) bg = %d,%d want %d,%d",
				x, y, p.bgR, p.bgG, 200+y, x)
		}
	}
}

func TestCellModeMapsRowsCorrectly(t *testing.T) {
	const cols, rows = 6, 4
	var buf bytes.Buffer
	r := NewColorDiffRenderer(&buf, cols, rows, ColorTrue, GlyphCell)
	f := make([]byte, cols*rows*3)
	for y := 0; y < rows; y++ {
		for x := 0; x < cols; x++ {
			i := y*cols + x
			f[i*3], f[i*3+1], f[i*3+2] = byte(10+y), byte(x), 0
		}
	}
	if err := r.Draw(f); err != nil {
		t.Fatalf("Draw: %v", err)
	}
	got := replay(t, buf.String(), cols*rows)
	if len(got) != cols*rows {
		t.Fatalf("got %d painted cells, want %d", len(got), cols*rows)
	}
	for i, p := range got {
		y, x := i/cols, i%cols
		if p.fgR != 10+y || p.fgG != x {
			t.Errorf("cell (x=%d,y=%d) = %d,%d want %d,%d",
				x, y, p.fgR, p.fgG, 10+y, x)
		}
	}
}
