// Command go-zap-stdout emulates, with plain fmt (no zap or pkg/errors
// dependency), what internal/stacktrace's zap.go parser needs to recognize
// in a Go service whose logger writes everything to stdout
// (ErrorOutputPaths: [stdout]): a zap development-console error entry
// ("<ISO8601 ts>\tERROR\t<caller>\t<msg>\t<json fields>") followed by the
// error's pkg/errors-style "%+v" stack dump.
package main

import (
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"time"
)

type frame struct {
	function string
	file     string
	line     int
}

// tracedError is a minimal, dependency-free stand-in for pkg/errors'
// typical "withStack" error: it captures a real call stack at creation time
// via runtime.Callers and formats it exactly like pkg/errors' "%+v" verb
// does (message, then one "<func>\n\t<file>:<line>" pair per frame, deepest
// first), so the parser that reads this program's output is exercised
// against real, accurate file:line data instead of a hand-typed fixture.
type tracedError struct {
	msg    string
	frames []frame
}

func newTracedError(msg string) error {
	pcs := make([]uintptr, 32)
	n := runtime.Callers(2, pcs) // skip runtime.Callers and newTracedError itself
	iter := runtime.CallersFrames(pcs[:n])
	var fs []frame
	for {
		fr, more := iter.Next()
		fs = append(fs, frame{function: fr.Function, file: fr.File, line: fr.Line})
		if !more {
			break
		}
	}
	return &tracedError{msg: msg, frames: fs}
}

func (e *tracedError) Error() string { return e.msg }

func (e *tracedError) Format(s fmt.State, verb rune) {
	if verb == 'v' && s.Flag('+') {
		io.WriteString(s, e.msg)
		for _, f := range e.frames {
			fmt.Fprintf(s, "\n%s\n\t%s:%d", f.function, f.file, f.line)
		}
		return
	}
	io.WriteString(s, e.msg)
}

// worker has a pointer-receiver method so the trace carries a
// "main.(*worker).doWork" frame, the shape real services print most.
type worker struct{ name string }

func (w *worker) doWork() error {
	return newTracedError("root cause boom") // the call site the golden test expects as the crash frame
}

// caller renders zap's short caller column ("dir/file.go:line").
func caller() string {
	_, file, line, _ := runtime.Caller(1)
	return fmt.Sprintf("%s/%s:%d", filepath.Base(filepath.Dir(file)), filepath.Base(file), line)
}

func main() {
	fmt.Println("[go-zap-stdout] starting")
	w := &worker{name: "sync"}
	err := w.doWork()

	ts := time.Now().Format("2006-01-02T15:04:05.000Z0700")
	fmt.Printf("%s\tERROR\t%s\trequest failed\t{\"error\": %q, \"request_id\": \"abc123\"}\n", ts, caller(), err.Error())
	fmt.Printf("%+v\n", err)
}
