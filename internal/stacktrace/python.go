// python.go parses CPython traceback blocks, including chained exceptions
// ("The above exception was the direct cause of the following exception:"
// for `raise ... from ...`, and "During handling of the above exception,
// another exception occurred:" for an exception raised while handling
// another) and the `logging` module's default format ("ERROR:root:msg"
// immediately followed by a traceback, from logger.exception(...)).
//
// Unlike every other runtime this package parses, CPython's traceback
// format gives no textual marker distinguishing a caught-and-printed
// traceback from a truly uncaught one -- traceback.print_exc() and the
// interpreter's own uncaught-exception dump look identical. This package
// uses one real structural signal instead of guessing: a traceback whose
// oldest (first-printed) frame is "<module>" (or another top-level
// callable) reached the very top of some call stack, which is only
// possible for a *truly* uncaught exception -- Python's traceback object
// only accumulates frames between where the exception was raised and
// where a "except" clause caught it, so a caught traceback re-raised
// from inside a function is never able to include its caller's frames.
package stacktrace

import (
	"regexp"
	"strconv"
	"strings"
)

const (
	pyConnectorCause   = "The above exception was the direct cause of the following exception:"
	pyConnectorContext = "During handling of the above exception, another exception occurred:"
)

var (
	reTracebackHeader = regexp.MustCompile(`^Traceback \(most recent call last\):$`)
	// INFO and DEBUG are deliberately excluded: they're never exception-
	// worthy on their own, and including them would turn ordinary chatter
	// into false-positive message events (breaking "clean logs -> 0
	// events").
	reLoggingPrefix = regexp.MustCompile(`^(CRITICAL|ERROR|WARNING|WARN):\S+:.*$`)
	rePyFileLine    = regexp.MustCompile(`^  File "(.*)", line (\d+), in (.*)$`)
	rePyCaret       = regexp.MustCompile(`^\s*[~^]+\s*$`)
	rePyClosingLine = regexp.MustCompile(`^[A-Za-z_][\w.]*(: .*)?$`)
)

func pythonBlockStart(line string) bool {
	return reTracebackHeader.MatchString(line) || reLoggingPrefix.MatchString(line)
}

func pyTracebackCount(buf []string) int {
	n := 0
	for _, l := range buf {
		if reTracebackHeader.MatchString(l) {
			n++
		}
	}
	return n
}

func lastNonBlank(buf []string) string {
	for i := len(buf) - 1; i >= 0; i-- {
		if strings.TrimSpace(buf[i]) != "" {
			return buf[i]
		}
	}
	return ""
}

func pythonBlockContinues(buf []string, line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	if rePyFileLine.MatchString(line) {
		return true
	}
	if rePyCaret.MatchString(line) {
		return true
	}
	if line == pyConnectorCause || line == pyConnectorContext {
		return true
	}
	if reTracebackHeader.MatchString(line) {
		if pyTracebackCount(buf) == 0 {
			return true // first segment header, e.g. after a logging prefix
		}
		last := lastNonBlank(buf)
		return last == pyConnectorCause || last == pyConnectorContext
	}
	if strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t") {
		// An indented source-context line under a File line: arbitrary
		// code text we don't need to parse.
		return true
	}
	// The segment's closing "Type: message" (or bare "Type") line.
	return rePyClosingLine.MatchString(line)
}

var pythonRule = blockRule{kind: "python", start: pythonBlockStart, cont: pythonBlockContinues}

// pySegment is one "Traceback (most recent call last): ... Type: message"
// section of a (possibly chained) block.
type pySegment struct {
	frames []Frame
	typ    string
	val    string
}

func (s pySegment) topIsModule() bool {
	if len(s.frames) == 0 {
		return false
	}
	fn := s.frames[0].Function
	return fn == "<module>" || strings.HasPrefix(fn, "<module ") || fn == "<lambda>"
}

