package pst

import (
	"errors"
	"sync"
	"testing"
)

func TestOSC94Encode(t *testing.T) {
	tests := []struct {
		r    Report
		want string
	}{
		{Report{State: StateWorking, Progress: Percent(40)}, "\x1b]9;4;1;40\x1b\\"},
		{Report{State: StateWorking, Progress: Percent(0)}, "\x1b]9;4;1;0\x1b\\"},
		{Report{State: StateWorking}, "\x1b]9;4;3\x1b\\"},
		{Report{State: StateBlocked, Progress: Percent(60)}, "\x1b]9;4;4;60\x1b\\"},
		{Report{State: StateBlocked, Kind: KindAuth}, "\x1b]9;4;4;0\x1b\\"},
		{Report{State: StateError}, "\x1b]9;4;2\x1b\\"},
		{Report{State: StateIdle}, "\x1b]9;4;0\x1b\\"},
		{Report{State: StateDone}, "\x1b]9;4;0\x1b\\"},
		{Report{State: StateClear}, "\x1b]9;4;0\x1b\\"},
	}
	for _, tt := range tests {
		got, err := OSC94(tt.r)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != tt.want {
			t.Errorf("OSC94(%+v) = %q, want %q", tt.r, got, tt.want)
		}
	}
	if got, _ := OSC94(Report{State: StateDone}, WithBEL()); string(got) != "\x1b]9;4;0\a" {
		t.Errorf("BEL: %q", got)
	}
	if _, err := OSC94(Report{State: StateDone, Progress: Percent(1)}); !errors.Is(err, ErrProgress) {
		t.Errorf("invalid report err = %v", err)
	}
}

func TestOSC94Mapper(t *testing.T) {
	tests := []struct {
		params string
		want   Report
		ok     bool
	}{
		{"4;0", Report{State: StateClear}, true},
		{"4;0;50", Report{State: StateClear}, true},
		{"4;1;40", Report{State: StateWorking, Progress: Percent(40)}, true},
		{"4;1;0", Report{State: StateWorking, Progress: Percent(0)}, true},
		{"4;1;150", Report{State: StateWorking, Progress: Percent(100)}, true},
		{"4;1;99999999999", Report{State: StateWorking, Progress: Percent(100)}, true},
		{"4;1", Report{State: StateWorking}, true},
		{"4;1;", Report{State: StateWorking}, true},
		{"4;1;x", Report{State: StateWorking}, true},
		{"4;1;-5", Report{State: StateWorking}, true},
		{"4;2", Report{State: StateError}, true},
		{"4;2;30", Report{State: StateError}, true},
		{"4;3", Report{State: StateWorking}, true},
		{"4;4", Report{State: StateBlocked}, true},
		{"4;4;70", Report{State: StateBlocked, Progress: Percent(70)}, true},
		{"4;5", Report{}, false},
		{"4;", Report{}, false},
		{"4", Report{}, false},
		{"", Report{}, false},
		{"Hello", Report{}, false}, // iTerm2 notification, OSC 9 ; text
		{"9;4;1;40", Report{}, false},
	}
	var m OSC94Mapper
	for _, tt := range tests {
		got, ok := m.Map([]byte(tt.params))
		if ok != tt.ok || got != tt.want {
			t.Errorf("Map(%q) = %+v, %v; want %+v, %v", tt.params, got, ok, tt.want, tt.ok)
		}
		if ok {
			if err := got.Validate(); err != nil {
				t.Errorf("Map(%q) result invalid: %v", tt.params, err)
			}
		}
	}
}

func TestOSC94MapperStopsAfter7501(t *testing.T) {
	var m OSC94Mapper
	if _, ok := m.Map([]byte("4;1;10")); !ok {
		t.Fatal("not mapping initially")
	}
	m.Saw7501()
	if _, ok := m.Map([]byte("4;1;10")); ok {
		t.Error("still mapping after an OSC 7501 report")
	}
	m.Reset()
	if _, ok := m.Map([]byte("4;1;10")); !ok {
		t.Error("not mapping after Reset")
	}
}

func TestOSC94RoundTrip(t *testing.T) {
	var m OSC94Mapper
	for _, r := range []Report{
		{State: StateWorking, Progress: Percent(40)},
		{State: StateWorking},
		{State: StateBlocked, Progress: Percent(10)},
		{State: StateError},
		{State: StateClear},
	} {
		seq, err := OSC94(r)
		if err != nil {
			t.Fatal(err)
		}
		params := seq[len("\x1b]9;") : len(seq)-2]
		got, ok := m.Map(params)
		if !ok || got != r {
			t.Errorf("round trip of %+v = %+v, %v", r, got, ok)
		}
	}
}

func TestOSC94MapperConcurrent(t *testing.T) {
	var m OSC94Mapper
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				m.Map([]byte("4;1;5"))
				m.Saw7501()
				m.Reset()
			}
		}()
	}
	wg.Wait()
}
