package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"syscall"
	"time"
)

// Synced ASCII playback: one ffmpeg process is the single clock for BOTH video
// and audio.
//
// Why one process: mpv-audio + ffmpeg-video on separate clocks drift from the
// first second and never recover. A single demux/decode emitting both streams
// makes sync structural instead of something to correct for. Audio is written
// to a FIFO that mpv reads; video arrives on our pipe.
//
// Why -re: without it ffmpeg decodes and pushes as fast as the CPU allows, so
// the ASCII ran ahead of the music (reported as "playing fastforward"). -re
// paces input at native frame rate, which is what makes wall-clock pacing in
// Next() meaningful.
//
// Why we hold the FIFO open O_RDWR: ffmpeg blocks opening a FIFO for write
// until a reader appears, and mpv sees EOF if the last writer closes. Holding
// it ourselves keeps both ends alive for the life of the process.

const (
	videoFps   = 12
	audioRate  = 44100
	audioChans = 2
)

// SyncPlayer plays a track: mpv owns audio, ffmpeg optionally owns video.
//
// musicOnly is the audio-with-no-video case, and it changes where the position
// comes from rather than adding a second player. Video mode has two children on
// two clocks and derives a position from the frame count; music mode has one
// child that will simply be asked. See Seconds.
type SyncPlayer struct {
	ff        *exec.Cmd
	vidOut    io.ReadCloser
	audio     *exec.Cmd
	frame     []byte
	primed    []byte // first frame, read before audio started
	primedSet bool
	frames    int
	paused    bool
	ipc       *mpvIPC
	sockPath  string
	cols      int
	rows      int
	fps       int
	startAt   float64 // media offset this player was rebuilt at

	musicOnly bool
	clock     posClock
}

// startSyncPlayer plays video (ffmpeg, -re paced) and audio (mpv) with a shared
// start timestamp.
//
// Sync strategy, and why it is not one process: a single ffmpeg writing both a
// video pipe and an audio FIFO deadlocks, because it blocks on the second
// output while the first pipe waits for a reader (verified: 0 bytes produced).
// So ffmpeg owns video only, mpv owns audio only, and both begin at the same
// media position -- ffmpeg reads from the head of the stream and mpv is given
// --start=<seconds>. Since ffmpeg paces with -re at native frame rate, the frame
// we render for media time T is displayed at wall-clock T.
func startSyncPlayer(videoURL, audioURL string, cols, rows, fps int, mute bool, mode ColorMode, glyph GlyphMode) (*SyncPlayer, error) {
	return startSyncPlayerAt(videoURL, audioURL, cols, rows, fps, mute, 0, mode, glyph)
}

// startSyncPlayerAt is startSyncPlayer with a resume offset, used after a
// terminal resize so playback continues instead of restarting.
func startSyncPlayerAt(videoURL, audioURL string, cols, rows, fps int, mute bool, startAt float64, mode ColorMode, glyph GlyphMode) (*SyncPlayer, error) {
	// Video first, and the first frame is read inside startVideoTap: mpv must not
	// start until the video clock is already running, or the two begin at
	// different wall-clock instants and video leads by however long the socket
	// handshake took. That ordering is the whole reason this is a function call
	// rather than two independent starts.
	vt, err := startVideoTap(videoURL, cols, rows, fps, startAt, mode, glyph)
	if err != nil {
		return nil, err
	}

	audio, ipc, sock, err := startMpv(audioURL, mute, startAt)
	if err != nil {
		vt.kill()
		return nil, fmt.Errorf("mpv start: %w", err)
	}

	return &SyncPlayer{
		ff:        vt.cmd,
		vidOut:    vt.out,
		audio:     audio,
		frame:     make([]byte, vt.size),
		ipc:       ipc,
		sockPath:  sock,
		primed:    vt.first,
		primedSet: true,
		cols:      cols,
		rows:      rows,
		fps:       fps,
		startAt:   startAt,
	}, nil
}