func (s pySegment) toException() Exception {
	level, handled := LevelError, boolPtr(true)
	if s.topIsModule() {
		level, handled = LevelFatal, boolPtr(false)
	}
	return Exception{
		Runtime: "python",
		Type:    s.typ,
		Value:   s.val,
		Parser:  "python",
		Handled: handled,
		Frames:  s.frames,
		Level:   level,
	}
}

// parsePythonSegment parses one segment's lines (starting at its own
// "Traceback (most recent call last):" header) into a pySegment. It stops
// at the segment's own closing line and ignores anything after (a
// connector sentence, or the start of the next segment) that the caller
// may have included in lines.
func parsePythonSegment(lines []string) pySegment {
	var seg pySegment
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if m := rePyFileLine.FindStringSubmatch(l); m != nil {
			ln, _ := strconv.Atoi(m[2])
			f := Frame{Filename: m[1], Lineno: ln, Function: m[3]}
			if strings.HasPrefix(m[1], "/") {
				f.AbsPath = m[1]
			}
			seg.frames = append(seg.frames, f)
			continue
		}
		if rePyCaret.MatchString(l) {
			continue
		}
		if strings.HasPrefix(l, " ") || strings.HasPrefix(l, "\t") {
			continue // source-context line
		}
		// First non-indented, non-blank line after the frames: the
		// segment's closing "Type: message" line. Nothing after this
		// belongs to this segment.
		if at := strings.Index(l, ": "); at >= 0 {
			seg.typ, seg.val = l[:at], l[at+2:]
		} else {
			seg.typ = l
		}
		break
	}
	return seg
}

// parsePython turns a joined "python"-kind block into an Exception. Frames
// are already oldest-first ("most recent call last"), so unlike js.go and
// ruby.go this parser never reverses them.
func parsePython(block Block) *Exception {
	buf := block.Lines
	var segStarts []int
	for i, l := range buf {
		if reTracebackHeader.MatchString(l) {
			segStarts = append(segStarts, i)
		}
	}
	if len(segStarts) == 0 {
		return parsePythonMessageOnly(block)
	}
	segs := make([]pySegment, 0, len(segStarts))
	for i, start := range segStarts {
		end := len(buf)
		if i+1 < len(segStarts) {
			end = segStarts[i+1]
		}
		segs = append(segs, parsePythonSegment(buf[start:end]))
	}
	outer := segs[len(segs)-1]
	exVal := outer.toException()
	ex := &exVal
	ex.Parser = "python"
	ex.LineStart, ex.LineEnd = block.LineStart, block.LineEnd
	if ts, ok := parseTimestamp(buf[0]); ok {
		ex.ObservedAt = ts
	}
	for i := len(segs) - 2; i >= 0; i-- {
		ex.Chained = append(ex.Chained, segs[i].toException())
	}
	return ex
}

// parsePythonMessageOnly handles a logging-prefixed line with no attached
// traceback (logger.error(...) without exc_info): "ERROR:root:message".
func parsePythonMessageOnly(block Block) *Exception {
	if len(block.Lines) == 0 {
		return nil
	}
	first := block.Lines[0]
	m := reLoggingPrefix.FindStringSubmatch(first)
	if m == nil {
		return nil
	}
	level, ok := levelFromLogPrefix(m[1])
	if !ok {
		level = LevelError
	}
	// "LEVEL:logger:message" -- split off the level and logger name,
	// keep the rest (which may itself contain colons) as the message.
	parts := strings.SplitN(first, ":", 3)
	msg := first
	if len(parts) == 3 {
		msg = parts[2]
	}
	ex := &Exception{
		Runtime:   "python",
		Value:     strings.TrimSpace(msg),
		Parser:    "message",
		Level:     level,
		LineStart: block.LineStart,
		LineEnd:   block.LineEnd,
	}
	if ts, ok := parseTimestamp(first); ok {
		ex.ObservedAt = ts
	}
	return ex
}
