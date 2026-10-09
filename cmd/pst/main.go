// Command pst reports program status to the terminal with the Program Status
// Protocol (OSC 7501), and helps debug it.
//
//	pst working "Syncing photos"
//	pst progress 40 "Pushing image" --id us-east --title "US East"
//	pst blocked --kind auth "Password required"
//	pst done "Photos synced"
//	pst error "rsync failed"
//	pst idle
//	pst clear [--id x]
//	pst detect          # exit 0 supported, 1 unsupported, 2 unknown
//	pst terminfo        # exit 0 if Pst is advertised, 1 if not
//	pst decode < file   # print each OSC 7501 sequence found in a byte stream
//
// Reports go to /dev/tty when it can be opened, so pst works inside $(...)
// and pipelines, and to stdout otherwise.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/adamvduke/pst"
)

const usage = `usage: pst <command> [flags] [args]

Report status (the message is optional):
  pst idle [msg]
  pst working [msg]
  pst progress <0-100> [msg]
  pst blocked [--kind permission|question|auth] [--progress N] [msg]
  pst done [msg]
  pst error [msg]
  pst clear              remove the record and every record beneath it
                         (without --id, every record on the terminal)

Report flags:
  --id path      record id, such as us-east or build/test (default: root record)
  --app name     machine-readable program name
  --title text   short label for the record
  --bel          terminate with BEL instead of ESC \
  --tmux         wrap for tmux passthrough (needs allow-passthrough on)
  --osc94        also send ConEmu OSC 9;4 progress (root record only)
  --sanitize     clean up msg and title instead of rejecting them

Other commands:
  pst detect     ask the terminal; exit 0 supported, 1 unsupported, 2 unknown
  pst terminfo   exit 0 if $TERM's terminfo advertises Pst, 1 if not
  pst decode     print each OSC 7501 sequence found on stdin, decoded
  pst version    print the implemented spec revision
`

type env struct {
	stdin          io.Reader
	stdout, stderr io.Writer
	// output returns where reports are written and a function to release it.
	output func() (io.Writer, func())
}

func main() {
	os.Exit(run(os.Args[1:], env{stdin: os.Stdin, stdout: os.Stdout, stderr: os.Stderr, output: ttyOrStdout}))
}

// ttyOrStdout opens the controlling terminal, falling back to stdout.
func ttyOrStdout() (io.Writer, func()) {
	name := "/dev/tty"
	if runtime.GOOS == "windows" {
		name = "CONOUT$"
	}
	if f, err := os.OpenFile(name, os.O_WRONLY, 0); err == nil {
		return f, func() { f.Close() }
	}
	return os.Stdout, func() {}
}

func run(args []string, e env) int {
	if len(args) == 0 {
		fmt.Fprint(e.stderr, usage)
		return 2
	}
	cmd, args := args[0], args[1:]
	switch cmd {
	case "idle", "working", "progress", "blocked", "done", "error", "clear":
		return report(cmd, args, e)
	case "detect":
		return detect(args, e)
	case "terminfo":
		return terminfo(args, e)
	case "decode":
		return decode(args, e)
	case "version":
		fmt.Fprintf(e.stdout, "pst (Program Status Protocol %s)\n", pst.SpecRevision)
		return 0
	case "help", "-h", "-help", "--help":
		fmt.Fprint(e.stdout, usage)
		return 0
	}
	fmt.Fprintf(e.stderr, "pst: unknown command %q\n\n%s", cmd, usage)
	return 2
}