// startAudioOnly is music mode: mpv and nothing else.
//
// No ffmpeg, because there is no video to decode, and because decoding a stream
// this program is going to throw away is the most expensive thing it could do on
// a low-end machine. The visualizer gets its audio from a separate LevelTap over
// a separately resolved URL -- one consumer per grant, since a googlevideo URL
// is effectively single-use (see AGENT.MD).
func startAudioOnly(audioURL string, mute bool, startAt float64) (*SyncPlayer, error) {
	audio, ipc, sock, err := startMpv(audioURL, mute, startAt)
	if err != nil {
		return nil, fmt.Errorf("mpv start: %w", err)
	}
	return &SyncPlayer{
		audio:     audio,
		ipc:       ipc,
		sockPath:  sock,
		startAt:   startAt,
		musicOnly: true,
	}, nil
}

// videoTap is the ffmpeg half of video playback: the process, its pipe, and the
// first frame already read off it.
type videoTap struct {
	cmd   *exec.Cmd
	out   io.ReadCloser
	first []byte
	size  int
}

// kill retires the process without waiting on the pipe.
//
// Deliberately SIGKILL and not a graceful close: Close on the read end of a pipe
// with a read in flight waits for that read to finish, and the read is waiting on
// ffmpeg, which is blocked on a full pipe. See SyncPlayer.Close for the full
// account of that deadlock.
func (vt *videoTap) kill() {
	if vt == nil {
		return
	}
	if vt.cmd != nil && vt.cmd.Process != nil {
		_ = vt.cmd.Process.Kill()
	}
	if vt.cmd != nil {
		_ = vt.cmd.Wait()
	}
}

// startVideoTap launches ffmpeg for the video pipe and returns once frame 1 is in
// hand, which is the moment the video clock starts running.
//
// scale straight to the character grid with flags=area. Area averaging is a
// proper box filter: every source pixel contributes.
//
// The previous approach scaled to cols*2 x rows*2 and then sampled every
// second pixel in Go. That threw away 3 of every 4 pixels, which is
// aliasing, not downsampling -- the image looked soft and blocky.
//
// unsharp adds a little edge contrast back, which is what makes the result
// read as "sharp" rather than uniformly grey.
//
// Colour path: output rgb24 at DOUBLE height, because each cell holds two
// stacked pixels (the half-block trick). Mono path: gray at cell height.
func startVideoTap(videoURL string, cols, rows, fps int, startAt float64, mode ColorMode, glyph GlyphMode) (*videoTap, error) {
	var filter, pixFmt string
	frameBytes := 0
	if mode == ColorNone {
		filter = fmt.Sprintf(
			"fps=%d,scale=%d:%d:flags=area,unsharp=5:5:0.7:5:5:0.0,format=gray",
			fps, cols, rows)
		pixFmt = "gray"
		frameBytes = cols * rows
	} else {
		// Half-block mode gets double height so each cell holds two pixels.
		// One-pixel mode (used when the terminal renders U+2580 double-width)
		// matches the grid exactly.
		outRows := rows
		if glyph == GlyphHalf {
			outRows = rows * 2
		}
		filter = fmt.Sprintf("fps=%d,scale=%d:%d:flags=area,format=rgb24",
			fps, cols, outRows)
		pixFmt = "rgb24"
		frameBytes = cols * outRows * 3
	}

	// One ffmpeg, ONE output: the video frames on our stdout pipe.
	//
	// Writing audio from this same process deadlocks: ffmpeg blocks on the
	// second output while the first pipe is blocked waiting for us, and nothing
	// ever arrives (verified: 0 bytes). Audio therefore belongs to mpv, which
	// gets its own URL. Sync comes from --start below, not from one process.
	//
	// The audio URL is deliberately NOT passed to ffmpeg as an input. It is
	// never mapped or decoded here, but ffmpeg still opens it at startup, and a
	// googlevideo URL is effectively single-use: opening it twice spends the
	// grant and mpv's own request comes back "403 Forbidden (access denied)".
	// That is exactly how it failed — ffmpeg held the only good copy of the
	// audio stream and threw it away. One input, one consumer.
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
	}
	if startAt > 0 {
		// Fast seek on the video input; mpv gets the same offset via --start.
		args = append(args, "-ss", fmt.Sprintf("%.3f", startAt))
	}
	args = append(args,
		"-re", // pace to native frame rate; without it ASCII races ahead
		"-i", videoURL)
	args = append(args,
		"-map", "0:v:0",
		"-vf", filter,
		"-pix_fmt", pixFmt,
		"-f", "rawvideo", "pipe:1",
	)
	ff := exec.Command("ffmpeg", args...)
	ff.Stderr = os.Stderr
	vidOut, err := ff.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := ff.Start(); err != nil {
		return nil, fmt.Errorf("ffmpeg start: %w", err)
	}

	// Read the first frame BEFORE returning. ffmpeg with -re emits frames on
	// wall-clock, so the caller starting audio the moment this returns puts both
	// children at media time 0 together.
	first := make([]byte, frameBytes)
	if _, err := io.ReadFull(vidOut, first); err != nil {
		vt := &videoTap{cmd: ff, out: vidOut, size: frameBytes}
		vt.kill()
		return nil, fmt.Errorf("read first frame: %w", err)
	}

	return &videoTap{cmd: ff, out: vidOut, first: first, size: frameBytes}, nil
}

