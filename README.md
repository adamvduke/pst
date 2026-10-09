# pst

> [!NOTE]
> **The initial version of this module was generated entirely by an AI
> agent.** Claude Code (Claude Opus 5.5) wrote all of its code, tests,
> fixtures, and documentation from a design handoff document created by
> directly reading the
> [spec](https://www.superlogical.com/rex/docs/build/program-status). A person
> directed the work and made the design decisions, but wrote none of the code.
> Later revisions may include human-authored changes.
>
> It began as an experiment. The spec is short, simple, and complete, and I
> was mostly curious how complete an implementation would turn out when built
> from it this way. Treat it accordingly, and read
> [Known gaps](#known-gaps) before depending on it.

`pst` is a dependency-free Go implementation of the **Program Status Protocol
(OSC 7501)**: a terminal escape sequence that lets a program tell the terminal
whether it is idle, working, blocked on the user, done, or failed, and why.

It covers the whole protocol in both directions, using only the Go standard
library:

- **program side:** encode, validate, emit, feature detection, terminfo `Pst` lookup
- **terminal side:** parse, a record store with the spec's lifetime rules,
  feature-detection replies, and OSC 9;4 mapping

It never decides how status is shown. There are no notifications, sounds, or
spinners here.

- Spec: <https://www.superlogical.com/rex/docs/build/program-status>
- Announcement and rationale: <https://mitchellh.com/writing/program-status-osc7501>
- Implements spec revision **0.3** (2026-10-07). `pst.SpecRevision` holds the
  same value.

```
go get github.com/adamvduke/pst
```

## Who it's for

Charm already has OSC 7501 helpers inside its ANSI package, and Bubble Tea
programs use those. `pst` is for everyone else:

- plain CLIs that don't want a TUI framework;
- Go terminal emulators and multiplexers that need a correct record store;
- anyone who wants the complete protocol in one small, dependency-free package.

## Quick start

```go
st := pst.NewForFile(os.Stdout, pst.WithApp("sync")) // no-op unless stdout is a terminal

st.Working("Syncing photos")
for i, f := range files {
	st.Progressf(i*100/len(files), "Copying %s", f)
	// ...
}
if err != nil {
	st.Errorf("Sync failed: %v", err)
} else {
	st.Done("Photos synced")
}
```

Other states are reported with `Idle`, `Blocked(kind, msg)`, `BlockedProgress`,
and `Clear`. Each method validates its input and returns an error rather than
letting the terminal silently drop a report. Use `WithSanitize()` to clean up
arbitrary text instead. A handle and its children are safe for concurrent use,
and byte-identical repeats are skipped.

The spec asks programs to follow a pattern, shown in full in `Example_lifecycle`:

- report `idle` when the user interrupts (for example, with
  `signal.NotifyContext`);
- report `done` or `error` right before exiting, so the user finds a record
  afterwards.

### Reports replace records

**Every report replaces its record completely.** Keys missing from a report
are gone afterwards. A `Status` handle therefore repeats its `app` and `title`
on every report. If you build `Report` values yourself, include them every
time.

### Several records

```go
root := pst.New(w, pst.WithApp("deploy"))
east, _ := root.Child("us-east", pst.WithTitle("US East"))
east.Progress(40, "Pushing image")  // id=us-east; app=deploy is inherited on the terminal side
east.Clear()                        // removes us-east and everything beneath it
root.Clear()                        // removes every record on the terminal
```

### Detection and terminfo

Detection is optional. Terminals ignore OSC sequences they don't know, so
sending reports without detecting first is safe.

```go
support, err := pst.Detect(ctx, nil) // Supported, Unsupported, or Unknown
```

`Detect` opens `/dev/tty` (or `CONIN$`/`CONOUT$` on Windows), puts it in raw
mode, and sends the query followed by DA1. Whichever reply arrives first
decides the result. After a `Supported` result it keeps reading until the DA1
reply has also arrived, so that reply never leaks into your shell. If the
context has no deadline, the default timeout is 200 ms.

`pst.TerminfoHasPst("")` reports whether `$TERM`'s compiled terminfo entry
advertises the `Pst` capability. If it does, you may skip detection. If it
doesn't, that does **not** mean the terminal lacks support. The reply to
`Detect` is always authoritative.

### tmux

tmux drops unknown OSC sequences. `WithTmuxPassthrough()` wraps each sequence
in tmux's DCS passthrough, which works only with `set -g allow-passthrough on`.
This is not part of the spec, and `pst` never enables it automatically. Use
`pst.InTmux()` to decide whether to enable it.

### OSC 9;4

`WithOSC94()` also mirrors the root record as ConEmu OSC 9;4 progress, for
terminals that support 9;4 but not 7501.

## Terminal side

Emulators that already have a VT parser call `pst.ParseBody` from their OSC
dispatch. Otherwise, `Scanner` pulls OSC 7501 sequences out of a raw pty
stream. It only observes and never consumes the bytes.

```go
store := pst.NewStore() // cap 256 by default; WithCap(n) accepts 64 to 256
var sc pst.Scanner

sc.Feed(ptyOutput, func(seq []byte) {
	r, err := pst.ParseSequence(seq)
	switch {
	case errors.Is(err, pst.ErrQuery):
		pty.Write(pst.Query()) // the only bytes a terminal ever writes back
	case err == nil:
		for _, c := range store.Apply(r) {
			// c.Kind is Added, Replaced, or Removed; update your UI.
		}
	}
})

// Lifetime events the terminal already sees:
store.PromptStarted() // OSC 133 A: drops working, blocked (and idle) records
store.ProcessExited() // same rule
store.Reset()         // RIS: drops everything. DECSTR and screen switches do nothing.
```

- `store.EffectiveApp(id)` resolves `app` inheritance through missing parents.
- `pst.DisarmText` strips bidi overrides and invisible characters before
  showing text outside the grid.
- `OSC94Mapper` maps OSC 9;4 to the root record, and stops after the first
  7501 report, as the spec recommends.

Parsing is lenient where the spec is lenient:

- malformed pairs are skipped, unknown keys ignored, and the last repeated key
  wins;
- an invalid `kind`, `progress`, or `app` is treated as absent.

It is strict where the spec is strict:

- a report that breaks a limit, has bad base64, or has control characters in
  decoded text is discarded (`ErrDiscarded`);
- a missing or unknown state, or an invalid id, makes the report ignored
  (`ErrIgnored`);
- a 7501 sequence whose body is `?` is the feature-detection query
  (`ErrQuery`), and the terminal answers it with `Query()`.

Any report returned without an error passes `Validate`, and every valid report
round-trips through `Encode` and `ParseSequence`.

## CLI

`cmd/pst` replaces the shell function in the spec:

```
go install github.com/adamvduke/pst/cmd/pst@latest

pst working "Syncing photos"
pst progress 40 "Pushing image" --id us-east --title "US East"
pst blocked --kind auth "Password required"
pst done "Photos synced"
pst error "rsync failed"
pst idle
pst clear [--id x]
pst detect          # exit 0 supported, 1 unsupported, 2 unknown
pst terminfo        # exit 0 if Pst is advertised, 1 if not
pst decode < file   # print each OSC 7501 report in a byte stream, decoded
```

Report commands take these flags: `--app`, `--id`, `--title`, `--bel`,
`--tmux`, `--osc94`, and `--sanitize`.

- Output goes to `/dev/tty` when it can be opened, so `pst` works inside
  `$(…)` and pipelines. Otherwise it goes to stdout.
- Invalid input exits 1 with a message.

```sh
rsync -a ~/Photos backup:/photos && pst done "Photos synced" || pst error "rsync failed"
```

## Interpretation notes

Where the spec leaves room, `pst` chooses as follows. These choices are also
documented in the package docs.

- "Whitespace" around keys and values means ASCII space and tab.
- A key with characters outside `a-z` makes its pair malformed, so the pair is
  skipped.
- Pairs are checked for well-formedness before limits, so a malformed pair is
  skipped no matter how long it is.
- Limits apply to every well-formed occurrence of a key, including one that a
  later repeat overrides.
- `progress` accepts canonical decimal only (`0`–`100`, with no sign or
  leading zeros). `040` and `4.5` are treated as absent.
- Decoded text must be valid UTF-8 as well as free of control characters.
- base64 padding is optional, but must be correct when present.
- An id that breaks a limit (128 bytes, 32 per segment, 8 segments) gives
  `ErrDiscarded`. An id that is otherwise malformed gives `ErrIgnored`. Either
  way, nothing is applied.
- `ParseBody` can't see the terminator, so it assumes BEL, the shorter one, and
  accepts bodies up to 4088 bytes. `ParseSequence` enforces the 4096-byte limit
  exactly.
- The encoder writes keys in a fixed order: `state`, `id`, `kind`, `progress`,
  `app`, `title`, `msg`. Two of the spec's multi-record examples list keys in
  a different order, so `Encode` output matches them in content but not byte
  for byte.
- `SanitizeText` and `DisarmText` remove U+061C, U+200B–U+200F, U+202A–U+202E,
  U+2060–U+2064, U+2066–U+2069, and U+FEFF.
- For OSC 9;4, state 0 maps to `clear`, not `idle`. State 0 means "nothing to
  show", and until the first 7501 report the mapped root record is the only
  record that can exist.

## Compared to charmbracelet/x/ansi

These notes are as of 2026-10-09, comparing against charmbracelet/x #1004 (merged)
and #1005 (open). Charm's helpers produce strings for Bubble Tea and are a
good fit there. `pst` differs in a few ways:

- **Validation:** `pst` returns typed, `errors.Is`-able errors for every
  problem, where Charm silently clamps or drops. For example, Charm's
  `Percent(140)` becomes 100, while `pst` reports `ErrProgress`.
- **Progress parsing:** `pst` accepts canonical decimal progress only.
- **Terminator:** `pst` defaults to the ST terminator (`ESC \`), as the spec's
  examples do. `WithBEL()` switches to BEL.
- **Scope:** `pst` adds the terminal side (scanner, store, lifetime, OSC 9;4
  mapper), feature detection, terminfo lookup, and a CLI.
- **Shared conventions:** `pst` mirrors Charm's choices where they're
  arbitrary. `Percent(n)` and a zero-value `Progress` meaning indeterminate
  work the same way, so moving between the two is easy.

## Known gaps

- **No supporting terminal to test against yet.** As of October 2026, no
  generally available terminal answers the feature detection query. Rex is
  in a closed macOS beta. Ghostty's library implements the protocol, but the
  Ghostty app doesn't opt in. `Detect` has been verified against Ghostty 1.3.1,
  which correctly gives `Unsupported`, and against the `internal/manual/host`
  harness, which gives `Supported`. It has never been verified against a real
  supporting terminal.
- **Windows `Detect` has never been run.** It compiles and is best effort. It
  needs a console that understands VT input, such as Windows Terminal.
- **OpenBSD is untested.** The termios code calls `syscall.Syscall`, which may
  not work on OpenBSD 7.5 and later. If it doesn't, `IsTerminal` returns false
  and `Detect` fails there.
- **`Detect` changes the file you pass it.** On Unix, a `*os.File` you pass is
  left in blocking mode, a side effect of `os.File.Fd`. Pass `nil` to let
  `Detect` open and close its own `/dev/tty`.
- **The `Scanner` handles 7-bit sequences only.** It recognizes OSC as
  `ESC ]` and ST as `ESC \`, which is how the spec defines them. 8-bit C1
  forms (0x9D, 0x9C) are ignored.
- **The spec is a draft.** This implements revision 0.3 (2026-10-07), and the
  API may change with the spec until v1.

## Development

```
go test -race ./...
go test -run '^$' -fuzz FuzzParseBody -fuzztime 30s .   # also FuzzRoundTrip, FuzzScanner, FuzzTerminfo, FuzzClassifier, …
go run -tags manual ./internal/manual/detect             # try detection against a real terminal
go run -tags manual ./internal/manual/host               # be an OSC 7501 terminal (see below)
```

Few terminals support the protocol yet. To try it anyway, `internal/manual/host`
(macOS and Linux) runs a command, by default your shell, in a pseudo-terminal
inside your current terminal. It passes all output through unchanged, and acts
as a supporting terminal along the way:

- it answers the detection query, so emitters that detect first (such as
  Claude Code) start reporting;
- it keeps a `Store`, applies OSC 133 A, RIS, and process-exit lifetime rules;
- it logs every record change.

Watch the log in another split with `tail -f "${TMPDIR:-/tmp}/pst-host.log"`.
`$PST_HOST=1` is set inside.

`go.mod` has no `require` lines, and CI fails if it ever gains one.
`testdata/README` explains how the compiled terminfo fixtures were generated.

## License

MIT
