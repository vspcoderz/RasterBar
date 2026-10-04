package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
)

// ASCII video + visualizer. No image libraries: ffmpeg emits raw grayscale
// bytes on a pipe and we map each byte to a character. That is the whole trick.
//
// Performance note (low-end PCs): the expensive part is ffmpeg decoding source
// frames, not our byte loop. So we cap the source resolution and frame rate at
// the *format selection* level (see resolveVideoURL) instead of upscaling
// cheaply afterwards — decoding 360p is dramatically less work than 1080p.

// ASCII grid sizing.
//
// The shared `ramp` lives in render.go.
// The grid follows the terminal window (TIOCGWINSZ, see raw.go). Terminal cells
// are taller than they are wide -- roughly 2:1 in most default fonts -- so a
// width W grid needs about W/heightRatio rows to avoid a stretched image. That
// ratio is a property of the font, not something we can measure, so it is
// configurable with -aspect.
//
// Defaults are the fallback when stdout is not a TTY (piped output, CI).
const (
	defaultCols   = 80
	defaultRows   = 22
	defaultAspect = 2.0 // cell height / cell width
	minCols       = 20
	// A grid this large is not worth filling: past a point the cell count
	// exceeds what a terminal absorbs per second at any usable frame rate, so
	// more size only adds lag.
	maxCols           = 300
	maxRows           = 120
	sourceCharsPerRes // approximate characters across a 360p frame
)

// layout is the resolved ASCII grid plus the video height worth requesting.
type layout struct {
	cols, rows int
	termRows   int // terminal height it was derived from, for resize checks
	fps        int
	sourceH    int // requested source height in pixels
	aspect     float64
}

// computeLayout derives the character grid from the terminal size.
//
// Video resolution follows the grid: a 40-column display does not benefit from
// 1080p, and asking for it wastes decode CPU on a low-end machine. sourceH is
// clamped to what the grid can actually resolve.
func computeLayout(termCols, termRows int, aspect float64, quality Quality) layout {
	if termCols <= 0 || termRows <= 0 {
		termCols, termRows = defaultCols, defaultRows
	}
	if aspect <= 0 {
		aspect = defaultAspect
	}

	// Leave room for the title bar and the key hints. Without this the frame
	// overwrites the header and the screen tears.
	const chromeRows = 3
	availRows := termRows - chromeRows
	if availRows < 4 {
		availRows = 4
	}

	cols := termCols
	if cols > maxCols {
		cols = maxCols
	}
	if cols < minCols {
		cols = minCols
	}

	// height = width / aspect, clamped to the space we actually have.
	rows := int(float64(cols) / aspect)
	if rows > availRows {
		rows = availRows
	}
	if rows < 4 {
		rows = 4
	}
	if rows > maxRows {
		rows = maxRows
	}

	l := layout{cols: cols, rows: rows, termRows: termRows, aspect: aspect, fps: fpsForGrid(cols, rows)}

	// Match source height to the grid. Roughly 4 characters of width per 16px
	// of source height is a reasonable ratio from real captures; the point is
	// that a small grid does not request a large frame.
	pixels := float64(cols) * 16.0 / 4.0
	switch {
	case pixels < 240:
		l.sourceH = 240
	case pixels < 360:
		l.sourceH = 360
	case pixels < 480:
		l.sourceH = 480
	case pixels < 720:
		l.sourceH = 720
	default:
		l.sourceH = 1080
	}

	if quality > 0 {
		// -q is a hard override in both directions. Treat it as the number the
		// user asked for, not a hint: someone passing -q 240 on a big terminal
		// is deliberately asking for a cheap stream, and silently upgrading
		// them to 1080p would spend the CPU they were trying to save.
		l.sourceH = int(quality)
	}
	return l
}

// fpsForGrid picks a frame rate from the grid size.
//
// The constraint is characters per second, not frames per second: a 200x57 grid
// is 11400 cells against 80x21's 1680, nearly 7x the terminal traffic for the
// same frame rate. Budgeting total output keeps big grids smooth instead of
// falling behind.
// fpsForGrid picks a frame rate from the grid size.
//
// The constraint is cells per second, not frames per second. Budgeting output
// keeps big grids smooth instead of falling behind: a 400x200 grid at 6fps is
// 480k cells/sec, far more than a terminal can absorb, so the rate has to keep
// dropping as the grid grows.
func fpsForGrid(cols, rows int) int {
	cells := cols * rows
	switch {
	case cells <= 2500:
		return videoFps
	case cells <= 6000:
		return 10
	case cells <= 12000:
		return 8
	case cells <= 25000:
		return 6
	case cells <= 50000:
		return 4
	default:
		return 3
	}
}

