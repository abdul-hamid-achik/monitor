// python.go parses CPython traceback blocks:
//
//   - "Traceback (most recent call last):" dumps, including chains joined by
//     "The above exception was the direct cause of the following exception:"
//     (raise ... from ...) and "During handling of the above exception,
//     another exception occurred:" (implicit context), at any depth;
//   - the logging module's default format, "ERROR:root:msg" immediately
//     followed by the traceback logger.exception(...) prints, or alone for a
//     message-only logger.error(...);
//   - a compile-time "  File "x.py", line N" + caret + "SyntaxError: ..."
//     dump (no Traceback header);
//   - ExceptionGroup renderings ("  + Exception Group Traceback"), alone or
//     as one link of a chain, read at their top nesting level.
//
// Handled vs uncaught. CPython prints a caught-and-printed traceback exactly
// like an uncaught one, so the parser uses the structural signals it has, in
// order: a logging prefix on the block or a log record with a level on the
// line just before it (logger.exception, Flask's "ERROR in app: ...") means
// handled, at that record's level; "Exception in thread ..." before it means an uncaught thread
// crash; a traceback whose oldest frame is the module top level ("<module>")
// or runpy's entry point ("<frozen runpy>", `python -m pkg`) reached the top
// of the interpreter's stack, which only an uncaught exception can do;
// anything else was caught somewhere below the top level.
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
	rePyGroupHeader   = regexp.MustCompile(`^  \+ Exception Group Traceback \(most recent call last\):$`)
	// A logging-module default-format record at warning level or worse;
	// INFO/DEBUG never start a block (they are not exception-worthy) but
	// still count as boundaries (reLoggingAnyLevel).
	reLoggingPrefix   = regexp.MustCompile(`^(CRITICAL|FATAL|ERROR|WARNING):([^\s:]*):(.*)$`)
	reLoggingAnyLevel = regexp.MustCompile(`^(?:CRITICAL|FATAL|ERROR|WARNING|INFO|DEBUG):[^\s:]*:`)
	// A traceback frame line; ", in func" is absent for a SyntaxError's
	// location line.
	rePyFileLine = regexp.MustCompile(`^  File "(.*)", line (\d+)(?:, in (.*))?$`)
	rePySyntaxAt = regexp.MustCompile(`^  File "(.*)", line (\d+)$`)
	// The segment's closing "Type: message" (or bare "Type") line; a class
	// defined in a function prints as "make.<locals>.LocalErr".
	rePyClosingLine = regexp.MustCompile(`^[A-Za-z_][\w.<>]*(?::(?: .*)?)?$`)
	// A logger-shaped record with a warning-or-worse level on the line
	// before a traceback: Python logging's formats put the level right
	// after the leading asctime ("2026-09-22 10:04:37,123 ERROR app: ...")
	// or at the start of the line (Flask's "ERROR in app: ..."). The level
	// word in any other position is prose or source code ("no ERROR here",
	// "print('ERROR: ...')") and must not mark the traceback handled.
	rePyLogRecordPrev = regexp.MustCompile(`^(?:\d{4}-\d{2}-\d{2}[T ][\d:,.+TZ-]*\s+)?(ERROR|CRITICAL|EXCEPTION|FATAL|WARNING)(?::|\s)`)
	// A line of an ExceptionGroup rendering ("  | ...", "  +-+----").
	rePyGroupLine = regexp.MustCompile(`^\s+[|+]`)
)

func pythonBlockStart(line string) bool {
	return reTracebackHeader.MatchString(line) || reLoggingPrefix.MatchString(line) || rePyGroupHeader.MatchString(line)
}

// Python traceback grammar states.
const (
	pyStPrefix    = iota // saw "ERROR:root:msg", expecting a Traceback header
	pyStHeader           // saw "Traceback ...", expecting the first File line
	pyStFrames           // inside File/source/caret lines, expecting the closing line
	pyStClosed           // saw the closing "Type: msg" line
	pyStConnector        // saw a chain connector sentence, expecting the next Traceback
	pyStGroup            // inside an ExceptionGroup rendering
)

type pythonGrammar struct{ st int }

var pythonRule = blockRule{
	kind:  "python",
	start: pythonBlockStart,
	open: func(first string) grammar {
		switch {
		case reTracebackHeader.MatchString(first):
			return &pythonGrammar{st: pyStHeader}
		case rePyGroupHeader.MatchString(first):
			return &pythonGrammar{st: pyStGroup}
		}
		return &pythonGrammar{st: pyStPrefix}
	},
}

