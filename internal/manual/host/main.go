//go:build manual && (darwin || linux)

// Command host is a minimal OSC 7501 terminal for trying the protocol before
// real terminals support it. It runs a command (your shell by default) in a
// pseudo-terminal and passes everything through to the terminal you run it
// in, untouched. Along the way it acts as a supporting terminal: it answers
// the OSC 7501 feature detection query, keeps records in a pst.Store, applies
// the lifetime rules, and logs every change to a file.
//
//	go run -tags manual ./internal/manual/host [-log file] [command [args...]]
//
// In another split, watch the records:
//
//	tail -f "${TMPDIR:-/tmp}/pst-host.log"
//
// Inside, $PST_HOST is set to 1. Exit the command to stop.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/adamvduke/pst"
)

func main() {
	logPath := flag.String("log", filepath.Join(os.TempDir(), "pst-host.log"), "file to log records to")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: go run -tags manual ./internal/manual/host [-log file] [command [args...]]")
		flag.PrintDefaults()
	}
	flag.Parse()
	if err := run(*logPath, flag.Args()); err != nil {
		fmt.Fprintln(os.Stderr, "pst-host:", err)
		os.Exit(1)
	}
}

func run(logPath string, args []string) error {
	if !pst.IsTerminal(os.Stdin) || !pst.IsTerminal(os.Stdout) {
		return errors.New("run me from an interactive terminal")
	}
	if len(args) == 0 {
		sh := os.Getenv("SHELL")
		if sh == "" {
			sh = "/bin/sh"
		}
		args = []string{sh}
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	defer logFile.Close()
	h := &host{
		log:   log.New(logFile, "", log.Ltime|log.Lmicroseconds),
		store: pst.NewStore(),
	}

	master, slave, err := openPTY()
	if err != nil {
		return fmt.Errorf("opening pty: %w", err)
	}
	defer master.Close()
	h.master = master
	copyWinsize(os.Stdin.Fd(), master.Fd())

	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = slave, slave, slave
	cmd.Env = append(os.Environ(), "PST_HOST=1")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true, Setctty: true, Ctty: 0}
	if err := cmd.Start(); err != nil {
		slave.Close()
		return err
	}
	slave.Close() // the child has its own copy; reads on master end when it exits

	fmt.Fprintf(os.Stderr, "pst-host: running %s; records are logged to %s\n", strings.Join(args, " "), logPath)
	fmt.Fprintf(os.Stderr, "pst-host: watch them with: tail -f %s\n", logPath)
	h.log.Printf("=== started %s (pid %d)", strings.Join(args, " "), cmd.Process.Pid)

	restore, err := makeRaw(os.Stdin.Fd())
	if err != nil {
		cmd.Process.Kill()
		return fmt.Errorf("raw mode: %w", err)
	}
	defer restore()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			copyWinsize(os.Stdin.Fd(), master.Fd())
		}
	}()

	go io.Copy(h, os.Stdin) // keystrokes and the outer terminal's replies (DA1, ...)

	pumped := make(chan struct{})
	go func() {
		h.pump()
		close(pumped)
	}()
	waitErr := cmd.Wait()
	select {
	case <-pumped:
	case <-time.After(200 * time.Millisecond): // background jobs may hold the pty open
	}

	h.event("process exited", h.store.ProcessExited())
	restore()
	h.summary(os.Stderr)
	var exit *exec.ExitError
	if errors.As(waitErr, &exit) {
		os.Exit(exit.ExitCode())
	}
	return nil
}

type host struct {
	log    *log.Logger
	store  *pst.Store
	sc     pst.Scanner
	master *os.File
	mu     sync.Mutex // serializes writes to master
	tail   []byte     // end of the previous chunk, for patterns split across reads
}

// Write forwards input to the child.
func (h *host) Write(p []byte) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.master.Write(p)
}

// pump copies the child's output to stdout, watching it on the way. Replies
// are written before the chunk is passed on, so the answer to the 7501 query
// reaches the child before the outer terminal can answer the DA1 that
// follows it.
func (h *host) pump() {
	buf := make([]byte, 32*1024)
	for {
		n, err := h.master.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			h.sc.Feed(chunk, h.onSequence)
			h.watchLifetime(chunk)
			os.Stdout.Write(chunk)
		}
		if err != nil {
			return
		}
	}
}

func (h *host) onSequence(seq []byte) {
	r, err := pst.ParseSequence(seq)
	switch {
	case errors.Is(err, pst.ErrQuery):
		h.Write(pst.Query())
		h.log.Printf("query: answered %q", pst.Query())
	case err != nil:
		h.log.Printf("rejected: %v (%d bytes)", err, len(seq))
	default:
		h.event("report", h.store.Apply(r))
	}
}

var (
	promptStart = []byte("\x1b]133;A") // OSC 133 A, a new shell prompt
	fullReset   = []byte("\x1bc")      // RIS
)

