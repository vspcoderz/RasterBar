package main

// Transport: the playback state machine and the decoder/render split.
//
// This is the layer that makes the thing a player rather than a one-shot
// viewer. Before it, playback was a single blocking loop — SyncPlayer.Next()
// parked in io.ReadFull on an -re paced pipe and the only way out was a frame
// arriving, EOF, or 'q'. There was nowhere for a keypress to land even if one
// had been read.
//
// Two ideas carry the whole file:
//
//  1. Decode runs in its own goroutine feeding a buffered channel. The render
//     loop selects over {frame, key, resize, quit, track-end} and never blocks,
//     so transport keys have somewhere to go. Channel capacity 2 is the existing
//     backpressure: when rendering falls behind, ffmpeg blocks on write, exactly
//     as it did when the loop was synchronous.
//
//  2. Pause is the stall handler, promoted. flow.go already SIGSTOPs both
//     children in the same instant so their clocks freeze together and A/V sync
//     survives a covered window. That is precisely what pausing is. No new
//     mechanism, no new failure mode.

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"
)

// Cmd is one transport keypress, decoded from the keyboard by the render loop
// and handed to the state machine. Keeping the state machine on actions rather
// than on key bytes is what lets it be tested without a terminal.
type Cmd int

const (
	CmdNone Cmd = iota
	CmdTogglePause
	CmdSeekBack // -seekStep
	CmdSeekFwd  // +seekStep
	CmdNext
	CmdPrev
	CmdVolUp
	CmdVolDown
	CmdQuit
	CmdSeekBackFine // -fineStep
	CmdSeekFwdFine  // +fineStep
	CmdSeekBackLong // -longStep
	CmdSeekFwdLong  // +longStep
	CmdChapNext
	CmdChapPrev
	// CmdPrompt opens the jump-to-time prompt. It is a command rather than a
	// render-loop special case so that the key table and the transport stay in
	// one place; what happens once it is open is the loop's business.
	CmdPrompt
	// Visualizer keys. These carry no state of their own: Player does not know
	// what a visualizer is, so they are passed straight through to the render
	// loop, which does. They are still Cmds rather than raw bytes because the key
	// table and the decoder have to agree on which byte means what, and splitting
	// that across two places is how a key ends up half-wired.
	CmdVizNext
	CmdVizPrev
	CmdPalette
	// CmdStrip toggles the spectrum row under the video.
	//
	// The only key that changes the layout, and therefore the only one that has to
	// rebuild the decoder: the strip takes a row from the grid, and the grid is
	// ffmpeg's scale target. It also works in music mode, where it is free --
	// there is no video to give up a row to -- so the same key means "more
	// information" in both modes rather than "sometimes nothing".
	CmdStrip
	// CmdPaletteSelect is the base of a range: CmdPaletteSelect+n selects palette
	// n directly, for the digit keys.
	//
	// A range rather than ten constants because the digit *is* the payload and a
	// Cmd is a plain int, so the offset carries it with no new type and no struct.
	// The alternative -- ten constants and ten cases on each side -- is the same
	// ten mappings in twice the space, and one of them would eventually be wrong.
	//
	// The decoder and the render loop both derive the base from this constant, so
	// they cannot drift apart. See paletteDigit.
	CmdPaletteSelect
)

// paletteDigit maps a digit byte to a palette index, or -1 if it is not one.
//
// `1` through `9` are indices 0 through 8 and `0` is the last palette. That is
// the only ordering that keeps `mono` off the first keypress, and mono being last
// is deliberate (see palettes). It also reads the way people expect: ten keys,
// left to right, 1-9 then 0.
//
// A palette added past the tenth stays reachable through `c` but gets no digit of
// its own. Adding a key would mean taking one away from something.
func paletteDigit(c byte) int {
	switch {
	case c >= '1' && c <= '9':
		return int(c - '1')
	case c == '0':
		return len(palettes) - 1
	}
	return -1
}

// Three seek sizes, because one is never the right one.
//
// 10s is a spoken-word nudge. 1s is for the last few seconds of a jump, where
// "roughly right" is not right. 60s is for skipping a section you do not want,
// which with a single 10s step means twenty keypresses held down.
//
// `<` and `>` are the shifted forms of `,` and `.`, so all four cost one key
// and none of them collide: , and . are fine seek, < and > are long.
const (
	seekStep = 10
	fineStep = 1
	longStep = 60
)

