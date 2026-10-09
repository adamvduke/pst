package pst

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// TerminfoHasPst reports whether the terminfo entry for term defines the
// extended string capability Pst, which terminals that implement the protocol
// should advertise. If term is "", $TERM is used.
//
// A program that finds Pst may send reports without querying first. Absence
// is not evidence that the protocol is unsupported: the entry may be missing
// or stale, for example over ssh or inside a multiplexer. [Detect] is always
// authoritative.
//
// Entries are searched for as ncurses does: $TERMINFO, $HOME/.terminfo, each
// directory in $TERMINFO_DIRS (an empty entry means the system directories),
// then /etc/terminfo, /lib/terminfo, /usr/share/terminfo, /usr/lib/terminfo,
// and /usr/local/share/terminfo, trying both the letter and the hexadecimal
// subdirectory layouts. If no entry exists, the error wraps fs.ErrNotExist.
// On platforms without terminfo, such as Windows, it returns false, nil.
func TerminfoHasPst(term string) (bool, error) {
	if !hasTerminfo {
		return false, nil
	}
	if term == "" {
		term = os.Getenv("TERM")
	}
	if term == "" {
		return false, errors.New("pst: TERM is not set")
	}
	if term == "." || term == ".." || strings.ContainsAny(term, "/\\\x00") {
		return false, fmt.Errorf("pst: invalid terminal name %q", truncateForError(term))
	}
	data, err := readTerminfo(term)
	if err != nil {
		return false, err
	}
	return terminfoHasPst(data)
}

// maxTerminfoBytes caps how much of a terminfo file is read.
const maxTerminfoBytes = 1 << 20

var systemTerminfoDirs = []string{
	"/etc/terminfo", "/lib/terminfo", "/usr/share/terminfo", "/usr/lib/terminfo", "/usr/local/share/terminfo",
}

func terminfoDirs() []string {
	var dirs []string
	if d := os.Getenv("TERMINFO"); d != "" {
		dirs = append(dirs, d)
	}
	if h := os.Getenv("HOME"); h != "" {
		dirs = append(dirs, filepath.Join(h, ".terminfo"))
	}
	if v := os.Getenv("TERMINFO_DIRS"); v != "" {
		for _, d := range strings.Split(v, ":") {
			if d == "" {
				dirs = append(dirs, systemTerminfoDirs...)
			} else {
				dirs = append(dirs, d)
			}
		}
	}
	return append(dirs, systemTerminfoDirs...)
}

func readTerminfo(term string) ([]byte, error) {
	for _, dir := range terminfoDirs() {
		for _, sub := range []string{term[:1], fmt.Sprintf("%02x", term[0])} {
			data, err := readCapped(filepath.Join(dir, sub, term))
			if err == nil {
				return data, nil
			}
			if !errors.Is(err, fs.ErrNotExist) {
				return nil, err
			}
		}
	}
	return nil, fmt.Errorf("pst: no terminfo entry for %q: %w", term, fs.ErrNotExist)
}

func readCapped(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, maxTerminfoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxTerminfoBytes {
		return nil, fmt.Errorf("pst: terminfo file %s exceeds 1 MiB", path)
	}
	return data, nil
}

func terminfoHasPst(data []byte) (bool, error) {
	caps, err := terminfoExtStrings(data)
	if err != nil {
		return false, err
	}
	return caps["Pst"], nil
}

var errTerminfo = errors.New("pst: malformed terminfo entry")

// terminfoExtStrings parses a compiled terminfo entry and returns its
// extended string capabilities, each mapped to whether it has a value (false
// for absent or cancelled). See term(5).
func terminfoExtStrings(data []byte) (map[string]bool, error) {
	if len(data) > maxTerminfoBytes {
		return nil, fmt.Errorf("%w: exceeds 1 MiB", errTerminfo)
	}
	r := tiReader{b: data}
	magic := r.u16()
	var numSize int
	switch magic {
	case 0o432:
		numSize = 2
	case 0o1036:
		numSize = 4
	default:
		return nil, fmt.Errorf("%w: bad magic %#o", errTerminfo, magic)
	}
	nameSize, boolCount, numCount, strCount, strTableSize := r.count(), r.count(), r.count(), r.count(), r.count()
	r.skip(nameSize)
	r.skip(boolCount)
	r.align()
	r.skip(numCount * numSize)
	r.skip(strCount * 2)
	r.skip(strTableSize)
	if r.err != nil {
		return nil, r.err
	}
	r.align()
	if len(data)-r.off < 10 {
		return map[string]bool{}, nil // no extended section
	}
	extBool, extNum, extStr := r.count(), r.count(), r.count()
	r.count() // number of items in the string table; derived below instead
	extTableSize := r.count()
	r.skip(extBool)
	r.align()
	r.skip(extNum * numSize)
	values := make([]int, extStr)
	for i := range values {
		values[i] = int(int16(r.u16()))
	}
	nameCount := extBool + extNum + extStr
	names := make([]int, nameCount)
	for i := range names {
		names[i] = int(int16(r.u16()))
	}
	table := r.bytes(extTableSize)
	if r.err != nil {
		return nil, r.err
	}

	// The table holds the values, then the names. Name offsets are relative
	// to the end of the last value.
	base := 0
	for _, off := range values {
		if off < 0 {
			continue
		}
		s, ok := cString(table, off)
		if !ok {
			return nil, fmt.Errorf("%w: string value out of bounds", errTerminfo)
		}
		base = max(base, off+len(s)+1)
	}
	caps := make(map[string]bool, extStr)
	for i, off := range values {
		nameOff := names[extBool+extNum+i]
		if nameOff < 0 {
			return nil, fmt.Errorf("%w: negative name offset", errTerminfo)
		}
		name, ok := cString(table, base+nameOff)
		if !ok {
			return nil, fmt.Errorf("%w: name out of bounds", errTerminfo)
		}
		caps[name] = off >= 0 // -1 is absent, -2 cancelled
	}
	return caps, nil
}

// cString returns the NUL-terminated string at off in b.
func cString(b []byte, off int) (string, bool) {
	if off < 0 || off >= len(b) {
		return "", false
	}
	end := off
	for end < len(b) && b[end] != 0 {
		end++
	}
	if end == len(b) {
		return "", false
	}
	return string(b[off:end]), true
}

// tiReader reads little-endian fields, remembering the first error.
type tiReader struct {
	b   []byte
	off int
	err error
}

func (r *tiReader) fail(what string) {
	if r.err == nil {
		r.err = fmt.Errorf("%w: truncated %s", errTerminfo, what)
	}
}

func (r *tiReader) u16() uint16 {
	if r.err != nil || len(r.b)-r.off < 2 {
		r.fail("field")
		return 0
	}
	v := binary.LittleEndian.Uint16(r.b[r.off:])
	r.off += 2
	return v
}

// count reads a non-negative 16-bit count.
func (r *tiReader) count() int {
	v := int(int16(r.u16()))
	if v < 0 && r.err == nil {
		r.err = fmt.Errorf("%w: negative count", errTerminfo)
	}
	return max(v, 0)
}

func (r *tiReader) skip(n int) {
	if r.err != nil || n < 0 || len(r.b)-r.off < n {
		r.fail("section")
		return
	}
	r.off += n
}

func (r *tiReader) align() {
	if r.off%2 == 1 && r.off < len(r.b) {
		r.off++
	}
}

func (r *tiReader) bytes(n int) []byte {
	if r.err != nil || n < 0 || len(r.b)-r.off < n {
		r.fail("string table")
		return nil
	}
	b := r.b[r.off : r.off+n]
	r.off += n
	return b
}
