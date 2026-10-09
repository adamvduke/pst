package pst

import "os"

type tmuxOption struct{}

func (tmuxOption) applyStatus(c *statusConfig)             { c.enc.tmux = true }
func (tmuxOption) applyEncode(c encodeConfig) encodeConfig { c.tmux = true; return c }

// WithTmuxPassthrough wraps every sequence in tmux's DCS passthrough,
// ESC P tmux ; <sequence with ESC doubled> ESC \, so it reaches the terminal
// tmux runs in. This is not part of the spec, and works only when tmux has
// "set -g allow-passthrough on". It is never enabled automatically; see
// [InTmux].
func WithTmuxPassthrough() EncodeOption { return tmuxOption{} }

// InTmux reports whether the program appears to run inside tmux, judged by
// $TMUX. Use it to decide whether to pass [WithTmuxPassthrough]; this package
// never enables passthrough on its own.
func InTmux() bool { return os.Getenv("TMUX") != "" }
