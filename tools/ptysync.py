#!/usr/bin/env python3
"""Drive a command under a real pty, implementing DEC 2026 the way Ghostty does.

Modes that ignore private sequences (a bare pty, most CI) will happily accept a
program that opens 2026 and never closes it, because nothing renders until the
process exits and by then everything has been written anyway. A terminal that
implements the mode buffers between `h` and `l` and presents nothing in between,
so the same program shows a black screen for the whole track.

This harness implements it, which is the only way to catch that class of bug
without a real terminal. It reports, on stderr, how many frames were presented
and whether the mode was still open at exit — the two numbers that separate
"playing" from "painted once at the end".

Usage: ptysync.py <seconds> <keyscript> [--snapshot] <cmd> [args...]

  12 "2.0:\\r,9.0:q" ./rasterbar play -a track.mp4

keyscript is a comma-separated list of "when:BYTES" steps. --snapshot dumps the
bytes still held in the 2026 buffer instead of the presented stream, which is how
you see what a real terminal would be withholding.
"""
import fcntl
import os
import pty
import re
import select
import struct
import sys
import termios
import time

SYNC = re.compile(rb"\x1b\[\?2026([hl])")


def main():
    args = sys.argv[1:]
    secs = float(args[0])
    script = args[1]
    rest = args[2:]
    snapshot = False
    if rest and rest[0] == "--snapshot":
        snapshot = True
        rest = rest[1:]
    argv = rest

    pid, fd = pty.fork()
    if pid == 0:
        os.environ["TERM"] = "xterm-256color"
        os.environ["COLORTERM"] = "truecolor"
        os.execvp(argv[0], argv)
        os._exit(127)

    fcntl.ioctl(fd, termios.TIOCSWINSZ, struct.pack("HHHH", 40, 100, 0, 0))

    steps = []
    for part in script.split(","):
        if not part:
            continue
        t, _, b = part.partition(":")
        steps.append((float(t), b.encode().decode("unicode_escape").encode("latin1")))

    presented = bytearray()   # what a terminal would actually have shown
    held = bytearray()         # the 2026 buffer
    buffering = False
    saw_open_at = None
    frames = 0

    start = time.time()
    idx = 0
    while time.time() - start < secs:
        now = time.time() - start
        while idx < len(steps) and steps[idx][0] <= now:
            os.write(fd, steps[idx][1])
            idx += 1
        r, _, _ = select.select([fd], [], [], 0.05)
        if fd in r:
            try:
                chunk = os.read(fd, 65536)
            except OSError:
                break
            if not chunk:
                break
            # Honour 2026: hold everything between h and l, present at l.
            pos = 0
            for m in SYNC.finditer(chunk):
                body = chunk[pos:m.start()]
                if buffering:
                    held += body
                else:
                    presented += body
                pos = m.end()
                if m.group(1) == b"h":
                    if not buffering:
                        saw_open_at = now
                    buffering = True
                else:
                    if buffering:
                        presented += held
                        held = bytearray()
                        frames += 1
                    buffering = False
            tail = chunk[pos:]
            if buffering:
                held += tail
            else:
                presented += tail

    try:
        os.kill(pid, 9)
    except ProcessLookupError:
        pass
    try:
        os.waitpid(pid, 0)
    except ChildProcessError:
        pass

    sys.stderr.write(
        f"frames presented: {frames}   still buffering at exit: {buffering}   "
        f"held bytes: {len(held)}\n")
    data = bytes(held if snapshot else presented)
    sys.stdout.buffer.write(data)


if __name__ == "__main__":
    main()