// startMpv launches mpv on the audio stream and connects to its IPC socket.
//
// mpv creates and binds its own IPC socket; we connect to it afterwards. A
// connection failure is not fatal: playback still works, it simply cannot be
// measured or seeked from here.
func startMpv(audioURL string, mute bool, startAt float64) (*exec.Cmd, *mpvIPC, string, error) {
	sockPath := ipcSocketPath()

	mpvArgs := []string{"--no-config", "--no-video", fmt.Sprintf("--start=%.3f", startAt)}
	if mute {
		mpvArgs = append(mpvArgs, "--mute=yes")
	}
	// --idle is deliberately NOT set.
	//
	// mpv --idle=yes survives the end of a file and waits for another one, so it
	// never exits, so the mpv process never signals that a track ended. The old
	// render loop got away with it because ffmpeg's EOF ended playback instead,
	// but a player that listens for the audio ending in order to advance a queue
	// then hangs on the last frame of every track. Track end has to be
	// observable, so mpv has to exit.
	mpvArgs = append(mpvArgs, "--input-ipc-server="+sockPath, audioURL)
	audio := exec.Command("mpv", mpvArgs...)
	audio.Stdout = io.Discard
	audio.Stderr = os.Stderr
	if err := audio.Start(); err != nil {
		os.Remove(sockPath)
		return nil, nil, "", err
	}

	ipc, err := dialIPC(sockPath, 4*time.Second)
	if err != nil {
		ipc = nil
	}
	return audio, ipc, sockPath, nil
}

// posClock holds the last position mpv reported.
//
// It holds rather than extrapolating. At the rate the music pump polls, a
// 40ms-stale clock is invisible, and extrapolating would be actively wrong
// during a pause: the children are SIGSTOPped, mpv's reading correctly stops
// advancing, and a wall-clock estimate would keep counting straight through the
// freeze and come back overstating the position by the length of the pause.
type posClock struct {
	mu   sync.Mutex
	pos  float64
	seen bool
}

// update records a reading. A failed read is ignored rather than zeroing the
// clock, so a dropped IPC reply cannot make the HUD jump back to 0:00.
func (c *posClock) update(pos float64, ok bool) {
	if !ok {
		return
	}
	c.mu.Lock()
	c.pos, c.seen = pos, true
	c.mu.Unlock()
}

func (c *posClock) now() float64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.pos
}

// started reports whether any reading has landed. mpv reports position 0 while it
// is still probing the stream, and a clock that shows 0:00 for the first half
// second of every seek reads as a seek that did not happen.
func (c *posClock) started() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seen
}

