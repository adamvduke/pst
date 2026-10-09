package pst

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"
)

const (
	replyST  = "\x1b]7501;?\x1b\\"
	replyBEL = "\x1b]7501;?\a"
	da1Reply = "\x1b[?62;22;52c"
)

func TestReplyClassifier(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		decided Support
		done    bool
	}{
		{"7501 then DA1", replyST + da1Reply, Supported, true},
		{"7501 only so far", replyST, Supported, false},
		{"DA1 only", da1Reply, Unsupported, true},
		{"DA1 then 7501", da1Reply + replyST, Unsupported, true},
		{"BEL reply", replyBEL + da1Reply, Supported, true},
		{"extra pairs after ?", "\x1b]7501;?:version=2:x=y\x1b\\" + da1Reply, Supported, true},
		{"garbage interleaved", "abc\x1b[1;1R\x1b]0;t\axyz" + replyST + "\x1b[2J" + "q" + da1Reply, Supported, true},
		{"not a reply: other OSC", "\x1b]7502;?\a" + da1Reply, Unsupported, true},
		{"not a reply: report", "\x1b]7501;state=idle\a" + da1Reply, Unsupported, true},
		{"not DA1: DA2", "\x1b[>1;10;0c", Unknown, false},
		{"not DA1: no ?", "\x1b[62c", Unknown, false},
		{"other CSI", "\x1b[?1;2$y", Unknown, false},
		{"nothing", "", Unknown, false},
		{"ESC restarts OSC", "\x1b]7501\x1b]7501;?\a" + da1Reply, Supported, true},
		{"CAN cancels", "\x1b]7501;?\x18\a" + da1Reply, Unsupported, true},
		{"minimal DA1", "\x1b[?c", Unsupported, true},
		{"input after done ignored", da1Reply + replyST, Unsupported, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var c replyClassifier
			decided, done := c.Feed([]byte(tt.input))
			if decided != tt.decided || done != tt.done {
				t.Errorf("Feed = %v, %v; want %v, %v", decided, done, tt.decided, tt.done)
			}
			// Byte by byte must agree.
			var c2 replyClassifier
			for i := 0; i < len(tt.input); i++ {
				decided, done = c2.Feed([]byte{tt.input[i]})
			}
			if len(tt.input) > 0 && (decided != tt.decided || done != tt.done) {
				t.Errorf("byte by byte = %v, %v; want %v, %v", decided, done, tt.decided, tt.done)
			}
		})
	}
}

func TestSupportString(t *testing.T) {
	for s, want := range map[Support]string{Unknown: "unknown", Supported: "supported", Unsupported: "unsupported", 99: "unknown"} {
		if s.String() != want {
			t.Errorf("%d.String() = %q", s, s.String())
		}
	}
}

func TestDetectNonTTY(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "notatty")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	s, err := Detect(ctx, f)
	if s != Unknown || !errors.Is(err, ErrNoTTY) {
		t.Errorf("Detect(file) = %v, %v; want Unknown, ErrNoTTY", s, err)
	}
	if fi, _ := f.Stat(); fi.Size() != 0 {
		t.Errorf("Detect wrote %d bytes to a non-terminal", fi.Size())
	}
}
