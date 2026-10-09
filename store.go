package pst

import (
	"slices"
	"strings"
	"sync"
	"time"
)

// Store is the terminal-side record set for one terminal (one window, tab,
// split, or pane attached to one pseudo-terminal). It applies reports and the
// spec's lifetime rules and reports what changed. It never presents
// anything.
//
// A Store is safe for concurrent use. Changes are returned rather than
// delivered through callbacks, so callers never re-enter the Store.
type Store struct {
	mu       sync.Mutex
	recs     map[string]*Record
	cap      int
	dropIdle bool
	now      func() time.Time
	seq      uint64
}

// Record is a stored report.
type Record struct {
	Report
	Updated time.Time // when the record was last added or replaced
	seq     uint64
}

// ChangeKind says how a record changed.
type ChangeKind int

// Change kinds.
const (
	Added ChangeKind = iota
	Replaced
	Removed
)

func (k ChangeKind) String() string {
	switch k {
	case Added:
		return "added"
	case Replaced:
		return "replaced"
	case Removed:
		return "removed"
	}
	return "unknown"
}

// Change describes one record change. Old is nil for Added and New is nil for
// Removed. Both are copies the caller may keep.
type Change struct {
	Kind ChangeKind
	ID   string
	Old  *Record
	New  *Record
}

// StoreOption configures a [Store].
type StoreOption func(*Store)

// WithCap sets the maximum number of records. Values are clamped to
// [MinRecords, MaxRecords] (64 to 256); the default is 256.
func WithCap(n int) StoreOption {
	return func(s *Store) { s.cap = min(max(n, MinRecords), MaxRecords) }
}

// WithDropIdleOnLifetime sets whether idle records are dropped, along with
// working and blocked ones, when the attached process exits or a shell prompt
// starts. The spec allows either; the default is true.
func WithDropIdleOnLifetime(drop bool) StoreOption {
	return func(s *Store) { s.dropIdle = drop }
}

// WithClock sets the clock used for [Record.Updated]. The default is
// time.Now.
func WithClock(now func() time.Time) StoreOption {
	return func(s *Store) {
		if now != nil {
			s.now = now
		}
	}
}

// NewStore returns an empty Store.
func NewStore(opts ...StoreOption) *Store {
	s := &Store{recs: make(map[string]*Record), cap: MaxRecords, dropIdle: true, now: time.Now}
	for _, o := range opts {
		if o != nil {
			o(s)
		}
	}
	return s
}

// Apply applies one report. A clear report removes the addressed record and
// every record beneath it, or every record if it has no id. Any other report
// adds or completely replaces the record for its id; adding a record past the
// cap first removes the least recently updated record.
//
// Apply checks r with [Report.Validate] first and changes nothing if it
// fails. Reports from [ParseBody] and [ParseSequence] always pass.
func (s *Store) Apply(r Report) []Change {
	if r.Validate() != nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if r.State == StateClear {
		if r.ID == "" {
			return s.removeLocked(func(*Record) bool { return true })
		}
		prefix := r.ID + "/"
		return s.removeLocked(func(rec *Record) bool {
			return rec.ID == r.ID || strings.HasPrefix(rec.ID, prefix)
		})
	}
	s.seq++
	rec := &Record{Report: r, Updated: s.now(), seq: s.seq}
	if old, ok := s.recs[r.ID]; ok {
		s.recs[r.ID] = rec
		return []Change{{Kind: Replaced, ID: r.ID, Old: copyRecord(old), New: copyRecord(rec)}}
	}
	var changes []Change
	if len(s.recs) >= s.cap {
		var lru *Record
		for _, c := range s.recs {
			if lru == nil || c.seq < lru.seq {
				lru = c
			}
		}
		delete(s.recs, lru.ID)
		changes = append(changes, Change{Kind: Removed, ID: lru.ID, Old: copyRecord(lru)})
	}
	s.recs[r.ID] = rec
	return append(changes, Change{Kind: Added, ID: r.ID, New: copyRecord(rec)})
}

// ProcessExited applies the lifetime rule for the attached process exiting:
// working and blocked records are dropped, and idle records too unless
// WithDropIdleOnLifetime(false) was given. done and error records survive.
func (s *Store) ProcessExited() []Change { return s.lifetimeEvent() }

// PromptStarted applies the lifetime rule for a new shell prompt (OSC 133 A).
// It is the same rule as ProcessExited.
func (s *Store) PromptStarted() []Change { return s.lifetimeEvent() }

func (s *Store) lifetimeEvent() []Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(func(rec *Record) bool {
		switch rec.State {
		case StateWorking, StateBlocked:
			return true
		case StateIdle:
			return s.dropIdle
		}
		return false
	})
}

// Reset applies a full reset (RIS): every record is removed. A soft reset
// (DECSTR) and switching screens do not affect records, so they have no
// method.
func (s *Store) Reset() []Change {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.removeLocked(func(*Record) bool { return true })
}

// removeLocked removes matching records and returns changes sorted by id.
func (s *Store) removeLocked(match func(*Record) bool) []Change {
	var changes []Change
	for id, rec := range s.recs {
		if match(rec) {
			delete(s.recs, id)
			changes = append(changes, Change{Kind: Removed, ID: id, Old: copyRecord(rec)})
		}
	}
	slices.SortFunc(changes, func(a, b Change) int { return compareIDs(a.ID, b.ID) })
	return changes
}

// Get returns the record for id ("" for the root record).
func (s *Store) Get(id string) (Record, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	rec, ok := s.recs[id]
	if !ok {
		return Record{}, false
	}
	return *rec, true
}

// All returns every record in tree order: the root record first, and each
// record before its descendants.
func (s *Store) All() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collectLocked(func(*Record) bool { return true })
}

// Children returns the records directly beneath id, in tree order. It is
// structural: a record whose parent does not exist is not a child of its
// grandparent.
func (s *Store) Children(id string) []Record {
	prefix := ""
	if id != "" {
		prefix = id + "/"
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.collectLocked(func(rec *Record) bool {
		rest, ok := strings.CutPrefix(rec.ID, prefix)
		return ok && rest != "" && !strings.Contains(rest, "/")
	})
}

func (s *Store) collectLocked(match func(*Record) bool) []Record {
	out := make([]Record, 0, len(s.recs))
	for _, rec := range s.recs {
		if match(rec) {
			out = append(out, *rec)
		}
	}
	slices.SortFunc(out, func(a, b Record) int { return compareIDs(a.ID, b.ID) })
	return out
}

// EffectiveApp returns the app a record takes for display: its own, or else
// that of its nearest ancestor that has one. Ancestors that do not exist are
// skipped, and the result is "" if no record on the path has an app. id need
// not exist itself.
func (s *Store) EffectiveApp(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	for {
		if rec, ok := s.recs[id]; ok && rec.App != "" {
			return rec.App
		}
		if id == "" {
			return ""
		}
		if i := strings.LastIndexByte(id, '/'); i >= 0 {
			id = id[:i]
		} else {
			id = ""
		}
	}
}

// Len returns the number of records.
func (s *Store) Len() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.recs)
}

func copyRecord(r *Record) *Record {
	c := *r
	return &c
}

// compareIDs orders ids as a tree: "/" sorts before every other byte, so
// "build" < "build/test" < "build-x".
func compareIDs(a, b string) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		ca, cb := a[i], b[i]
		if ca == cb {
			continue
		}
		if ca == '/' {
			return -1
		}
		if cb == '/' {
			return 1
		}
		if ca < cb {
			return -1
		}
		return 1
	}
	return len(a) - len(b)
}
