// Package events is the ingest side of monitor's own SDKs: the
// monitor.event.v1 contract (docs/contracts/event-v1.md), its conversion
// into the same stacktrace.Exception every other source produces, and the
// file inbox SDKs deliver to.
//
// An SDK (sdk/node, sdk/python, sdk/go) never talks to a server. It writes
// one JSON file per event, atomically (a dot-prefixed temp file renamed into
// place), into $MONITOR_EVENTS_DIR when `monitor run --` launched it, or into
// the global inbox ($XDG_STATE_HOME/monitor/events/inbox) otherwise. Monitor
// drains those files: `monitor run --` live, `monitor events drain`,
// `monitor issues`, and `monitor serve` when it is not read-only.
//
// The SDK sends the stack exactly as its runtime formats it (or, for Go,
// structured frames), and parsing, scrubbing, fingerprinting and the
// culprit stay here in Go, so an SDK event and the same crash parsed from
// stderr group into one issue.
package events

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"
)

// Schema is the only $schema value this package accepts.
const Schema = "monitor.event.v1"

// EnvDir names the per-launch events directory `monitor run --` exports
// to its child. SDKs fall back to the global inbox when it is unset or
// unwritable.
const EnvDir = "MONITOR_EVENTS_DIR"

// Event kinds. The first three are crashes the SDK saw on its way out;
// "logged" is an error the app caught and printed or logged; "captured"
// and "message" come from the explicit API.
const (
	KindUncaught  = "uncaught"
	KindRejection = "rejection"
	KindThread    = "thread"
	KindLogged    = "logged"
	KindCaptured  = "captured"
	KindMessage   = "message"
)

// Levels an event may carry. Stack parsing never produces "info"; only an
// explicit captureMessage does.
const (
	LevelFatal   = "fatal"
	LevelError   = "error"
	LevelWarning = "warning"
	LevelInfo    = "info"
)

// Bounds enforced on decode. An event over MaxFileBytes is rejected
// outright; the others trim (stacks, messages) or cut (causes, frames,
// breadcrumbs, tags) so one runaway event can never bloat the store.
const (
	MaxFileBytes      = 512 << 10
	maxStackBytes     = 64 << 10
	maxValueBytes     = 8 << 10
	maxCauseDepth     = 8
	maxFrames         = 256
	maxBreadcrumbs    = 100
	maxTags           = 64
	maxShortFieldSize = 256
)

