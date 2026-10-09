package pst

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func ids(recs []Record) string {
	s := make([]string, len(recs))
	for i, r := range recs {
		s[i] = r.ID
		if s[i] == "" {
			s[i] = "<root>"
		}
	}
	return strings.Join(s, ",")
}

func changeIDs(cs []Change) string {
	s := make([]string, len(cs))
	for i, c := range cs {
		s[i] = c.Kind.String() + ":" + c.ID
	}
	return strings.Join(s, ",")
}

func fakeClock() func() time.Time {
	t := time.Date(2026, 10, 9, 0, 0, 0, 0, time.UTC)
	return func() time.Time { t = t.Add(time.Second); return t }
}

func TestStoreApplyReplace(t *testing.T) {
	s := NewStore(WithClock(fakeClock()))
	c := s.Apply(Report{State: StateWorking, App: "brew", Title: "T", Msg: "a"})
	if changeIDs(c) != "added:" || c[0].Old != nil || c[0].New.Msg != "a" {
		t.Fatalf("changes = %+v", c)
	}
	c = s.Apply(Report{State: StateDone, Msg: "b"})
	if changeIDs(c) != "replaced:" || c[0].Old.Msg != "a" || c[0].New.Msg != "b" {
		t.Fatalf("changes = %+v", c)
	}
	rec, ok := s.Get("")
	if !ok || rec.App != "" || rec.Title != "" || rec.State != StateDone {
		t.Errorf("report must replace the record completely; got %+v", rec)
	}
	if !rec.Updated.Equal(time.Date(2026, 10, 9, 0, 0, 2, 0, time.UTC)) {
		t.Errorf("Updated = %v", rec.Updated)
	}
	// Changes are copies.
	c[0].New.Msg = "mutated"
	if rec, _ := s.Get(""); rec.Msg != "b" {
		t.Error("mutating a Change affected the store")
	}
}

func TestStoreRejectsInvalid(t *testing.T) {
	s := NewStore()
	if c := s.Apply(Report{State: "bogus"}); c != nil || s.Len() != 0 {
		t.Errorf("invalid report applied: %v", c)
	}
	if c := s.Apply(Report{State: StateIdle, ID: "a//b"}); c != nil || s.Len() != 0 {
		t.Errorf("invalid id applied: %v", c)
	}
}

func TestStoreClearSubtree(t *testing.T) {
	s := NewStore()
	for _, id := range []string{"", "build", "build/test", "build/test/unit", "builder", "build-x", "other"} {
		s.Apply(Report{State: StateWorking, ID: id})
	}
	c := s.Apply(Report{State: StateClear, ID: "build"})
	if got := changeIDs(c); got != "removed:build,removed:build/test,removed:build/test/unit" {
		t.Errorf("clear build removed %s", got)
	}
	if got := ids(s.All()); got != "<root>,build-x,builder,other" {
		t.Errorf("remaining = %s", got)
	}
	if c := s.Apply(Report{State: StateClear, ID: "missing"}); len(c) != 0 {
		t.Errorf("clearing a missing id = %v", c)
	}
	c = s.Apply(Report{State: StateClear})
	if len(c) != 4 || s.Len() != 0 {
		t.Errorf("clear with no id: %s, %d left", changeIDs(c), s.Len())
	}
}

func TestStoreClearChildKeepsParent(t *testing.T) {
	s := NewStore()
	s.Apply(Report{State: StateWorking, ID: "a"})
	s.Apply(Report{State: StateWorking, ID: "a/b"})
	s.Apply(Report{State: StateClear, ID: "a/b"})
	if got := ids(s.All()); got != "a" {
		t.Errorf("remaining = %s", got)
	}
}

