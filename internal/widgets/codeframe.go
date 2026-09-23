package widgets

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// CodeFrameLine is one source line inside a CodeFrame. Percent/CumPercent
// are pre-computed by the caller (profiler.HeatLine.PctOfFunction and, for
// a dual-column pprof view, the line's own cum share) — CodeFrame only
// renders numbers it's given; it never recomputes a percentage itself, so
// it stays agnostic to which profiler produced them and can never disagree
// with the caller's own numbers.
type CodeFrameLine struct {
	Line int
	Code string
	// Percent sizes the single bar (CDP self-only view) or the SELF column
	// bar (pprof dual-column view).
	Percent float64
	// CumPercent sizes the CUM column bar. Only read when ShowCum is set.
	CumPercent float64
}

// CodeFrame renders one function's touched lines — line numbers, a '>'
// marker on the hottest line, a percentage column, and a proportional bar —
// the "Mapa de calor por línea" view `monitor hot` prints beneath its
// TOTAL/SELF/FUNCTION table. Two layouts:
//
//   - self-only (ShowCum false): one PCT|BAR column, for a CDP-sourced
//     heatmap, whose lines carry only a self share (V8 positionTicks have
//     no separate cumulative concept — see profiler.HeatLine's doc).
//   - dual-column (ShowCum true): SELF and CUM side by side, for a
//     pprof-sourced heatmap, where a wrapper's near-zero self but high cum
//     is exactly the signal worth seeing at a glance.
type CodeFrame struct {
	FuncName  string
	File      string
	StartLine int
	EndLine   int
	// SelfPct/CumPct are the function-level headline numbers shown in the
	// title bar.
	SelfPct float64
	CumPct  float64
	ShowCum bool

	Lines []CodeFrameLine
	// HotLine marks which Line gets the '>' marker. 0 (or a value not
	// present in Lines) means "the line with the highest Percent" —
	// CodeFrame picks it, so a caller that already found the hottest line
	// doesn't have to duplicate that search.
	HotLine int

	// Footer is one caption line rendered dimmed beneath the table (e.g.
	// the roadmap's "% = share of this function's self samples · JIT may
	// smear +-1 line" for CDP, or "method: pprof proto (inlining-aware; no
	// go toolchain needed)" for pprof). Omitted entirely when "".
	Footer string

	// Width is the frame's fixed total column width. <=0 defaults to 100
	// (the width every `monitor hot` golden and glyphrun spec terminal
	// uses).
	Width int
	// Color enables lipgloss ANSI styling. false renders plain text with
	// no escape codes at all — the caller (monitor hot) decides this from
	// whether stdout is a TTY and whether NO_COLOR is set; CodeFrame itself
	// never inspects the environment.
	Color bool
}

// DefaultCodeFrameWidth is the fixed column width `monitor hot`'s golden
// output and glyphrun specs use.
const DefaultCodeFrameWidth = 100

// codeframe column layout. codeColumnWidth is sized so that, combined with
// the gutter and one PCT|BAR column at DefaultCodeFrameWidth, an 80th-
// percentile source line (bundler-formatted JS or gofmt'd Go, both usually
// well under 60 columns for a single statement) fits without truncation;
// longer lines are truncated with an ellipsis rather than pushing the PCT
// column out of alignment.
const codeColumnWidth = 58

var (
	frameHeaderStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#607089"))
	frameHotStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#E5C07B")).Bold(true)
	frameBarStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#88C0D0"))
	frameCumBarStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#A3BE8C"))
	frameDimStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("#607089"))
)

func (f CodeFrame) style(s lipgloss.Style, text string) string {
	if !f.Color {
		return text
	}
	return s.Render(text)
}

// Render returns the frame as a fixed-width, multi-line string.
func (f CodeFrame) Render() string {
	width := f.Width
	if width <= 0 {
		width = DefaultCodeFrameWidth
	}
	if len(f.Lines) == 0 {
		return f.style(frameDimStyle, "(no lines to show)")
	}

	hotLine := f.HotLine
	if !f.hasLine(hotLine) {
		hotLine = f.hottestLine()
	}

	var b strings.Builder
	b.WriteString(f.renderHeader(width))
	if f.ShowCum {
		b.WriteByte('\n')
		b.WriteString(f.renderColumnHeader())
	}
	lineNumWidth := f.lineNumWidth()
	for _, l := range f.Lines {
		b.WriteByte('\n')
		b.WriteString(f.renderLine(l, l.Line == hotLine, lineNumWidth))
	}
	if f.Footer != "" {
		b.WriteByte('\n')
		b.WriteString(f.style(frameDimStyle, "     "+f.Footer))
	}
	return b.String()
}

