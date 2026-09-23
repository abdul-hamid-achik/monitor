// js.go parses V8/JSC-family stack traces as Node 26, Deno 2.9 and Bun 1.4
// print them (see testdata/real/{node,deno,bun}):
//
//   - a caught-and-printed error: "TypeError [ERR_X]: msg" (or a prefixed
//     console.error("request failed:", err) line, or a process warning
//     "(node:123) DeprecationWarning: ...") followed by "    at ..." frames,
//     util.inspect's "{ code: ..., [cause]: ... }" property blocks (nested to
//     any depth), and "... N lines matching cause stack trace ..." markers;
//   - Node's uncaught-exception printer: a "path:N" code-frame intro
//     ("/abs/x.js:12", "file:///x.mjs:3", "node:internal/...:107",
//     "[eval]:1"), the source line and caret, the error, and the "Node.js
//     vX" footer;
//   - Deno's "error: Uncaught (in promise) Type: msg" printer with its
//     source/caret lines and "Caused by: ..." sections;
//   - Bun's numbered code-frame printer ("49 |   throw ..."), one block per
//     cause, with the "Bun vX" footer when uncaught;
//   - Deno/Bun CLI errors ("error: Cannot find module ...") when frames or a
//     runtime footer back them up.
//
// A block that only looked like one of these -- a lone "Error: x" line, a
// "1 | alice | admin" table row, a rustc diagnostic -- has no frames and no
// crash signal, and Parse returns nil for it.
package stacktrace

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

const jsTypeExpr = `(?:[A-Za-z_$][\w$.]*)?(?:Error|Exception|Failure|Warning|Rejection)`

var (
	// "TypeError: msg", "TypeError [ERR_INVALID_ARG_TYPE]: msg", or a bare
	// "Error" line, at column 0.
	reJSHeader = regexp.MustCompile(`^(` + jsTypeExpr + `)(?: \[([\w.-]+)\])?(?::(?: (.*))?)?$`)
	// The same header after a console.error prefix ("request failed:
	// SyntaxError: ...") or a process-warning prefix ("(node:123) ...").
	reJSHeaderPrefixed = regexp.MustCompile(`^(.*?\S)\s+(` + jsTypeExpr + `)(?: \[([\w.-]+)\])?: (.*)$`)
	// Any "Name: msg" (Bun prints fs errors as "ENOENT: ..."; a custom
	// class need not end in Error).
	reJSTypeValue   = regexp.MustCompile(`^([A-Za-z_$][\w$.]*)(?: \[([\w.-]+)\])?: (.*)$`)
	reNodeCodeFrame = regexp.MustCompile(`^(?:(?:file://)?(?:/|[A-Za-z]:[\\/])\S*\.[cm]?[jt]sx?|node:\S+|\[eval\]|\[stdin\]|evalmachine\.<anonymous>):\d+$`)
	reDenoUncaught  = regexp.MustCompile(`^error: Uncaught\b ?(.*)$`)
	// "error: msg"; a bare "error" precedes Bun's dump of a thrown or
	// rejected non-Error value.
	reJSCLIError = regexp.MustCompile(`^error(?:: (.+))?$`)
	// Bun's numbered source line; console.error("msg:", err) puts its
	// prefix in front of the first one.
	reBunCodeFrame      = regexp.MustCompile(`^(?:.*?: )?\s*\d+ \|(?: .*)?$`)
	reJSFrameLine       = regexp.MustCompile(`^\s+at\s+\S`)
	reJSCause           = regexp.MustCompile(`^\s+\[cause\]: ?(.*)$`)
	reJSErrorsProp      = regexp.MustCompile(`^(\s+)\[errors\]: \[$`)
	reDenoCausedBy      = regexp.MustCompile(`^Caused by: (.*)$`)
	reJSCaretLine       = regexp.MustCompile(`^\s*[~^]*\^[~^]*\s*$`)
	reJSCollapsedFrames = regexp.MustCompile(`^\s+\.\.\. \d+ lines? matching cause stack trace \.\.\.$`)
	reNodeFooter        = regexp.MustCompile(`^Node\.js v\d+`)
	reBunFooter         = regexp.MustCompile(`^Bun v\d+\.\d+`)
	reNodeUseTrace      = regexp.MustCompile("^\\(Use `node --trace-uncaught")
	reJSLoc             = regexp.MustCompile(`^(.*):(\d+):(\d+)$`)
	reJSLocLineOnly     = regexp.MustCompile(`^(.*):(\d+)$`)
)