// volumeStep is one press, in percent.
const volumeStep = 5

// Chapter is one entry of a track's chapter list.
type Chapter struct {
	Start float64
	End   float64
	Title string
}

// chapterTarget is where a chapter key should land, as a pure function of the
// list and the position.
//
// Both directions are strict comparisons with no tolerance, and that is the whole
// design. The obvious rule for "previous" — from inside a chapter go to its start,
// otherwise go to the one before it — is the one most players use, and it has a
// fatal property here: seeking to a chapter's start and then playing two seconds
// puts you *inside* it again, so the next press returns to the same start. You can
// mash the key for as long as you like and never leave the chapter. A key that
// cannot make progress is a dead key, so previous is always the chapter before
// the one you are in, full stop.
//
// The cost is that `[` cannot restart the chapter you are in. The fine seek and
// the jump prompt are how you do that, and saying so beats a key that lies.
//
// Neither direction falls off the end: next past the last chapter does nothing
// rather than wrapping to the start, and previous in or before the first restarts
// the track, which is the same choice CmdPrev makes at the head of the queue.
func chapterTarget(chaps []Chapter, pos float64, next bool) (float64, bool) {
	if len(chaps) == 0 {
		return 0, false
	}
	if next {
		for _, c := range chaps {
			if c.Start > pos {
				return c.Start, true
			}
		}
		return 0, false
	}
	// cur is the chapter containing pos, or -1 before the first one starts.
	cur := -1
	for i, c := range chaps {
		if c.Start <= pos {
			cur = i
			continue
		}
		break
	}
	if cur <= 0 {
		return 0, true // in or before the first chapter: restart
	}
	return chaps[cur-1].Start, true
}

// Outcome is why playback stopped. runASCII used to return a bare error, which
// could not distinguish "the user quit" from "the track ended", so the queue
// could never advance itself.
type Outcome int

const (
	// OutcomePlaying is the zero value so a Player that has not been told to
	// stop reads as still going.
	OutcomePlaying Outcome = iota
	OutcomeEnded           // the track finished and there is no next one
	OutcomeNext            // stop this track, play the next in the queue
	OutcomePrev
	OutcomeQuit
	OutcomeError
)

func (o Outcome) String() string {
	switch o {
	case OutcomeEnded:
		return "ended"
	case OutcomeNext:
		return "next"
	case OutcomePrev:
		return "prev"
	case OutcomeQuit:
		return "quit"
	case OutcomeError:
		return "error"
	}
	return "playing"
}

// media is the process-facing half of a playing track: the decoder on one side,
// mpv on the other. It is an interface so the transport rules above can be
// tested with no ffmpeg, no mpv and no network — see fakeMedia in main_test.go.
//
// Deliberately small. Every method here corresponds to something that genuinely
// crosses a process boundary; anything the state machine can decide on its own
// it decides on its own.
type media interface {
	Position() float64
	Duration() float64
	SetPaused(bool) error
	Seek(float64) error
	SetVolume(int) error
	Close() error
	// Chapters comes from the same request that already produced the streams
	// and the duration, so it is free. It is here rather than passed to
	// NewPlayer because the media backend is what resolved them.
	Chapters() []Chapter
}

// State is everything the HUD and the render loop need to know, read in one
// shot so they cannot observe a half-applied transition.
type State struct {
	Pos     float64
	Dur     float64
	Paused  bool
	Volume  int
	Index   int   // 0-based position in the queue
	Queue   int   // queue length
	Track   Track // current track
	Outcome Outcome
	// LastErr is the most recent transport failure, or nil.
	//
	// Transport actions are fire-and-forget from the key handler's point of
	// view, but a keypress is something the user just did, and silently ignoring
	// a failure — a seek that did not happen, a volume change mpv rejected —
	// reads as a broken program. It is surfaced here rather than returned so the
	// render loop can report it without every call site handling an error.
	LastErr error
	// Mark is where playback was before the last seek, and HasMark says whether
	// there is one worth drawing. The bar keeps it as a tick so a jump shows
	// both where it came from and where it landed — without that, a 40-minute
	// seek and a 4-second one look identical.
	Mark    float64
	HasMark bool
}

