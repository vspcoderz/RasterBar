package main

import (
	"testing"
)

func TestSyncPlayerSeconds(t *testing.T) {
	s := &SyncPlayer{fps: 10}
	s.frames.Store(50)
	if got := s.Seconds(); got != 5.0 {
		t.Errorf("Seconds = %v, want 5", got)
	}
	zero := &SyncPlayer{}
	if got := zero.Seconds(); got != 0 {
		t.Errorf("zero-fps Seconds = %v, want 0", got)
	}
}

func TestSyncReportIgnoresEarlyPositions(t *testing.T) {
	// Before both sides are really running, positions are meaningless and a seek
	// there would jump the track.
	s := &SyncPlayer{fps: 10, ipc: nil}
	s.frames.Store(2)
	rep, corrected := s.checkSync()
	if corrected {
		t.Error("must not correct without an ipc connection")
	}
	if rep.VideoPos != 0.2 {
		t.Errorf("video pos = %v, want 0.2", rep.VideoPos)
	}
}

// Seconds must include the resume offset. A player rebuilt at -ss 300 reports
// media time 300, not 0 — and getting this wrong made every terminal resize
// rewind the audio to the start, because checkSync read a 300s "drift" and
// seeked mpv back onto the (wrongly believed) video position.
func TestSyncPlayerSecondsIncludesResumeOffset(t *testing.T) {
	resumed := &SyncPlayer{fps: 10, startAt: 300}
	resumed.frames.Store(30)
	if got := resumed.Seconds(); got != 303 {
		t.Errorf("resumed Seconds() = %v, want 303", got)
	}
	fresh := &SyncPlayer{fps: 10}
	fresh.frames.Store(30)
	if got := fresh.Seconds(); got != 3 {
		t.Errorf("fresh Seconds() = %v, want 3", got)
	}
}
