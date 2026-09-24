package widgets

import (
	"strings"
	"testing"
)

func selfOnlyFixture() CodeFrame {
	return CodeFrame{
		FuncName:  "heavyStringify",
		File:      "js/workload.js",
		StartLine: 11,
		EndLine:   22,
		SelfPct:   60.8,
		Lines: []CodeFrameLine{
			{Line: 13, Code: "  for (const item of items) {", Percent: 0.1},
			{Line: 16, Code: "    for (let i = 0; i < 2500; i++) {", Percent: 0.0},
			{Line: 17, Code: "      s += JSON.stringify({ i, item, doubled, pad: 'x'.repeat(64) });", Percent: 99.9},
			{Line: 19, Code: "    out.push(s.length);", Percent: 0.0},
		},
		Footer: "% = share of this function's self samples",
		Width:  100,
	}
}

func TestCodeFrameRenderIsMultiLine(t *testing.T) {
	out := selfOnlyFixture().Render()
	lines := strings.Split(out, "\n")
	// header + 4 code lines + footer = 6
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6:\n%s", len(lines), out)
	}
}

func TestCodeFrameHeaderIsExactlyWidth(t *testing.T) {
	f := selfOnlyFixture()
	out := f.Render()
	header := strings.Split(out, "\n")[0]
	if got := len([]rune(header)); got != f.Width {
		t.Errorf("header width = %d, want %d: %q", got, f.Width, header)
	}
}

func TestCodeFrameMarksHottestLineAutomatically(t *testing.T) {
	f := selfOnlyFixture()
	out := f.Render()
	var hotRow string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "17") && strings.HasPrefix(l, ">") {
			hotRow = l
		}
	}
	if hotRow == "" {
		t.Fatalf("no row starting with '>' found for line 17:\n%s", out)
	}
	if !strings.Contains(hotRow, "99.9%") {
		t.Errorf("hot row = %q, want it to contain 99.9%%", hotRow)
	}
	// Every other row must NOT start with '>'.
	for _, l := range strings.Split(out, "\n")[1:] {
		if strings.HasPrefix(l, ">") && l != hotRow {
			t.Errorf("unexpected '>' marker on non-hot row: %q", l)
		}
	}
}

func TestCodeFrameExplicitHotLineOverridesAutoDetection(t *testing.T) {
	f := selfOnlyFixture()
	f.HotLine = 13 // not the highest-Percent line
	out := f.Render()
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, ">") && !strings.Contains(l, "13") {
			t.Errorf("marker landed on the wrong row: %q, want line 13", l)
		}
	}
}

func TestCodeFrameNoColorEmitsNoEscapeCodes(t *testing.T) {
	f := selfOnlyFixture()
	f.Color = false
	out := f.Render()
	if strings.Contains(out, "\x1b[") {
		t.Errorf("Color=false must never emit ANSI escape codes:\n%q", out)
	}
}

func TestCodeFrameColorEmitsEscapeCodes(t *testing.T) {
	f := selfOnlyFixture()
	f.Color = true
	out := f.Render()
	if !strings.Contains(out, "\x1b[") {
		t.Error("Color=true should emit ANSI escape codes")
	}
}

func TestCodeFrameLongCodeTruncatesWithEllipsis(t *testing.T) {
	f := CodeFrame{
		FuncName: "f", File: "a.js", Width: 100,
		Lines: []CodeFrameLine{
			{Line: 1, Code: strings.Repeat("x", 200), Percent: 100},
		},
	}
	out := f.Render()
	rows := strings.Split(out, "\n")
	if !strings.Contains(rows[1], "…") {
		t.Errorf("expected an ellipsis for a 200-char source line: %q", rows[1])
	}
}

func TestCodeFrameDualColumnShowsSelfAndCum(t *testing.T) {
	f := CodeFrame{
		FuncName: "main.heavyStringify", File: "go-pprof/main.go", StartLine: 14, EndLine: 22,
		SelfPct: 0.0, CumPct: 74.8, ShowCum: true, Width: 100,
		Lines: []CodeFrameLine{
			{Line: 18, Code: "for i := 0; i < n; i++ {", Percent: 0.0, CumPercent: 0.4},
			{Line: 19, Code: "b, _ := json.Marshal(rows[i])", Percent: 0.0, CumPercent: 74.1},
			{Line: 20, Code: "total += len(b)", Percent: 0.0, CumPercent: 0.3},
		},
		Footer: "method: pprof proto (inlining-aware; no go toolchain needed)",
	}
	out := f.Render()
	lines := strings.Split(out, "\n")
	// header + column-header + 3 code lines + footer = 6
	if len(lines) != 6 {
		t.Fatalf("got %d lines, want 6:\n%s", len(lines), out)
	}
	if !strings.Contains(lines[1], "FLAT") || !strings.Contains(lines[1], "CUM") {
		t.Errorf("column header = %q, want FLAT and CUM", lines[1])
	}
	hot := lines[3] // line 19, the json.Marshal call
	if !strings.HasPrefix(hot, ">") || !strings.Contains(hot, "74.1%") {
		t.Errorf("hot row = %q, want a '>' marker and 74.1%%", hot)
	}
}

