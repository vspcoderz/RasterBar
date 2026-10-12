package main

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Discord Rich Presence over the local IPC socket.
//
// Hand-rolled for the same reason the FFT and the PTY layer are: this project
// takes no third-party modules, and the protocol is a length-prefixed JSON
// frame over a unix socket. It is about two hundred lines.
//
// What it is NOT: an activity library. There is no thumbnail, because Discord
// wants a registered asset *key* and not an arbitrary URL — Track.Thumbs is a
// plain https link that Discord will not fetch, so shipping it would put an
// image slot on the user's profile that is always blank.
//
// The shape of the traffic is the interesting part. Presence is published on
// track start, on pause, and on seek — four times per track, not thirty per
// second — because Discord interpolates the progress bar from
// timestamps.start/end itself. Per-frame updates would tell Discord something
// it can already work out, thirty times a second, for nothing.

// discordAppID is the registered Discord application. Override with
// --discord-app-id or VSPZ_YT_CLI_DISCORD_APP_ID.
//
// There is no way around having one: Discord rejects an activity whose
// application it does not know, server-side, during the handshake. A client
// implementation does not buy you an exemption from that.
const discordAppID = "1559235914843033610"

// Protocol opcodes. The handshake is opcode 0 and everything after it rides on
// opcode 1; the rest are Discord-initiated.
type rpcOp uint32

const (
	rpcOpHandshake rpcOp = 0
	rpcOpFrame     rpcOp = 1
	rpcOpClose     rpcOp = 2
	rpcOpPing      rpcOp = 3
	rpcOpPong      rpcOp = 4
)

// rpcMaxPayload caps a single frame's JSON.
//
// Enforced BEFORE the allocation, which is the entire point. The length field
// is four bytes from a socket we did not write, so a garbled or hostile one can
// legitimately ask for 4GB and Go will happily try. Reading into a buffer sized
// from an unvalidated length is how a music player turns into an OOM.
const rpcMaxPayload = 1 << 20 // 1 MiB. The largest real payload here is ~400B.

// encodeRPCFrame builds the wire frame: opcode and length, both little-endian
// uint32, then the JSON body.
//
// Length-prefixed rather than newline-delimited because Discord's payload can
// legally contain a newline in a track title, and a line reader would tear the
// frame in half exactly when someone plays a song with a newline in its name.
func encodeRPCFrame(op rpcOp, payload []byte) ([]byte, error) {
	if len(payload) > rpcMaxPayload {
		return nil, fmt.Errorf("rpc payload of %d bytes exceeds the %d byte limit",
			len(payload), rpcMaxPayload)
	}
	buf := make([]byte, 8+len(payload))
	binary.LittleEndian.PutUint32(buf[0:4], uint32(op))
	binary.LittleEndian.PutUint32(buf[4:8], uint32(len(payload)))
	copy(buf[8:], payload)
	return buf, nil
}

// readRPCFrame reads one frame.
func readRPCFrame(r io.Reader) (rpcOp, []byte, error) {
	var head [8]byte
	if _, err := io.ReadFull(r, head[:]); err != nil {
		return 0, nil, err
	}
	op := rpcOp(binary.LittleEndian.Uint32(head[0:4]))
	n := binary.LittleEndian.Uint32(head[4:8])
	if n > rpcMaxPayload {
		return 0, nil, fmt.Errorf("rpc frame claims %d bytes, over the %d byte limit",
			n, rpcMaxPayload)
	}
	body := make([]byte, n)
	if _, err := io.ReadFull(r, body); err != nil {
		return 0, nil, err
	}
	return op, body, nil
}

