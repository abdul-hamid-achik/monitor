// ruby.go parses three shapes of Ruby (3.4, and the older backtick-quoted
// 3.3-) output:
//
//   - Ruby's own uncaught-exception dump at process exit: a
//     "file:line:in 'method': message (Type)" header line -- which doubles
//     as the crash frame -- optionally followed by Ruby 3.4's error_highlight
//     snippet (a blank line, the code line, a caret line), then
//     "\tfrom file:line:in 'method'" lines for each caller. Each cause is
//     printed right after as another header with its own from-lines, outer
//     to inner, and becomes a Chained entry.
//   - A printed backtrace from a rescue clause (`warn e.backtrace`): a run
//     of bare "file:line:in 'method'" lines with no class or message --
//     Handled, with empty Type/Value.
//   - The stdlib Logger: "E, [2026-09-22T10:04:37.123456 #123] ERROR -- :
//     message (Type)" followed by the backtrace logger.error(exception)
//     prints (handled, with Type/Value), or a message-only record.
//
// All three print the crash (innermost) frame first and are reversed to this
// package's oldest-first, crash-last convention.
package stacktrace

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reRubyHandledLine  = regexp.MustCompile("^(\\S+):(\\d+):in [`']([^']*)'$")
	reRubyFatalHeader  = regexp.MustCompile("^(\\S+):(\\d+):in [`']([^']*)': (.*) \\(([A-Z][\\w:]*)\\)$")
	reRubyFatalFrame   = regexp.MustCompile("^\\tfrom (\\S+):(\\d+):in [`']([^']*)'$")
	reRubyLevelsElided = regexp.MustCompile(`^\t? *\.\.\. \d+ levels\.\.\.$`)
	reRubyCaret        = regexp.MustCompile(`^\s*\^+\s*$`)
	reRubyLogger       = regexp.MustCompile(`^([DIWEFA]), \[(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?) #\d+\]\s+(DEBUG|INFO|WARN|ERROR|FATAL|ANY|UNKNOWN) -- ([^:]*): (.*)$`)
	reRubyMsgClass     = regexp.MustCompile(`^(.*) \(([A-Z][\w:]*)\)$`)
	// "ArgumentError: bad config" as an app prints "#{e.class}: #{e.message}".
	reRubyClassMsg = regexp.MustCompile(`^([A-Z][\w:]*(?:Error|Exception)): (.*)$`)
)

func rubyFrame(file string, line string, method string) Frame {
	ln, _ := strconv.Atoi(line)
	f := Frame{Filename: file, Lineno: ln, Function: method}
	if isAbsPath(file) {
		f.AbsPath = file
	}
	return f
}

// --- rescue-printed backtraces ---

var rubyHandledRule = blockRule{
	kind:  "ruby-handled",
	start: reRubyHandledLine.MatchString,
	open:  func(string) grammar { return rubyBacktraceGrammar{} },
}

type rubyBacktraceGrammar struct{}

func (rubyBacktraceGrammar) next(line string, boundary bool) verdict {
	if !boundary && (reRubyHandledLine.MatchString(line) || reRubyLevelsElided.MatchString(line)) {
		return vAccept
	}
	return vReject
}

func rubyBacktraceFrames(lines []string) []Frame {
	var frames []Frame
	for _, l := range lines {
		if m := reRubyHandledLine.FindStringSubmatch(l); m != nil {
			frames = append(frames, rubyFrame(m[1], m[2], m[3]))
		}
	}
	reverseFrames(frames)
	return frames
}

func parseRubyHandled(block Block) *Exception {
	frames := rubyBacktraceFrames(block.Lines)
	if len(frames) == 0 {
		return nil
	}
	ex := &Exception{
		Runtime:    "ruby",
		Parser:     "ruby",
		Handled:    boolPtr(true),
		Level:      LevelError,
		Frames:     frames,
		LineStart:  block.LineStart,
		LineEnd:    block.LineEnd,
		ObservedAt: blockTimestamp(block),
	}
	// `warn "#{e.class}: #{e.message}"; warn e.backtrace` prints the class
	// and message on the line just before the backtrace.
	if m := reRubyClassMsg.FindStringSubmatch(block.Prev); m != nil {
		ex.Type, ex.Value = m[1], m[2]
	}
	return ex
}

