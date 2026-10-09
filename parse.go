package pst

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Parse errors. Returned errors wrap one of these with detail, so test with
// errors.Is.
var (
	// ErrQuery means the body was the feature detection query, "?" possibly
	// followed by more. A supporting terminal replies with [Query]().
	ErrQuery = errors.New("pst: feature detection query")
	// ErrDiscarded means the report broke a limit, held base64 that does not
	// decode, or held text with a control character or invalid UTF-8.
	ErrDiscarded = errors.New("pst: report discarded")
	// ErrIgnored means the report had a missing or unknown state or an
	// invalid id.
	ErrIgnored = errors.New("pst: report ignored")
	// ErrNotReport means ParseSequence was given something other than an
	// OSC 7501 sequence.
	ErrNotReport = errors.New("pst: not an OSC 7501 sequence")
)

// maxBodyBytes is the longest body that fits in MaxSequenceBytes with the
// shortest framing (ESC ] 7501 ; body BEL).
const maxBodyBytes = MaxSequenceBytes - 1 - len(oscPST) - 1

// ParseSequence parses a whole sequence, ESC ] 7501 ; body ST, terminated by
// either ESC \ or BEL. It enforces the 4096-byte sequence limit.
//
// Any Report returned with a nil error passes [Report.Validate].
func ParseSequence(seq []byte) (Report, error) {
	if len(seq) > MaxSequenceBytes {
		return Report{}, fmt.Errorf("%w: sequence exceeds %d bytes", ErrDiscarded, MaxSequenceBytes)
	}
	if len(seq) < 1+len(oscPST) || seq[0] != 0x1b || string(seq[1:1+len(oscPST)]) != oscPST {
		return Report{}, ErrNotReport
	}
	body := seq[1+len(oscPST):]
	switch {
	case bytes.HasSuffix(body, []byte("\x1b\\")):
		body = body[:len(body)-2]
	case bytes.HasSuffix(body, []byte("\x07")):
		body = body[:len(body)-1]
	default:
		return Report{}, fmt.Errorf("%w: missing string terminator", ErrNotReport)
	}
	return parseBody(body)
}

// ParseBody parses the body of a report: the bytes between "7501;" and ST.
// Emulators with their own VT parser call it from OSC dispatch.
//
// ParseBody cannot see the framing, so it assumes the shortest one (BEL) and
// accepts bodies up to 4088 bytes. Callers whose parser saw an ESC \
// terminator and want the limit exact can reject bodies over 4087 bytes, or
// use [ParseSequence].
//
// All of the spec's lenient rules apply: malformed pairs are skipped, unknown
// keys are ignored, the last of a repeated key wins, an invalid kind,
// progress, or app is treated as absent, kind is dropped unless the state is
// blocked, and progress is dropped unless it is working or blocked.
//
// Any Report returned with a nil error passes [Report.Validate].
func ParseBody(body []byte) (Report, error) {
	if len(body) > maxBodyBytes {
		return Report{}, fmt.Errorf("%w: sequence exceeds %d bytes", ErrDiscarded, MaxSequenceBytes)
	}
	return parseBody(body)
}

type rawFields struct {
	state, id, kind, progress, app []byte
	title, msg                     string
}

func parseBody(body []byte) (Report, error) {
	if len(body) > 0 && body[0] == '?' {
		return Report{}, ErrQuery
	}
	var f rawFields
	rest := body
	for more := true; more; {
		var pair []byte
		if i := bytes.IndexByte(rest, ':'); i >= 0 {
			pair, rest = rest[:i], rest[i+1:]
		} else {
			pair, more = rest, false
		}
		if err := f.add(pair); err != nil {
			return Report{}, err
		}
	}
	return f.report()
}

// add records one pair. It returns an error only when the whole report must
// be discarded.
func (f *rawFields) add(pair []byte) error {
	eq := bytes.IndexByte(pair, '=')
	if eq < 0 {
		return nil
	}
	key, val := trimSpaceTab(pair[:eq]), trimSpaceTab(pair[eq+1:])
	if !isKey(key) || !isValue(val) {
		return nil
	}
	if len(key) > MaxKeyBytes {
		return fmt.Errorf("%w: key exceeds %d bytes", ErrDiscarded, MaxKeyBytes)
	}
	switch string(key) {
	case "state":
		f.state = val
	case "id":
		if reason, limit := checkID(string(val)); limit {
			return fmt.Errorf("%w: id %s", ErrDiscarded, reason)
		}
		f.id = val
	case "kind":
		f.kind = val
	case "progress":
		f.progress = val
	case "app":
		if len(val) > MaxAppBytes {
			return fmt.Errorf("%w: app exceeds %d bytes", ErrDiscarded, MaxAppBytes)
		}
		f.app = val
	case "title":
		s, err := decodeText("title", val, MaxTitleEncodedBytes, MaxTitleBytes)
		if err != nil {
			return err
		}
		f.title = s
	case "msg":
		s, err := decodeText("msg", val, MaxMsgEncodedBytes, MaxMsgBytes)
		if err != nil {
			return err
		}
		f.msg = s
	}
	return nil
}

