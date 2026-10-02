package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"
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

// resolveMediaPair returns the video and audio URLs for a track.
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
func resolveMediaPair(track Track, sourceH int) (videoURL, audioURL string, err error) {
	h := sourceH
	if h <= 0 {
		h = 360
	}
	cmd := exec.Command("yt-dlp", "-J", "--no-warnings", "--no-playlist", track.URL)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", "", fmt.Errorf("yt-dlp -J: %w: %s", err, strings.TrimSpace(stderr.String()))
	}

	var info ytInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return "", "", fmt.Errorf("parse yt-dlp -J: %w", err)
	}
	return pickStreams(info.Formats, h)
}

// ytInfo is the subset of `yt-dlp -J` we need.
type ytInfo struct {
	Formats []ytFormat `json:"formats"`
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

// runASCII plays a track as ASCII video with audio in sync.
//
// Sync model: a single ffmpeg process demuxes the track once and emits BOTH the
// grayscale video frames (our stdout pipe) and the audio PCM (a FIFO mpv reads),
// paced with -re. One process means one clock, so the frames cannot drift from
// the music. The previous design ran mpv and ffmpeg independently and came out
// fast-forward and out of sync.
func runASCII(track Track, in *os.File, out *os.File, mute bool, quality Quality, aspect float64, colsOverride, rowsOverride int, mode ColorMode, glyphPref GlyphMode) error {
	// Terminal size drives the grid. If stdout is not a TTY (piped, CI) fall
	// back to the defaults rather than rendering nothing.
	termCols, termRows := 0, 0
	if c, r, err := termSize(out); err == nil {
		termCols, termRows = c, r
	}
	// Explicit overrides win over detection.
	if colsOverride > 0 {
		termCols = colsOverride
	}
	if rowsOverride > 0 {
		termRows = rowsOverride
	}
	restore, err := makeRaw(in)
	if err == nil {
		defer restore()
	}
	fmt.Fprint(out, "\x1b[?25l")
	defer fmt.Fprint(out, "\x1b[?25h\x1b[2J\x1b[H")

	// The grid follows the terminal; video resolution follows the grid.
	l := computeLayout(termCols, termRows, aspect, quality)

	// yt-dlp hands back separate video and audio URLs; both go into one ffmpeg
	// so they cannot drift apart.
	videoURL, audioURL, err := resolveMediaPair(track, l.sourceH)
	if err != nil {
		return fmt.Errorf("resolve media: %w", err)
	}

	// Decide the cell layout before ffmpeg starts, because the frame height
	// depends on it. The probe asks the terminal directly: U+2580 is
	// East Asian Ambiguous width and some terminals draw it two columns wide,
	// which makes every row overrun and wrap (reported as ghosting/double
	// images). Probing costs one round trip and removes the guess.
	glyph := GlyphCell
	if mode != ColorNone {
		glyph = resolveGlyph(glyphPref, in, out)
	}

	player, err := startSyncPlayerAt(videoURL, audioURL, l.cols, l.rows, l.fps, mute, 0, mode, glyph)
	if err != nil {
		return err
	}
	defer player.Close()

	quit := watchQuit(in)
	audioDone := make(chan error, 1)
	go func() { audioDone <- player.WaitAudioEnd() }()

	// Buffered writer: one syscall per frame batch instead of many small ones.
	bw := bufio.NewWriterSize(out, 32*1024)
	renderer := newRenderer(bw, l.cols, l.rows, mode, glyph)

	// Drift correction timing.
	lastSync := time.Now()
	const syncCheckInterval = 2 * time.Second

	// Stall handling: suspend both children rather than rebuilding them.
	//
	// When the terminal is not being read (window covered, Hyprland workspace
	// switch, another app focused) the tty stops accepting output. mpv writes
	// straight to PipeWire and keeps playing regardless, so the streams drift.
	// Stopping ffmpeg and mpv in the same instant freezes both clocks: sync is
	// preserved exactly, nothing is respawned, and -- importantly -- nothing is
	// written to the terminal, which is what was painting ASCII over the desktop.
	restartAt := func(pos float64) error {
		player.Close()
		vURL, aURL, rerr := resolveMediaPair(track, l.sourceH)
		if rerr != nil {
			return rerr
		}
		np, rerr := startSyncPlayerAt(vURL, aURL, l.cols, l.rows, l.fps, mute, pos, mode, glyph)
		if rerr != nil {
			return rerr
		}
		player = np
		audioDone = make(chan error, 1)
		go func(p *SyncPlayer) { audioDone <- p.WaitAudioEnd() }(player)
		renderer = newRenderer(bw, l.cols, l.rows, mode, glyph)
		fmt.Fprint(bw, "\x1b[2J\x1b[H")
		return nil
	}

	// SIGWINCH: a resize must refill the screen, not kill playback. The ffmpeg
	// scale filter is baked into the running process, so a genuine size change
	// means rebuilding the player. Rebuilding is only cheap if we know the
	// current position, which ffmpeg can tell us.
	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)

	runtime.GC() // settle before the steady-state loop
	for {
		select {
		case <-quit:
			return nil
		case err := <-audioDone:
			return err
		case <-resized:
			// Drain: a drag can emit many SIGWINCHs.
			for drained := false; !drained; {
				select {
				case <-resized:
				default:
					drained = true
				}
			}
			c, r, err := termSize(out)
			if err != nil || (c == l.cols && r == l.termRows) {
				continue // spurious signal, keep playing
			}
			pos := player.Seconds()
			// Rebuild at the new size, resuming from where we were. The grid
			// changed, so the ffmpeg scale filter has to change with it.
			l = computeLayout(c, r, aspect, quality)
			if rerr := restartAt(pos); rerr != nil {
				return nil
			}
			continue
		default:
		}
		frame, err := player.Next()
		if err != nil {
			return nil
		}
		if err := renderer.Draw(frame); err != nil {
			return nil
		}
		if err := bw.Flush(); err != nil {
			return nil
		}

		// If the terminal has stopped accepting output, suspend both children
		// and wait for it to come back. Checked after the write so the flush
		// above is what fills the buffer, not the poll.
		if !fdWritable(out) {
			player.pauseChildren()
			if !awaitDrain(out, player, quit, 120*time.Millisecond) {
				player.resumeChildren()
				return nil
			}
			player.resumeChildren()
			// The terminal may have been repainted while we were suspended, and
			// our diff buffer no longer matches what is on screen.
			renderer.ForceNext()
			if err := bw.Flush(); err != nil {
				return nil
			}
			// Time passed while suspended, so re-sync before showing more.
			lastSync = time.Time{}
		}

		// Drift correction. Sampled periodically rather than every frame: an IPC
		// round trip per frame is wasteful, and drift develops slowly.
		if time.Since(lastSync) > syncCheckInterval {
			lastSync = time.Now()
			rep, corrected := player.checkSync()
			if debugSync {
				tag := "ok"
				if corrected {
					tag = "CORRECTED"
				} else if player.ipc == nil {
					tag = "no-ipc"
				} else if e := ipcLastErr(); e != "" {
					tag = "ERR:" + e
				}
				fmt.Fprintf(os.Stderr,
					"\rsync: video %s audio %s drift %+.3fs %s      ",
					formatPos(rep.VideoPos), formatPos(rep.AudioPos), rep.Drift, tag)
			}
		}
	}
}

// newRenderer picks the mono or colour renderer. Both satisfy FrameRenderer so
// runASCII does not care which is active.
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
