package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"time"
)

// mpv control over its JSON IPC socket.
//
// Needed because the two streams cannot be kept in sync by construction:
// ffmpeg paces video on the system clock, mpv paces audio on the sound card's
// clock. Those clocks drift relative to each other, so a fixed relationship set
// up at startup diverges over a long track. The only way to correct drift is to
// read the audio side's actual position and nudge it.
//
// mpv CREATES AND BINDS the socket; we are the client that connects to it. This
// was initially backwards, which showed up as mpv never connecting and the
// corrector silently doing nothing.

type mpvIPC struct {
	conn net.Conn
	r    *bufio.Reader
	next int
}

type ipcRequest struct {
	Command   []interface{} `json:"command"`
	RequestID int           `json:"request_id"`
}

type ipcReply struct {
	Data      *float64 `json:"data"`
	Error     string   `json:"error"`
	RequestID int      `json:"request_id"`
	Event     string   `json:"event"`
}

// ipcSocketPath returns a per-process socket path and clears any stale socket
// left by a previous crash, which would otherwise stop mpv from binding.
func ipcSocketPath() string {
	path := filepath.Join(os.TempDir(),
		fmt.Sprintf("vspz-yt-cli-%d.ipc", os.Getpid()))
	_ = os.Remove(path)
	return path
}

// dialIPC waits for mpv to create its socket and connects to it.
func dialIPC(path string, timeout time.Duration) (*mpvIPC, error) {
	deadline := time.Now().Add(timeout)
	for {
		if _, err := os.Stat(path); err == nil {
			c, derr := net.Dial("unix", path)
			if derr == nil {
				return &mpvIPC{conn: c, r: bufio.NewReader(c)}, nil
			}
		}
		if time.Now().After(deadline) {
			return nil, fmt.Errorf("mpv ipc socket never appeared")
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// ipcErr records why an IPC call failed. Debugging a silently-failing sync
// corrector is otherwise impossible: a failed query looked identical to a real
// reading of zero.
var ipcErr atomic.Value // string

// ipcLastErr returns the most recent IPC failure reason.
func ipcLastErr() string {
	if v, ok := ipcErr.Load().(string); ok {
		return v
	}
	return ""
}

func setIpcErr(format string, args ...interface{}) {
	ipcErr.Store(fmt.Sprintf(format, args...))
}

// parseTimePosReply extracts a position from one IPC line.
//
// Split out from timePos so the protocol handling can be tested without mpv.
// The important subtlety: mpv ALWAYS includes "error" in a reply, set to the
// string "success" when the call worked. Checking for a non-empty error field
// therefore rejects every successful reply -- which is exactly the bug that
// stopped the drift corrector from ever running.
func parseTimePosReply(line []byte, wantID int) (float64, bool, bool) {
	var rep ipcReply
	if err := json.Unmarshal(line, &rep); err != nil {
		return 0, false, false // not our problem; caller should keep reading
	}
	if rep.RequestID != wantID {
		return 0, false, false // an event or a stale reply
	}
	if (rep.Error != "" && rep.Error != "success") || rep.Data == nil {
		return 0, false, true
	}
	return *rep.Data, true, true
}

// timePos asks mpv where it actually is, in seconds. ok=false if it will not say.
func (i *mpvIPC) timePos(timeout time.Duration) (float64, bool) {
	if i == nil || i.conn == nil {
		setIpcErr("no connection")
		return 0, false
	}
	i.next++
	id := i.next
	req, _ := json.Marshal(ipcRequest{
		Command:   []interface{}{"get_property", "time-pos"},
		RequestID: id,
	})
	if _, err := i.conn.Write(append(req, '\n')); err != nil {
		setIpcErr("write: %v", err)
		return 0, false
	}
	if err := i.conn.SetReadDeadline(time.Now().Add(timeout)); err != nil {
		setIpcErr("deadline: %v", err)
		return 0, false
	}
	for {
		line, err := i.r.ReadBytes('\n')
		if err != nil {
			setIpcErr("read: %v (partial=%q)", err, string(line))
			return 0, false
		}
		pos, ok, done := parseTimePosReply(line, id)
		if !done {
			continue
		}
		if !ok {
			setIpcErr("reply rejected")
			return 0, false
		}
		setIpcErr("")
		return pos, true
	}
}

// seek moves playback to an absolute position. Used only to correct drift, so it
// is deliberately rare and never applied for small offsets.
func (i *mpvIPC) seek(sec float64) error {
	if i == nil || i.conn == nil {
		return fmt.Errorf("no ipc connection")
	}
	i.next++
	payload := map[string]interface{}{
		"command":    []interface{}{"seek", sec, "absolute+exact"},
		"request_id": i.next,
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if _, err := i.conn.Write(append(b, '\n')); err != nil {
		return err
	}
	_ = i.conn.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	// Drain the ack so the buffer does not fill with replies.
	for {
		line, err := i.r.ReadBytes('\n')
		if err != nil {
			return nil
		}
		var rep ipcReply
		if json.Unmarshal(line, &rep) == nil && rep.RequestID == i.next {
			if rep.Error != "" && rep.Error != "success" {
				return fmt.Errorf("seek: %s", rep.Error)
			}
			return nil
		}
	}
}

func (i *mpvIPC) close() {
	if i != nil && i.conn != nil {
		i.conn.Close()
	}
}

// formatPos is used in log lines; kept separate so the rounding is consistent.
func formatPos(f float64) string { return strconv.FormatFloat(f, 'f', 2, 64) }
