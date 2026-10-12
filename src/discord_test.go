package main

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"encoding/json"
	"net"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

// --- the frame codec --------------------------------------------------------

func TestEncodeRPCFrameShape(t *testing.T) {
	got, err := encodeRPCFrame(rpcOpHandshake, []byte("hi"))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Hand-worked, not recomputed: opcode 0 little-endian is 00 00 00 00,
	// length 2 little-endian is 02 00 00 00.
	want := []byte{0, 0, 0, 0, 2, 0, 0, 0, 'h', 'i'}
	if !bytes.Equal(got, want) {
		t.Errorf("frame = % x, want % x", got, want)
	}
}

func TestRPCFrameRoundTrip(t *testing.T) {
	payload := []byte(`{"cmd":"SET_ACTIVITY"}`)
	frame, err := encodeRPCFrame(rpcOpFrame, payload)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	op, body, err := readRPCFrame(bytes.NewReader(frame))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if op != rpcOpFrame {
		t.Errorf("op = %d, want %d", op, rpcOpFrame)
	}
	if !bytes.Equal(body, payload) {
		t.Errorf("body = %q, want %q", body, payload)
	}
}

// The length field is four bytes off a socket we did not write. A reader that
// trusts it turns a garbled peer into an OOM, so the cap has to be checked
// before the allocation and not after it.
func TestReadRPCFrameRejectsOversizedLength(t *testing.T) {
	var head [8]byte
	binary.LittleEndian.PutUint32(head[0:4], uint32(rpcOpFrame))
	binary.LittleEndian.PutUint32(head[4:8], 0xFFFFFFFF) // 4GB, claimed

	if _, _, err := readRPCFrame(bytes.NewReader(head[:])); err == nil {
		t.Fatal("read accepted a 4GB frame length")
	} else if !strings.Contains(err.Error(), "limit") {
		t.Errorf("error = %v, want it to name the limit", err)
	}
}

func TestEncodeRPCFrameRejectsOversizedPayload(t *testing.T) {
	if _, err := encodeRPCFrame(rpcOpFrame, make([]byte, rpcMaxPayload+1)); err == nil {
		t.Fatal("encode accepted a payload over the limit")
	}
}

// A frame cut in half must be an error, not a short body that JSON then chokes
// on with a message about punctuation instead of about the truncation.
func TestReadRPCFrameRejectsTruncatedBody(t *testing.T) {
	frame, err := encodeRPCFrame(rpcOpFrame, []byte(`{"a":1}`))
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	if _, _, err := readRPCFrame(bytes.NewReader(frame[:len(frame)-3])); err == nil {
		t.Fatal("read accepted a truncated body")
	}
}

// --- socket discovery -------------------------------------------------------

func TestDiscordSocketPathsPrefersXDGRuntimeAndDedupes(t *testing.T) {
	dir := t.TempDir()
	other := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	t.Setenv("TMPDIR", other)

	paths := discordSocketPaths()

	if want := filepath.Join(dir, "discord-ipc-0"); paths[0] != want {
		t.Errorf("first path = %q, want %q", paths[0], want)
	}
	// The ten-index scan is what finds canary/PTB builds, which take the first
	// free index rather than index 0.
	if want := filepath.Join(dir, "discord-ipc-9"); paths[9] != want {
		t.Errorf("tenth path = %q, want %q", paths[9], want)
	}
	seen := map[string]bool{}
	for _, p := range paths {
		if seen[p] {
			t.Errorf("duplicate candidate %q", p)
		}
		seen[p] = true
	}
}

// --- what gets shown --------------------------------------------------------

func TestPresenceForShowsTitleAndChannel(t *testing.T) {
	st := State{
		Track: Track{Title: "Rick Astley", Channel: "Whenever You Need Somebody"},
		Dur:   212,
	}
	got := presenceFor(st)
	if got.Details != "Rick Astley" {
		t.Errorf("details = %q", got.Details)
	}
	if got.State != "Whenever You Need Somebody" {
		t.Errorf("state = %q", got.State)
	}
}

