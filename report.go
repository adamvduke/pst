package pst

import (
	"errors"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// State is the value of the state key.
type State string

// States defined by the spec. StateClear is not a state a record can hold; it
// removes records.
const (
	StateIdle    State = "idle"
	StateWorking State = "working"
	StateDone    State = "done"
	StateBlocked State = "blocked"
	StateError   State = "error"
	StateClear   State = "clear"
)

// Valid reports whether s is one of the states defined by the spec.
func (s State) Valid() bool {
	switch s {
	case StateIdle, StateWorking, StateDone, StateBlocked, StateError, StateClear:
		return true
	}
	return false
}

// hasProgress reports whether progress is meaningful with s.
func (s State) hasProgress() bool { return s == StateWorking || s == StateBlocked }

// Kind says what a blocked record is waiting for.
type Kind string

// Kinds defined by the spec.
const (
	KindNone       Kind = ""
	KindPermission Kind = "permission" // approval to do something
	KindQuestion   Kind = "question"   // the user must type an answer
	KindAuth       Kind = "auth"       // a login, token, or credential
)

// Valid reports whether k is one of the non-empty kinds defined by the spec.
func (k Kind) Valid() bool {
	switch k {
	case KindPermission, KindQuestion, KindAuth:
		return true
	}
	return false
}

// Progress is an optional percentage. The zero value means absent, which the
// spec defines as indeterminate: busy, with no percentage to report.
type Progress struct {
	v  int
	ok bool
}

// Percent returns a Progress of n percent. Out-of-range values are kept as-is
// so that [Report.Validate] can report them; they are never clamped.
func Percent(n int) Progress { return Progress{v: n, ok: true} }

// Value returns the percentage and whether one is set.
func (p Progress) Value() (int, bool) { return p.v, p.ok }

// String returns "indeterminate" or the percentage, such as "40%".
func (p Progress) String() string {
	if !p.ok {
		return "indeterminate"
	}
	return strconv.Itoa(p.v) + "%"
}

// Limits from the spec. A terminal discards any report that exceeds one.
const (
	MaxSequenceBytes     = 4096 // whole sequence, OSC through ST
	MaxKeyBytes          = 16
	MaxMsgBytes          = 2048 // decoded
	MaxMsgEncodedBytes   = 2732
	MaxTitleBytes        = 192 // decoded
	MaxTitleEncodedBytes = 256
	MaxAppBytes          = 32
	MaxIDBytes           = 128
	MaxSegmentBytes      = 32
	MaxIDDepth           = 8
	MaxRecords           = 256 // per terminal
	MinRecords           = 64  // a terminal must support at least this many
)

// Report is one OSC 7501 report. Title and Msg hold plain UTF-8; base64 is
// applied by the encoder and removed by the parser.
type Report struct {
	State    State
	ID       string   // "" addresses the root record
	Kind     Kind     // StateBlocked only
	Progress Progress // StateWorking and StateBlocked only
	App      string
	Title    string
	Msg      string
}

// Validation sentinels. A *ValidationError matches the sentinel for its
// problem and, for state, id, kind, progress, and app, the sentinel for its
// field too, so errors.Is(err, ErrID) holds for an id that is too long.
var (
	ErrState       = errors.New("pst: invalid state")
	ErrID          = errors.New("pst: invalid id")
	ErrApp         = errors.New("pst: invalid app")
	ErrKind        = errors.New("pst: invalid kind")
	ErrProgress    = errors.New("pst: invalid progress")
	ErrControlChar = errors.New("pst: control character in text")
	ErrLimit       = errors.New("pst: limit exceeded")
	ErrUTF8        = errors.New("pst: invalid UTF-8 in text")
)

// ValidationError describes why a report cannot be sent.
type ValidationError struct {
	Field  string // "state", "id", "kind", "progress", "app", "title", "msg", or "sequence"
	Reason string
	Err    error // one of the Err* sentinels
}

func (e *ValidationError) Error() string { return "pst: invalid " + e.Field + ": " + e.Reason }

// Unwrap returns the problem sentinel and, if different, the field sentinel.
func (e *ValidationError) Unwrap() []error {
	var field error
	switch e.Field {
	case "state":
		field = ErrState
	case "id":
		field = ErrID
	case "kind":
		field = ErrKind
	case "progress":
		field = ErrProgress
	case "app":
		field = ErrApp
	}
	if field == nil || field == e.Err {
		return []error{e.Err}
	}
	return []error{e.Err, field}
}

func invalid(field string, err error, reason string) error {
	return &ValidationError{Field: field, Reason: reason, Err: err}
}

// Validate reports whether a terminal would apply r exactly as given. It
// rejects everything a terminal would discard or silently ignore, so a caller
// never loses part of a report without knowing.
func (r Report) Validate() error {
	if !r.State.Valid() {
		return invalid("state", ErrState, fmt.Sprintf("unknown state %q", truncateForError(string(r.State))))
	}
	if r.ID != "" {
		if reason, limit := checkID(r.ID); reason != "" {
			if limit {
				return invalid("id", ErrLimit, reason)
			}
			return invalid("id", ErrID, reason)
		}
	}
	if r.Kind != KindNone {
		if !r.Kind.Valid() {
			return invalid("kind", ErrKind, fmt.Sprintf("unknown kind %q", truncateForError(string(r.Kind))))
		}
		if r.State != StateBlocked {
			return invalid("kind", ErrKind, "kind requires state blocked")
		}
	}
	if v, ok := r.Progress.Value(); ok {
		if !r.State.hasProgress() {
			return invalid("progress", ErrProgress, "progress requires state working or blocked")
		}
		if v < 0 || v > 100 {
			return invalid("progress", ErrProgress, fmt.Sprintf("%d is outside 0-100", v))
		}
	}
	if r.App != "" {
		if len(r.App) > MaxAppBytes {
			return invalid("app", ErrLimit, "exceeds 32 bytes")
		}
		if !segmentChars(r.App) {
			return invalid("app", ErrApp, "characters outside [A-Za-z0-9_.+-]")
		}
	}
	if err := checkText("title", r.Title, MaxTitleBytes); err != nil {
		return err
	}
	if err := checkText("msg", r.Msg, MaxMsgBytes); err != nil {
		return err
	}
	// Unreachable with the limits above (the largest legal report is about
	// 3300 bytes), but the spec states the limit separately, so check it.
	if len(oscPST)+1+r.bodyLen()+2 > MaxSequenceBytes {
		return invalid("sequence", ErrLimit, "exceeds 4096 bytes")
	}
	return nil
}

func checkText(field, s string, max int) error {
	if !utf8.ValidString(s) {
		return invalid(field, ErrUTF8, "not valid UTF-8")
	}
	if len(s) > max {
		return invalid(field, ErrLimit, fmt.Sprintf("exceeds %d bytes", max))
	}
	for _, r := range s {
		if isControl(r) {
			return invalid(field, ErrControlChar, fmt.Sprintf("contains control character %U", r))
		}
	}
	return nil
}

// isControl reports whether r is a control character as the spec defines it:
// U+0000-U+001F, U+007F, and U+0080-U+009F.
func isControl(r rune) bool { return r < 0x20 || (r >= 0x7f && r <= 0x9f) }

// checkID returns a non-empty reason if id is not a valid non-empty id, and
// whether the problem is a broken limit rather than a grammar violation.
func checkID(id string) (reason string, limit bool) {
	if len(id) > MaxIDBytes {
		return "exceeds 128 bytes", true
	}
	depth, start := 0, 0
	for i := 0; i <= len(id); i++ {
		if i < len(id) && id[i] != '/' {
			continue
		}
		seg := id[start:i]
		start = i + 1
		depth++
		switch {
		case depth > MaxIDDepth:
			return "more than 8 segments", true
		case len(seg) == 0:
			return "empty segment", false
		case len(seg) > MaxSegmentBytes:
			return "segment exceeds 32 bytes", true
		case !segmentChars(seg):
			return "characters outside [A-Za-z0-9_.+-]", false
		}
	}
	return "", false
}

// validSegment reports whether s matches [A-Za-z0-9_.+-]{1,32}, the grammar of
// both id segments and app.
func validSegment[T string | []byte](s T) bool {
	return len(s) >= 1 && len(s) <= MaxSegmentBytes && segmentChars(s)
}

func segmentChars[T string | []byte](s T) bool {
	for i := 0; i < len(s); i++ {
		if !isSegmentByte(s[i]) {
			return false
		}
	}
	return true
}

func isSegmentByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
		c == '_' || c == '.' || c == '+' || c == '-'
}

func truncateForError(s string) string {
	if len(s) > 32 {
		return s[:32] + "..."
	}
	return s
}
