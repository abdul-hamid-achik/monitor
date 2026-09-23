// gopanic.go parses Go's runtime crash dump:
//
//	panic: <value> [recovered]               <- zero or more re-panics:
//		panic: <value>                          "\tpanic: ..." lines
//	[signal SIGSEGV: segmentation violation code=... addr=0x0 pc=...]
//
//	goroutine 1 [running]:
//	example.com/app/internal/svc.(*Server).Handle(...)
//		/repo/internal/svc/svc.go:13
//	main.main()
//		/repo/main.go:63 +0x74
//	exit status 2                            <- added by `go run`
//
// or "fatal error: <message>" followed by the same stanzas. Only the first
// goroutine stanza (the one that panicked) becomes Frames; later stanzas (a
// deadlock report, GOTRACEBACK=all) are consumed as part of the block but
// not turned into frames. When a deferred function re-panics, the last panic
// value is the event's Value and the earlier ones become Chained entries
// (outer = the final panic, innermost = the first).
package stacktrace

import (
	"regexp"
	"strconv"
	"strings"
)

var (
	reGoPanicStart    = regexp.MustCompile(`^(?:panic|fatal error): `)
	reGoRepanic       = regexp.MustCompile(`^\tpanic: (.*)$`)
	reGoSignal        = regexp.MustCompile(`^\[signal .*\]$`)
	reGoroutineHeader = regexp.MustCompile(`^goroutine \d+(?: .*)? \[[^\]]*\]:$`)
	// "\t/path/file.go:13" or "\t/path/file.go:63 +0x74", optionally with
	// GOTRACEBACK=system's " fp=0x... sp=0x... pc=0x..." suffix.
	reGoFrameFile = regexp.MustCompile(`^\t(\S+):(\d+)(?: \+0x[0-9a-f]+)?(?: .*)?$`)
	// A frame's call line: the function (which may itself contain
	// parentheses, brackets and dots: "pkg.(*T).M", "pkg.Map[...]",
	// "main.main.func1") followed by exactly one argument list with no
	// nested parentheses ("(...)", "()", "({0x1?, 0x2?})").
	reGoFuncCallLine = regexp.MustCompile(`^(\S+)\([^()]*\)$`)
	reGoRecovered    = regexp.MustCompile(`\s*\[recovered(?:, repanicked)?\]$`)
)

// Go panic grammar states.
const (
	goStHeader = iota // panic value, re-panics, [signal ...]
	goStFrames        // goroutine stanzas
	goStDone          // after "exit status N"
)

type gopanicGrammar struct {
	st     int
	extra  int // tentative multi-line panic value lines
	blanks int
}

var gopanicRule = blockRule{
	kind:  "gopanic",
	start: reGoPanicStart.MatchString,
	open:  func(string) grammar { return &gopanicGrammar{} },
}

func (g *gopanicGrammar) next(line string, boundary bool) verdict {
	if boundary {
		return vReject
	}
	blank := strings.TrimSpace(line) == ""
	switch g.st {
	case goStHeader:
		switch {
		case reGoroutineHeader.MatchString(line):
			g.st = goStFrames
			return vAccept
		case reGoRepanic.MatchString(line), reGoSignal.MatchString(line):
			return vAccept
		case blank:
			return vTentative
		}
		// A multi-line panic value ("panic: line one\nline two").
		g.extra++
		if g.extra > 20 {
			return vReject
		}
		return vTentative
	case goStFrames:
		switch {
		case reGoroutineHeader.MatchString(line),
			reGoFuncCallLine.MatchString(line),
			reGoFrameFile.MatchString(line),
			strings.HasPrefix(line, "created by "),
			strings.HasPrefix(line, "...additional frames elided..."):
			g.blanks = 0
			return vAccept
		case strings.HasPrefix(line, "exit status "):
			g.st = goStDone
			return vAccept
		case blank:
			g.blanks++
			if g.blanks > 1 {
				return vReject
			}
			return vTentative
		}
		return vReject
	}
	return vReject
}

// goModule returns the package path of a Go function symbol:
// "example.com/app/internal/svc.(*Server).Handle" -> "example.com/app/internal/svc".
func goModule(fn string) string {
	slash := strings.LastIndex(fn, "/")
	if dot := strings.Index(fn[slash+1:], "."); dot >= 0 {
		return fn[:slash+1+dot]
	}
	return ""
}

func goFrame(fn, file, line string) Frame {
	f := Frame{Function: fn, Module: goModule(fn), Filename: file}
	if isAbsPath(file) {
		f.AbsPath = file
	}
	f.Lineno, _ = strconv.Atoi(line)
	return f
}

func parseGopanic(block Block) *Exception {
	lines := block.Lines
	typ := "panic"
	first := strings.TrimPrefix(lines[0], "panic: ")
	if strings.HasPrefix(lines[0], "fatal error: ") {
		typ = "fatal error"
		first = strings.TrimPrefix(lines[0], "fatal error: ")
	}
	values := []string{reGoRecovered.ReplaceAllString(first, "")}

	goroutineIdx := -1
	for i, l := range lines[1:] {
		if reGoroutineHeader.MatchString(l) {
			goroutineIdx = i + 1
			break
		}
		if m := reGoRepanic.FindStringSubmatch(l); m != nil {
			values = append(values, reGoRecovered.ReplaceAllString(m[1], ""))
		}
	}

	var frames []Frame
	if goroutineIdx >= 0 {
		for i := goroutineIdx + 1; i < len(lines); i++ {
			l := lines[i]
			if strings.TrimSpace(l) == "" || reGoroutineHeader.MatchString(l) {
				break
			}
			if strings.HasPrefix(l, "created by ") {
				// The spawn site, not part of the panicking stack.
				if i+1 < len(lines) && reGoFrameFile.MatchString(lines[i+1]) {
					i++
				}
				continue
			}
			m := reGoFuncCallLine.FindStringSubmatch(l)
			if m == nil {
				continue
			}
			f := Frame{Function: m[1], Module: goModule(m[1])}
			if i+1 < len(lines) {
				if fm := reGoFrameFile.FindStringSubmatch(lines[i+1]); fm != nil {
					f = goFrame(m[1], fm[1], fm[2])
					i++
				}
			}
			frames = append(frames, f)
		}
	}
	reverseFrames(frames)

	ex := &Exception{
		Runtime:    "go",
		Type:       typ,
		Value:      values[len(values)-1],
		Parser:     "gopanic",
		Handled:    boolPtr(false),
		Level:      LevelFatal,
		Frames:     frames,
		LineStart:  block.LineStart,
		LineEnd:    block.LineEnd,
		ObservedAt: blockTimestamp(block),
	}
	for i := len(values) - 2; i >= 0; i-- {
		ex.Chained = append(ex.Chained, Exception{Type: "panic", Value: values[i]})
	}
	inheritChain(ex)
	return ex
}