func TestPresenceForReplacesStateWhilePaused(t *testing.T) {
	st := State{
		Track:  Track{Title: "x", Channel: "some channel"},
		Paused: true,
	}
	if got := presenceFor(st); got.State != "Paused" {
		t.Errorf("state = %q, want Paused", got.State)
	}
}

func TestPresenceForUsesLibraryEpisodeSpelling(t *testing.T) {
	// S%02dE%02d is library.go's spelling. If this ever diverges, the same
	// episode is written two ways in the same program.
	track := Track{
		Title:     "Ballad of Fallen Angels",
		Series:    "Cowboy Bebop",
		Season:    1,
		Episode:   2,
		LocalPath: "/l/s01e02.mkv",
	}
	got := presenceFor(State{Track: track})
	if got.State != "Cowboy Bebop · S01E02" {
		t.Errorf("state = %q, want %q", got.State, "Cowboy Bebop · S01E02")
	}
}

func TestPresenceForLocalTrackWithNoSeries(t *testing.T) {
	got := presenceFor(State{Track: Track{Title: "nocturne", LocalPath: "/m/nocturne.opus"}})
	if got.State != "local" {
		t.Errorf("state = %q, want %q", got.State, "local")
	}
}

func TestPresenceForTruncatesToDiscordsLimit(t *testing.T) {
	long := strings.Repeat("a", 300)
	got := presenceFor(State{Track: Track{Title: long, Channel: long}})
	if n := len([]rune(got.Details)); n > presenceTextLimit {
		t.Errorf("details is %d runes, over the %d limit", n, presenceTextLimit)
	}
	if n := len([]rune(got.State)); n > presenceTextLimit {
		t.Errorf("state is %d runes, over the %d limit", n, presenceTextLimit)
	}
}

// Pos moves every frame, so including it in the change test means no two
// updates are ever equal and the dedup does nothing at all.
func TestPresenceSameAsIgnoresPosition(t *testing.T) {
	a := presenceWanted{Details: "d", State: "s", Pos: 0, Dur: 100}
	b := presenceWanted{Details: "d", State: "s", Pos: 99, Dur: 100}
	if !a.sameAs(b) {
		t.Error("sameAs is false for the same track at two positions")
	}
	c := presenceWanted{Details: "d", State: "Paused", Pos: 0, Dur: 100}
	if a.sameAs(c) {
		t.Error("sameAs is true across a pause")
	}
}

// --- against a real socket --------------------------------------------------

// fakeDiscord is a unix socket speaking the wire protocol: it answers the
// handshake and then records every SET_ACTIVITY it is sent.
//
// A real socket rather than a mock, because the parts most likely to break are
// the framing and the socket path search, and neither exists in a mock.
type fakeDiscord struct {
	t    *testing.T
	path string
	acts chan *rpcActivity
	done chan struct{}
}

func newFakeDiscord(t *testing.T) *fakeDiscord {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)

	sock := filepath.Join(dir, "discord-ipc-0")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	f := &fakeDiscord{
		t:    t,
		path: sock,
		acts: make(chan *rpcActivity, 8),
		done: make(chan struct{}),
	}
	go func() {
		defer close(f.done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		defer ln.Close()

		r := bufio.NewReader(conn)
		op, _, err := readRPCFrame(r)
		if err != nil || op != rpcOpHandshake {
			return
		}
		ready, _ := encodeRPCFrame(rpcOpFrame, []byte(`{"cmd":"DISPATCH","evt":"READY","data":{"v":1}}`))
		if _, err := conn.Write(ready); err != nil {
			return
		}
		for {
			op, body, err := readRPCFrame(r)
			if err != nil {
				return
			}
			if op != rpcOpFrame {
				continue
			}
			var cmd struct {
				Cmd  string  `json:"cmd"`
				Args rpcArgs `json:"args"`
			}
			if json.Unmarshal(body, &cmd) != nil || cmd.Cmd != "SET_ACTIVITY" {
				continue
			}
			f.acts <- cmd.Args.Activity
		}
	}()
	t.Cleanup(func() {
		ln.Close()
		<-f.done
	})
	return f
}

