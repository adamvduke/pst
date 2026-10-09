package pst

import (
	"errors"
	"strings"
	"testing"
)

func TestValidate(t *testing.T) {
	tests := []struct {
		name  string
		r     Report
		field string
		errs  []error // all must match with errors.Is
	}{
		{"ok minimal", Report{State: StateIdle}, "", nil},
		{"ok full", Report{State: StateBlocked, ID: "a/b", Kind: KindAuth, Progress: Percent(100), App: "a", Title: "t", Msg: "m"}, "", nil},
		{"ok clear with text", Report{State: StateClear, App: "a", Title: "t", Msg: "m"}, "", nil},
		{"empty state", Report{}, "state", []error{ErrState}},
		{"bad state", Report{State: "sleeping"}, "state", []error{ErrState}},
		{"bad id char", Report{State: StateIdle, ID: "a b"}, "id", []error{ErrID}},
		{"empty segment", Report{State: StateIdle, ID: "a//b"}, "id", []error{ErrID}},
		{"long segment", Report{State: StateIdle, ID: strings.Repeat("a", 33)}, "id", []error{ErrID, ErrLimit}},
		{"deep id", Report{State: StateIdle, ID: "1/2/3/4/5/6/7/8/9"}, "id", []error{ErrID, ErrLimit}},
		{"long id", Report{State: StateIdle, ID: strings.Repeat("abcdefg/", 16) + "a"}, "id", []error{ErrID, ErrLimit}},
		{"unknown kind", Report{State: StateBlocked, Kind: "coffee"}, "kind", []error{ErrKind}},
		{"kind on working", Report{State: StateWorking, Kind: KindAuth}, "kind", []error{ErrKind}},
		{"progress on done", Report{State: StateDone, Progress: Percent(1)}, "progress", []error{ErrProgress}},
		{"progress on clear", Report{State: StateClear, Progress: Percent(1)}, "progress", []error{ErrProgress}},
		{"progress 101", Report{State: StateWorking, Progress: Percent(101)}, "progress", []error{ErrProgress}},
		{"progress -1", Report{State: StateWorking, Progress: Percent(-1)}, "progress", []error{ErrProgress}},
		{"bad app", Report{State: StateIdle, App: "my app"}, "app", []error{ErrApp}},
		{"long app", Report{State: StateIdle, App: strings.Repeat("a", 33)}, "app", []error{ErrApp, ErrLimit}},
		{"control in msg", Report{State: StateIdle, Msg: "a\tb"}, "msg", []error{ErrControlChar}},
		{"C1 in title", Report{State: StateIdle, Title: "\u009b"}, "title", []error{ErrControlChar}},
		{"invalid UTF-8", Report{State: StateIdle, Msg: "\xff"}, "msg", []error{ErrUTF8}},
		{"long msg", Report{State: StateIdle, Msg: strings.Repeat("m", 2049)}, "msg", []error{ErrLimit}},
		{"long title", Report{State: StateIdle, Title: strings.Repeat("t", 193)}, "title", []error{ErrLimit}},
		{"max msg", Report{State: StateIdle, Msg: strings.Repeat("m", 2048)}, "", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.r.Validate()
			if tt.errs == nil {
				if err != nil {
					t.Fatalf("Validate() = %v, want nil", err)
				}
				return
			}
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("Validate() = %v, want *ValidationError", err)
			}
			if ve.Field != tt.field {
				t.Errorf("Field = %q, want %q", ve.Field, tt.field)
			}
			for _, want := range tt.errs {
				if !errors.Is(err, want) {
					t.Errorf("errors.Is(%v, %v) = false", err, want)
				}
			}
			if !strings.HasPrefix(err.Error(), "pst: invalid "+tt.field+": ") {
				t.Errorf("Error() = %q", err.Error())
			}
		})
	}
}

func TestValidateErrorIsNotOtherSentinels(t *testing.T) {
	err := Report{State: StateIdle, Msg: "\x01"}.Validate()
	for _, other := range []error{ErrState, ErrID, ErrApp, ErrKind, ErrProgress, ErrLimit, ErrUTF8} {
		if errors.Is(err, other) {
			t.Errorf("control-char error matches %v", other)
		}
	}
}

func TestProgress(t *testing.T) {
	var p Progress
	if v, ok := p.Value(); ok || v != 0 {
		t.Errorf("zero Progress = %d, %v", v, ok)
	}
	if p.String() != "indeterminate" {
		t.Errorf("String() = %q", p.String())
	}
	p = Percent(140)
	if v, ok := p.Value(); !ok || v != 140 {
		t.Errorf("Percent(140) = %d, %v; must not clamp", v, ok)
	}
	if Percent(40).String() != "40%" {
		t.Errorf("String() = %q", Percent(40).String())
	}
	if Percent(0) == (Progress{}) {
		t.Error("Percent(0) must differ from the zero value")
	}
}

func TestStateAndKindValid(t *testing.T) {
	for _, s := range []State{StateIdle, StateWorking, StateDone, StateBlocked, StateError, StateClear} {
		if !s.Valid() {
			t.Errorf("%q not valid", s)
		}
	}
	for _, s := range []State{"", "Idle", "paused"} {
		if s.Valid() {
			t.Errorf("%q valid", s)
		}
	}
	for _, k := range []Kind{KindPermission, KindQuestion, KindAuth} {
		if !k.Valid() {
			t.Errorf("%q not valid", k)
		}
	}
	if KindNone.Valid() || Kind("other").Valid() {
		t.Error("KindNone or unknown kind reported valid")
	}
}