// Seconds reports the media position reached so far.
//
// Two clocks, two answers. Video mode tracks the frame count, because ffmpeg is
// paced by -re at native frame rate and frames rendered divided by fps is an
// accurate position -- no probing needed, which matters because probing a
// googlevideo URL burns it (see resolveMedia).
//
// Music mode asks mpv, because there is no ffmpeg to count frames and mpv will
// answer directly. That is not a downgrade: it is the sound card's own clock,
// reported by the process making the sound.
//
// startAt is added in the video path because a rebuilt player starts decoding
// mid-stream at -ss <startAt>: its frame counter begins at zero while the media
// it is decoding is at 300s. Without the offset this reported 0, checkSync read
// that as 300s of drift against mpv, and seeked the audio back to the beginning
// -- so every terminal resize rewound the track. The same applied to every seek.
// Music mode does not need it, because mpv reports absolute position itself.
func (s *SyncPlayer) Seconds() float64 {
	if s.musicOnly {
		return s.clock.now()
	}
	if s.fps <= 0 {
		return s.startAt
	}
	return s.startAt + float64(s.frames)/float64(s.fps)
}

// pollPosition refreshes the cached position from mpv.
//
// The only caller is the music pump, and it polls on the render tick rather than
// having Seconds reach for the socket. Seconds is called from the render loop,
// and a socket round trip there is a stall of unknown length on a connection that
// can block: a busy mpv answers late, and the frame budget is gone. Polling
// somewhere else turns that into one missed tick instead of a dropped frame.
func (s *SyncPlayer) pollPosition(timeout time.Duration) (float64, bool) {
	if s.ipc == nil {
		return 0, false
	}
	pos, ok := s.ipc.timePos(timeout)
	s.clock.update(pos, ok)
	return pos, ok
}

// ClockStarted reports whether mpv has ever reported a position. False means it
// is still probing, and the position is not yet meaningful.
func (s *SyncPlayer) ClockStarted() bool { return s.clock.started() }

// SyncReport describes one drift measurement.
type SyncReport struct {
	VideoPos  float64
	AudioPos  float64
	Drift     float64 // AudioPos - VideoPos
	Corrected bool
}

// driftCorrectThreshold is how far the two streams may diverge before acting.
// Too small and ordinary jitter causes constant audible seeking; too large and
// the error becomes noticeable. 250ms is roughly the point where lip-sync
// error is obvious.
const driftCorrectThreshold = 0.25

// checkSync compares the video position (frames delivered) against mpv's actual
// playback position and corrects the audio when they diverge.
//
// This is the part that structural sync cannot do. ffmpeg paces video on the
// system clock while mpv paces audio on the sound card's clock, so the two drift
// apart over a long track no matter how carefully they are started. Measuring
// and nudging is the only fix.
//
// The audio is corrected rather than the video: video is a live pipe that
// cannot be seeked cheaply, whereas an mpv seek is cheap. It is rare (only on
// real drift) and small.
//
// In music mode it is a no-op, and not because it was skipped: there is one
// clock and it is mpv's, so the two positions being compared would be the same
// number. Reporting a drift of zero from a subtraction of a value with itself
// would look like a passing measurement rather than an absent one.
func (s *SyncPlayer) checkSync() (SyncReport, bool) {
	v := s.Seconds()
	if s.musicOnly || s.paused || s.ipc == nil {
		return SyncReport{VideoPos: v}, false
	}
	a, ok := s.ipc.timePos(400 * time.Millisecond)
	if !ok {
		return SyncReport{VideoPos: v}, false
	}
	rep := SyncReport{VideoPos: v, AudioPos: a, Drift: a - v}
	// Ignore nonsense before the track has really started: mpv reports the
	// position of a stream that is still probing, and seeking there would jump.
	if v < 0.5 || a < 0.5 {
		return rep, false
	}
	if absFloat(rep.Drift) < driftCorrectThreshold {
		return rep, false
	}
	if err := s.ipc.seek(v); err != nil {
		return rep, false
	}
	rep.Corrected = true
	return rep, true
}

func absFloat(f float64) float64 {
	if f < 0 {
		return -f
	}
	return f
}

