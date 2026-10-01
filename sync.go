package main

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"os/exec"
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

// SyncPlayer plays a track as ASCII video with audio in sync.
type SyncPlayer struct {
	ff        *exec.Cmd
	vidOut    io.ReadCloser
	audio     *exec.Cmd
	frame     []byte
	primed    []byte // first frame, read before audio started
	primedSet bool
	frames    int
	cols      int
	rows      int
	fps       int
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
func startSyncPlayer(videoURL, audioURL string, cols, rows, fps int, mute bool, mode ColorMode) (*SyncPlayer, error) {
	return startSyncPlayerAt(videoURL, audioURL, cols, rows, fps, mute, 0, mode)
}

// startSyncPlayerAt is startSyncPlayer with a resume offset, used after a
// terminal resize so playback continues instead of restarting.
func startSyncPlayerAt(videoURL, audioURL string, cols, rows, fps int, mute bool, startAt float64, mode ColorMode) (*SyncPlayer, error) {
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
	var filter, pixFmt string
	frameBytes := 0
	if mode == ColorNone {
		filter = fmt.Sprintf(
			"fps=%d,scale=%d:%d:flags=area,unsharp=5:5:0.7:5:5:0.0,format=gray",
			fps, cols, rows)
		pixFmt = "gray"
		frameBytes = cols * rows
	} else {
		filter = fmt.Sprintf("fps=%d,scale=%d:%d:flags=area,format=rgb24",
			fps, cols, rows*2)
		pixFmt = "rgb24"
		frameBytes = cols * rows * 2 * 3
	}

	// One ffmpeg, ONE output: the grayscale frames on our stdout pipe.
	//
	// Writing audio from this same process deadlocks: ffmpeg blocks on the
	// second output while the first pipe is blocked waiting for us, and nothing
	// ever arrives (verified: 0 bytes). Audio therefore belongs to mpv, which
	// gets its own URL. Sync comes from --start below, not from one process.
	args := []string{
		"-hide_banner", "-loglevel", "error", "-nostdin",
	}
	if startAt > 0 {
		// Fast seek on both inputs so video and audio resume at the same media
		// position after a resize.
		ss := fmt.Sprintf("%.3f", startAt)
		args = append(args, "-ss", ss, "-re", "-i", videoURL, "-ss", ss, "-i", audioURL)
	} else {
		args = append(args,
			"-re", // pace to native frame rate; without it ASCII races ahead
			"-i", videoURL)
		args = append(args, "-i", audioURL)
	}
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

	// Read the first frame BEFORE starting audio. ffmpeg with -re emits frames
	// on wall-clock, so once frame 1 is in hand the video clock is running;
	// starting mpv at position 0 here puts both at media time 0 together.
	first := make([]byte, frameBytes)
	if _, err := io.ReadFull(vidOut, first); err != nil {
		ff.Process.Kill()
		ff.Wait()
		return nil, fmt.Errorf("read first frame: %w", err)
	}

	mpvArgs := []string{"--no-config", "--no-video", fmt.Sprintf("--start=%.3f", startAt)}
	if mute {
		mpvArgs = append(mpvArgs, "--mute=yes")
	}
	mpvArgs = append(mpvArgs, audioURL)
	audio := exec.Command("mpv", mpvArgs...)
	audio.Stdout = io.Discard
	audio.Stderr = os.Stderr
	if err := audio.Start(); err != nil {
		ff.Process.Kill()
		ff.Wait()
		return nil, fmt.Errorf("mpv start: %w", err)
	}

	return &SyncPlayer{
		ff:        ff,
		vidOut:    vidOut,
		audio:     audio,
		frame:     make([]byte, frameBytes),
		primed:    first,
		primedSet: true,
		cols:      cols,
		rows:      rows,
		fps:       fps,
	}, nil
}

// Seconds reports the media position reached so far, tracked from the frame
// count. Because ffmpeg is paced by -re at native frame rate, frames rendered
// divided by fps is an accurate position -- no probing needed, which matters
// because probing a googlevideo URL burns it (see resolveMediaPair).
func (s *SyncPlayer) Seconds() float64 {
	if s.fps <= 0 {
		return 0
	}
	return float64(s.frames) / float64(s.fps)
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

// WaitAudioEnd blocks until mpv finishes, i.e. the track ended.
func (s *SyncPlayer) WaitAudioEnd() error {
	if s.audio == nil {
		return nil
	}
	return s.audio.Wait()
}

func (s *SyncPlayer) Close() {
	if s.vidOut != nil {
		s.vidOut.Close()
	}
	if s.ff != nil && s.ff.Process != nil {
		s.ff.Process.Kill()
		s.ff.Wait()
	}
	if s.audio != nil && s.audio.Process != nil {
		s.audio.Process.Kill()
		s.audio.Wait()
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
