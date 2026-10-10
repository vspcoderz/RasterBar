package main

// One playing track: the real media backend, the decoder goroutine, and the
// render loop that drives both plus the HUD.
//
// This replaces the old monolithic runASCII, where SyncPlayer.Next() blocked in
// io.ReadFull and the loop's only exits were a frame arriving, EOF, or 'q' —
// which is why there was nowhere for a keypress to go.

import (
	"bufio"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/vspcoderz/rasterbar/internal/term"
)

// mediaPair is one track's resolved streams and duration.
//
// The pair comes from a single `yt-dlp -J` call. Duration rides along in that
// same response, which is why the progress bar needs no second request and no
// ffprobe — probing a googlevideo URL burns its grant (see resolveMedia).
//
// music mode fills a different set: videoURL stays empty, and tapURL is a
// *different* URL from audioURL so the level tap and mpv do not share a grant.
//
// That duplication is the fix for a real bug, not defensive style.
// `runVisualAudio` resolved one `bestaudio` URL and handed it to both the tap's
// ffmpeg and mpv. A googlevideo URL is effectively single-use, so one of the two
// consumers spent the other's grant and got "403 Forbidden (access denied)". It
// failed intermittently because the grant is a cache: whichever consumer opened
// it first won and the other lost, depending on scheduling.
//
// The rule in AGENTS.md is one input, one consumer, and it is a rule about URLs,
// not about streams. Two separate yt-dlp calls mint two independent grants, so
// each consumer genuinely holds its own. See resolveAudioPair.
type mediaPair struct {
	videoURL string // empty in music mode: there is no video to decode
	audioURL string // mpv
	tapURL   string // the level tap's ffmpeg: music mode, and video mode with the strip on
	// thumbURL is the still image for the split view's thumbnail mode. Free: it
	// rides along in a `yt-dlp -J` response already being made.
	thumbURL string
	dur      float64
	// chapters comes from the same response as the URLs and the duration, so
	// chapter navigation costs no extra request on either path.
	chapters []Chapter
	// silent is true when the source has no audio stream at all.
	//
	// Only ever set on the local path, where the probe already knows. YouTube
	// always has audio, so the zero value is the right default and the network
	// path needs no extra request to learn it.
	//
	// Music mode reads it as fatal rather than falling back to video: a track
	// with no audio has no spectrum, which is the entire thing that mode draws.
	// Showing a silent video there would be worse than refusing it.
	silent bool
	// videoLess is true when the source has no video stream at all -- an mp3, an
	// opus file, a .wav. The mirror of silent, and needed for the same reason:
	// the video tap is handed `-map 0:v:0`, which on a file with no video is a
	// hard ffmpeg error and then EOF on the pipe.
	//
	// A videoLess track plays as a visualiser whatever mode was asked for, because
	// "no picture" is not a mode the user can act on -- there is nothing to switch
	// to. The reverse is not true: silent stays fatal in music mode, since the
	// spectrum is the whole content there.
	//
	// Local only, same as silent. yt-dlp's formats carry codec metadata, so the
	// network path knows without asking and needs no extra request.
	videoLess bool
}

// videoFrame is one decoded frame plus the media position it was decoded at.
//
// Carrying the position matters: decoding moved to its own goroutine, so the
// player's frame counter is written there and read by the render loop. Passing
// the position alongside the frame makes that a single-writer handoff instead of
// a counter two goroutines race on.
type videoFrame struct {
	buf []byte
	pos float64
}

// trackSession is the media backend for the track currently playing.
//
// It satisfies Player's media interface. Everything here crosses a process
// boundary on purpose; the transport rules live in Player and are tested without
// any of it.
type trackSession struct {
	track Track
	pair  mediaPair
	// l is the grid, source resolution and frame rate in force. Held whole
	// because the source resolution travels with the grid: a re-resolve that
	// forgot l.sourceH would quietly downgrade a 1080p session to 360p.
	l layout

	cur    *SyncPlayer
	frames chan videoFrame // per-generation; swapped on rebuild
	audio  chan struct{}   // per-generation; closed when mpv exits
	stop   chan struct{}   // per-generation; closed to retire the decoder
	pos    atomic.Uint64   // float64 bits of the last decoded position

	dirty  bool    // a seek or resize wants a rebuild
	seekTo float64 // where to rebuild
	paused bool    // children are SIGSTOPped

	// Volume requested while paused, applied on resume. See SetVolume.
	pendingVol    int
	hasPendingVol bool

	mute  bool
	mode  ColorMode
	glyph GlyphMode

	// music is music mode: no ffmpeg, no video pipe, and the spectrum instead of
	// frames. It is a flag rather than a second session type because everything
	// above this struct -- the render loop, the prompt, the resize path, the
	// stall handler, the queue -- is mode-agnostic already, and duplicating it
	// is what phase 6's byte pump and key router were spent avoiding.
	music bool
	// tap is the level tap feeding the spectrum. Per-generation like the
	// decoder: it is rebuilt on every seek so it re-reads from the new offset,
	// and closing it is part of retiring the old generation.
	tap *LevelTap

	// wantTap keeps the spectrum strip alive. True in music mode, where the
	// spectrum is the whole picture, and in video mode only when the strip is on.
	//
	// A video-mode tap that fails to start is logged and ignored rather than
	// fatal: the strip is a decoration on a picture that is already playing, and
	// killing the track because one optional row would not draw is the wrong
	// trade. Music mode is the opposite — see startTap.
	wantTap bool

	// The split view: a video pane beside the visualiser in music mode. Off until
	// someone asks for it, because it costs a second ffmpeg and half the cells.
	//
	// Only the lifetime lives here. The geometry belongs to the render loop, which
	// is the only place that knows what the grid currently is; duplicating it on
	// the session is how the strip and the grid ended up disagreeing about height.
	split bool
	thumb bool // still thumbnail rather than live video
	pane  *videoPane
	// paneURL is the pane's own video URL, minted by its own yt-dlp call on the
	// first press of W and then reused for the life of the track. A googlevideo URL
	// is single-use, so this is never shared with mpv or the tap.
	paneURL string

	// vizCap is the current style's CapScale, read by pumpMusic every tick.
	// Atomic because the style is chosen by the render loop while the pump reads
	// it from its own goroutine, and a data race here would be a stale frame rate
	// at worst — still worth not having.
	//
	// It exists because Viz.Heavy and Viz.CapScale were implemented by all six
	// styles and called by none, so the expensive ones were never actually capped
	// and the usage text describing that was wrong.
	vizCap atomic.Value // float64
}

// musicModeFor decides which mode a track can actually play in.
//
// `wantMusic` is what was asked for. The answer can be music regardless, because
// an mp3 has no video stream: asking for video hands the path to ffmpeg with
// `-map 0:v:0`, which is a hard error ("Stream map ” matches no streams"), then
// EOF on the pipe, then "cannot play ...: read first frame: EOF" — with ffmpeg's
// own stderr landing in the middle of the screen. `rasterbar -l ~/Music --play`
// hit this on track one and OutcomeError exited the whole queue.
//
// Falling back rather than refusing is the call: there is no mode the user could
// switch to, because "no picture" is not a failure they can act on. `silent` is
// the mirror and stays fatal in music mode — the spectrum is the content there,
// and a silent video behind a flat grid is worse than a refusal.
//
// Probed only for local files. A YouTube result always has video in at least one
// format, and resolveMedia has to ask for the probe anyway; branching on IsLocal
// keeps the network path paying nothing.
//
// Deliberately NOT done inside newTrackSession, even though that is where the
// original version put it. The answer changes the grid, the chrome height, the
// tap, and every `if o.music` in the render loop. A session that quietly decided
// for itself produced nil-buffer frames while the loop handed them to the video
// renderer, which rejects them as "frame too small" and ends the track without a
// word — so `-a` on an mp3 painted nothing at all.
func musicModeFor(track Track, wantMusic bool) bool {
	if wantMusic || !track.IsLocal() {
		return wantMusic
	}
	// An unreadable file is not a reason to change mode; the resolver will say so
	// with a real message a moment later.
	info, err := probeMedia(track.LocalPath)
	return err == nil && !info.HasVideo
}

