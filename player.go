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
)

// seekStep is one arrow-key press, in seconds. Long enough to be useful on a
// spoken-word track, short enough to land inside a music video.
const seekStep = 10

// volumeStep is one press, in percent.
const volumeStep = 5

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
	}
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
func (p *Player) seekTo(target float64) {
	if p.m != nil {
		if dur := p.m.Duration(); dur > 0 && target > dur {
			target = dur
		}
	}
	if target < 0 {
		target = 0
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
// whole reason makeRawVT sets a read timeout is that the reader has to be able to
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
		c := pending[i]
		if c == 0x1b {
			// ESC on its own: could be the start of a sequence, or the Escape key.
			if i+1 >= len(pending) {
				break // incomplete: wait for more
			}
			if pending[i+1] != '[' {
				i++ // ESC followed by something else: drop the ESC, handle the rest
				continue
			}
			if i+2 >= len(pending) {
				break // ESC [ seen, final byte still to come
			}
			switch pending[i+2] {
			case 'C':
				cmds = append(cmds, CmdSeekFwd)
			case 'D':
				cmds = append(cmds, CmdSeekBack)
			default:
				cmds = append(cmds, CmdNone) // up/down, or a sequence we do not use
			}
			i += 3
			continue
		}
		cmds = append(cmds, cmdForByte(c))
		i++
	}
	return cmds, i
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

// readKeys pumps terminal input into a command channel until in closes.
//
// The read timeout in makeRawVT means this wakes regularly and cheaply instead of
// parking on a blocking read, which is what lets an arrow key arrive as a
// complete three-byte sequence.
//
// A no-data read is NOT end of input. With VMIN=0 the kernel reports "nothing
// available" as EAGAIN or as a zero-length read depending on how Go ends up
// waiting, and treating either as EOF would quit playback the instant it
// started. Only a real error closes the channel.
// readKeys pumps terminal input into a command channel until in closes.
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
//
// The VTIME read timeout from makeRawVT means this wakes regularly and cheaply
// instead of parking on a blocking read, which is what lets an arrow key arrive
// as a complete three-byte sequence.
func readKeys(in *os.File, out chan<- Cmd) {
	src := in
	if alt, err := os.Open("/dev/stdin"); err == nil {
		src = alt
		defer alt.Close()
	}
	buf := make([]byte, 64)
	// Bytes of an escape sequence that has started but not finished. Carried
	// across reads; see decodeStream.
	var pending []byte
	for {
		n, err := src.Read(buf)
		if debugKeys {
			fmt.Fprintf(os.Stderr, "keys: %d bytes %q pending=%q\n", n, buf[:n], pending)
		}
		if n > 0 {
			pending = append(pending, buf[:n]...)
			cmds, used := decodeStream(pending)
			for _, c := range cmds {
				out <- c
			}
			// Keep only the incomplete tail.
			pending = append(pending[:0], pending[used:]...)
			continue
		}
		if len(pending) > 0 {
			// A read that returned nothing means the timeout expired, so
			// whatever is pending is not going to be completed. Flush it rather
			// than hold a lone ESC forever.
			for _, c := range decodeKeys(pending) {
				out <- c
			}
			pending = pending[:0]
		}
		if err == nil {
			continue // spurious wakeup or a timed-out empty read
		}
		if errors.Is(err, syscall.EAGAIN) || errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EINTR) {
			continue
		}
		close(out)
		return
	}
}
