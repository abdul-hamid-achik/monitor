package widgets

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
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
	// Issues lists pre-formatted "SHORTID xN status" entries for local
	// issues whose culprit lands on this line (E3.4: errors × heat overlay
	// — see the roadmap's "6. Errores × calor en la misma vista" mockup).
	// Rendered as "E <entry>[, <entry>...]" appended after this line's bar.
	// nil/empty (every line before E3.4 populated this) renders nothing
	// extra, byte-for-byte the same output as before this field existed.
	Issues []string
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

	// HideMetrics renders this frame with NO per-line percentages, bars, or
	// '>' hot-line marker at all — for a SECONDARY frame whose numbers
	// would be actively misleading rather than merely absent (e.g. a
	// JIT-inlined callee's own source, shown purely for context, where
	// every line's Percent/CumPercent is meaningless zero-value data, not
	// a real measurement). Without this mode, Render's own hottestLine
	// fallback marks the FIRST line '>' whenever every percentage ties at
	// zero — the callee's declaration line, not its actual hot statement —
	// and the header prints a fabricated-looking "0.0% self", both of
	// which the review's "the secondary 'inlined callee' frame claims
	// things that are not true" finding called out by name. When true, the
	// header also drops the usual "N.N% self"/"cum N.N% flat N.N%"
	// headline in favor of Subtitle, shown in parentheses right after
	// FuncName.
	HideMetrics bool
	// Subtitle is HideMetrics' own header annotation (e.g. "inlined
	// callee, no per-line data"), rendered as "FuncName (Subtitle) · loc
	// ----". Ignored when HideMetrics is false.
	Subtitle string
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

	var b strings.Builder
	b.WriteString(f.renderHeader(width))
	lineNumWidth := f.lineNumWidth()

	if f.HideMetrics {
		// No hot-line marker, no percentage/bar columns at all — see
		// HideMetrics' own doc comment: every one of those would be
		// fabricated data for a frame whose Lines carry no real
		// measurement.
		for _, l := range f.Lines {
			b.WriteByte('\n')
			b.WriteString(f.renderHideMetricsLine(l, lineNumWidth, width))
		}
		if f.Footer != "" {
			b.WriteByte('\n')
			b.WriteString(f.style(frameDimStyle, "     "+f.Footer))
		}
		return b.String()
	}

	hotLine := f.HotLine
	if !f.hasLine(hotLine) {
		hotLine = f.hottestLine()
	}

	if f.ShowCum {
		b.WriteByte('\n')
		b.WriteString(f.renderColumnHeader())
	}
	for _, l := range f.Lines {
		b.WriteByte('\n')
		b.WriteString(f.renderLine(l, l.Line == hotLine, lineNumWidth, width))
	}
	if f.hasIssues() {
		b.WriteByte('\n')
		b.WriteString(f.style(frameDimStyle, "     E = issues whose culprit is this line"))
	}
	if f.Footer != "" {
		b.WriteByte('\n')
		b.WriteString(f.style(frameDimStyle, "     "+f.Footer))
	}
	return b.String()
}

// renderHideMetricsLine renders one HideMetrics-mode line: marker column
// (always blank — never a fabricated '>' on zero-value data), line number,
// and the code itself, truncated (never padded — there is no column after
// it to keep aligned) to fit the remaining width.
func (f CodeFrame) renderHideMetricsLine(l CodeFrameLine, lineNumWidth, width int) string {
	numStr := fmt.Sprintf("%*d", lineNumWidth, l.Line)
	prefix := fmt.Sprintf("  %s | ", numStr)
	code := expandTabs(l.Code, codeTabWidth)
	if avail := width - ansi.StringWidth(prefix); avail > 0 && ansi.StringWidth(code) > avail {
		code = ansi.Truncate(code, avail, "…")
	}
	return prefix + code
}

// hasIssues reports whether any line carries an E3.4 issue overlay, so
// Render can print the "E = issues whose culprit is this line" legend only
// when it's actually needed instead of on every CodeFrame.
func (f CodeFrame) hasIssues() bool {
	for _, l := range f.Lines {
		if len(l.Issues) > 0 {
			return true
		}
	}
	return false
}

func (f CodeFrame) hasLine(line int) bool {
	for _, l := range f.Lines {
		if l.Line == line {
			return true
		}
	}
	return false
}

