package stacktrace

import (
	"strings"
	"time"
	"unicode/utf8"
)

// Default Joiner limits, per the roadmap contract.
const (
	DefaultIdle     = 300 * time.Millisecond
	DefaultMaxLines = 400
	DefaultMaxBytes = 64 * 1024
)

// maxSkips bounds how many consecutive interleaved noise lines a grammar may
// skip (vSkip) before the open block is closed anyway.
const maxSkips = 2

// verdict is a grammar's decision about the next line of an open block.
type verdict uint8

const (
	// vReject: the line is not part of the block. The block closes (with
	// its tentative lines un-committed) and the tentative lines plus this
	// one are re-read as fresh input.
	vReject verdict = iota
	// vAccept: the line is part of the block; it also confirms every
	// tentative line before it.
	vAccept
	// vTentative: the line may be part of the block (a message
	// continuation, a blank line before a footer, a pkg/errors message
	// line). It is committed by a later vAccept, or re-read as fresh input
	// if the block closes first -- so a real exception hiding behind a
	// tentative line is never swallowed.
	vTentative
	// vSkip: interleaved noise inside a block that is still expecting
	// more (e.g. another stream's log line between a Python traceback's
	// frames and its closing line). The line is dropped, the block stays
	// open; at most maxSkips in a row.
	vSkip
)

// grammar is the continuation state machine one block kind uses once a block
// of that kind is open. It sees every line after the first, in order,
// including lines it answered vTentative for.
type grammar interface {
	// next classifies line. boundary reports whether line is a hard block
	// boundary (see isBoundary): a grammar must answer vReject for a
	// boundary line unless it explicitly owns it (a chained Python
	// "Traceback" after a connector sentence, a Ruby cause header).
	next(line string, boundary bool) verdict
}

// blockRule is how each parser file tells the Joiner what its blocks look
// like, without the Joiner itself knowing any exception grammar.
type blockRule struct {
	kind  string
	start func(line string) bool
	open  func(first string) grammar
}

// blockRules lists every recognized block grammar. The first rule whose
// start predicate matches wins, so the more specific shapes come first and
// the loosest one (a JS "Type: message" header, which only yields an event
// when frames follow) comes last.
var blockRules = []blockRule{
	pythonRule,
	pythonSyntaxRule,
	pythonGroupRule,
	gopanicRule,
	zapConsoleRule,
	zapJSONRule,
	rubyLoggerRule,
	rubyFatalRule,
	rubyHandledRule,
	tslogRule,
	jsRule,
}

func matchStart(line string) (blockRule, bool) {
	for _, r := range blockRules {
		if r.start(line) {
			return r, true
		}
	}
	return blockRule{}, false
}

func ruleByKind(kind string) (blockRule, bool) {
	for _, r := range blockRules {
		if r.kind == kind {
			return r, true
		}
	}
	return blockRule{}, false
}

// isBoundary reports whether line unmistakably starts a new record (a new
// exception or a new structured log entry of any level), so it must close
// whatever block is open unless that block's grammar owns it. This is what
// keeps a zap error line from swallowing a following Go panic, a JS header
// from swallowing a Python traceback, and so on.
func isBoundary(line string) bool {
	switch {
	case line == "":
		return false
	case reTracebackHeader.MatchString(line), rePyGroupHeader.MatchString(line):
		return true
	case reLoggingAnyLevel.MatchString(line):
		return true
	case reGoPanicStart.MatchString(line):
		return true
	case zapConsoleLevel(line) != "":
		return true
	case zapJSONLevel(line) != "":
		return true
	case reRubyLogger.MatchString(line), reRubyFatalHeader.MatchString(line):
		return true
	case reTsLogHeader.MatchString(line):
		return true
	case reDenoUncaught.MatchString(line), reNodeCodeFrame.MatchString(line):
		return true
	}
	return false
}