// Player owns the queue and the transport rules.
//
// It holds no goroutine and touches no process beyond the injected media, which
// is what keeps the whole of Phase 2 testable in-process.
type Player struct {
	queue []Track
	index int
	m     media

	paused  bool
	volume  int
	pos     float64
	outcome Outcome
	lastErr error

	// markPos/marked remember the position before the last seek. See State.Mark.
	markPos float64
	marked  bool
}

// NewPlayer builds a player over a queue, starting at index 0 of it.
func NewPlayer(queue []Track, m media) *Player {
	return NewPlayerAt(queue, 0, m)
}

// NewPlayerAt builds a player starting at a queue position.
//
// The index is clamped rather than trusted: it comes from navigation state, and
// an out-of-range value should start at the beginning rather than panic in a
// render loop.
func NewPlayerAt(queue []Track, index int, m media) *Player {
	if index < 0 || index >= len(queue) {
		index = 0
	}
	return &Player{queue: queue, index: index, m: m, volume: 100, outcome: OutcomePlaying}
}

// State returns a snapshot.
func (p *Player) State() State {
	dur := 0.0
	if p.m != nil {
		dur = p.m.Duration()
	}
	var trk Track
	if p.index >= 0 && p.index < len(p.queue) {
		trk = p.queue[p.index]
	}
	return State{
		Pos:     p.pos,
		Dur:     dur,
		Paused:  p.paused,
		Volume:  p.volume,
		Index:   p.index,
		Queue:   len(p.queue),
		Track:   trk,
		Outcome: p.outcome,
		LastErr: p.lastErr,
		Mark:    p.markPos,
		HasMark: p.marked,
	}
}

// ClearMark drops the pre-seek marker once the HUD has shown it long enough.
// Driven by the render loop's clock rather than one of its own, because the
// Player has no clock: its position comes from the media, not from time.
func (p *Player) ClearMark() { p.marked = false }

// JumpTo moves to an absolute position.
//
// A separate entry point from Do rather than a Cmd carrying a float, because a
// Cmd is a keypress and this comes from a parsed timestamp. The clamp lives in
// seekTo, so a jump past the end of the track lands on its last second instead
// of on nothing.
func (p *Player) JumpTo(target float64) { p.seekTo(target) }

// Chapters returns the current track's chapters, or nil when it has none.
func (p *Player) Chapters() []Chapter {
	if p.m == nil {
		return nil
	}
	return p.m.Chapters()
}

// apply runs a media call and records its failure instead of discarding it.
//
// See State.LastErr for why the error is kept rather than returned.
func (p *Player) apply(err error) {
	if err != nil {
		p.lastErr = err
	}
}

// ClearErr acknowledges a reported failure so it is shown once, not every frame.
func (p *Player) ClearErr() { p.lastErr = nil }

// Do applies one transport action.
//
// Pausing is applied to the media backend but the state is kept here too. A seek
// rebuilds both child processes, so anything that inferred "playing" from a
// running process would silently resume on the user's next seek.
func (p *Player) Do(c Cmd) {
	switch c {
	case CmdNone:
		return

	case CmdTogglePause:
		p.paused = !p.paused
		if p.m != nil {
			p.apply(p.m.SetPaused(p.paused))
		}

	case CmdSeekFwd:
		p.seekTo(p.pos + seekStep)
	case CmdSeekBack:
		p.seekTo(p.pos - seekStep)
	case CmdSeekFwdFine:
		p.seekTo(p.pos + fineStep)
	case CmdSeekBackFine:
		p.seekTo(p.pos - fineStep)
	case CmdSeekFwdLong:
		p.seekTo(p.pos + longStep)
	case CmdSeekBackLong:
		p.seekTo(p.pos - longStep)

	case CmdChapNext:
		if t, ok := chapterTarget(p.Chapters(), p.pos, true); ok {
			p.seekTo(t)
		}
	case CmdChapPrev:
		if t, ok := chapterTarget(p.Chapters(), p.pos, false); ok {
			p.seekTo(t)
		}

	case CmdNext:
		// Past the last result there is nothing to play. Stopping is honest;
		// wrapping to the top of a search result is not.
		if p.index+1 >= len(p.queue) {
			p.outcome = OutcomeEnded
			return
		}
		p.index++
		p.outcome = OutcomeNext

	case CmdPrev:
		if p.index > 0 {
			p.index--
			p.outcome = OutcomePrev
			return
		}
		// Already first: restart the track, which is what every other player
		// does and beats a dead key.
		p.seekTo(0)

	case CmdVolUp:
		p.setVolume(p.volume + volumeStep)
	case CmdVolDown:
		p.setVolume(p.volume - volumeStep)

	case CmdQuit:
		p.outcome = OutcomeQuit
	}
}