// jsSub is the shape of a JS block's first line.
type jsSub int

const (
	jsSubHeader jsSub = iota // "TypeError: msg" (column 0 or prefixed)
	jsSubCLI                 // "error: msg" (Deno/Bun CLI error)
	jsSubDeno                // "error: Uncaught ..."
	jsSubNode                // Node code-frame intro "path:N"
	jsSubBun                 // Bun numbered code frame "N | code"
)

func jsSubOf(line string) (jsSub, bool) {
	switch {
	case reDenoUncaught.MatchString(line):
		return jsSubDeno, true
	case reNodeCodeFrame.MatchString(line):
		return jsSubNode, true
	case reBunCodeFrame.MatchString(line):
		return jsSubBun, true
	case reJSCLIError.MatchString(line):
		return jsSubCLI, true
	case reJSHeader.MatchString(line), reJSHeaderPrefixed.MatchString(line):
		return jsSubHeader, true
	}
	return 0, false
}

func isJSFooter(line string) bool {
	return reNodeFooter.MatchString(line) || reBunFooter.MatchString(line)
}

var jsRule = blockRule{
	kind: "js",
	start: func(line string) bool {
		_, ok := jsSubOf(line)
		return ok
	},
	open: func(first string) grammar {
		sub, _ := jsSubOf(first)
		g := &jsGrammar{sub: sub, st: jsStMessage}
		if sub == jsSubNode || sub == jsSubBun {
			g.st = jsStPreamble
		}
		return g
	},
}

// JS grammar states.
const (
	jsStPreamble = iota // Node: source/caret/blank before the error; Bun: code frame + caret
	jsStValue           // Node: a thrown non-Error value after the caret
	jsStMessage         // after the error line, before the first frame
	jsStFrames          // "    at ..." lines
	jsStProps           // inside a util.inspect "{ ... }" property block
	jsStAfter           // blank lines after the stack, before a footer or a Bun cause
	jsStDone            // after the runtime footer
)

type jsGrammar struct {
	sub      jsSub
	st       int
	preamble int
	msg      int
	blanks   int
	depth    int
	// inCause is set once a Bun block is followed by another code frame:
	// Bun prints every cause as its own code-frame block, which is only
	// unambiguous when the "Bun vX" footer closes the whole dump, so
	// everything after the first block stays tentative until the footer.
	inCause bool
}

func (g *jsGrammar) next(line string, boundary bool) verdict {
	v := g.step(line, boundary)
	if g.inCause && v == vAccept && g.st != jsStDone {
		return vTentative
	}
	return v
}

func (g *jsGrammar) step(line string, boundary bool) verdict {
	if boundary {
		return vReject
	}
	blank := strings.TrimSpace(line) == ""
	switch g.st {
	case jsStPreamble:
		if g.sub == jsSubBun {
			switch {
			case reBunCodeFrame.MatchString(line), reJSCaretLine.MatchString(line):
				g.preamble++
				if g.preamble > 16 {
					return vReject
				}
				return vAccept
			case blank:
				return vReject
			}
			g.st = jsStMessage // the error line
			return vAccept
		}
		if reJSHeader.MatchString(line) {
			g.st = jsStMessage
			return vAccept
		}
		g.preamble++
		if g.preamble > 4 {
			return vReject
		}
		if g.preamble > 1 && !blank && !reJSCaretLine.MatchString(line) {
			// Neither the source line, the caret nor a blank: the
			// printed value of a thrown non-Error ("throw 'boom'").
			g.st = jsStValue
		}
		return vTentative
	case jsStValue:
		switch {
		case reNodeUseTrace.MatchString(line):
			return vAccept
		case reJSFrameLine.MatchString(line):
			g.frame(line)
			return vAccept
		case isJSFooter(line):
			g.st = jsStDone
			return vAccept
		case blank:
			return vTentative
		}
		return vReject
	case jsStMessage:
		switch {
		case reJSFrameLine.MatchString(line):
			g.frame(line)
			return vAccept
		case isJSFooter(line):
			g.st = jsStDone
			return vAccept
		case reDenoCausedBy.MatchString(line):
			return vAccept
		case g.sub != jsSubBun && !blank && !isIndented(line) && reJSHeader.MatchString(line):
			return vReject // another error starts
		}
		// Message continuation, Deno/Bun source + caret lines, Bun
		// property lines: confirmed only by a frame or a footer.
		g.msg++
		if g.msg > 16 {
			return vReject
		}
		return vTentative
	case jsStFrames:
		switch {
		case reJSFrameLine.MatchString(line):
			g.frame(line)
			return vAccept
		case reJSCollapsedFrames.MatchString(line):
			return vAccept
		case reDenoCausedBy.MatchString(line):
			g.st, g.msg = jsStMessage, 0
			return vAccept
		case isJSFooter(line):
			g.st = jsStDone
			return vAccept
		case blank:
			g.st, g.blanks = jsStAfter, 1
			return vTentative
		}
		return vReject
	case jsStProps:
		g.depth += braceDelta(line)
		if g.depth <= 0 {
			g.st, g.blanks = jsStAfter, 0
		}
		return vAccept
	case jsStAfter:
		switch {
		case isJSFooter(line):
			g.st = jsStDone
			return vAccept
		case blank:
			g.blanks++
			if g.blanks > 3 {
				return vReject
			}
			return vTentative
		case g.sub == jsSubBun && reBunCodeFrame.MatchString(line):
			g.inCause = true
			g.st, g.preamble, g.msg = jsStPreamble, 1, 0
			return vTentative
		}
		return vReject
	}
	return vReject
}

