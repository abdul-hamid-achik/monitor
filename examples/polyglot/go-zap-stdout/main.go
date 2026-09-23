// Command go-zap-stdout emulates, with plain fmt (no zap or pkg/errors
// dependency), the two things internal/stacktrace's zap.go parser needs to
// recognize: a zap console-encoder error line ("<ts>\terror\t<msg>\t<json>")
// and a pkg/errors-style "%+v" stack dump, both to stdout -- matching how
// Graphite's own services are configured (ErrorOutputPaths: [stdout]).
package main

import (
	"fmt"
	"io"
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

func doWork() error {
	return newTracedError("root cause boom") // the call site the golden test expects as the top frame
}

func main() {
	fmt.Println("[go-zap-stdout] starting")
	err := doWork()

	ts := time.Now().UTC().Format(time.RFC3339Nano)
	fmt.Printf("%s\terror\trequest failed\t{\"error\":%q,\"request_id\":\"abc123\"}\n", ts, err.Error())
	fmt.Printf("%+v\n", err)
}
