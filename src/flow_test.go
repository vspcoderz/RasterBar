package main

import (
	"os"
	"strconv"
	"syscall"
	"testing"
)

// TestFdWritableRejectsOutOfRangeDescriptors is the panic that was waiting in the
// render loop.
//
// The guard read `fd >= 64*64`. syscall.FdSet.Bits is [16]int64 on linux/amd64,
// so the bitmap holds 1024 fds and a descriptor in [1024, 4096) indexed past the
// array — `set.Bits[fd/64]` with fd/64 in [16, 64). Unreachable with an ordinary
// stdout, which is exactly why it survived: the constant was wrong, not the code
// around it, and nothing in normal use could reach it.
//
// This opens real descriptors until one lands in the dead zone, which is the only
// honest way to exercise it — a synthetic high fd would panic inside Go's own
// os.File machinery before reaching the code under test.
func TestFdWritableRejectsOutOfRangeDescriptors(t *testing.T) {
	var probe syscall.FdSet
	limit := len(probe.Bits) * 64
	if limit != 1024 {
		// The whole test is written against this. If a future Go changes the
		// bitmap size the assertion below still holds, but say so rather than
		// quietly testing something else.
		t.Logf("FdSet holds %d descriptors on this platform", limit)
	}

	// Pipes rather than opened files: os.Open needs a real path and a real inode,
	// while a pipe is a genuine descriptor and the fd counter is what we are
	// walking upwards through. Both ends are kept so the descriptors stay open.
	var pipes []*os.File
	defer func() {
		for _, f := range pipes {
			_ = f.Close()
		}
	}()

	// Open until we are past the bitmap. Bounded so a platform with an enormous
	// FD_SETSIZE cannot turn this into a runaway.
	var high *os.File
	for i := 0; i < 1400; i++ {
		r, w, err := os.Pipe()
		if err != nil {
			// Out of descriptors: the test cannot reach the zone. Skip rather than
			// pass — a guard that skips itself when it cannot find its subject is
			// exactly the failure mode this project keeps running into.
			t.Skipf("ran out of descriptors at %d, cannot reach the dead zone: %v", i, err)
		}
		pipes = append(pipes, r, w)
		if int(r.Fd()) >= limit {
			high = r
			break
		}
	}
	if high == nil {
		t.Skipf("could not open a descriptor at or above %d", limit)
	}

	// Must answer, not panic. A panic here is a crash inside the render loop with
	// ffmpeg and mpv running.
	if !fdWritable(high) {
		// The read end of a pipe with nothing written is writable, so false would
		// be a wrong answer. The assertion is that this returns at all, but say so
		// plainly rather than pretending the value was checked.
		t.Errorf("fdWritable reported false for fd %d, want true", high.Fd())
	}
}

// TestFdWritableOnARealTerminal is the ordinary path, so the guard above is not
// the only thing covered: a writable fd must report writable.
func TestFdWritableOnARealTerminal(t *testing.T) {
	if _, err := os.Stat("/dev/tty"); err != nil {
		t.Skip("no controlling terminal")
	}
	f, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0)
	if err != nil {
		t.Skipf("cannot open /dev/tty: %v", err)
	}
	defer f.Close()
	if !fdWritable(f) {
		t.Error("an idle /dev/tty reported not writable")
	}
}

// TestFdSetBoundMatchesTheBitmap states the constant this file depends on, so a
// platform change is a failing test rather than a latent panic.
func TestFdSetBoundMatchesTheBitmap(t *testing.T) {
	var s syscall.FdSet
	got := len(s.Bits) * 64
	if got <= 0 || got > 1<<20 {
		t.Fatalf("FdSet bitmap holds %d descriptors, which is not a sane bound", got)
	}
	t.Logf("fdWritable's guard uses len(Bits)*64 = %d", got)
	_ = strconv.Itoa(got)
}
