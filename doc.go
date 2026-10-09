// Package pst implements the Program Status Protocol (OSC 7501), a terminal
// escape sequence that lets a program tell the terminal what it is doing:
// idle, working, waiting on the user, finished, or failed, and why.
//
// Spec: https://www.superlogical.com/rex/docs/build/program-status
//
// The package covers both directions of the protocol and depends only on the
// Go standard library. It never decides how a status is presented; there are
// no notifications, sounds, or spinners here.
//
// # Program side
//
// Most programs only need a [Status] handle:
//
//	st := pst.NewForFile(os.Stdout, pst.WithApp("sync"))
//	st.Working("Syncing photos")
//	// ...
//	st.Done("Photos synced")
//
// Lower-level building blocks are [Report], [Report.Validate],
// [Report.Encode], [Query], [Detect], and [TerminfoHasPst].
//
// Every report replaces its record on the terminal completely: keys missing
// from a report are gone afterwards. [Status] repeats its app and title on
// every report for that reason.
//
// # Terminal side
//
// Emulators and multiplexers parse reports with [ParseBody] (from their own
// OSC dispatch) or [Scanner] + [ParseSequence] (from a raw pty stream), keep
// records in a [Store], and may map OSC 9;4 with [OSC94Mapper]. The only
// bytes a terminal ever writes back are [Query]'s, in reply to a query.
//
// # Interpretation notes
//
// Where the spec leaves room, this package chooses as follows:
//
//   - "Whitespace" around keys and values means ASCII space and tab.
//   - A key containing anything other than a-z makes its pair malformed.
//   - Pairs are checked for well-formedness first. A malformed pair is skipped
//     entirely and is never subject to limits.
//   - Limits are checked on every well-formed occurrence of a key, even one a
//     later repeat overrides.
//   - progress accepts canonical decimal only: 0 to 100 without sign or
//     leading zeros. Anything else is treated as absent.
//   - Decoded text must also be valid UTF-8; invalid UTF-8 discards the report
//     like a control character does.
//   - base64 padding is optional, but if present it must be correct.
//   - An id that breaks a limit (128 bytes, 32 per segment, 8 segments)
//     discards the report ([ErrDiscarded]); an id that is otherwise malformed
//     makes it ignored ([ErrIgnored]). Either way nothing is applied.
package pst

// SpecRevision is the revision of the Program Status Protocol this package
// implements.
const SpecRevision = "0.3"
