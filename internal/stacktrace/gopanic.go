// gopanic.go parses Go's runtime crash dump: "panic: message" or "fatal
// error: message", a blank line, then one or more "goroutine N [state]:"
// stanzas, each a list of (function-call line, tab-indented "file:line
// [+0xOFFSET]" line) frame pairs. Per the roadmap, only the first goroutine
// stanza is used for the culprit -- later stanzas (other goroutines dumped
// alongside a fatal error, e.g. a deadlock report) are consumed as part of
// the block but not turned into Frames.
package stacktrace

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reGoPanicStart    = regexp.MustCompile(`^(?:panic: .*|fatal error: .*)$`)
	reGoroutineHeader = regexp.MustCompile(`^goroutine \d+ \[[^\]]*\]:$`)
	reGoFrameFile     = regexp.MustCompile(`^\t(\S+):(\d+)(?: \+0x[0-9a-f]+)?$`)
	reGoFuncCallLine  = regexp.MustCompile(`^\S.*\)$`)
)

func gopanicHasFrame(buf []string) bool {
	for _, l := range buf {
		if reGoFrameFile.MatchString(l) {
			return true
		}
	}
	return false
}

func gopanicContinues(buf []string, line string) bool {
	if strings.TrimSpace(line) == "" {
		return true
	}
	if reGoroutineHeader.MatchString(line) {
		return true
	}
	if reGoFrameFile.MatchString(line) {
		return true
	}
	if strings.HasPrefix(line, "created by ") {
		return true
	}
	if strings.HasPrefix(line, "exit status ") {
		return true
	}
	if reGoPanicStart.MatchString(line) {
		// A second, unrelated panic header after we already collected a
		// full first stanza starts a new block; otherwise (e.g. a
		// two-line fatal-error message) it's still this one.
		return !gopanicHasFrame(buf)
	}
	return reGoFuncCallLine.MatchString(line)
}

var gopanicRule = blockRule{kind: "gopanic", start: reGoPanicStart.MatchString, cont: gopanicContinues}

func parseGopanic(block Block) *Exception {
	lines := block.Lines
	if len(lines) == 0 {
		return nil
	}
	typ, val := "panic", strings.TrimPrefix(lines[0], "panic: ")
	if strings.HasPrefix(lines[0], "fatal error: ") {
		typ = "fatal error"
		val = strings.TrimPrefix(lines[0], "fatal error: ")
	}

	goroutineIdx := -1
	for i, l := range lines {
		if reGoroutineHeader.MatchString(l) {
			goroutineIdx = i
			break
		}
	}

	var frames []Frame
	if goroutineIdx >= 0 {
		i := goroutineIdx + 1
		for i < len(lines) {
			l := lines[i]
			if strings.TrimSpace(l) == "" || reGoroutineHeader.MatchString(l) {
				break
			}
			if strings.HasPrefix(l, "created by ") {
				i++
				if i < len(lines) && reGoFrameFile.MatchString(lines[i]) {
					i++
				}
				continue
			}
			if !reGoFuncCallLine.MatchString(l) {
				break
			}
			funcName := l
			if idx := strings.Index(l, "("); idx >= 0 {
				funcName = l[:idx]
			}
			f := Frame{Function: funcName}
			i++
			if i < len(lines) {
				if m := reGoFrameFile.FindStringSubmatch(lines[i]); m != nil {
					f.Filename = m[1]
					if strings.HasPrefix(m[1], "/") {
						f.AbsPath = m[1]
					}
					f.Lineno, _ = strconv.Atoi(m[2])
					i++
				}
			}
			frames = append(frames, f)
		}
	}
	reverseFrames(frames)

	return &Exception{
		Runtime:   "go",
		Type:      typ,
		Value:     val,
		Parser:    "gopanic",
		Handled:   boolPtr(false),
		Level:     LevelFatal,
		Frames:    frames,
		LineStart: block.LineStart,
		LineEnd:   block.LineEnd,
	}
}
