package main

import (
	"fmt"
	"strings"
	"testing"
)

func truncateForLog(s string) string {
	if len(s) > 120 {
		return s[:120] + "..."
	}
	return s
}

// --- Transport state machine (S1) -------------------------------------------
//
// The player is tested against a fake media backend, so every case here runs
// with no ffmpeg, no mpv and no network. These are transport semantics — the
// rules a user feels — not process plumbing.

type fakeMedia struct {
	pos      float64
	dur      float64
	paused   bool
	volume   int
	seeks    []float64
	closed   bool
	pauseErr error
	chapters []Chapter
}

func (f *fakeMedia) Position() float64 { return f.pos }

func (f *fakeMedia) Duration() float64 { return f.dur }

func (f *fakeMedia) SetPaused(p bool) error {
	f.paused = p
	return f.pauseErr
}

func (f *fakeMedia) Seek(sec float64) error {
	f.seeks = append(f.seeks, sec)
	f.pos = sec
	return nil
}

func (f *fakeMedia) SetVolume(v int) error { f.volume = v; return nil }

func (f *fakeMedia) Close() error { f.closed = true; return nil }

func (f *fakeMedia) Chapters() []Chapter { return f.chapters }

func newTestPlayer(m *fakeMedia, queue ...Track) *Player {
	return NewPlayer(queue, m)
}

func TestPlayerSeekClampsToTrack(t *testing.T) {
	m := &fakeMedia{dur: 100}
	p := newTestPlayer(m, Track{ID: "a"})

	p.Tick(90)
	p.Do(CmdSeekFwd) // +10 would be 100 exactly
	if got := p.State().Pos; got != 100 {
		t.Errorf("seek to end = %v, want 100", got)
	}
	p.Do(CmdSeekFwd) // past the end must clamp, not wrap or overflow
	if got := p.State().Pos; got != 100 {
		t.Errorf("seek past end = %v, want 100", got)
	}
	// 100 back to 0 is 10 presses; one more must clamp rather than go negative.
	for i := 0; i < 11; i++ {
		p.Do(CmdSeekBack)
	}
	if got := p.State().Pos; got != 0 {
		t.Errorf("seek before start = %v, want 0", got)
	}
	if len(m.seeks) == 0 {
		t.Error("media was never told to seek")
	}
}

func TestPlayerPauseSurvivesSeek(t *testing.T) {
	// The bug this pins: a seek rebuilds ffmpeg, and a naive implementation
	// leaves the player thinking it is playing while nothing is moving. Pause
	// must be a state, not a side effect of a signal.
	m := &fakeMedia{dur: 100}
	p := newTestPlayer(m, Track{ID: "a"})

	p.Do(CmdTogglePause)
	p.Do(CmdSeekFwd)
	if !p.State().Paused {
		t.Error("seek while paused left the player playing")
	}
	p.Do(CmdSeekFwd)
	if !p.State().Paused {
		t.Error("second seek while paused left the player playing")
	}
	p.Do(CmdTogglePause)
	if p.State().Paused {
		t.Error("toggle did not resume")
	}
}

func TestPlayerPauseReachesMedia(t *testing.T) {
	m := &fakeMedia{dur: 100}
	p := newTestPlayer(m, Track{ID: "a"})
	p.Do(CmdTogglePause)
	if !m.paused {
		t.Error("media was not paused")
	}
	p.Do(CmdTogglePause)
	if m.paused {
		t.Error("media was not resumed")
	}
}

func TestPlayerVolumeClamps(t *testing.T) {
	m := &fakeMedia{}
	p := newTestPlayer(m, Track{ID: "a"})
	p.Do(CmdVolUp) // from 100 default
	p.Do(CmdVolUp)
	if got := p.State().Volume; got != 100 {
		t.Errorf("vol up at max = %d, want 100", got)
	}
	for i := 0; i < 40; i++ {
		p.Do(CmdVolDown)
	}
	if got := p.State().Volume; got != 0 {
		t.Errorf("vol down past min = %d, want 0", got)
	}
	if m.volume != 0 {
		t.Errorf("media volume = %d, want 0", m.volume)
	}
}

