package studio

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// renderProcessDetail renders a read-only, PID-pinned diagnostic panel. It is
// intentionally modal so a key intended to dismiss or inspect it cannot
// trigger a destructive action in the process table underneath.
func (m Model) renderProcessDetail() string {
	panelWidth := m.width - 4
	if panelWidth > 86 {
		panelWidth = 86
	}
	if panelWidth < 20 {
		panelWidth = 20
	}
	if m.width > 0 && panelWidth > m.width {
		panelWidth = m.width
	}
	contentWidth := maxInt(1, panelWidth-m.panelStyle.GetHorizontalFrameSize())
	contentBudget := m.processDetailContentBudget()
	short := contentBudget <= 21

	p, ok := m.processByPID(m.processDetailPID)
	if !ok {
		body := lipgloss.JoinVertical(lipgloss.Left,
			detailFitLine(m.titleStyle.Render(fmt.Sprintf(" Process Detail · PID %d ", m.processDetailPID)), contentWidth),
			"",
			detailFitLine("This process is no longer present in the latest snapshot.", contentWidth),
			detailFitLine("It may have exited or process collection may be unavailable.", contentWidth),
			"",
			m.processDetailFooter(contentWidth),
		)
		return m.centerProcessDetail(m.panelStyle.Width(panelWidth).Render(body))
	}

	// A short narrow terminal gets a prioritized, single-line metric list.
	// This guarantees that safety policy and controls stay visible without
	// introducing modal scrolling state.
	if short && m.width < 78 {
		body := m.renderCompactProcessDetail(p, contentWidth)
		return m.centerProcessDetail(m.panelStyle.Width(panelWidth).Render(body))
	}

	safety, safetyNote := m.processSafety(p)
	title := fmt.Sprintf(" Process Detail · %s · PID %d ", displayProcessName(p), p.PID)
	twoColumn := m.width >= 78
	rowWidth := contentWidth
	if twoColumn {
		rowWidth = (contentWidth - 2) / 2
	}
	row := detailRow
	if short {
		// Long unavailable reasons are useful in a tall detail view, but must
		// stay on one line in a bounded short-terminal presentation.
		row = detailRowSingle
	}
	identity := []string{
		m.titleStyle.Render(" Identity "),
		"",
		row("PID", fmt.Sprintf("%d", p.PID), rowWidth),
		row("Name", processStringMetric(p, "name", p.Name, "process name was not reported"), rowWidth),
		row("User", processStringMetric(p, "user", p.User, "process owner was not reported"), rowWidth),
		row("Status", processStatusMetric(p), rowWidth),
		row("Parent", processParentMetric(p), rowWidth),
	}
	resources := []string{
		m.titleStyle.Render(" Resources "),
		"",
		row("CPU", processMetric(p, []string{"cpu"}, fmt.Sprintf("%.1f%%", p.CPUPercent)), rowWidth),
		row("RSS", processMetric(p, []string{"memory"}, collector.FormatBytes(p.Memory)), rowWidth),
		row("Mem share", processMetric(p, []string{"memory_percent"}, fmt.Sprintf("%.1f%%", p.MemoryPercent)), rowWidth),
		row("I/O read", processMetric(p, []string{"io_read", "io"}, collector.FormatBytes(p.IOReadBytes)), rowWidth),
		row("I/O write", processMetric(p, []string{"io_write", "io"}, collector.FormatBytes(p.IOWriteBytes)), rowWidth),
		row("Threads", processMetric(p, []string{"threads"}, fmt.Sprintf("%d", p.Threads)), rowWidth),
	}

	var metrics string
	if twoColumn {
		left := lipgloss.NewStyle().Width(rowWidth).Render(strings.Join(identity, "\n"))
		right := lipgloss.NewStyle().Width(rowWidth).Render(strings.Join(resources, "\n"))
		metrics = lipgloss.JoinHorizontal(lipgloss.Top, left, "  ", right)
	} else {
		metrics = lipgloss.JoinVertical(lipgloss.Left, strings.Join(identity, "\n"), "", strings.Join(resources, "\n"))
	}

	safetyLine := detailFitLine(fmt.Sprintf("  Protection  %s · %s", safety, safetyNote), contentWidth)
	body := lipgloss.JoinVertical(lipgloss.Left,
		detailFitLine(m.titleStyle.Render(title), contentWidth),
		"",
		metrics,
		"",
		safetyLine,
		"",
		m.processDetailFooter(contentWidth),
	)
	panel := m.panelStyle.Width(panelWidth).Render(body)
	if lipgloss.Height(panel) > contentBudget {
		// Defensive fallback for unusually verbose platform reasons. The
		// compact body is intentionally complete enough to diagnose and safe
		// enough to act, while remaining bounded.
		panel = m.panelStyle.Width(panelWidth).Render(m.renderCompactProcessDetail(p, contentWidth))
	}
	return m.centerProcessDetail(panel)
}