// newTrackSession resolves a track's streams once and starts playback at pos.
//
// Resolution happens exactly once per track. The obvious alternative —
// re-resolving on every seek — costs a yt-dlp round trip of about a second,
// which is unusable on a held arrow key. The URLs are reused for every rebuild
// and only re-resolved if ffmpeg refuses them, the rare case of a grant having
// expired.
//
// The layout is taken whole rather than as cols/rows/fps because the source
// resolution travels with it: a re-resolve that forgot l.sourceH would silently
// downgrade a 1080p session to the 360p default.
//
// `music` must already be the decided mode — musicModeFor, not the caller's
// preference. See musicModeFor for why the answer cannot be discovered here.
func newTrackSession(track Track, l layout, pos float64, mute bool, mode ColorMode, glyph GlyphMode, music, wantTap bool) (*trackSession, error) {
	var (
		pair mediaPair
		err  error
	)
	if music {
		pair, err = resolveAudioPair(track)
	} else {
		pair, err = resolveMedia(track, l.sourceH, wantTap)
	}
	if err != nil {
		return nil, err
	}
	if music && pair.silent {
		return nil, fmt.Errorf("no audio stream in %s", track.Title)
	}
	s := &trackSession{
		track:   track,
		pair:    pair,
		l:       l,
		mute:    mute,
		mode:    mode,
		glyph:   glyph,
		music:   music,
		wantTap: wantTap || music,
	}
	s.vizCap.Store(1.0)
	if err := s.start(pos); err != nil {
		return nil, err
	}
	return s, nil
}

// SetVizCap records the active style's CapScale for the music pump.
//
// Called on every style switch and on every rebuild. See vizCap for why it is an
// atomic rather than a plain field.
func (s *trackSession) SetVizCap(c float64) {
	if s == nil {
		return
	}
	if c <= 0 || c > 1 {
		c = 1
	}
	s.vizCap.Store(c)
}

// resolve re-mints this track's streams after a failure.
//
// One re-resolve is worth it and a second failure is the track's problem. The
// same source height is requested on the video path, because dropping it here
// would quietly downgrade the stream mid-session.
func (s *trackSession) resolve() (mediaPair, error) {
	if s.music {
		return resolveAudioPair(s.track)
	}
	return resolveMedia(s.track, s.l.sourceH, s.wantTap)
}

// start launches the children at a media offset and spawns the decoder.
func (s *trackSession) start(pos float64) error {
	var (
		p   *SyncPlayer
		err error
	)
	launch := func() (*SyncPlayer, error) {
		if s.music {
			return startAudioOnly(s.pair.audioURL, s.mute, pos)
		}
		return startSyncPlayerAt(s.pair.videoURL, s.pair.audioURL, s.l.cols, s.l.rows, s.l.fps, s.mute, pos, s.mode, s.glyph)
	}
	p, err = launch()
	if err != nil {
		if pair, rerr := s.resolve(); rerr == nil {
			s.pair = pair
			p, err = launch()
		}
		if err != nil {
			return err
		}
	}

	s.stopDecoder()
	s.cur = p

	// Fresh channels per generation. The render loop reads s.frames and s.audio
	// on every select evaluation, so swapping the fields is what hands it the new
	// ones without a second loop or a lock.
	s.frames = make(chan videoFrame, frameQueueDepth)
	s.stop = make(chan struct{})

	if s.music {
		if err := s.startTap(pos); err != nil {
			// mpv is already running. newTrackSession returning an error means
			// playTrack never gets a session to Close, so the child has to be
			// reaped here or it plays on with no owner and no waiter.
			s.stopDecoder()
			return err
		}
		go s.pumpMusic(p, s.frames, s.stop)
		// mpv is the only child, so it is also the end of the track. This is the
		// same signal video mode gets from mpv, minus the silent case: music mode
		// refuses a source with no audio stream, so there is nothing where the
		// channel would be left nil.
		s.audio = make(chan struct{})
		go func(a *exec.Cmd, done chan struct{}) {
			var werr error
			if a != nil {
				werr = a.Wait()
			}
			if debugQueue {
				fmt.Fprintf(os.Stderr, "music: mpv exited: %v\n", werr)
			}
			close(done)
		}(p.audio, s.audio)
		return nil
	}

	// s.audio stays nil for a source with no audio stream, and a nil channel in a
	// select blocks forever — which is the point. `mpv --no-video` on a video-only
	// file exits immediately having played nothing, and the loop would read that
	// as the end of the track: a silent video stopped about two seconds in. With
	// nothing to listen to, the video pipe is the only clock there is, and the
	// grace timer after it closes is what ends the track.
	go s.decode(p, s.frames, s.stop)
	if !s.pair.silent {
		s.audio = make(chan struct{})
		go func(a *exec.Cmd, done chan struct{}) {
			if a != nil {
				_ = a.Wait()
			}
			close(done)
		}(p.audio, s.audio)
	}
	// The spectrum strip under a video needs its own tap. Started here, per
	// generation, so a seek or resize re-reads from the new offset exactly as
	// the music-mode tap does — a tap left at the old position analyses the wrong
	// part of the track.
	//
	// Failure is deliberately not fatal here, the opposite of music mode: the
	// picture is already playing and the strip is one row of chrome on top of it.
	if s.wantTap && !s.music {
		if err := s.startTap(pos); err != nil {
			if debugQueue {
				fmt.Fprintf(os.Stderr, "queue: video strip unavailable: %v\n", err)
			}
		}
	}
	return nil
}

// startTap opens the level tap for this generation.
//
// A failure is fatal to music mode, and deliberately not degraded to "play the
// audio with a dead screen". The spectrum is the content in this mode: a muted
// track playing silently behind a flat grid is indistinguishable from a hung
// process, and the user has no way to tell which it is.
func (s *trackSession) startTap(at float64) error {
	tap, err := StartLevelTapAt(s.pair.tapURL, at)
	if err != nil {
		return fmt.Errorf("level tap: %w", err)
	}
	s.tap = tap
	return nil
}

// pumpMusic paces the spectrum and reports the position.
//
// It is deliberately the same channel video mode's decoder fills, with a nil
// frame buffer. That is what keeps the render loop mode-agnostic: one select arm,
// one place that decides whether the frame is pixels or a visualizer redraw. The
// alternative -- a second loop for music mode -- is the duplication phase 6
// exists to warn about.
//
// The position is refreshed here rather than inside SyncPlayer.Seconds because
// this is the only goroutine in music mode that is allowed to block, and the
// render loop is not it.
func (s *trackSession) pumpMusic(p *SyncPlayer, out chan<- videoFrame, stop <-chan struct{}) {
	defer close(out)
	// The ticker runs at the *grid's* rate and the frame budget is enforced below,
	// rather than the ticker being built at the style's rate. That is what lets `v`
	// take effect immediately: a ticker cannot change rate, so a style switch
	// would otherwise need the whole pump restarted — another mpv and another tap
	// — to change how often it paints.
	base := musicFPS(s.l.cols, s.l.rows)
	tick := time.NewTicker(time.Second / time.Duration(base))
	defer tick.Stop()

	var (
		lastPaint time.Time
		every     = time.Second / time.Duration(base)
	)
	for {
		select {
		case <-stop:
			return
		case now := <-tick.C:
			// Heavy styles are capped, not disabled: they paint at CapScale of the
			// grid's rate. Both halves of the Viz interface existed and nothing
			// called CapScale, so `usage`'s claim that "the expensive styles are
			// capped rather than disabled" described an intention, not behaviour.
			capScale, _ := s.vizCap.Load().(float64)
			if capScale <= 0 || capScale > 1 {
				capScale = 1
			}
			every = time.Second / time.Duration(float64(base)*capScale)
			if every <= 0 {
				every = time.Second / time.Duration(base)
			}
			if !lastPaint.IsZero() && now.Sub(lastPaint) < every {
				continue
			}
			lastPaint = now
			// Poll on every tick, not just until the first reading lands. In video
			// mode the frame counter is the clock and mpv is only asked every two
			// seconds to correct drift; here mpv *is* the clock, so skipping the
			// poll freezes the position at whatever the first reading said.
			//
			// A dropped or late read is harmless -- posClock holds its last value
			// -- so paying a socket round trip per frame is the right trade. At
			// 30fps it is a few tens of microseconds a second.
			p.pollPosition(musicPollTimeout)
			select {
			case out <- videoFrame{buf: nil, pos: p.Seconds()}:
			case <-stop:
				return
			}
		}
	}
}

// frameQueueDepth is how many frames may sit between the decoder and the
// renderer.
//
// Two is enough to absorb a slow renderer without adding latency, and it keeps
// the existing backpressure: when rendering falls behind, the decoder blocks on
// the send and ffmpeg blocks on the pipe, exactly as it did when the loop was
// synchronous. Deeper would trade visible lag for memory.
const frameQueueDepth = 2