func (f CodeFrame) hasLine(line int) bool {
	for _, l := range f.Lines {
		if l.Line == line {
			return true
		}
	}
	return false
}

// hottestLine returns the Line with the highest Percent (ties broken by
// CumPercent, then by the first occurrence), the same "which row gets the
// marker" rule a caller that omits HotLine gets for free.
func (f CodeFrame) hottestLine() int {
	best := f.Lines[0]
	for _, l := range f.Lines[1:] {
		if l.Percent > best.Percent || (l.Percent == best.Percent && l.CumPercent > best.CumPercent) {
			best = l
		}
	}
	return best.Line
}

func (f CodeFrame) lineNumWidth() int {
	w := 2
	for _, l := range f.Lines {
		n := len(fmt.Sprintf("%d", l.Line))
		if n > w {
			w = n
		}
	}
	return w
}

// renderHeader builds the "-- Func · File · headline ----" title bar,
// padded with '-' to exactly width runes (never more: a long func/file name
// truncates the trailing dashes to zero rather than overflowing width).
func (f CodeFrame) renderHeader(width int) string {
	loc := f.File
	if f.StartLine > 0 {
		if f.EndLine > 0 && f.EndLine != f.StartLine {
			loc = fmt.Sprintf("%s:%d-%d", f.File, f.StartLine, f.EndLine)
		} else {
			loc = fmt.Sprintf("%s:%d", f.File, f.StartLine)
		}
	}
	var headline string
	if f.ShowCum {
		headline = fmt.Sprintf("cum %.1f%% · flat %.1f%%", f.CumPct, f.SelfPct)
	} else {
		headline = fmt.Sprintf("%.1f%% self", f.SelfPct)
	}
	prefix := fmt.Sprintf("-- %s · %s · %s ", f.FuncName, loc, headline)
	pad := width - len([]rune(prefix))
	if pad < 0 {
		pad = 0
	}
	return f.style(frameHeaderStyle, prefix+strings.Repeat("-", pad))
}

func (f CodeFrame) renderColumnHeader() string {
	lineNumWidth := f.lineNumWidth()
	gutter := strings.Repeat(" ", lineNumWidth+4) // "  NN | " gutter width, minus the leading space renderLine's marker column adds
	return f.style(frameDimStyle, gutter+"  FLAT     CUM")
}

// renderLine renders one source line: marker, line number, code (padded/
// truncated to codeColumnWidth), and either one PCT|BAR column or two
// (SELF, CUM) depending on ShowCum.
func (f CodeFrame) renderLine(l CodeFrameLine, isHot bool, lineNumWidth int) string {
	marker := " "
	if isHot {
		marker = ">"
	}
	numStr := fmt.Sprintf("%*d", lineNumWidth, l.Line)
	if isHot {
		marker = f.style(frameHotStyle, marker)
		numStr = f.style(frameHotStyle, numStr)
	}
	code := padCode(l.Code, codeColumnWidth)

	if f.ShowCum {
		// One trailing bar, sized by CUM: a pprof wrapper's SELF is
		// routinely ~0 (see profiler_test.go's
		// TestSymbolsFromPprofWrapperHasZeroFlatButFullCum), so a bar sized
		// by SELF would be empty on exactly the rows CUM makes interesting.
		// Both numbers are still printed, in FLAT-then-CUM column order.
		cumBar := f.style(frameCumBarStyle, bar(l.CumPercent, 20))
		return fmt.Sprintf("%s %s | %s %6.1f%% %6.1f%% | %s", marker, numStr, code, l.Percent, l.CumPercent, cumBar)
	}
	pctStr := fmt.Sprintf("%5.1f%%", l.Percent)
	barStr := f.style(frameBarStyle, bar(l.Percent, 26))
	return fmt.Sprintf("%s %s | %s %s |%s", marker, numStr, code, pctStr, barStr)
}

// bar renders a proportional bar of at most width runes for a 0-100 pct.
func bar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	filled := int(pct/100*float64(width) + 0.5)
	if filled > width {
		filled = width
	}
	if filled <= 0 {
		return ""
	}
	return strings.Repeat("#", filled)
}

// padCode right-pads code to exactly width runes, or truncates it with a
// trailing ellipsis when longer, so the PCT|BAR column that follows lines
// up identically across every row regardless of source-line length.
func padCode(code string, width int) string {
	r := []rune(code)
	if len(r) == width {
		return code
	}
	if len(r) < width {
		return code + strings.Repeat(" ", width-len(r))
	}
	if width <= 1 {
		return string(r[:width])
	}
	return string(r[:width-1]) + "…"
}
