// js.go parses V8-family stack traces: plain Node/Deno/Bun "Error: msg" +
// "at ..." stacks printed by a caught-and-logged error (console.error(err)
// or console.error(err.stack)), Node's own uncaught-exception formatter
// (a bare "path:line" code-frame intro), Deno's "error: Uncaught ..."
// formatter, and Bun's numbered code-frame crash printer. All three crash
// formats end with the same "at ..." stack shape once past their header.
package stacktrace

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	reJSErrorLine         = regexp.MustCompile(`^[\w$.]*(?:Error|Exception):\s.*$`)
	reJSErrorLineOnlyType = regexp.MustCompile(`^[\w$.]*(?:Error|Exception)$`)
	reNodeCodeFrame       = regexp.MustCompile(`^/\S+\.(?:m?js|cjs|ts|tsx|jsx):\d+$`)
	reDenoCrash           = regexp.MustCompile(`^error: Uncaught\s.*$`)
	reBunCodeFrame        = regexp.MustCompile(`^\d+ \|.*$`)
	reJSFrameLine         = regexp.MustCompile(`^\s+at\s+.+$`)
	reJSCause             = regexp.MustCompile(`^\s*\[cause\]:\s*(.*)$`)
	reJSCaretLine         = regexp.MustCompile(`^\s*\^\s*$`)
	reJSCollapsedFrames   = regexp.MustCompile(`^\s*\.\.\.\s+\d+\s+lines?\s+matching\s+.*\.\.\.$`)
	reJSLoc               = regexp.MustCompile(`^(.*):(\d+):(\d+)$`)
)

func jsBlockStart(line string) bool {
	return reJSErrorLine.MatchString(line) || reJSErrorLineOnlyType.MatchString(line) ||
		reNodeCodeFrame.MatchString(line) || reDenoCrash.MatchString(line) || reBunCodeFrame.MatchString(line)
}

func jsSeenFrame(buf []string) bool {
	for _, l := range buf {
		if reJSFrameLine.MatchString(l) {
			return true
		}
	}
	return false
}

func jsBlockContinues(buf []string, line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	if reJSFrameLine.MatchString(line) {
		return true
	}
	if reJSCaretLine.MatchString(line) {
		return true
	}
	if reJSCollapsedFrames.MatchString(line) {
		// Recent Node versions collapse a shared-suffix run of frames
		// between an outer error and its cause into a single "... N
		// lines matching cause stack trace ..." placeholder. It carries
		// no frame data of its own; the crash frame either side of it is
		// still printed explicitly, so this is simply consumed.
		return true
	}
	if reJSCause.MatchString(line) {
		return true
	}
	if strings.TrimSpace(line) == "}" {
		return true
	}
	if !jsSeenFrame(buf) {
		// Still in the header: an as-yet-unrecognized source-context
		// line, a numbered Bun code-frame continuation, or the "Error:
		// ..." line itself for a Node/Bun crash whose header started
		// with a bare code-frame intro.
		return true
	}
	return false
}

var jsRule = blockRule{kind: "js", start: jsBlockStart, cont: jsBlockContinues}

// parseJS turns a joined "js"-kind block into an Exception.
func parseJS(block Block) *Exception {
	buf := block.Lines
	if len(buf) == 0 {
		return nil
	}
	first := buf[0]

	fatal := false
	var headerRest []string // lines after the header, to search for the Type: message line
	switch {
	case reDenoCrash.MatchString(first):
		fatal = true
		msg := strings.TrimPrefix(first, "error: Uncaught ")
		headerRest = append([]string{msg}, buf[1:]...)
	case reNodeCodeFrame.MatchString(first):
		fatal = true
		headerRest = buf[1:]
	case reBunCodeFrame.MatchString(first):
		fatal = true
		headerRest = buf[1:]
	default:
		headerRest = buf
	}

	typ, val, msgLineIdxInRest := extractJSTypeValue(headerRest, fatal)
	if typ == "" && fatal {
		// Bun's crash printer has no "Type: message" line at all, only
		// its own "error: message" line.
		for i, l := range headerRest {
			if strings.HasPrefix(l, "error: ") {
				typ = "Error"
				val = strings.TrimPrefix(l, "error: ")
				msgLineIdxInRest = i
				break
			}
		}
	}

	var frames []Frame
	causeStart := -1
	for i := msgLineIdxInRest + 1; i < len(headerRest); i++ {
		l := headerRest[i]
		if m := reJSCause.FindStringSubmatch(l); m != nil {
			causeStart = i
			break
		}
		if f, ok := parseJSFrameLine(l); ok {
			frames = append(frames, f)
		}
	}
	reverseFrames(frames) // V8 prints innermost (crash) first.

	handled := boolPtr(!fatal)
	level := LevelError
	if fatal {
		level = LevelFatal
	}

	ex := &Exception{
		Runtime:   "", // the caller/CLI knows which runtime invoked it; Parse doesn't guess from process name
		Type:      typ,
		Value:     val,
		Parser:    "js",
		Handled:   handled,
		Frames:    frames,
		Level:     level,
		LineStart: block.LineStart,
		LineEnd:   block.LineEnd,
	}
	if ts, ok := parseTimestamp(first); ok {
		ex.ObservedAt = ts
	}

	if causeStart >= 0 {
		if cause := parseJSCause(headerRest[causeStart:]); cause != nil {
			ex.Chained = append(ex.Chained, *cause)
		}
	}
	return ex
}

