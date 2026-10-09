package pst

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
)

// writes records each Write call separately.
type writes struct {
	mu  sync.Mutex
	got []string
	err error
}

func (w *writes) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.err != nil {
		return 0, w.err
	}
	w.got = append(w.got, string(p))
	return len(p), nil
}

func (w *writes) take() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	g := w.got
	w.got = nil
	return g
}

func seq(body string) string { return "\x1b]7501;" + body + "\x1b\\" }

func TestStatusRepeatsAppAndTitle(t *testing.T) {
	var buf bytes.Buffer
	st := New(&buf, WithApp("brew"), WithTitle("Upgrade"))
	must(t, st.Working("Installing updates"))
	must(t, st.Blocked(KindAuth, "Password required to install updates"))
	must(t, st.Progress(50, "Installing updates"))
	must(t, st.Done("Upgraded 12 packages"))
	want := seq("state=working:app=brew:title=VXBncmFkZQ==:msg=SW5zdGFsbGluZyB1cGRhdGVz") +
		seq("state=blocked:kind=auth:app=brew:title=VXBncmFkZQ==:msg=UGFzc3dvcmQgcmVxdWlyZWQgdG8gaW5zdGFsbCB1cGRhdGVz") +
		seq("state=working:progress=50:app=brew:title=VXBncmFkZQ==:msg=SW5zdGFsbGluZyB1cGRhdGVz") +
		seq("state=done:app=brew:title=VXBncmFkZQ==:msg=VXBncmFkZWQgMTIgcGFja2FnZXM=")
	if buf.String() != want {
		t.Errorf("got  %q\nwant %q", buf.String(), want)
	}
}