// frameBufs recycles decoded-frame byte slices between the decoder goroutine and
// the render loop. Each slice is owned by exactly one goroutine at a time: the
// decoder fills one and sends it, the render loop Draws it and returns it. The
// renderer copies what it needs and does not retain the slice.
var frameBufs sync.Pool

// decode reads frames and hands them to the render loop.
//
// The send selects on the generation's stop channel so retiring a decoder can
// never leave this goroutine blocked on a channel nobody will read again, which
// is what a plain send into a full buffer would do.
//
// Each frame is copied out of the player's reusable buffer, because with a
// buffered channel the decoder can be two frames ahead of the renderer, and
// handing over the pointer would let the decoder overwrite bytes the renderer is
// reading. The copy target comes from frameBufs: at 12fps a fresh allocation per
// frame is steady garbage, and the renderer only borrows the buffer for one Draw.
func (s *trackSession) decode(p *SyncPlayer, out chan<- videoFrame, stop <-chan struct{}) {
	// Closing the channel is how end-of-track reaches the render loop. Without it
	// a finished decoder looks identical to a slow one and the loop waits forever.
	defer close(out)
	for {
		src, err := p.Next()
		if err != nil {
			return
		}
		pos := p.Seconds()
		buf, _ := frameBufs.Get().([]byte)
		if cap(buf) < len(src) {
			buf = make([]byte, len(src))
		}
		buf = buf[:len(src)]
		copy(buf, src)
		select {
		case out <- videoFrame{buf: buf, pos: pos}:
		case <-stop:
			frameBufs.Put(buf)
			return
		}
	}
}

// stopDecoder retires the current decoder goroutine and both children.
func (s *trackSession) stopDecoder() {
	if s.stop != nil {
		close(s.stop)
		s.stop = nil
	}
	if s.tap != nil {
		s.tap.Close()
		s.tap = nil
	}
	if s.pane != nil {
		s.pane.Close()
		s.pane = nil
	}
	if s.cur != nil {
		s.cur.Close()
		s.cur = nil
	}
}

// startPane opens the split view's video pane at a media offset.
//
// Per generation, exactly like the decoder and the tap: after a seek the pane has
// to be rebuilt from the new offset or it keeps showing the part you left. That is
// why this is called from start rather than once per track.
//
// The URL is resolved lazily and remembered, so the second and later rebuilds cost
// nothing. Failing here is reported but never fatal for the same reason a
// video-mode strip failure is: the visualiser is already running and the pane is
// an extra.
func (s *trackSession) startPane(sl splitLayout, at float64) error {
	if !s.split {
		return nil
	}
	cols, rows := sl.video.cols, sl.video.rows
	if cols <= 0 || rows <= 0 {
		return nil
	}
	p := newVideoPane(cols, rows, s.mode, s.glyph)

	if s.thumb {
		// The still needs no video stream at all, which is why `T` works even
		// when the video URL could not be resolved.
		if s.pair.thumbURL == "" {
			return fmt.Errorf("no thumbnail for this track")
		}
		frame, err := startThumbnail(s.pair.thumbURL, cols, rows, s.mode, s.glyph)
		if err != nil {
			return err
		}
		p.Set(frame)
		s.pane = p
		return nil
	}

	if s.paneURL == "" {
		url, err := resolvePaneVideoURL(s.track, s.l.sourceH)
		if err != nil {
			return fmt.Errorf("split pane: %w", err)
		}
		s.paneURL = url
	}
	fps := fpsForGrid(cols, rows)
	if err := p.startLive(s.paneURL, fps, at, s.mode, s.glyph); err != nil {
		return fmt.Errorf("split pane: %w", err)
	}
	s.pane = p
	return nil
}

// --- media interface --------------------------------------------------------

func (s *trackSession) Position() float64 {
	return math.Float64frombits(s.pos.Load())
}

func (s *trackSession) Duration() float64 { return s.pair.dur }

// Chapters returns the track's chapter list, resolved with the streams.
func (s *trackSession) Chapters() []Chapter { return s.pair.chapters }

// currentChapter is the title of the chapter containing pos, or "".
//
// Strictly `Start <= pos`, with no tolerance. A tenth of a second before a
// boundary you are still in the chapter you have been watching for ninety
// seconds, and the footer should say so.
func currentChapter(chaps []Chapter, pos float64) string {
	title := ""
	for _, c := range chaps {
		if c.Start > pos {
			break
		}
		title = c.Title
	}
	return title
}

// SetPaused freezes or resumes every clock the track is running on.
//
// SIGSTOP on all of them in the same instant is the whole mechanism, and it is
// the one flow.go already uses for a covered terminal. Freezing them together is
// what preserves sync exactly, which is why pausing does not go through mpv's
// pause property: mpv would stop on the sound card's schedule while the video
// pipe kept filling, and that backlog would surface as a burst on resume.
//
// "Every clock" is three, not two, and the third was the bug. Video mode has mpv
// and ffmpeg, both inside SyncPlayer, so pauseChildren covered it. Music mode has
// no ffmpeg — and therefore no frame counter and no video pipe — but it has the
// LevelTap's ffmpeg instead, plus the split pane's, and both are -re paced clocks
// of the same media. PauseChildren saw a nil ff, correctly, and froze one of the
// three. The visible half was a spectrum and a split pane animating against a
// stopped sound; the half that actually mattered was that the tap kept consuming
// audio, so it ended the pause permanently ahead of mpv by the pause's length,
// and no later correction could close that — checkSync compares mpv against a
// frame counter that music mode does not have.
//
// The same two taps sit under video mode's `s` strip, which is why this fixes
// that too rather than only the full-screen visualizer.
//
// Order is not load-bearing — the signals land microseconds apart — but mpv goes
// first because it is the reference clock the other two are drawn against, and
// the reading should match the rule rather than look arbitrary.
func (s *trackSession) SetPaused(paused bool) error {
	if s.cur == nil {
		return nil
	}
	s.paused = paused
	if paused {
		s.cur.pauseChildren()
		s.tap.SetPaused(true)
		if s.pane != nil {
			s.pane.SetPaused(true)
		}
		return nil
	}
	s.cur.resumeChildren()
	s.tap.SetPaused(false)
	if s.pane != nil {
		s.pane.SetPaused(false)
	}
	// Apply anything the user asked for while we were frozen.
	if s.hasPendingVol && s.cur.ipc != nil {
		if err := s.cur.ipc.setVolume(s.pendingVol); err == nil {
			s.hasPendingVol = false
		}
	}
	return nil
}

// SetVolume changes volume over mpv's IPC.
//
// While paused this only records the request. A paused child is SIGSTOPped and
// cannot answer an IPC call, so sending it would block for the full timeout on
// every keypress — two seconds of apparent hang per volume key. Deferring to the
// resume is the fix, and the HUD shows the new value immediately so the keypress
// still feels acknowledged.
func (s *trackSession) SetVolume(v int) error {
	if s.paused {
		s.pendingVol = v
		s.hasPendingVol = true
		return nil
	}
	if s.cur == nil || s.cur.ipc == nil {
		return nil
	}
	return s.cur.ipc.setVolume(v)
}

// Seek requests a rebuild at target. Applied by the render loop, which is the
// only place that can swap the decoder and its channel while a select over them
// is in flight.
//
// Returns nil without acting, deliberately: the seek has been accepted and will
// happen at the top of the next loop iteration, and reporting an error here would
// invite a caller to retry a request that is already queued.
func (s *trackSession) Seek(target float64) error {
	s.seekTo = target
	s.dirty = true
	return nil
}

// Resize requests a rebuild at a new grid size, resuming from the current
// position so the track does not restart.
func (s *trackSession) Resize(l layout, at float64) {
	s.l = l
	s.seekTo = at
	s.dirty = true
}

func (s *trackSession) Close() error {
	s.stopDecoder()
	s.paneURL = ""
	return nil
}

// syncCheckInterval is how often drift is measured.
//
// Sampled rather than done per frame: an IPC round trip per frame is wasteful,
// and drift between a system clock and a sound card develops slowly.
const syncCheckInterval = 2 * time.Second

// --- the render loop --------------------------------------------------------

