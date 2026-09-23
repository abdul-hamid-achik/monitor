// tslog.go parses a typescript-logging-style structured log line and its
// "at fn()@file:line:col" frames, plus a Temporal ApplicationFailure-like
// "Caused by:" chain. Both shapes are synthetic (there is no single de
// facto text format for either), fixed here so the parser and the fixture
// it reads agree:
//
//	<RFC3339Nano ts> (ERROR|WARN|FATAL|INFO|DEBUG) [<logger>] <Type>: <message>
//	    at <func>() @ <file>:<line>:<col>
//	Caused by: <Type>: <message>
//	    at <func>() @ <file>:<line>:<col>
//
// Unlike Node's nested "[cause]:", a "Caused by:" section here is printed
// physically AFTER the outer exception's own frames, so it is already in
// this package's outer-to-inner Chained order and needs no reversal.
package stacktrace

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	// INFO and DEBUG never start a block: they're not exception-worthy,
	// and matching them would turn ordinary startup chatter into
	// false-positive events.
	reTsLogHeader = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T[\d:.]+Z\s+(ERROR|WARN|FATAL)\s+\[[^\]]+\]\s+.*$`)
	reTsLogFrame  = regexp.MustCompile(`^\s+at\s+(.+?)\(\)\s*@\s*(.+):(\d+):(\d+)$`)
	reTsLogCause  = regexp.MustCompile(`^Caused by:\s*(.*)$`)
)

func tslogContinues(_ []string, line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	return reTsLogFrame.MatchString(line) || reTsLogCause.MatchString(line)
}

var tslogRule = blockRule{kind: "tslog", start: reTsLogHeader.MatchString, cont: tslogContinues}

// tslogTypeValue splits "Type: message" as printed after the level/logger
// prefix (or after "Caused by:"); the message is allowed to contain ": "
// itself, so only the first occurrence is treated as the separator.
func tslogTypeValue(s string) (typ, val string) {
	if at := strings.Index(s, ": "); at >= 0 {
		return s[:at], s[at+2:]
	}
	return s, ""
}

func tslogFrame(m []string) Frame {
	ln, _ := strconv.Atoi(m[3])
	col, _ := strconv.Atoi(m[4])
	f := Frame{Function: m[1], Filename: m[2], Lineno: ln, Colno: col}
	if strings.HasPrefix(m[2], "/") {
		f.AbsPath = m[2]
	}
	return f
}

func parseTslog(block Block) *Exception {
	lines := block.Lines
	if len(lines) == 0 {
		return nil
	}
	header := reTsLogHeader.FindStringSubmatch(lines[0])
	if header == nil {
		return nil
	}
	levelTok := header[1]
	level, ok := levelFromLogPrefix(levelTok)
	if !ok {
		level = LevelError
	}
	// Everything after "[logger] " is "Type: message".
	rest := lines[0]
	if idx := strings.Index(rest, "] "); idx >= 0 {
		rest = rest[idx+2:]
	}
	typ, val := tslogTypeValue(rest)

	ex := &Exception{
		Runtime:   "node",
		Type:      typ,
		Value:     val,
		Parser:    "tslog",
		Handled:   boolPtr(level != LevelFatal),
		Level:     level,
		LineStart: block.LineStart,
		LineEnd:   block.LineEnd,
	}
	if ts, ok := parseTimestamp(lines[0]); ok {
		ex.ObservedAt = ts
	}

	var frames []Frame
	i := 1
	for ; i < len(lines); i++ {
		m := reTsLogFrame.FindStringSubmatch(lines[i])
		if m == nil {
			break
		}
		frames = append(frames, tslogFrame(m))
	}
	ex.Frames = frames // already oldest -> newest as printed; no reversal.

	for i < len(lines) {
		m := reTsLogCause.FindStringSubmatch(lines[i])
		if m == nil {
			i++
			continue
		}
		i++
		ctyp, cval := tslogTypeValue(m[1])
		var cframes []Frame
		for ; i < len(lines); i++ {
			fm := reTsLogFrame.FindStringSubmatch(lines[i])
			if fm == nil {
				break
			}
			cframes = append(cframes, tslogFrame(fm))
		}
		ex.Chained = append(ex.Chained, Exception{
			Runtime: "node", Type: ctyp, Value: cval, Parser: "tslog",
			Handled: boolPtr(true), Level: LevelError, Frames: cframes,
		})
	}
	return ex
}
