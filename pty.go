package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
	"unsafe"
)

// Minimal PTY so interactive CLIs (ytfzf -> fzf) can run from a program.
// Standard library only: allocating a pty without cgo means raw ioctls, which
// is a few dozen lines and beats pulling in creack/pty for one call.
//
// Rejected: github.com/creack/pty — a real dependency to open one file
// descriptor, in a project whose selling point is zero module dependencies.

const (
	tiocsptlck = 0x40045431 // unlock pty slave
	tiocGPTN   = 0x80045430 // get pty number
)

// runUnderPTY runs name with args attached to a pty, sends a newline once the
// child has had time to draw its UI (to confirm an fzf selection), and returns
// everything written to the pty.
func runUnderPTY(name string, args []string) ([]byte, error) {
	ptmx, err := os.OpenFile("/dev/ptmx", os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open ptmx: %w", err)
	}
	defer ptmx.Close()

	// unlockpt
	var unlock int32
	if err := ioctlPtr(ptmx.Fd(), tiocsptlck, unsafe.Pointer(&unlock)); err != nil {
		return nil, fmt.Errorf("unlockpt: %w", err)
	}
	// ptsname via TIOCGPTN
	var n uint32
	if err := ioctlPtr(ptmx.Fd(), tiocGPTN, unsafe.Pointer(&n)); err != nil {
		return nil, fmt.Errorf("ptsname: %w", err)
	}
	slave, err := os.OpenFile(fmt.Sprintf("/dev/pts/%d", n), os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, fmt.Errorf("open slave: %w", err)
	}
	defer slave.Close()

	cmd := exec.Command(name, args...)
	cmd.Stdin = slave
	cmd.Stdout = slave
	cmd.Stderr = slave
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid:  true,
		Setctty: true,
		Ctty:    0,
	}
	cmd.Env = append(os.Environ(), "TERM=xterm-256color")

	if err := cmd.Start(); err != nil {
		return nil, err
	}

	// Close our slave copy so EOF is seen when the child exits.
	slave.Close()

	var buf bytes.Buffer
	done := make(chan error, 1)
	go func() {
		chunk := make([]byte, 8192)
		for {
			n, err := ptmx.Read(chunk)
			if n > 0 {
				buf.Write(chunk[:n])
			}
			if err != nil {
				done <- nil
				return
			}
		}
	}()

	// fzf opens fullscreen and waits for input. Confirm the first result so it
	// exits and ytfzf prints its JSON payload.
	go func() {
		time.Sleep(4 * time.Second)
		ptmx.Write([]byte("\r"))
	}()

	waitErr := cmd.Wait()
	select {
	case <-done:
	case <-time.After(500 * time.Millisecond):
	}
	if waitErr != nil {
		// fzf exits non-zero when its selection is dismissed; the JSON we
		// want may still be in the buffer, so let the caller decide.
		return buf.Bytes(), nil
	}
	return buf.Bytes(), nil
}

func ioctlPtr(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