// frame records a frame line; one ending in " {" opens util.inspect's
// property block (own properties, "[cause]: ...", "[errors]: [...]").
func (g *jsGrammar) frame(line string) {
	g.st = jsStFrames
	if strings.HasSuffix(strings.TrimRight(line, " "), " {") {
		g.st, g.depth = jsStProps, 1
	}
}

// braceDelta is the change in util.inspect nesting depth a line causes.
func braceDelta(line string) int {
	t := strings.TrimSpace(line)
	d := 0
	if strings.HasPrefix(t, "}") || strings.HasPrefix(t, "]") {
		d--
	}
	if strings.HasSuffix(t, "{") || strings.HasSuffix(t, "[") {
		d++
	}
	return d
}

// jsSegment is one exception level of a JS block: the outer error, then
// each cause in printed (outer -> inner) order.
type jsSegment struct {
	typ, val string
	frames   []Frame
}

// splitJSTypeValue splits an error line ("TypeError [ERR_X]: msg", "Error",
// "ENOENT: msg", or a thrown primitive) into Type and Value.
func splitJSTypeValue(s string) (typ, val string) {
	s = strings.TrimSpace(s)
	if m := reJSHeader.FindStringSubmatch(s); m != nil {
		return m[1], m[3]
	}
	if m := reJSTypeValue.FindStringSubmatch(s); m != nil {
		return m[1], m[3]
	}
	return "", strings.Trim(s, `"'`)
}

func parseJS(block Block) *Exception {
	lines := block.Lines
	sub, ok := jsSubOf(lines[0])
	if !ok {
		return nil
	}
	footer := ""
	for _, l := range lines {
		switch {
		case reNodeFooter.MatchString(l):
			footer = "node"
		case reBunFooter.MatchString(l):
			footer = "bun"
		}
	}

	var segs []jsSegment
	if sub == jsSubBun {
		segs = parseBunSegments(lines)
	} else {
		segs = parseV8Segments(lines, sub)
	}
	if len(segs) == 0 {
		return nil
	}
	outer := segs[0]

	fatal := footer != "" || sub == jsSubNode || sub == jsSubDeno ||
		(sub == jsSubCLI && len(outer.frames) > 0)
	if len(outer.frames) == 0 {
		switch sub {
		case jsSubHeader:
			return nil
		case jsSubCLI, jsSubBun:
			if footer == "" {
				return nil
			}
		}
	}
	if outer.typ == "" && outer.val == "" && len(outer.frames) == 0 {
		return nil
	}

	level := levelFor(fatal)
	if !fatal && strings.HasSuffix(outer.typ, "Warning") {
		level = LevelWarning
	}
	ex := &Exception{
		Runtime:    jsRuntime(sub, footer, lines[0], segs),
		Type:       outer.typ,
		Value:      outer.val,
		Parser:     "js",
		Handled:    boolPtr(!fatal),
		Frames:     outer.frames,
		Level:      level,
		LineStart:  block.LineStart,
		LineEnd:    block.LineEnd,
		ObservedAt: blockTimestamp(block),
	}
	for _, s := range segs[1:] {
		ex.Chained = append(ex.Chained, Exception{Type: s.typ, Value: s.val, Frames: s.frames})
	}
	inheritChain(ex)
	return ex
}

