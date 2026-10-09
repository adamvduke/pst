//go:build windows

package pst

import (
	"context"
	"fmt"
	"os"
	"syscall"
)

const hasTerminfo = false

const (
	enableProcessedInput            = 0x0001
	enableLineInput                 = 0x0002
	enableEchoInput                 = 0x0004
	enableVirtualTerminalInput      = 0x0200
	enableVirtualTerminalProcessing = 0x0004
	waitObject0                     = 0x00000000
	waitTimeout                     = 0x00000102
)

var procSetConsoleMode = syscall.NewLazyDLL("kernel32.dll").NewProc("SetConsoleMode")

func setConsoleMode(h syscall.Handle, mode uint32) error {
	r, _, err := procSetConsoleMode.Call(uintptr(h), uintptr(mode))
	if r == 0 {
		return err
	}
	return nil
}

// IsTerminal reports whether f is a terminal. On Unix it checks that the
// termios get-ioctl succeeds; on Windows, that GetConsoleMode does.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	var mode uint32
	return syscall.GetConsoleMode(syscall.Handle(f.Fd()), &mode) == nil
}

// detect is best effort on Windows: it needs a console that understands VT
// input, such as Windows Terminal.
func detect(ctx context.Context, tty *os.File) (Support, error) {
	in := tty
	if in == nil {
		f, err := os.OpenFile("CONIN$", os.O_RDWR, 0)
		if err != nil {
			return Unknown, fmt.Errorf("%w: %v", ErrNoTTY, err)
		}
		defer f.Close()
		in = f
	}
	out, err := os.OpenFile("CONOUT$", os.O_RDWR, 0)
	if err != nil {
		if tty == nil {
			return Unknown, fmt.Errorf("%w: %v", ErrNoTTY, err)
		}
		out = tty
	} else {
		defer out.Close()
	}

	inH, outH := syscall.Handle(in.Fd()), syscall.Handle(out.Fd())
	var oldIn, oldOut uint32
	if err := syscall.GetConsoleMode(inH, &oldIn); err != nil {
		return Unknown, fmt.Errorf("%w: %v", ErrNoTTY, err)
	}
	if err := syscall.GetConsoleMode(outH, &oldOut); err != nil {
		return Unknown, fmt.Errorf("%w: %v", ErrNoTTY, err)
	}
	rawIn := oldIn&^(enableLineInput|enableEchoInput|enableProcessedInput) | enableVirtualTerminalInput
	if err := setConsoleMode(inH, rawIn); err != nil {
		return Unknown, fmt.Errorf("pst: setting console input mode: %w", err)
	}
	defer setConsoleMode(inH, oldIn) //nolint:errcheck // best effort
	if oldOut&enableVirtualTerminalProcessing == 0 {
		if err := setConsoleMode(outH, oldOut|enableVirtualTerminalProcessing); err != nil {
			return Unknown, fmt.Errorf("pst: setting console output mode: %w", err)
		}
		defer setConsoleMode(outH, oldOut) //nolint:errcheck // best effort
	}

	if _, err := out.Write(append(Query(), da1...)); err != nil {
		return Unknown, fmt.Errorf("pst: writing query: %w", err)
	}

	var c replyClassifier
	buf := make([]byte, 256)
	for ctx.Err() == nil {
		ev, err := syscall.WaitForSingleObject(inH, 100)
		if err != nil {
			return c.decided, fmt.Errorf("pst: waiting for reply: %w", err)
		}
		if ev == waitTimeout {
			continue
		}
		if ev != waitObject0 {
			return c.decided, fmt.Errorf("pst: waiting for reply: unexpected result %#x", ev)
		}
		n, err := syscall.Read(inH, buf)
		if err != nil {
			return c.decided, fmt.Errorf("pst: reading reply: %w", err)
		}
		if decided, done := c.Feed(buf[:n]); done {
			return decided, nil
		}
	}
	return c.decided, nil
}