func TestStatusAllMethods(t *testing.T) {
	w := &writes{}
	st := New(w, WithoutDedupe())
	must(t, st.Idle(""))
	must(t, st.Working(""))
	must(t, st.Progress(0, ""))
	must(t, st.Blocked(KindNone, ""))
	must(t, st.BlockedProgress(KindQuestion, 7, ""))
	must(t, st.Done(""))
	must(t, st.Error(""))
	must(t, st.Clear())
	must(t, st.Idlef("%d", 1))
	must(t, st.Workingf("%d", 2))
	must(t, st.Progressf(3, "%d", 3))
	must(t, st.Blockedf(KindAuth, "%d", 4))
	must(t, st.Donef("%d", 5))
	must(t, st.Errorf("%d", 6))
	want := []string{
		seq("state=idle"), seq("state=working"), seq("state=working:progress=0"),
		seq("state=blocked"), seq("state=blocked:kind=question:progress=7"),
		seq("state=done"), seq("state=error"), seq("state=clear"),
		seq("state=idle:msg=MQ=="), seq("state=working:msg=Mg=="), seq("state=working:progress=3:msg=Mw=="),
		seq("state=blocked:kind=auth:msg=NA=="), seq("state=done:msg=NQ=="), seq("state=error:msg=Ng=="),
	}
	if got := w.take(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestStatusChild(t *testing.T) {
	w := &writes{}
	root := New(w, WithApp("deploy"), WithTitle("Deploy"))
	east, err := root.Child("us-east", WithTitle("US East"))
	must(t, err)
	if east.ID() != "us-east" {
		t.Errorf("ID = %q", east.ID())
	}
	pod, err := east.Child("pod-1")
	must(t, err)
	if pod.ID() != "us-east/pod-1" {
		t.Errorf("ID = %q", pod.ID())
	}
	tagged, err := root.Child("x", WithApp("tool"))
	must(t, err)

	must(t, east.Progress(40, "Pushing image"))
	must(t, pod.Working(""))
	must(t, tagged.Idle(""))
	must(t, east.Clear())
	want := []string{
		seq("state=working:id=us-east:progress=40:title=VVMgRWFzdA==:msg=UHVzaGluZyBpbWFnZQ=="),
		seq("state=working:id=us-east/pod-1"),
		seq("state=idle:id=x:app=tool"),
		seq("state=clear:id=us-east"),
	}
	if got := w.take(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got  %q\nwant %q", got, want)
	}

	for _, bad := range []string{"", "a/b", "a b", strings.Repeat("a", 33)} {
		if _, err := root.Child(bad); !errors.Is(err, ErrID) {
			t.Errorf("Child(%q) err = %v, want ErrID", bad, err)
		}
	}
	deep := root
	for i := 0; i < MaxIDDepth; i++ {
		deep, err = deep.Child("d")
		must(t, err)
	}
	if _, err := deep.Child("d"); !errors.Is(err, ErrLimit) || !errors.Is(err, ErrID) {
		t.Errorf("ninth level err = %v, want ErrLimit and ErrID", err)
	}
	long := root
	for i := 0; i < 3; i++ {
		long, err = long.Child(strings.Repeat("x", 32))
		must(t, err)
	}
	if _, err := long.Child(strings.Repeat("y", 32)); !errors.Is(err, ErrLimit) {
		t.Errorf("132-byte id err = %v, want ErrLimit", err)
	}
}

func TestStatusReport(t *testing.T) {
	w := &writes{}
	root := New(w, WithApp("a"), WithTitle("T"))
	child, _ := root.Child("c")
	must(t, child.Report(Report{State: StateDone, ID: "ignored", Msg: "m"}))
	must(t, root.Report(Report{State: StateIdle, App: "override"}))
	must(t, root.Report(Report{State: StateClear}))
	want := []string{
		seq("state=done:id=c:msg=bQ=="),
		seq("state=idle:app=override:title=VA=="),
		seq("state=clear"),
	}
	if got := w.take(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestStatusDedupe(t *testing.T) {
	w := &writes{}
	st := New(w)
	child, _ := st.Child("c")
	must(t, st.Working("a"))
	must(t, st.Working("a"))    // skipped
	must(t, child.Working("a")) // different record
	must(t, st.Working("b"))    // changed
	must(t, st.Working("a"))    // changed back
	must(t, child.Working("a")) // skipped
	must(t, st.Clear())         // clears everything
	must(t, st.Working("a"))    // the record is gone, so send again
	must(t, child.Working("a")) // the child too
	must(t, child.Clear())      // clears are never skipped
	must(t, child.Clear())
	if got := len(w.take()); got != 9 {
		t.Errorf("wrote %d sequences, want 9", got)
	}

	w2 := &writes{}
	st2 := New(w2, WithoutDedupe())
	must(t, st2.Idle(""))
	must(t, st2.Idle(""))
	if got := len(w2.take()); got != 2 {
		t.Errorf("WithoutDedupe wrote %d, want 2", got)
	}
}

func TestStatusDedupeRetriesAfterWriteError(t *testing.T) {
	w := &writes{err: errors.New("boom")}
	st := New(w)
	if err := st.Working("a"); err == nil || err.Error() != "boom" {
		t.Fatalf("err = %v, want write error", err)
	}
	w.err = nil
	must(t, st.Working("a"))
	if got := len(w.take()); got != 1 {
		t.Errorf("a failed write must not count for dedupe; wrote %d", got)
	}
}

func TestStatusOSC94(t *testing.T) {
	w := &writes{}
	st := New(w, WithOSC94())
	child, _ := st.Child("c")
	must(t, st.Progress(40, ""))
	must(t, st.Working(""))
	must(t, st.BlockedProgress(KindNone, 10, ""))
	must(t, st.Blocked(KindNone, ""))
	must(t, st.Error(""))
	must(t, st.Done(""))
	must(t, st.Idle(""))
	must(t, child.Working("")) // children never mirror
	must(t, st.Clear())
	want := []string{
		seq("state=working:progress=40"), "\x1b]9;4;1;40\x1b\\",
		seq("state=working"), "\x1b]9;4;3\x1b\\",
		seq("state=blocked:progress=10"), "\x1b]9;4;4;10\x1b\\",
		seq("state=blocked"), "\x1b]9;4;4;0\x1b\\",
		seq("state=error"), "\x1b]9;4;2\x1b\\",
		seq("state=done"), "\x1b]9;4;0\x1b\\",
		seq("state=idle"), "\x1b]9;4;0\x1b\\",
		seq("state=working:id=c"),
		seq("state=clear"), "\x1b]9;4;0\x1b\\",
	}
	if got := w.take(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
}

func TestStatusFraming(t *testing.T) {
	w := &writes{}
	st := New(w, WithBEL(), WithTmuxPassthrough(), WithOSC94())
	must(t, st.Done(""))
	want := []string{
		"\x1bPtmux;\x1b\x1b]7501;state=done\a\x1b\\",
		"\x1bPtmux;\x1b\x1b]9;4;0\a\x1b\\",
	}
	if got := w.take(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("got  %q\nwant %q", got, want)
	}
	child, _ := st.Child("c") // inherits framing
	must(t, child.Done(""))
	if got := w.take(); len(got) != 1 || got[0] != "\x1bPtmux;\x1b\x1b]7501;state=done:id=c\a\x1b\\" {
		t.Errorf("child got %q", got)
	}
}

func TestStatusValidation(t *testing.T) {
	for name, st := range map[string]*Status{
		"enabled":  New(&writes{}),
		"disabled": New(&writes{}, Disabled()),
		"nil":      New(nil),
	} {
		t.Run(name, func(t *testing.T) {
			if err := st.Working("bad\nmsg"); !errors.Is(err, ErrControlChar) {
				t.Errorf("err = %v, want ErrControlChar", err)
			}
			if err := st.Progress(101, ""); !errors.Is(err, ErrProgress) {
				t.Errorf("err = %v, want ErrProgress", err)
			}
			if err := st.Blocked("coffee", ""); !errors.Is(err, ErrKind) {
				t.Errorf("err = %v, want ErrKind", err)
			}
			if err := st.Done(strings.Repeat("m", 2049)); !errors.Is(err, ErrLimit) {
				t.Errorf("err = %v, want ErrLimit", err)
			}
			if err := st.Done("fine"); err != nil {
				t.Errorf("err = %v", err)
			}
		})
	}
	w := &writes{}
	st := New(w, Disabled())
	must(t, st.Done("x"))
	if len(w.take()) != 0 {
		t.Error("disabled handle wrote")
	}
	if err := New(w, WithApp("bad app")).Idle(""); !errors.Is(err, ErrApp) {
		t.Errorf("bad WithApp err = %v", err)
	}
}

func TestStatusSanitize(t *testing.T) {
	w := &writes{}
	st := New(w, WithSanitize(), WithTitle("a\tb"))
	must(t, st.Working("line one\nline two\x1b[31m"))
	if got := w.take(); len(got) != 1 || got[0] != seq("state=working:title="+b64("a b")+":msg="+b64("line one line two [31m")) {
		t.Errorf("got %q", got)
	}
	must(t, st.Done(strings.Repeat("é", 2000)))
	r, err := ParseSequence([]byte(w.take()[0]))
	must(t, err)
	if len(r.Msg) > MaxMsgBytes || !strings.HasSuffix(r.Msg, "…") {
		t.Errorf("sanitized msg not truncated: %d bytes", len(r.Msg))
	}
}

func TestNewForFileNonTTY(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "out")
	must(t, err)
	defer f.Close()
	if IsTerminal(f) {
		t.Fatal("temp file reported as a terminal")
	}
	st := NewForFile(f, WithApp("x"))
	must(t, st.Done("ok"))
	if err := st.Done("bad\x00"); !errors.Is(err, ErrControlChar) {
		t.Errorf("no-op handle must still validate; err = %v", err)
	}
	if fi, _ := f.Stat(); fi.Size() != 0 {
		t.Errorf("non-TTY handle wrote %d bytes", fi.Size())
	}
	must(t, NewForFile(nil).Done("ok"))
	if IsTerminal(nil) {
		t.Error("IsTerminal(nil) = true")
	}
}

func TestStatusConcurrent(t *testing.T) {
	w := &writes{}
	root := New(w, WithApp("app"), WithOSC94())
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		child, err := root.Child(fmt.Sprintf("w%d", g))
		must(t, err)
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 100; i++ {
				must(t, child.Progress(i, "working"))
				must(t, root.Progressf(i, "g%d", g))
			}
			must(t, child.Done("done"))
		}(g)
	}
	wg.Wait()
	for _, s := range w.take() {
		if strings.HasPrefix(s, "\x1b]9;4;") {
			continue
		}
		if _, err := ParseSequence([]byte(s)); err != nil {
			t.Fatalf("interleaved or corrupt write %q: %v", s, err)
		}
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