// watchLifetime spots OSC 133 A and RIS. Scanner only extracts OSC 7501, so
// this does a plain search, carrying a short tail across chunks.
func (h *host) watchLifetime(chunk []byte) {
	data := append(h.tail, chunk...)
	for i := 0; i < countNew(data, promptStart, len(h.tail)); i++ {
		h.event("prompt started (OSC 133 A)", h.store.PromptStarted())
	}
	for i := 0; i < countNew(data, fullReset, len(h.tail)); i++ {
		h.event("full reset (RIS)", h.store.Reset())
	}
	keep := len(promptStart) - 1
	if len(data) < keep {
		keep = len(data)
	}
	h.tail = append(h.tail[:0], data[len(data)-keep:]...)
}

// countNew counts matches of p in data that end past the carried-over tail,
// so a match is never counted twice.
func countNew(data, p []byte, tail int) int {
	n := 0
	for off := 0; ; {
		i := bytes.Index(data[off:], p)
		if i < 0 {
			return n
		}
		if off+i+len(p) > tail {
			n++
		}
		off += i + 1
	}
}

func (h *host) event(what string, changes []pst.Change) {
	if len(changes) == 0 {
		if what != "report" {
			h.log.Printf("%s: no records affected", what)
		}
		return
	}
	for _, c := range changes {
		switch c.Kind {
		case pst.Removed:
			h.log.Printf("%s: removed %s (was %s)", what, label(c.ID), c.Old.State)
		default:
			h.log.Printf("%s: %s %s", what, c.Kind, h.describe(*c.New))
		}
	}
	h.log.Printf("    %d record(s): %s", h.store.Len(), h.snapshot())
}

func (h *host) describe(r pst.Record) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", label(r.ID), r.State)
	if r.Kind != pst.KindNone {
		fmt.Fprintf(&b, " kind=%s", r.Kind)
	}
	if r.State == pst.StateWorking || r.State == pst.StateBlocked {
		fmt.Fprintf(&b, " progress=%s", r.Progress)
	}
	if app := h.store.EffectiveApp(r.ID); app != "" {
		inherited := ""
		if r.App == "" {
			inherited = " (inherited)"
		}
		fmt.Fprintf(&b, " app=%s%s", app, inherited)
	}
	if r.Title != "" {
		fmt.Fprintf(&b, " title=%q", pst.DisarmText(r.Title))
	}
	if r.Msg != "" {
		fmt.Fprintf(&b, " msg=%q", pst.DisarmText(r.Msg))
	}
	return b.String()
}

func (h *host) snapshot() string {
	recs := h.store.All()
	parts := make([]string, len(recs))
	for i, r := range recs {
		parts[i] = label(r.ID) + "=" + string(r.State)
	}
	return strings.Join(parts, ", ")
}

func (h *host) summary(w io.Writer) {
	recs := h.store.All()
	fmt.Fprintf(w, "pst-host: %d record(s) left after exit (done and error survive)\n", len(recs))
	for _, r := range recs {
		fmt.Fprintf(w, "  %s\n", h.describe(r))
	}
	h.log.Printf("=== exited; %d record(s) left", len(recs))
}

func label(id string) string {
	if id == "" {
		return "<root>"
	}
	return id
}

func ioctl(fd, req, arg uintptr) error {
	if _, _, errno := syscall.Syscall(syscall.SYS_IOCTL, fd, req, arg); errno != 0 {
		return errno
	}
	return nil
}

func copyWinsize(from, to uintptr) {
	var ws [4]uint16 // struct winsize: rows, cols, xpixel, ypixel
	if ioctl(from, syscall.TIOCGWINSZ, uintptr(unsafe.Pointer(&ws))) == nil {
		ioctl(to, syscall.TIOCSWINSZ, uintptr(unsafe.Pointer(&ws)))
	}
}

// makeRaw puts the terminal in raw mode, like cfmakeraw, and returns a
// function that restores it. The function is safe to call more than once.
func makeRaw(fd uintptr) (func(), error) {
	var old syscall.Termios
	if err := ioctl(fd, ioctlGetTermios, uintptr(unsafe.Pointer(&old))); err != nil {
		return nil, err
	}
	raw := old
	raw.Iflag &^= syscall.IGNBRK | syscall.BRKINT | syscall.PARMRK | syscall.ISTRIP |
		syscall.INLCR | syscall.IGNCR | syscall.ICRNL | syscall.IXON
	raw.Oflag &^= syscall.OPOST
	raw.Lflag &^= syscall.ECHO | syscall.ECHONL | syscall.ICANON | syscall.ISIG | syscall.IEXTEN
	raw.Cflag &^= syscall.CSIZE | syscall.PARENB
	raw.Cflag |= syscall.CS8
	raw.Cc[syscall.VMIN] = 1
	raw.Cc[syscall.VTIME] = 0
	if err := ioctl(fd, ioctlSetTermios, uintptr(unsafe.Pointer(&raw))); err != nil {
		return nil, err
	}
	var once sync.Once
	return func() {
		once.Do(func() { ioctl(fd, ioctlSetTermios, uintptr(unsafe.Pointer(&old))) })
	}, nil
}