// jsRuntime names the runtime when the text identifies it: a footer, a
// Deno/Bun-only printer, Node's code-frame intro, or node:/ext: frames.
func jsRuntime(sub jsSub, footer, first string, segs []jsSegment) string {
	switch {
	case footer == "node":
		return "node"
	case footer == "bun", sub == jsSubBun:
		return "bun"
	case sub == jsSubDeno:
		return "deno"
	}
	for _, s := range segs {
		for _, f := range s.frames {
			switch {
			case strings.HasPrefix(f.Filename, "ext:"):
				return "deno"
			case strings.HasPrefix(f.Filename, "node:"):
				return "node"
			}
		}
	}
	if sub == jsSubNode || strings.HasPrefix(first, "(node:") {
		return "node"
	}
	return ""
}

// parseV8Segments reads the Node/Deno shapes: the error line, its frames,
// then every "[cause]:" (util.inspect, nested to any depth, printed outer
// first) or "Caused by:" (Deno's uncaught printer) section in order.
func parseV8Segments(lines []string, sub jsSub) []jsSegment {
	var cur jsSegment
	start := 1
	switch sub {
	case jsSubDeno:
		rest := reDenoUncaught.FindStringSubmatch(lines[0])[1]
		rest = strings.TrimPrefix(rest, "(in promise) ")
		cur.typ, cur.val = splitJSTypeValue(rest)
	case jsSubCLI:
		if msg := reJSCLIError.FindStringSubmatch(lines[0])[1]; msg != "" {
			cur.typ, cur.val = "Error", msg
		} else {
			// Bun's "error" + the inspected non-Error value, up to the
			// first blank line.
			var parts []string
			for _, l := range lines[1:] {
				if strings.TrimSpace(l) == "" {
					break
				}
				parts = append(parts, strings.TrimSpace(l))
			}
			cur.val = strings.Join(parts, " ")
		}
	case jsSubHeader:
		if m := reJSHeader.FindStringSubmatch(lines[0]); m != nil {
			cur.typ, cur.val = m[1], m[3]
		} else if m := reJSHeaderPrefixed.FindStringSubmatch(lines[0]); m != nil {
			cur.typ, cur.val = m[2], m[4]
		}
	case jsSubNode:
		// Past the intro, the source line and the caret: the error line
		// (or the printed value of a thrown non-Error).
		h := -1
		for i := 1; i < len(lines); i++ {
			if reJSHeader.MatchString(lines[i]) {
				h = i
				break
			}
		}
		if h < 0 {
			for i := 2; i < len(lines); i++ {
				l := lines[i]
				if strings.TrimSpace(l) != "" && !reJSCaretLine.MatchString(l) && !isIndented(l) {
					h = i
					break
				}
			}
		}
		if h < 0 {
			return nil
		}
		cur.typ, cur.val = splitJSTypeValue(lines[h])
		start = h + 1
	}

	var segs []jsSegment
	skipIndent := ""
	skipping := false
	for _, l := range lines[start:] {
		if skipping {
			if strings.HasPrefix(l, skipIndent+"]") {
				skipping = false
			}
			continue
		}
		if m := reJSErrorsProp.FindStringSubmatch(l); m != nil {
			// AggregateError's sibling errors: not causes, not frames.
			skipIndent, skipping = m[1], true
			continue
		}
		if m := reJSCause.FindStringSubmatch(l); m != nil {
			segs = append(segs, cur)
			cur = jsSegment{}
			cur.typ, cur.val = splitJSTypeValue(strings.TrimSuffix(m[1], " {"))
			continue
		}
		if m := reDenoCausedBy.FindStringSubmatch(l); m != nil {
			segs = append(segs, cur)
			cur = jsSegment{}
			cur.typ, cur.val = splitJSTypeValue(m[1])
			continue
		}
		if f, ok := parseJSFrameLine(l); ok {
			cur.frames = append(cur.frames, f)
		}
	}
	segs = append(segs, cur)
	for i := range segs {
		reverseFrames(segs[i].frames) // V8 prints innermost (crash) first
	}
	return segs
}

