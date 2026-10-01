package main

import (
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

// Raw-mode terminal handling without cgo or x/term. The standard library
// exposes no termios API on Linux, so this issues the ioctls directly.
//
// Rejected: golang.org/x/term — a real dependency for two ioctl calls we can
// make ourselves, in a program whose selling point is a zero-dependency binary.

type termios struct {
	Iflag  uint32
	Oflag  uint32
	Cflag  uint32
	Lflag  uint32
	Line   uint8
	Cc     [32]uint8
	Ispeed uint32
	Ospeed uint32
}

const (
	ioctlReadTermios  = 0x5401 // TCGETS
	ioctlWriteTermios = 0x5402 // TCSETS
	echoBit           = 0x00000008
	icanonBit         = 0x00000002
)

// makeRaw disables canonical mode and echo so keys arrive one at a time
// without Enter. Returns a restore func and an error if stdin is not a TTY
// (e.g. piped input), in which case the caller should keep line mode.
func makeRaw(f *os.File) (func(), error) {
	var old termios
	if err := ioctl(f.Fd(), ioctlReadTermios, unsafe.Pointer(&old)); err != nil {
		return nil, err
	}
	raw := old
	raw.Lflag &^= echoBit | icanonBit
	raw.Cc[6] = 1 // VMIN: one byte is enough to return
	raw.Cc[5] = 0 // VTIME: no read timeout, blocking
	if err := ioctl(f.Fd(), ioctlWriteTermios, unsafe.Pointer(&raw)); err != nil {
		return nil, err
	}
	return func() {
		saved := old
		_ = ioctl(f.Fd(), ioctlWriteTermios, unsafe.Pointer(&saved))
	}, nil
}

func ioctl(fd uintptr, req uintptr, arg unsafe.Pointer) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}

// winsize mirrors struct winsize from <asm-generic/ioctls.h>.
type winsize struct {
	Row, Col, Xpixel, Ypixel uint16
}

const tiocgwinsz = 0x5413 // TIOCGWINSZ

// termSize returns the terminal's character grid dimensions.
// Rejected: golang.org/x/term — one ioctl is not worth a dependency.
func termSize(f *os.File) (cols, rows int, err error) {
	var ws winsize
	if err := ioctl(f.Fd(), tiocgwinsz, unsafe.Pointer(&ws)); err != nil {
		return 0, 0, err
	}
	if ws.Col == 0 || ws.Row == 0 {
		return 0, 0, fmt.Errorf("terminal reported a zero-sized window")
	}
	return int(ws.Col), int(ws.Row), nil
}
