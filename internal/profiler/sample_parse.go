package profiler

// This file has NO build tag: parseSampleTree is a pure text parser (no
// exec, no darwin-only syscalls), so its behavior is tested on every
// platform CI runs on (ubuntu and macOS). Only the actual `sample <pid>`
// subprocess invocation is darwin-only (sample_darwin.go / sample_other.go).

import (
	"bufio"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// sampleLineRe matches one macOS `sample` call-graph line:
//
//	<prefix><count>\s+<rest>
//
// prefix is zero or more of the tree-drawing characters `sample` uses to
// render nesting: ' ' (indentation), '+' (a new branch), '!' and ':' and '|'
// (continuation markers for branches with siblings below the current one at
// that column — dropped back to plain spaces once nothing later in the
// listing needs to connect through that column). Every depth level adds
// exactly two characters of prefix width, so len(prefix) is a monotonic,
// comparable proxy for tree depth; it is NOT a reliable way to tell a
// thread-root line from a nested one, because a nested frame that happens to
// be the last thing needing its column also gets an all-space prefix (see
// parseSampleTree's baseCol tracking instead).
var sampleLineRe = regexp.MustCompile(`^([ +!:|]*)(\d+)\s+(.*)$`)

// sampleFrameNameRe splits a frame's trailing text into the symbol name and
// its containing image, e.g. "runtime.retake  (in gowork) + 48  [0x...]" →
// ("runtime.retake", "gowork"). Frames sample can't symbolicate render as
// "???  (in <unknown binary>)  [0x...]" and still match (name "???").
var sampleFrameNameRe = regexp.MustCompile(`^(.*?)\s+\(in\s+([^)]*)\)`)

// idleLeafFuncs are macOS kernel/libsystem calls that mean "this thread was
// parked waiting for work, an event, or a lock" rather than doing anything
// the profiled program can fix. Bucketing them separately (Stats.IdlePct)
// keeps them from dominating the Symbol list ahead of a real, if smaller,
// CPU-bound hot line — the same problem V8's (idle) pseudo-frame caused
// before flattenCDPProfile started excluding it from the denominator.
var idleLeafFuncs = map[string]bool{
	"__psynch_cvwait":           true,
	"__psynch_cvsignal":         true,
	"__psynch_mutexwait":        true,
	"__psynch_mutexdrop":        true,
	"__psynch_rw_rdlock":        true,
	"__psynch_rw_wrlock":        true,
	"kevent":                    true,
	"kevent64":                  true,
	"kevent_qos":                true,
	"__semwait_signal":          true,
	"__semwait_signal_nocancel": true,
	"__workq_kernreturn":        true,
	"__select":                  true,
	"__pselect":                 true,
	"swtch_pri":                 true,
}

// isIdleLeafFunc reports whether fn is a known idle/blocking syscall leaf.
func isIdleLeafFunc(fn string) bool {
	if idleLeafFuncs[fn] {
		return true
	}
	return strings.HasPrefix(fn, "mach_msg")
}

// parseSampleLine parses one line of `sample` call-graph output. ok is
// false for any line that isn't a call-graph row (headers, the "Sort by top
// of stack" section, the Binary Images table, blank lines): all of those
// either don't start with tree-prefix characters immediately followed by a
// digit, or (for the by-stack-top section) have their count in the wrong
// position.
func parseSampleLine(line string) (prefix string, count int64, name, image string, ok bool) {
	m := sampleLineRe.FindStringSubmatch(line)
	if m == nil {
		return "", 0, "", "", false
	}
	n, err := strconv.ParseInt(m[2], 10, 64)
	if err != nil {
		return "", 0, "", "", false
	}
	rest := strings.TrimSpace(m[3])
	name = rest
	image = ""
	if fm := sampleFrameNameRe.FindStringSubmatch(rest); fm != nil {
		name = strings.TrimSpace(fm[1])
		image = strings.TrimSpace(fm[2])
	}
	return m[1], n, name, image, true
}

// sampleStackFrame is one still-open node while walking a thread's call
// tree depth-first.
type sampleStackFrame struct {
	col      int
	count    int64
	childSum int64
	name     string
	image    string
}

// aggKey identifies one function for self-time aggregation across every
// call path and every thread it appears in (macOS `sample` carries no
// file:line, only the containing image).
type aggKey struct {
	Func  string
	Image string
}

// parseSampleTree parses macOS `sample` call-graph text into a flat
// []Symbol ranked by aggregate self time, plus the idle/active breakdown.
//
// sample's output is a per-thread call tree rendered with '+'/'!'/':'/'|'
// tree-drawing prefixes (see sampleLineRe). Each node's own count already
// includes every descendant's samples, so the node's true self time is
// count minus the sum of its direct children's counts — exactly like a
// pprof flat/cum split. This walks every thread (the old regex-based parser
// only ever saw whichever frames happened to match a "starts with a plain
// digit" pattern, which real sample output never produces, and had no
// concept of self time at all), and buckets known idle/blocking syscall
// leaves (see idleLeafFuncs) out of the active total so they can never
// crowd out the real hot function the way (idle) used to in V8 profiles.
//
// Thread-root detection: a thread's root line ("    784 Thread_28989881 ...")
// always sits at the shallowest column in the whole "Call graph:" section —
// real call frames are always at least one depth level (two columns)
// deeper. A nested frame can ALSO have an all-space prefix (once nothing
// later in the listing needs to draw a connector through its column), so
// "no tree-drawing characters" is not a valid thread-root test; instead this
// tracks the column of the first call-graph line seen (baseCol) and treats
// any later line at that same column as a new thread's root.
func parseSampleTree(text string) ([]Symbol, Stats) {
	selfTotals := make(map[aggKey]int64)
	var order []aggKey
	var idleTotal, activeTotal, allTotal int64

	finalize := func(n *sampleStackFrame) {
		self := n.count - n.childSum
		if self < 0 {
			// Malformed/rounded input (sample occasionally under-counts a
			// child by a sample or two); never let a negative self distort
			// the totals.
			self = 0
		}
		if self == 0 {
			return
		}
		allTotal += self
		if isIdleLeafFunc(n.name) {
			idleTotal += self
			return
		}
		k := aggKey{Func: n.name, Image: n.image}
		if _, ok := selfTotals[k]; !ok {
			order = append(order, k)
		}
		selfTotals[k] += self
		activeTotal += self
	}

	var stack []*sampleStackFrame
	drain := func() {
		for len(stack) > 0 {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			finalize(top)
		}
	}
	popTo := func(col int) {
		for len(stack) > 0 && stack[len(stack)-1].col >= col {
			top := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			finalize(top)
		}
	}

	sc := bufio.NewScanner(strings.NewReader(text))
	// `sample` can emit very long frame lines (many collapsed PC offsets);
	// grow well past bufio.Scanner's 64KiB default so a long line is
	// skipped only for genuinely being longer than any real frame, not
	// silently truncating our tree.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	baseCol := -1
	for sc.Scan() {
		prefix, count, name, image, ok := parseSampleLine(sc.Text())
		if !ok {
			continue
		}
		col := len(prefix)
		if baseCol == -1 {
			baseCol = col
		}
		if col <= baseCol {
			// A new thread's tree starts fresh; close out everything left
			// over from the previous thread (or the "Call graph:" line
			// itself, which never reaches here since it has no leading
			// digit).
			drain()
			stack = append(stack, &sampleStackFrame{col: col, count: count, name: name, image: image})
			continue
		}
		popTo(col)
		if len(stack) > 0 {
			stack[len(stack)-1].childSum += count
		}
		stack = append(stack, &sampleStackFrame{col: col, count: count, name: name, image: image})
	}
	drain()

	if allTotal <= 0 {
		return nil, Stats{}
	}

	out := make([]Symbol, 0, len(order))
	for _, k := range order {
		self := selfTotals[k]
		if self <= 0 {
			continue
		}
		var weight float64
		if activeTotal > 0 {
			weight = float64(self) / float64(activeTotal) * 100
		}
		out = append(out, Symbol{Func: k.Func, File: k.Image, Weight: weight})
	}
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Weight != out[j].Weight {
			return out[i].Weight > out[j].Weight
		}
		if out[i].Func != out[j].Func {
			return out[i].Func < out[j].Func
		}
		return out[i].File < out[j].File
	})
	if len(out) > 50 {
		out = out[:50]
	}

	stats := Stats{
		Samples:       int(allTotal),
		ActiveSamples: int(activeTotal),
		IdlePct:       float64(idleTotal) / float64(allTotal) * 100,
	}
	return out, stats
}
