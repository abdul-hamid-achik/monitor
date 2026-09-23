package stacktrace

import (
	"strings"
	"time"
)

// Parse turns a completed Block into an Exception using the parser of the
// grammar that opened it (Block.Kind, or a re-match of the first line for a
// hand-built block). It returns nil for an empty block and for one that
// holds nothing recognizable: a JS header with no frames and no crash
// signal, a code-frame-looking table, a traceback with neither frames nor a
// closing line.
func Parse(block Block) *Exception {
	if block.empty() {
		return nil
	}
	kind := block.Kind
	if kind == "" {
		r, ok := matchStart(block.Lines[0])
		if !ok {
			return nil
		}
		kind = r.kind
	} else if _, ok := ruleByKind(kind); !ok {
		return nil
	}
	switch kind {
	case "js":
		return parseJS(block)
	case "tslog":
		return parseTslog(block)
	case "python":
		return parsePython(block)
	case "python-syntax":
		return parsePythonSyntax(block)
	case "ruby-handled":
		return parseRubyHandled(block)
	case "ruby-fatal":
		return parseRubyFatal(block)
	case "ruby-logger":
		return parseRubyLogger(block)
	case "gopanic":
		return parseGopanic(block)
	case "go-goroutine":
		return parseGoroutineDump(block)
	case "zap-console":
		return parseZapConsole(block)
	case "zap-json":
		return parseZapJSON(block)
	}
	return nil
}

// Detect runs the full text through a fresh Joiner and Parse, splitting it
// into blocks exactly as a batch (whole-file) read would: a synthetic clock
// one millisecond per line never reaches the idle timeout, so blocks are
// separated by structure alone and the final Flush. A block no parser
// recognizes contributes nothing. Zone-less timestamps are read in
// time.Local; use a Joiner with Location set to choose another zone.
func Detect(text string) []*Exception {
	return detectWith(NewJoiner(), text)
}

func detectWith(j *Joiner, text string) []*Exception {
	var out []*Exception
	add := func(bs []Block) {
		for _, b := range bs {
			if ex := Parse(b); ex != nil {
				out = append(out, ex)
			}
		}
	}
	base := time.Unix(0, 0)
	for i, line := range strings.Split(text, "\n") {
		add(j.Feed(line, base.Add(time.Duration(i)*time.Millisecond)))
	}
	add(j.Flush())
	return out
}
