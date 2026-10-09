//go:build darwin || freebsd || netbsd || openbsd || dragonfly

package pst

import "syscall"

const (
	ioctlGetTermios = syscall.TIOCGETA
	ioctlSetTermios = syscall.TIOCSETA
)
