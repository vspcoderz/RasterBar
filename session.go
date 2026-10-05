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
// The rule in AGENT.MD is one input, one consumer, and it is a rule about URLs,
// not about streams. Two separate yt-dlp calls mint two independent grants, so
// each consumer genuinely holds its own. See resolveAudioPair.
type mediaPair struct {
	videoURL string // empty in music mode: there is no video to decode
	audioURL string // mpv
	tapURL   string // music mode only: the level tap's ffmpeg
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
func newTrackSession(track Track, l layout, pos float64, mute bool, mode ColorMode, glyph GlyphMode, music bool) (*trackSession, error) {
	var (
		pair mediaPair
		err  error
	)
	if music {
		pair, err = resolveAudioPair(track)
	} else {
		pair, err = resolveMedia(track, l.sourceH)
	}
	if err != nil {
		return nil, err
	}
	if music && pair.silent {
		return nil, fmt.Errorf("no audio stream in %s", track.Title)
	}
	s := &trackSession{
		track: track,
		pair:  pair,
		l:     l,
		mute:  mute,
		mode:  mode,
		glyph: glyph,
		music: music,
	}
	if err := s.start(pos); err != nil {
		return nil, err
	}
	return s, nil
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
	return resolveMedia(s.track, s.l.sourceH)
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
	tick := time.NewTicker(time.Second / time.Duration(musicFPS(s.l.cols, s.l.rows)))
	defer tick.Stop()
	for {
		select {
		case <-stop:
			return
		case <-tick.C:
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

// decode reads frames and hands them to the render loop.
//
// The send selects on the generation's stop channel so retiring a decoder can
// never leave this goroutine blocked on a channel nobody will read again, which
// is what a plain send into a full buffer would do.
//
// Each frame is copied out of the player's reusable buffer. The player recycles
// one buffer for speed, and with a buffered channel the decoder can be two frames
// ahead of the renderer, so handing over the pointer would let the decoder
// overwrite bytes the renderer is reading. A copy per frame is a few tens of KB
// at 12fps and removes the race entirely.
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
		buf := make([]byte, len(src))
		copy(buf, src)
		select {
		case out <- videoFrame{buf: buf, pos: pos}:
		case <-stop:
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
	if s.cur != nil {
		s.cur.Close()
		s.cur = nil
	}
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

// SetPaused freezes or resumes both children.
//
// SIGSTOP on both in the same instant is the whole mechanism, and it is the one
// flow.go already uses for a covered terminal. Freezing both clocks together is
// what preserves A/V sync exactly, which is why pausing does not go through
// mpv's pause property: mpv would stop on the sound card's schedule while the
// video pipe kept filling, and that backlog would surface as a burst on resume.
func (s *trackSession) SetPaused(paused bool) error {
	if s.cur == nil {
		return nil
	}
	s.paused = paused
	if paused {
		s.cur.pauseChildren()
		return nil
	}
	s.cur.resumeChildren()
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
	if restore, err := term.MakeRawVT(o.in, 0, 1); err == nil {
		defer restore()
	}
	fmt.Fprint(o.out, "\x1b[?25l")
	defer fmt.Fprint(o.out, "\x1b[?25h\x1b[2J\x1b[H")

	l := computeLayout(termCols, termRows, o.aspect, o.quality, chromeRowsFor(strip))

	// The glyph probe round-trips with the terminal, so it must happen after raw
	// mode is set and before ffmpeg starts: the frame height depends on it.
	glyph := GlyphCell
	if o.mode != ColorNone {
		glyph = resolveGlyph(o.glyphPref, o.in, o.out)
	}

	if index < 0 || index >= len(queue) {
		return OutcomeEnded
	}
	track := queue[index]

	sess, err := newTrackSession(track, l, 0, o.mute, o.mode, glyph, o.music)
	if err != nil {
		fmt.Fprintf(o.out, "\r\ncannot play %s: %v\r\n", truncate(track.Title, 60), err)
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
	if o.music {
		viz = prefs.makeViz()
		viz.Resize(l.cols, l.rows)
		grid = NewVizGrid(l.cols, l.rows, o.mode != ColorNone, vizPixPerCell(glyph))
		grid.SetPalette(paletteAt(prefs.palette))
	}

	// paintMusic draws one spectrum frame. Split out so the render loop's select
	// arm stays a single statement and the rebuild path can share it.
	paintMusic := func() error {
		// Push before Clear/Paint: the styles fold the new analysis into their
		// own state (waterfall captures a row, particles integrate), and doing it
		// in this order means the frame drawn is the frame analysed.
		if sess.tap != nil {
			f := sess.tap.Frame()
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
		grid.Clear()
		viz.Paint(grid)
		if o.mode == ColorNone {
			return renderer.Draw(grid.MonoFrame())
		}
		return renderer.Draw(grid.ColorFrame())
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
				stripViz.Push(sess.tap.Bands())
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
		case CmdStrip:
			strip = !strip
			// Recompute the layout and rebuild. In video mode this is mandatory
			// rather than cosmetic: the strip takes a row out of the grid, and the
			// grid is ffmpeg's scale target, so the decoder has to be told. In music
			// mode there is no video and nothing to rebuild, which is why the same
			// key is free there.
			l = computeLayout(termCols, termRows, o.aspect, o.quality, chromeRowsFor(strip))
			if strip {
				stripViz = Visualizer{level: make([]float64, l.cols), peak: make([]float64, l.cols)}
			}
			if !o.music {
				sess.Resize(l, sess.Position())
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
			viz.Resize(l.cols, l.rows)
			viz.Reset()
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
			l = computeLayout(c, r, o.aspect, o.quality, chromeRowsFor(strip))
			sess.Resize(l, sess.Position())
			if o.music {
				// The visualizer has to be told, and so does the grid: both hold
				// cols*rows state. This is the case the plan flagged as the one
				// only a real terminal shows -- a style whose internal row count is
				// from before the resize paints out of bounds or leaves a band of
				// stale cells, and neither shows up in a unit test that never
				// resizes.
				viz.Resize(l.cols, l.rows)
				grid = NewVizGrid(l.cols, l.rows, o.mode != ColorNone, vizPixPerCell(glyph))
				grid.SetPalette(paletteAt(prefs.palette))
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
			if o.music {
				if err := paintMusic(); err != nil {
					return OutcomeError
				}
			} else if err := renderer.Draw(fr.buf); err != nil {
				return OutcomeError
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