// Block is a completed, joined group of lines the Joiner believes make up
// one exception (or one log message), ready for Parse.
type Block struct {
	// Kind is the grammar that opened the block ("js", "python",
	// "gopanic", ...). Parse falls back to re-matching Lines[0] when it is
	// empty (hand-built blocks in tests).
	Kind string
	// Lines are the block's lines, CR- and ANSI-stripped. Interleaved
	// noise lines a grammar skipped are not included, so len(Lines) can be
	// smaller than LineEnd-LineStart+1.
	Lines     []string
	LineStart int // 1-based, inclusive
	LineEnd   int // 1-based, inclusive
	// Prev is the line immediately before LineStart when that line
	// belonged to no block (typically the logger line that introduced
	// the trace, e.g. "2026-09-22 10:04:37,123 ERROR app: request
	// failed"); "" otherwise. Parsers read ObservedAt and, for Python,
	// the handled signal from it.
	Prev string
	// Location is the zone used for zone-less timestamps (Python
	// asctime, Ruby Logger, Go's log package); nil means time.Local.
	Location *time.Location
}

func (b Block) empty() bool { return len(b.Lines) == 0 }

// Text joins the block's lines back into the text Parse expects.
func (b Block) Text() string { return strings.Join(b.Lines, "\n") }

// Joiner groups the lines of a single stream (e.g. one process's stderr)
// into candidate exception blocks. It never sleeps or reads a clock itself:
// callers drive it with Feed/Tick, passing "now" explicitly, which makes it
// deterministic in tests and usable both for a live tail (real time between
// calls) and a batch replay of a whole file (a synthetic clock plus Flush at
// EOF).
//
// A block is completed and returned when:
//   - at least Idle passed since the previous line: checked by Tick, and
//     by Feed itself before it looks at the new line, so a live caller
//     that never ticks still splits blocks correctly;
//   - Feed sees a boundary line (a new Traceback, "panic:", a zap or
//     other structured log line of any level, a Ruby crash header, a
//     Deno/Node crash header, ...) the open grammar does not own;
//   - the open grammar rejects the line (it neither continues the block
//     nor could plausibly continue it);
//   - appending the line would exceed MaxLines/MaxBytes.
//
// Lines a grammar only accepted tentatively are re-read as fresh input when
// the block closes before confirming them, and a line that neither
// continues the open block nor starts a new one is dropped (it is ordinary
// program output between two exceptions), but remembered as the next
// block's Prev.
type Joiner struct {
	Idle     time.Duration
	MaxLines int
	MaxBytes int
	// Location is stamped on every Block for zone-less timestamps; nil
	// means time.Local.
	Location *time.Location

	open      bool
	kind      string
	g         grammar
	lines     []string
	pending   []queued
	bytes     int // committed + pending bytes, one newline per line
	skips     int
	lineStart int
	lineEnd   int
	blockPrev string

	prev   string
	prevNo int

	lineNo   int
	lastFeed time.Time
	hasLast  bool
}

// queued is a line waiting to be (re)processed, with its stream line number.
type queued struct {
	line string
	no   int
}

// NewJoiner returns a Joiner with the default idle timeout and caps.
func NewJoiner() *Joiner {
	return &Joiner{Idle: DefaultIdle, MaxLines: DefaultMaxLines, MaxBytes: DefaultMaxBytes}
}

func (j *Joiner) idle() time.Duration {
	if j.Idle > 0 {
		return j.Idle
	}
	return DefaultIdle
}

func (j *Joiner) maxLines() int {
	if j.MaxLines > 0 {
		return j.MaxLines
	}
	return DefaultMaxLines
}

func (j *Joiner) maxBytes() int {
	if j.MaxBytes > 0 {
		return j.MaxBytes
	}
	return DefaultMaxBytes
}

