package main

import (
	"bufio"
	"net"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

// TestKeyRouterSplitArrowThroughThePrompt is the composition neither existing
// prompt test covered.
//
// TestPromptHoldsASplitArrowSequence drives prompt.consume alone, and
// TestKeyRouterSplitEscapeSurvivesThePrompt drives the router with the prompt
// CLOSED. The bug lived in the seam: consume prepends a held escape prefix to the
// caller's chunk and returned len of the *combined* slice, which the router then
// applied to the un-combined buffer. `:` followed by an arrow arriving as two
// reads ("\x1b[" then "C") made it return 3 against a 1-byte buffer —
// panic: slice bounds out of range [3:1], inside the render loop with ffmpeg and
// mpv still running.
func TestKeyRouterSplitArrowThroughThePrompt(t *testing.T) {
	var r keyRouter

	// Read one: `:` opens the field, "1:30" is typed, then an escape prefix that
	// stops mid-sequence. All one burst, which is what a real keypress run looks
	// like and is why the router has to hand the tail to the prompt itself.
	r.feed([]byte(":1:30\x1b["), 0, 100)
	if !r.pr.open {
		t.Fatal("the prompt closed on a partial escape sequence")
	}
	if got := r.pr.text(); got != "1:30" {
		t.Fatalf("typed text = %q, want %q", got, "1:30")
	}

	// Read two: the final byte completes the arrow. This used to panic.
	r.feed([]byte("C"), 0, 100)

	if !r.pr.open {
		t.Error("a completed arrow cancelled the prompt")
	}
	if got := r.pr.text(); got != "1:30" {
		t.Errorf("typed text = %q after the arrow, want it untouched", got)
	}
	if len(r.buf) != 0 {
		t.Errorf("router buffer = %q, want it drained", r.buf)
	}
}

// TestKeyRouterSplitEscapeConsumesExactlyTheCallersBytes is the invariant behind
// the fix, stated directly: `used` is a count against the slice the router passed,
// never against a longer slice consume assembled internally. Any violation slices
// out of range on the next line.
func TestKeyRouterSplitEscapeConsumesExactlyTheCallersBytes(t *testing.T) {
	for _, tc := range []struct {
		name     string
		first    string
		second   string
		wantOpen bool
	}{
		{"csi split after introducer", ":1:30\x1b[", "C", true},
		{"csi split after one param", ":1:30\x1b[1", ";5C", true},
		{"ss3 split", ":1:30\x1bO", "A", true},
		{"bare escape cancels", ":1:30\x1b", "", false},
		{"enter submits after a held prefix", ":1:30\x1b[", "C\r", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var r keyRouter
			r.feed([]byte(tc.first), 0, 100)
			if tc.second != "" {
				r.feed([]byte(tc.second), 0, 100)
			}
			if r.pr.open != tc.wantOpen {
				t.Errorf("prompt open = %v, want %v", r.pr.open, tc.wantOpen)
			}
		})
	}
}

// TestIPCIsSafeFromTwoGoroutines is the race that music mode has on every track.
//
// pumpMusic polls timePos on the render tick while the key handler calls
// setVolume for `+` and `-`. mpvIPC had no mutex: two goroutines incremented
// `next` (a lost update hands one request_id to two calls, and each reads the
// other's reply) and two goroutines ReadBytes on one bufio.Reader.
//
// The lock in mpvIPC is the fix; this is the test that makes removing it fail.
// `go test -race` is the whole point — without it the two goroutines interleave
// so rarely that a plain run goes green.
func TestIPCIsSafeFromTwoGoroutines(t *testing.T) {
	dir := t.TempDir()
	sock := filepath.Join(dir, "ipc.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	// Stands in for mpv: answers every request, whatever its id.
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		r := bufio.NewReader(c)
		for {
			if _, rerr := r.ReadBytes('\n'); rerr != nil {
				return
			}
			for id := 1; id <= 64; id++ {
				_, werr := c.Write([]byte(`{"error":"success","data":1.5,"request_id":` +
					strconv.Itoa(id) + "}\n"))
				if werr != nil {
					return
				}
			}
		}
	}()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	i := &mpvIPC{conn: conn, r: bufio.NewReader(conn)}
	defer i.close()

	stop := make(chan struct{})
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				_, _ = i.timePos(20 * time.Millisecond)
			}
		}
	}()
	for n := 0; n < 20; n++ {
		if err := i.setVolume(50); err != nil {
			t.Fatalf("setVolume: %v", err)
		}
	}
	close(stop)
	wg.Wait()

	// 20 setProperty calls means at least 20 ids, so a next below 20 is a lost
	// increment — the symptom a user sees as a volume key that did nothing. The
	// poll count is timing-dependent and deliberately not asserted on; -race is
	// what actually catches the interleaving.
	if i.next < 20 {
		t.Errorf("request ids issued = %d, want at least 20 (one per setVolume)", i.next)
	}
}