// TestCodeFrameDualColumnMarksHotLineByCum reproduces the review's finding:
// a wrapper line's own FLAT (self) can be nonzero while the line beneath it
// — a call into the real hot work — carries almost all the CUM cost. The
// '>' marker must land on the high-CUM line, matching mockup 7 (the
// json.Marshal line), not the higher-FLAT one.
func TestCodeFrameDualColumnMarksHotLineByCum(t *testing.T) {
	f := CodeFrame{
		FuncName: "main.heavyStringify", File: "main.go", ShowCum: true, Width: 100,
		Lines: []CodeFrameLine{
			{Line: 18, Code: "for i := 0; i < n; i++ {", Percent: 2.6, CumPercent: 2.6},
			{Line: 19, Code: "b, _ := json.Marshal(rows[i])", Percent: 0.0, CumPercent: 97.4},
		},
	}
	out := f.Render()
	var hot string
	for _, l := range strings.Split(out, "\n") {
		if strings.HasPrefix(l, ">") {
			hot = l
		}
	}
	if !strings.Contains(hot, "19") {
		t.Errorf("hot row = %q, want the CUM-dominant line 19, not the FLAT-dominant line 18", hot)
	}
}

// TestCodeFrameDualColumnFitsInWidthWithFullBar asserts the dual-column
// layout's own "not wider than Width" contract holds even at a full 100%
// CUM bar — the review found a real dual row rendering at 104 columns.
func TestCodeFrameDualColumnFitsInWidthWithFullBar(t *testing.T) {
	f := CodeFrame{
		FuncName: "f", File: "a.go", ShowCum: true, Width: 100,
		Lines: []CodeFrameLine{{Line: 1, Code: "work()", Percent: 100, CumPercent: 100}},
	}
	for _, row := range strings.Split(f.Render(), "\n") {
		if w := runeWidth(row); w > f.Width {
			t.Errorf("row %q is %d columns wide, want <=%d", row, w, f.Width)
		}
	}
}

