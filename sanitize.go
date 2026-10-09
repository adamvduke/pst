package pst

import (
	"strings"
	"unicode"
	"unicode/utf8"
)

// isInvisible reports whether r is a text direction override or isolate, or
// an invisible formatting character: U+061C, U+200B-U+200F, U+202A-U+202E,
// U+2060-U+2064, U+2066-U+2069, and U+FEFF.
func isInvisible(r rune) bool {
	switch {
	case r == 0x061c, r == 0xfeff:
		return true
	case r >= 0x200b && r <= 0x200f:
		return true
	case r >= 0x202a && r <= 0x202e:
		return true
	case r >= 0x2060 && r <= 0x2064:
		return true
	case r >= 0x2066 && r <= 0x2069:
		return true
	}
	return false
}

// SanitizeText makes s safe to send as a msg or title. It replaces invalid
// UTF-8 and control characters with spaces, removes the characters
// [DisarmText] removes, collapses runs of whitespace into one space, and
// trims the ends. If the result is longer than maxBytes it is cut on a rune
// boundary and "…" is appended when it fits. A maxBytes of zero or less means
// no limit.
//
// Pass [MaxMsgBytes] or [MaxTitleBytes] as maxBytes; the result then always
// passes [Report.Validate].
func SanitizeText(s string, maxBytes int) string {
	var b strings.Builder
	b.Grow(len(s))
	space := false
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 || isControl(r) {
			r = ' '
		}
		if isInvisible(r) {
			continue
		}
		if unicode.IsSpace(r) {
			space = true
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := b.String()
	if maxBytes <= 0 || len(out) <= maxBytes {
		return out
	}
	const ellipsis = "…"
	keep := maxBytes
	if maxBytes >= len(ellipsis) {
		keep -= len(ellipsis)
	}
	for keep > 0 && !utf8.RuneStart(out[keep]) {
		keep--
	}
	out = strings.TrimRight(out[:keep], " ")
	if maxBytes >= len(ellipsis) {
		out += ellipsis
	}
	return out
}

// SanitizeSegment maps arbitrary text to a legal id segment or app name.
// Each rune outside [A-Za-z0-9_.+-] becomes "-", runs of "-" collapse to
// one, leading and trailing "-" are trimmed, and the result is cut to 32
// bytes. It returns "" if nothing usable is left.
func SanitizeSegment(s string) string {
	b := make([]byte, 0, min(len(s), MaxSegmentBytes))
	for _, r := range s {
		c := byte('-')
		if r < utf8.RuneSelf && isSegmentByte(byte(r)) {
			c = byte(r)
		}
		if c == '-' && (len(b) == 0 || b[len(b)-1] == '-') {
			continue
		}
		b = append(b, c)
		if len(b) == MaxSegmentBytes {
			break
		}
	}
	return strings.TrimRight(string(b), "-")
}

// DisarmText removes text direction overrides and isolates and invisible
// formatting characters from s, for terminals that show record text outside
// the grid. See the spec's Security section. Invalid UTF-8 becomes U+FFFD.
func DisarmText(s string) string {
	return strings.Map(func(r rune) rune {
		if isInvisible(r) {
			return -1
		}
		return r
	}, s)
}
