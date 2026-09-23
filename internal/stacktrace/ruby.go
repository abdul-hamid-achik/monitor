// ruby.go parses two shapes of Ruby 3.4 stderr output:
//
//   - A printed backtrace from a rescue clause (commonly `warn
//     e.backtrace.join("\n")` or `STDERR.puts e.backtrace`): a run of bare
//     "file:line:in 'method'" lines with no exception class or message at
//     all -- Ruby doesn't attach one unless the app chooses to print it, so
//     this shape is marked Handled (it was rescued) but its Type/Value stay
//     empty.
//   - Ruby's own uncaught-exception dump at process exit: a single
//     "file:line:in 'method': message (Type)" header line -- which doubles
//     as the crash frame -- followed by "\tfrom file:line:in 'method'"
//     lines for each caller.
//
// Both print the crash (innermost) frame first, so both are reversed to
// this package's oldest-first, crash-last convention.
package stacktrace

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reRubyHandledLine = regexp.MustCompile(`^(\S+):(\d+):in '([^']*)'$`)
	reRubyFatalHeader = regexp.MustCompile(`^(\S+):(\d+):in '([^']*)': (.+) \((\S+)\)$`)
	reRubyFatalFrame  = regexp.MustCompile(`^\tfrom (\S+):(\d+):in '([^']*)'$`)
)

func rubyHandledStart(line string) bool { return reRubyHandledLine.MatchString(line) }
func rubyHandledContinues(_ []string, line string) bool {
	return reRubyHandledLine.MatchString(line)
}

var rubyHandledRule = blockRule{kind: "ruby-handled", start: rubyHandledStart, cont: rubyHandledContinues}

func rubyFatalStart(line string) bool { return reRubyFatalHeader.MatchString(line) }
func rubyFatalContinues(_ []string, line string) bool {
	return reRubyFatalFrame.MatchString(line)
}

var rubyFatalRule = blockRule{kind: "ruby-fatal", start: rubyFatalStart, cont: rubyFatalContinues}

func rubyFrame(file string, line int, method string) Frame {
	f := Frame{Filename: file, Lineno: line, Function: method}
	if strings.HasPrefix(file, "/") {
		f.AbsPath = file
	}
	return f
}

func parseRubyHandled(block Block) *Exception {
	var frames []Frame
	for _, l := range block.Lines {
		m := reRubyHandledLine.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		ln, _ := strconv.Atoi(m[2])
		frames = append(frames, rubyFrame(m[1], ln, m[3]))
	}
	reverseFrames(frames)
	return &Exception{
		Runtime:   "ruby",
		Parser:    "ruby",
		Handled:   boolPtr(true),
		Level:     LevelError,
		Frames:    frames,
		LineStart: block.LineStart,
		LineEnd:   block.LineEnd,
	}
}

func parseRubyFatal(block Block) *Exception {
	if len(block.Lines) == 0 {
		return nil
	}
	header := reRubyFatalHeader.FindStringSubmatch(block.Lines[0])
	if header == nil {
		return nil
	}
	ln, _ := strconv.Atoi(header[2])
	frames := []Frame{rubyFrame(header[1], ln, header[3])}
	for _, l := range block.Lines[1:] {
		m := reRubyFatalFrame.FindStringSubmatch(l)
		if m == nil {
			continue
		}
		fln, _ := strconv.Atoi(m[2])
		frames = append(frames, rubyFrame(m[1], fln, m[3]))
	}
	reverseFrames(frames)
	ex := &Exception{
		Runtime:   "ruby",
		Type:      header[5],
		Value:     header[4],
		Parser:    "ruby",
		Handled:   boolPtr(false),
		Level:     LevelFatal,
		Frames:    frames,
		LineStart: block.LineStart,
		LineEnd:   block.LineEnd,
	}
	if ts, ok := parseTimestamp(block.Lines[0]); ok {
		ex.ObservedAt = ts
	}
	return ex
}
