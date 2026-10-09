package pst

import (
	"context"
	"errors"
	"os"
	"time"
)

// Support is the result of feature detection.
type Support int

// Detection results.
const (
	Unknown     Support = iota // no answer in time, or no terminal to ask
	Supported                  // the terminal replied to the query
	Unsupported                // the terminal answered DA1 without replying
)

func (s Support) String() string {
	switch s {
	case Supported:
		return "supported"
	case Unsupported:
		return "unsupported"
	}
	return "unknown"
}

// ErrNoTTY means there was no terminal to detect support on.
var ErrNoTTY = errors.New("pst: no terminal")

// DefaultDetectTimeout bounds [Detect] when its context has no deadline.
const DefaultDetectTimeout = 200 * time.Millisecond

// da1 is primary device attributes (CSI c). Every terminal answers it, so its
// reply arriving first means the query went unanswered.
const da1 = "\x1b[c"

// Detect asks the terminal whether it supports the protocol. It writes the
// query followed by DA1 and waits for the replies: Supported if the query's
// reply arrives before DA1's, Unsupported if DA1's arrives first. After
// deciding Supported it keeps reading until DA1's reply also arrives, so the
// reply does not leak into the shell's input.
//
// tty must be a terminal opened for reading and writing; if it is nil, Detect
// opens /dev/tty (CONIN$ and CONOUT$ on Windows), which works even when stdin
// and stdout are redirected. On Unix, a tty passed in is left in blocking
// mode, a side effect of [os.File.Fd]. The terminal is put in raw mode for the duration
// and restored afterwards. If ctx has no deadline, [DefaultDetectTimeout]
// applies. On timeout Detect returns Unknown and a nil error; with no
// terminal it returns Unknown and an error wrapping [ErrNoTTY].
//
// Detection is optional: sending reports without it is safe, because
// terminals ignore OSC sequences they do not know. Detect reads from the
// terminal, so do not call it while something else is reading input.
func Detect(ctx context.Context, tty *os.File) (Support, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultDetectTimeout)
		defer cancel()
	}
	return detect(ctx, tty)
}

// replyClassifier watches terminal input for the replies to the query and DA1.
type replyClassifier struct {
	state    classState
	buf      []byte // OSC body bytes, up to len("7501;?")
	csiFirst byte   // first parameter byte of the current CSI
	decided  Support
	done     bool
}

type classState uint8

const (
	classGround classState = iota
	classEsc
	classOSC
	classOSCEsc
	classCSI
)

const queryReplyPrefix = "7501;?"

// Feed consumes terminal input. It returns the decision so far and whether
// the classifier has seen everything it is waiting for: DA1's reply in either
// case. Once done, further input is ignored.
func (c *replyClassifier) Feed(b []byte) (decided Support, done bool) {
	for _, x := range b {
		if c.done {
			break
		}
		c.feedByte(x)
	}
	return c.decided, c.done
}

func (c *replyClassifier) feedByte(x byte) {
	switch c.state {
	case classGround:
		if x == esc {
			c.state = classEsc
		}
	case classEsc:
		c.afterEsc(x)
	case classOSC:
		switch x {
		case bel:
			c.endOSC()
		case esc:
			c.state = classOSCEsc
		case can, sub:
			c.state = classGround
		default:
			if len(c.buf) < len(queryReplyPrefix) {
				c.buf = append(c.buf, x)
			}
		}
	case classOSCEsc:
		if x == '\\' {
			c.endOSC()
		} else {
			c.afterEsc(x)
		}
	case classCSI:
		switch {
		case x == esc:
			c.state = classEsc
		case x == can || x == sub:
			c.state = classGround
		case x >= 0x40 && x <= 0x7e: // final byte
			if x == 'c' && c.csiFirst == '?' {
				if c.decided == Unknown {
					c.decided = Unsupported
				}
				c.done = true
			}
			c.state = classGround
		case x >= 0x20 && x <= 0x3f: // parameter and intermediate bytes
			if c.csiFirst == 0 {
				c.csiFirst = x
			}
		default:
			c.state = classGround
		}
	}
}

func (c *replyClassifier) afterEsc(x byte) {
	switch x {
	case ']':
		c.buf = c.buf[:0]
		c.state = classOSC
	case '[':
		c.csiFirst = 0
		c.state = classCSI
	case esc:
		c.state = classEsc
	default:
		c.state = classGround
	}
}

func (c *replyClassifier) endOSC() {
	c.state = classGround
	if string(c.buf) == queryReplyPrefix && c.decided == Unknown {
		c.decided = Supported
	}
}
