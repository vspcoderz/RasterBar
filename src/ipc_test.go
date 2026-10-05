package main

import (
	"strings"
	"testing"
)

func TestParseTimePosReply(t *testing.T) {
	// mpv sets "error":"success" on a SUCCESSFUL reply. Treating any non-empty
	// error as a failure silently disabled the drift corrector, which showed up
	// as a permanent desync. This case must be accepted.
	got, ok, done := parseTimePosReply(
		[]byte(`{"data":12.5,"error":"success","request_id":7}`), 7)
	if !done || !ok {
		t.Fatalf(`error:"success" must be treated as success (ok=%v done=%v)`, ok, done)
	}
	if got != 12.5 {
		t.Errorf("position = %v, want 12.5", got)
	}

	// A real error must be rejected.
	_, ok, done = parseTimePosReply(
		[]byte(`{"error":"property unavailable","request_id":7}`), 7)
	if !done || ok {
		t.Errorf("a real error must be rejected (ok=%v done=%v)", ok, done)
	}

	// An event for another request must be skipped, not treated as our answer.
	_, ok, done = parseTimePosReply([]byte(`{"event":"playback-restart"}`), 7)
	if done {
		t.Error("an event is not a reply to our request")
	}

	// Null data with success is still not a position.
	_, ok, done = parseTimePosReply([]byte(`{"data":null,"error":"success","request_id":7}`), 7)
	if !done || ok {
		t.Errorf("null data must be rejected (ok=%v done=%v)", ok, done)
	}

	// Garbage is skipped, not fatal.
	_, ok, done = parseTimePosReply([]byte(`not json`), 7)
	if done {
		t.Error("garbage should not be treated as a completed reply")
	}
}

func TestParseDecision(t *testing.T) {
	if a, i := parseDecision("q", 10); a != ActionQuit || i != -1 {
		t.Errorf("q -> (%v,%d), want quit/-1", a, i)
	}
	if a, i := parseDecision("3", 10); a != ActionPlay || i != 3 {
		t.Errorf("3 -> (%v,%d), want play/3", a, i)
	}
	if a, _ := parseDecision("", 10); a != ActionQuit {
		t.Errorf("empty -> %v, want quit", a)
	}
	if a, _ := parseDecision("99", 10); a != ActionQuit {
		t.Errorf("out of range -> %v, want quit", a)
	}
}

// --- mpv IPC properties (S4) -----------------------------------------------

func TestParsePropReplyAcceptsSuccess(t *testing.T) {
	// Same trap as parseTimePosReply: mpv sets "error":"success" on a GOOD
	// reply, so rejecting any non-empty error field rejects every working call.
	err, ok := parsePropReply([]byte(`{"data":null,"error":"success","request_id":7}`), 7)
	if !ok {
		t.Fatal("success reply was not recognised as ours")
	}
	if err != nil {
		t.Errorf("success reply rejected: %v", err)
	}
}

func TestParsePropReplyReportsFailure(t *testing.T) {
	err, ok := parsePropReply([]byte(`{"error":"property not found","request_id":7}`), 7)
	if !ok {
		t.Fatal("failure was not recognised as our reply")
	}
	if err == nil {
		t.Fatal("failing reply accepted")
	}
	if !strings.Contains(err.Error(), "property not found") {
		t.Errorf("error = %v, want mpv's reason", err)
	}
}

func TestParsePropReplyIgnoresOtherIDs(t *testing.T) {
	// Events and replies to earlier requests share the socket; only ours counts.
	if _, ok := parsePropReply([]byte(`{"error":"success","request_id":8}`), 7); ok {
		t.Error("reply for another request treated as ours")
	}
	if _, ok := parsePropReply([]byte(`{"event":"playback-restart"}`), 7); ok {
		t.Error("event treated as a reply")
	}
}

func TestPropertyRequestEncodesNameAndValue(t *testing.T) {
	// Pause and volume are one property each. A malformed command here fails
	// silently at runtime, so the encoding is worth pinning.
	b, err := marshalCommand("set_property", []interface{}{"volume", 40}, 3)
	if err != nil {
		t.Fatalf("marshalCommand: %v", err)
	}
	// Trailing newline included: the newline is the frame delimiter, so a caller
	// that forgot it would leave mpv waiting for the rest of the line.
	want := "{\"command\":[\"set_property\",\"volume\",40],\"request_id\":3}\n"
	if string(b) != want {
		t.Errorf("request = %s, want %s", b, want)
	}
}