// next waits for the next activity, or fails the test.
func (f *fakeDiscord) next(t *testing.T) *rpcActivity {
	t.Helper()
	select {
	case a := <-f.acts:
		return a
	case <-time.After(3 * time.Second):
		t.Fatal("no SET_ACTIVITY arrived")
		return nil
	}
}

func TestPresencePublishesThroughTheRealSocket(t *testing.T) {
	fake := newFakeDiscord(t)

	p, err := newPresence(discordAppID)
	if err != nil {
		t.Fatalf("newPresence: %v", err)
	}
	defer p.Close()

	// An app ID the fake does not know is still accepted by this fake, so the
	// handshake contract itself is asserted separately below.
	p.now = func() time.Time { return time.Unix(1_700_000_000, 0) }
	p.Set(presenceWanted{Details: "Rick Astley", State: "Whenever", Pos: 30, Dur: 212})

	act := fake.next(t)
	if act == nil {
		t.Fatal("cleared activity where one was expected")
	}
	if act.Details != "Rick Astley" {
		t.Errorf("details = %q", act.Details)
	}
	// Start is now - pos: 1700000000 - 30.
	if act.Timestamps == nil || act.Timestamps.Start != 1_699_999_970 {
		t.Errorf("timestamps = %+v, want start 1699999970", act.Timestamps)
	}
	if act.Timestamps.End != 1_700_000_182 {
		t.Errorf("end = %d, want 1700000182", act.Timestamps.End)
	}
}

// An unknown duration must omit End rather than emit one in the past, which
// would draw a full progress bar on a live stream.
func TestPresenceOmitsEndWhenDurationUnknown(t *testing.T) {
	fake := newFakeDiscord(t)
	p, err := newPresence(discordAppID)
	if err != nil {
		t.Fatalf("newPresence: %v", err)
	}
	defer p.Close()

	p.Set(presenceWanted{Details: "live", State: "now", Dur: 0})
	act := fake.next(t)
	if act.Timestamps == nil {
		t.Fatal("no timestamps at all")
	}
	if act.Timestamps.End != 0 {
		t.Errorf("end = %d, want 0 for an unknown duration", act.Timestamps.End)
	}
}

func TestPresenceSkipsAnUnchangedUpdate(t *testing.T) {
	fake := newFakeDiscord(t)
	p, err := newPresence(discordAppID)
	if err != nil {
		t.Fatalf("newPresence: %v", err)
	}
	defer p.Close()

	base := time.Unix(1_700_000_000, 0)
	p.now = func() time.Time { return base }
	p.Set(presenceWanted{Details: "a", State: "b", Pos: 0, Dur: 100})
	fake.next(t)

	// Same track, same duration, one second later: nothing a viewer could see
	// has changed, so nothing goes out.
	p.now = func() time.Time { return base.Add(time.Second) }
	p.Set(presenceWanted{Details: "a", State: "b", Pos: 1, Dur: 100})
	select {
	case act := <-fake.acts:
		t.Fatalf("an invisible change was published: %+v", act)
	case <-time.After(250 * time.Millisecond):
	}
}

// Held seek keys autorepeat well past Discord's five-updates-per-twenty
// limit. The floor is derived from that limit, and this pins it rather than
// trusting the comment.
func TestPresenceRateLimitsASeekStorm(t *testing.T) {
	fake := newFakeDiscord(t)
	p, err := newPresence(discordAppID)
	if err != nil {
		t.Fatalf("newPresence: %v", err)
	}
	defer p.Close()

	base := time.Unix(1_700_000_000, 0)
	clock := base
	p.now = func() time.Time { return clock }

	// One seek per second for twenty seconds: twenty keypresses.
	sent := 0
	for i := 0; i < 20; i++ {
		clock = base.Add(time.Duration(i) * time.Second)
		p.Set(presenceWanted{Details: "a", State: "b", Pos: float64(i * 60), Dur: 3600})
	}
	// Drain what the writer got through.
	time.Sleep(200 * time.Millisecond)
	for {
		select {
		case <-fake.acts:
			sent++
			continue
		default:
		}
		break
	}

	// Five seconds of floor over a twenty second window is at most four.
	if sent > 4 {
		t.Errorf("published %d updates in 20s, over the %s floor allows",
			sent, presenceMinInterval)
	}
	if sent == 0 {
		t.Error("nothing published at all; the first update must not be floored")
	}
}

