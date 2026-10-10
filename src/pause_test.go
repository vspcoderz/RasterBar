package main

import (
	"bufio"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync"
	"syscall"
	"testing"
	"time"
)

// signalRecorder collects the signals a fake child was sent.
//
// A recorder rather than a count of booleans because the order and the exact
// signal are both part of the contract: sending SIGCONT to a paused child is what
// resumes it, and sending SIGSTOP twice is not idempotent in the way the tests
// want to pretend it is.
type signalRecorder struct {
	mu   sync.Mutex
	sigs []os.Signal
}

func (r *signalRecorder) sig(s os.Signal) error {
	r.mu.Lock()
	r.sigs = append(r.sigs, s)
	r.mu.Unlock()
	return nil
}

func (r *signalRecorder) got() []os.Signal {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]os.Signal(nil), r.sigs...)
}

func (r *signalRecorder) reset() {
	r.mu.Lock()
	r.sigs = nil
	r.mu.Unlock()
}

func wantSignals(t *testing.T, r *signalRecorder, want ...os.Signal) {
	t.Helper()
	got := r.got()
	if len(got) != len(want) {
		t.Fatalf("signals = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("signals = %v, want %v", got, want)
		}
	}
}

// TestPauseReachesEveryChildClock is the bug.
//
// A session in music mode runs three clocks: mpv (inside SyncPlayer), the level
// tap's ffmpeg and, with the split pane up, the pane's ffmpeg. pauseChildren only
// ever knew about two fields and music mode has one of them nil, so the tap and
// the pane ran straight through a pause.
//
// The two symptoms are a spectrum and a video animating against a stopped sound,
// and — the one that cannot be undone — the tap ending the pause permanently ahead
// of mpv by the pause's length. checkSync cannot fix it: it compares mpv against a
// frame counter, and music mode has no frame counter.
func TestPauseReachesEveryChildClock(t *testing.T) {
	var tapRec, paneRec signalRecorder

	tap := &LevelTap{sig: tapRec.sig}
	pane := newVideoPane(4, 4, ColorNone, GlyphCell)
	pane.sig = paneRec.sig

	sess := &trackSession{
		cur:  &SyncPlayer{},
		tap:  tap,
		pane: pane,
	}

	if err := sess.SetPaused(true); err != nil {
		t.Fatalf("SetPaused(true): %v", err)
	}
	wantSignals(t, &tapRec, syscall.SIGSTOP)
	wantSignals(t, &paneRec, syscall.SIGSTOP)

	if err := sess.SetPaused(false); err != nil {
		t.Fatalf("SetPaused(false): %v", err)
	}
	wantSignals(t, &tapRec, syscall.SIGSTOP, syscall.SIGCONT)
	wantSignals(t, &paneRec, syscall.SIGSTOP, syscall.SIGCONT)

	if sess.cur.Paused() {
		t.Error("cur still reports paused after resume")
	}
	if sess.paused {
		t.Error("session still reports paused after resume")
	}
}

// TestVideoModeStripTapPausesToo is the same defect in the place nobody reported:
// the `s` strip has its own LevelTap under a video, and it was sliding under a
// frozen picture for the life of the project. Same field, so the test is the same
// one minus the pane.
func TestVideoModeStripTapPausesToo(t *testing.T) {
	var tapRec signalRecorder
	sess := &trackSession{
		cur:     &SyncPlayer{},
		tap:     &LevelTap{sig: tapRec.sig},
		wantTap: true,
		music:   false,
	}
	if err := sess.SetPaused(true); err != nil {
		t.Fatalf("SetPaused(true): %v", err)
	}
	wantSignals(t, &tapRec, syscall.SIGSTOP)
}

// TestPauseSurvivesMissingClocks is the nil-panic guard.
//
// A pause reaches a session whose children are any mix of the three — a silent
// track has no tap, a track with the split off has no pane, and a pause that
// arrives after a seek can land between a tap being retired and its replacement
// being started. Reaching for a nil there would turn a keypress into a crash in
// the render loop.
func TestPauseSurvivesMissingClocks(t *testing.T) {
	cases := []struct {
		name string
		sess *trackSession
	}{
		{"no tap, no pane", &trackSession{cur: &SyncPlayer{}}},
		{"nil tap", &trackSession{cur: &SyncPlayer{}, tap: (*LevelTap)(nil)}},
		{"nil pane", &trackSession{cur: &SyncPlayer{}, pane: (*videoPane)(nil)}},
		{"tap with no process", &trackSession{cur: &SyncPlayer{}, tap: &LevelTap{}}},
		{"pane with no tap", &trackSession{cur: &SyncPlayer{}, pane: newVideoPane(2, 2, ColorNone, GlyphCell)}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.sess.SetPaused(true); err != nil {
				t.Fatalf("SetPaused(true): %v", err)
			}
			if err := tc.sess.SetPaused(false); err != nil {
				t.Fatalf("SetPaused(false): %v", err)
			}
		})
	}
}

// TestClosedPaneIgnoresPause pins the ordering Close already documents.
//
// videoPane.Close kills ffmpeg without SIGCONT, because vt.kill has its own
// handling and a SIGSTOPped process would never be reaped by the Wait inside it.
// A pause arriving after Close must therefore be a no-op rather than a signal to
// a dead process.
func TestClosedPaneIgnoresPause(t *testing.T) {
	var rec signalRecorder
	pane := newVideoPane(4, 4, ColorNone, GlyphCell)
	pane.sig = rec.sig
	pane.Close()
	pane.SetPaused(true)
	wantSignals(t, &rec)
}

// TestPollPositionSkipsAFrozenPlayer is the second half of the bug's cost.
//
// SIGSTOP does not stop mpv from listening on its IPC socket, it stops it from
// answering — so the read does not fail fast, it blocks for the full timeout. At
// 60ms against a 33ms frame budget, held down for ten seconds, that is a pump
// doing nothing but timing out. posClock holds its last value regardless, so
// refusing to ask costs nothing.
func TestPollPositionSkipsAFrozenPlayer(t *testing.T) {
	// A listener that accepts and then says nothing, which is what a SIGSTOPped
	// mpv looks like from the socket's side. A fake that answered would pass
	// against the bug: the read would return in microseconds and the timeout
	// being wasted would be invisible.
	sock := filepath.Join(t.TempDir(), "frozen.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()

	released := make(chan struct{})
	defer close(released)
	go func() {
		c, aerr := ln.Accept()
		if aerr != nil {
			return
		}
		defer c.Close()
		io.Copy(io.Discard, c) // drain the request, never reply
		<-released
	}()

	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	p := &SyncPlayer{ipc: &mpvIPC{conn: conn, r: bufio.NewReader(conn)}}
	p.clock.update(42.5, true)

	// Running: the read is attempted and burns the timeout, reporting failure.
	if _, ok := p.pollPosition(100 * time.Millisecond); ok {
		t.Error("pollPosition reported ok against a socket that never answers")
	}

	// Paused: the same socket, but the read must not be attempted at all. Two
	// seconds of timeout against a 100ms ceiling, so the un-guarded version
	// fails this by an order of magnitude rather than by a hair.
	p.paused.Store(true)
	start := time.Now()
	pos, ok := p.pollPosition(2 * time.Second)
	elapsed := time.Since(start)

	if ok {
		t.Error("pollPosition on a paused player reported ok")
	}
	if pos != 42.5 {
		t.Errorf("pos = %v, want the held value 42.5", pos)
	}
	if elapsed > 100*time.Millisecond {
		t.Errorf("paused pollPosition took %v; it is reading the socket", elapsed)
	}
	p.ipc.close()
}
