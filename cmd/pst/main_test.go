package main

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

func runCLI(t *testing.T, stdin string, args ...string) (code int, out, errOut, tty string) {
	t.Helper()
	var o, e, w bytes.Buffer
	code = run(args, env{
		stdin:  strings.NewReader(stdin),
		stdout: &o,
		stderr: &e,
		output: func() (io.Writer, func()) { return &w, func() {} },
	})
	return code, o.String(), e.String(), w.String()
}

func TestReportCommands(t *testing.T) {
	tests := []struct {
		args []string
		want string
	}{
		{[]string{"working", "Syncing photos"}, "\x1b]7501;state=working:msg=U3luY2luZyBwaG90b3M=\x1b\\"},
		{[]string{"progress", "40", "Pushing image", "--id", "us-east", "--title", "US East"},
			"\x1b]7501;state=working:id=us-east:progress=40:title=VVMgRWFzdA==:msg=UHVzaGluZyBpbWFnZQ==\x1b\\"},
		{[]string{"blocked", "--kind", "auth", "Password required"}, "\x1b]7501;state=blocked:kind=auth:msg=UGFzc3dvcmQgcmVxdWlyZWQ=\x1b\\"},
		{[]string{"blocked", "--progress", "20", "--kind=question"}, "\x1b]7501;state=blocked:kind=question:progress=20\x1b\\"},
		{[]string{"done", "Photos", "synced"}, "\x1b]7501;state=done:msg=UGhvdG9zIHN5bmNlZA==\x1b\\"},
		{[]string{"error", "--app", "rsync", "rsync failed"}, "\x1b]7501;state=error:app=rsync:msg=cnN5bmMgZmFpbGVk\x1b\\"},
		{[]string{"idle"}, "\x1b]7501;state=idle\x1b\\"},
		{[]string{"clear"}, "\x1b]7501;state=clear\x1b\\"},
		{[]string{"clear", "--id", "x"}, "\x1b]7501;state=clear:id=x\x1b\\"},
		{[]string{"done", "--bel"}, "\x1b]7501;state=done\a"},
		{[]string{"done", "--tmux"}, "\x1bPtmux;\x1b\x1b]7501;state=done\x1b\x1b\\\x1b\\"},
		{[]string{"progress", "--osc94", "5"}, "\x1b]7501;state=working:progress=5\x1b\\\x1b]9;4;1;5\x1b\\"},
		{[]string{"progress", "--osc94", "--id", "c", "5"}, "\x1b]7501;state=working:id=c:progress=5\x1b\\"},
		{[]string{"working", "--sanitize", "a\nb"}, "\x1b]7501;state=working:msg=YSBi\x1b\\"},
		{[]string{"working", "--", "-dash"}, "\x1b]7501;state=working:msg=LWRhc2g=\x1b\\"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, _, errOut, tty := runCLI(t, "", tt.args...)
			if code != 0 {
				t.Fatalf("exit %d: %s", code, errOut)
			}
			if tty != tt.want {
				t.Errorf("wrote %q, want %q", tty, tt.want)
			}
		})
	}
}

func TestReportErrors(t *testing.T) {
	tests := []struct {
		args []string
		code int
		msg  string
	}{
		{[]string{"working", "a\nb"}, 1, "control character"},
		{[]string{"progress", "101"}, 1, "invalid progress"},
		{[]string{"progress"}, 2, "needs a percentage"},
		{[]string{"progress", "x"}, 2, "invalid percentage"},
		{[]string{"blocked", "--kind", "coffee"}, 1, "invalid kind"},
		{[]string{"idle", "--id", "a b"}, 1, "invalid id"},
		{[]string{"done", "--app", "my app"}, 1, "invalid app"},
		{[]string{"clear", "msg"}, 2, "no message"},
		{[]string{"done", "--nope"}, 2, "not defined"},
		{[]string{"bogus"}, 2, "unknown command"},
		{nil, 2, "usage"},
	}
	for _, tt := range tests {
		t.Run(strings.Join(tt.args, " "), func(t *testing.T) {
			code, _, errOut, tty := runCLI(t, "", tt.args...)
			if code != tt.code || !strings.Contains(errOut, tt.msg) {
				t.Errorf("exit %d, stderr %q; want exit %d containing %q", code, errOut, tt.code, tt.msg)
			}
			if tty != "" {
				t.Errorf("wrote %q on error", tty)
			}
		})
	}
}

func TestDecode(t *testing.T) {
	in := "noise\x1b]7501;state=working:id=us-east:title=VVMgRWFzdA==:progress=40:msg=UHVzaGluZyBpbWFnZQ==\x1b\\" +
		"\x1b]0;title\a\x1b]7501;?\a\x1b]7501;state=bogus\a\x1b]7501;state=done:app=brew\a"
	code, out, errOut, _ := runCLI(t, in, "decode")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, errOut)
	}
	want := `working id=us-east progress=40% title="US East" msg="Pushing image"
query
pst: report ignored: unknown state "bogus"
done app=brew
`
	if out != want {
		t.Errorf("got\n%s\nwant\n%s", out, want)
	}
}

func TestVersionAndHelp(t *testing.T) {
	if code, out, _, _ := runCLI(t, "", "version"); code != 0 || !strings.Contains(out, "0.3") {
		t.Errorf("version: %d %q", code, out)
	}
	if code, out, _, _ := runCLI(t, "", "help"); code != 0 || !strings.Contains(out, "usage") {
		t.Errorf("help: %d %q", code, out)
	}
}
