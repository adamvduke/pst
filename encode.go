package pst

import (
	"slices"
	"strconv"
)

const (
	oscPST   = "]7501;" // OSC introducer after ESC
	oscOSC94 = "]9;4;"
)

// Option configures a [Status].
type Option interface {
	applyStatus(*statusConfig)
}

// EncodeOption configures how a sequence is framed. Every EncodeOption is
// also an [Option], so it can be passed to [New].
type EncodeOption interface {
	Option
	applyEncode(encodeConfig) encodeConfig
}

type encodeConfig struct {
	bel  bool
	tmux bool
}

func applyEncodeOptions(opts []EncodeOption) encodeConfig {
	var c encodeConfig
	for _, o := range opts {
		if o != nil {
			c = o.applyEncode(c)
		}
	}
	return c
}

type belOption struct{}

func (belOption) applyStatus(c *statusConfig)             { c.enc.bel = true }
func (belOption) applyEncode(c encodeConfig) encodeConfig { c.bel = true; return c }

// WithBEL terminates sequences with BEL (0x07) instead of the default ST
// (ESC \). Both are valid; use BEL for terminals that mishandle ST.
func WithBEL() EncodeOption { return belOption{} }

// frameLen is the number of bytes the framing adds around a body that
// follows intro.
func (c encodeConfig) frameLen(intro string) int {
	n := 1 + len(intro) + 2 // ESC intro ... ESC \
	if c.bel {
		n--
	}
	if c.tmux {
		n += len("\x1bPtmux;") + 1 + len("\x1b\\") // prefix, doubled ESC, wrapper ST
		if !c.bel {
			n++ // doubled ESC of the inner ST
		}
	}
	return n
}

func (c encodeConfig) appendOpen(dst []byte, intro string) []byte {
	if c.tmux {
		dst = append(dst, "\x1bPtmux;\x1b"...)
	}
	dst = append(dst, 0x1b)
	return append(dst, intro...)
}

func (c encodeConfig) appendClose(dst []byte) []byte {
	switch {
	case c.bel:
		dst = append(dst, 0x07)
	case c.tmux:
		dst = append(dst, "\x1b\x1b\\"...)
	default:
		dst = append(dst, "\x1b\\"...)
	}
	if c.tmux {
		dst = append(dst, "\x1b\\"...)
	}
	return dst
}

// AppendBody appends the report body, the "state=...:msg=..." part between
// "7501;" and ST, to dst. It is meant for programs that frame the body
// themselves, for example with the terminfo Pst capability. Keys are written
// in a fixed order (state, id, kind, progress, app, title, msg) and empty
// optional keys are omitted. On error dst is returned unchanged.
func (r Report) AppendBody(dst []byte) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return dst, err
	}
	return r.appendBody(slices.Grow(dst, r.bodyLen())), nil
}

// AppendSequence appends the full escape sequence for r to dst. It does not
// allocate if dst has enough spare capacity. On error dst is returned
// unchanged.
func (r Report) AppendSequence(dst []byte, opts ...EncodeOption) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return dst, err
	}
	return r.appendSequence(dst, applyEncodeOptions(opts)), nil
}

// appendSequence appends the framed sequence for an already validated r.
func (r Report) appendSequence(dst []byte, c encodeConfig) []byte {
	dst = slices.Grow(dst, c.frameLen(oscPST)+r.bodyLen())
	dst = c.appendOpen(dst, oscPST)
	dst = r.appendBody(dst)
	return c.appendClose(dst)
}

// Encode returns the full escape sequence for r in a newly allocated slice.
func (r Report) Encode(opts ...EncodeOption) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	c := applyEncodeOptions(opts)
	return r.appendSequence(make([]byte, 0, c.frameLen(oscPST)+r.bodyLen()), c), nil
}

// Query returns the feature detection query, OSC 7501 ; ? ST. A supporting
// terminal replies with exactly the same bytes, so terminals use Query to
// build their reply too.
func Query(opts ...EncodeOption) []byte {
	c := applyEncodeOptions(opts)
	dst := make([]byte, 0, c.frameLen(oscPST)+1)
	dst = c.appendOpen(dst, oscPST)
	dst = append(dst, '?')
	return c.appendClose(dst)
}

// bodyLen returns the exact length appendBody will write.
func (r Report) bodyLen() int {
	n := len("state=") + len(r.State)
	if r.ID != "" {
		n += len(":id=") + len(r.ID)
	}
	if r.Kind != KindNone {
		n += len(":kind=") + len(r.Kind)
	}
	if v, ok := r.Progress.Value(); ok {
		n += len(":progress=") + decimalLen(v)
	}
	if r.App != "" {
		n += len(":app=") + len(r.App)
	}
	if r.Title != "" {
		n += len(":title=") + base64Len(len(r.Title))
	}
	if r.Msg != "" {
		n += len(":msg=") + base64Len(len(r.Msg))
	}
	return n
}

func (r Report) appendBody(dst []byte) []byte {
	dst = append(dst, "state="...)
	dst = append(dst, r.State...)
	if r.ID != "" {
		dst = append(dst, ":id="...)
		dst = append(dst, r.ID...)
	}
	if r.Kind != KindNone {
		dst = append(dst, ":kind="...)
		dst = append(dst, r.Kind...)
	}
	if v, ok := r.Progress.Value(); ok {
		dst = append(dst, ":progress="...)
		dst = strconv.AppendInt(dst, int64(v), 10)
	}
	if r.App != "" {
		dst = append(dst, ":app="...)
		dst = append(dst, r.App...)
	}
	if r.Title != "" {
		dst = append(dst, ":title="...)
		dst = appendBase64(dst, r.Title)
	}
	if r.Msg != "" {
		dst = append(dst, ":msg="...)
		dst = appendBase64(dst, r.Msg)
	}
	return dst
}

func decimalLen(v int) int {
	n := 1
	if v < 0 {
		n++
		v = -v
	}
	for v >= 10 {
		v /= 10
		n++
	}
	return n
}

func base64Len(n int) int { return (n + 2) / 3 * 4 }

const base64Alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/"

// appendBase64 appends standard padded base64 of s. It exists so that
// encoding a string never allocates, which base64.StdEncoding.AppendEncode
// cannot promise for a []byte(s) conversion.
func appendBase64(dst []byte, s string) []byte {
	i := 0
	for ; i+3 <= len(s); i += 3 {
		v := uint(s[i])<<16 | uint(s[i+1])<<8 | uint(s[i+2])
		dst = append(dst, base64Alphabet[v>>18&0x3f], base64Alphabet[v>>12&0x3f],
			base64Alphabet[v>>6&0x3f], base64Alphabet[v&0x3f])
	}
	switch len(s) - i {
	case 1:
		v := uint(s[i]) << 16
		dst = append(dst, base64Alphabet[v>>18&0x3f], base64Alphabet[v>>12&0x3f], '=', '=')
	case 2:
		v := uint(s[i])<<16 | uint(s[i+1])<<8
		dst = append(dst, base64Alphabet[v>>18&0x3f], base64Alphabet[v>>12&0x3f],
			base64Alphabet[v>>6&0x3f], '=')
	}
	return dst
}
