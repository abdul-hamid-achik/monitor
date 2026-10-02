package events

import (
	"strings"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// ParserSDK names an exception whose frames came from the event itself
// (structured frames, or no frames at all) rather than from one of
// stacktrace's text parsers.
const ParserSDK = "sdk"

// ToException converts ev into the stacktrace.Exception the rest of the
// pipeline (git root, scrub, FingerprintV2, culprit) consumes, exactly as
// if the same error had been parsed from the process's output:
//
//   - Error.Frames, when present, become the frames as given;
//   - otherwise Error.Stack goes through stacktrace.Detect, and the parsed
//     type and message win over the SDK's (they come from the same text
//     stderr would show, so both sources fingerprint alike);
//   - Error.Cause becomes Chained, outer to innermost; with no structured
//     cause, a chain the stack text itself carried is kept;
//   - a message event becomes a frameless exception with only a Value,
//     grouped by its normalized message.
//
// Handled and Level come from the SDK (it knows exactly), falling back to
// what Kind implies. ok is false when ev holds nothing to record.
func ToException(ev Event) (stacktrace.Exception, bool) {
	var ex stacktrace.Exception
	switch {
	case ev.Error != nil:
		ex = convertError(*ev.Error, true)
	case strings.TrimSpace(ev.Message) != "":
		ex = stacktrace.Exception{Parser: ParserSDK, Value: firstLine(ev.Message)}
	default:
		return stacktrace.Exception{}, false
	}
	if ex.Type == "" && ex.Value == "" && len(ex.Frames) == 0 {
		return stacktrace.Exception{}, false
	}
	if ev.Runtime != "" {
		ex.Runtime = ev.Runtime
	}
	handled := handledFor(ev)
	ex.Handled = &handled
	ex.Level = levelFor(ev, handled)
	if !ev.Timestamp.IsZero() {
		ex.ObservedAt = ev.Timestamp.UTC()
	}
	ex.LineStart, ex.LineEnd = 0, 0
	for i := range ex.Chained {
		c := &ex.Chained[i]
		c.Runtime = ex.Runtime
		if c.Parser == "" {
			c.Parser = ex.Parser
		}
		c.Handled = &handled
		c.Level = ex.Level
		c.LineStart, c.LineEnd = 0, 0
	}
	return ex, true
}

// convertError turns one Error and its causes into an Exception whose
// Chained is flat, outer cause first. textChain lets a chain parsed out of
// the stack text stand in for a missing structured cause; it is off for
// the causes themselves, so a chain is never counted twice.
func convertError(e Error, textChain bool) stacktrace.Exception {
	ex := stacktrace.Exception{Parser: ParserSDK, Type: e.Type, Value: firstLine(e.Value)}
	switch {
	case len(e.Frames) > 0:
		ex.Frames = make([]stacktrace.Frame, 0, len(e.Frames))
		for _, f := range e.Frames {
			ex.Frames = append(ex.Frames, structuredFrame(f))
		}
	case strings.TrimSpace(e.Stack) != "":
		if parsed := parseStack(e.Stack); parsed != nil {
			ex.Parser = parsed.Parser
			ex.Runtime = parsed.Runtime
			ex.Frames = parsed.Frames
			if parsed.Type != "" {
				ex.Type = parsed.Type
			}
			if parsed.Value != "" {
				ex.Value = parsed.Value
			}
			if textChain && e.Cause == nil {
				ex.Chained = parsed.Chained
			}
		}
	}
	for c := e.Cause; c != nil; c = c.Cause {
		next := *c
		next.Cause = nil
		ex.Chained = append(ex.Chained, convertError(next, false))
	}
	return ex
}

// parseStack runs the runtime's stack text through stacktrace.Detect and
// returns the first exception that carries frames (an SDK sends one
// error's text, so there is normally exactly one).
func parseStack(stack string) *stacktrace.Exception {
	for _, ex := range stacktrace.Detect(stack) {
		if len(ex.Frames) > 0 {
			return ex
		}
	}
	return nil
}

// structuredFrame maps an SDK frame onto a stacktrace.Frame the same way a
// parser fills one: AbsPath only for an absolute path, InApp left to
// ApplyGitRoot.
func structuredFrame(f Frame) stacktrace.Frame {
	out := stacktrace.Frame{
		Function: short(f.Function),
		Module:   short(f.Module),
		Filename: clip(strings.TrimSpace(f.Filename), 1024),
		Lineno:   f.Lineno,
		Colno:    f.Colno,
	}
	if strings.HasPrefix(out.Filename, "/") {
		out.AbsPath = out.Filename
	}
	return out
}

// handledFor is the SDK's own handled flag, or what Kind implies: a crash
// the SDK saw on its way out is unhandled, everything else was caught.
func handledFor(ev Event) bool {
	if ev.Handled != nil {
		return *ev.Handled
	}
	switch ev.Kind {
	case KindUncaught, KindRejection, KindThread:
		return false
	}
	return true
}

// levelFor is the SDK's level, or fatal for an unhandled crash, info for
// a message and error for everything else.
func levelFor(ev Event, handled bool) string {
	if ev.Level != "" {
		return ev.Level
	}
	switch {
	case !handled && ev.Kind == KindUncaught:
		return LevelFatal
	case ev.Kind == KindMessage:
		return LevelInfo
	}
	return LevelError
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}

// Prepared is everything issues.RecordException needs for one event,
// already scrubbed.
type Prepared struct {
	Exception stacktrace.Exception
	// Hints resolve the project for an event drained from the global
	// inbox (from the event's cwd and service). A live `monitor run --`
	// uses its launch identity instead.
	Hints project.Hints
	// Run carries the event's release and environment.
	Run     contextids.IDs
	Options issues.RecordExceptionOptions
}

// Prepare converts ev and scrubs every free-text field that will be
// persisted: the exception's type, message and function names (as the
// stderr detector does), breadcrumb messages and tag values. The event id
// becomes the DedupeKey, so draining the same file twice (two drainers, or
// a crash between recording and removing it) folds into one occurrence.
func Prepare(ev Event, scrubber *scrub.Scrubber) (Prepared, bool) {
	ex, ok := ToException(ev)
	if !ok {
		return Prepared{}, false
	}
	ScrubException(scrubber, &ex)

	pid := ev.PID
	if pid <= 0 {
		// An SDK event always comes from a process; PID <= 0 would make
		// project.Resolve treat it as a host-wide alert.
		pid = 1
	}
	p := Prepared{
		Exception: ex,
		Hints:     project.Hints{Dir: ev.Cwd, ExplicitService: ev.Service, PID: pid},
		Run:       contextids.IDs{Release: ev.Release, Environment: ev.Environment, Service: ev.Service},
		Options: issues.RecordExceptionOptions{
			ObservedAt:  ex.ObservedAt,
			PID:         ev.PID,
			Breadcrumbs: breadcrumbs(ev.Breadcrumbs, scrubber),
			Tags:        tags(ev, scrubber),
			Metadata:    metadata(ev),
		},
	}
	if ev.EventID != "" {
		p.Options.DedupeKey = DedupeKey(ev.EventID)
	}
	return p, true
}

// DedupeKey is the store DedupeKey for an SDK event id.
func DedupeKey(eventID string) string { return "sdk:" + eventID }

// ScrubException redacts ex's Type/Value and every frame's Function text
// in place, recursively through Chained -- error text is untrusted data and
// is redacted before it is persisted or printed (the naming ADR's "Scrub
// por defecto"). Every source uses it: stderr detection, stack-trace
// replays and SDK events. Filename/AbsPath are left alone: they are
// resolved paths (see stacktrace.ApplyGitRoot), not message text.
func ScrubException(scrubber *scrub.Scrubber, ex *stacktrace.Exception) {
	if ex == nil || scrubber == nil {
		return
	}
	ex.Type = scrubber.String(ex.Type)
	ex.Value = scrubber.String(ex.Value)
	for i := range ex.Frames {
		ex.Frames[i].Function = scrubber.String(ex.Frames[i].Function)
	}
	for i := range ex.Chained {
		ScrubException(scrubber, &ex.Chained[i])
	}
}

func breadcrumbs(in []Breadcrumb, scrubber *scrub.Scrubber) []issues.Breadcrumb {
	if len(in) == 0 {
		return nil
	}
	out := make([]issues.Breadcrumb, 0, len(in))
	for _, b := range in {
		msg := b.Message
		if scrubber != nil {
			msg = scrubber.String(msg)
		}
		ts := b.Timestamp
		if !ts.IsZero() {
			ts = ts.UTC()
		}
		out = append(out, issues.Breadcrumb{Timestamp: ts, Category: b.Category, Message: msg, Level: b.Level})
	}
	return out
}

// tags scrubs the event's tags and adds its environment as the
// "environment" tag, the one place a reader sees it.
func tags(ev Event, scrubber *scrub.Scrubber) map[string]string {
	if len(ev.Tags) == 0 && ev.Environment == "" {
		return nil
	}
	out := make(map[string]string, len(ev.Tags)+1)
	for k, v := range ev.Tags {
		if scrubber != nil {
			k, v = scrubber.String(k), scrubber.String(v)
		}
		out[k] = v
	}
	if ev.Environment != "" {
		out["environment"] = ev.Environment
	}
	return out
}

// metadata records where the occurrence came from: which SDK, in which
// mode, and the event kind.
func metadata(ev Event) map[string]string {
	m := map[string]string{"source": "sdk", "event_kind": ev.Kind}
	if ev.SDK.Name != "" {
		name := ev.SDK.Name
		if ev.SDK.Version != "" {
			name += "/" + ev.SDK.Version
		}
		m["sdk"] = name
	}
	if ev.SDK.Mode != "" {
		m["sdk_mode"] = ev.SDK.Mode
	}
	return m
}