// clean normalizes one input line: it drops a trailing CR (CRLF input from
// Windows tools or TTY-attached containers), strips ANSI escapes, and
// truncates the line so that it alone always fits under MaxBytes.
func (j *Joiner) clean(line string) string {
	line = strings.TrimRight(line, "\r")
	line = StripANSI(line)
	return truncateUTF8(line, j.maxBytes()-1)
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune.
func truncateUTF8(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

// Feed presents the next line of the stream to the Joiner and returns the
// blocks that feeding it completed (usually none; at most a few when an idle
// gap and a structural close coincide).
func (j *Joiner) Feed(line string, now time.Time) []Block {
	line = j.clean(line)
	var out []Block
	if j.open && j.hasLast && now.Sub(j.lastFeed) >= j.idle() {
		out = j.flushInto(out)
	}
	j.lastFeed, j.hasLast = now, true
	j.lineNo++
	return j.process(out, []queued{{line: line, no: j.lineNo}})
}

// Tick lets a live caller flush a block after Idle has passed with no new
// Feed calls. Batch callers (reading a whole file at once) can skip Tick and
// rely on the final Flush.
func (j *Joiner) Tick(now time.Time) []Block {
	if !j.open || !j.hasLast || now.Sub(j.lastFeed) < j.idle() {
		return nil
	}
	return j.flushInto(nil)
}

// Flush force-closes whatever is buffered (at EOF, or when shutting a live
// stream down), re-reading any tentative lines so none is lost.
func (j *Joiner) Flush() []Block { return j.flushInto(nil) }

func (j *Joiner) flushInto(out []Block) []Block {
	for j.open {
		b, pend := j.close()
		out = appendBlock(out, b)
		out = j.process(out, pend)
	}
	return out
}

func appendBlock(out []Block, b Block) []Block {
	if b.empty() {
		return out
	}
	return append(out, b)
}

// process runs queue through the open grammar (or the start rules when no
// block is open). A rejection closes the block and puts its tentative lines
// back at the front of the queue, ahead of the line that caused it.
func (j *Joiner) process(out []Block, queue []queued) []Block {
	for len(queue) > 0 {
		q := queue[0]
		queue = queue[1:]
		if !j.open {
			j.tryStart(q)
			continue
		}
		if len(j.lines)+len(j.pending)+1 > j.maxLines() || j.bytes+len(q.line)+1 > j.maxBytes() {
			b, pend := j.close()
			out = appendBlock(out, b)
			queue = requeue(pend, q, queue)
			continue
		}
		v := j.g.next(q.line, isBoundary(q.line))
		if v == vSkip {
			j.skips++
			if j.skips > maxSkips {
				v = vReject
			}
		}
		switch v {
		case vAccept:
			for _, p := range j.pending {
				j.lines = append(j.lines, p.line)
			}
			j.pending = j.pending[:0]
			j.lines = append(j.lines, q.line)
			j.bytes += len(q.line) + 1
			j.lineEnd = q.no
			j.skips = 0
		case vTentative:
			j.pending = append(j.pending, q)
			j.bytes += len(q.line) + 1
		case vSkip:
			// Interleaved noise: dropped, the block stays open.
		default:
			b, pend := j.close()
			out = appendBlock(out, b)
			queue = requeue(pend, q, queue)
		}
	}
	return out
}

func requeue(pend []queued, q queued, rest []queued) []queued {
	out := make([]queued, 0, len(pend)+1+len(rest))
	out = append(out, pend...)
	out = append(out, q)
	return append(out, rest...)
}

func (j *Joiner) tryStart(q queued) {
	r, ok := matchStart(q.line)
	if !ok {
		j.prev, j.prevNo = q.line, q.no
		return
	}
	j.open = true
	j.kind = r.kind
	j.g = r.open(q.line)
	j.lines = []string{q.line}
	j.pending = nil
	j.bytes = len(q.line) + 1
	j.skips = 0
	j.lineStart, j.lineEnd = q.no, q.no
	j.blockPrev = ""
	if j.prevNo > 0 && j.prevNo == q.no-1 {
		j.blockPrev = j.prev
	}
}

// close completes the open block with its committed lines and returns the
// tentative lines that were never confirmed, for the caller to re-read.
func (j *Joiner) close() (Block, []queued) {
	b := Block{
		Kind:      j.kind,
		Lines:     j.lines,
		LineStart: j.lineStart,
		LineEnd:   j.lineEnd,
		Prev:      j.blockPrev,
		Location:  j.Location,
	}
	pend := append([]queued(nil), j.pending...)
	j.open = false
	j.kind = ""
	j.g = nil
	j.lines = nil
	j.pending = nil
	j.bytes = 0
	j.skips = 0
	j.lineStart, j.lineEnd = 0, 0
	j.blockPrev = ""
	return b, pend
}
