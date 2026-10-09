package pst

import (
	"encoding/base64"
	"errors"
	"strings"
	"testing"
)

func b64(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// specExamples are every report in the spec's examples, as written there.
// canonical is the encoder's output when it differs from the spec's key
// order.
var specExamples = []struct {
	name      string
	body      string
	want      Report
	canonical string
}{
	{
		name: "terraform",
		body: "state=blocked:kind=permission:app=terraform:msg=QXBwbHkgMyB0byBhZGQsIDEgdG8gY2hhbmdlLCAwIHRvIGRlc3Ryb3k/",
		want: Report{State: StateBlocked, Kind: KindPermission, App: "terraform", Msg: "Apply 3 to add, 1 to change, 0 to destroy?"},
	},
	{
		name: "brew working",
		body: "state=working:app=brew:msg=SW5zdGFsbGluZyB1cGRhdGVz",
		want: Report{State: StateWorking, App: "brew", Msg: "Installing updates"},
	},
	{
		name: "brew blocked",
		body: "state=blocked:kind=auth:app=brew:msg=UGFzc3dvcmQgcmVxdWlyZWQgdG8gaW5zdGFsbCB1cGRhdGVz",
		want: Report{State: StateBlocked, Kind: KindAuth, App: "brew", Msg: "Password required to install updates"},
	},
	{
		name: "brew done",
		body: "state=done:app=brew:msg=VXBncmFkZWQgMTIgcGFja2FnZXM=",
		want: Report{State: StateDone, App: "brew", Msg: "Upgraded 12 packages"},
	},
	{
		name: "deploy root working",
		body: "state=working:app=deploy:msg=RGVwbG95aW5nIHYyLjQuMQ==",
		want: Report{State: StateWorking, App: "deploy", Msg: "Deploying v2.4.1"},
	},
	{
		name:      "deploy us-east working",
		body:      "state=working:id=us-east:title=VVMgRWFzdA==:progress=40:msg=UHVzaGluZyBpbWFnZQ==",
		want:      Report{State: StateWorking, ID: "us-east", Title: "US East", Progress: Percent(40), Msg: "Pushing image"},
		canonical: "state=working:id=us-east:progress=40:title=VVMgRWFzdA==:msg=UHVzaGluZyBpbWFnZQ==",
	},
	{
		name:      "deploy eu-west blocked",
		body:      "state=blocked:kind=permission:id=eu-west:title=RVUgV2VzdA==:msg=QXBwcm92ZSBkZXBsb3kgdG8gZXUtd2VzdCAocHJvZHVjdGlvbik/",
		want:      Report{State: StateBlocked, Kind: KindPermission, ID: "eu-west", Title: "EU West", Msg: "Approve deploy to eu-west (production)?"},
		canonical: "state=blocked:id=eu-west:kind=permission:title=RVUgV2VzdA==:msg=QXBwcm92ZSBkZXBsb3kgdG8gZXUtd2VzdCAocHJvZHVjdGlvbik/",
	},
	{
		name: "deploy us-east done",
		body: "state=done:id=us-east:title=VVMgRWFzdA==:msg=SGVhbHRoeQ==",
		want: Report{State: StateDone, ID: "us-east", Title: "US East", Msg: "Healthy"},
	},
	{
		name: "deploy clear us-east",
		body: "state=clear:id=us-east",
		want: Report{State: StateClear, ID: "us-east"},
	},
	{
		name: "deploy clear eu-west",
		body: "state=clear:id=eu-west",
		want: Report{State: StateClear, ID: "eu-west"},
	},
	{
		name: "deploy root done",
		body: "state=done:app=deploy:msg=RGVwbG95ZWQgdG8gMyByZWdpb25z",
		want: Report{State: StateDone, App: "deploy", Msg: "Deployed to 3 regions"},
	},
	{
		name: "shell function",
		body: "state=working:msg=" + b64("Syncing photos"),
		want: Report{State: StateWorking, Msg: "Syncing photos"},
	},
}

func TestSpecExamples(t *testing.T) {
	for _, tt := range specExamples {
		t.Run(tt.name, func(t *testing.T) {
			for _, term := range []string{"\x1b\\", "\a"} {
				got, err := ParseSequence([]byte("\x1b]7501;" + tt.body + term))
				if err != nil {
					t.Fatalf("ParseSequence: %v", err)
				}
				if got != tt.want {
					t.Errorf("ParseSequence = %+v\nwant           %+v", got, tt.want)
				}
			}
			canonical := tt.canonical
			if canonical == "" {
				canonical = tt.body
			}
			enc, err := tt.want.Encode()
			if err != nil {
				t.Fatal(err)
			}
			if want := "\x1b]7501;" + canonical + "\x1b\\"; string(enc) != want {
				t.Errorf("Encode = %q\nwant     %q", enc, want)
			}
		})
	}
}

func TestParseBody(t *testing.T) {
	ok := func(r Report) Report { return r }
	long := func(c byte, n int) string { return strings.Repeat(string(c), n) }
	tests := []struct {
		name string
		body string
		want Report
		err  error
	}{
		// Lenient rules.
		{"value with =", "state=idle:msg=YQ==", ok(Report{State: StateIdle, Msg: "a"}), nil},
		{"unpadded base64", "state=idle:msg=YQ", ok(Report{State: StateIdle, Msg: "a"}), nil},
		{"unpadded base64 2", "state=idle:title=YWI", ok(Report{State: StateIdle, Title: "ab"}), nil},
		{"whitespace", " state = working \t:\tapp =  brew ", ok(Report{State: StateWorking, App: "brew"}), nil},
		{"malformed pair in middle", "state=working:garbage:app=brew", ok(Report{State: StateWorking, App: "brew"}), nil},
		{"empty key", "state=working:=x:app=brew", ok(Report{State: StateWorking, App: "brew"}), nil},
		{"bad value byte", "state=working:app=br;ew:msg=YQ", ok(Report{State: StateWorking, Msg: "a"}), nil},
		{"space inside value", "state=working:app=br ew", ok(Report{State: StateWorking}), nil},
		{"empty pairs", "::state=idle::", ok(Report{State: StateIdle}), nil},
		{"uppercase key", "state=working:App=brew", ok(Report{State: StateWorking}), nil},
		{"uppercase key long", "state=working:ABCDEFGHIJKLMNOPQRSTUVWXYZ=x", ok(Report{State: StateWorking}), nil},
		{"digit in key", "state=working:app2=brew", ok(Report{State: StateWorking}), nil},
		{"unknown key", "state=working:future=1:app=brew", ok(Report{State: StateWorking, App: "brew"}), nil},
		{"repeated key", "state=working:app=one:app=two", ok(Report{State: StateWorking, App: "two"}), nil},
		{"repeated state", "state=bogus:state=done", ok(Report{State: StateDone}), nil},
		{"repeated msg", "state=done:msg=YQ:msg=Yg", ok(Report{State: StateDone, Msg: "b"}), nil},
		{"empty msg", "state=done:msg=", ok(Report{State: StateDone}), nil},
		{"kind on working", "state=working:kind=auth", ok(Report{State: StateWorking}), nil},
		{"kind on clear", "state=clear:kind=auth", ok(Report{State: StateClear}), nil},
		{"unknown kind", "state=blocked:kind=coffee", ok(Report{State: StateBlocked}), nil},
		{"progress on done", "state=done:progress=50", ok(Report{State: StateDone}), nil},
		{"progress -1", "state=working:progress=-1", ok(Report{State: StateWorking}), nil},
		{"progress 101", "state=working:progress=101", ok(Report{State: StateWorking}), nil},
		{"progress 4.5", "state=working:progress=4.5", ok(Report{State: StateWorking}), nil},
		{"progress 040", "state=working:progress=040", ok(Report{State: StateWorking}), nil},
		{"progress 00", "state=working:progress=00", ok(Report{State: StateWorking}), nil},
		{"progress +5", "state=working:progress=+5", ok(Report{State: StateWorking}), nil},
		{"progress empty", "state=working:progress=", ok(Report{State: StateWorking}), nil},
		{"progress 0", "state=blocked:progress=0", ok(Report{State: StateBlocked, Progress: Percent(0)}), nil},
		{"progress 100", "state=working:progress=100", ok(Report{State: StateWorking, Progress: Percent(100)}), nil},
		{"app bad chars", "state=idle:app=a,b", ok(Report{State: StateIdle}), nil},
		{"app empty", "state=idle:app=", ok(Report{State: StateIdle}), nil},
		{"app 32", "state=idle:app=" + long('a', 32), ok(Report{State: StateIdle, App: long('a', 32)}), nil},
		{"clear keeps text", "state=clear:app=x:msg=YQ", ok(Report{State: StateClear, App: "x", Msg: "a"}), nil},
		{"id max", "state=idle:id=" + strings.Repeat(long('a', 15)+"/", 7) + long('b', 16),
			ok(Report{State: StateIdle, ID: strings.Repeat(long('a', 15)+"/", 7) + long('b', 16)}), nil},
		{"id segment 32", "state=idle:id=" + long('a', 32), ok(Report{State: StateIdle, ID: long('a', 32)}), nil},
		{"key 16", "state=idle:" + long('k', 16) + "=v", ok(Report{State: StateIdle}), nil},

		// Ignored.
		{"missing state", "app=brew", Report{}, ErrIgnored},
		{"empty body", "", Report{}, ErrIgnored},
		{"empty state", "state=", Report{}, ErrIgnored},
		{"unknown state", "state=sleeping", Report{}, ErrIgnored},
		{"uppercase state", "state=IDLE", Report{}, ErrIgnored},
		{"id bad char", "state=idle:id=a,b", Report{}, ErrIgnored},
		{"id empty", "state=idle:id=", Report{}, ErrIgnored},
		{"id empty segment", "state=idle:id=a//b", Report{}, ErrIgnored},
		{"id trailing slash", "state=idle:id=a/", Report{}, ErrIgnored},
		{"id leading slash", "state=idle:id=/a", Report{}, ErrIgnored},
		{"id with =", "state=idle:id=a=b", Report{}, ErrIgnored},

		// Discarded.
		{"id segment 33", "state=idle:id=" + long('a', 33), Report{}, ErrDiscarded},
		{"id 9 levels", "state=idle:id=a/b/c/d/e/f/g/h/i", Report{}, ErrDiscarded},
		{"id 129 bytes", "state=idle:id=" + strings.Repeat(long('a', 31)+"/", 4) + "a", Report{}, ErrDiscarded},
		{"overridden id over limit", "state=idle:id=" + long('a', 33) + ":id=ok", Report{}, ErrDiscarded},
		{"key 17", "state=idle:" + long('k', 17) + "=v", Report{}, ErrDiscarded},
		{"app 33", "state=idle:app=" + long('a', 33), Report{}, ErrDiscarded},
		{"bad base64", "state=idle:msg=Y", Report{}, ErrDiscarded},
		{"bad padding 1", "state=idle:msg=YQ=", Report{}, ErrDiscarded},
		{"bad padding 3", "state=idle:msg=Y===", Report{}, ErrDiscarded},
		{"padding in middle", "state=idle:msg=YQ==YQ==", Report{}, ErrDiscarded},
		{"control char", "state=idle:msg=" + b64("a\nb"), Report{}, ErrDiscarded},
		{"DEL", "state=idle:msg=" + b64("a\x7f"), Report{}, ErrDiscarded},
		{"C1 control", "state=idle:msg=" + b64("a\u0085"), Report{}, ErrDiscarded},
		{"NUL in title", "state=idle:title=" + b64("\x00"), Report{}, ErrDiscarded},
		{"invalid UTF-8", "state=idle:msg=" + b64("\xff\xfe"), Report{}, ErrDiscarded},
		{"overridden bad msg", "state=idle:msg=" + b64("\x01") + ":msg=YQ", Report{}, ErrDiscarded},
		{"bad msg on unknown state", "state=nope:msg=Y", Report{}, ErrDiscarded},
		{"title 192 ok", "state=idle:title=" + b64(long('t', 192)), ok(Report{State: StateIdle, Title: long('t', 192)}), nil},
		{"title 193", "state=idle:title=" + b64(long('t', 193)), Report{}, ErrDiscarded},
		{"title encoded 257", "state=idle:title=" + long('A', 257), Report{}, ErrDiscarded},
		{"msg 2048 ok", "state=idle:msg=" + b64(long('m', 2048)), ok(Report{State: StateIdle, Msg: long('m', 2048)}), nil},
		{"msg 2049", "state=idle:msg=" + b64(long('m', 2049)), Report{}, ErrDiscarded},
		{"msg decoded 2049 unpadded under 2733", "state=idle:msg=" + strings.TrimRight(b64(long('m', 2049)), "="), Report{}, ErrDiscarded},
		{"msg encoded 2733", "state=idle:msg=" + long('A', 2733), Report{}, ErrDiscarded},

		// Query.
		{"query", "?", Report{}, ErrQuery},
		{"query with pairs", "?:version=2", Report{}, ErrQuery},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := ParseBody([]byte(tt.body))
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("ParseBody error = %v, want %v", err, tt.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseBody: %v", err)
			}
			if got != tt.want {
				t.Errorf("ParseBody = %+v\nwant       %+v", got, tt.want)
			}
			if err := got.Validate(); err != nil {
				t.Errorf("parsed report fails Validate: %v", err)
			}
		})
	}
}

