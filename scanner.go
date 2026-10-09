package pst

import "bytes"

// Scanner pulls OSC 7501 sequences out of a pty output stream incrementally.
// It is for emulators and tools that do not already have a VT parser;
// emulators that do should call [ParseBody] from their OSC dispatch instead.
//
// A Scanner only observes: it never modifies or consumes the caller's bytes.
// Its zero value is ready to use. It is not safe for concurrent use.
type Scanner struct {
	state scanState
	buf   []byte
}

type scanState uint8

const (
	scanGround  scanState = iota
	scanEsc               // saw ESC
	scanOSC               // inside an OSC that is, or may still become, OSC 7501
	scanOSCEsc            // saw ESC inside scanOSC
	scanSkip              // inside another OSC, or an OSC 7501 that grew too long
	scanSkipEsc           // saw ESC inside scanSkip
)

const (
	esc = 0x1b
	bel = 0x07
	can = 0x18
	sub = 0x1a
)

// Feed scans b and calls fn once for each complete OSC 7501 sequence, from
// ESC ] through the terminator (ESC \ or BEL). Sequences may be split across
// any number of Feed calls. The slice passed to fn is only valid until fn
// returns. Sequences longer than 4096 bytes are dropped, and the Scanner
// resynchronizes at the next ESC. All other sequences are ignored. A CAN or
// SUB byte cancels a sequence in progress, as in a VT parser.
func (s *Scanner) Feed(b []byte, fn func(seq []byte)) {
	for i := 0; i < len(b); i++ {
		c := b[i]
		switch s.state {
		case scanGround:
			j := bytes.IndexByte(b[i:], esc)
			if j < 0 {
				return
			}
			i += j
			s.state = scanEsc
		case scanEsc:
			s.afterEsc(c)
		case scanOSC:
			switch c {
			case bel:
				s.buf = append(s.buf, c)
				s.finish(fn)
			case esc:
				s.state = scanOSCEsc
			case can, sub:
				s.state = scanGround
			default:
				s.buf = append(s.buf, c)
				if n := len(s.buf); n <= 1+len(oscPST) && s.buf[n-1] != ("\x1b" + oscPST)[n-1] {
					s.state = scanSkip
				} else if n > MaxSequenceBytes {
					s.state = scanSkip
				}
			}
		case scanOSCEsc:
			if c == '\\' {
				s.buf = append(s.buf, esc, c)
				s.finish(fn)
			} else {
				s.afterEsc(c)
			}
		case scanSkip:
			switch c {
			case bel, can, sub:
				s.state = scanGround
			case esc:
				s.state = scanSkipEsc
			}
		case scanSkipEsc:
			if c == '\\' {
				s.state = scanGround
			} else {
				s.afterEsc(c)
			}
		}
	}
}

// afterEsc handles the byte following an ESC that starts a new sequence.
func (s *Scanner) afterEsc(c byte) {
	switch c {
	case ']':
		s.buf = append(s.buf[:0], esc, ']')
		s.state = scanOSC
	case esc:
		s.state = scanEsc
	default:
		s.state = scanGround
	}
}

func (s *Scanner) finish(fn func([]byte)) {
	s.state = scanGround
	if len(s.buf) > MaxSequenceBytes || !bytes.HasPrefix(s.buf, []byte("\x1b"+oscPST)) {
		return
	}
	if fn != nil {
		fn(s.buf)
	}
}
