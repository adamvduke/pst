package pst

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
)

type statusConfig struct {
	enc      encodeConfig
	app      string
	title    string
	sanitize bool
	osc94    bool
	noDedupe bool
	disabled bool
}

type statusOption func(*statusConfig)

func (o statusOption) applyStatus(c *statusConfig) { o(c) }

// WithApp sets the app included in every report from the handle. Children
// take their app from the nearest ancestor on the terminal side, so it is
// rarely needed on a child.
func WithApp(app string) Option { return statusOption(func(c *statusConfig) { c.app = app }) }

// WithTitle sets the title included in every report from the handle. It is
// not inherited by children.
func WithTitle(title string) Option {
	return statusOption(func(c *statusConfig) { c.title = title })
}

// WithSanitize runs [SanitizeText] on msg and title instead of returning an
// error for control characters, invalid UTF-8, or excess length.
func WithSanitize() Option { return statusOption(func(c *statusConfig) { c.sanitize = true }) }

// WithOSC94 also mirrors the root record as ConEmu OSC 9;4 progress (see
// [OSC94]), written after each OSC 7501 report. Child handles never mirror.
func WithOSC94() Option { return statusOption(func(c *statusConfig) { c.osc94 = true }) }

// WithoutDedupe sends every report, even one byte-identical to the last
// report sent for the same record.
func WithoutDedupe() Option { return statusOption(func(c *statusConfig) { c.noDedupe = true }) }

// Disabled makes a handle that validates reports and returns the same errors
// but never writes.
func Disabled() Option { return statusOption(func(c *statusConfig) { c.disabled = true }) }

// Status reports the state of one record. It is what most programs use.
//
// Each report is a single Write to the underlying writer, plus a separate
// Write for OSC 9;4 when [WithOSC94] is set. A handle and all of its children
// share one mutex, so they are safe for concurrent use and their sequences
// never interleave. By default a report byte-identical to the last one sent
// for the same record is skipped; there is no time-based throttling.
type Status struct {
	sh  *sharedWriter
	id  string
	cfg statusConfig
}

type sharedWriter struct {
	mu   sync.Mutex
	w    io.Writer
	buf  []byte
	last map[string][]byte // last sequence sent per record id, for dedupe
}

// New returns a handle for the root record that writes to w. A nil w makes a
// disabled handle.
func New(w io.Writer, opts ...Option) *Status {
	var cfg statusConfig
	for _, o := range opts {
		if o != nil {
			o.applyStatus(&cfg)
		}
	}
	if w == nil {
		cfg.disabled = true
	}
	return &Status{sh: &sharedWriter{w: w, last: make(map[string][]byte)}, cfg: cfg}
}

// NewForFile is like [New], but returns a disabled handle unless f is a
// terminal, so output redirected to a file or pipe stays clean.
func NewForFile(f *os.File, opts ...Option) *Status {
	if f == nil || !IsTerminal(f) {
		opts = append(opts[:len(opts):len(opts)], Disabled())
	}
	if f == nil {
		return New(nil, opts...)
	}
	return New(f, opts...)
}

// Child returns a handle for the record segment beneath s, such as "test"
// beneath "build" for the id "build/test". segment must match
// [A-Za-z0-9_.+-]{1,32}; see [SanitizeSegment]. The child shares s's writer
// and settings except app, title, and OSC 9;4 mirroring; opts apply on top.
func (s *Status) Child(segment string, opts ...Option) (*Status, error) {
	if !validSegment(segment) {
		if len(segment) > MaxSegmentBytes {
			return nil, invalid("id", ErrLimit, "segment exceeds 32 bytes")
		}
		return nil, invalid("id", ErrID, fmt.Sprintf("invalid segment %q", truncateForError(segment)))
	}
	id := segment
	if s.id != "" {
		id = s.id + "/" + segment
	}
	if reason, limit := checkID(id); reason != "" {
		if limit {
			return nil, invalid("id", ErrLimit, reason)
		}
		return nil, invalid("id", ErrID, reason)
	}
	cfg := s.cfg
	cfg.app, cfg.title, cfg.osc94 = "", "", false
	for _, o := range opts {
		if o != nil {
			o.applyStatus(&cfg)
		}
	}
	return &Status{sh: s.sh, id: id, cfg: cfg}, nil
}

// ID returns the handle's record id, "" for the root record.
func (s *Status) ID() string { return s.id }

// Idle reports that the program is at rest, waiting for the user. Report idle
// when the user interrupts or cancels.
func (s *Status) Idle(msg string) error { return s.send(Report{State: StateIdle, Msg: msg}) }

