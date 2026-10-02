// Package monitor is Monitor's own Go SDK: explicit error capture for Go
// programs, delivered to the monitor CLI as monitor.event.v1 files.
//
// Go cannot be instrumented from the outside, so this SDK is explicit only:
//
//	monitor.Init(monitor.Options{Service: "api", Release: version})
//	defer monitor.Recover() // in main and at the top of goroutines
//	...
//	if err != nil {
//		monitor.CaptureError(err)
//	}
//
// It has no dependencies, never prints, and never reads anything but the
// error itself (its type, message, Unwrap chain and the call stack where it
// was captured). Each event is one JSON file written atomically into
// $MONITOR_EVENTS_DIR when `monitor run --` launched the program, else into
// the global inbox that `monitor events drain` and `monitor issues` read.
// Grouping, scrubbing and the culprit line are worked out by monitor.
package monitor

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Version is this SDK's version, sent with every event.
const Version = "0.1.0"

const (
	schema         = "monitor.event.v1"
	maxBreadcrumbs = 30
	maxCauses      = 8
	maxFrames      = 128
	maxText        = 8 << 10
	// At most rateMax events per rateWindow; the rest are dropped, so an
	// error loop never becomes a disk-filling loop.
	rateMax    = 50
	rateWindow = 10 * time.Second
)

// Level is an event's severity.
type Level string

const (
	LevelFatal   Level = "fatal"
	LevelError   Level = "error"
	LevelWarning Level = "warning"
	LevelInfo    Level = "info"
)

// Options configure the SDK. Every field is optional.
type Options struct {
	Service     string
	Release     string
	Environment string
	Tags        map[string]string
	// Dir overrides where event files go (default: $MONITOR_EVENTS_DIR,
	// then the global inbox).
	Dir string
	// BeforeSend may edit an event or drop it by returning nil.
	BeforeSend func(*Event) *Event
}

// Breadcrumb is one step recorded before an event.
type Breadcrumb struct {
	Timestamp time.Time `json:"timestamp"`
	Category  string    `json:"category,omitempty"`
	Message   string    `json:"message"`
	Level     Level     `json:"level,omitempty"`
}

// Event is one monitor.event.v1 document.
type Event struct {
	Schema      string            `json:"$schema"`
	EventID     string            `json:"event_id"`
	Timestamp   time.Time         `json:"timestamp"`
	Kind        string            `json:"kind"`
	Handled     *bool             `json:"handled,omitempty"`
	Level       Level             `json:"level,omitempty"`
	Runtime     string            `json:"runtime"`
	Error       *Error            `json:"error,omitempty"`
	Message     string            `json:"message,omitempty"`
	Tags        map[string]string `json:"tags,omitempty"`
	Breadcrumbs []Breadcrumb      `json:"breadcrumbs,omitempty"`
	Service     string            `json:"service,omitempty"`
	Release     string            `json:"release,omitempty"`
	Environment string            `json:"environment,omitempty"`
	Cwd         string            `json:"cwd,omitempty"`
	PID         int               `json:"pid,omitempty"`
	LaunchID    string            `json:"launch_id,omitempty"`
	SDK         SDKInfo           `json:"sdk"`
}

// Error is one error in the chain, with structured frames, oldest first
// and the frame closest to the fault last.
type Error struct {
	Type   string  `json:"type,omitempty"`
	Value  string  `json:"value,omitempty"`
	Frames []Frame `json:"frames,omitempty"`
	Cause  *Error  `json:"cause,omitempty"`
}

// Frame is one stack frame.
type Frame struct {
	Function string `json:"function,omitempty"`
	Module   string `json:"module,omitempty"`
	Filename string `json:"filename,omitempty"`
	Lineno   int    `json:"lineno,omitempty"`
}

// SDKInfo names the producer.
type SDKInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Mode    string `json:"mode"`
}

var (
	mu          sync.Mutex
	opts        Options
	tags        = map[string]string{}
	breadcrumbs []Breadcrumb
	window      time.Time
	windowCount int
	dropped     int
	seq         uint64
)

// Init sets the context every later event carries. Safe to call more than
// once; the latest call wins.
func Init(o Options) {
	mu.Lock()
	defer mu.Unlock()
	opts = o
	for k, v := range o.Tags {
		tags[k] = v
	}
}

// CaptureOption adjusts one capture.
type CaptureOption func(*capture)

type capture struct {
	level   Level
	tags    map[string]string
	handled bool
}