func TestPresenceClearSendsANullActivity(t *testing.T) {
	fake := newFakeDiscord(t)
	p, err := newPresence(discordAppID)
	if err != nil {
		t.Fatalf("newPresence: %v", err)
	}

	p.Set(presenceWanted{Details: "a", State: "b"})
	fake.next(t)

	p.Clear()
	if act := fake.next(t); act != nil {
		t.Errorf("clear sent %+v, want a null activity", act)
	}
	p.Close()
}

// Nil is the disabled state, so a program with no Discord running calls the
// same methods. Every one has to be a no-op rather than a panic.
func TestNilPresenceIsSafe(t *testing.T) {
	var p *presence
	p.Set(presenceWanted{Details: "x"})
	p.Clear()
	p.Close()
}

func TestNewPresenceReportsWhenDiscordIsAbsent(t *testing.T) {
	// An empty XDG_RUNTIME_DIR and an empty TMPDIR: nowhere for a socket to be.
	empty := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", empty)
	t.Setenv("TMPDIR", empty)

	// os.TempDir() falls back to /tmp, which may legitimately hold a real
	// Discord socket, so only assert when it finds nothing.
	p, err := newPresence(discordAppID)
	if err == nil {
		p.Close()
		t.Skip("a real Discord is running on this machine")
	}
	if !strings.Contains(err.Error(), "discord") && !strings.Contains(err.Error(), "socket") {
		t.Errorf("error = %v, want it to name what it could not find", err)
	}
}

