// Package stacktrace is the zero-privilege stack-trace detector: it turns raw
// stderr/stdout/log text from a development process into structured
// exceptions, without any SDK, preload hook, or attach step.
//
// Pipeline: a per-stream Joiner groups raw lines into candidate blocks
// (joiner.go), and Parse (detect.go) turns a block into an Exception using
// the parser of the grammar that opened it (js.go, tslog.go, python.go,
// ruby.go, gopanic.go, zap.go), or returns nil when the block holds nothing
// recognizable (a lone "Error: x" line with no stack, a table row that only
// looked like a Bun code frame, ...). ApplyGitRoot (inapp.go) then resolves
// InApp and the git-root-relative Filename once the caller knows the root.
//
// Frame ordering. Every parser normalizes Frames to oldest -> newest, with
// the frame closest to the fault (the "crash frame") last -- the same
// convention Sentry uses. Node/Deno/Bun, Ruby, Go panics, pkg/errors traces
// and the typescript-logging format all print the crash frame FIRST
// (innermost-first) and are reversed by their parser; Python's traceback
// module already prints oldest-first ("Traceback (most recent call last)")
// and needs no reversal.
//
// Chained ordering. Exception.Chained always runs from the OUTER exception's
// immediate cause down to the innermost/root cause, regardless of how the
// runtime printed it:
//   - Python prints the root cause FIRST (joined by "The above exception was
//     the direct cause ..." / "During handling ..."), so the parser reverses
//     the printed segment order;
//   - Node/Deno util.inspect nests "[cause]: ..." inside the outer error at
//     any depth, Deno's uncaught printer appends "Caused by: ..." sections,
//     Bun prints one code-frame block per cause, Ruby prints one header per
//     cause, and tslog appends "Caused by:" -- all already outer-to-inner;
//   - pkg/errors "%+v" prints the ROOT error's stack first and each wrapping
//     layer after it, so zap.go reverses those segments.
//
// The Exception returned by Parse always describes the outermost /
// most-recently-raised exception; Chained holds what is beneath it.
//
// Filename vs AbsPath. Parse never consults the filesystem or git: it sets
// Frame.Filename to the path as printed (a file:// URL is reduced to its
// path) and AbsPath to the same value when it is absolute. ApplyGitRoot then
// rewrites Filename to the git-root-relative, slash-separated form for every
// frame under the root, keeping the absolute path in AbsPath -- the roadmap
// contract ("Filename relative to the git root when known").
package stacktrace

import "time"

// Frame is one entry in an Exception's call stack.
type Frame struct {
	// Function is the function/method name as printed (may include
	// qualifiers like "new ", "async ", "Object.<anonymous>", or a Go
	// receiver such as "pkg.(*Server).Handle").
	Function string `json:"function,omitempty"`
	// Module is a runtime-specific grouping hint: the Go package path
	// ("example.com/app/internal/svc"); left empty when the parser has no
	// clean signal for it.
	Module string `json:"module,omitempty"`
	// Filename is relative to the git root (slash-separated) once
	// ApplyGitRoot has run and the frame lives under the root; otherwise
	// it is the path as printed.
	Filename string `json:"filename,omitempty"`
	// AbsPath is the absolute filesystem path when the runtime printed an
	// absolute path (or a file:// URL); empty for relative paths and for
	// pseudo-paths such as "node:internal/timers" or "<frozen runpy>".
	AbsPath string `json:"abs_path,omitempty"`
	Lineno  int    `json:"lineno,omitempty"`
	Colno   int    `json:"colno,omitempty"`
	// InApp is set by ApplyGitRoot (or a caller using InApp directly);
	// Parse always leaves it false since it has no git root to test
	// against.
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
	// Runtime is the producing runtime when the text identifies it:
	// "node", "deno", "bun", "python", "ruby", "go"; "" when it cannot be
	// told from the text alone (e.g. a plain V8 "err.stack" print with no
	// node:/ext: frames and no runtime footer).
	Runtime string `json:"runtime,omitempty"`
	// Type is the exception's class/constructor name (e.g. "TypeError",
	// "ValueError", "RuntimeError", "panic"); empty when the format
	// carries no type (a Ruby rescue-printed backtrace, a pkg/errors
	// layer, a thrown JS primitive).
	Type string `json:"type,omitempty"`
	// Value is the exception's message (its first line for multi-line
	// messages).
	Value string `json:"value,omitempty"`
	// Parser names which parser recognized the block: "js", "tslog",
	// "python", "ruby", "gopanic", "zap", or "message" for a message-only
	// event (a logger error line with no stack).
	Parser string `json:"parser"`
	// Handled is true when the text shows the error was caught and
	// printed (console.error(err), logging.exception, a rescue-printed
	// backtrace, a zap error line), false for an uncaught, process- or
	// thread-ending crash, and nil when the text gives no signal.
	Handled *bool `json:"handled,omitempty"`
	// Frames runs oldest -> newest; the crash frame is last. Empty for a
	// message-only event.
	Frames []Frame `json:"frames,omitempty"`
	// Chained runs from the outer exception's immediate cause down to
	// the innermost/root cause. Empty when there is no chain.
	Chained []Exception `json:"chained,omitempty"`
	// ObservedAt is the event's own timestamp when one could be read from
	// the block's first line or from the logger line immediately before
	// it (see timestamps.go); the zero value otherwise -- omitted from
	// JSON -- leaving the mtime-vs-now fallback decision to the caller.
	ObservedAt time.Time `json:"observed_at,omitzero"`
	// LineStart and LineEnd are 1-based, inclusive line numbers within
	// the fed stream that produced this block.
	LineStart int `json:"line_start,omitempty"`
	LineEnd   int `json:"line_end,omitempty"`
	// Level is "fatal" (uncaught / panic / process-ending), "error" (a
	// printed, handled stack or an error log line), or "warning".
	Level string `json:"level"`
}

// boolPtr is a small helper so parsers can write boolPtr(true) instead of
// declaring a local variable every time they need to set Handled.
func boolPtr(v bool) *bool { return &v }

// levelFor maps an uncaught/handled decision to the matching Level.
func levelFor(fatal bool) string {
	if fatal {
		return LevelFatal
	}
	return LevelError
}

// inheritChain stamps the outer exception's Runtime, Parser, Handled and
// Level onto each chained cause: a cause is part of the same event, so it
// shares the event's disposition.
func inheritChain(ex *Exception) {
	for i := range ex.Chained {
		c := &ex.Chained[i]
		if c.Runtime == "" {
			c.Runtime = ex.Runtime
		}
		if c.Parser == "" {
			c.Parser = ex.Parser
		}
		if ex.Handled != nil {
			c.Handled = boolPtr(*ex.Handled)
		}
		c.Level = ex.Level
	}
}
