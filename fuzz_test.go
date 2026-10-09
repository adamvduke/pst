package pst

import (
	"bytes"
	"errors"
	"testing"
)

func FuzzParseBody(f *testing.F) {
	for _, ex := range specExamples {
		f.Add([]byte(ex.body))
	}
	for _, s := range []string{
		"", "?", "state=idle", "state=working:progress=040", "state=idle:msg=YQ=",
		"state=blocked:kind=auth:id=a/b/c:app=x:title=VA==:msg=TQ", " state = idle ::=:x",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, body []byte) {
		r, err := ParseBody(body)
		if err != nil {
			if !errors.Is(err, ErrQuery) && !errors.Is(err, ErrDiscarded) && !errors.Is(err, ErrIgnored) {
				t.Fatalf("unexpected error class: %v", err)
			}
			return
		}
		if err := r.Validate(); err != nil {
			t.Fatalf("ParseBody(%q) = %+v, which fails Validate: %v", body, r, err)
		}
		// A parsed report re-encodes and parses back to itself.
		seq, err := r.Encode()
		if err != nil {
			t.Fatal(err)
		}
		r2, err := ParseSequence(seq)
		if err != nil || r2 != r {
			t.Fatalf("re-encode of %+v: %+v, %v", r, r2, err)
		}
	})
}

func FuzzParseSequence(f *testing.F) {
	f.Add([]byte("\x1b]7501;state=idle\x1b\\"))
	f.Add([]byte("\x1b]7501;?\a"))
	f.Fuzz(func(t *testing.T, seq []byte) {
		if r, err := ParseSequence(seq); err == nil {
			if err := r.Validate(); err != nil {
				t.Fatalf("ParseSequence(%q) fails Validate: %v", seq, err)
			}
		}
	})
}

func FuzzRoundTrip(f *testing.F) {
	f.Add(uint8(1), "build", "test", uint8(1), int16(40), "brew", "US East", "Installing updates", false)
	f.Add(uint8(3), "", "", uint8(2), int16(-1), "", "", "", true)
	f.Fuzz(func(t *testing.T, state uint8, seg1, seg2 string, kind uint8, pct int16, app, title, msg string, bel bool) {
		states := []State{StateIdle, StateWorking, StateDone, StateBlocked, StateError, StateClear}
		r := Report{State: states[int(state)%len(states)]}
		if a, b := SanitizeSegment(seg1), SanitizeSegment(seg2); a != "" {
			r.ID = a
			if b != "" {
				r.ID += "/" + b
			}
		}
		if r.State == StateBlocked {
			r.Kind = []Kind{KindNone, KindPermission, KindQuestion, KindAuth}[int(kind)%4]
		}
		if r.State.hasProgress() && pct >= 0 {
			r.Progress = Percent(int(pct) % 101)
		}
		r.App = SanitizeSegment(app)
		r.Title = SanitizeText(title, MaxTitleBytes)
		r.Msg = SanitizeText(msg, MaxMsgBytes)
		if err := r.Validate(); err != nil {
			t.Fatalf("sanitized report %+v fails Validate: %v", r, err)
		}
		var opts []EncodeOption
		if bel {
			opts = append(opts, WithBEL())
		}
		seq, err := r.Encode(opts...)
		if err != nil {
			t.Fatal(err)
		}
		got, err := ParseSequence(seq)
		if err != nil || got != r {
			t.Fatalf("round trip of %+v = %+v, %v", r, got, err)
		}
	})
}

func FuzzScanner(f *testing.F) {
	f.Add([]byte("a\x1b]7501;state=idle\x1b\\b\x1b]0;t\a\x1b]7501;?\a"), uint8(3))
	f.Add([]byte("\x1b\x1b]\x1b]7501\x18\x1b]7501;"), uint8(1))
	f.Fuzz(func(t *testing.T, data []byte, split uint8) {
		// Whole-buffer and chunked feeding must find the same sequences.
		var whole, chunked [][]byte
		var s1, s2 Scanner
		s1.Feed(data, func(seq []byte) { whole = append(whole, bytes.Clone(seq)) })
		n := int(split)%16 + 1
		for i := 0; i < len(data); i += n {
			s2.Feed(data[i:min(i+n, len(data))], func(seq []byte) { chunked = append(chunked, bytes.Clone(seq)) })
		}
		if len(whole) != len(chunked) {
			t.Fatalf("whole found %d sequences, chunked %d", len(whole), len(chunked))
		}
		for i := range whole {
			if !bytes.Equal(whole[i], chunked[i]) {
				t.Fatalf("sequence %d differs: %q vs %q", i, whole[i], chunked[i])
			}
			seq := whole[i]
			if len(seq) > MaxSequenceBytes || !bytes.HasPrefix(seq, []byte("\x1b]7501;")) ||
				!(bytes.HasSuffix(seq, []byte("\x1b\\")) || bytes.HasSuffix(seq, []byte("\a"))) {
				t.Fatalf("bad sequence emitted: %q", seq)
			}
			ParseSequence(seq) //nolint:errcheck // must not panic
		}
	})
}

func FuzzTerminfo(f *testing.F) {
	for _, name := range []string{"pst-present", "pst-absent", "pst-cancelled", "pst-plain", "pst-only", "pst-present-32"} {
		f.Add(readFixture(f, name))
	}
	f.Add([]byte{0x1a, 0x01})
	f.Fuzz(func(t *testing.T, data []byte) {
		terminfoHasPst(data) //nolint:errcheck // must not panic
	})
}

func FuzzClassifier(f *testing.F) {
	f.Add([]byte(replyST + da1Reply))
	f.Add([]byte(da1Reply + replyBEL))
	f.Add([]byte("\x1b]7501;?:x=y\a\x1b[?1;2c"))
	f.Fuzz(func(t *testing.T, data []byte) {
		var whole, bytewise replyClassifier
		d1, done1 := whole.Feed(data)
		var d2 Support
		var done2 bool
		for i := range data {
			d2, done2 = bytewise.Feed(data[i : i+1])
		}
		if len(data) > 0 && (d1 != d2 || done1 != done2) {
			t.Fatalf("whole = %v,%v; bytewise = %v,%v", d1, done1, d2, done2)
		}
		if done1 && d1 == Unknown {
			t.Fatal("done without a decision")
		}
	})
}

func FuzzOSC94Mapper(f *testing.F) {
	f.Add([]byte("4;1;40"))
	f.Add([]byte("4;4"))
	f.Fuzz(func(t *testing.T, params []byte) {
		var m OSC94Mapper
		if r, ok := m.Map(params); ok {
			if err := r.Validate(); err != nil {
				t.Fatalf("Map(%q) = %+v fails Validate: %v", params, r, err)
			}
		}
	})
}