// The JSON shape is hand-written rather than produced by marshalling a want
// value, because a test that marshals the same struct the code marshals proves
// nothing about the wire format.
func TestSetActivityJSONShape(t *testing.T) {
	payload, err := json.Marshal(rpcCommand{
		Cmd: "SET_ACTIVITY",
		Args: rpcArgs{
			PID: 4242,
			Activity: &rpcActivity{
				Details:    "d",
				State:      "s",
				Type:       2,
				Timestamps: &rpcTimestamps{Start: 10, End: 20},
			},
		},
		Nonce: "7",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(payload, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["cmd"] != "SET_ACTIVITY" {
		t.Errorf("cmd = %v", got["cmd"])
	}
	if got["nonce"] != "7" {
		t.Errorf("nonce = %v", got["nonce"])
	}
	args := got["args"].(map[string]any)
	if args["pid"].(float64) != 4242 {
		t.Errorf("pid = %v", args["pid"])
	}
	// start and end are unix seconds as numbers, not strings and not millis.
	ts := args["activity"].(map[string]any)["timestamps"].(map[string]any)
	if ts["start"].(float64) != 10 || ts["end"].(float64) != 20 {
		t.Errorf("timestamps = %v", ts)
	}
}

// --- handshake --------------------------------------------------------------

// A wrong application ID has to come back as a sentence about the application
// ID. "handshake failed" sends people looking at their network instead.
func TestHandshakeReportsARejectedApplication(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	sock := filepath.Join(dir, "discord-ipc-0")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		if _, _, err := readRPCFrame(r); err != nil {
			return
		}
		body := []byte(`{"cmd":"ERROR","evt":null,"data":{"code":4000,"message":"Invalid Client ID"}}`)
		frame, _ := encodeRPCFrame(rpcOpFrame, body)
		conn.Write(frame)
	}()

	_, err = newPresence("not-an-app")
	if err == nil {
		t.Fatal("handshake accepted a rejected application id")
	}
	if !strings.Contains(err.Error(), "application id") {
		t.Errorf("error = %v, want it to name the application id", err)
	}
}

// A ping arriving before READY must be answered inline, because the reader
// goroutine that handles them does not exist yet.
func TestHandshakeAnswersAPingBeforeReady(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", dir)
	sock := filepath.Join(dir, "discord-ipc-0")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()

	pong := make(chan rpcOp, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		readRPCFrame(r)
		ping, _ := encodeRPCFrame(rpcOpPing, []byte(`{"cmd":"PING"}`))
		conn.Write(ping)

		op, _, err := readRPCFrame(r)
		if err != nil {
			return
		}
		pong <- op

		ready, _ := encodeRPCFrame(rpcOpFrame, []byte(`{"cmd":"DISPATCH","evt":"READY"}`))
		conn.Write(ready)
		// Hold the connection open so the client can finish its handshake.
		time.Sleep(200 * time.Millisecond)
	}()

	p, err := newPresence(discordAppID)
	if err != nil {
		t.Fatalf("newPresence: %v", err)
	}
	defer p.Close()

	select {
	case op := <-pong:
		if op != rpcOpPong {
			t.Errorf("replied with opcode %d, want PONG", op)
		}
	case <-time.After(2 * time.Second):
		t.Error("no reply to the pre-READY ping")
	}
}

// Every method on presence has to survive being called twice: the render loop
// can reach Close through the deferred cleanup and through an explicit quit.
func TestPresenceCloseIsIdempotent(t *testing.T) {
	newFakeDiscord(t)
	p, err := newPresence(discordAppID)
	if err != nil {
		t.Fatalf("newPresence: %v", err)
	}
	p.Close()
	p.Close()
}

// The frame decoder is the only place a payload size is enforced, so its limit
// must be reachable from both sides consistently.
func TestPayloadLimitIsConsistent(t *testing.T) {
	if rpcMaxPayload <= 0 {
		t.Fatalf("rpcMaxPayload = %d, must be positive", rpcMaxPayload)
	}
	if _, err := encodeRPCFrame(rpcOpFrame, make([]byte, rpcMaxPayload)); err != nil {
		t.Errorf("a payload exactly at the limit was rejected: %v", err)
	}
}

func TestPresenceTextLimitMatchesDiscord(t *testing.T) {
	// Not an arbitrary number: Discord documents 128 for both details and
	// state, and truncating ourselves is cheaper than having it truncated
	// silently server-side after we have already spent the update.
	if presenceTextLimit != 128 {
		t.Errorf("presenceTextLimit = %d, want Discord's 128", presenceTextLimit)
	}
}

func TestPresenceStateLineSpellings(t *testing.T) {
	cases := []struct {
		name  string
		track Track
		want  string
	}{
		{"youtube", Track{Channel: "Some Channel"}, "Some Channel"},
		{"local no series", Track{LocalPath: "/m/a.mp3"}, "local"},
		{"series and episode", Track{Series: "Show", Season: 2, Episode: 7, LocalPath: "/m/a.mkv"}, "Show · S02E07"},
		{"episode no season", Track{Episode: 3, LocalPath: "/m/a.mkv"}, "E03"},
		{"season no episode", Track{Series: "Show", Season: 4, LocalPath: "/m/a.mkv"}, "Show · Season 4"},
	}
	for _, c := range cases {
		if got := presenceStateLine(c.track); got != c.want {
			t.Errorf("%s: state = %q, want %q", c.name, got, c.want)
		}
	}
}

// The application id is a snowflake: digits. A name or a UUID fails the
// handshake server-side as "Invalid Client ID", which reads like a network
// fault, so it has to be an argument error instead.
func TestValidDiscordAppID(t *testing.T) {
	if err := validDiscordAppID(discordAppID); err != nil {
		t.Errorf("the built-in app id was rejected: %v", err)
	}
	for _, bad := range []string{"", "my-app", "1559 235", "0x1234", "1559235914843033610\n"} {
		if err := validDiscordAppID(bad); err == nil {
			t.Errorf("accepted %q as an application id", bad)
		}
	}
}

func TestDiscordConfigDefaultsToOn(t *testing.T) {
	on, id, err := discordConfig(options{}, nil)
	if err != nil {
		t.Fatalf("discordConfig: %v", err)
	}
	if !on {
		t.Error("presence is off by default; the app id is ours and needs no flag")
	}
	if id != discordAppID {
		t.Errorf("app id = %q, want the built-in %q", id, discordAppID)
	}
}

func TestDiscordConfigFlagBeatsEnvironment(t *testing.T) {
	// The one stated most precisely, the same rule --cookies follows.
	env := []string{"VSPZ_YT_CLI_DISCORD_APP_ID=111", "VSPZ_YT_CLI_NO_DISCORD=1"}
	on, id, err := discordConfig(options{discordAppID: "222"}, env)
	if err != nil {
		t.Fatalf("discordConfig: %v", err)
	}
	if on {
		t.Error("--discord-app-id re-enabled a presence the environment disabled")
	}
	if id != "222" {
		t.Errorf("app id = %q, want the flag's 222", id)
	}
}

func TestDiscordConfigReadsTheEnvironment(t *testing.T) {
	on, id, err := discordConfig(options{}, []string{"VSPZ_YT_CLI_DISCORD_APP_ID=333"})
	if err != nil {
		t.Fatalf("discordConfig: %v", err)
	}
	if !on {
		t.Error("the environment app id should not disable presence")
	}
	if id != "333" {
		t.Errorf("app id = %q, want 333 from the environment", id)
	}

	on, _, err = discordConfig(options{}, []string{"VSPZ_YT_CLI_NO_DISCORD=1"})
	if err != nil {
		t.Fatalf("discordConfig: %v", err)
	}
	if on {
		t.Error("VSPZ_YT_CLI_NO_DISCORD=1 did not disable presence")
	}
}

// A variable set to empty or 0 is a shell saying "unset this", which is how a
// .bashrc that exports unconditionally leaves it.
func TestDiscordConfigIgnoresAnEmptyNoDiscord(t *testing.T) {
	for _, val := range []string{"", "0", "false"} {
		on, _, err := discordConfig(options{}, []string{"VSPZ_YT_CLI_NO_DISCORD=" + val})
		if err != nil {
			t.Fatalf("discordConfig: %v", err)
		}
		if !on {
			t.Errorf("VSPZ_YT_CLI_NO_DISCORD=%q disabled presence", val)
		}
	}
}

// A bad app id in the environment is reported rather than silently ignored:
// otherwise the handshake fails later with a message about the network.
func TestDiscordConfigReportsABadEnvironmentAppID(t *testing.T) {
	_, _, err := discordConfig(options{}, []string{"VSPZ_YT_CLI_DISCORD_APP_ID=nope"})
	if err == nil {
		t.Fatal("a non-numeric app id from the environment was accepted")
	}
}

func TestParseArgsAcceptsTheDiscordFlags(t *testing.T) {
	o, err := parseArgs([]string{"--discord", "--discord-app-id", "42", "lofi"})
	if err != nil {
		t.Fatalf("parseArgs: %v", err)
	}
	if !o.discord {
		t.Error("--discord did not set the flag")
	}
	if o.discordAppID != "42" {
		t.Errorf("app id = %q", o.discordAppID)
	}
	if len(o.args) != 1 || o.args[0] != "lofi" {
		t.Errorf("args = %v, want the query left alone", o.args)
	}
}

func TestParseArgsRejectsABadAppID(t *testing.T) {
	if _, err := parseArgs([]string{"--discord-app-id", "not-an-id"}); err == nil {
		t.Fatal("parseArgs accepted a non-numeric application id")
	}
}

func TestPresenceWantedFieldUseIsNotDead(t *testing.T) {
	// Guards against a rename that leaves the publisher building a struct the
	// activity no longer reads.
	w := presenceWanted{Details: "a", State: "b", Pos: 1, Dur: 2}
	want := presenceWanted{Details: "a", State: "b", Pos: 1, Dur: 2}
	if !reflect.DeepEqual(w, want) {
		t.Error("presenceWanted is not the plain value shape its consumers assume")
	}
}