// playOpts is everything playTrack needs that is not the queue.
type playOpts struct {
	in, out    *os.File
	mute       bool
	quality    Quality
	aspect     float64
	cols, rows int // explicit overrides, 0 = auto
	mode       ColorMode
	glyphPref  GlyphMode

	// keys is the raw terminal byte stream. It is owned by the caller, not by
	// playTrack, because the key reader outlives a single track: one reader per
	// track would leave the previous one blocked in Read forever after a queue
	// advance, and the two would then compete for stdin — silently eating
	// keystrokes, including the one that quits.
	//
	// Bytes and not commands, because the render loop is the only thing that
	// knows whether a prompt is open. See readKeys.
	keys <-chan []byte

	// strip starts with the spectrum row on. Only meaningful in video mode.
	strip bool

	// music selects the spectrum over the video. See trackSession.music.
	music bool
	// prefs is the visualizer style and palette, shared across the whole queue.
	//
	// A pointer so that pressing `v` once survives a track ending. playQueue
	// owns the value and playOpts only carries the handle; the alternative -- a
	// copy per track -- would mean re-pressing `v` on every song, which is the
	// opposite of remembered. See vizPrefs.
	prefs *vizPrefs
}

// playTrack plays one track and reports why it stopped.
//
// The queue lives outside: playTrack receives the whole queue plus the index to
// start at and returns OutcomeNext/OutcomePrev so the caller can move the index
// and call again. That keeps this function about one track and leaves queue
// navigation to whoever owns the list.
func playTrack(o playOpts, queue []Track, index int) Outcome {
	termCols, termRows := 0, 0
	if c, r, err := term.TermSize(o.out); err == nil {
		termCols, termRows = c, r
	}
	if o.cols > 0 {
		termCols = o.cols
	}
	if o.rows > 0 {
		termRows = o.rows
	}

	// The spectrum strip. Off by default in video mode, because it costs a row of
	// video and most people watching a video are watching the video. On by default
	// in music mode, where it costs nothing -- there is no video to take a row from.
	strip := o.strip || o.music

	// VTIME read so arrow keys arrive whole; see makeRawVT.
	rawRestore, rawErr := term.MakeRawVT(o.in, 0, 1)

	// restoreTerminal puts the tty back and clears the screen. Deferred AND
	// callable, because it has to run in a specific order relative to an error
	// message: the message has to land on a clean screen, or the deferred clear
	// erases it. A deferred print runs *before* an earlier deferred restore (LIFO),
	// so calling this directly is the only way to get the order right.
	var restoreOnce sync.Once
	restoreTerminal := func() {
		restoreOnce.Do(func() {
			fmt.Fprint(o.out, syncOff)
			fmt.Fprint(o.out, "\x1b[?25h\x1b[2J\x1b[H")
			if rawErr == nil {
				rawRestore()
			}
		})
	}
	defer restoreTerminal()

	// SIGINT and SIGTERM quit, like `q`.
	//
	// Raw mode here clears ECHO and ICANON but NOT ISIG, so Ctrl-C is still
	// delivered by the tty driver as SIGINT — which means the `0x03` case in
	// cmdForByte is unreachable in practice, and the default action kills the
	// process outright. No deferred restore runs, so the shell is left with echo
	// and line editing off and the cursor hidden; ffmpeg and mpv are not reaped
	// and keep playing. The `usage` text promises "q / ctrl-c quit", and it did
	// quit — just without tidying up.
	//
	// A buffered channel of size 1 and a non-blocking send: a second Ctrl-C while
	// one is already queued does not block, and the loop reaches the select within
	// a frame.
	quitSig := make(chan os.Signal, 1)
	signal.Notify(quitSig, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(quitSig)

	// Synchronized output is emitted PER FRAME, not once per session. See syncOn.
	fmt.Fprint(o.out, "\x1b[?25l")

	if index < 0 || index >= len(queue) {
		return OutcomeEnded
	}
	track := queue[index]

	// Decide the mode BEFORE anything that depends on it.
	//
	// A local file with no video stream plays as a visualiser whatever was asked
	// for, and that answer changes the grid, the chrome height, the tap and every
	// `if o.music` in the loop below. Deciding it inside newTrackSession — which
	// is where it was — left the session pushing nil-buffer frames while the loop
	// handed them to the video renderer, which rejects them as "frame too small"
	// and ends the track with no message at all.
	o.music = musicModeFor(track, o.music)
	// The strip rides on the mode: in music mode it costs nothing, because there
	// is no video for it to take a row from.
	strip = o.strip || o.music
	l := computeLayout(termCols, termRows, o.aspect, o.quality, chromeRowsFor(strip))

	// The glyph probe round-trips with the terminal, so it must happen before
	// ffmpeg starts: the frame height depends on it.
	//
	// playQueue has already resolved it, before the key reader existed, because
	// the probe reads the Device Status Report straight off `in`. Probing it here
	// meant two readers on one tty: the report could be swallowed by the key pump
	// (700ms stall, then a silent downgrade to one pixel per cell), or the probe
	// could swallow a keystroke the user pressed during startup.
	glyph := GlyphCell
	if o.mode != ColorNone {
		glyph = resolveGlyph(o.glyphPref, o.in, o.out)
	}

	sess, err := newTrackSession(track, l, 0, o.mute, o.mode, glyph, o.music, strip)
	if err != nil {
		// Reported AFTER the deferred cursor-restore and screen-clear, so the one
		// diagnostic the user gets is not wiped microseconds after it is printed.
		//
		// It used to be printed before, and the defer ran immediately after: the
		// message existed for no measurable time and the process exited having said
		// nothing. With ffmpeg's own stderr landing in the middle of the screen
		// ("Stream map '' matches no streams", three times) before it, a failed
		// track was completely silent.
		//
		// Defer order in Go is LIFO, so this func runs before the restore defers
		// registered above it only if registered above — which it is, because
		// those run at return. Registering the print with defer here instead keeps
		// it to one line and puts it after.
		restoreTerminal()
		fmt.Fprintf(o.out, "cannot play %s: %v\r\n", truncate(track.Title, 60), err)
		return OutcomeError
	}
	defer sess.Close()

	pl := NewPlayerAt(queue, index, sess)

	// readKeys closes the channel on EOF, and the select below treats a closed
	// channel as "nobody left to drive the transport". There is deliberately no
	// second consumer on this channel: two readers would split the keystrokes
	// between them and transport keys would vanish at random.
	keys := o.keys
	if keys == nil {
		// Only reachable if a caller forgets to supply one. Owning it here keeps
		// the transport alive rather than deadlocking on a nil channel.
		own := make(chan []byte, 16)
		go readKeys(o.in, own)
		keys = own
	}

	bw := newBufWriter(o.out)
	renderer := newRenderer(bw, l.cols, l.rows, o.mode, glyph)

	// The visualizer, its grid and the palette. All three are rebuilt on a resize
	// or a style switch, and none of them exist in video mode -- a visualizer in
	// the video path would be paying for state nobody draws.
	//
	// prefs is defaulted here rather than in parseArgs so that a caller who
	// forgets it gets a working visualizer instead of a nil pointer on the first
	// keypress.
	prefs := o.prefs
	if prefs == nil {
		prefs = newVizPrefs()
	}
	var (
		viz  Viz
		grid *VizGrid
	)
	// The strip's own TryFrame cursor. Separate from the visualiser's because they
	// are two consumers of one tap and a shared cursor makes one of them starve.
	var stripReadGen uint64

	// Split-view state. sp owns what the user asked for; this closure owns the
	// geometry, because only the loop knows what the grid currently is.
	var (
		sp           splitState
		sl           splitLayout
		composite    []byte
		paneFallback []byte
	)
	sp.initDivider(l.cols)
	if o.music {
		viz = prefs.makeViz()
	}

	// vizPane is the visualiser's cell rectangle: the whole grid with the split
	// off, and one side of it with the split on.
	vizPane := func() paneRect { return sp.vizRect(l.cols, l.rows) }

	// rebuildViz recomputes the split geometry and resizes everything that
	// depends on it. One closure rather than four copies of the same three
	// statements, because the alternative is what already went wrong once: the
	// resize path resized the grid and forgot the strip, and the strip path
	// resized the chrome and forgot the grid.
	rebuildViz := func() {
		sl = computeSplitLayout(l.cols, l.rows, sp.divider, sp.videoLeft)
		vz := vizPane()
		if viz == nil {
			viz = prefs.makeViz()
		}
		viz.Resize(vz.cols, vz.rows)
		// Publish the cap after the resize, since CapScale reads the grid size.
		sess.SetVizCap(viz.CapScale(vz.cols, vz.rows))
		grid = NewVizGrid(vz.cols, vz.rows, o.mode != ColorNone, perCellFor(o.mode, glyph))
		grid.SetPalette(paletteAt(prefs.palette))
		if sp.on {
			composite = make([]byte, frameBytesFor(l.cols, l.rows, o.mode, glyph))
			paneFallback = fillPaneBackground(paneFallback,
				sl.video.cols, sl.video.rows, o.mode, glyph, paletteAt(prefs.palette))
		} else {
			composite = nil
			paneFallback = nil
		}
	}
	rebuildViz()

	// drawSplit composes both panes into one frame and hands it to the single
	// renderer.
	//
	// One renderer rather than one per pane, deliberately. The colour renderer
	// elides an SGR when a cell's colour matches the last one it drew, and that
	// assumption is "nothing else changed the terminal's colour state since my
	// last cell" -- which the other pane breaks constantly. Pane A decides it can
	// skip an SGR, pane B repaints in between, and A's next frame inherits B's
	// colours. Compositing first keeps one diff cache over the whole screen, which
	// is also what lets the no-stale-cells suite cover this for free.
	drawSplit := func() error {
		var vizFrame []byte
		if o.mode == ColorNone {
			vizFrame = grid.MonoFrame()
		} else {
			vizFrame = grid.ColorFrame()
		}
		// The pane's own frame, or the background-filled placeholder while it has
		// not produced one yet. Never nil: composeSplitFrame refuses a short
		// buffer rather than trusting the caller's arithmetic.
		vidFrame := paneFallback
		if sess.pane != nil {
			if latest := sess.pane.Latest(); len(latest) > 0 {
				vidFrame = latest
			}
		}
		if !composeSplitFrame(composite, sl, l.cols, l.rows, o.mode, glyph, vizFrame, vidFrame) {
			// A pane was resized without being rebuilt. Refuse rather than copy
			// past the end of a buffer: this runs inside a painter, where a panic
			// takes the whole player down with it.
			return fmt.Errorf("split: pane %dx%d does not fit grid %dx%d",
				sl.video.cols, sl.video.rows, l.cols, l.rows)
		}
		return renderer.Draw(composite)
	}

	// paintMusic draws one spectrum frame. Split out so the render loop's select
	// arm stays a single statement and the rebuild path can share it.
	paintMusic := func() error {
		// A dead tap is an error, not a quiet picture.
		//
		// Measured on a live YouTube track: the tap's ffmpeg exited within
		// seconds, the process stayed up, and all eighteen styles drew an empty
		// grid for the rest of the song with nothing anywhere saying why. The tap
		// has always recorded its exit and Dead has always existed -- nothing
		// called it. Same shape as the Viz.CapScale bug in AGENTS.md: both halves
		// of the interface, and the consumer never written.
		//
		// Reported once and then fatal, because a tap that is dead stays dead:
		// there is no spectrum left to draw and continuing would just hold the
		// last frame on screen forever.
		if sess.tap != nil {
			if d := sess.tap.Dead(); d != nil {
				return fmt.Errorf("audio tap died, so the visualiser has nothing to draw: %w", d)
			}
		}
		// Push before Clear/Paint: the styles fold the new analysis into their
		// own state (waterfall captures a row, particles integrate), and doing it
		// in this order means the frame drawn is the frame analysed.
		if sess.tap != nil {
			if f, ok := sess.tap.TryFrame(); ok {
				viz.Push(&f)
				if debugViz {
					alive := -1
					if p, ok := viz.(*particlesViz); ok {
						alive = p.alive
					}
					fmt.Fprintf(os.Stderr, "viz %s beat=%.3f bpm=%.1f alive=%d band[0]=%.3f band[last]=%.3f\r\n",
						viz.Name(), f.Beat, f.BPM, alive,
						f.Bands[0], f.Bands[len(f.Bands)-1])
				}
			}
		}
		grid.Clear()
		viz.Paint(grid)
		if o.mode == ColorNone {
			if !sp.on {
				return renderer.Draw(grid.MonoFrame())
			}
			return drawSplit()
		}
		if !sp.on {
			return renderer.Draw(grid.ColorFrame())
		}
		return drawSplit()
	}

	// applySplit rebuilds the geometry and restarts the pane, returning a status
	// line or "".
	//
	// It returns rather than calling reportLine because reportLine is declared
	// after this point in playTrack, and reaching forward for it is how a closure
	// ends up capturing a variable that is not what it looks like.
	//
	// Restarting rather than resizing is not optional: the pane's pixel width is
	// ffmpeg's scale target, so a new width is a new decode, exactly as a
	// SIGWINCH rebuilds the video decoder. Resizing the buffer alone would leave
	// ffmpeg sending frames of the old size and the compositor would refuse them.
	applySplit := func() string {
		rebuildViz()
		sess.split = sp.on
		sess.thumb = sp.thumb
		if sess.pane != nil {
			sess.pane.Close()
			sess.pane = nil
		}
		if sp.on {
			if err := sess.startPane(sl, sess.Position()); err != nil {
				// Back out rather than leave an empty rectangle on screen: a
				// half-drawn split reads as a broken renderer, not as a failure to
				// fetch a picture.
				sp.on = false
				sess.split = false
				rebuildViz()
				return "video pane: " + err.Error()
			}
		}
		return ""
	}

	// lastKey drives the hint fade: the row shows itself on every keypress and
	// then gets out of the way.
	lastKey := time.Now()

	// markAt is when the pre-seek tick was painted, so it can be taken away again.
	markAt := time.Time{}

	// The jump-to-time field and the bytes of an escape sequence that has started
	// but not finished. Both live here rather than in the reader goroutine: this is
	// the only place that knows what the bytes are allowed to mean.
	var router keyRouter

	// The last HUD painted, so an unchanged repaint is skipped instead of
	// written ten times a second for nothing.
	hudText := ""

	// statusText is the transient footer message and when it expires. Held here
	// rather than written to the terminal, because the HUD owns the footer row and
	// repaints it -- see hud.status.
	statusText := ""
	statusUntil := time.Time{}

	// spectrum is the band's current level and peak, for the HUD strip. A Visualizer
	// rather than the raw analyser bands so the strip and the music-mode styles
	// react identically -- otherwise the strip would sit still while the
	// full-screen bars move, which reads as one of them being broken.
	var stripViz Visualizer
	if strip {
		stripViz.Resize(l.cols)
	}

	paintHUD := func(force bool) {
		st := pl.State()
		h := hud{
			title:     track.Title,
			channel:   track.ChannelText(),
			pos:       st.Pos,
			dur:       st.Dur,
			volume:    st.Volume,
			muted:     o.mute,
			paused:    st.Paused,
			queue:     st.Index + 1,
			total:     st.Queue,
			chapter:   currentChapter(pl.Chapters(), st.Pos),
			mark:      st.Mark,
			hasMark:   st.HasMark,
			showHints: time.Since(lastKey) < hintLinger,
			status:    statusText,
		}
		if strip {
			if sess.tap != nil {
				// TryFrame, not Bands. This is the same rule the visualiser
				// follows and it was being broken one layer up: paintHUD runs on
				// every keypress as well as the 10Hz tick, and Push decays the
				// level by 0.82 and the peak by 0.93 *per call*. Mashing keys
				// therefore ran the strip's release ~3x faster than the music,
				// which reads as the strip being wrong rather than as a stale
				// one. TryFrame reports false until a new analysis window has
				// landed, so the decay happens at the audio's ~11Hz.
				if f, ok := sess.tap.TryFrameAt(&stripReadGen); ok {
					stripViz.Push(f.Bands)
				}
				h.strip = miniBars(stripViz.Level(), l.cols, stripViz.Peak())
			} else {
				h.strip = miniBars(nil, l.cols, nil)
			}
		}
		lines := h.lines(l.cols)
		joined := strings.Join(lines, "\n")
		if !force && joined == hudText {
			return
		}
		hudText = joined
		// Reset SGR before the chrome.
		//
		// The colour renderer sets a foreground and background per cell and never
		// resets at the end of a frame, so whatever colour the last cell painted
		// is still active here. Plain HUD text then inherits a colour sampled
		// from the video, and on a dark scene the whole HUD renders near-black on
		// near-black: emitted correctly, invisible on screen. Only colour mode is
		// affected, which is why a mono run never showed it.
		fmt.Fprint(bw, "\x1b[0m")
		for i, line := range lines {
			fmt.Fprintf(bw, "\x1b[%d;1H%s", l.rows+1+i, line)
		}
		_ = bw.Flush()
	}

	// paintOverlay draws the prompt over the bottom of the grid. Called after every
	// frame while it is open, because the frame is what paints over it.
	paintOverlay := func() {
		if !router.pr.open {
			return
		}
		st := pl.State()
		if err := renderer.Overlay([]string{router.pr.line(), router.pr.status(st.Pos, st.Dur)}); err == nil {
			_ = bw.Flush()
		}
	}

	// closeOverlay takes the prompt down and forces the next frame to repaint the
	// cells it covered.
	//
	// The ForceNext is not an optimisation, it is the fix. The diff caches hold the
	// video values for those cells, so a frame drawn after the overlay is gone sees
	// no change and skips them — the prompt would stay on screen for the rest of
	// the track. One full repaint is the price of not shipping that.
	//
	// Deliberately unconditional. The field has already closed itself by the time
	// this is called — prompt.consume closes it before reporting — so guarding on
	// "was it open" skips the repaint exactly when it is needed, which is always.
	// That bug shipped in the first draft of this and only a real terminal showed
	// it: the unit test called ForceNext itself, so it tested the intent rather
	// than the code.
	closeOverlay := func() {
		router.pr.stop()
		renderer.ForceNext()
		hudText = ""
	}

	resized := make(chan os.Signal, 1)
	signal.Notify(resized, syscall.SIGWINCH)
	defer signal.Stop(resized)

	lastSync := time.Time{}
	lastHUD := time.Time{}
	userPaused := false
	// Non-nil once the video pipe has closed; fires if mpv has not reported by
	// then. Reset on every rebuild so a new track starts with a clean slate.
	var videoEnded <-chan time.Time
	// Which of the two streams have finished. The track ends when both have, not
	// when the first one does.
	//
	// This was "mpv exits -> end", and that is wrong whenever the audio is shorter
	// than the video: a file whose soundtrack runs out thirty seconds before the
	// picture does would jump to the next track with the video still going. It was
	// invisible for the whole life of the project because `probeMedia` never passed
	// -show_streams, so hasAudio was always false, every local file was classified
	// silent, `sess.audio` stayed nil, and video mode ended tracks on the grace
	// timer after the video pipe closed instead. Fixing the probe turned a latent
	// ordering bug into a live one.
	//
	// Music mode has no video stream to wait for, so mpv's exit is the end there.
	var videoDone, audioDone bool
	// lastKey drives the hint fade: the row shows itself on every keypress and
	// then gets out of the way.
	// endOfTrack reports play finished with this track: on to the next result if
	// there is one, otherwise the queue is done.
	endOfTrack := func() Outcome {
		if index+1 < len(queue) {
			return OutcomeNext
		}
		return OutcomeEnded
	}

	// reportLine writes a one-line status over the error row and lets it expire
	// on the hint clock.
	//
	// Shares the transport error row rather than adding a fourth HUD row: the row
	// already exists for "a key you pressed failed", and this is the same kind of
	// message. A dedicated row would mean recomputing the layout, re-resolving
	// ffmpeg's scale target and rebuilding the decoder to change how many rows
	// there are, which is not worth a status line.
	reportLine := func(s string) {
		statusText = s
		statusUntil = time.Now().Add(hintLinger)
		lastKey = time.Now()
		paintHUD(true)
	}

	// applyCmd runs one decoded transport command and reports whether playback
	// should stop. Split out of the select so feedKeys can drive it while a
	// prompt is closing and the bytes after the closing key still count.
	applyCmd := func(cmd Cmd) Outcome {
		// Digit palette select, ahead of the switch because the Cmd is a range
		// and a Go `case` cannot spell one.
		//
		// Same two guard rails as `c`, for the same reasons: the grid only exists
		// in music mode, and in mono every palette resolves to the same picture,
		// so selecting one silently would look like a broken key.
		if cmd >= CmdPaletteSelect && cmd < CmdPaletteSelect+Cmd(len(palettes)) {
			if !o.music {
				reportLine("viz keys need music mode (-M)")
				return OutcomePlaying
			}
			if o.mode == ColorNone {
				reportLine("no colour in this terminal  ·  --mono, or try --color")
				return OutcomePlaying
			}
			prefs.palette = int(cmd - CmdPaletteSelect)
			prefs.clamp()
			grid.SetPalette(paletteAt(prefs.palette))
			// ForceNext and hudText are both load-bearing, for the reasons
			// spelled out at the style-switch case below: the palette lives on the
			// grid and the diff cache holds the old frame's colours, and that
			// Draw clears the whole screen including the chrome.
			renderer.ForceNext()
			hudText = ""
			reportLine(describeStyle(prefs))
			return OutcomePlaying
		}
		switch cmd {
		case CmdNone:
			return OutcomePlaying
		case CmdQuit:
			return OutcomeQuit
		case CmdTogglePause:
			userPaused = !userPaused
			pl.Do(cmd)
		case CmdSplitToggle, CmdPaneLeft, CmdPaneRight, CmdThumbToggle, CmdDividerLeft, CmdDividerRight:
			// Split view. Every one of these changes the grid's geometry or the
			// pane behind it, so they all go through applySplit, which rebuilds
			// and restarts -- the pane's pixel width is ffmpeg's scale target, so
			// there is no cheaper version of this.
			//
			// a/d set a side rather than toggling. Idempotent is worth more here
			// than a single keypress, because the natural mistake is pressing the
			// same one twice and finding the pane on the other side.
			switch cmd {
			case CmdSplitToggle:
				sp.toggle()
			case CmdPaneLeft:
				sp.setSide(true)
			case CmdPaneRight:
				sp.setSide(false)
			case CmdThumbToggle:
				sp.toggleThumb()
			case CmdDividerLeft:
				sp.nudge(l.cols, -1)
			case CmdDividerRight:
				sp.nudge(l.cols, +1)
			}
			if !o.music {
				reportLine("split view needs music mode (-M)")
				return OutcomePlaying
			}
			if msg := applySplit(); msg != "" {
				reportLine(msg)
			}
			renderer.ForceNext()
			hudText = ""
			paintHUD(true)
			return OutcomePlaying
		case CmdStrip:
			strip = !strip
			// Recompute the layout and rebuild. In video mode this is mandatory
			// rather than cosmetic: the strip takes a row out of the grid, and the
			// grid is ffmpeg's scale target, so the decoder has to be told. In music
			// mode there is no video and nothing to rebuild, which is why the same
			// key is free there.
			// The live window size, not the one captured at entry. CmdStrip was
			// recomputing from termCols/termRows as read at line 616, so after a
			// resize the strip toggle snapped the grid back to the pre-resize
			// geometry and rebuilt the decoder at it — the resize that had just
			// been handled got undone by the next keypress. The SIGWINCH arm
			// updates these; this reads them.
			c, r, terr := term.TermSize(o.out)
			if terr == nil {
				termCols, termRows = c, r
				if o.cols > 0 {
					termCols = o.cols
				}
				if o.rows > 0 {
					termRows = o.rows
				}
			}
			l = computeLayout(termCols, termRows, o.aspect, o.quality, chromeRowsFor(strip))
			if strip {
				stripViz = Visualizer{level: make([]float64, l.cols), peak: make([]float64, l.cols)}
			}
			if !o.music {
				sess.wantTap = strip
				// Turning the strip on mid-track is the one moment the tap does not
				// already exist, because it was only resolved at track start when
				// the strip was off. It needs a URL of its own — one input, one
				// consumer — and for a YouTube source that means one more
				// `yt-dlp -J`, which is why it is done here and not eagerly on
				// every video track.
				if strip && sess.tap == nil {
					pair, err := resolveMedia(track, l.sourceH, true)
					if err != nil {
						reportLine("strip needs a second audio stream")
					} else {
						sess.pair = pair
						if err := sess.startTap(sess.Position()); err != nil {
							reportLine("strip unavailable")
						}
					}
				}
				sess.Resize(l, sess.Position())
			} else {
				// Music mode has no decoder to rebuild, but it still holds a grid,
				// a style and a renderer sized to the old row count. Toggling the
				// strip changed the chrome height without any of them being told,
				// so the HUD drew its extra row over a grid cell. rebuildViz is the
				// same recompute SIGWINCH does, minus the process churn.
				//
				// The pane restarts with it when the split is on, because its pixel
				// width is ffmpeg's scale target and the grid just changed shape.
				if sp.on {
					if msg := applySplit(); msg != "" {
						reportLine(msg)
					}
				} else {
					rebuildViz()
				}
				// The renderer is sized to the grid, and the grid just changed
				// shape. Only the `sess.dirty` rebuild recreates it, and the music
				// branch never sets dirty — so a renderer left over from a wider
				// grid would reject the next frame as "frame too small" and return
				// OutcomeError, which ends the track and then the queue, silently.
				// Verified reachable: strip off grows the grid by a row, a seek
				// rebuilds the renderer at the new size, and stripping back on
				// makes the grid smaller than the renderer again.
				renderer = newRenderer(bw, l.cols, l.rows, o.mode, glyph)
				renderer.ForceNext()
				fmt.Fprint(bw, "\x1b[2J\x1b[H")
				hudText = ""
			}
			reportLine(map[bool]string{true: "spectrum on", false: "spectrum off"}[strip])
		case CmdVizNext, CmdVizPrev, CmdPalette:
			// Not transport, and Player has no business knowing about them.
			// Handled here because the render loop is the only thing that knows
			// whether a visualizer exists -- in video mode there is no grid and no
			// tap, so switching style would switch nothing and say so, which is
			// better than a key that appears broken.
			//
			// Written as early returns rather than breaks on purpose. A `break`
			// inside the inner switch below breaks out of *that switch*, not out of
			// this case, so the mono message fell through and was immediately
			// overwritten by the status line at the end -- which is what made the
			// first version of this report a palette name in mono mode.
			if !o.music {
				reportLine("viz keys need music mode (-M)")
				return OutcomePlaying
			}
			if cmd == CmdPalette {
				if o.mode == ColorNone {
					// Not a dead key: the grid draws through the grey ramp when
					// colour is off, so every palette would produce an identical
					// screen and cycling would look broken.
					reportLine("no colour in this terminal  ·  --mono, or try --color")
					return OutcomePlaying
				}
				prefs.nextPalette()
				grid.SetPalette(paletteAt(prefs.palette))
				// No new Viz and no Reset: the palette lives on the grid, and the
				// style keeps its history. Only the colours changed.
				renderer.ForceNext()
				hudText = ""
				reportLine(describeStyle(prefs))
				return OutcomePlaying
			}
			if cmd == CmdVizNext {
				prefs.nextStyle()
			} else {
				prefs.prevStyle()
			}
			// A new style has never drawn these cells. Without the forced repaint
			// the diff cache still holds the old frame's values and the next Draw
			// sees no change -- the same stale-cell trap as the prompt overlay,
			// and the reason ForceNext is not optional here.
			//
			// hudText has to go with it. ForceNext's next Draw clears the whole
			// screen, chrome included, and paintHUD's "unchanged, skip" cache
			// would then decide there is nothing to repaint and leave the title,
			// clock and footer blank until something else changed them. This is the
			// same trap the stall-recovery path documents, and a style switch is
			// another way to reach it.
			viz = prefs.makeViz()
			vp := vizPane()
			viz.Resize(vp.cols, vp.rows)
			viz.Reset()
			// A new style brings its own budget. radial's cap depends on the grid
			// size, particles' on the particle budget, so this has to follow the
			// switch rather than being set once per track.
			sess.SetVizCap(viz.CapScale(vp.cols, vp.rows))
			renderer.ForceNext()
			hudText = ""
			reportLine(describeStyle(prefs))
		default:
			pl.Do(cmd)
			if st := pl.State(); st.Outcome != OutcomePlaying {
				return st.Outcome
			}
		}
		// A keypress the user just made failed. Say so rather than letting it look
		// like a broken key.
		//
		// Through reportLine, not straight to the terminal: the footer row is
		// repainted by paintHUD a few lines below, and a message written directly
		// would be overwritten before it was ever on screen. That was the previous
		// behaviour and it is why a failed transport key looked like no key at all.
		if st := pl.State(); st.LastErr != nil {
			reportLine("transport: " + st.LastErr.Error())
			pl.ClearErr()
		}
		return OutcomePlaying
	}

	// feedKeys applies one chunk of terminal bytes. The routing itself lives in
	// keyRouter so it can be tested without a terminal; this is the adapter that
	// turns its answer into transport calls and repaints.
	feedKeys := func(chunk []byte) Outcome {
		st := pl.State()
		res := router.feed(chunk, st.Pos, st.Dur)
		for _, c := range res.cmds {
			if out := applyCmd(c); out != OutcomePlaying {
				return out
			}
		}
		switch {
		case res.submit:
			// Closed before the seek, so the overlay's cells are already marked for
			// repaint by the time the jump rebuilds the decoder.
			closeOverlay()
			if res.valid {
				pl.JumpTo(res.target)
				markAt = time.Now()
			}
		case res.cancel:
			closeOverlay()
		}
		if res.open {
			paintOverlay()
		}
		paintHUD(true)
		return OutcomePlaying
	}

	runtime.GC()
	for {
		// Rebuild before selecting, so the select below always watches the
		// current channels. Seeking and resizing both need the decoder swapped.
		if sess.dirty {
			pos := sess.seekTo
			sess.dirty = false
			sess.stopDecoder()
			if err := sess.start(pos); err != nil {
				return OutcomeError
			}
			renderer = newRenderer(bw, l.cols, l.rows, o.mode, glyph)
			fmt.Fprint(bw, "\x1b[2J\x1b[H")
			hudText = ""
			lastSync = time.Time{}
			videoEnded = nil
			videoDone, audioDone = false, false
			if o.music {
				// Reset rather than Resize: the level tap is restarted from the new
				// offset, so its history describes the wrong part of the song.
				// Keeping it would draw a waterfall of the part you just left.
				viz.Reset()
				grid.Clear()
				// The tap is a new process, so its generation counter restarts at
				// zero. A stale cursor would read as "nothing new yet" until the
				// count climbed past wherever the previous generation stopped —
				// which for a long-running tap is the rest of the track, i.e. a
				// frozen visualiser with a live process behind it.
				stripReadGen = 0
				// The split pane is per generation like everything else here. It has
				// to be restarted at the new offset or it keeps showing the part of
				// the track that was just seeked away from, and it needs a new URL
				// because a googlevideo grant is single-use.
				if sp.on {
					sess.pane = nil // stopDecoder already closed it
					if err := sess.startPane(sl, pos); err != nil && debugQueue {
						fmt.Fprintf(os.Stderr, "queue: split pane after seek: %v\n", err)
					}
				}
			}
			if userPaused {
				_ = sess.SetPaused(true)
			}
			// The rebuild cleared the screen the overlay was on, and the new
			// renderer has never drawn it.
			paintOverlay()
		}

		select {
		case chunk, ok := <-keys:
			if !ok {
				return OutcomeQuit
			}
			lastKey = time.Now()
			if out := feedKeys(chunk); out != OutcomePlaying {
				return out
			}

		case <-quitSig:
			// Ctrl-C, or a SIGTERM from the window manager. The same outcome `q`
			// produces, so every deferred cleanup above runs: the terminal is
			// restored, the screen is cleared, and Close reaps ffmpeg and mpv.
			// Before this existed the default action killed the process and none
			// of that happened.
			return OutcomeQuit

		case <-resized:
			// Drain: a drag emits a burst of these.
			for drained := false; !drained; {
				select {
				case <-resized:
				default:
					drained = true
				}
			}
			c, r, err := term.TermSize(o.out)
			if err != nil || (c == l.cols && r == l.termRows) {
				continue
			}
			// An explicit --cols/--rows outranks the window, on the resize as well
			// as at startup. It did not: the recompute used the terminal size, so
			// one drag of the window edge snapped a deliberately fixed grid back
			// to whatever the terminal happened to be.
			if o.cols > 0 {
				c = o.cols
			}
			if o.rows > 0 {
				r = o.rows
			}
			l = computeLayout(c, r, o.aspect, o.quality, chromeRowsFor(strip))
			sess.Resize(l, sess.Position())
			if strip {
				// The strip's own band arrays were sized to the old width, so
				// after a drag it resampled to the new column count out of a
				// buffer that was too short and rendered letterboxed for the rest
				// of the track. It is created once per track and on `s`, never on
				// SIGWINCH, which is why only this path could fix it.
				stripViz.Resize(l.cols)
			}
			if o.music {
				// The visualizer has to be told, and so does the grid: both hold
				// cols*rows state. This is the case the plan flagged as the one
				// only a real terminal shows -- a style whose internal row count is
				// from before the resize paints out of bounds or leaves a band of
				// stale cells, and neither shows up in a unit test that never
				// resizes.
				//
				// rebuildViz, not the three statements inline, because with the
				// split on the grid is one pane of the new shape and not the whole
				// of it -- and the pane itself is a new ffmpeg scale target.
				if sp.on {
					if msg := applySplit(); msg != "" && debugQueue {
						fmt.Fprintf(os.Stderr, "queue: %s\n", msg)
					}
				} else {
					rebuildViz()
				}
				renderer.ForceNext()
			}

		case fr, open := <-sess.frames:
			if !open {
				// The decoder hit end of file, but mpv may still be sounding the
				// tail. Returning here would cut the audio off early, so treat it
				// as "video finished" and let sess.audio decide, with a deadline
				// in case mpv never reports.
				videoDone = true
				if audioDone {
					// Both streams are finished. No grace period needed: there is
					// nothing left to wait for.
					if debugQueue {
						fmt.Fprintf(os.Stderr, "queue: both streams done at %s\n",
							formatPos(sess.Position()))
					}
					return endOfTrack()
				}
				if videoEnded == nil {
					videoEnded = time.After(audioTailGrace)
				}
				// Same reason as the audio arm: a closed channel is permanently
				// ready, so the loop has to stop watching it.
				sess.frames = nil
				continue
			}
			sess.pos.Store(math.Float64bits(fr.pos))
			pl.Tick(fr.pos)
			// One arm, two sources. Music mode sends a nil buffer and a position;
			// video mode sends pixels. Branching here rather than having two loops
			// is what keeps the prompt, the stall handler and the HUD working
			// identically in both modes -- which is the entire reason music mode is
			// a flag on trackSession and not a second player.
			//
			// The synchronized-output pair brackets everything painted for this
			// frame: the grid, the HUD at the bottom of the loop, and the prompt
			// overlay. One frame to the user is one begin and one end.
			fmt.Fprint(bw, syncOn)
			if o.music {
				if err := paintMusic(); err != nil {
					return OutcomeError
				}
			} else {
				drawErr := renderer.Draw(fr.buf)
				if fr.buf != nil {
					frameBufs.Put(fr.buf)
				}
				if drawErr != nil {
					return OutcomeError
				}
			}
			if err := bw.Flush(); err != nil {
				return OutcomeError
			}

			// A terminal that has stopped reading must not be written to, and
			// both children have to freeze together or the clocks drift.
			if !fdWritable(o.out) {
				_ = sess.SetPaused(true)
				if !awaitDrain(o.out, sess.cur, keys, 120*time.Millisecond) {
					return OutcomeQuit
				}
				if !userPaused {
					_ = sess.SetPaused(false)
				}
				// ForceNext makes the next Draw clear the screen and repaint the
				// grid. That clear takes the chrome with it, so the HUD's
				// "unchanged, skip" cache has to be invalidated too —
				// otherwise paintHUD decides there is nothing to do and the
				// title, clock and hints stay blank until the clock ticks
				// over a second or a key is pressed.
				renderer.ForceNext()
				hudText = ""
				lastSync = time.Time{}
			}

			if time.Since(lastHUD) > hudInterval {
				lastHUD = time.Now()
				// Expire the status line on the same tick as the mark. Expiring it
				// inside reportLine would need a timer, and expiring it in the HUD's
				// own skip check would leave the footer pinned to a message that
				// has nothing to replace it -- a HUD whose "unchanged" cache never
				// fires is a HUD that repaints every frame for nothing.
				if statusText != "" && time.Now().After(statusUntil) {
					statusText = ""
					lastKey = time.Time{} // let the hints stay faded rather than
					// popping back because of an expiring message
				}
				// The pre-seek tick has been up long enough to be read. Leaving it
				// would make a bar that is never entirely clean, which reads as a
				// rendering fault rather than as information.
				if !markAt.IsZero() && time.Since(markAt) > markLinger {
					pl.ClearMark()
					markAt = time.Time{}
				}
				paintHUD(false)
			}

			// After the HUD, because the HUD is chrome outside the grid and the
			// overlay is inside it. Drawing it first would have the frame's own
			// cells win, which is the same as not drawing it.
			paintOverlay()

			// End of frame. Everything painted since the matching syncOn is now
			// presented in one go, so a viewer never sees the grid half-updated.
			//
			// Flushed here rather than at the top of the next iteration because the
			// `l` has to reach the terminal after this frame's bytes, not before the
			// next frame's. Leaving it open across the select would hold the frame
			// for however long the loop waits on a channel, which on a slow track is
			// most of a second.
			fmt.Fprint(bw, syncOff)
			_ = bw.Flush()

			if time.Since(lastSync) > syncCheckInterval {
				lastSync = time.Now()
				rep, corrected := sess.cur.checkSync()
				if debugSync {
					tag := "ok"
					if corrected {
						tag = "CORRECTED"
					} else if sess.cur.ipc == nil {
						tag = "no-ipc"
					} else if e := ipcLastErr(); e != "" {
						tag = "ERR:" + e
					}
					fmt.Fprintf(os.Stderr, "\rsync: video %s audio %s drift %+.3fs %s      ",
						formatPos(rep.VideoPos), formatPos(rep.AudioPos), rep.Drift, tag)
				}
			}

		case <-sess.audio:
			// mpv has exited. That is the end of the track only if there is no
			// video left to watch -- either because this is music mode, or because
			// the video pipe already closed.
			//
			// Ending here unconditionally is what made a file with a short
			// soundtrack skip to the next track while its picture was still
			// running.
			audioDone = true
			// Nil the channel before doing anything else. A closed channel is
			// *always* ready, so leaving it in the select makes this arm fire on
			// every single iteration -- a busy loop that spins a core and, with any
			// logging on, floods stderr. A nil channel blocks forever in a select,
			// which is exactly the "never again" this needs.
			sess.audio = nil
			if !o.music && !videoDone {
				if debugQueue {
					fmt.Fprintf(os.Stderr, "queue: mpv done at %s, waiting for video\n",
						formatPos(sess.Position()))
				}
				continue
			}
			if debugQueue {
				fmt.Fprintf(os.Stderr, "queue: ending on mpv exit at %s\n",
					formatPos(sess.Position()))
			}
			return endOfTrack()

		case <-videoEnded:
			// The video finished and mpv has not reported within the grace period.
			// Its clock is the one the user hears, but it is not going to speak.
			if debugQueue {
				fmt.Fprintf(os.Stderr, "queue: ending on video-grace at %s\n",
					formatPos(sess.Position()))
			}
			return endOfTrack()
		}
	}
}

// chromeRows is the HUD's row count without the spectrum strip, and
// chromeRowsFor is the count with it.
//
// A function rather than two constants because the number is used in exactly two
// places -- computeLayout and the strip toggle -- and a pair of constants that has
// to stay in step with the HUD's own row count is a way to have a strip that eats
// the footer without anything complaining.
func chromeRowsFor(strip bool) int {
	if strip {
		return hudStripRows + 1
	}
	return hudStripRows
}

// hudStripRows is the HUD's own size: title, progress, footer. The strip, when
// on, is a fourth row above the footer.
const hudStripRows = 3

// Synchronized output, DEC private mode 2026: the terminal buffers what is
// written after `h` and presents it in one go when it sees `l`, which is what
// stops a half-updated grid being visible mid-repaint.
//
// IT IS A PAIR, NOT A MODE. Sending `h` at startup and `l` at exit — reading it
// as a mode you switch on, which is the obvious mistake and the one made here —
// means a terminal that actually implements it buffers the whole track and
// presents nothing until the program exits: a black screen for the entire video.
// On a terminal that ignores unknown private modes, which is every pty harness
// and most CI, that version looks perfect. Every test passed and the feature was
// broken on the terminals it was written for.
//
// Emitted unconditionally and unconditionally closed per frame: a terminal that
// does not know the mode ignores it, and there is nothing to detect.
const (
	syncOn  = "\x1b[?2026h"
	syncOff = "\x1b[?2026l"
)

// audioTailGrace is how long the audio gets to finish after the video pipe
// closes. Short enough that a stuck mpv is not a hang, long enough that a track
// whose audio outlasts its video is not cut off mid-word.
const audioTailGrace = 5 * time.Second

// hudInterval is how often the chrome is repainted. The clock changes once a
// second, so 10Hz is already generous; per-frame would rewrite three lines
// twelve times a second to display the same digits.
const hudInterval = 100 * time.Millisecond

// hintLinger is how long the key hints stay up after a keypress. Long enough to
// read at a glance, short enough that they are not permanent furniture.
const hintLinger = 4 * time.Second

// markLinger is how long the pre-seek tick stays on the bar. Long enough to
// catch in peripheral vision, short enough that the bar looks like a bar again
// before the next keypress.
const markLinger = 6 * time.Second

// newBufWriter is the batched writer behind every repaint.
func newBufWriter(w io.Writer) *bufio.Writer { return bufio.NewWriterSize(w, 32*1024) }
