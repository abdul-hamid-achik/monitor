// tslog.go parses a typescript-logging-style structured log line and its
// "at fn() @ file:line:col" frames, plus a Temporal ApplicationFailure-like
// "Caused by:" chain. Both shapes are synthetic (there is no single de
// facto text format for either), fixed here so the parser and the fixture
// it reads agree:
//
//	<RFC3339 ts> (ERROR|WARN|FATAL|INFO|DEBUG) [<logger>] <Type>: <message>
//	    at <func>() @ <file>:<line>:<col>      <- crash frame first
//	    at <caller>() @ <file>:<line>:<col>
//	Caused by: <Type>: <message>
//	    at <func>() @ <file>:<line>:<col>
//
// Like V8, the format prints the crash (innermost) frame first, so frames
// are reversed to this package's oldest-first, crash-last order. "Caused
// by:" sections follow the outer exception's frames, which is already this
// package's outer-to-inner Chained order.
package stacktrace

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// Any level is a record boundary; only WARN/ERROR/FATAL open a block.
	reTsLogHeader = regexp.MustCompile(`^(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2}))\s+(ERROR|WARN|FATAL|INFO|DEBUG|TRACE)\s+\[[^\]]+\]\s+(.*)$`)
	reTsLogFrame  = regexp.MustCompile(`^\s+at\s+(.+?)\(\)\s*@\s*(.+):(\d+):(\d+)$`)
	reTsLogCause  = regexp.MustCompile(`^Caused by:\s*(.*)$`)
)

func tslogStart(line string) bool {
	m := reTsLogHeader.FindStringSubmatch(line)
	if m == nil {
		return false
	}
	_, ok := levelFromLogPrefix(m[2])
	return ok
}

var tslogRule = blockRule{
	kind:  "tslog",
	start: tslogStart,
	open:  func(string) grammar { return tslogGrammar{} },
}

type tslogGrammar struct{}

func (tslogGrammar) next(line string, boundary bool) verdict {
	switch {
	case boundary:
		return vReject
	case reTsLogFrame.MatchString(line), reTsLogCause.MatchString(line),
		reJSFrameLine.MatchString(line):
		// A printf-style logger ("<ts> ERROR [api] ${err.stack}") puts
		// ordinary V8 "at" frames under the same header.
		return vAccept
	case strings.TrimSpace(line) == "":
		return vTentative
	}
	return vReject
}

// tslogTypeValue splits "Type: message" as printed after the level/logger
// prefix (or after "Caused by:"); only the first ": " separates them.
func tslogTypeValue(s string) (typ, val string) {
	if at := strings.Index(s, ": "); at >= 0 {
		return s[:at], s[at+2:]
	}
	return s, ""
}

func tslogFrame(m []string) Frame {
	ln, _ := strconv.Atoi(m[3])
	col, _ := strconv.Atoi(m[4])
	f := Frame{Function: strings.TrimSpace(m[1]), Filename: m[2], Lineno: ln, Colno: col}
	if isAbsPath(m[2]) {
		f.AbsPath = m[2]
	}
	return f
}

func parseTslog(block Block) *Exception {
	lines := block.Lines
	header := reTsLogHeader.FindStringSubmatch(lines[0])
	if header == nil {
		return nil
	}
	level, ok := levelFromLogPrefix(header[2])
	if !ok {
		return nil
	}
	typ, val := tslogTypeValue(header[3])
	ex := &Exception{
		Runtime:    "node",
		Type:       typ,
		Value:      val,
		Parser:     "tslog",
		Handled:    boolPtr(level != LevelFatal),
		Level:      level,
		LineStart:  block.LineStart,
		LineEnd:    block.LineEnd,
		ObservedAt: blockTimestamp(block),
	}
	cur := ex
	var chain []Exception
	var frames []Frame
	flush := func() {
		reverseFrames(frames)
		if cur == ex {
			ex.Frames = frames
		} else {
			chain[len(chain)-1].Frames = frames
		}
		frames = nil
	}
	for _, l := range lines[1:] {
		if m := reTsLogFrame.FindStringSubmatch(l); m != nil {
			frames = append(frames, tslogFrame(m))
			continue
		}
		if f, ok := parseJSFrameLine(l); ok {
			frames = append(frames, f)
			continue
		}
		if m := reTsLogCause.FindStringSubmatch(l); m != nil {
			flush()
			ctyp, cval := tslogTypeValue(m[1])
			chain = append(chain, Exception{Type: ctyp, Value: cval})
			cur = nil
		}
	}
	flush()
	ex.Chained = chain
	if len(ex.Frames) == 0 {
		// A logger error line with no stack: message-only.
		ex.Parser = "message"
		ex.Type, ex.Value = "", header[3]
		ex.Chained = nil
	}
	inheritChain(ex)
	return ex
}
