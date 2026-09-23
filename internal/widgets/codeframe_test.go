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