func TestMsgExactlyAtEncodedLimit(t *testing.T) {
	// 2732 encoded bytes that decode to 2049 bytes: within the encoded limit
	// but over the decoded one.
	v := strings.Repeat("QUJD", 683) // decodes to 2049 bytes of "ABC"
	if _, err := ParseBody([]byte("state=idle:msg=" + v)); !errors.Is(err, ErrDiscarded) {
		t.Errorf("err = %v, want ErrDiscarded", err)
	}
	// 2732 encoded bytes, 2048 decoded: accepted.
	v = b64(strings.Repeat("x", 2048))
	if len(v) != MaxMsgEncodedBytes {
		t.Fatalf("len = %d", len(v))
	}
	r, err := ParseBody([]byte("state=idle:msg=" + v))
	if err != nil || len(r.Msg) != MaxMsgBytes {
		t.Errorf("got %d bytes, %v", len(r.Msg), err)
	}
}

func TestParseSequence(t *testing.T) {
	pad := func(total int, term string) []byte {
		prefix := "\x1b]7501;state=idle:x="
		n := total - len(prefix) - len(term)
		return []byte(prefix + strings.Repeat("a", n) + term)
	}
	tests := []struct {
		name string
		seq  []byte
		err  error
	}{
		{"st", []byte("\x1b]7501;state=idle\x1b\\"), nil},
		{"bel", []byte("\x1b]7501;state=idle\a"), nil},
		{"4096 st", pad(4096, "\x1b\\"), nil},
		{"4096 bel", pad(4096, "\a"), nil},
		{"4097 st", pad(4097, "\x1b\\"), ErrDiscarded},
		{"4097 bel", pad(4097, "\a"), ErrDiscarded},
		{"query", []byte("\x1b]7501;?\x1b\\"), ErrQuery},
		{"query bel", []byte("\x1b]7501;?\a"), ErrQuery},
		{"no terminator", []byte("\x1b]7501;state=idle"), ErrNotReport},
		{"other osc", []byte("\x1b]0;title\a"), ErrNotReport},
		{"no esc", []byte("]7501;state=idle\a"), ErrNotReport},
		{"empty", nil, ErrNotReport},
		{"intro only", []byte("\x1b]7501;"), ErrNotReport},
		{"lone ESC terminator", []byte("\x1b]7501;state=idle\x1b"), ErrNotReport},
		{"empty body", []byte("\x1b]7501;\a"), ErrIgnored},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ParseSequence(tt.seq)
			if tt.err == nil {
				if err != nil || r.State != StateIdle {
					t.Errorf("ParseSequence = %+v, %v", r, err)
				}
				return
			}
			if !errors.Is(err, tt.err) {
				t.Errorf("err = %v, want %v", err, tt.err)
			}
		})
	}
}