func TestStoreLRU(t *testing.T) {
	s := NewStore(WithCap(MinRecords))
	for i := 0; i < MinRecords; i++ {
		s.Apply(Report{State: StateWorking, ID: fmt.Sprintf("r%d", i)})
	}
	// Touch r0 so r1 becomes least recently updated.
	if c := s.Apply(Report{State: StateDone, ID: "r0"}); changeIDs(c) != "replaced:r0" {
		t.Fatalf("replacing at cap = %s; must never evict", changeIDs(c))
	}
	c := s.Apply(Report{State: StateWorking, ID: "new"})
	if got := changeIDs(c); got != "removed:r1,added:new" {
		t.Errorf("changes = %s", got)
	}
	c = s.Apply(Report{State: StateWorking, ID: "new2"})
	if got := changeIDs(c); got != "removed:r2,added:new2" {
		t.Errorf("changes = %s", got)
	}
	if s.Len() != MinRecords {
		t.Errorf("Len = %d", s.Len())
	}
	if _, ok := s.Get("r0"); !ok {
		t.Error("recently updated r0 was evicted")
	}
}

func TestStoreCapClamp(t *testing.T) {
	for _, tt := range []struct{ in, want int }{{0, 64}, {10, 64}, {100, 100}, {256, 256}, {1000, 256}} {
		if got := NewStore(WithCap(tt.in)).cap; got != tt.want {
			t.Errorf("WithCap(%d) = %d, want %d", tt.in, got, tt.want)
		}
	}
	if NewStore().cap != MaxRecords {
		t.Error("default cap is not 256")
	}
}

func TestStoreLifetime(t *testing.T) {
	setup := func(opts ...StoreOption) *Store {
		s := NewStore(opts...)
		s.Apply(Report{State: StateIdle})
		s.Apply(Report{State: StateWorking, ID: "w"})
		s.Apply(Report{State: StateBlocked, ID: "b"})
		s.Apply(Report{State: StateDone, ID: "d"})
		s.Apply(Report{State: StateError, ID: "e"})
		return s
	}
	for _, event := range []string{"exit", "prompt"} {
		fire := func(s *Store) []Change {
			if event == "exit" {
				return s.ProcessExited()
			}
			return s.PromptStarted()
		}
		s := setup()
		c := fire(s)
		if got := changeIDs(c); got != "removed:,removed:b,removed:w" {
			t.Errorf("%s: changes = %s", event, got)
		}
		if got := ids(s.All()); got != "d,e" {
			t.Errorf("%s: remaining = %s", event, got)
		}
		s = setup(WithDropIdleOnLifetime(false))
		fire(s)
		if got := ids(s.All()); got != "<root>,d,e" {
			t.Errorf("%s keeping idle: remaining = %s", event, got)
		}
	}
}

func TestStoreReset(t *testing.T) {
	s := NewStore()
	s.Apply(Report{State: StateDone})
	s.Apply(Report{State: StateError, ID: "x/y"})
	if c := s.Reset(); len(c) != 2 || s.Len() != 0 {
		t.Errorf("Reset: %s", changeIDs(c))
	}
}

func TestStoreTreeOrderAndChildren(t *testing.T) {
	s := NewStore()
	for _, id := range []string{"b", "a-x", "a/z", "a", "a/b", "a/b/c", "", "c/d"} {
		s.Apply(Report{State: StateIdle, ID: id})
	}
	if got := ids(s.All()); got != "<root>,a,a/b,a/b/c,a/z,a-x,b,c/d" {
		t.Errorf("All = %s", got)
	}
	if got := ids(s.Children("")); got != "a,a-x,b" {
		t.Errorf("Children(root) = %s", got)
	}
	if got := ids(s.Children("a")); got != "a/b,a/z" {
		t.Errorf("Children(a) = %s", got)
	}
	if got := ids(s.Children("c")); got != "c/d" {
		t.Errorf("Children(c) = %s (parent need not exist)", got)
	}
	if got := s.Children("a/b/c"); len(got) != 0 {
		t.Errorf("Children(leaf) = %v", got)
	}
}