// WithLevel sets the event's level (default error for errors, info for
// messages).
func WithLevel(l Level) CaptureOption { return func(c *capture) { c.level = l } }

// WithTags adds tags to this event only.
func WithTags(t map[string]string) CaptureOption {
	return func(c *capture) {
		if c.tags == nil {
			c.tags = map[string]string{}
		}
		for k, v := range t {
			c.tags[k] = v
		}
	}
}

// Unhandled marks the error as one the program did not recover from.
func Unhandled() CaptureOption { return func(c *capture) { c.handled = false } }

// CaptureError records err with the stack of the caller and returns the
// event id ("" when nothing was written, e.g. err is nil or the rate limit
// was hit).
func CaptureError(err error, options ...CaptureOption) string {
	if err == nil {
		return ""
	}
	c := capture{level: LevelError, handled: true}
	for _, o := range options {
		o(&c)
	}
	e := serialize(err)
	e.Frames = callerFrames(3)
	return send("captured", e, "", c)
}

// CaptureMessage records a message as an event, grouped by its text.
func CaptureMessage(message string, level Level, options ...CaptureOption) string {
	message = strings.TrimSpace(message)
	if message == "" {
		return ""
	}
	if level == "" {
		level = LevelInfo
	}
	c := capture{level: level, handled: true}
	for _, o := range options {
		o(&c)
	}
	return send("message", nil, clip(message, maxText), c)
}

// Recover records a panic and panics again, so the program still crashes
// exactly as it would have (use RecoverAndContinue to keep going). It must
// be called directly by defer:
//
//	defer monitor.Recover()
func Recover() {
	if v := recover(); v != nil {
		capturePanic(v)
		panic(v)
	}
}

// RecoverAndContinue records a panic and swallows it: for a goroutine or
// request handler that should survive one. It must be called directly by
// defer.
func RecoverAndContinue() {
	if v := recover(); v != nil {
		capturePanic(v)
	}
}

func capturePanic(v any) {
	var e *Error
	if err, ok := v.(error); ok {
		e = serialize(err)
		e.Type = "panic"
		e.Value = err.Error()
	} else {
		e = &Error{Type: "panic", Value: clip(fmt.Sprint(v), maxText)}
	}
	e.Frames = panicFrames()
	send("uncaught", e, "", capture{level: LevelFatal, handled: false})
}

// AddBreadcrumb records a step that later events carry (the newest 30 are
// kept).
func AddBreadcrumb(category, message string) {
	message = strings.TrimSpace(message)
	if message == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	breadcrumbs = append(breadcrumbs, Breadcrumb{Timestamp: time.Now().UTC(), Category: category, Message: clip(message, 1024)})
	if len(breadcrumbs) > maxBreadcrumbs {
		breadcrumbs = breadcrumbs[len(breadcrumbs)-maxBreadcrumbs:]
	}
}

// SetTag labels every later event; an empty value removes the tag.
func SetTag(key, value string) {
	mu.Lock()
	defer mu.Unlock()
	if value == "" {
		delete(tags, key)
		return
	}
	tags[key] = clip(value, 256)
}

// serialize turns err and its Unwrap chain into an Error. Only the outer
// error gets frames (the capture site's stack): Go errors carry no stack.
func serialize(err error) *Error {
	out := &Error{Type: fmt.Sprintf("%T", err), Value: clip(err.Error(), maxText)}
	cur := out
	for depth := 1; depth < maxCauses; depth++ {
		err = unwrapOne(err)
		if err == nil {
			break
		}
		cur.Cause = &Error{Type: fmt.Sprintf("%T", err), Value: clip(err.Error(), maxText)}
		cur = cur.Cause
	}
	return out
}

func unwrapOne(err error) error {
	if next := errors.Unwrap(err); next != nil {
		return next
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range multi.Unwrap() {
			if e != nil {
				return e
			}
		}
	}
	return nil
}

// callerFrames returns the calling goroutine's stack, skipping skip frames
// (runtime.Callers itself counts as one), oldest first.
func callerFrames(skip int) []Frame {
	pcs := make([]uintptr, maxFrames)
	n := runtime.Callers(skip, pcs)
	return framesOf(pcs[:n], nil)
}

// panicFrames returns the panicking goroutine's stack from the function
// that panicked outward: everything up to and including the runtime's
// panic machinery (and this SDK) is cut.
func panicFrames() []Frame {
	pcs := make([]uintptr, maxFrames)
	n := runtime.Callers(1, pcs)
	cut := func(fn string) bool {
		return fn == "runtime.gopanic" || fn == "runtime.sigpanic" || strings.HasPrefix(fn, "runtime.panic")
	}
	return framesOf(pcs[:n], cut)
}