// seekTo moves to a clamped position and invalidates in-flight frames.
//
// Every position change goes through here, which is what lets the pre-seek
// marker be set in exactly one place. A seek that does not move — already at
// the start, or already at the end — leaves no marker: there is nothing to
// point back from, and a tick sitting under the playhead is just noise.
func (p *Player) seekTo(target float64) {
	if p.m != nil {
		if dur := p.m.Duration(); dur > 0 && target > dur {
			target = dur
		}
	}
	if target < 0 {
		target = 0
	}
	if target != p.pos {
		p.markPos = p.pos
		p.marked = true
	}
	p.pos = target
	if p.m != nil {
		p.apply(p.m.Seek(target))
	}
}

func (p *Player) setVolume(v int) {
	if v < 0 {
		v = 0
	}
	if v > 100 {
		v = 100
	}
	p.volume = v
	if p.m != nil {
		p.apply(p.m.SetVolume(v))
	}
}

// Tick reports the media clock and moves the displayed position with it.
//
// The clock is authoritative: position is reported, never predicted. There is
// deliberately no guard against a frame from before a seek, because a seek
// replaces the decoder and its channel outright rather than sharing them, so
// such a frame cannot arrive. Guarding anyway would be defending against a race
// that the structure makes impossible, and would only ever mask a real bug.
func (p *Player) Tick(mediaPos float64) {
	p.pos = mediaPos
}

// Done reports whether playback should stop.
func (p *Player) Done() bool { return p.outcome != OutcomePlaying }

// Close releases the media backend.
func (p *Player) Close() {
	if p.m != nil {
		p.apply(p.m.Close())
	}
}

// debugKeys traces raw key reads on stderr. Off by default: stderr is the
// same stream the renderer uses.
var debugKeys = os.Getenv("VSPZ_YT_CLI_DEBUG_KEYS") != ""

// seekTimeout bounds the mpv handshake after a seek. Seeking is user-initiated
// and expects feedback, so it gets a real wait rather than the 400ms the
// background drift corrector uses.
const seekTimeout = 2 * time.Second

// decodeStream parses as many complete commands out of pending as it can, and
// reports how many bytes it consumed.
//
// A terminal does NOT guarantee that an escape sequence arrives in one read. The
// whole reason term.MakeRawVT sets a read timeout is that the reader has to be able to
// ask "is there more of this sequence?" — and when it does, the ESC turns up in
// one read and "[C" in the next. A stateless per-read parser throws the arrow
// away in that case, which is exactly what it did: the seek keys did nothing and
// the symptom looked like "keys are not being delivered at all".
//
// So an incomplete trailing sequence is left unconsumed and the caller carries it
// into the next read. Only a genuinely lone ESC is ambiguous, and the caller
// resolves that on a read timeout.
func decodeStream(pending []byte) (cmds []Cmd, used int) {
	i := 0
	for i < len(pending) {
		cs, n, ok := decodeOne(pending[i:])
		if !ok {
			break
		}
		cmds = append(cmds, cs...)
		i += n
	}
	return cmds, i
}

