package pst

import (
	"encoding/base64"
	"errors"
	"strconv"
	"testing"
)

func TestGoldenBase64(t *testing.T) {
	// Encoded values from the spec's examples.
	tests := []struct {
		plain, encoded string
	}{
		{"Installing updates", "SW5zdGFsbGluZyB1cGRhdGVz"},
		{"Upgraded 12 packages", "VXBncmFkZWQgMTIgcGFja2FnZXM="},
		{"US East", "VVMgRWFzdA=="},
		{"Deploying v2.4.1", "RGVwbG95aW5nIHYyLjQuMQ=="},
		{"Apply 3 to add, 1 to change, 0 to destroy?", "QXBwbHkgMyB0byBhZGQsIDEgdG8gY2hhbmdlLCAwIHRvIGRlc3Ryb3k/"},
		{"", ""},
		{"a", "YQ=="},
		{"ab", "YWI="},
		{"abc", "YWJj"},
		{"\xff\xfe\xfd", "//79"},
	}
	for _, tt := range tests {
		if got := string(appendBase64(nil, tt.plain)); got != tt.encoded {
			t.Errorf("appendBase64(%q) = %q, want %q", tt.plain, got, tt.encoded)
		}
		if got := base64.StdEncoding.EncodeToString([]byte(tt.plain)); got != tt.encoded {
			t.Errorf("test vector %q is wrong: stdlib gives %q", tt.plain, got)
		}
		if got := base64Len(len(tt.plain)); got != len(tt.encoded) {
			t.Errorf("base64Len(%d) = %d, want %d", len(tt.plain), got, len(tt.encoded))
		}
	}
}

func TestEncodeGolden(t *testing.T) {
	r := Report{State: StateWorking, App: "brew", Msg: "Installing updates"}
	got, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	const want = "\x1b]7501;state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz\x1b\\"
	if string(got) != want {
		t.Errorf("Encode() = %q, want %q", got, want)
	}
}

func TestEncodeKeyOrderAndOmission(t *testing.T) {
	r := Report{
		State: StateBlocked, ID: "a/b", Kind: KindQuestion, Progress: Percent(0),
		App: "app", Title: "T", Msg: "M",
	}
	got, err := r.Encode()
	if err != nil {
		t.Fatal(err)
	}
	const want = "\x1b]7501;state=blocked:id=a/b:kind=question:progress=0:app=app:title=VA==:msg=TQ==\x1b\\"
	if string(got) != want {
		t.Errorf("got  %q\nwant %q", got, want)
	}

	got, err = Report{State: StateIdle}.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\x1b]7501;state=idle\x1b\\" {
		t.Errorf("minimal report = %q", got)
	}
}

func TestEncodeFraming(t *testing.T) {
	r := Report{State: StateDone, Msg: "ok"}
	tests := []struct {
		name string
		opts []EncodeOption
		want string
	}{
		{"st", nil, "\x1b]7501;state=done:msg=b2s=\x1b\\"},
		{"bel", []EncodeOption{WithBEL()}, "\x1b]7501;state=done:msg=b2s=\a"},
		{"tmux", []EncodeOption{WithTmuxPassthrough()}, "\x1bPtmux;\x1b\x1b]7501;state=done:msg=b2s=\x1b\x1b\\\x1b\\"},
		{"tmux+bel", []EncodeOption{WithTmuxPassthrough(), WithBEL()}, "\x1bPtmux;\x1b\x1b]7501;state=done:msg=b2s=\a\x1b\\"},
		{"nil option", []EncodeOption{nil}, "\x1b]7501;state=done:msg=b2s=\x1b\\"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := r.Encode(tt.opts...)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tt.want {
				t.Errorf("got  %q\nwant %q", got, tt.want)
			}
			if len(got) != cap(got) {
				t.Errorf("Encode allocated %d bytes for %d; frameLen is off", cap(got), len(got))
			}
		})
	}
}

func TestQuery(t *testing.T) {
	if got := string(Query()); got != "\x1b]7501;?\x1b\\" {
		t.Errorf("Query() = %q", got)
	}
	if got := string(Query(WithBEL())); got != "\x1b]7501;?\a" {
		t.Errorf("Query(WithBEL()) = %q", got)
	}
	if got := string(Query(WithTmuxPassthrough())); got != "\x1bPtmux;\x1b\x1b]7501;?\x1b\x1b\\\x1b\\" {
		t.Errorf("Query(WithTmuxPassthrough()) = %q", got)
	}
}

func TestAppendBody(t *testing.T) {
	r := Report{State: StateWorking, ID: "x", Progress: Percent(100)}
	got, err := r.AppendBody([]byte("prefix:"))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "prefix:state=working:id=x:progress=100" {
		t.Errorf("AppendBody = %q", got)
	}
	dst := []byte("keep")
	got, err = Report{State: "bogus"}.AppendBody(dst)
	if err == nil || string(got) != "keep" {
		t.Errorf("AppendBody on invalid report = %q, %v; want dst unchanged and an error", got, err)
	}
}

func TestAppendSequenceErrorLeavesDst(t *testing.T) {
	dst := []byte("keep")
	got, err := Report{State: StateIdle, Progress: Percent(5)}.AppendSequence(dst)
	if !errors.Is(err, ErrProgress) || string(got) != "keep" {
		t.Errorf("got %q, %v", got, err)
	}
}

func TestAppendSequenceNoAlloc(t *testing.T) {
	r := Report{
		State: StateBlocked, ID: "deploy/eu-west", Kind: KindPermission, Progress: Percent(40),
		App: "deploy", Title: "EU West", Msg: "Approve deploy to eu-west (production)?",
	}
	buf := make([]byte, 0, 512)
	opts := []EncodeOption{WithBEL()}
	allocs := testing.AllocsPerRun(100, func() {
		var err error
		if _, err = r.AppendSequence(buf[:0]); err != nil {
			t.Fatal(err)
		}
		if _, err = r.AppendSequence(buf[:0], opts...); err != nil {
			t.Fatal(err)
		}
	})
	if allocs != 0 {
		t.Errorf("AppendSequence allocated %v times per run, want 0", allocs)
	}
}

func TestBodyLenMatches(t *testing.T) {
	for _, r := range []Report{
		{State: StateWorking, Progress: Percent(0)},
		{State: StateWorking, Progress: Percent(9)},
		{State: StateWorking, Progress: Percent(10)},
		{State: StateWorking, Progress: Percent(100)},
		{State: StateClear, ID: "a/b/c", App: "x", Title: "t", Msg: "mm"},
	} {
		body := r.appendBody(nil)
		if len(body) != r.bodyLen() {
			t.Errorf("bodyLen = %d, appendBody wrote %d (%q)", r.bodyLen(), len(body), body)
		}
	}
	for _, v := range []int{-100, -1, 0, 1, 9, 10, 99, 100, 1000} {
		if got, want := decimalLen(v), len(strconv.Itoa(v)); got != want {
			t.Errorf("decimalLen(%d) = %d, want %d", v, got, want)
		}
	}
}

func BenchmarkAppendSequence(b *testing.B) {
	r := Report{State: StateWorking, App: "brew", Progress: Percent(40), Msg: "Installing updates"}
	buf := make([]byte, 0, 256)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		buf, _ = r.AppendSequence(buf[:0])
	}
}
