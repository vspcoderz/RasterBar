package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"math"
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
//
// chromeRows is how many rows below the grid the HUD will use. It is a parameter
// rather than a constant because the spectrum strip adds one, and the strip has to
// come out of the *grid* rather than being drawn over it -- see the strip note in
// PLAN-phase7.md. Overlaying the bottom rows of the video would mean fighting the
// diff cache for those cells and forcing a full repaint every frame, which throws
// away the reason the diff renderer exists.
func computeLayout(termCols, termRows int, aspect float64, quality Quality, chromeRows int) layout {
	if termCols <= 0 || termRows <= 0 {
		termCols, termRows = defaultCols, defaultRows
	}
	if aspect <= 0 {
		aspect = defaultAspect
	}
	if chromeRows < 3 {
		chromeRows = 3
	}
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
	l.sourceH = sourceHForCols(cols)

	if quality > 0 {
		// -q is a hard override in both directions. Treat it as the number the
		// user asked for, not a hint: someone passing -q 240 on a big terminal
		// is deliberately asking for a cheap stream, and silently upgrading
		// them to 1080p would spend the CPU they were trying to save.
		l.sourceH = int(quality)
	}
	return l
}

// sourceHForCols is the automatic source height for a grid this wide.
//
// Factored out of computeLayout because the split view asks the same question of
// a pane that is only half the terminal, and a second copy of this switch is
// exactly the kind of thing that ends up tuned in one place only.
func sourceHForCols(cols int) int {
	pixels := float64(cols) * 16.0 / 4.0
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

// fpsForGrid picks a frame rate from the grid size.
//
// The constraint is cells per second, not frames per second: a 200x57 grid is
// 11400 cells against 80x21's 1680, nearly 7x the terminal traffic for the same
// frame rate. Budgeting output keeps big grids smooth instead of falling behind:
// a 400x200 grid at 6fps is 480k cells/sec, far more than a terminal can absorb,
// so the rate has to keep dropping as the grid grows.
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

// Quality lets the user cap or raise source resolution.
type Quality int

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
func resolveMedia(track Track, sourceH int, wantTap bool) (mediaPair, error) {
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
			// The strip's tap reads the same path. A filesystem path is not a
			// signed grant, so it can be opened as many times as there are
			// readers; no second probe and no second URL are needed.
			tapURL:   track.LocalPath,
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
	pair := mediaPair{
		videoURL: videoURL,
		audioURL: audioURL,
		dur:      info.Duration,
		chapters: chaptersFrom(info.Chapters),
	}
	if wantTap {
		// A third grant, minted by its own `yt-dlp -J`.
		//
		// The strip under a video needs its own audio, and audioURL already
		// belongs to mpv. Reusing it is precisely the single-use bug above: one
		// consumer would open the URL and the other would get 403, intermittently,
		// because the grant is a cache and scheduling decides who wins.
		//
		// Only paid when the strip is actually wanted. It is a whole extra yt-dlp
		// round trip, and the strip is off by default in video mode because it
		// costs a row of picture.
		tapURL, _, _, err := audioURLOnce(track)
		if err != nil {
			return mediaPair{}, err
		}
		pair.tapURL = tapURL
	}
	return pair, nil
}

// resolveAudioPair resolves music mode's streams into a mediaPair with no video.
//
// Two `yt-dlp -J` calls, and the second one is not redundant. One call would hand
// out a single URL, which is the bug above; the point of the second call is
// precisely that it mints a different grant. Duration and chapters come from the
// first response only, because they are metadata rather than a consumable --
// reading them again would buy nothing.
//
// A local file needs no resolving and is safe to open twice, because a filesystem
// path is not a signed grant: it can be opened as many times as there are readers.
// So local playback is one probe and two copies of the same path.
func resolveAudioPair(track Track) (mediaPair, error) {
	if track.IsLocal() {
		dur, chaps, hasAudio := probeMedia(track.LocalPath)
		return mediaPair{
			tapURL:   track.LocalPath,
			audioURL: track.LocalPath,
			dur:      float64(dur),
			chapters: chaps,
			silent:   !hasAudio,
		}, nil
	}

	info, err := ytdlpInfo(track)
	if err != nil {
		return mediaPair{}, err
	}
	tapURL, err := pickAudio(info.Formats)
	if err != nil {
		return mediaPair{}, err
	}
	audioURL, _, _, err := audioURLOnce(track)
	if err != nil {
		return mediaPair{}, err
	}
	return mediaPair{
		tapURL:   tapURL,
		audioURL: audioURL,
		// Free: the still is in the response the tap's URL came from, so the
		// split view's thumbnail mode costs no request of its own.
		thumbURL: pickThumbnail(info),
		dur:      info.Duration,
		chapters: chaptersFrom(info.Chapters),
	}, nil
}

// ytdlpInfo is one `yt-dlp -J` call, parsed.
//
// Split out because there were three copies of the exec-plus-unmarshal, and the
// split view needs metadata out of a call it was not previously using for that.
// Three copies of a subprocess invocation is three places to fix a flag.
func ytdlpInfo(track Track) (ytInfo, error) {
	cmd := exec.Command("yt-dlp", "-J", "--no-warnings", "--no-playlist", track.URL)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return ytInfo{}, fmt.Errorf("yt-dlp -J: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	var info ytInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return ytInfo{}, fmt.Errorf("parse yt-dlp -J: %w", err)
	}
	return info, nil
}

// pickThumbnail is the still image the split view's thumbnail mode draws.
//
// The widest entry of the `thumbnails` array, which is the one carrying real
// dimensions. The scalar `thumbnail` field is only a fallback because it is
// frequently the webp variant, and whether ffmpeg can decode webp depends on how
// it was built -- verified working on this machine's build, but the array entries
// are jpegs and are the safer thing to ask for.
//
// An empty result is normal, not a failure: plenty of uploads have no thumbnail,
// and the caller treats that as "no still available".
func pickThumbnail(info ytInfo) string {
	best, bestW := "", 0
	for _, th := range info.Thumbnails {
		if th.URL == "" || th.Width <= bestW {
			continue
		}
		best, bestW = th.URL, th.Width
	}
	if best == "" {
		return info.Thumbnail
	}
	return best
}

// resolvePaneVideoURL is the video stream for the split view's live pane.
//
// Lazy on purpose. Music mode makes two `yt-dlp -J` calls; this would be a third,
// paid on every track for a feature that is off by default. So it happens the
// first time someone presses `W`.
//
// A local file is its own video, and a filesystem path is not a signed grant, so
// any number of readers may open it.
func resolvePaneVideoURL(track Track, sourceH int) (string, error) {
	if track.IsLocal() {
		return track.LocalPath, nil
	}
	info, err := ytdlpInfo(track)
	if err != nil {
		return "", err
	}
	videoURL, _, err := pickStreams(info.Formats, sourceH)
	if err != nil {
		return "", err
	}
	return videoURL, nil
}

// audioURLOnce is one `yt-dlp -J` call reduced to an audio URL plus the metadata
// that rides along in the same response.
func audioURLOnce(track Track) (url string, dur float64, chaps []Chapter, err error) {
	info, err := ytdlpInfo(track)
	if err != nil {
		return "", 0, nil, err
	}
	url, err = pickAudio(info.Formats)
	if err != nil {
		return "", 0, nil, err
	}
	return url, info.Duration, chaptersFrom(info.Chapters), nil
}

// pickAudio chooses the cheapest audio stream, preferring one that carries no
// video at all.
//
// Audio-only first, and the reason is decode cost rather than tidiness: the level
// tap's ffmpeg is given `-vn`, so it never *outputs* video, but ffmpeg still
// demuxes and decodes what it is handed before discarding it. Pointed at a
// progressive format that means decoding a 360p h264 video purely to throw it
// away, on every track, on the machine that can least afford it. An audio-only
// DASH format has no video track in the container to decode at all.
//
// Progressive is the fallback rather than never, because it is all some uploads
// have.
func pickAudio(formats []ytFormat) (string, error) {
	var bestAudio, progressive ytFormat
	for _, f := range formats {
		if f.Protocol == "m3u8" || f.URL == "" {
			continue
		}
		hasA := !isNone(f.ACodec)
		hasV := !isNone(f.VCodec)
		switch {
		case hasA && !hasV:
			if betterAudio(f, bestAudio) {
				bestAudio = f
			}
		case hasA && hasV:
			if progressive.URL == "" || f.TBR > progressive.TBR {
				progressive = f
			}
		}
	}
	if bestAudio.URL != "" {
		return bestAudio.URL, nil
	}
	if progressive.URL != "" {
		return progressive.URL, nil
	}
	return "", fmt.Errorf("no usable audio format")
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
	// Thumbnail is yt-dlp's scalar "best guess"; Thumbnails is the full list with
	// real dimensions. The split view's still mode wants one, and it is already
	// in a response music mode makes. See pickThumbnail.
	Thumbnail  string `json:"thumbnail"`
	Thumbnails []struct {
		URL    string `json:"url"`
		Height int    `json:"height"`
		Width  int    `json:"width"`
	} `json:"thumbnails"`
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