// decodeOne pulls a single command off the front of pending.
//
// Split out from decodeStream because the render loop now decodes one command at
// a time rather than a whole read at once: it has to be able to stop at the `:`
// and hand the rest of the burst to the prompt. Decoding a whole read first is
// what made a prompt impossible — every byte had already been assigned a meaning
// before the loop got a chance to say otherwise, so `q` inside a timestamp
// would have quit the player.
//
// The result is a slice because one input can legitimately produce no command at
// all: a dropped escape is consumed without inventing a CmdNone for it, which is
// what the transport never had to see.
//
// ok=false means "this is the start of a sequence, give me more bytes", and used
// is 0 in that case so the caller keeps holding them.
func decodeOne(pending []byte) (cmds []Cmd, used int, ok bool) {
	if len(pending) == 0 {
		return nil, 0, false
	}
	c := pending[0]
	if c != 0x1b {
		return []Cmd{cmdForByte(c)}, 1, true
	}
	// ESC on its own: could be the start of a sequence, or the Escape key.
	if len(pending) < 2 {
		return nil, 0, false
	}
	if pending[1] != '[' {
		// Not a CSI. Dropping the ESC keeps the key after it working; holding it
		// would eat that key, which is worse than losing the Escape press.
		return nil, 1, true
	}
	// A CSI is ESC [ parameters final, and the final byte is the first in
	// 0x40-0x7E. Scan to it rather than assuming three bytes.
	//
	// Assuming three is what let a bracketed-paste marker reach the key table:
	// ESC [ 200 ~ is six bytes, so "200~" was decoded as ordinary keystrokes.
	// That was harmless while every unmapped byte meant CmdNone. It stopped being
	// harmless the moment a digit became a palette select, because pasting
	// anything at all would have silently changed the palette.
	const csiLimit = 32
	end := -1
	for j := 2; j < len(pending) && j < csiLimit; j++ {
		if pending[j] >= 0x40 && pending[j] <= 0x7e {
			end = j
			break
		}
	}
	if end < 0 {
		if len(pending) >= csiLimit {
			// Truncated or malformed beyond any sequence a terminal really sends.
			// Swallow the cap rather than hold the bytes waiting for a final byte
			// that is never coming, which would wedge the decoder.
			return []Cmd{CmdNone}, csiLimit, true
		}
		return nil, 0, false // the rest of the sequence has not arrived
	}
	if end == 2 {
		switch pending[2] {
		case 'C':
			return []Cmd{CmdSeekFwd}, 3, true
		case 'D':
			return []Cmd{CmdSeekBack}, 3, true
		}
	}
	// Up/down, or a sequence we do not use. Swallowed rather than guessed at, so
	// a mouse report or a function key cannot be mistaken for a seek — but still
	// reported as one consumed command, so every byte the decoder eats is
	// accounted for by exactly one entry.
	return []Cmd{CmdNone}, end + 1, true
}

// cmdForByte maps one non-escape byte to a command.
func cmdForByte(c byte) Cmd {
	switch c {
	case ' ':
		return CmdTogglePause
	case 'q', 'Q', 0x03:
		return CmdQuit
	case 'n':
		return CmdNext
	case 'p':
		return CmdPrev
	case '+', '=':
		return CmdVolUp
	case '-', '_':
		return CmdVolDown
	case '.':
		return CmdSeekFwdFine
	case ',':
		return CmdSeekBackFine
	case '>':
		return CmdSeekFwdLong
	case '<':
		return CmdSeekBackLong
	case ']':
		return CmdChapNext
	case '[':
		return CmdChapPrev
	case ':':
		return CmdPrompt
	case 'v':
		return CmdVizNext
	case 'V':
		return CmdVizPrev
	case 'c':
		return CmdPalette
	case 's':
		return CmdStrip
	default:
		// Digits are checked last and only if nothing above claimed the byte.
		// They were free here: the browse list's 1-9 is a different router
		// (tui.go), and the jump-to-time prompt is routed before this table is
		// consulted at all.
		if n := paletteDigit(c); n >= 0 {
			return CmdPaletteSelect + Cmd(n)
		}
	}
	return CmdNone
}

// decodeKeys maps a complete buffer of terminal bytes to transport commands.
//
// Thin wrapper over decodeStream for callers holding all the bytes at once;
// anything left over is an incomplete sequence and yields nothing. Only one
// multi-byte sequence is recognised — ESC [ <final> — because the arrows are the
// only keys that need it. Everything else is swallowed rather than mapped, so an
// unhandled sequence (a mouse report, a function key, a bracketed-paste marker)
// cannot be mistaken for a seek.
func decodeKeys(b []byte) []Cmd {
	cmds, _ := decodeStream(b)
	return cmds
}