// extractJSTypeValue finds the "Type: message" (or bare "Type") line inside
// lines and returns its parsed Type/Value plus its index. For a non-fatal
// (handled) block this is always lines[0]; for a fatal block it's whichever
// line first matches the pattern (the header before it holds the code-frame
// preamble).
func extractJSTypeValue(lines []string, fatal bool) (typ, val string, idx int) {
	for i, l := range lines {
		if reJSErrorLine.MatchString(l) {
			if at := strings.Index(l, ": "); at >= 0 {
				return l[:at], l[at+2:], i
			}
			return l, "", i
		}
		if reJSErrorLineOnlyType.MatchString(l) {
			return l, "", i
		}
		if !fatal {
			// Handled blocks always start with the message line; if the
			// very first line isn't recognized, there's nothing to find.
			break
		}
	}
	return "", "", -1
}

// parseJSCause parses a "[cause]: Error: message" line plus its nested
// frames (ending at a lone "}") into an Exception.
func parseJSCause(lines []string) *Exception {
	m := reJSCause.FindStringSubmatch(lines[0])
	if m == nil || m[1] == "" {
		return nil
	}
	rest := m[1]
	typ, val := rest, ""
	if at := strings.Index(rest, ": "); at >= 0 {
		typ, val = rest[:at], rest[at+2:]
	}
	var frames []Frame
	for _, l := range lines[1:] {
		if strings.TrimSpace(l) == "}" {
			break
		}
		if f, ok := parseJSFrameLine(l); ok {
			frames = append(frames, f)
		}
	}
	reverseFrames(frames)
	return &Exception{Type: typ, Value: val, Parser: "js", Frames: frames, Level: LevelError, Handled: boolPtr(true)}
}

// parseJSFrameLine parses one "    at Func (file:line:col)" (or the bare
// "    at file:line:col" anonymous form) line into a Frame.
func parseJSFrameLine(line string) (Frame, bool) {
	trimmed := strings.TrimSpace(line)
	if !strings.HasPrefix(trimmed, "at ") {
		return Frame{}, false
	}
	rest := strings.TrimPrefix(trimmed, "at ")

	var funcPart, locPart string
	if strings.HasSuffix(rest, ")") {
		depth := 0
		open := -1
		for i := len(rest) - 1; i >= 0; i-- {
			switch rest[i] {
			case ')':
				depth++
			case '(':
				depth--
				if depth == 0 {
					open = i
				}
			}
			if open != -1 {
				break
			}
		}
		if open >= 0 {
			funcPart = strings.TrimSpace(rest[:open])
			locPart = rest[open+1 : len(rest)-1]
		} else {
			locPart = rest
		}
	} else {
		locPart = rest
	}

	// Eval frames nest a location inside the func part, e.g.
	// "eval at <anonymous> (file:10:5), <anonymous>:3:9" as the loc; the
	// last comma-separated segment is the actual innermost location.
	effectiveLoc := locPart
	if i := strings.LastIndex(locPart, ", "); i >= 0 {
		if cand := locPart[i+2:]; reJSLoc.MatchString(cand) {
			effectiveLoc = cand
		}
	}

	file, ln, col := splitJSLoc(effectiveLoc)
	f := Frame{Function: funcPart, Filename: file, Lineno: ln, Colno: col}
	if file != "" && (strings.HasPrefix(file, "/") || strings.Contains(file, ":\\")) {
		f.AbsPath = file
	}
	return f, true
}

func splitJSLoc(loc string) (file string, line, col int) {
	loc = strings.TrimPrefix(loc, "file://")
	if u, err := url.PathUnescape(loc); err == nil {
		loc = u
	}
	if loc == "native" || loc == "<anonymous>" {
		return "", 0, 0
	}
	m := reJSLoc.FindStringSubmatch(loc)
	if m == nil {
		return loc, 0, 0
	}
	ln, _ := strconv.Atoi(m[2])
	col, _ = strconv.Atoi(m[3])
	return m[1], ln, col
}

func reverseFrames(f []Frame) {
	for i, j := 0, len(f)-1; i < j; i, j = i+1, j-1 {
		f[i], f[j] = f[j], f[i]
	}
}