func (m Model) centerProcessDetail(panel string) string {
	width := m.width
	if width < 1 {
		width = lipgloss.Width(panel)
	}
	height := m.processDetailContentBudget()
	if height < 1 {
		height = lipgloss.Height(panel)
	}
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, panel)
}

func (m Model) processDetailContentBudget() int {
	// Studio's frame reserves two header rows and one footer row.
	return m.height - 3
}

func (m Model) renderCompactProcessDetail(p collector.ProcessInfo, width int) string {
	safety, _ := m.processSafety(p)
	safetyAction := "confirm"
	if p.IsProtected || p.IsSystem {
		safetyAction = "blocked"
	}
	rows := []string{
		detailFitLine(m.titleStyle.Render(fmt.Sprintf(" Process Detail · %s · PID %d ", displayProcessName(p), p.PID)), width),
		detailRowSingle("PID", fmt.Sprintf("%d", p.PID), width),
		detailRowSingle("Name", processStringMetric(p, "name", p.Name, "process name was not reported"), width),
		detailRowSingle("User", processStringMetric(p, "user", p.User, "process owner was not reported"), width),
		detailRowSingle("Status", processStatusMetric(p), width),
		detailRowSingle("Parent", processParentMetric(p), width),
		detailRowSingle("CPU", processMetric(p, []string{"cpu"}, fmt.Sprintf("%.1f%%", p.CPUPercent)), width),
		detailRowSingle("RSS", processMetric(p, []string{"memory"}, collector.FormatBytes(p.Memory)), width),
		detailRowSingle("Mem share", processMetric(p, []string{"memory_percent"}, fmt.Sprintf("%.1f%%", p.MemoryPercent)), width),
		detailRowSingle("Threads", processMetric(p, []string{"threads"}, fmt.Sprintf("%d", p.Threads)), width),
		detailFitLine(fmt.Sprintf("  Protection  %s · %s", safety, safetyAction), width),
		m.processDetailFooter(width),
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m Model) processDetailFooter(width int) string {
	text := "  Enter / Esc / q close | r refresh | ? help"
	if width < 46 {
		text = "Esc close | r refresh | ? help"
	}
	return lipgloss.NewStyle().Foreground(m.theme.Good).Render(detailFitLine(text, width))
}

func (m Model) processByPID(pid int32) (collector.ProcessInfo, bool) {
	for _, p := range m.last.Processes {
		if p.PID == pid {
			return p, true
		}
	}
	return collector.ProcessInfo{}, false
}

func detailRow(label, value string, width int) string {
	prefix := fmt.Sprintf("  %-10s ", label)
	available := width - len([]rune(prefix))
	if available < 6 {
		available = 6
	}
	wrapped := wrapDetailValue(value, available)
	lines := make([]string, 0, len(wrapped))
	for i, line := range wrapped {
		if i == 0 {
			lines = append(lines, prefix+line)
		} else {
			lines = append(lines, strings.Repeat(" ", len([]rune(prefix)))+line)
		}
	}
	return strings.Join(lines, "\n")
}

// detailRowSingle is the bounded counterpart to detailRow. It keeps the
// label and the most useful prefix of a value on one terminal row.
func detailRowSingle(label, value string, width int) string {
	prefix := fmt.Sprintf("  %-10s ", label)
	if width <= len([]rune(prefix)) {
		return detailFitLine(strings.TrimSpace(label)+" "+value, width)
	}
	return prefix + detailFitLine(value, width-len([]rune(prefix)))
}

func detailFitLine(value string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(value) <= width {
		return value
	}
	tail := ""
	if width >= 4 {
		tail = "..."
	}
	return ansi.Truncate(value, width, tail)
}

func wrapDetailValue(value string, width int) []string {
	words := strings.Fields(value)
	if len(words) == 0 {
		return []string{"-"}
	}
	var lines []string
	current := ""
	for _, word := range words {
		// Split an unusually long token so the containing Lipgloss column never
		// performs an unindented implicit wrap.
		for len([]rune(word)) > width {
			if current != "" {
				lines = append(lines, current)
				current = ""
			}
			runes := []rune(word)
			lines = append(lines, string(runes[:width]))
			word = string(runes[width:])
		}
		if current == "" {
			current = word
			continue
		}
		if len([]rune(current))+1+len([]rune(word)) <= width {
			current += " " + word
			continue
		}
		lines = append(lines, current)
		current = word
	}
	if current != "" {
		lines = append(lines, current)
	}
	return lines
}

func displayProcessName(p collector.ProcessInfo) string {
	if strings.TrimSpace(p.Name) == "" {
		return "unnamed process"
	}
	return p.Name
}

func processStringMetric(p collector.ProcessInfo, key, value, fallback string) string {
	if issue := processMetricIssue(p, key); issue != "" {
		return issue
	}
	if strings.TrimSpace(value) == "" {
		return "unavailable · " + fallback
	}
	return value
}

func processStatusMetric(p collector.ProcessInfo) string {
	value := processStringMetric(p, "status", p.Status, "process status was not reported")
	if strings.HasPrefix(value, "unavailable ·") || value == "" {
		return value
	}
	runes := []rune(value)
	code := strings.ToUpper(string(runes[0]))
	description := map[string]string{
		"R": "running",
		"S": "sleeping",
		"D": "uninterruptible wait",
		"U": "uninterruptible wait",
		"T": "stopped or traced",
		"Z": "zombie",
		"I": "idle",
	}[code]
	if description == "" || len(runes) > 2 {
		return value
	}
	return code + " · " + description
}

func processParentMetric(p collector.ProcessInfo) string {
	if issue := processMetricIssue(p, "parent"); issue != "" {
		return issue
	}
	if p.Parent <= 0 {
		if status, ok := p.MetricStates["parent"]; ok && status.State == collector.MetricObserved {
			return "PID 0 · kernel/root parent"
		}
		return "unavailable · parent PID was not reported"
	}
	return fmt.Sprintf("PID %d", p.Parent)
}

// processMetric accepts aliases because process I/O collectors commonly expose
// either one aggregate status ("io") or separate read/write statuses. Missing
// status entries preserve backward compatibility: the numeric zero is then a
// legitimate observed value rather than an invented failure.
func processMetric(p collector.ProcessInfo, keys []string, value string) string {
	for _, key := range keys {
		if _, ok := p.MetricStates[key]; ok {
			if issue := processMetricIssue(p, key); issue != "" {
				return issue
			}
			break
		}
	}
	return value
}

func processMetricIssue(p collector.ProcessInfo, key string) string {
	status, ok := p.MetricStates[key]
	if !ok || status.State == "" || status.State == collector.MetricObserved {
		return ""
	}
	reason := strings.TrimSpace(status.Reason)
	if reason == "" {
		reason = string(status.State)
	}
	return "unavailable · " + reason
}

func (m Model) processSafety(p collector.ProcessInfo) (string, string) {
	switch {
	case p.IsProtected:
		return lipgloss.NewStyle().Bold(true).Foreground(m.theme.Critical).Render("PROTECTED"), "termination is blocked"
	case p.IsSystem:
		return lipgloss.NewStyle().Bold(true).Foreground(m.theme.Warning).Render("SYSTEM"), "termination is blocked by policy"
	default:
		return lipgloss.NewStyle().Bold(true).Foreground(m.theme.Good).Render("USER"), "termination requires confirmation"
	}
}
