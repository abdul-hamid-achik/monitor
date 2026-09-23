package stacktrace

import "time"

// Default Joiner limits, per the roadmap contract.
const (
	DefaultIdle     = 300 * time.Millisecond
	DefaultMaxLines = 400
	DefaultMaxBytes = 64 * 1024
)

// blockRule is how each parser file (js.go, python.go, ruby.go, gopanic.go,
// zap.go, tslog.go) tells the Joiner what its blocks look like, without the
// Joiner itself knowing anything about any particular exception grammar.
//
//   - start reports whether line begins a new block of this kind.
//   - cont reports whether line continues a block of this kind that has
//     already accumulated buf (buf never includes line itself). It is only
//     consulted while a block of this same kind is open.
type blockRule struct {
	kind  string
	start func(line string) bool
	cont  func(buf []string, line string) bool
}

// blockRules lists every recognized block grammar. Order matters only in
// that the first matching rule's kind wins when more than one start
// predicate matches the same line (none currently overlap).
var blockRules = []blockRule{
	jsRule,
	tslogRule,
	pythonRule,
	rubyHandledRule,
	rubyFatalRule,
	gopanicRule,
	zapConsoleRule,
	zapJSONRule,
}

func matchStart(line string) (kind string, ok bool) {
	for _, r := range blockRules {
		if r.start(line) {
			return r.kind, true
		}
	}
	return "", false
}

func ruleByKind(kind string) (blockRule, bool) {
	for _, r := range blockRules {
		if r.kind == kind {
			return r, true
		}
	}
	return blockRule{}, false
}

// Block is a completed, joined group of lines the Joiner believes make up
// one exception (or one message), ready for Parse.
type Block struct {
	Lines     []string
	LineStart int // 1-based, inclusive
	LineEnd   int // 1-based, inclusive
}

func (b Block) empty() bool { return len(b.Lines) == 0 }

// Text joins the block's lines back into the text Parse expects.
func (b Block) Text() string {
	s := ""
	for i, l := range b.Lines {
		if i > 0 {
			s += "\n"
		}
		s += l
	}
	return s
}

// Joiner groups the lines of a single stream (e.g. one process's stderr)
// into candidate exception blocks. It never sleeps or reads a clock itself:
// callers drive it with Feed/Tick, passing "now" explicitly, which makes it
// fully deterministic in tests and usable both for a live tail (real time
// between calls) and a batch replay of a whole file (a single synthetic
// time, relying on Flush at EOF).
//
// A block is completed and returned when:
//   - Tick observes at least Idle since the last Feed with a non-empty
//     buffer ("idle timeout"), or
//   - Feed sees a line that clearly starts a new block while one is
//     already open ("a new block clearly starts"), or
//   - appending the incoming line would exceed MaxLines/MaxBytes (the
//     block is completed as-is, and the line that triggered the cap is
//     considered fresh input, exactly as if it had arrived after a Flush).
//
// A stray line that neither continues the open block nor starts a new one
// is dropped (not buffered): it is ordinary program output between two
// exceptions, not part of either.
type Joiner struct {
	Idle     time.Duration
	MaxLines int
	MaxBytes int

	kind      string
	buf       []string
	bufBytes  int
	lineStart int
	lineNo    int // running count of lines fed, for lineStart bookkeeping
	lineEnd   int // line number of the last line actually appended to buf
	lastFeed  time.Time
	hasLast   bool
}

// NewJoiner returns a Joiner with the default idle timeout and caps.
func NewJoiner() *Joiner {
	return &Joiner{Idle: DefaultIdle, MaxLines: DefaultMaxLines, MaxBytes: DefaultMaxBytes}
}

func (j *Joiner) reset() {
	j.kind = ""
	j.buf = nil
	j.bufBytes = 0
	j.lineStart = 0
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

func (j *Joiner) completed() Block {
	b := Block{Lines: j.buf, LineStart: j.lineStart, LineEnd: j.lineEnd}
	j.reset()
	return b
}

// Feed presents the next line of the stream to the Joiner. now is used only
// to record when the next Tick should consider the buffer idle; Feed itself
// never flushes on time, only on structure or on the size cap. It returns a
// completed Block when feeding line closed one out (either because line
// started a new block while one was open, or because appending it would
// exceed the cap); the zero Block (empty Lines) otherwise.
func (j *Joiner) Feed(line string, now time.Time) Block {
	j.lineNo++
	j.lastFeed = now
	j.hasLast = true

	if len(j.buf) == 0 {
		if kind, ok := matchStart(line); ok {
			j.kind = kind
			j.lineStart = j.lineNo
			j.appendLocked(line)
		}
		// A line that starts nothing is ordinary output; drop it.
		return Block{}
	}

	rule, ok := ruleByKind(j.kind)
	if ok && rule.cont(j.buf, line) {
		if j.wouldExceedCap(line) {
			out := j.completed()
			// The line that triggered the cap is fresh input: replay the
			// same decision Feed would make on an empty buffer.
			if kind, ok := matchStart(line); ok {
				j.kind = kind
				j.lineStart = j.lineNo
				j.appendLocked(line)
			}
			return out
		}
		j.appendLocked(line)
		return Block{}
	}

	// Not a continuation of the open block. Either a new block clearly
	// starts here, or this line is unrelated noise between two blocks.
	if kind, ok := matchStart(line); ok {
		out := j.completed()
		j.kind = kind
		j.lineStart = j.lineNo
		j.appendLocked(line)
		return out
	}
	// Unrelated line: the open block is finished, and this line is
	// dropped (it belongs to neither the block that just ended nor any
	// recognized block of its own).
	return j.completed()
}

func (j *Joiner) wouldExceedCap(line string) bool {
	return len(j.buf)+1 > j.maxLines() || j.bufBytes+len(line)+1 > j.maxBytes()
}

func (j *Joiner) appendLocked(line string) {
	j.buf = append(j.buf, line)
	j.bufBytes += len(line) + 1
	j.lineEnd = j.lineNo
}

// Tick lets a live caller flush a block after Idle has passed with no new
// Feed calls. Batch callers (reading a whole file at once) can skip Tick
// entirely and rely on a final Flush.
func (j *Joiner) Tick(now time.Time) Block {
	if len(j.buf) == 0 || !j.hasLast {
		return Block{}
	}
	if now.Sub(j.lastFeed) < j.idle() {
		return Block{}
	}
	return j.completed()
}

// Flush force-closes whatever is currently buffered (used at EOF, or when
// shutting a live stream down). Returns the zero Block if nothing was open.
func (j *Joiner) Flush() Block {
	if len(j.buf) == 0 {
		return Block{}
	}
	return j.completed()
}