// framesOf resolves pcs (innermost first) into Frames, oldest first. When
// cutAfter is set, frames up to and including the last one it matches are
// dropped.
func framesOf(pcs []uintptr, cutAfter func(string) bool) []Frame {
	var inner []Frame
	it := runtime.CallersFrames(pcs)
	for {
		f, more := it.Next()
		if f.Function != "" {
			inner = append(inner, Frame{Function: f.Function, Module: goModule(f.Function), Filename: f.File, Lineno: f.Line})
		}
		if !more {
			break
		}
	}
	if cutAfter != nil {
		for i := len(inner) - 1; i >= 0; i-- {
			if cutAfter(inner[i].Function) {
				inner = inner[i+1:]
				break
			}
		}
	}
	// Drop the runtime's own entry frames (runtime.main, goexit): they are
	// on every stack and say nothing about the error.
	out := make([]Frame, 0, len(inner))
	for i := len(inner) - 1; i >= 0; i-- {
		if fn := inner[i].Function; fn == "runtime.main" || fn == "runtime.goexit" {
			continue
		}
		out = append(out, inner[i])
	}
	return out
}

// goModule returns a function symbol's package path:
// "example.com/app/svc.(*Server).Handle" -> "example.com/app/svc".
func goModule(fn string) string {
	slash := strings.LastIndex(fn, "/")
	if dot := strings.Index(fn[slash+1:], "."); dot >= 0 {
		return fn[:slash+1+dot]
	}
	return ""
}

func send(kind string, e *Error, message string, c capture) string {
	handled := c.handled
	ev := &Event{
		Schema:    schema,
		EventID:   newID(),
		Timestamp: time.Now().UTC(),
		Kind:      kind,
		Handled:   &handled,
		Level:     c.level,
		Runtime:   "go",
		Error:     e,
		Message:   message,
		PID:       os.Getpid(),
		LaunchID:  strings.TrimSpace(os.Getenv("MONITOR_LAUNCH_ID")),
		SDK:       SDKInfo{Name: "monitor.go", Version: Version, Mode: "explicit"},
	}
	if wd, err := os.Getwd(); err == nil {
		ev.Cwd = wd
	}

	mu.Lock()
	ev.Service, ev.Release, ev.Environment = opts.Service, opts.Release, opts.Environment
	if len(tags)+len(c.tags) > 0 {
		ev.Tags = make(map[string]string, len(tags)+len(c.tags))
		for k, v := range tags {
			ev.Tags[k] = v
		}
		for k, v := range c.tags {
			ev.Tags[k] = v
		}
	}
	ev.Breadcrumbs = append([]Breadcrumb(nil), breadcrumbs...)
	beforeSend, dir := opts.BeforeSend, opts.Dir
	mu.Unlock()

	if beforeSend != nil {
		if ev = beforeSend(ev); ev == nil {
			return ""
		}
	}

	mu.Lock()
	now := time.Now()
	if now.Sub(window) > rateWindow {
		window, windowCount = now, 0
	}
	if windowCount >= rateMax {
		dropped++
		mu.Unlock()
		return ""
	}
	windowCount++
	seq++
	n := seq
	mu.Unlock()

	data, err := json.Marshal(ev)
	if err != nil {
		return ""
	}
	name := fmt.Sprintf("%019d-%d-%s", ev.Timestamp.UnixNano()+int64(n%1000), ev.PID, ev.EventID)
	for _, d := range []string{dir, strings.TrimSpace(os.Getenv("MONITOR_EVENTS_DIR")), inboxDir()} {
		if d != "" && writeInto(d, name, data) == nil {
			return ev.EventID
		}
	}
	return ""
}

func writeInto(dir, name string, data []byte) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	tmp := filepath.Join(dir, "."+name+".tmp")
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, filepath.Join(dir, name+".json")); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func inboxDir() string {
	base := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		base = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(base, "monitor", "events", "inbox")
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b[:])
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	s = s[:n]
	for len(s) > 0 && s[len(s)-1]&0xC0 == 0x80 {
		s = s[:len(s)-1]
	}
	if len(s) > 0 && s[len(s)-1] >= 0xC0 {
		s = s[:len(s)-1]
	}
	return s
}
