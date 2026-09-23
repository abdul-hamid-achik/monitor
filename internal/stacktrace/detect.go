package stacktrace

import (
	"strings"
	"time"
)

// Parse turns a completed Block into an Exception, auto-selecting the
// parser by the kind the Joiner assigned when it opened the block. It
// returns nil for an empty block or one whose kind has no registered
// parser (which should not happen for any block the Joiner itself
// produced, since every registered blockRule has a matching parse
// function below).
func Parse(block Block) *Exception {
	if block.empty() {
		return nil
	}
	kind, ok := matchStart(block.Lines[0])
	if !ok {
		return nil
	}
	switch kind {
	case "js":
		return parseJS(block)
	case "tslog":
		return parseTslog(block)
	case "python":
		return parsePython(block)
	case "ruby-handled":
		return parseRubyHandled(block)
	case "ruby-fatal":
		return parseRubyFatal(block)
	case "gopanic":
		return parseGopanic(block)
	case "zap-console":
		return parseZapConsole(block)
	case "zap-json":
		return parseZapJSON(block)
	default:
		return nil
	}
}

// Detect runs the full text through a fresh Joiner and Parse, splitting it
// into blocks exactly as a batch (whole-file) read would: no idle timeout
// is ever reached, so blocks are separated solely by the Joiner's
// structural "a new block clearly starts" rule and the final Flush. A
// block no parser recognizes contributes no Exception (never a nil-ish
// placeholder), matching the "clean logs produce 0 events" requirement.
func Detect(text string) []*Exception {
	j := NewJoiner()
	var out []*Exception
	feed := func(b Block) {
		if b.empty() {
			return
		}
		if ex := Parse(b); ex != nil {
			out = append(out, ex)
		}
	}
	base := time.Unix(0, 0)
	for i, line := range strings.Split(StripANSI(text), "\n") {
		// A strictly increasing synthetic clock, one tick per line. Batch
		// detection never relies on Tick/idle timeouts, so its only job
		// is to give Feed a monotonic "now" for bookkeeping.
		feed(j.Feed(line, base.Add(time.Duration(i)*time.Millisecond)))
	}
	feed(j.Flush())
	return out
}
