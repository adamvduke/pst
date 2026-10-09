//go:build !(linux || darwin || freebsd || netbsd || openbsd || dragonfly || windows)

package pst

import (
	"context"
	"os"
)

const hasTerminfo = false

// IsTerminal reports whether f is a terminal. It always returns false on this
// platform.
func IsTerminal(f *os.File) bool { return false }

func detect(ctx context.Context, tty *os.File) (Support, error) {
	return Unknown, ErrNoTTY
}