// readKeys pumps raw terminal bytes into a channel until in closes.
//
// It is a byte pump and nothing else. Decoding used to happen here, in this
// goroutine, which made a text prompt impossible: the bytes of a timestamp were
// assigned their transport meanings before the render loop had any way to say
// "a prompt is open", so a `q` typed into it quit the player and `-30` turned
// the volume down three times. Whatever the bytes mean is now decided by the
// one consumer, which is the only place that knows.
//
// An empty chunk is a tick, not an event: it is the VTIME read timeout expiring,
// and it is what tells the consumer that an escape sequence which started will
// not be completed.
//
// A no-data read is NOT end of input. With VMIN=0 the kernel reports "nothing
// available" as EAGAIN or as a zero-length read depending on how Go ends up
// waiting, and treating either as EOF would quit playback the instant it
// started. Only a real error closes the channel.
//
// It deliberately does NOT read from the *os.File it is handed. Verified by
// measurement: once the browse list has wrapped os.Stdin in a bufio.Reader and
// read from it, a later direct read of os.Stdin blocks forever, and the bytes sit
// in the pipe unread — every transport key silently dead. Reopening /dev/stdin
// gives a fresh handle that reads them immediately, so the two readers stop
// sharing one *os.File.
//
// The underlying reason os.Stdin gets stuck is not fully explained (it looks like
// the runtime poller leaving fd 0 registered in a state a second handle
// sidesteps). The fix is verified by measurement, not understood, and that is
// recorded here so nobody "simplifies" it back to reading the passed-in file.
func readKeys(in *os.File, out chan<- []byte) {
	src := in
	if alt, err := os.Open("/dev/stdin"); err == nil {
		src = alt
		defer alt.Close()
	}
	buf := make([]byte, 64)
	for {
		n, err := src.Read(buf)
		if n > 0 {
			if debugKeys {
				fmt.Fprintf(os.Stderr, "keys: %d bytes %q\n", n, buf[:n])
			}
			// Copied out because buf is reused on the next read, and the
			// consumer holds this chunk until it has drained it.
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			out <- chunk
			continue
		}
		if err == nil {
			out <- nil // timed-out empty read: the consumer's flush signal
			continue
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
			continue
		}
		close(out)
		return
	}
}

// keyRouter turns terminal bytes into transport commands, handing them to the
// prompt first while it is open.
// It exists as a type rather than as code inside the render loop so the routing
// can be tested directly. That is the whole point: the bug this design exists to
// prevent is a burst-handling bug — a `:` and the timestamp that follows it
// routinely arrive in one read — and a rule that can only be exercised by
// pressing keys at a terminal is a rule that will regress.
type keyRouter struct {
	buf []byte
	pr  prompt
}

// keyResult is what one chunk of input did.
type keyResult struct {
	// cmds are transport commands, in order. Empty while the prompt has the bytes.
	cmds []Cmd
	// submit is set when the field was closed with Enter, and valid says whether
	// its contents were a timestamp. Both false when the field is still open.
	submit bool
	valid  bool
	target float64
	// cancel is set when the field was dismissed with Escape or ctrl-c.
	cancel bool
	// open reports whether the field is still on screen afterwards, which decides
	// whether the caller repaints it.
	open bool
}

// feed routes one chunk of terminal bytes.
//
// pos and dur are the current position and duration, needed to resolve a
// timestamp as it is submitted rather than when the field opened — `50%` typed
// while paused should land on half of the track, not half of where it was when
// the prompt appeared.
func (r *keyRouter) feed(chunk []byte, pos, dur float64) keyResult {
	// An empty chunk is the read timeout expiring, which is the only signal that
	// an escape sequence which started is never going to complete.
	if len(chunk) == 0 {
		return r.flush()
	}

	var res keyResult
	r.buf = append(r.buf, chunk...)
	for len(r.buf) > 0 {
		if r.pr.open {
			used, act := r.pr.consume(r.buf)
			r.buf = append(r.buf[:0], r.buf[used:]...)
			switch act {
			case promptSubmit:
				res.submit = true
				res.target, res.valid = parseTimestamp(r.pr.text(), pos, dur)
			case promptCancel:
				res.cancel = true
			}
			if r.pr.open {
				continue // still typing: the rest of the chunk is text
			}
			// The field just closed. Anything after it is transport input again,
			// which is what makes `:1:30<CR>q` quit and `:1:30<CR>n` skip a track.
		}
		cmds, used, complete := decodeOne(r.buf)
		if !complete {
			break // an escape sequence still arriving
		}
		r.buf = append(r.buf[:0], r.buf[used:]...)
		for _, c := range cmds {
			if c == CmdPrompt {
				// Opened here and not handed to the caller. This is the whole
				// reason the router exists: the caller applies commands after
				// feed has returned, so a `:` that went out as a command would
				// still have the rest of its own burst decoded as transport keys
				// before the field existed to claim them.
				r.pr.start()
				continue
			}
			res.cmds = append(res.cmds, c)
		}
	}
	res.open = r.pr.open
	return res
}