func isIndented(line string) bool {
	return strings.HasPrefix(line, " ") || strings.HasPrefix(line, "\t")
}

func (g *pythonGrammar) next(line string, boundary bool) verdict {
	blank := strings.TrimSpace(line) == ""
	switch g.st {
	case pyStPrefix, pyStConnector:
		switch {
		case reTracebackHeader.MatchString(line):
			g.st = pyStHeader
			return vAccept
		case rePyGroupHeader.MatchString(line):
			g.st = pyStGroup
			return vAccept
		case g.st == pyStConnector && blank:
			return vTentative
		}
		return vReject
	case pyStGroup:
		switch {
		case rePyGroupLine.MatchString(line) && !boundary:
			return vAccept
		case blank:
			// Either the end of the group or the gap before a chain
			// connector sentence.
			g.st = pyStClosed
			return vTentative
		}
		return vReject
	case pyStHeader:
		if rePyFileLine.MatchString(line) {
			g.st = pyStFrames
			return vAccept
		}
		return vReject
	case pyStFrames:
		if boundary {
			return vReject
		}
		if rePyFileLine.MatchString(line) || (isIndented(line) && !blank) {
			return vAccept
		}
		if blank {
			return vTentative
		}
		if rePyClosingLine.MatchString(line) {
			g.st = pyStClosed
			return vAccept
		}
		// Another stream's output interleaved between the frames and
		// the closing line (merged stdout/stderr): skip it.
		return vSkip
	case pyStClosed:
		if line == pyConnectorCause || line == pyConnectorContext {
			g.st = pyStConnector
			return vAccept
		}
		if blank {
			return vTentative
		}
		return vReject
	}
	return vReject
}

// pySegment is one "Traceback (most recent call last): ... Type: message"
// section of a (possibly chained) block.
type pySegment struct {
	frames []Frame
	typ    string
	val    string
}

// topLevel reports whether the segment's oldest frame is an interpreter
// entry point: the module top level, or runpy's `python -m` driver.
func (s pySegment) topLevel() bool {
	if len(s.frames) == 0 {
		return false
	}
	f := s.frames[0]
	if f.Filename == "<frozen runpy>" {
		return true
	}
	return f.Function == "<module>"
}

func pyFrame(file, line, fn string) Frame {
	ln, _ := strconv.Atoi(line)
	f := Frame{Filename: file, Lineno: ln, Function: fn}
	if isAbsPath(file) {
		f.AbsPath = file
	}
	return f
}

func splitPyClosing(l string) (typ, val string) {
	if at := strings.Index(l, ":"); at >= 0 {
		return l[:at], strings.TrimSpace(l[at+1:])
	}
	return l, ""
}

// parsePythonSegment parses one segment's lines (starting at its own
// "Traceback (most recent call last):" header) into a pySegment. It stops at
// the segment's own closing line.
func parsePythonSegment(lines []string) pySegment {
	var seg pySegment
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "" {
			continue
		}
		if m := rePyFileLine.FindStringSubmatch(l); m != nil {
			seg.frames = append(seg.frames, pyFrame(m[1], m[2], m[3]))
			continue
		}
		if isIndented(l) {
			continue // source-context, caret, or "[Previous line repeated ...]"
		}
		seg.typ, seg.val = splitPyClosing(l)
		break
	}
	return seg
}

// pythonDisposition decides Handled for the outer segment (see the file doc
// for the order of signals) and the level: a logged traceback takes its
// logger's level
// (WARNING:root: -> warning, CRITICAL -> fatal), an uncaught one is fatal,
// anything else caught is an error.
func pythonDisposition(b Block, outer pySegment) (handled bool, level string) {
	if m := reLoggingPrefix.FindStringSubmatch(b.Lines[0]); m != nil {
		return true, logRecordLevel(m[1])
	}
	switch {
	case strings.HasPrefix(b.Prev, "Exception in thread "):
		return false, LevelFatal
	case b.Prev != "":
		if m := reLoggingPrefix.FindStringSubmatch(b.Prev); m != nil {
			return true, logRecordLevel(m[1])
		}
		if m := rePyLogRecordPrev.FindStringSubmatch(b.Prev); m != nil {
			return true, logRecordLevel(m[1])
		}
	}
	if outer.topLevel() {
		return false, LevelFatal
	}
	return true, LevelError
}

// logRecordLevel maps a logging level word to a Level; logger.exception
// records at ERROR, so EXCEPTION is an error too.
func logRecordLevel(word string) string {
	if level, ok := levelFromLogPrefix(word); ok {
		return level
	}
	return LevelError
}