// sourceHForGrid reports the resolution a grid can actually resolve, for
// callers that want the automatic value.
func sourceHForGrid(cols int) int {
	pixels := float64(cols) * 4.0
	switch {
	case pixels < 240:
		return 240
	case pixels < 360:
		return 360
	case pixels < 480:
		return 480
	case pixels < 720:
		return 720
	default:
		return 1080
	}
}

// Quality lets the user cap or raise source resolution.
type Quality int

// playAudio resolves a direct audio URL via yt-dlp and streams it to mpv.
// The resolve step is mandatory: mpv/ffmpeg cannot open youtube.com URLs
// themselves (verified: ffmpeg returns "Invalid data found when processing
// input").
func playAudio(track Track) error {
	url, err := resolveURL(track, "140/251/bestaudio")
	if err != nil {
		return err
	}
	cmd := exec.Command("mpv", "--no-config", "--no-video", url)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// resolveURL asks yt-dlp for direct media URLs.
func resolveURL(track Track, format string) (string, error) {
	// ffmpeg and mpv both open a filesystem path directly, so a library track
	// resolves to itself. Running yt-dlp here would fail on every seek: it finds
	// no extractor for a path and the format selector is never consulted.
	if track.IsLocal() {
		return track.LocalPath, nil
	}
	out, err := runYtdlp(format, track.URL)
	if err != nil {
		return "", err
	}
	url := lastURL(out)
	if url == "" {
		return "", fmt.Errorf("yt-dlp returned no url for %s (%s)", track.ID, track.Title)
	}
	return url, nil
}

// resolveVideoURL picks the cheapest source that still looks acceptable in
// ASCII. Caps height at 360p and prefers h264 (cheapest decode) over AV1/VP9.
// This is the single biggest low-end win: decode cost scales with resolution
// and codec, and ASCII output at 80x22 cannot show the difference between
// 360p and 1080p.
//
// Video-only on purpose: the DASH video formats carry no audio track, so audio
// is resolved separately by resolveAudioURL. Feeding a video-only URL to
// `mpv --no-video` exits 0 instantly and silently (verified), which looks like
// a successful play but plays nothing.
func resolveVideoURL(track Track) (string, error) {
	format := "bestvideo[height<=360][vcodec^=avc1]/bestvideo[height<=360]/best"
	return resolveURL(track, format)
}

// resolveAudioURL gets a standalone audio stream for playback alongside ASCII.
func resolveAudioURL(track Track) (string, error) {
	return resolveURL(track, "bestaudio")
}

// resolveMedia returns the video and audio URLs for a track, plus its duration.
//
// They are two SEPARATE URLs: `yt-dlp -g` never muxes, no matter what
// --merge-output-format says -- it only simulates the merge (verified: it
// printed one video-only and one audio-only URL).
//
// Selection uses `yt-dlp -J`, which returns every format's codec metadata in the
// same request as the URLs. An earlier version ran ffprobe against each resolved
// URL to work out which was which. That was a real bug: googlevideo URLs are
// effectively single-use, so probing burned the grant and ffmpeg's later
// request came back "403 Forbidden (access denied)" (verified). Reading codec
// info from yt-dlp's own JSON touches the media URL zero times and is faster.
// resolveMedia returns the video and audio URLs plus the track duration, from a
// single yt-dlp call.
//
// Duration rides along in the -J response that already carries every format's
// codec metadata, so the progress bar costs no extra request. Getting it any
// other way would mean ffprobe against a resolved URL, and probing a
// googlevideo URL burns its grant (see below).
func resolveMedia(track Track, sourceH int) (mediaPair, error) {
	h := sourceH
	if h <= 0 {
		h = 360
	}

	// A local file needs no resolving, and yt-dlp cannot resolve one: it is
	// handed a filesystem path, finds no extractor, and exits non-zero. Its
	// container already holds both streams, so this is the same shape
	// pickStreams returns for a progressive format — one ref used twice.
	if track.IsLocal() {
		dur, chaps, hasAudio := probeMedia(track.LocalPath)
		return mediaPair{
			videoURL: track.LocalPath,
			audioURL: track.LocalPath,
			dur:      float64(dur),
			chapters: chaps,
			silent:   !hasAudio,
		}, nil
	}

	cmd := exec.Command("yt-dlp", "-J", "--no-warnings", "--no-playlist", track.URL)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return mediaPair{}, fmt.Errorf("yt-dlp -J: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var info ytInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return mediaPair{}, fmt.Errorf("parse yt-dlp -J: %w", err)
	}
	videoURL, audioURL, err := pickStreams(info.Formats, h)
	if err != nil {
		return mediaPair{}, err
	}
	return mediaPair{
		videoURL: videoURL,
		audioURL: audioURL,
		dur:      info.Duration,
		chapters: chaptersFrom(info.Chapters),
	}, nil
}

// ytInfo is the subset of `yt-dlp -J` we need.
type ytInfo struct {
	Formats []ytFormat `json:"formats"`
	// Duration in seconds, for the progress bar. Absent or zero on a live
	// stream, which is why the HUD falls back to elapsed-only.
	Duration float64 `json:"duration"`
	// Chapters come along in the same response, so reading them costs no extra
	// request. Most uploads have none, which is why an empty list is normal
	// rather than a failure.
	Chapters []ytChapter `json:"chapters"`
}

// ytChapter is one entry of yt-dlp's chapter list.
type ytChapter struct {
	StartTime float64 `json:"start_time"`
	EndTime   float64 `json:"end_time"`
	Title     string  `json:"title"`
}

// chaptersFrom converts the wire shape into the player's, dropping entries with
// no usable start. A chapter that begins at -1 or at NaN is yt-dlp saying
// "unknown", and letting one through would make `]` seek to a position that does
// not exist.
func chaptersFrom(c []ytChapter) []Chapter {
	if len(c) == 0 {
		return nil
	}
	out := make([]Chapter, 0, len(c))
	for _, ch := range c {
		if math.IsNaN(ch.StartTime) || math.IsNaN(ch.EndTime) ||
			ch.StartTime < 0 || ch.EndTime < ch.StartTime {
			continue
		}
		out = append(out, Chapter{Start: ch.StartTime, End: ch.EndTime, Title: ch.Title})
	}
	return out
}

type ytFormat struct {
	FormatID string  `json:"format_id"`
	URL      string  `json:"url"`
	Ext      string  `json:"ext"`
	Height   int     `json:"height"`
	VCodec   string  `json:"vcodec"`
	ACodec   string  `json:"acodec"`
	TBR      float64 `json:"tbr"`
	ABR      float64 `json:"abr"`
	Protocol string  `json:"protocol"`
}

func isNone(s string) bool { return s == "" || s == "none" }

// pickStreams chooses a video URL and an audio URL from the format list,
// honouring the height cap and preferring h264 (cheapest software decode, which
// matters on a low-end machine).
func pickStreams(formats []ytFormat, maxH int) (string, string, error) {
	var bestVideo, bestAudio, progressive ytFormat

	for _, f := range formats {
		hasV := !isNone(f.VCodec)
		hasA := !isNone(f.ACodec)
		// Skip m3u8: ffmpeg would have to re-resolve it and we would lose
		// control over the clock. Skip anything with no direct URL.
		if f.Protocol == "m3u8" || f.URL == "" {
			continue
		}

		if hasV && hasA {
			// Progressive: one file carrying both.
			if progressive.URL == "" || f.Height > progressive.Height {
				progressive = f
			}
			continue
		}
		if hasV {
			if f.Height <= maxH && betterVideo(f, bestVideo) {
				bestVideo = f
			}
		}
		if hasA && betterAudio(f, bestAudio) {
			bestAudio = f
		}
	}

	// Prefer a progressive stream when it fits the cap: one URL and one input.
	if progressive.URL != "" && progressive.Height <= maxH {
		return progressive.URL, progressive.URL, nil
	}
	if bestVideo.URL == "" || bestAudio.URL == "" {
		// A progressive stream above the cap is still better than nothing.
		if progressive.URL != "" {
			return progressive.URL, progressive.URL, nil
		}
		return "", "", fmt.Errorf("no usable video+audio formats under %dp", maxH)
	}
	return bestVideo.URL, bestAudio.URL, nil
}

// betterVideo prefers h264, then higher resolution, then higher bitrate.
// Only "avc1" counts as h264: "av01" begins with "av" but is AV1, the most
// expensive of the three to decode.
func betterVideo(cand, cur ytFormat) bool {
	if cur.URL == "" {
		return true
	}
	ch, curh := isH264(cand.VCodec), isH264(cur.VCodec)
	if ch != curh {
		return ch
	}
	if cand.Height != cur.Height {
		return cand.Height > cur.Height
	}
	return cand.TBR > cur.TBR
}

func isH264(vc string) bool {
	return len(vc) >= 4 && vc[:4] == "avc1"
}

func betterAudio(cand, cur ytFormat) bool {
	if cur.URL == "" {
		return true
	}
	return cand.TBR > cur.TBR
}

func runYtdlp(format, url string) (string, error) {
	cmd := exec.Command("yt-dlp", "-f", format, "-g", url)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("yt-dlp -g: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return string(out), nil
}

// lastURL returns the last http line from `yt-dlp -g`.
//
// "Last", not "first": for a merged `video+audio` selector yt-dlp may print two
// URLs (separate DASH streams) or one muxed URL. Taking the first silently gave
// us a video-only stream, and the ASCII player then failed with "Output file
// does not contain any stream" because there was no audio to map. Verified.
func lastURL(out string) string {
	last := ""
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if strings.HasPrefix(l, "http") {
			last = l
		}
	}
	return last
}

// ASCIIStream decodes frames from ffmpeg and renders them as characters.
type ASCIIStream struct {
	cmd    *exec.Cmd
	stdout io.ReadCloser
	raw    []byte
	cols   int
	rows   int
	buf    *bufio.Writer
	out    io.Writer
}

func NewASCIIStream(mediaURL string, cols, rows, fps int, out io.Writer) (*ASCIIStream, error) {
	if fps <= 0 {
		fps = 10
	}
	// Sample at 2x the character grid because cells are ~2x tall as wide,
	// then pick every other pixel in Go. scale does the heavy lifting in C.
	filter := fmt.Sprintf("fps=%d,scale=%d:%d:flags=fast_bilinear,format=gray", fps, cols*2, rows*2)
	cmd := exec.Command("ffmpeg",
		"-hide_banner", "-loglevel", "error", "-nostdin",
		"-i", mediaURL,
		"-vf", filter,
		"-pix_fmt", "gray",
		"-f", "rawvideo", "-",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}
	return &ASCIIStream{
		cmd:    cmd,
		stdout: stdout,
		raw:    make([]byte, cols*2*rows*2),
		cols:   cols,
		rows:   rows,
		buf:    bufio.NewWriterSize(out, 64*1024),
		out:    out,
	}, nil
}

// Next renders the next frame. Reuses its buffers: no per-frame allocation,
// which keeps the GC idle during playback.
func (s *ASCIIStream) Next() (string, error) {
	if _, err := io.ReadFull(s.stdout, s.raw); err != nil {
		return "", err
	}
	var sb strings.Builder
	sb.Grow(s.cols * (s.rows + 1))
	for y := 0; y < s.rows*2; y += 2 {
		row := y * s.cols * 2
		for x := 0; x < s.cols*2; x += 2 {
			idx := int(s.raw[row+x]) * len(ramp) / 256
			if idx >= len(ramp) {
				idx = len(ramp) - 1
			}
			sb.WriteByte(ramp[idx])
		}
		sb.WriteByte('\n')
	}
	return sb.String(), nil
}

func (s *ASCIIStream) Close() {
	if s.stdout != nil {
		s.stdout.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
}

// newRenderer picks the mono or colour renderer. Both satisfy FrameRenderer, so
// the render loop does not care which is active.
func newRenderer(w io.Writer, cols, rows int, mode ColorMode, glyph GlyphMode) FrameRenderer {
	if mode == ColorNone {
		return NewDiffRenderer(w, cols, rows)
	}
	return newColorRenderer(w, cols, rows, mode, glyph)
}

// renderFrameTo renders a whole frame as lines of characters. Used by tests and
// one-shot previews; live playback uses DiffRenderer, which writes far less.
func renderFrameTo(w io.Writer, raw []byte, cols, rows int) error {
	bw := bufio.NewWriter(w)
	defer bw.Flush()
	line := make([]byte, cols)
	for y := 0; y < rows; y++ {
		off := y * cols
		if off+cols > len(raw) {
			break
		}
		for x := 0; x < cols; x++ {
			line[x] = levelFor(raw[off+x])
		}
		bw.Write(line)
		bw.WriteByte('\n')
	}
	return nil
}