// discordSocketPaths lists candidate IPC sockets, most specific first.
//
// XDG_RUNTIME_DIR comes first because that is where Discord puts it on a modern
// Linux, and a stale socket left in /tmp by a crashed session would otherwise
// win. TMPDIR is the macOS location. The last two are the historical fallbacks.
//
// Ten sub-paths per directory because Discord's stable, canary and PTB builds
// each grab the first free index, and connecting to the wrong one gets you a
// socket that is live but belongs to a different client. Duplicates are dropped
// while preserving order, because os.TempDir() is usually "/tmp" and that would
// otherwise be probed twice per index.
func discordSocketPaths() []string {
	var dirs []string
	seen := map[string]bool{}
	add := func(d string) {
		if d == "" {
			return
		}
		d = filepath.Clean(d)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	add(os.Getenv("XDG_RUNTIME_DIR"))
	add(os.Getenv("TMPDIR"))
	add(os.TempDir())
	add("/tmp")

	out := make([]string, 0, len(dirs)*10)
	for _, d := range dirs {
		for i := 0; i < 10; i++ {
			out = append(out, filepath.Join(d, "discord-ipc-"+strconv.Itoa(i)))
		}
	}
	return out
}

// --- the wire types ---------------------------------------------------------

type rpcHandshake struct {
	V        int    `json:"v"`
	ClientID string `json:"client_id"`
}

type rpcActivity struct {
	Details    string         `json:"details,omitempty"`
	State      string         `json:"state,omitempty"`
	Assets     *rpcAssets     `json:"assets,omitempty"`
	Timestamps *rpcTimestamps `json:"timestamps,omitempty"`
	// Type 2 is "listening", which is what a player is. Omitted would read as
	// "playing", which for a music player is close enough to be unhelpful and
	// for a video one is wrong.
	Type int `json:"type,omitempty"`
}

type rpcAssets struct {
	LargeImage string `json:"large_image,omitempty"`
	LargeText  string `json:"large_text,omitempty"`
}

type rpcTimestamps struct {
	Start int64 `json:"start,omitempty"`
	End   int64 `json:"end,omitempty"`
}

type rpcArgs struct {
	PID      int          `json:"pid"`
	Activity *rpcActivity `json:"activity"`
}

type rpcCommand struct {
	Cmd   string  `json:"cmd"`
	Args  rpcArgs `json:"args"`
	Nonce string  `json:"nonce,omitempty"`
}

type rpcReply struct {
	Cmd  string `json:"cmd"`
	Evt  string `json:"evt"`
	Data struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"data"`
}

// --- the connection ---------------------------------------------------------

// discordConn is one connected client socket.
//
// mu serialises writes. There are two writers: the publish path and the
// reader goroutine's PONG reply, and the protocol has no request ids that make
// interleaved writes safe. Holding a lock across a frame is cheap next to the
// rate we publish at.
type discordConn struct {
	mu sync.Mutex
	c  net.Conn
	r  *bufio.Reader
}

// dialDiscord connects to the first socket that accepts.
//
// Returns the path alongside, because "Discord is not running" and "Discord is
// running but every socket refused us" are different problems and the caller
// reports which one it hit.
func dialDiscord(timeout time.Duration) (*discordConn, string, error) {
	var tried []string
	for _, path := range discordSocketPaths() {
		if _, err := os.Stat(path); err != nil {
			continue
		}
		c, err := net.DialTimeout("unix", path, timeout)
		if err != nil {
			tried = append(tried, fmt.Sprintf("%s (%v)", path, err))
			continue
		}
		return &discordConn{c: c, r: bufio.NewReader(c)}, path, nil
	}
	if len(tried) == 0 {
		return nil, "", fmt.Errorf("no discord IPC socket in %s",
			strings.Join(discordSocketDirs(), ", "))
	}
	return nil, "", fmt.Errorf("discord sockets present but refused: %s",
		strings.Join(tried, "; "))
}

// discordSocketDirs is the directory list on its own, for an error message
// that does not need twenty near-identical paths.
func discordSocketDirs() []string {
	seen := map[string]bool{}
	var dirs []string
	for _, p := range discordSocketPaths() {
		d := filepath.Dir(p)
		if !seen[d] {
			seen[d] = true
			dirs = append(dirs, d)
		}
	}
	return dirs
}

// handshake performs the protocol handshake and waits for READY.
//
// Synchronous, and it must be: the reader goroutine that answers Discord's
// pings afterwards does not exist yet, so anything that arrives before READY
// has to be handled here. A PING before READY is answered inline rather than
// ignored, because Discord does send them during connect and a client that
// ignores them gets disconnected for being unresponsive.
func (d *discordConn) handshake(appID string) error {
	payload, err := json.Marshal(rpcHandshake{V: 1, ClientID: appID})
	if err != nil {
		return err
	}
	frame, err := encodeRPCFrame(rpcOpHandshake, payload)
	if err != nil {
		return err
	}
	if err := d.c.SetWriteDeadline(time.Now().Add(2 * time.Second)); err != nil {
		return err
	}
	if _, err := d.c.Write(frame); err != nil {
		return fmt.Errorf("handshake write: %w", err)
	}

	// Discord answers the handshake within milliseconds or not at all, so the
	// read is bounded rather than left to block until the process exits.
	deadline := time.Now().Add(2 * time.Second)
	for {
		if err := d.c.SetReadDeadline(deadline); err != nil {
			return err
		}
		op, body, err := readRPCFrame(d.r)
		if err != nil {
			return fmt.Errorf("handshake read: %w", err)
		}
		switch op {
		case rpcOpPing:
			if err := d.write(rpcOpPong, body); err != nil {
				return fmt.Errorf("handshake pong: %w", err)
			}
		case rpcOpClose:
			return fmt.Errorf("discord closed the connection during the handshake")
		case rpcOpFrame:
			var reply rpcReply
			if err := json.Unmarshal(body, &reply); err != nil {
				return fmt.Errorf("handshake reply: %w", err)
			}
			switch {
			case reply.Evt == "READY":
				return nil
			case reply.Cmd == "ERROR":
				// Almost always an invalid application ID. Say that, because
				// "handshake failed" sends people looking at their network.
				return fmt.Errorf("discord rejected the application id %q: %s",
					appID, reply.Data.Message)
			}
			// Anything else is a dispatch we do not care about yet.
		}
	}
}

// write sends one frame, bounded by a deadline.
//
// The deadline is the reason the publish path cannot wedge the render loop: a
// Discord that has stopped reading its socket would otherwise block this write
// forever, on a goroutine that used to be a frame.
func (d *discordConn) write(op rpcOp, payload []byte) error {
	frame, err := encodeRPCFrame(op, payload)
	if err != nil {
		return err
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if err := d.c.SetWriteDeadline(time.Now().Add(250 * time.Millisecond)); err != nil {
		return err
	}
	_, err = d.c.Write(frame)
	return err
}

// setActivity publishes, or clears when act is nil.
//
// Fire and forget. The reply is a DISPATCH on a socket nobody is reading
// synchronously, and an activity that Discord accepted and one it silently
// dropped look identical from here; waiting for a confirmation per track would
// buy nothing.
func (d *discordConn) setActivity(pid int, act *rpcActivity, nonce string) error {
	payload, err := json.Marshal(rpcCommand{
		Cmd:   "SET_ACTIVITY",
		Args:  rpcArgs{PID: pid, Activity: act},
		Nonce: nonce,
	})
	if err != nil {
		return err
	}
	return d.write(rpcOpFrame, payload)
}

// serve answers pings until the socket closes.
//
// This goroutine exists for one reason and it is not elegance: Discord sends
// PING roughly every 30s and expects a PONG. A client that never reads never
// answers, Discord considers it dead and closes the socket, and the presence
// silently stops updating with no error anywhere. Reading is also what keeps
// the receive buffer from filling and blocking our own writes.
func (d *discordConn) serve(done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		default:
		}
		// A long read deadline so this is not a spin loop, and a reset to none
		// afterwards would be wrong anyway: the next read is the one that
		// should be interruptible.
		_ = d.c.SetReadDeadline(time.Now().Add(30 * time.Second))
		op, body, err := readRPCFrame(d.r)
		if err != nil {
			return // socket closed or timed out; either way there is nothing to read
		}
		if op == rpcOpPing {
			if err := d.write(rpcOpPong, body); err != nil {
				return
			}
		}
	}
}

func (d *discordConn) Close() error {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.c.Close()
}

// --- what gets shown --------------------------------------------------------

// presenceWanted is what should be on the user's profile right now.
//
// Pos and Dur are captured here but deliberately NOT part of the change test
// (see sameAs). The progress bar is absolute timestamps, so publishing twice
// with the same track and the same duration is publishing the same picture —
// and comparing Pos would make every pair of updates unequal, because it moves
// every frame, so the dedup would silently never fire.
type presenceWanted struct {
	Details string
	State   string
	Pos     float64
	Dur     float64
}

func (w presenceWanted) sameAs(o presenceWanted) bool {
	return w.Details == o.Details && w.State == o.State && w.Dur == o.Dur
}

// Discord's documented cap for details and state.
const presenceTextLimit = 128

// presenceFor builds the presence for a transport state.
func presenceFor(st State) presenceWanted {
	w := presenceWanted{
		Details: truncate(st.Track.Title, presenceTextLimit),
		Pos:     st.Pos,
		Dur:     st.Dur,
	}
	// "Paused" in place of the channel rather than as a third line: Discord
	// allows only details and state, and a paused track showing its channel
	// looks like it is still playing to anyone glancing at the profile.
	if st.Paused {
		w.State = "Paused"
		return w
	}
	w.State = truncate(presenceStateLine(st.Track), presenceTextLimit)
	return w
}

// presenceStateLine is the second line: who it is by, or where in a series.
//
// The S02E07 form is library.go's, not a second convention written here. Two
// spellings of the same episode number is exactly the drift this codebase's
// tests exist to prevent.
func presenceStateLine(t Track) string {
	episode := ""
	switch {
	case t.Episode > 0 && t.Season > 0:
		episode = fmt.Sprintf("S%02dE%02d", t.Season, t.Episode)
	case t.Episode > 0:
		episode = fmt.Sprintf("E%02d", t.Episode)
	case t.Season > 0:
		episode = fmt.Sprintf("Season %d", t.Season)
	}
	if episode == "" {
		return t.ChannelText()
	}
	if t.Series == "" {
		return episode
	}
	return t.Series + " · " + episode
}

// --- the publisher ----------------------------------------------------------

// presenceMinInterval is the floor between two published updates.
//
// Derived, not guessed: Discord allows five activity updates per twenty
// seconds, which is one per four seconds, so a five-second floor means at most
// four per twenty. Sitting inside the documented limit is the whole
// requirement — going over does not error, it just makes Discord drop updates,
// and the presence then lags behind the player with nothing to show for it.
//
// Held keys are the reason it exists at all: `<` and `>` seek 60 seconds and
// autorepeat at the key rate, which is far more updates than a track has
// moments.
const presenceMinInterval = 5 * time.Second

// presence owns the connection and decides what actually gets sent.
//
// Every method is safe on a nil *presence, which is how "Discord is not
// running" is represented: a nil publisher rather than a disabled flag, so no
// call site has to check. The alternative — a bool plus four `if enabled`
// guards — is four places to forget one.
type presence struct {
	conn  *discordConn
	appID string
	pid   int

	// now is injectable so the interval test does not have to sleep.
	now func() time.Time

	mu      sync.Mutex
	last    presenceWanted
	haveOne bool
	lastAt  time.Time
	nonce   int
	dead    bool

	out       chan *rpcActivity
	done      chan struct{}
	closed    chan struct{}
	closeOnce sync.Once
}

// newPresence connects and handshakes. A nil presence and a nil error means
// Discord is simply not there, which is not an error worth stopping playback
// for; the reason is returned so an explicit --discord can explain it.
func newPresence(appID string) (*presence, error) {
	conn, _, err := dialDiscord(500 * time.Millisecond)
	if err != nil {
		return nil, err
	}
	if err := conn.handshake(appID); err != nil {
		_ = conn.Close()
		return nil, err
	}
	p := &presence{
		conn:   conn,
		appID:  appID,
		pid:    os.Getpid(),
		now:    time.Now,
		out:    make(chan *rpcActivity, 4),
		done:   make(chan struct{}),
		closed: make(chan struct{}),
	}
	go p.writer()
	go conn.serve(p.done)
	return p, nil
}

// writer owns the socket. Everything Set does is handed over here rather than
// written inline, because Set is called from the render loop and a unix socket
// write is a syscall on a frame budget.
//
// The channel is buffered and the send is non-blocking: when it is full the
// update is dropped, and dropping is correct here because the next Set
// supersedes it. A blocking send would queue stale positions forever and
// publish them after the track had moved on.
func (p *presence) writer() {
	defer close(p.closed)
	for act := range p.out {
		p.mu.Lock()
		p.nonce++
		nonce := strconv.Itoa(p.nonce)
		p.mu.Unlock()
		if err := p.conn.setActivity(p.pid, act, nonce); err != nil {
			// The socket is unusable. Mark it dead so later calls stop trying,
			// and give up: reconnecting mid-track would race the render loop
			// for a socket that is probably gone for good.
			p.mu.Lock()
			p.dead = true
			p.mu.Unlock()
			return
		}
	}
}

// Set publishes, unless nothing visible changed or the floor has not elapsed.
func (p *presence) Set(w presenceWanted) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return
	}
	if p.haveOne && (w.sameAs(p.last) || p.now().Sub(p.lastAt) < presenceMinInterval) {
		p.mu.Unlock()
		return
	}
	p.last, p.haveOne, p.lastAt = w, true, p.now()
	p.mu.Unlock()

	// Now, at send time, not at capture time: the start timestamp is
	// `now - position`, and a queued update that went out a second late should
	// describe where the player is, not where it was.
	start := p.now().Add(-time.Duration(w.Pos * float64(time.Second)))
	act := &rpcActivity{
		Details: w.Details,
		State:   w.State,
		Type:    2, // listening
	}
	// No end timestamp when the duration is unknown. A live stream and a library
	// scan that failed to probe both read as 0 here, and an end in the past
	// makes Discord draw a full red bar on a track that has barely started.
	if w.Dur > 0 {
		act.Timestamps = &rpcTimestamps{
			Start: start.Unix(),
			End:   start.Add(time.Duration(w.Dur * float64(time.Second))).Unix(),
		}
	} else {
		act.Timestamps = &rpcTimestamps{Start: start.Unix()}
	}

	select {
	case p.out <- act:
	default:
		// Superseded. See writer.
	}
}

// Clear removes the activity. Best effort: dropping the connection does the
// same thing, so a failure here costs nothing.
func (p *presence) Clear() {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.dead {
		p.mu.Unlock()
		return
	}
	p.haveOne = false
	p.mu.Unlock()
	select {
	case p.out <- nil:
	default:
	}
}

// Close clears the activity and hangs up.
//
// Bounded, because this runs from the deferred cleanup at quit and every other
// defer on that path is also waiting to run — a presence Close that blocked on
// a wedged socket would make `q` feel like the program had hung.
func (p *presence) Close() {
	if p == nil {
		return
	}
	p.closeOnce.Do(func() {
		p.Clear()
		close(p.done)
		close(p.out)
		select {
		case <-p.closed:
		case <-time.After(500 * time.Millisecond):
		}
		_ = p.conn.Close()
	})
}