// parseInterspersed parses flags that may appear before, between, or after
// positional arguments, which the flag package alone does not allow.
func parseInterspersed(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		if n := len(args) - len(rest); n > 0 && args[n-1] == "--" {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

func report(cmd string, args []string, e env) int {
	fs := flag.NewFlagSet("pst "+cmd, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	fs.Usage = func() { fmt.Fprint(e.stderr, usage) }
	var (
		id, app, title, kind, progress string
		useBEL, tmux, osc94, sanitize  bool
	)
	fs.StringVar(&id, "id", "", "record id")
	fs.StringVar(&app, "app", "", "program name")
	fs.StringVar(&title, "title", "", "record label")
	fs.BoolVar(&useBEL, "bel", false, "terminate with BEL")
	fs.BoolVar(&tmux, "tmux", false, "wrap for tmux passthrough")
	fs.BoolVar(&osc94, "osc94", false, "also send OSC 9;4")
	fs.BoolVar(&sanitize, "sanitize", false, "clean up msg and title")
	if cmd == "blocked" {
		fs.StringVar(&kind, "kind", "", "permission, question, or auth")
		fs.StringVar(&progress, "progress", "", "percentage, 0-100")
	}
	pos, err := parseInterspersed(fs, args)
	if err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	r := pst.Report{ID: id, App: app, Title: title, Kind: pst.Kind(kind)}
	switch cmd {
	case "progress":
		if len(pos) == 0 {
			fmt.Fprintln(e.stderr, "pst: progress needs a percentage")
			return 2
		}
		n, err := strconv.Atoi(pos[0])
		if err != nil {
			fmt.Fprintf(e.stderr, "pst: invalid percentage %q\n", pos[0])
			return 2
		}
		r.State, r.Progress, pos = pst.StateWorking, pst.Percent(n), pos[1:]
	case "blocked":
		r.State = pst.StateBlocked
		if progress != "" {
			n, err := strconv.Atoi(progress)
			if err != nil {
				fmt.Fprintf(e.stderr, "pst: invalid percentage %q\n", progress)
				return 2
			}
			r.Progress = pst.Percent(n)
		}
	default:
		r.State = pst.State(cmd)
	}
	if cmd == "clear" && len(pos) > 0 {
		fmt.Fprintln(e.stderr, "pst: clear takes no message")
		return 2
	}
	r.Msg = strings.Join(pos, " ")
	if sanitize {
		r.Msg = pst.SanitizeText(r.Msg, pst.MaxMsgBytes)
		r.Title = pst.SanitizeText(r.Title, pst.MaxTitleBytes)
	}

	var opts []pst.EncodeOption
	if useBEL {
		opts = append(opts, pst.WithBEL())
	}
	if tmux {
		opts = append(opts, pst.WithTmuxPassthrough())
	}
	seq, err := r.Encode(opts...)
	if err != nil {
		fmt.Fprintln(e.stderr, err)
		return 1
	}
	if osc94 && r.ID == "" {
		mirror, err := pst.OSC94(r, opts...)
		if err != nil {
			fmt.Fprintln(e.stderr, err)
			return 1
		}
		seq = append(seq, mirror...)
	}
	w, release := e.output()
	defer release()
	if _, err := w.Write(seq); err != nil {
		fmt.Fprintf(e.stderr, "pst: %v\n", err)
		return 1
	}
	return 0
}

func detect(args []string, e env) int {
	fs := flag.NewFlagSet("pst detect", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	timeout := fs.Duration("timeout", pst.DefaultDetectTimeout, "how long to wait for the terminal")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	s, err := pst.Detect(ctx, nil)
	if err != nil {
		fmt.Fprintln(e.stderr, err)
	}
	fmt.Fprintln(e.stdout, s)
	switch s {
	case pst.Supported:
		return 0
	case pst.Unsupported:
		return 1
	}
	return 2
}

func terminfo(args []string, e env) int {
	term := ""
	if len(args) > 0 {
		term = args[0]
	}
	ok, err := pst.TerminfoHasPst(term)
	if err != nil {
		fmt.Fprintln(e.stderr, err)
		return 1
	}
	if !ok {
		fmt.Fprintln(e.stdout, "Pst not advertised (the terminal may still support the protocol; try pst detect)")
		return 1
	}
	fmt.Fprintln(e.stdout, "Pst advertised")
	return 0
}

func decode(args []string, e env) int {
	if len(args) > 0 {
		fmt.Fprintln(e.stderr, "pst: decode reads stdin and takes no arguments")
		return 2
	}
	var sc pst.Scanner
	buf := make([]byte, 32*1024)
	for {
		n, err := e.stdin.Read(buf)
		sc.Feed(buf[:n], func(seq []byte) { fmt.Fprintln(e.stdout, describe(seq)) })
		if err == io.EOF {
			return 0
		}
		if err != nil {
			fmt.Fprintf(e.stderr, "pst: %v\n", err)
			return 1
		}
	}
}

// describe renders one sequence for humans.
func describe(seq []byte) string {
	r, err := pst.ParseSequence(seq)
	if errors.Is(err, pst.ErrQuery) {
		return "query"
	}
	if err != nil {
		return err.Error()
	}
	var b strings.Builder
	b.WriteString(string(r.State))
	if r.ID != "" {
		fmt.Fprintf(&b, " id=%s", r.ID)
	}
	if r.Kind != pst.KindNone {
		fmt.Fprintf(&b, " kind=%s", r.Kind)
	}
	if r.State == pst.StateWorking || r.State == pst.StateBlocked {
		fmt.Fprintf(&b, " progress=%s", r.Progress)
	}
	if r.App != "" {
		fmt.Fprintf(&b, " app=%s", r.App)
	}
	if r.Title != "" {
		fmt.Fprintf(&b, " title=%q", r.Title)
	}
	if r.Msg != "" {
		fmt.Fprintf(&b, " msg=%q", r.Msg)
	}
	return b.String()
}
