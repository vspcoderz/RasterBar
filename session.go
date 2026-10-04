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
	"sync/atomic"
	"syscall"
	"time"
)

// mediaPair is one track's resolved streams and duration.
//
// The pair comes from a single `yt-dlp -J` call. Duration rides along in that
// same response, which is why the progress bar needs no second request and no
// ffprobe — probing a googlevideo URL burns its grant (see resolveMedia).
type mediaPair struct {
	videoURL string
	audioURL string
	dur      float64
	// chapters comes from the same response as the URLs and the duration, so
	// chapter navigation costs no extra request on either path.
	chapters []Chapter
	// silent is true when the source has no audio stream at all.
	//
	// Only ever set on the local path, where the probe already knows. YouTube
	// always has audio, so the zero value is the right default and the network
	// path needs no extra request to learn it.
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
func newTrackSession(track Track, l layout, pos float64, mute bool, mode ColorMode, glyph GlyphMode) (*trackSession, error) {
	pair, err := resolveMedia(track, l.sourceH)
	if err != nil {
		return nil, err
	}
	s := &trackSession{
		track: track,
		pair:  pair,
		l:     l,
		mute:  mute,
		mode:  mode,
		glyph: glyph,
	}
	if err := s.start(pos); err != nil {
		return nil, err
	}
	return s, nil
}

// start launches ffmpeg + mpv at a media offset and spawns the decoder.
func (s *trackSession) start(pos float64) error {
	p, err := startSyncPlayerAt(s.pair.videoURL, s.pair.audioURL, s.l.cols, s.l.rows, s.l.fps, s.mute, pos, s.mode, s.glyph)
	if err != nil {
		// A grant can expire between resolving and playing. One re-resolve is
		// worth it; a second failure is the track's problem, not ours. The same
		// source height is requested, because dropping it here would quietly
		// downgrade the stream mid-session.
		if pair, rerr := resolveMedia(s.track, s.l.sourceH); rerr == nil {
			s.pair = pair
			p, err = startSyncPlayerAt(s.pair.videoURL, s.pair.audioURL, s.l.cols, s.l.rows, s.l.fps, s.mute, pos, s.mode, s.glyph)
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
}

// playTrack plays one track and reports why it stopped.
//
// The queue lives outside: playTrack receives the whole queue plus the index to
// start at and returns OutcomeNext/OutcomePrev so the caller can move the index
// and call again. That keeps this function about one track and leaves queue
// navigation to whoever owns the list.
func playTrack(o playOpts, queue []Track, index int) Outcome {
	termCols, termRows := 0, 0
	if c, r, err := termSize(o.out); err == nil {
		termCols, termRows = c, r
	}
	if o.cols > 0 {
		termCols = o.cols
	}
	if o.rows > 0 {
		termRows = o.rows
	}

	// VTIME read so arrow keys arrive whole; see makeRawVT.
	if restore, err := makeRawVT(o.in, 0, 1); err == nil {
		defer restore()
	}
	fmt.Fprint(o.out, "\x1b[?25l")
	defer fmt.Fprint(o.out, "\x1b[?25h\x1b[2J\x1b[H")

	l := computeLayout(termCols, termRows, o.aspect, o.quality)

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

	sess, err := newTrackSession(track, l, 0, o.mute, o.mode, glyph)
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
	paintHUD := func(force bool) {
		st := pl.State()
		lines := hud{
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
		}.lines(l.cols)
		joined := lines[0] + "\n" + lines[1] + "\n" + lines[2]
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

	// applyCmd runs one decoded transport command and reports whether playback
	// should stop. Split out of the select so feedKeys can drive it while a
	// prompt is closing and the bytes after the closing key still count.
	applyCmd := func(cmd Cmd) Outcome {
		switch cmd {
		case CmdNone:
			return OutcomePlaying
		case CmdQuit:
			return OutcomeQuit
		case CmdTogglePause:
			userPaused = !userPaused
			pl.Do(cmd)
		default:
			pl.Do(cmd)
			if st := pl.State(); st.Outcome != OutcomePlaying {
				return st.Outcome
			}
		}
		// A keypress the user just made failed. Say so rather than letting it look
		// like a broken key.
		//
		// The SGR reset is not decoration. The colour renderer leaves the last
		// cell's colour in force, so without it this line is drawn in a colour
		// sampled from the video — which on a dark scene is dark on dark.
		if st := pl.State(); st.LastErr != nil {
			fmt.Fprintf(bw, "\x1b[0m\x1b[%d;1H%s", l.rows+3,
				fit("transport: "+st.LastErr.Error(), l.cols))
			_ = bw.Flush()
			pl.ClearErr()
			lastKey = time.Now()
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
			c, r, err := termSize(o.out)
			if err != nil || (c == l.cols && r == l.termRows) {
				continue
			}
			l = computeLayout(c, r, o.aspect, o.quality)
			sess.Resize(l, sess.Position())

		case fr, open := <-sess.frames:
			if !open {
				// The decoder hit end of file, but mpv may still be sounding the
				// tail. Returning here would cut the audio off early, so treat it
				// as "video finished" and let sess.audio decide, with a deadline
				// in case mpv never reports.
				if videoEnded == nil {
					videoEnded = time.After(audioTailGrace)
				}
				continue
			}
			sess.pos.Store(math.Float64bits(fr.pos))
			pl.Tick(fr.pos)
			if err := renderer.Draw(fr.buf); err != nil {
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
			// The track finished. Another one queued means keep going.
			return endOfTrack()

		case <-videoEnded:
			// The video finished and mpv has not reported within the grace period.
			// Its clock is the one the user hears, but it is not going to speak.
			return endOfTrack()
		}
	}
}

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
