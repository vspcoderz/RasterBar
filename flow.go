package main

import (
	"os"
	"syscall"
	"time"
)

// Flow control for a terminal that is not being read.
//
// Backgrounding the window (or covering it, or switching Hyprland workspaces)
// stops the terminal draining its buffer. Writes to the tty then block, ffmpeg
// blocks writing video, and mpv -- which writes straight to PipeWire -- keeps
// playing audio, so the two drift apart. An earlier attempt rebuilt both
// processes after a 3s gap, which was wrong twice over: it was expensive, and it
// kept writing to a terminal nobody was reading, painting ASCII over whatever
// was actually on screen.
//
// The fix is to detect that the tty has stopped accepting output and suspend
// BOTH children at the same instant. They run on separate clocks, but stopping
// them together freezes both clocks, so the offset does not grow and no state is
// lost. Resuming continues in sync.

// fdWritable reports whether f can accept a write without blocking.
//
// A zero timeout makes this a poll, not a wait: the caller decides what to do
// when the answer is no.
func fdWritable(f *os.File) bool {
	fd := int(f.Fd())
	if fd < 0 || fd >= 64*64 {
		// Beyond the FdSet bitmap, or not a real fd. Assume writable rather than
		// stalling playback on a technicality.
		return true
	}
	var set syscall.FdSet
	set.Bits[fd/64] |= 1 << (uint(fd) % 64)
	var tv syscall.Timeval // zero: return immediately
	n, err := syscall.Select(fd+1, nil, &set, nil, &tv)
	if err != nil {
		// EINTR or anything unexpected: do not let that stall playback.
		return true
	}
	return n > 0
}

// pauseChildren suspends every child process.
func (s *SyncPlayer) pauseChildren() {
	if s.ff != nil && s.ff.Process != nil {
		_ = s.ff.Process.Signal(syscall.SIGSTOP)
	}
	if s.audio != nil && s.audio.Process != nil {
		_ = s.audio.Process.Signal(syscall.SIGSTOP)
	}
	s.paused = true
}

// resumeChildren continues every child process.
func (s *SyncPlayer) resumeChildren() {
	if s.ff != nil && s.ff.Process != nil {
		_ = s.ff.Process.Signal(syscall.SIGCONT)
	}
	if s.audio != nil && s.audio.Process != nil {
		_ = s.audio.Process.Signal(syscall.SIGCONT)
	}
	s.paused = false
}

// Paused reports whether the children are currently suspended.
func (s *SyncPlayer) Paused() bool { return s.paused }

// awaitDrain blocks until the terminal accepts output again, or until the key
// channel closes (the user quit, or stdin reached EOF). Returns false if we gave
// up.
//
// Polled rather than parked on the fd, and it consumes keys as it goes, because
// while the children are suspended the render loop is not selecting: without
// this the keystrokes that arrive during a stall would sit in the buffer and be
// replayed as a burst on resume.
func awaitDrain(out *os.File, player *SyncPlayer, keys <-chan Cmd, poll time.Duration) bool {
	if poll <= 0 {
		poll = 120 * time.Millisecond
	}
	for {
		select {
		case _, ok := <-keys:
			if !ok {
				return false
			}
			// Any other keypress while suspended is dropped: acting on it now
			// would mean seeking a decoder that is stopped.
		default:
		}
		if fdWritable(out) {
			return true
		}
		time.Sleep(poll)
	}
}
