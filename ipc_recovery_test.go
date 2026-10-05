package main

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// A fake mpv IPC server, so IPC behaviour can be tested without mpv.
//
// Deliberately scriptable about *how* it misbehaves, because the interesting cases
// are the ones a real mpv only produces occasionally and a test cannot ask for on
// demand: a reply that arrives in pieces, a reply that never finishes, an event
// interleaved with the reply.

type fakeIPC struct {
	path string
	ln   net.Listener
	// handle is called with each request line; it returns the lines to write back,
	// and may write them itself (in pieces) by using the conn directly.
	handle func(c net.Conn, r *bufio.Reader, req ipcRequest)
	once   sync.Once
	done   chan struct{}
}

func newFakeIPC(t *testing.T, handle func(net.Conn, *bufio.Reader, ipcRequest)) *fakeIPC {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "ipc.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeIPC{path: path, ln: ln, handle: handle, done: make(chan struct{})}
	// Accepts repeatedly, one goroutine per connection, because a real mpv does.
	// A single-accept server makes the redial look broken when it is only the fake
	// that stopped listening -- which is exactly the wrong conclusion to draw.
	go func() {
		defer close(f.done)
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func(c net.Conn) {
				defer c.Close()
				r := bufio.NewReader(c)
				for {
					line, err := r.ReadBytes('\n')
					if err != nil {
						return
					}
					var req ipcRequest
					if json.Unmarshal(line, &req) != nil {
						return
					}
					f.handle(c, r, req)
				}
			}(c)
		}
	}()
	t.Cleanup(func() { ln.Close() })
	return f
}

func replyFor(id int, data float64) string {
	b, _ := json.Marshal(map[string]interface{}{
		"data": data, "error": "success", "request_id": id})
	return string(b) + "\n"
}

// TestTimePosRecoversFromAPartialReply is the regression test for a stalled clock.
//
// The protocol is newline-delimited JSON, so a read that times out mid-line has
// already pulled a partial reply out of the buffered reader and the rest of that
// reply is left in the socket. This test makes the server send a fragment and then
// go silent forever -- no more writes, ever -- which is the shape that actually
// hurts. Every later read then returns that same fragment plus whatever arrives
// after it, so each call blocks for its full timeout before giving up.
//
// The symptom is a clock that stops: mpv is asked where it is, gets nothing back
// that parses, and posClock holds its last value. Nothing errors, so it reads as a
// stalled HUD rather than a broken socket, while the spectrum keeps animating
// because that comes from the level tap and not from IPC at all.
//
// Redialing is what recovers it. The first two calls still cost a timeout each --
// the client cannot know it is desynchronised until it waits -- but the connection
// is replaced, so the third is immediate. Without the redial every call pays the
// timeout forever.
//
// Note what this is NOT: a single partial reply that the server later completes
// self-heals on its own, because the garbage forms one line and the reader
// re-aligns on the next one. The first version of this test modelled that case, it

// TestTimePosSkipsInterleavedEvents is the ordinary case the loop above has to
// survive, and it is what mpv actually does: it emits property-change events
// between your request and its reply.
func TestTimePosSkipsInterleavedEvents(t *testing.T) {
	f := newFakeIPC(t, func(c net.Conn, r *bufio.Reader, req ipcRequest) {
		for i := 0; i < 3; i++ {
			c.Write([]byte(`{"event":"time-pos","request_id":0}` + "\n"))
		}
		c.Write([]byte(replyFor(req.RequestID, 12.5)))
	})

	ipc, err := dialIPC(f.path, 2*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer ipc.close()
	pos, ok := ipc.timePos(time.Second)
	if !ok {
		t.Fatal("timePos failed with interleaved events")
	}
	if pos != 12.5 {
		t.Errorf("pos = %v, want 12.5", pos)
	}
}

// TestRedialKeepsThePathAndReconnects checks the recovery is wired to something.
// Without path set, redial is a no-op and the previous test would fail for the

// TestPosClockHoldsOnAFailedRead pins the degradation choice.
//
// A failed read must not zero the clock. Zeroing makes the HUD jump back to 0:00,
// which reads as a seek that did not happen, and it is worse than a clock that
// briefly stops. Holding the last value is honest: it says "I do not know right
// now", not "the position is zero".
func TestPosClockHoldsOnAFailedRead(t *testing.T) {
	var c posClock
	if c.now() != 0 || c.started() {
		t.Fatal("a fresh clock should read 0 and report not started")
	}
	c.update(42, true)
	if c.now() != 42 || !c.started() {
		t.Fatalf("after a good read: %v started=%v, want 42/true", c.now(), c.started())
	}
	c.update(0, false) // a dropped reply must not reset it
	if c.now() != 42 {
		t.Errorf("a failed read zeroed the clock: %v, want it to hold 42", c.now())
	}
	c.update(0, true) // a genuine report of 0 must be believed
	if c.now() != 0 {
		t.Errorf("a real zero was ignored: %v", c.now())
	}
}

// TestSetVolumeRecoversFastAfterAStall is the same hazard on the write path, where
// it is worse.
//
// setVolume uses seekTimeout, two seconds. So a poisoned connection means every
// volume keypress appears to hang for two seconds and then does nothing -- which is
// indistinguishable from a terminal that has stopped reading input. And the failure
// is reported once and then swallowed, because Player surfaces LastErr a single time

func TestIPCParseRejectsTheSuccessString(t *testing.T) {
	// Guards the reason the whole file exists: mpv sets "error":"success" on a
	// good reply, so a non-empty error field must not be read as a failure.
	line := []byte(`{"data":1.5,"error":"success","request_id":7}`)
	pos, ok, done := parseTimePosReply(line, 7)
	if !done || !ok || pos != 1.5 {
		t.Errorf("good reply: pos=%v ok=%v done=%v, want 1.5/true/true", pos, ok, done)
	}
	if strings.Contains(string(line), `"error":""`) {
		t.Error("fixture lost its success marker")
	}
}