// isPySegmentStart reports a line that opens one section of a (possibly
// chained) Python dump: a Traceback header or an ExceptionGroup header.
func isPySegmentStart(l string) bool {
	return reTracebackHeader.MatchString(l) || rePyGroupHeader.MatchString(l)
}

// parsePython turns a joined "python"-kind block into an Exception. Frames
// are already oldest-first ("most recent call last"), so unlike the other
// parsers this one never reverses them; the printed segment order is
// reversed instead to get Chained outer -> innermost.
func parsePython(block Block) *Exception {
	buf := block.Lines
	var segStarts []int
	for i, l := range buf {
		if isPySegmentStart(l) {
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
		if rePyGroupHeader.MatchString(buf[start]) {
			segs = append(segs, parsePythonGroupSegment(buf[start:end]))
			continue
		}
		segs = append(segs, parsePythonSegment(buf[start:end]))
	}
	outer := segs[len(segs)-1]
	if outer.typ == "" && len(outer.frames) == 0 {
		return nil
	}
	handled, level := pythonDisposition(block, outer)
	ex := &Exception{
		Runtime:    "python",
		Type:       outer.typ,
		Value:      outer.val,
		Parser:     "python",
		Handled:    boolPtr(handled),
		Frames:     outer.frames,
		Level:      level,
		LineStart:  block.LineStart,
		LineEnd:    block.LineEnd,
		ObservedAt: blockTimestamp(block),
	}
	for i := len(segs) - 2; i >= 0; i-- {
		ex.Chained = append(ex.Chained, Exception{Type: segs[i].typ, Value: segs[i].val, Frames: segs[i].frames})
	}
	inheritChain(ex)
	return ex
}

// parsePythonMessageOnly handles a logging-prefixed line with no attached
// traceback (logger.error(...) without exc_info): "ERROR:root:message".
func parsePythonMessageOnly(block Block) *Exception {
	m := reLoggingPrefix.FindStringSubmatch(block.Lines[0])
	if m == nil {
		return nil
	}
	level, ok := levelFromLogPrefix(m[1])
	if !ok {
		return nil
	}
	return &Exception{
		Runtime:    "python",
		Value:      strings.TrimSpace(m[3]),
		Parser:     "message",
		Handled:    boolPtr(true),
		Level:      level,
		LineStart:  block.LineStart,
		LineEnd:    block.LineEnd,
		ObservedAt: blockTimestamp(block),
	}
}

// --- SyntaxError dumps (no Traceback header) ---

var pythonSyntaxRule = blockRule{
	kind:  "python-syntax",
	start: rePySyntaxAt.MatchString,
	open:  func(string) grammar { return &pySyntaxGrammar{} },
}

type pySyntaxGrammar struct{ done bool }

func (g *pySyntaxGrammar) next(line string, boundary bool) verdict {
	if g.done || boundary {
		return vReject
	}
	if isIndented(line) && strings.TrimSpace(line) != "" {
		return vAccept // source line, caret line
	}
	if rePyClosingLine.MatchString(line) {
		g.done = true
		return vAccept
	}
	return vReject
}

func parsePythonSyntax(block Block) *Exception {
	m := rePySyntaxAt.FindStringSubmatch(block.Lines[0])
	if m == nil || len(block.Lines) < 2 {
		return nil
	}
	last := block.Lines[len(block.Lines)-1]
	if isIndented(last) {
		return nil
	}
	typ, val := splitPyClosing(last)
	if !strings.HasSuffix(typ, "Error") {
		return nil
	}
	return &Exception{
		Runtime:    "python",
		Type:       typ,
		Value:      val,
		Parser:     "python",
		Handled:    boolPtr(false),
		Level:      LevelFatal,
		Frames:     []Frame{pyFrame(m[1], m[2], "")},
		LineStart:  block.LineStart,
		LineEnd:    block.LineEnd,
		ObservedAt: blockTimestamp(block),
	}
}

// --- ExceptionGroup segments ---

// parsePythonGroupSegment parses an ExceptionGroup rendering's own
// traceback (the "  | " lines at the top nesting level); the
// sub-exceptions are summarized by the group's message ("... (2
// sub-exceptions)") and not turned into Chained causes, since they are
// siblings, not causes. The group may itself be a link of a chain
// ("raise RuntimeError(...) from eg").
func parsePythonGroupSegment(lines []string) pySegment {
	tb := []string{"Traceback (most recent call last):"}
	for _, l := range lines[1:] {
		if rest, ok := strings.CutPrefix(l, "  | "); ok {
			tb = append(tb, rest)
		}
	}
	return parsePythonSegment(tb)
}
