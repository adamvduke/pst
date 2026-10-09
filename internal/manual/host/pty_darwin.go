//go:build manual

package main

import (
	"bytes"
	"fmt"
	"os"
	"syscall"
	"unsafe"
)

const (
	ioctlGetTermios = syscall.TIOCGETA
	ioctlSetTermios = syscall.TIOCSETA
)

// openPTY opens a pseudo-terminal pair with posix_openpt semantics:
// /dev/ptmx, then grantpt, unlockpt, and ptsname as ioctls.
func openPTY() (master, slave *os.File, err error) {
	m, err := os.OpenFile("/dev/ptmx", os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		return nil, nil, err
	}
	fd := m.Fd()
	var name [128]byte
	for _, step := range []struct {
		req uintptr
		arg uintptr
	}{
		{syscall.TIOCPTYGRANT, 0},
		{syscall.TIOCPTYUNLK, 0},
		{syscall.TIOCPTYGNAME, uintptr(unsafe.Pointer(&name[0]))},
	} {
		if err := ioctl(fd, step.req, step.arg); err != nil {
			m.Close()
			return nil, nil, fmt.Errorf("pty ioctl %#x: %w", step.req, err)
		}
	}
	path := string(name[:bytes.IndexByte(name[:], 0)])
	s, err := os.OpenFile(path, os.O_RDWR|syscall.O_NOCTTY, 0)
	if err != nil {
		m.Close()
		return nil, nil, err
	}
	return m, s, nil
}