// Next returns the next frame as raw grayscale bytes, one per character cell.
//
// The first call returns the frame already read while starting playback (that
// frame is media time 0, the same moment mpv begins). After that, reading a
// full frame from a -re paced pipe blocks at exactly the native rate, so the
// frame for media time T is displayed at wall-clock T -- which is what keeps
// the ASCII locked to the music.
//
// Frames are returned as bytes, not strings: the renderer maps bytes to
// characters only for cells that actually changed, so pre-converting every
// cell would throw away most of that work.
func (s *SyncPlayer) Next() ([]byte, error) {
	if s.primedSet {
		s.primedSet = false
		copy(s.frame, s.primed)
	} else if _, err := io.ReadFull(s.vidOut, s.frame); err != nil {
		return nil, err
	}
	s.frames++
	return s.frame, nil
}

// childReapTimeout bounds how long the render loop will wait for a child to be
// reaped before giving up on it.
//
// Long enough that a healthy ffmpeg (killed microseconds earlier) is always
// reaped inside it, short enough that a child wedged in the kernel cannot freeze
// the player. What "give up" means is that the wait moves to a goroutine of its
// own and the loop carries on; the old generation's channels are about to be
// dropped, so a straggler cannot corrupt the new one.
const childReapTimeout = 2 * time.Second

// Close retires both children and everything attached to them.
//
// Three things here are load-bearing, and each was measured rather than reasoned
// about. Getting any of them wrong is a hang, not a crash — the render loop parks
// inside this function and the quit key does nothing, because it never reaches a
// select again.
//
//  1. Kill before closing the pipe. vidOut is an *os.File from StdoutPipe, and Go's
//     poller makes Close on a file with a read in flight *wait* for that read to
//     finish. The read waits on a writer; the writer is ffmpeg blocked on a full
//     pipe; the only thing draining that pipe is the read Close is waiting on.
//     Two goroutines, one pipe, neither can move.
//
//  2. SIGCONT before SIGKILL. flow.go SIGSTOPs both children when the terminal
//     stops reading, and a rebuild can land while they are stopped. SIGKILL is
//     normally delivered to a stopped process, but resuming first costs nothing
//     and removes any doubt about which signal the child actually acts on.
//
//  3. Never Wait on mpv here. start() already has a goroutine waiting on it, to
//     close s.audio. Two Wait calls on one *exec.Cmd is a second waiter queued
//     behind a process whose exit we do not control, and that is where this froze:
//     the loop sat in audio.Wait with mpv still in state T. Close kills it; the
//     goroutine that owns it reaps it.
//
// ffmpeg does have to be reaped before the loop moves on, because it holds the
// pipe — but even that is bounded, for the same reason. Wait closes the parent end
// of the pipe itself, so there is nothing left for this function to close.
func (s *SyncPlayer) Close() {
	for _, cmd := range []*exec.Cmd{s.ff, s.audio} {
		if cmd == nil || cmd.Process == nil {
			continue
		}
		_ = cmd.Process.Signal(syscall.SIGCONT)
		_ = cmd.Process.Kill()
	}
	if s.ff != nil {
		reaped := make(chan struct{})
		go func() {
			_ = s.ff.Wait()
			close(reaped)
		}()
		select {
		case <-reaped:
		case <-time.After(childReapTimeout):
		}
	}
	if s.ipc != nil {
		s.ipc.close()
	}
	if s.sockPath != "" {
		os.Remove(s.sockPath)
	}
}

// makeFifo creates a unique FIFO in $TMPDIR. Uses syscall.Mknod's fifo mode so
// there is no dependency on mkfifo(1) or a temp-file library.
func makeFifo() (string, error) {
	dir := os.TempDir()
	name := fmt.Sprintf("%s/vspz-yt-cli-%d-%d.audio", dir, os.Getpid(), time.Now().UnixNano())
	if err := syscall.Mkfifo(name, 0o600); err != nil {
		return "", fmt.Errorf("mkfifo: %w", err)
	}
	return name, nil
}

// ensure bufio stays referenced for future stream helpers
var _ = bufio.NewReader