// TestCodeFrameDualColumnGoldenAt100ColumnsNoColor is the dual-column
// counterpart to TestCodeFrameGoldenAt100ColumnsNoColor: a tab-indented Go
// source line (real gofmt output, not the self-only fixture's hand-spaced
// JS), percentages before the code as mockup 7 specifies, and every row
// within the 100-column budget.
func TestCodeFrameDualColumnGoldenAt100ColumnsNoColor(t *testing.T) {
	f := CodeFrame{
		FuncName: "main.heavyStringify", File: "go-pprof/main.go", StartLine: 14, EndLine: 22,
		SelfPct: 0.0, CumPct: 74.8, ShowCum: true, Width: 100, Color: false,
		Lines: []CodeFrameLine{
			{Line: 18, Code: "\tfor i := 0; i < n; i++ {", Percent: 0.0, CumPercent: 0.4},
			{Line: 19, Code: "\t\tb, _ := json.Marshal(rows[i])", Percent: 0.0, CumPercent: 74.1},
			{Line: 20, Code: "\t\ttotal += len(b)", Percent: 0.0, CumPercent: 0.3},
		},
		Footer: "method: pprof proto (inlining-aware; no go toolchain needed)",
	}
	got := f.Render()
	want := "-- main.heavyStringify · go-pprof/main.go:14-22 · cum 74.8% · flat 0.0% ----------------------------\n" +
		"          FLAT     CUM\n" +
		"  18 |    0.0%    0.4% |     for i := 0; i < n; i++ {                               \n" +
		"> 19 |    0.0%   74.1% |         b, _ := json.Marshal(rows[i])                      ############\n" +
		"  20 |    0.0%    0.3% |         total += len(b)                                    \n" +
		"     method: pprof proto (inlining-aware; no go toolchain needed)"
	if got != want {
		t.Errorf("golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
	for _, row := range strings.Split(got, "\n") {
		if w := runeWidth(row); w > f.Width {
			t.Errorf("row %q is %d columns wide, want <=%d", row, w, f.Width)
		}
	}
}

func runeWidth(s string) int {
	return len([]rune(s))
}

func TestCodeFrameEmptyLinesRendersPlaceholder(t *testing.T) {
	f := CodeFrame{FuncName: "idle", File: "a.js"}
	out := f.Render()
	if out == "" {
		t.Error("expected a non-empty placeholder for zero lines")
	}
}

func TestBarProportional(t *testing.T) {
	if b := bar(100, 10); b != strings.Repeat("#", 10) {
		t.Errorf("bar(100,10) = %q, want 10 chars", b)
	}
	if b := bar(0, 10); b != "" {
		t.Errorf("bar(0,10) = %q, want empty", b)
	}
	if b := bar(50, 10); len(b) != 5 {
		t.Errorf("bar(50,10) = %q, want 5 chars", b)
	}
	// Never overflows even for an out-of-range input.
	if b := bar(500, 10); len(b) != 10 {
		t.Errorf("bar(500,10) = %q, want capped at 10 chars", b)
	}
}

func TestPadCode(t *testing.T) {
	if got := padCode("abc", 6); got != "abc   " {
		t.Errorf("padCode short = %q, want %q", got, "abc   ")
	}
	if got := padCode("abcdef", 6); got != "abcdef" {
		t.Errorf("padCode exact = %q, want unchanged", got)
	}
	if got := padCode("abcdefgh", 6); got != "abcde…" {
		t.Errorf("padCode long = %q, want %q", got, "abcde…")
	}
}

// TestPadCodeExpandsTabs asserts a literal tab is expanded to
// codeTabWidth-column stops before padding, so a tab-indented Go source
// line still lines up its trailing PCT|BAR column exactly like a
// space-indented one at the same visual depth.
func TestPadCodeExpandsTabs(t *testing.T) {
	got := padCode("\tx", 6)
	want := "    x " // one tab -> 4 columns (codeTabWidth), then "x", padded to 6
	if got != want {
		t.Errorf("padCode(%q, 6) = %q, want %q", "\tx", got, want)
	}
}

// TestPadCodeUsesDisplayWidthNotRuneCount asserts a wide (2-cell) CJK rune
// is padded/truncated by its actual terminal width, not counted as one
// rune — a rune-counting pad would under-pad a CJK line by one column per
// wide character and silently misalign every row after it.
func TestPadCodeUsesDisplayWidthNotRuneCount(t *testing.T) {
	// "汉字" is 2 runes but 4 terminal columns.
	got := padCode("汉字", 6)
	if got != "汉字  " {
		t.Errorf("padCode(%q, 6) = %q, want %q (2 trailing spaces, not 4)", "汉字", got, "汉字  ")
	}
}

// TestCodeFrameGoldenAt100ColumnsNoColor is the Done-when golden: a fixed
// render, no color, exactly matching a known-good snapshot. If this test
// needs to change, the rendering format changed — update the golden
// deliberately, don't just make the diff pass.
func TestCodeFrameGoldenAt100ColumnsNoColor(t *testing.T) {
	f := selfOnlyFixture()
	f.Color = false
	got := f.Render()
	want := "-- heavyStringify · js/workload.js:11-22 · 60.8% self ----------------------------------------------\n" +
		"  13 |   for (const item of items) {                                0.1% |\n" +
		"  16 |     for (let i = 0; i < 2500; i++) {                         0.0% |\n" +
		"> 17 |       s += JSON.stringify({ i, item, doubled, pad: 'x'.re…  99.9% |##########################\n" +
		"  19 |     out.push(s.length);                                      0.0% |\n" +
		"     % = share of this function's self samples"
	if got != want {
		t.Errorf("golden mismatch:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// TestCodeFrameRendersIssueOverlay is the E3.4 (errors × heat) regression:
// a line carrying Issues gets an "E <entry>" marker after its bar, and the
// frame gains the "E = issues whose culprit is this line" legend — but only
// when at least one line actually has an overlay, so a plain heatmap with
// no matching issues renders exactly as before (see
// TestCodeFrameGoldenAt100ColumnsNoColor, which has no Issues set anywhere).
func TestCodeFrameRendersIssueOverlay(t *testing.T) {
	f := selfOnlyFixture()
	f.Color = false
	f.Lines[2].Issues = []string{"5C1D x10 open"}
	out := f.Render()

	var hotLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "17 |") {
			hotLine = l
		}
	}
	if !strings.Contains(hotLine, "E 5C1D x10 open") {
		t.Errorf("hot line = %q, want an E marker for the overlaid issue", hotLine)
	}
	if !strings.Contains(out, "E = issues whose culprit is this line") {
		t.Errorf("output missing the E legend:\n%s", out)
	}
	// The E3.4 marker must not push the marked line past the frame's own
	// fixed Width — every OTHER line still fits inside it, and a marked
	// line that overflows would wrap in a Width-column terminal (see the
	// E3.1 golden-width review finding).
	if got := len([]rune(hotLine)); got > f.Width {
		t.Errorf("hot line width = %d runes, want <= %d (Width): %q", got, f.Width, hotLine)
	}
}

// TestCodeFrameOmitsIssueLegendWithNoOverlay guards the "byte-for-byte
// unchanged when nothing overlays" half of the same finding: with every
// line's Issues empty (the zero value, same as before this field existed),
// neither an "E " marker nor the legend line appears anywhere.
func TestCodeFrameOmitsIssueLegendWithNoOverlay(t *testing.T) {
	out := selfOnlyFixture().Render()
	if strings.Contains(out, "issues whose culprit") {
		t.Errorf("output must omit the E legend with no overlaid issues:\n%s", out)
	}
}

// TestCodeFrameIssueOverlayInDualColumnView asserts the marker also renders
// in the pprof dual-column (ShowCum) layout, not just the CDP self-only one.
func TestCodeFrameIssueOverlayInDualColumnView(t *testing.T) {
	f := CodeFrame{
		FuncName: "buildIndex", File: "go-pprof/main.go", StartLine: 40, EndLine: 52,
		ShowCum: true, Width: 100,
		Lines: []CodeFrameLine{
			{Line: 47, Code: `idx[key] = append(idx[key], r)`, Percent: 0.0, CumPercent: 74.1, Issues: []string{"9F2A x3 open"}},
		},
	}
	out := f.Render()
	if !strings.Contains(out, "E 9F2A x3 open") {
		t.Errorf("dual-column output missing the E marker:\n%s", out)
	}
}

// --- HideMetrics mode (polish-wave review: "the secondary 'inlined callee'
// frame claims things that are not true") -----------------------------------

func hideMetricsFixture() CodeFrame {
	return CodeFrame{
		FuncName:    "heavyStringify",
		File:        "js/workload.js",
		StartLine:   11,
		EndLine:     13,
		HideMetrics: true,
		Subtitle:    "inlined callee, no per-line data",
		Width:       100,
		Lines: []CodeFrameLine{
			{Line: 11, Code: "function heavyStringify(items) {"},
			{Line: 12, Code: "  return items.length;"},
			{Line: 13, Code: "}"},
		},
	}
}

// TestCodeFrameHideMetricsOmitsHotLineMarker is the direct regression: with
// every line's Percent/CumPercent left at their zero-value default (no real
// per-line data exists), the OLD unconditional hottestLine() fallback would
// mark the FIRST line -- the declaration -- '>', fabricating a "hot line"
// that was never measured. HideMetrics must print no '>' marker at all.
func TestCodeFrameHideMetricsOmitsHotLineMarker(t *testing.T) {
	out := hideMetricsFixture().Render()
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, ">") {
			t.Errorf("HideMetrics output must never print a '>' hot-line marker: %q\nfull output:\n%s", line, out)
		}
	}
}

// TestCodeFrameHideMetricsOmitsPercentColumn asserts no percentage figure
// (fabricated "0.0%" or otherwise) appears anywhere in a HideMetrics frame.
func TestCodeFrameHideMetricsOmitsPercentColumn(t *testing.T) {
	out := hideMetricsFixture().Render()
	if strings.Contains(out, "%") {
		t.Errorf("HideMetrics output must contain no percentage figures at all:\n%s", out)
	}
}

// TestCodeFrameHideMetricsHeaderUsesSubtitleNotSelfPct asserts the header
// shows "FuncName (Subtitle) · loc" -- never the ordinary "N.N%% self"
// headline, which for a HideMetrics frame would always read a fabricated
// "0.0%% self" (the zero-value SelfPct a caller never bothered to set,
// because there IS no real self percentage here).
func TestCodeFrameHideMetricsHeaderUsesSubtitleNotSelfPct(t *testing.T) {
	f := hideMetricsFixture()
	f.SelfPct = 0 // explicit: this must never reach the rendered header.
	out := f.Render()
	header := strings.Split(out, "\n")[0]
	if !strings.Contains(header, "heavyStringify (inlined callee, no per-line data)") {
		t.Errorf("header = %q, want it to name FuncName and Subtitle together", header)
	}
	if strings.Contains(header, "self") {
		t.Errorf("header = %q, want no fabricated \"N.N%% self\" headline", header)
	}
}

// TestCodeFrameHideMetricsStillTruncatesLongLines guards the one thing this
// mode still owes width: a pathologically long line must not blow the frame
// out past its configured Width.
func TestCodeFrameHideMetricsStillTruncatesLongLines(t *testing.T) {
	f := hideMetricsFixture()
	f.Lines = []CodeFrameLine{{Line: 1, Code: strings.Repeat("x", 500)}}
	out := f.Render()
	for _, line := range strings.Split(out, "\n") {
		if got := len([]rune(line)); got > f.Width {
			t.Errorf("line %q is %d runes, want at most Width=%d", line, got, f.Width)
		}
	}
}