// Working reports that the program is busy, with no percentage.
func (s *Status) Working(msg string) error { return s.send(Report{State: StateWorking, Msg: msg}) }

// Progress reports that the program is busy and pct percent done.
func (s *Status) Progress(pct int, msg string) error {
	return s.send(Report{State: StateWorking, Progress: Percent(pct), Msg: msg})
}

// Blocked reports that the program cannot continue until the user acts. kind
// may be KindNone.
func (s *Status) Blocked(kind Kind, msg string) error {
	return s.send(Report{State: StateBlocked, Kind: kind, Msg: msg})
}

// BlockedProgress is Blocked with a percentage.
func (s *Status) BlockedProgress(kind Kind, pct int, msg string) error {
	return s.send(Report{State: StateBlocked, Kind: kind, Progress: Percent(pct), Msg: msg})
}

// Done reports that a piece of work finished. A program that exits when it
// finishes should report Done or Error just before exiting.
func (s *Status) Done(msg string) error { return s.send(Report{State: StateDone, Msg: msg}) }

// Error reports that the program failed and stopped.
func (s *Status) Error(msg string) error { return s.send(Report{State: StateError, Msg: msg}) }

// Clear removes the handle's record and every record beneath it. On the root
// handle that is every record on the terminal.
func (s *Status) Clear() error { return s.send(Report{State: StateClear}) }

// Report sends r for the handle's record. The handle always sets r.ID, and
// fills r.App and r.Title from its settings when they are empty (except on
// clear reports).
func (s *Status) Report(r Report) error {
	r.ID = s.id
	return s.send(r)
}

// Idlef is Idle with fmt.Sprintf formatting.
func (s *Status) Idlef(format string, args ...any) error { return s.Idle(fmt.Sprintf(format, args...)) }

// Workingf is Working with fmt.Sprintf formatting.
func (s *Status) Workingf(format string, args ...any) error {
	return s.Working(fmt.Sprintf(format, args...))
}

// Progressf is Progress with fmt.Sprintf formatting.
func (s *Status) Progressf(pct int, format string, args ...any) error {
	return s.Progress(pct, fmt.Sprintf(format, args...))
}

// Blockedf is Blocked with fmt.Sprintf formatting.
func (s *Status) Blockedf(kind Kind, format string, args ...any) error {
	return s.Blocked(kind, fmt.Sprintf(format, args...))
}

// Donef is Done with fmt.Sprintf formatting.
func (s *Status) Donef(format string, args ...any) error { return s.Done(fmt.Sprintf(format, args...)) }

// Errorf is Error with fmt.Sprintf formatting.
func (s *Status) Errorf(format string, args ...any) error {
	return s.Error(fmt.Sprintf(format, args...))
}

func (s *Status) send(r Report) error {
	r.ID = s.id
	if r.State != StateClear {
		if r.App == "" {
			r.App = s.cfg.app
		}
		if r.Title == "" {
			r.Title = s.cfg.title
		}
	}
	if s.cfg.sanitize {
		r.Title = SanitizeText(r.Title, MaxTitleBytes)
		r.Msg = SanitizeText(r.Msg, MaxMsgBytes)
	}
	if err := r.Validate(); err != nil {
		return err
	}
	if s.cfg.disabled {
		return nil
	}

	sh := s.sh
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.buf = r.appendSequence(sh.buf[:0], s.cfg.enc)
	dedupe := !s.cfg.noDedupe && r.State != StateClear
	if last, ok := sh.last[s.id]; dedupe && ok && bytes.Equal(last, sh.buf) {
		return nil
	}
	if _, err := sh.w.Write(sh.buf); err != nil {
		return err
	}
	if r.State == StateClear {
		sh.forgetLocked(s.id)
	} else if dedupe {
		sh.last[s.id] = append(sh.last[s.id][:0], sh.buf...)
	}
	if s.cfg.osc94 && s.id == "" {
		sh.buf = appendOSC94(sh.buf[:0], r, s.cfg.enc)
		if _, err := sh.w.Write(sh.buf); err != nil {
			return err
		}
	}
	return nil
}

// forgetLocked drops dedupe state for id and its subtree, which a clear
// removed on the terminal.
func (sh *sharedWriter) forgetLocked(id string) {
	if id == "" {
		clear(sh.last)
		return
	}
	prefix := id + "/"
	for k := range sh.last {
		if k == id || strings.HasPrefix(k, prefix) {
			delete(sh.last, k)
		}
	}
}
