//go:build linux || darwin || freebsd || netbsd || openbsd || dragonfly

package pst

import (
	"context"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const hasTerminfo = true

func ioctlTermios(fd uintptr, req uintptr, t *syscall.Termios) error {
	_, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, uintptr(unsafe.Pointer(t)))
	if errno != 0 {
		return errno
	}
	return nil
}

// IsTerminal reports whether f is a terminal. On Unix it checks that the
// termios get-ioctl succeeds; on Windows, that GetConsoleMode does.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	sc, err := f.SyscallConn()
	if err != nil {
		return false
	}
	var t syscall.Termios
	var ioErr error
	if err := sc.Control(func(fd uintptr) { ioErr = ioctlTermios(fd, ioctlGetTermios, &t) }); err != nil {
		return false
	}
	return ioErr == nil
}

func detect(ctx context.Context, tty *os.File) (Support, error) {
	if tty == nil {
		f, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
		if err != nil {
			return Unknown, fmt.Errorf("%w: %v", ErrNoTTY, err)
		}
		defer f.Close()
		tty = f
	}
	// Fd puts the descriptor in blocking mode, which the VMIN/VTIME read loop
	// below relies on: SetReadDeadline does not work on every platform's tty.
	fd := tty.Fd()
	var old syscall.Termios
	if err := ioctlTermios(fd, ioctlGetTermios, &old); err != nil {
		return Unknown, fmt.Errorf("%w: %v", ErrNoTTY, err)
	}
	raw := old
	raw.Lflag &^= syscall.ICANON | syscall.ECHO | syscall.ISIG | syscall.IEXTEN
	raw.Iflag &^= syscall.IXON | syscall.ICRNL
	raw.Cc[syscall.VMIN] = 0
	raw.Cc[syscall.VTIME] = 1 // reads return after at most 100ms
	if err := ioctlTermios(fd, ioctlSetTermios, &raw); err != nil {
		return Unknown, fmt.Errorf("pst: setting raw mode: %w", err)
	}
	defer ioctlTermios(fd, ioctlSetTermios, &old) //nolint:errcheck // best effort; nothing better to do

	if err := writeAll(fd, Query()); err != nil {
		return Unknown, err
	}
	if err := writeAll(fd, []byte(da1)); err != nil {
		return Unknown, err
	}

	var c replyClassifier
	buf := make([]byte, 256)
	for ctx.Err() == nil {
		n, err := syscall.Read(int(fd), buf)
		if err == syscall.EINTR || err == syscall.EAGAIN {
			continue
		}
		if err != nil {
			return c.decided, fmt.Errorf("pst: reading reply: %w", err)
		}
		if decided, done := c.Feed(buf[:n]); done {
			return decided, nil
		}
	}
	return c.decided, nil
}

func writeAll(fd uintptr, b []byte) error {
	for len(b) > 0 {
		n, err := syscall.Write(int(fd), b)
		if err == syscall.EINTR || err == syscall.EAGAIN {
			continue
		}
		if err != nil {
			return fmt.Errorf("pst: writing query: %w", err)
		}
		b = b[n:]
	}
	return nil
}
