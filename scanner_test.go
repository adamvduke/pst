package pst

import (
	"bytes"
	"strings"
	"testing"
)

func scanAll(chunks ...string) []string {
	var s Scanner
	var out []string
	for _, c := range chunks {
		s.Feed([]byte(c), func(seq []byte) { out = append(out, string(seq)) })
	}
	return out
}

func TestScanner(t *testing.T) {
	r1 := "\x1b]7501;state=working:app=brew\x1b\\"
	r2 := "\x1b]7501;state=done\a"
	tests := []struct {
		name   string
		chunks []string
		want   []string
	}{
		{"single", []string{r1}, []string{r1}},
		{"bel", []string{r2}, []string{r2}},
		{"surrounded by output", []string{"hello " + r1 + " world\r\n" + r2}, []string{r1, r2}},
		{"query", []string{"\x1b]7501;?\x1b\\"}, []string{"\x1b]7501;?\x1b\\"}},
		{"other OSCs ignored", []string{"\x1b]0;title\a\x1b]9;4;1;50\x1b\\\x1b]8;;http://x\x1b\\" + r1}, []string{r1}},
		{"OSC 75010 is not 7501", []string{"\x1b]75010;state=idle\a"}, nil},
		{"CSI and plain ESC", []string{"\x1b[31mred\x1b[0m\x1b7\x1b8" + r2}, []string{r2}},
		{"CAN aborts", []string{"\x1b]7501;state=idle\x18" + r2}, []string{r2}},
		{"SUB aborts", []string{"\x1b]7501;state=idle\x1a;state=idle\a"}, nil},
		{"ESC aborts and restarts", []string{"\x1b]7501;state=idle\x1b" + r2[1:]}, []string{r2}},
		{"double ESC", []string{"\x1b\x1b]7501;state=done\a"}, []string{r2}},
		{"partial intro then other", []string{"\x1b]75\a" + r2}, []string{r2}},
		{"intro without semicolon", []string{"\x1b]7501\x1b\\"}, nil},
		{"empty body", []string{"\x1b]7501;\a"}, []string{"\x1b]7501;\a"}},
		{"ESC inside other OSC", []string{"\x1b]0;a\x1b]7501;state=done\a"}, []string{r2}},
		{"split ST", []string{"\x1b]7501;state=working:app=brew\x1b", "\\"}, []string{r1}},
		{"unterminated", []string{"\x1b]7501;state=idle"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scanAll(tt.chunks...)
			if strings.Join(got, "|") != strings.Join(tt.want, "|") {
				t.Errorf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestScannerByteByByte(t *testing.T) {
	stream := "out\x1b]7501;state=working:app=brew\x1b\\\x1b]0;x\amore\x1b]7501;state=done\a"
	chunks := make([]string, len(stream))
	for i := range stream {
		chunks[i] = stream[i : i+1]
	}
	got := scanAll(chunks...)
	want := []string{"\x1b]7501;state=working:app=brew\x1b\\", "\x1b]7501;state=done\a"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("got %q", got)
	}
}

func TestScannerLengthLimit(t *testing.T) {
	seq := func(total int, term string) string {
		prefix := "\x1b]7501;state=idle:x="
		return prefix + strings.Repeat("a", total-len(prefix)-len(term)) + term
	}
	for _, term := range []string{"\x1b\\", "\a"} {
		ok := seq(4096, term)
		if got := scanAll(ok); len(got) != 1 || got[0] != ok {
			t.Errorf("4096-byte sequence with %q not emitted", term)
		}
		long := seq(4097, term)
		after := "\x1b]7501;state=done\a"
		if got := scanAll(long + after); len(got) != 1 || got[0] != after {
			t.Errorf("4097-byte sequence with %q: got %d sequences; want only the following one", term, len(got))
		}
		// Overflow across chunks, then recovery.
		if got := scanAll(long[:3000], long[3000:], after); len(got) != 1 || got[0] != after {
			t.Errorf("split 4097-byte sequence with %q: got %d sequences", term, len(got))
		}
	}
}

func TestScannerDoesNotModifyInput(t *testing.T) {
	in := []byte("a\x1b]7501;state=idle\x1b\\b")
	orig := bytes.Clone(in)
	var s Scanner
	s.Feed(in, func(seq []byte) {
		for i := range seq {
			seq[i] = 'X' // the callback's slice is the scanner's own buffer
		}
	})
	if !bytes.Equal(in, orig) {
		t.Errorf("input modified: %q", in)
	}
	s.Feed(in, nil) // nil callback is allowed
}

func TestScannerFeedsParser(t *testing.T) {
	var s Scanner
	var got []Report
	for _, ex := range specExamples {
		s.Feed([]byte("noise\x1b]7501;"+ex.body+"\x1b\\"), func(seq []byte) {
			r, err := ParseSequence(seq)
			if err != nil {
				t.Errorf("ParseSequence(%q): %v", seq, err)
			}
			got = append(got, r)
		})
	}
	if len(got) != len(specExamples) {
		t.Fatalf("got %d reports, want %d", len(got), len(specExamples))
	}
	for i, ex := range specExamples {
		if got[i] != ex.want {
			t.Errorf("%s: got %+v", ex.name, got[i])
		}
	}
}