func TestPlayerNextAdvancesQueue(t *testing.T) {
	m := &fakeMedia{}
	q := []Track{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	p := newTestPlayer(m, q...)

	p.Do(CmdNext)
	if got := p.State().Index; got != 1 {
		t.Errorf("index = %d, want 1", got)
	}
	if got := p.State().Outcome; got != OutcomeNext {
		t.Errorf("outcome = %v, want OutcomeNext", got)
	}
}

func TestPlayerNextOnLastTrackEnds(t *testing.T) {
	// There is nothing after the last result. Stopping is the honest answer;
	// wrapping or silently doing nothing both read as a broken key.
	m := &fakeMedia{}
	q := []Track{{ID: "a"}, {ID: "b"}}
	p := newTestPlayer(m, q...)
	p.Do(CmdNext) // -> index 1, the last track
	p.Do(CmdNext)
	if got := p.State().Outcome; got != OutcomeEnded {
		t.Errorf("outcome = %v, want OutcomeEnded", got)
	}
}

func TestPlayerPrevStepsBackAndRestartsAtStart(t *testing.T) {
	m := &fakeMedia{dur: 100}
	q := []Track{{ID: "a"}, {ID: "b"}}
	p := newTestPlayer(m, q...)

	p.Do(CmdNext) // index 1
	p.Do(CmdPrev)
	if got := p.State().Index; got != 0 {
		t.Errorf("prev index = %d, want 0", got)
	}
	// Already on the first track: prev restarts it rather than doing nothing,
	// which is what every other player does.
	p.Tick(42)
	p.Do(CmdPrev)
	if got := p.State().Index; got != 0 {
		t.Errorf("prev at first track moved to index %d, want 0", got)
	}
	if got := p.State().Pos; got != 0 {
		t.Errorf("prev at first track = %v, want a restart to 0", got)
	}
}

func TestPlayerQuitStops(t *testing.T) {
	p := newTestPlayer(&fakeMedia{}, Track{ID: "a"})
	p.Do(CmdQuit)
	if got := p.State().Outcome; got != OutcomeQuit {
		t.Errorf("outcome = %v, want OutcomeQuit", got)
	}
}

func TestPlayerExposesDurationForHUD(t *testing.T) {
	// The progress bar needs the duration, and the media backend is the only
	// thing that knows it.
	p := newTestPlayer(&fakeMedia{dur: 3674}, Track{ID: "a"})
	if got := p.State().Dur; got != 3674 {
		t.Errorf("dur = %v, want 3674", got)
	}
}

// Seconds must include the resume offset. A player rebuilt at -ss 300 reports
// media time 300, not 0 — and getting this wrong made every terminal resize
// rewind the audio to the start, because checkSync read a 300s "drift" and
// seeked mpv back onto the (wrongly believed) video position.

func TestPlayerTickFollowsMediaClock(t *testing.T) {
	// The media clock is authoritative: position is reported, not predicted.
	p := newTestPlayer(&fakeMedia{dur: 100}, Track{ID: "a"})
	p.Tick(42)
	if got := p.State().Pos; got != 42 {
		t.Errorf("pos = %v, want 42", got)
	}
}

func TestPlayerTickIsNotConfusedBySeek(t *testing.T) {
	// Frames decoded before a seek cannot arrive after it, because a seek
	// replaces the decoder and its channel outright rather than sharing them.
	// The player therefore takes the clock at face value — and the real
	// guarantee is that SyncPlayer.Seconds() counts from the seek target.
	p := newTestPlayer(&fakeMedia{dur: 100}, Track{ID: "a"})
	p.Do(CmdSeekFwd) // seek to 10 from 0
	p.Tick(10 + 3)   // first tick after the rebuilt decoder starts
	if got := p.State().Pos; got != 13 {
		t.Errorf("pos = %v, want 13", got)
	}
}

// --- mpv IPC properties (S4) -----------------------------------------------

func TestVolumeCommandClampsBeforeSending(t *testing.T) {
	// mpv accepts 0-100 for volume. Sending 120 is rejected at the socket with an
	// opaque error, so the clamp belongs on our side of it.
	if got := clampVolume(140); got != 100 {
		t.Errorf("clampVolume(140) = %d, want 100", got)
	}
	if got := clampVolume(-4); got != 0 {
		t.Errorf("clampVolume(-4) = %d, want 0", got)
	}
	if got := clampVolume(65); got != 65 {
		t.Errorf("clampVolume(65) = %d, want 65", got)
	}
}

// --- Key decoding -----------------------------------------------------------
//
// Pure function so escape sequences can be tested without a terminal. Arrows
// arrive as ESC [ <final>, which is the only multi-byte sequence handled.

func TestPlayerRecordsTransportFailures(t *testing.T) {
	// A keypress the user just made must not fail silently: a seek that did not
	// happen or a volume change mpv rejected both read as a broken program.
	m := &fakeMedia{dur: 100, pauseErr: fmt.Errorf("mpv: property not found")}
	p := newTestPlayer(m, Track{ID: "a"})
	p.Do(CmdTogglePause)
	err := p.State().LastErr
	if err == nil {
		t.Fatal("failing pause was swallowed")
	}
	if !strings.Contains(err.Error(), "property not found") {
		t.Errorf("error = %v, want mpv's reason", err)
	}
	p.ClearErr()
	if p.State().LastErr != nil {
		t.Error("ClearErr did not clear the error")
	}
}