func TestStoreEffectiveApp(t *testing.T) {
	s := NewStore()
	s.Apply(Report{State: StateWorking, App: "deploy"})
	s.Apply(Report{State: StateWorking, ID: "a/b/c"}) // a and a/b do not exist
	s.Apply(Report{State: StateWorking, ID: "x", App: "other"})
	s.Apply(Report{State: StateWorking, ID: "x/y"})
	tests := map[string]string{
		"":        "deploy",
		"a/b/c":   "deploy",
		"a":       "deploy",
		"missing": "deploy",
		"x":       "other",
		"x/y":     "other",
		"x/y/z":   "other",
	}
	for id, want := range tests {
		if got := s.EffectiveApp(id); got != want {
			t.Errorf("EffectiveApp(%q) = %q, want %q", id, got, want)
		}
	}
	if got := NewStore().EffectiveApp("a"); got != "" {
		t.Errorf("empty store EffectiveApp = %q", got)
	}
}

// TestStoreSpecReplay replays the spec's "Several records" example.
func TestStoreSpecReplay(t *testing.T) {
	steps := []struct {
		body    string
		changes string
		records string // id=state, in tree order
	}{
		{"state=working:app=deploy:msg=RGVwbG95aW5nIHYyLjQuMQ==", "added:", "=working"},
		{"state=working:id=us-east:title=VVMgRWFzdA==:progress=40:msg=UHVzaGluZyBpbWFnZQ==", "added:us-east", "=working,us-east=working"},
		{"state=blocked:kind=permission:id=eu-west:title=RVUgV2VzdA==:msg=QXBwcm92ZSBkZXBsb3kgdG8gZXUtd2VzdCAocHJvZHVjdGlvbik/", "added:eu-west", "=working,eu-west=blocked,us-east=working"},
		{"state=done:id=us-east:title=VVMgRWFzdA==:msg=SGVhbHRoeQ==", "replaced:us-east", "=working,eu-west=blocked,us-east=done"},
		{"state=clear:id=us-east", "removed:us-east", "=working,eu-west=blocked"},
		{"state=clear:id=eu-west", "removed:eu-west", "=working"},
		{"state=done:app=deploy:msg=RGVwbG95ZWQgdG8gMyByZWdpb25z", "replaced:", "=done"},
	}
	s := NewStore()
	for i, step := range steps {
		r, err := ParseBody([]byte(step.body))
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
		if got := changeIDs(s.Apply(r)); got != step.changes {
			t.Errorf("step %d: changes = %s, want %s", i, got, step.changes)
		}
		var recs []string
		for _, rec := range s.All() {
			recs = append(recs, rec.ID+"="+string(rec.State))
		}
		if got := strings.Join(recs, ","); got != step.records {
			t.Errorf("step %d: records = %s, want %s", i, got, step.records)
		}
		if i == 2 {
			// The region records take app=deploy from the root.
			if got := s.EffectiveApp("eu-west"); got != "deploy" {
				t.Errorf("EffectiveApp(eu-west) = %q", got)
			}
		}
	}
	// The done record survives the shell prompt that follows.
	s.PromptStarted()
	if rec, ok := s.Get(""); !ok || rec.Msg != "Deployed to 3 regions" {
		t.Errorf("done record did not survive the prompt: %+v", rec)
	}
}

func TestStoreConcurrent(t *testing.T) {
	s := NewStore(WithCap(64))
	var wg sync.WaitGroup
	for g := 0; g < 8; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				id := fmt.Sprintf("g%d/r%d", g, i%80)
				s.Apply(Report{State: StateWorking, ID: id})
				s.Get(id)
				s.All()
				s.EffectiveApp(id)
				if i%50 == 0 {
					s.Apply(Report{State: StateClear, ID: fmt.Sprintf("g%d", g)})
					s.PromptStarted()
				}
			}
		}(g)
	}
	wg.Wait()
	if s.Len() > 64 {
		t.Errorf("Len = %d exceeds cap", s.Len())
	}
}