// hottestLine returns the Line CodeFrame marks '>' when the caller doesn't
// set HotLine explicitly. In the dual-column (ShowCum) view, that's the
// highest CumPercent (ties broken by Percent): a pprof wrapper's own loop
// line routinely carries a little self time while the line beneath it (a
// call into the real hot work) carries almost all the cumulative cost — see
// mockup 7's json.Marshal line — so ranking by self (Percent) there would
// mark the wrong row. In the self-only view, it's the highest Percent (ties
// broken by CumPercent) — that view's Percent IS the only per-line weight
// there is (see HeatLine's doc comment on why a CDP source has no separate
// cum concept to break ties against otherwise). Either way, ties finally
// fall back to the first occurrence.
func (f CodeFrame) hottestLine() int {
	best := f.Lines[0]
	for _, l := range f.Lines[1:] {
		if f.ShowCum {
			if l.CumPercent > best.CumPercent || (l.CumPercent == best.CumPercent && l.Percent > best.Percent) {
				best = l
			}
			continue
		}
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
	var prefix string
	if f.HideMetrics {
		name := f.FuncName
		if f.Subtitle != "" {
			name = fmt.Sprintf("%s (%s)", f.FuncName, f.Subtitle)
		}
		prefix = fmt.Sprintf("-- %s · %s ", name, loc)
	} else {
		var headline string
		if f.ShowCum {
			headline = fmt.Sprintf("cum %.1f%% · flat %.1f%%", f.CumPct, f.SelfPct)
		} else {
			headline = fmt.Sprintf("%.1f%% self", f.SelfPct)
		}
		prefix = fmt.Sprintf("-- %s · %s · %s ", f.FuncName, loc, headline)
	}
	pad := width - len([]rune(prefix))
	if pad < 0 {
		pad = 0
	}
	return f.style(frameHeaderStyle, prefix+strings.Repeat("-", pad))
}

// pctFieldWidth is the fixed width of one rendered "NNN.N%" percentage
// field (SELF, CUM, or the self-only view's PCT) — always 7 runes ("%6.1f"
// pads the numeric part to 6, plus the literal "%"), so the FLAT/CUM column
// header can line its own labels up over the numbers without having to
// duplicate the row format's own field widths.
const pctFieldWidth = 7

// dualColumnSep separates the two "%s %s" percentage fields, and again
// separates the percentage block from "| " before the code column, in both
// renderLine (ShowCum) and renderColumnHeader — kept as one named constant
// so the two stay in lockstep.
const dualColumnSep = " "

func (f CodeFrame) renderColumnHeader() string {
	lineNumWidth := f.lineNumWidth()
	// Mirrors renderLine's own ShowCum prefix exactly: marker(1) + " " +
	// numStr(lineNumWidth) + " | " — so "FLAT"/"CUM" land directly over
	// their own column's numbers on every row beneath this header.
	gutter := strings.Repeat(" ", 1+1+lineNumWidth+3)
	header := gutter + fmt.Sprintf("%*s%s%*s", pctFieldWidth, "FLAT", dualColumnSep, pctFieldWidth, "CUM")
	return f.style(frameDimStyle, header)
}

// renderLine renders one source line: marker, line number, code (padded/
// truncated to fill the width remaining after every fixed column), and
// either one PCT|BAR column (self-only) or FLAT/CUM before the code, then a
// CUM-sized bar (dual-column pprof view — see mockup 7 in the roadmap,
// where the percentages read left of the code, not right of it).
func (f CodeFrame) renderLine(l CodeFrameLine, isHot bool, lineNumWidth, width int) string {
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
	issueSuffix := f.renderIssueSuffix(l)
	// The suffix's own display width, measured on the PLAIN text (never
	// f.style's ANSI-escaped form, whose byte length has nothing to do with
	// what a terminal actually renders) — subtracted from the bar's budget
	// below so a marked line stays within width instead of overflowing it
	// (see the E3.1 golden-width review finding: an "E 5C1D x10 open" line
	// used to render 17+ runes past every other row).
	issueSuffixWidth := ansi.StringWidth(f.issueSuffixText(l))

	if f.ShowCum {
		// Fixed (non-bar) columns: marker(1) space num(lineNumWidth)
		// " | "(3) FLAT(7) sep(1) CUM(7) " | "(3) code(codeColumnWidth)
		// " "(1) issueSuffix(issueSuffixWidth) — whatever's left of width
		// goes to the bar.
		fixed := 1 + 1 + lineNumWidth + 3 + pctFieldWidth + len(dualColumnSep) + pctFieldWidth + 3 + codeColumnWidth + 1 + issueSuffixWidth
		// A bar sized by SELF would be empty on exactly the rows CUM makes
		// interesting: a pprof wrapper's SELF is routinely ~0 (see
		// profiler_test.go's TestSymbolsFromPprofWrapperHasZeroFlatButFullCum).
		// Both numbers are still printed, in FLAT-then-CUM column order.
		cumBar := f.style(frameCumBarStyle, bar(l.CumPercent, barWidth(width, fixed)))
		return fmt.Sprintf("%s %s | %*.1f%%%s%*.1f%% | %s %s%s",
			marker, numStr, pctFieldWidth-1, l.Percent, dualColumnSep, pctFieldWidth-1, l.CumPercent, code, cumBar, issueSuffix)
	}

	// Fixed (non-bar) columns: marker(1) space num(lineNumWidth) " | "(3)
	// code(codeColumnWidth) " "(1) pct(6) " |"(2) issueSuffix(issueSuffixWidth).
	fixed := 1 + 1 + lineNumWidth + 3 + codeColumnWidth + 1 + 6 + 2 + issueSuffixWidth
	pctStr := fmt.Sprintf("%5.1f%%", l.Percent)
	barStr := f.style(frameBarStyle, bar(l.Percent, barWidth(width, fixed)))
	return fmt.Sprintf("%s %s | %s %s |%s%s", marker, numStr, code, pctStr, barStr, issueSuffix)
}

// issueSuffixText is renderIssueSuffix's PLAIN (unstyled) text — "  E
// <entry>[, <entry>...]", the roadmap mockup's "E 5C1D x10 open" marker —
// used both to render the suffix and, via its own display width, to size
// down the bar budget that precedes it (see renderLine). "" when l carries
// no issue overlay.
func (f CodeFrame) issueSuffixText(l CodeFrameLine) string {
	if len(l.Issues) == 0 {
		return ""
	}
	return "  E " + strings.Join(l.Issues, ", ")
}

// renderIssueSuffix renders issueSuffixText, styled, appended after a
// line's bar. "" when l carries no issue overlay, so a CodeFrame with no
// E3.4 data renders byte-for-byte the same as before this field existed.
func (f CodeFrame) renderIssueSuffix(l CodeFrameLine) string {
	text := f.issueSuffixText(l)
	if text == "" {
		return ""
	}
	return f.style(frameHotStyle, text)
}

// barWidth is however much of width the fixed (non-bar) columns leave over,
// clamped to >=0 so a caller-chosen Width smaller than the fixed columns
// alone degrades to "no bar" instead of a negative repeat count.
func barWidth(width, fixed int) int {
	w := width - fixed
	if w < 0 {
		return 0
	}
	return w
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

// codeTabWidth is the column stop codeColumn expands a literal tab
// (indented Go source, tab-indented by convention) to before measuring or
// truncating it — real terminals disagree on a raw tab's own width, so
// leaving it unexpanded would make every column after it misalign.
const codeTabWidth = 4

// padCode right-pads code to exactly width CELLS (not runes: this must
// count a wide CJK rune as 2 and an ANSI escape as 0, via
// charmbracelet/x/ansi, the same width accounting the terminal itself
// uses — a rune-counting pad silently misaligns the PCT|BAR column that
// follows on any line with a tab or a wide character), or truncates it
// with a trailing ellipsis when wider, after first expanding any literal
// tabs to codeTabWidth-column stops.
func padCode(code string, width int) string {
	if width <= 0 {
		return ""
	}
	code = expandTabs(code, codeTabWidth)
	w := ansi.StringWidth(code)
	if w == width {
		return code
	}
	if w < width {
		return code + strings.Repeat(" ", width-w)
	}
	if width <= 1 {
		return ansi.Truncate(code, width, "")
	}
	return ansi.Truncate(code, width, "…")
}

// expandTabs replaces every '\t' in s with spaces up to the next
// tabWidth-column stop, measuring the columns consumed so far by display
// width (via charmbracelet/x/ansi) rather than byte or rune count, so a
// tab after a wide CJK character still lands on the right stop.
func expandTabs(s string, tabWidth int) string {
	if tabWidth <= 0 || !strings.ContainsRune(s, '\t') {
		return s
	}
	var b strings.Builder
	col := 0
	for _, r := range s {
		if r == '\t' {
			n := tabWidth - (col % tabWidth)
			b.WriteString(strings.Repeat(" ", n))
			col += n
			continue
		}
		b.WriteRune(r)
		col += ansi.StringWidth(string(r))
	}
	return b.String()
}
