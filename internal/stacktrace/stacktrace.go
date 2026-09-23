// Package stacktrace is the zero-privilege stack-trace detector: it turns raw
// stderr/stdout/log text from a development process into structured
// exceptions, without any SDK, preload hook, or attach step.
//
// Pipeline: a per-stream Joiner groups raw lines into candidate blocks
// (joiner.go), and Parse (detect.go) auto-selects one of the per-runtime
// parsers (js.go, tslog.go, python.go, ruby.go, gopanic.go, zap.go) to turn a
// block into an Exception, or nil when nothing recognizable is there.
//
// Frame ordering. Every parser normalizes Frames to oldest -> newest, with
// the frame closest to the fault (the "crash frame") last -- the same
// convention Sentry uses. Node/Deno/Bun, Ruby, Go panics, and pkg/errors
// traces all print the crash frame FIRST (innermost-first) and are reversed
// by their parser; Python's traceback module already prints oldest-first
// ("Traceback (most recent call last)") and needs no reversal.
//
// Chained ordering. Exception.Chained always runs from the OUTER exception's
// immediate cause down to the innermost/root cause, regardless of how the
// runtime printed it. Python prints the cause FIRST (with a "direct cause"
// or "During handling" connector) and the parser reverses the physical
// order; Node's util.inspect prints "[cause]: ..." nested AFTER the outer
// stack, which is already outer-to-inner and needs no reversal. The
// Exception returned by Parse always describes the outermost/most-recently
// raised exception; Chained holds what is beneath it.
//
// Filename vs AbsPath. Parse never consults the filesystem or git, so
// Frame.Filename is always the path exactly as the runtime printed it (the
// "else as printed" fallback of the Frame doc below) -- a caller that knows
// the git root can compute a relative display form itself. AbsPath holds the
// same value when the runtime printed an absolute (or absolute-looking,
// e.g. "file://") path, and is empty when the runtime only printed a bare
// module/script name (Ruby's "workload.rb", Node's "node:internal/...").
package stacktrace

import "time"

// Frame is one entry in an Exception's call stack.
type Frame struct {
	// Function is the function/method name as printed (may include
	// qualifiers like "new ", "async ", or "Object.<anonymous>").
	Function string `json:"function,omitempty"`
	// Module is a runtime-specific grouping hint (e.g. a Go package path
	// or a Ruby/Python module); left empty when the parser has no clean
	// signal for it.
	Module string `json:"module,omitempty"`
	// Filename is the path exactly as the runtime printed it (relative
	// when the runtime printed it relative, absolute otherwise). See the
	// package doc for why this package never relativizes it to a git
	// root itself.
	Filename string `json:"filename,omitempty"`
	// AbsPath is the same path when it was printed as (or derived to)
	// an absolute filesystem path; empty for pseudo-paths such as
	// "node:internal/timers" or "<anonymous>".
	AbsPath string `json:"abs_path,omitempty"`
	Lineno  int    `json:"lineno,omitempty"`
	Colno   int    `json:"colno,omitempty"`
	// InApp is set by a caller via InApp(frame, gitRoot); Parse always
	// leaves it false since it has no git root to test against.
	InApp bool `json:"in_app"`
	// Mapping describes how a frame's original coordinates relate to
	// what's printed here: "exact", "ambiguous", "transpiled", "inferred",
	// or "" when no source-map resolution has been attempted (the common
	// case for this package, which only parses printed text).
	Mapping string `json:"mapping,omitempty"`
}

// Exception is one detected error/crash event, possibly with a chain of
// causes. See the package doc for the Frames/Chained ordering contract.
type Exception struct {
	// Runtime is the producing runtime: "node", "deno", "bun", "python",
	// "ruby", "go", or "" when Parser == "message" and no runtime signal
	// was available.
	Runtime string `json:"runtime,omitempty"`
	// Type is the exception's class/constructor name (e.g. "TypeError",
	// "ValueError", "RuntimeError", "panic"); empty when the format
	// carries no type (e.g. a Ruby printed backtrace with no header).
	Type string `json:"type,omitempty"`
	// Value is the exception's message.
	Value string `json:"value,omitempty"`
	// Parser names which parser recognized the block: "js", "tslog",
	// "python", "ruby", "gopanic", "zap", or "message" for a
	// message-only event (a logger error line with no stack).
	Parser string `json:"parser"`
	// Handled is nil when it can't be determined from the text alone,
	// true when the format structurally implies the error was caught
	// (e.g. a Ruby rescue-printed backtrace, a Python traceback that
	// never reached module scope), and false for a process-ending crash
	// (an uncaught JS exception, a Go panic, a Ruby fatal exit).
	Handled *bool `json:"handled,omitempty"`
	// Frames runs oldest -> newest; the crash frame is last. Empty for a
	// message-only event.
	Frames []Frame `json:"frames,omitempty"`
	// Chained runs from the outer exception's immediate cause down to
	// the innermost/root cause. Empty when there is no chain.
	Chained []Exception `json:"chained,omitempty"`
	// ObservedAt is the line's own timestamp when one could be parsed
	// (see timestamps.go); the zero value otherwise, leaving the
	// mtime-vs-now fallback decision to the caller.
	ObservedAt time.Time `json:"observed_at,omitempty"`
	// LineStart and LineEnd are 1-based, inclusive line numbers within
	// the fed stream that produced this block.
	LineStart int `json:"line_start,omitempty"`
	LineEnd   int `json:"line_end,omitempty"`
	// Level is "fatal" (uncaught/panic/process-ending), "error" (a
	// printed, handled stack), or "warning".
	Level string `json:"level"`
}

// boolPtr is a small helper so parsers can write boolPtr(true) instead of
// declaring a local variable every time they need to set Handled.
func boolPtr(v bool) *bool { return &v }