// --- uncaught dump ---

var rubyFatalRule = blockRule{
	kind:  "ruby-fatal",
	start: reRubyFatalHeader.MatchString,
	open:  func(string) grammar { return &rubyFatalGrammar{} },
}

// rubyFatalGrammar tracks whether the current header has any from-lines
// yet: error_highlight's snippet (blank, code, caret) may only sit between a
// header and its first from-line, and is tentative until that from-line
// confirms it.
type rubyFatalGrammar struct {
	sawFrom bool
	snippet int
}

func (g *rubyFatalGrammar) next(line string, boundary bool) verdict {
	switch {
	case reRubyFatalHeader.MatchString(line):
		// The next cause, printed right after the previous exception.
		g.sawFrom, g.snippet = false, 0
		return vAccept
	case reRubyFatalFrame.MatchString(line), reRubyLevelsElided.MatchString(line):
		g.sawFrom, g.snippet = true, 0
		return vAccept
	case boundary || g.sawFrom:
		return vReject
	}
	// error_highlight snippet before the first from-line.
	blank := strings.TrimSpace(line) == ""
	if g.snippet < 4 && (blank || isIndented(line) || reRubyCaret.MatchString(line)) {
		g.snippet++
		return vTentative
	}
	return vReject
}

func parseRubyFatal(block Block) *Exception {
	var chain []Exception
	for _, l := range block.Lines {
		if h := reRubyFatalHeader.FindStringSubmatch(l); h != nil {
			chain = append(chain, Exception{
				Type:   h[5],
				Value:  h[4],
				Frames: []Frame{rubyFrame(h[1], h[2], h[3])},
			})
			continue
		}
		if m := reRubyFatalFrame.FindStringSubmatch(l); m != nil && len(chain) > 0 {
			cur := &chain[len(chain)-1]
			cur.Frames = append(cur.Frames, rubyFrame(m[1], m[2], m[3]))
		}
	}
	if len(chain) == 0 {
		return nil
	}
	for i := range chain {
		reverseFrames(chain[i].Frames)
	}
	ex := &chain[0]
	ex.Runtime = "ruby"
	ex.Parser = "ruby"
	ex.Handled = boolPtr(false)
	ex.Level = LevelFatal
	ex.LineStart, ex.LineEnd = block.LineStart, block.LineEnd
	ex.ObservedAt = blockTimestamp(block)
	ex.Chained = chain[1:]
	if len(ex.Chained) == 0 {
		ex.Chained = nil
	}
	inheritChain(ex)
	return ex
}

// --- stdlib Logger ---

var rubyLoggerRule = blockRule{
	kind: "ruby-logger",
	start: func(line string) bool {
		m := reRubyLogger.FindStringSubmatch(line)
		if m == nil {
			return false
		}
		_, ok := levelFromLogPrefix(m[3])
		return ok
	},
	open: func(string) grammar { return rubyBacktraceGrammar{} },
}

func parseRubyLogger(block Block) *Exception {
	m := reRubyLogger.FindStringSubmatch(block.Lines[0])
	if m == nil {
		return nil
	}
	level, ok := levelFromLogPrefix(m[3])
	if !ok {
		return nil
	}
	ex := &Exception{
		Runtime:    "ruby",
		Value:      m[5],
		Parser:     "message",
		Handled:    boolPtr(true),
		Level:      level,
		LineStart:  block.LineStart,
		LineEnd:    block.LineEnd,
		ObservedAt: blockTimestamp(block),
	}
	if frames := rubyBacktraceFrames(block.Lines[1:]); len(frames) > 0 {
		ex.Parser = "ruby"
		ex.Frames = frames
		if mc := reRubyMsgClass.FindStringSubmatch(m[5]); mc != nil {
			ex.Value, ex.Type = mc[1], mc[2] // logger.error(e): "msg (Class)"
		} else if mc := reRubyClassMsg.FindStringSubmatch(m[5]); mc != nil {
			ex.Type, ex.Value = mc[1], mc[2] // "#{e.class}: #{e.message}"
		}
	}
	return ex
}
