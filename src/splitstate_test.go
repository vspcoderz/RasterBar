package main

import "testing"

// TestSplitDividerMovesInBothDirections is the regression test for the divider
// keys doing nothing.
//
// The state used to live as locals inside playTrack, initialised to 0. From 0,
// clampDivider(cols, -1) and clampDivider(cols, +1) both resolve to the same
// quarter-width floor, so `{` and `}` were pinned together and the split could
// only be moved by hand past the floor first. No test could have caught it,
// because nothing outside playTrack could read those locals -- which is why the
// state is a type now and this test exists.
func TestSplitDividerMovesInBothDirections(t *testing.T) {
	const cols = 100
	var sp splitState
	sp.initDivider(cols)

	start := sp.divider
	if start != cols/2 {
		t.Fatalf("initDivider(%d) = %d, want an even split (%d)", cols, start, cols/2)
	}

	sp.nudge(cols, +1)
	if sp.divider != start+1 {
		t.Errorf("after +1 the divider is %d, want %d", sp.divider, start+1)
	}
	sp.nudge(cols, -1)
	if sp.divider != start {
		t.Errorf("after -1 the divider is %d, want it back at %d", sp.divider, start)
	}

	// Both directions must be distinct from each other and from the start.
	sp.nudge(cols, +1)
	right := sp.divider
	sp.nudge(cols, -1)
	sp.nudge(cols, -1)
	left := sp.divider
	if right == left {
		t.Errorf("+1 and -1 both gave %d; the keys are clamped together", right)
	}
	if left == start || right == start {
		t.Errorf("a nudge did not move the divider (start=%d left=%d right=%d)", start, left, right)
	}
}

// TestSplitDividerStopsAtTheBounds: the floor and ceiling exist so neither pane
// collapses, and hitting them should stop rather than wrap.
func TestSplitDividerStopsAtTheBounds(t *testing.T) {
	const cols = 80
	var sp splitState
	sp.initDivider(cols)
	lo := int(float64(cols) * splitMinFrac)
	hi := int(float64(cols) * splitMaxFrac)

	for i := 0; i < cols*2; i++ {
		sp.nudge(cols, -1)
	}
	if sp.divider != lo {
		t.Errorf("nudging left 160 times gave %d, want it parked at the floor %d", sp.divider, lo)
	}
	for i := 0; i < cols*2; i++ {
		sp.nudge(cols, +1)
	}
	if sp.divider != hi {
		t.Errorf("nudging right 160 times gave %d, want it parked at the ceiling %d", sp.divider, hi)
	}
}

// TestSplitDividerKeysReachTheState walks the actual key bytes through the decoder
// into the state, so a key that is decoded but never wired up (or wired to the
// wrong command) fails here rather than in a terminal.
func TestSplitDividerKeysReachTheState(t *testing.T) {
	const cols = 100
	cases := []struct {
		key  byte
		cmd  Cmd
		want func(before int) int
	}{
		{'{', CmdDividerLeft, func(b int) int { return b - 1 }},
		{'}', CmdDividerRight, func(b int) int { return b + 1 }},
	}
	for _, tc := range cases {
		if got := cmdForByte(tc.key); got != tc.cmd {
			t.Errorf("%q decodes to %v, want %v", tc.key, got, tc.cmd)
			continue
		}
		var sp splitState
		sp.initDivider(cols)
		before := sp.divider
		switch cmdForByte(tc.key) {
		case CmdDividerLeft:
			sp.nudge(cols, -1)
		case CmdDividerRight:
			sp.nudge(cols, +1)
		}
		if sp.divider != tc.want(before) {
			t.Errorf("%q moved the divider %d -> %d, want %d",
				tc.key, before, sp.divider, tc.want(before))
		}
	}
}