// Event is one monitor.event.v1 document.
type Event struct {
	Schema    string    `json:"$schema"`
	EventID   string    `json:"event_id"`
	Timestamp time.Time `json:"timestamp"`
	Kind      string    `json:"kind"`
	// Handled is the SDK's exact knowledge of whether the app survived
	// the error; nil derives it from Kind.
	Handled *bool  `json:"handled,omitempty"`
	Level   string `json:"level,omitempty"`
	// Runtime names the producing runtime: node, bun, deno, python, ruby
	// or go.
	Runtime     string            `json:"runtime,omitempty"`
	Error       *Error            `json:"error,omitempty"`
	Message     string            `json:"message,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Breadcrumbs []Breadcrumb      `json:"breadcrumbs,omitempty"`
	Service     string            `json:"service,omitempty"`
	Release     string            `json:"release,omitempty"`
	Environment string            `json:"environment,omitempty"`
	// Cwd is the process's working directory: the project is resolved
	// from it for an event drained from the global inbox.
	Cwd      string `json:"cwd,omitempty"`
	PID      int32  `json:"pid,omitempty"`
	LaunchID string `json:"launch_id,omitempty"`
	SDK      SDK    `json:"sdk"`
}

// Error is one exception, with its cause chain nested outer to inner.
type Error struct {
	Type  string `json:"type,omitempty"`
	Value string `json:"value,omitempty"`
	// Stack is the runtime's own text for THIS error only (V8's err.stack,
	// Python's traceback.format_exception(..., chain=False)); monitor's
	// parsers turn it into frames.
	Stack string `json:"stack,omitempty"`
	// Frames are structured frames, oldest to newest with the crash frame
	// last, for runtimes with no parseable stack text (Go). They win over
	// Stack when both are present.
	Frames []Frame `json:"frames,omitempty"`
	Cause  *Error  `json:"cause,omitempty"`
}

// Frame is one structured stack frame.
type Frame struct {
	Function string `json:"function,omitempty"`
	Module   string `json:"module,omitempty"`
	Filename string `json:"filename,omitempty"`
	Lineno   int    `json:"lineno,omitempty"`
	Colno    int    `json:"colno,omitempty"`
}

// Breadcrumb is one step the SDK recorded before the event.
type Breadcrumb struct {
	Timestamp time.Time `json:"timestamp"`
	Category  string    `json:"category,omitempty"`
	Message   string    `json:"message"`
	Level     string    `json:"level,omitempty"`
}

// SDK identifies the producer: its name ("monitorcli.node"), version, and
// mode ("auto" when `monitor run --probes` loaded it, "explicit" when the
// app imported it).
type SDK struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
	Mode    string `json:"mode,omitempty"`
}

// ErrInvalid marks an event that can never be recorded (wrong schema,
// unknown kind, nothing to record). A drain quarantines such a file
// instead of retrying it.
var ErrInvalid = errors.New("invalid monitor.event.v1 event")

// Decode parses and validates one event document, applying the bounds
// above. Every failure wraps ErrInvalid.
func Decode(data []byte) (Event, error) {
	if len(data) > MaxFileBytes {
		return Event{}, fmt.Errorf("%w: %d bytes exceeds %d", ErrInvalid, len(data), MaxFileBytes)
	}
	var ev Event
	if err := json.Unmarshal(data, &ev); err != nil {
		return Event{}, fmt.Errorf("%w: %v", ErrInvalid, err)
	}
	if err := ev.normalize(); err != nil {
		return Event{}, err
	}
	return ev, nil
}

// normalize validates ev and applies every bound in place.
func (ev *Event) normalize() error {
	if ev.Schema != Schema {
		return fmt.Errorf("%w: $schema %q, want %q", ErrInvalid, ev.Schema, Schema)
	}
	ev.Kind = strings.ToLower(strings.TrimSpace(ev.Kind))
	switch ev.Kind {
	case KindUncaught, KindRejection, KindThread, KindLogged, KindCaptured, KindMessage:
	default:
		return fmt.Errorf("%w: unknown kind %q", ErrInvalid, ev.Kind)
	}
	ev.EventID = cleanID(ev.EventID)
	ev.Level = strings.ToLower(strings.TrimSpace(ev.Level))
	switch ev.Level {
	case "", LevelFatal, LevelError, LevelWarning, LevelInfo:
	case "warn":
		ev.Level = LevelWarning
	case "critical":
		ev.Level = LevelFatal
	default:
		ev.Level = ""
	}
	ev.Runtime = short(strings.ToLower(ev.Runtime))
	ev.Message = clip(strings.TrimSpace(ev.Message), maxValueBytes)
	ev.Service = short(ev.Service)
	ev.Release = short(ev.Release)
	ev.Environment = short(ev.Environment)
	ev.LaunchID = short(ev.LaunchID)
	ev.Cwd = clip(strings.TrimSpace(ev.Cwd), 4096)
	ev.SDK.Name = short(ev.SDK.Name)
	ev.SDK.Version = short(ev.SDK.Version)
	ev.SDK.Mode = short(ev.SDK.Mode)
	if ev.Error != nil {
		ev.Error = normalizeError(ev.Error, 0)
	}
	if ev.Error == nil && ev.Message == "" {
		return fmt.Errorf("%w: neither error nor message", ErrInvalid)
	}
	if len(ev.Breadcrumbs) > maxBreadcrumbs {
		ev.Breadcrumbs = ev.Breadcrumbs[len(ev.Breadcrumbs)-maxBreadcrumbs:]
	}
	if len(ev.Tags) > maxTags {
		ev.Tags = nil // a runaway tag map carries no signal worth guessing at
	}
	return nil
}

// normalizeError trims one error and its causes, cutting the chain at
// maxCauseDepth. It returns nil for an error with nothing in it.
func normalizeError(e *Error, depth int) *Error {
	if e == nil || depth >= maxCauseDepth {
		return nil
	}
	e.Type = short(e.Type)
	e.Value = clip(strings.TrimSpace(e.Value), maxValueBytes)
	e.Stack = clip(e.Stack, maxStackBytes)
	if len(e.Frames) > maxFrames {
		e.Frames = e.Frames[len(e.Frames)-maxFrames:]
	}
	e.Cause = normalizeError(e.Cause, depth+1)
	if e.Type == "" && e.Value == "" && strings.TrimSpace(e.Stack) == "" && len(e.Frames) == 0 {
		return e.Cause
	}
	return e
}

// cleanID keeps an SDK-supplied event id only when it is a plain token
// (letters, digits, '-', '_', at most 64 bytes); anything else is dropped
// and the caller falls back to the file name.
func cleanID(id string) string {
	id = strings.TrimSpace(id)
	if id == "" || len(id) > 64 {
		return ""
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return ""
		}
	}
	return id
}

func short(s string) string { return clip(strings.TrimSpace(s), maxShortFieldSize) }

// clip cuts s to at most n bytes without splitting a UTF-8 sequence.
func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	// Drop at most one cut multi-byte sequence from the end.
	for i := 0; i < utf8.UTFMax-1 && len(s) > 0; i++ {
		if r, size := utf8.DecodeLastRuneInString(s); r != utf8.RuneError || size != 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}