// flush decodes bytes left over from an escape sequence that was never
// completed. Without it a lone ESC — or a mouse report cut in half — would sit in
// the buffer for the rest of the track.
func (r *keyRouter) flush() keyResult {
	var res keyResult
	if len(r.buf) > 0 {
		cmds, _ := decodeStream(r.buf)
		r.buf = r.buf[:0]
		res.cmds = append(res.cmds, cmds...)
	}
	res.open = r.pr.open
	return res
}

// The jump-to-time prompt: a one-line text field drawn over the video.
//
// It consumes terminal bytes itself instead of translating them into commands,
// which is the whole reason it can exist. Everything typed here is text —
// including the keys that mean quit, next and volume down — and the only way to
// guarantee that is for the field to get first refusal on the bytes.
type prompt struct {
	buf  []byte
	open bool
}

// promptMax is how many characters the field accepts.
//
// Bounded rather than scrolling: the longest sensible entry is `1h2m3s` and
// anything past that is a typo, so the cap costs nothing and keeps a held-down
// key from running the field off the side of the overlay.
const promptMax = 16

type promptAction int

const (
	promptNone promptAction = iota
	promptSubmit
	promptCancel
)

// start opens the field with an empty buffer.
func (p *prompt) start() {
	p.buf = p.buf[:0]
	p.open = true
}

// stop closes the field and leaves its text readable.
//
// Deliberately not clearing the buffer: the caller resolves what was typed after
// the field has closed, so wiping it here would submit an empty string. start is
// what resets it, and start runs before anything can be typed.
func (p *prompt) stop() { p.open = false }

func (p *prompt) text() string { return string(p.buf) }

// consume takes a prefix of chunk and reports what the user did.
//
// Returning the count rather than a single verdict is what makes a burst work:
// `:1:30<CR>` arrives as one chunk, and the bytes after the Enter have to go
// back to the transport rather than being swallowed by a field that has already
// closed.
func (p *prompt) consume(chunk []byte) (int, promptAction) {
	for i := 0; i < len(chunk); {
		c := chunk[i]
		switch c {
		case 0x1b:
			// Escape dismisses. An arrow key is ESC [ <final> and must not
			// dismiss the field just because the user reached for a seek that
			// does not apply here, so a complete sequence is swallowed whole.
			if i+2 < len(chunk) && chunk[i+1] == '[' {
				i += 3
				continue
			}
			p.stop()
			return i + 1, promptCancel

		case '\r', '\n':
			p.stop()
			return i + 1, promptSubmit

		case 0x03: // ctrl-c
			p.stop()
			return i + 1, promptCancel

		case 0x7f, 0x08: // backspace, and the ctrl-h some terminals send
			if len(p.buf) > 0 {
				p.buf = p.buf[:len(p.buf)-1]
			}
			i++

		case 0x15: // ctrl-u: clear the line
			p.buf = p.buf[:0]
			i++

		default:
			// Printable ASCII only. A control byte or a UTF-8 continuation is
			// dropped rather than stored, because half a multi-byte rune in a
			// field that only takes digits and separators is garbage either way.
			if c >= 0x20 && c < 0x7f && len(p.buf) < promptMax {
				p.buf = append(p.buf, c)
			}
			i++
		}
	}
	return len(chunk), promptNone
}

// blockCursor is U+2588 FULL BLOCK. The same glyph resolveGlyph probes with, so
// on a terminal that answered that probe it is known to be one cell wide — and
// it is the widest thing usable as a cursor, which matters because anything
// narrower would shift the text after it on every keystroke.
const blockCursor = '█'

// line is the field itself, with a block cursor so it reads as an input.
func (p *prompt) line() string {
	return " jump to " + string(blockCursor) + " " + p.text()
}

// status is the second row: where the text lands, or why it cannot.
//
// Showing the resolved position while the input is still editable is the point
// of the row. Typing `1:3` and landing at 1:03 instead of 1:30 is the kind of
// surprise a player should refuse to spring, and the only way to know is to show
// the answer before the jump rather than after it.
func (p *prompt) status(pos, dur float64) string {
	const keys = "   ·   enter jump  ·   esc cancel"
	if p.text() == "" {
		return "1:30  ·  90  ·  +30  ·  50%" + keys
	}
	target, ok := parseTimestamp(p.text(), pos, dur)
	if !ok {
		return "not a timestamp" + keys
	}
	landing := "→ " + formatClock(target)
	if dur > 0 {
		landing += " / " + formatClock(dur)
	}
	return landing + keys
}