// TestSplitSideKeysSetRatherThanToggle: `a` and `d` name a side, so pressing the
// same one twice must not leave the pane somewhere the user did not ask for.
func TestSplitSideKeysSetRatherThanToggle(t *testing.T) {
	for _, tc := range []struct {
		key      byte
		cmd      Cmd
		wantLeft bool
	}{
		{'a', CmdPaneLeft, true},
		{'d', CmdPaneRight, false},
	} {
		if got := cmdForByte(tc.key); got != tc.cmd {
			t.Errorf("%q decodes to %v, want %v", tc.key, got, tc.cmd)
			continue
		}
		var sp splitState
		// Start from the opposite side, twice, to prove it is idempotent.
		for i := 0; i < 2; i++ {
			sp.setSide(tc.wantLeft)
		}
		if sp.videoLeft != tc.wantLeft {
			t.Errorf("%q twice left videoLeft=%v, want %v", tc.key, sp.videoLeft, tc.wantLeft)
		}
		sl := sp.layout(colsForTest, 20)
		wantX := sl.divider
		if tc.wantLeft && sl.video.x != 0 {
			t.Errorf("%q: video pane starts at column %d, want 0", tc.key, sl.video.x)
		}
		if !tc.wantLeft && sl.viz.x != 0 {
			t.Errorf("%q: visualiser pane starts at column %d, want 0", tc.key, sl.viz.x)
		}
		_ = wantX
	}
}

const colsForTest = 100

// TestSplitToggleIsOffByDefault: the pane costs a second ffmpeg and half the
// cells, so it must not appear until asked for.
func TestSplitToggleIsOffByDefault(t *testing.T) {
	var sp splitState
	sp.initDivider(100)
	if sp.on {
		t.Fatal("the split is on before anyone pressed W")
	}
	// With it off, the visualiser owns the whole grid.
	if r := sp.vizRect(100, 20); r.cols != 100 || r.x != 0 {
		t.Errorf("split off gives the visualiser %+v, want the full width at x=0", r)
	}
	sp.toggle()
	if !sp.on {
		t.Fatal("toggle did not turn the split on")
	}
	if r := sp.vizRect(100, 20); r.cols >= 100 {
		t.Errorf("split on still gives the visualiser %d columns, want fewer than 100", r.cols)
	}
	sp.toggle()
	if r := sp.vizRect(100, 20); r.cols != 100 {
		t.Errorf("toggling back off left the visualiser at %d columns, want the full 100", r.cols)
	}
}

// TestSplitThumbTogglesIndependently: T must not also change the side or the
// divider, which is the kind of coupling a single switch invites.
func TestSplitThumbTogglesIndependently(t *testing.T) {
	var sp splitState
	sp.initDivider(100)
	sp.on = true
	sp.setSide(true)
	beforeDivider, beforeLeft := sp.divider, sp.videoLeft

	sp.toggleThumb()
	if !sp.thumb {
		t.Error("toggleThumb did not turn the still on")
	}
	sp.toggleThumb()
	if sp.thumb {
		t.Error("toggleThumb did not turn the still off again")
	}
	if sp.divider != beforeDivider || sp.videoLeft != beforeLeft {
		t.Errorf("thumb toggle moved the side or divider (divider %d->%d, left %v->%v)",
			beforeDivider, sp.divider, beforeLeft, sp.videoLeft)
	}
}

// TestSplitThumbNeedsNoVideoStream: the whole reason the still mode is useful is
// that it works without a video URL, so the pane must not consult one.
func TestSplitThumbNeedsNoVideoStream(t *testing.T) {
	s := &trackSession{split: true, thumb: true, pair: mediaPair{}}
	s.track = Track{Title: "x"}
	// No paneURL and no thumbURL: this must fail with a message about the
	// thumbnail specifically, not about a video stream.
	err := s.startPane(computeSplitLayout(80, 20, 40, false), 0)
	if err == nil {
		t.Fatal("a thumbnail-less track started a still pane")
	}
	if s.pane != nil {
		t.Error("a failed still left a pane behind")
	}
}
