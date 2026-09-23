// zap.go parses zap's two built-in encoders plus the pkg/errors "%+v" trace
// apps commonly print (or attach as a "stacktrace" field) alongside a
// zap.Error(err) call:
//
//   - console encoder: "<RFC3339Nano ts>\t<level>\t<msg>\t<json fields>"
//   - JSON encoder: a single-line JSON object with at least "level",
//     "ts"/"time", and "msg" keys, and often a "stacktrace" field holding
//     the same pkg/errors-formatted text.
//
// A pkg/errors trace prints "<message>" then, for each frame (deepest/
// point-of-creation first): "<func>\n\t<file>:<line>", optionally followed
// by trailing "key: value" fields. This package neither imports zap nor
// pkg/errors (see examples/polyglot/go-zap-stdout); both formats are
// reproduced here from their well-known, stable text shapes.
package stacktrace

import (
	"encoding/json"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var (
	reZapConsoleTS = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:?\d{2})\t`)
	// debug/info are deliberately excluded: they're not exception-worthy,
	// and matching them would turn ordinary zap chatter into
	// false-positive events (breaking "clean logs -> 0 events").
	reZapLevelWord  = regexp.MustCompile(`^(warn|error|dpanic|panic|fatal)$`)
	rePkgErrorsFunc = regexp.MustCompile(`^[\w./*@-]+$`)
	rePkgErrorsFile = regexp.MustCompile(`^\t(\S+):(\d+)$`)
)

func zapConsoleStart(line string) bool {
	if !reZapConsoleTS.MatchString(line) {
		return false
	}
	parts := strings.SplitN(line, "\t", 4)
	return len(parts) >= 3 && reZapLevelWord.MatchString(parts[1])
}

func zapJSONStart(line string) bool {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "{") || !strings.HasSuffix(line, "}") {
		return false
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(line), &m); err != nil {
		return false
	}
	lvl, ok := m["level"].(string)
	return ok && reZapLevelWord.MatchString(lvl)
}

// zapContinues absorbs a trailing pkg/errors trace: any line up to the next
// blank line or a fresh zap-shaped line.
func zapContinues(_ []string, line string) bool {
	if strings.TrimSpace(line) == "" {
		return false
	}
	return !zapConsoleStart(line) && !zapJSONStart(line)
}

var zapConsoleRule = blockRule{kind: "zap-console", start: zapConsoleStart, cont: zapContinues}
var zapJSONRule = blockRule{kind: "zap-json", start: zapJSONStart, cont: zapContinues}

func parseZapConsole(block Block) *Exception {
	if len(block.Lines) == 0 {
		return nil
	}
	parts := strings.SplitN(block.Lines[0], "\t", 4)
	if len(parts) < 3 {
		return nil
	}
	ex := zapBase(parts[0], parts[1], parts[2])
	ex.LineStart, ex.LineEnd = block.LineStart, block.LineEnd
	applyPkgErrorsTrace(ex, block.Lines[1:])
	return ex
}

func parseZapJSON(block Block) *Exception {
	if len(block.Lines) == 0 {
		return nil
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(strings.TrimSpace(block.Lines[0])), &m); err != nil {
		return nil
	}
	ts, _ := m["ts"].(string)
	if ts == "" {
		ts, _ = m["time"].(string)
	}
	lvl, _ := m["level"].(string)
	msg, _ := m["msg"].(string)
	ex := zapBase(ts, lvl, msg)
	ex.LineStart, ex.LineEnd = block.LineStart, block.LineEnd
	if st, ok := m["stacktrace"].(string); ok && st != "" {
		applyPkgErrorsTrace(ex, strings.Split(st, "\n"))
	} else {
		applyPkgErrorsTrace(ex, block.Lines[1:])
	}
	return ex
}

func zapBase(ts, levelTok, msg string) *Exception {
	level, ok := levelFromLogPrefix(levelTok)
	if !ok {
		level = LevelError
	}
	ex := &Exception{
		Runtime: "go",
		Parser:  "zap",
		Value:   msg,
		Level:   level,
		Handled: boolPtr(level != LevelFatal),
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339} {
		if t, err := time.Parse(layout, ts); err == nil {
			ex.ObservedAt = t.UTC()
			break
		}
	}
	return ex
}

// applyPkgErrorsTrace parses a pkg/errors "%+v" dump (message line, then
// func/file:line frame pairs deepest-first, then optional trailing "key:
// value" fields) and attaches its frames to ex, oldest-first with the
// crash frame last.
func applyPkgErrorsTrace(ex *Exception, lines []string) {
	lines = trimBlankEdges(lines)
	if len(lines) == 0 {
		return
	}
	i := 0
	// An optional leading message line (pkg/errors repeats/derives its
	// own message here, which may differ from the zap msg field).
	if !rePkgErrorsFunc.MatchString(lines[0]) || (i+1 < len(lines) && !rePkgErrorsFile.MatchString(lines[i+1])) {
		i++
	}
	var frames []Frame
	for i < len(lines) {
		if !rePkgErrorsFunc.MatchString(lines[i]) {
			break
		}
		if i+1 >= len(lines) {
			break
		}
		m := rePkgErrorsFile.FindStringSubmatch(lines[i+1])
		if m == nil {
			break
		}
		f := Frame{Function: lines[i], Filename: m[1]}
		if strings.HasPrefix(m[1], "/") {
			f.AbsPath = m[1]
		}
		f.Lineno, _ = strconv.Atoi(m[2])
		frames = append(frames, f)
		i += 2
	}
	if len(frames) == 0 {
		return
	}
	reverseFrames(frames) // pkg/errors prints deepest (creation site) first.
	ex.Frames = frames
}

func trimBlankEdges(lines []string) []string {
	start, end := 0, len(lines)
	for start < end && strings.TrimSpace(lines[start]) == "" {
		start++
	}
	for end > start && strings.TrimSpace(lines[end-1]) == "" {
		end--
	}
	return lines[start:end]
}