func TestParseBodyLengthLimit(t *testing.T) {
	body := "state=idle:x=" + strings.Repeat("a", maxBodyBytes-len("state=idle:x="))
	if _, err := ParseBody([]byte(body)); err != nil {
		t.Errorf("body of %d bytes: %v", len(body), err)
	}
	if _, err := ParseBody([]byte(body + "a")); !errors.Is(err, ErrDiscarded) {
		t.Errorf("body of %d bytes: err = %v, want ErrDiscarded", len(body)+1, err)
	}
}

func TestRoundTrip(t *testing.T) {
	reports := []Report{
		{State: StateIdle},
		{State: StateWorking, Progress: Percent(0)},
		{State: StateBlocked, ID: "a/b/c", Kind: KindQuestion, Progress: Percent(99), App: "x.y+z_-", Title: "Τίτλος", Msg: "日本語 ✓"},
		{State: StateDone, Msg: strings.Repeat("é", 1024)},
		{State: StateError, Title: strings.Repeat("t", MaxTitleBytes)},
		{State: StateClear, ID: "x", App: "a", Title: "t", Msg: "m"},
	}
	for _, r := range reports {
		for _, opts := range [][]EncodeOption{nil, {WithBEL()}} {
			seq, err := r.Encode(opts...)
			if err != nil {
				t.Fatalf("Encode(%+v): %v", r, err)
			}
			got, err := ParseSequence(seq)
			if err != nil || got != r {
				t.Errorf("round trip of %+v = %+v, %v", r, got, err)
			}
		}
	}
}
