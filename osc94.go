package pst

import (
	"bytes"
	"slices"
	"strconv"
	"sync/atomic"
)

// OSC94 returns the ConEmu OSC 9;4 progress sequence that mirrors r, for
// terminals that support 9;4 but not OSC 7501. State numbers are ConEmu's
// (https://conemu.github.io/en/AnsiEscapeCodes.html#ConEmu_specific_OSC):
//
//	working with progress N   ESC ] 9 ; 4 ; 1 ; N ST
//	working, indeterminate    ESC ] 9 ; 4 ; 3 ST
//	blocked                   ESC ] 9 ; 4 ; 4 ; N ST  (N = progress, or 0)
//	error                     ESC ] 9 ; 4 ; 2 ST
//	idle, done, clear         ESC ] 9 ; 4 ; 0 ST
//
// 9;4 has a single indicator per terminal, so only mirror the root record;
// [Status] does this with [WithOSC94]. r must pass [Report.Validate].
func OSC94(r Report, opts ...EncodeOption) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	c := applyEncodeOptions(opts)
	return appendOSC94(make([]byte, 0, c.frameLen(oscOSC94)+5), r, c), nil
}

func appendOSC94(dst []byte, r Report, c encodeConfig) []byte {
	dst = slices.Grow(dst, c.frameLen(oscOSC94)+5)
	dst = c.appendOpen(dst, oscOSC94)
	pct, hasPct := r.Progress.Value()
	switch r.State {
	case StateWorking:
		if hasPct {
			dst = strconv.AppendInt(append(dst, '1', ';'), int64(pct), 10)
		} else {
			dst = append(dst, '3')
		}
	case StateBlocked:
		dst = strconv.AppendInt(append(dst, '4', ';'), int64(pct), 10)
	case StateError:
		dst = append(dst, '2')
	default:
		dst = append(dst, '0')
	}
	return c.appendClose(dst)
}

// OSC94Mapper maps OSC 9;4 progress to the root record, which the spec allows
// terminals to do. As the spec recommends, it stops mapping once the terminal
// has received an OSC 7501 report (call Saw7501), until the next full reset
// (call Reset): a mapped 9;4 would otherwise wipe out the kind and msg of a
// real report.
//
// The mapping is the inverse of [OSC94]:
//
//	0        clear  (see below)
//	1;N      working, progress N (clamped to 0-100; missing or invalid N is indeterminate)
//	2        error
//	3        working, indeterminate
//	4[;N]    blocked, progress N if present
//
// State 0 means "no progress to show", so it maps to clear rather than idle:
// idle would leave behind a record the program never asked for. A clear
// without an id removes every record, but until the first OSC 7501 report the
// mapped root record is the only one that can exist, so in practice it
// removes just that.
//
// The zero value is ready to use, and an OSC94Mapper is safe for concurrent
// use.
type OSC94Mapper struct {
	saw atomic.Bool
}

// Map converts the parameters of an OSC 9 sequence that follow "9;", such as
// "4;1;40", into a root-record report. It returns false if params is not a
// recognized 9;4 sequence or mapping has stopped.
func (m *OSC94Mapper) Map(params []byte) (Report, bool) {
	if m.saw.Load() {
		return Report{}, false
	}
	rest, ok := bytes.CutPrefix(params, []byte("4;"))
	if !ok {
		return Report{}, false
	}
	st, pr, hasPr := bytes.Cut(rest, []byte(";"))
	n, nOK := atoiPositive(pr)
	if !hasPr {
		nOK = false
	}
	switch string(st) {
	case "0":
		return Report{State: StateClear}, true
	case "1":
		r := Report{State: StateWorking}
		if nOK {
			r.Progress = Percent(min(n, 100))
		}
		return r, true
	case "2":
		return Report{State: StateError}, true
	case "3":
		return Report{State: StateWorking}, true
	case "4":
		r := Report{State: StateBlocked}
		if nOK {
			r.Progress = Percent(min(n, 100))
		}
		return r, true
	}
	return Report{}, false
}

// Saw7501 records that the terminal received an OSC 7501 report, which stops
// mapping until Reset.
func (m *OSC94Mapper) Saw7501() { m.saw.Store(true) }

// Reset resumes mapping. Call it on a full reset (RIS).
func (m *OSC94Mapper) Reset() { m.saw.Store(false) }

// atoiPositive parses a short unsigned decimal, saturating large values.
func atoiPositive(b []byte) (int, bool) {
	if len(b) == 0 {
		return 0, false
	}
	n := 0
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		if n < 1000 {
			n = n*10 + int(c-'0')
		}
	}
	return n, true
}