func (f *rawFields) report() (Report, error) {
	var r Report
	switch string(f.state) {
	case "idle":
		r.State = StateIdle
	case "working":
		r.State = StateWorking
	case "done":
		r.State = StateDone
	case "blocked":
		r.State = StateBlocked
	case "error":
		r.State = StateError
	case "clear":
		r.State = StateClear
	case "":
		if f.state == nil {
			return Report{}, fmt.Errorf("%w: missing state", ErrIgnored)
		}
		fallthrough
	default:
		return Report{}, fmt.Errorf("%w: unknown state %q", ErrIgnored, truncateForError(string(f.state)))
	}
	if f.id != nil {
		id := string(f.id)
		if reason, _ := checkID(id); reason != "" {
			return Report{}, fmt.Errorf("%w: id %s", ErrIgnored, reason)
		}
		r.ID = id
	}
	if r.State == StateBlocked {
		if k := Kind(f.kind); k.Valid() {
			r.Kind = k
		}
	}
	if r.State.hasProgress() {
		if v, ok := parseProgress(f.progress); ok {
			r.Progress = Percent(v)
		}
	}
	if validSegment(f.app) {
		r.App = string(f.app)
	}
	r.Title, r.Msg = f.title, f.msg
	return r, nil
}

// parseProgress accepts canonical decimal 0-100 only: no sign, no leading
// zeros, no fraction.
func parseProgress(v []byte) (int, bool) {
	if len(v) == 0 || len(v) > 3 || (v[0] == '0' && len(v) > 1) {
		return 0, false
	}
	n := 0
	for _, c := range v {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + int(c-'0')
	}
	return n, n <= 100
}

// decodeText decodes a free-text value. Padding is optional, but must be
// correct when present.
func decodeText(field string, val []byte, encMax, decMax int) (string, error) {
	if len(val) > encMax {
		return "", fmt.Errorf("%w: %s exceeds %d encoded bytes", ErrDiscarded, field, encMax)
	}
	v := bytes.TrimRight(val, "=")
	if pad := len(val) - len(v); pad > 0 && (pad > 2 || len(val)%4 != 0) {
		return "", fmt.Errorf("%w: %s has invalid base64 padding", ErrDiscarded, field)
	}
	buf := make([]byte, base64.RawStdEncoding.DecodedLen(len(v)))
	n, err := base64.RawStdEncoding.Decode(buf, v)
	if err != nil {
		return "", fmt.Errorf("%w: %s is not valid base64", ErrDiscarded, field)
	}
	buf = buf[:n]
	if len(buf) > decMax {
		return "", fmt.Errorf("%w: %s exceeds %d decoded bytes", ErrDiscarded, field, decMax)
	}
	if !utf8.Valid(buf) {
		return "", fmt.Errorf("%w: %s is not valid UTF-8", ErrDiscarded, field)
	}
	s := string(buf)
	for _, r := range s {
		if isControl(r) {
			return "", fmt.Errorf("%w: %s contains control character %U", ErrDiscarded, field, r)
		}
	}
	return s, nil
}

func trimSpaceTab(b []byte) []byte {
	for len(b) > 0 && (b[0] == ' ' || b[0] == '\t') {
		b = b[1:]
	}
	for len(b) > 0 && (b[len(b)-1] == ' ' || b[len(b)-1] == '\t') {
		b = b[:len(b)-1]
	}
	return b
}

func isKey(k []byte) bool {
	if len(k) == 0 {
		return false
	}
	for _, c := range k {
		if c < 'a' || c > 'z' {
			return false
		}
	}
	return true
}

func isValue(v []byte) bool {
	for _, c := range v {
		if !isSegmentByte(c) && c != ',' && c != '/' && c != '=' {
			return false
		}
	}
	return true
}