// parseBunSegments reads Bun's printer: per exception (outer first, then
// each cause), code-frame lines and a caret, the error line, optional
// property lines, then frames.
func parseBunSegments(lines []string) []jsSegment {
	var segs []jsSegment
	i := 0
	for i < len(lines) {
		for i < len(lines) && (reBunCodeFrame.MatchString(lines[i]) || reJSCaretLine.MatchString(lines[i])) {
			i++
		}
		if i >= len(lines) {
			break
		}
		l := lines[i]
		i++
		if strings.TrimSpace(l) == "" || isJSFooter(l) {
			continue
		}
		var seg jsSegment
		if m := reJSCLIError.FindStringSubmatch(l); m != nil {
			if m[1] != "" {
				seg.typ, seg.val = "Error", m[1]
			}
		} else {
			seg.typ, seg.val = splitJSTypeValue(l)
		}
		for i < len(lines) && !reBunCodeFrame.MatchString(lines[i]) && !isJSFooter(lines[i]) {
			if f, ok := parseJSFrameLine(lines[i]); ok {
				seg.frames = append(seg.frames, f)
			}
			i++
		}
		reverseFrames(seg.frames)
		segs = append(segs, seg)
	}
	return segs
}

// parseJSFrameLine parses one "    at Func (file:line:col)" (or the bare
// "    at file:line:col" anonymous form) line into a Frame. A trailing " {"
// (util.inspect opening the error's property block) is not part of it.
func parseJSFrameLine(line string) (Frame, bool) {
	trimmed := strings.TrimSpace(line)
	rest, ok := strings.CutPrefix(trimmed, "at ")
	if !ok {
		return Frame{}, false
	}
	rest = strings.TrimSuffix(rest, " {")

	var funcPart, locPart string
	if strings.HasSuffix(rest, ")") {
		depth, open := 0, -1
		for i := len(rest) - 1; i >= 0 && open < 0; i-- {
			switch rest[i] {
			case ')':
				depth++
			case '(':
				depth--
				if depth == 0 {
					open = i
				}
			}
		}
		if open >= 0 {
			funcPart = strings.TrimSpace(rest[:open])
			locPart = rest[open+1 : len(rest)-1]
		} else {
			locPart = rest
		}
	} else {
		// "at async file:///x.mjs:81:5": an anonymous async frame.
		locPart = strings.TrimPrefix(rest, "async ")
	}

	// Eval frames nest a location inside the func part, e.g.
	// "eval at <anonymous> (file:10:5), <anonymous>:3:9" as the loc; the
	// last comma-separated segment is the actual innermost location.
	if i := strings.LastIndex(locPart, ", "); i >= 0 {
		if cand := locPart[i+2:]; reJSLoc.MatchString(cand) {
			locPart = cand
		}
	}

	file, ln, col := splitJSLoc(locPart)
	f := Frame{Function: funcPart, Filename: file, Lineno: ln, Colno: col}
	if isAbsPath(file) {
		f.AbsPath = file
	}
	return f, true
}

// splitJSLoc splits "file:line:col" (or "file:line"), reducing a file://
// URL to its path. Pseudo-locations ("native", "<anonymous>", "index 0")
// are returned as the filename with no line.
func splitJSLoc(loc string) (file string, line, col int) {
	if rest, ok := strings.CutPrefix(loc, "file://"); ok {
		if u, err := url.PathUnescape(rest); err == nil {
			rest = u
		}
		// file:///C:/x.js -> C:/x.js
		if len(rest) > 3 && rest[0] == '/' && rest[2] == ':' && isDriveLetter(rest[1]) {
			rest = rest[1:]
		}
		loc = rest
	}
	if m := reJSLoc.FindStringSubmatch(loc); m != nil {
		line, _ = strconv.Atoi(m[2])
		col, _ = strconv.Atoi(m[3])
		return m[1], line, col
	}
	if m := reJSLocLineOnly.FindStringSubmatch(loc); m != nil && !strings.Contains(m[1], " ") {
		line, _ = strconv.Atoi(m[2])
		return m[1], line, 0
	}
	return loc, 0, 0
}

func reverseFrames(f []Frame) {
	for i, j := 0, len(f)-1; i < j; i, j = i+1, j-1 {
		f[i], f[j] = f[j], f[i]
	}
}
