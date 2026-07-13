package studio

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

func metricPanelWidth(width int) int {
	width -= 4
	if width < 20 {
		return 20
	}
	return width
}

func metricBarWidth(width int) int {
	width -= 20
	if width < 8 {
		return 8
	}
	return width
}

func fitMetricText(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "…"
}

func (m Model) renderMetricStatePanel(title, state, reason string, retry bool) string {
	panelWidth := metricPanelWidth(m.width)
	bodyWidth := panelWidth - 4
	if bodyWidth < 12 {
		bodyWidth = 12
	}
	lines := []string{m.titleStyle.Render(" " + title + " "), "", "  " + state}
	if reason = strings.TrimSpace(reason); reason != "" {
		for _, line := range wrapDetailValue(reason, bodyWidth-2) {
			lines = append(lines, "  "+line)
		}
	}
	if retry {
		lines = append(lines, "")
		if m.width < 64 {
			lines = append(lines, "  r retry · monitor doctor")
		} else {
			lines = append(lines, "  Press r to retry; monitor doctor checks collector health.")
		}
	}
	return m.panelStyle.Width(panelWidth).Render(strings.Join(lines, "\n"))
}

func (m Model) renderMemory() string {
	if m.last.LastUpdate.IsZero() {
		return m.renderMetricStatePanel("Memory", "Waiting for first memory sample…", "", false)
	}

	mem := m.last.Memory
	panelWidth := metricPanelWidth(m.width)
	panels := make([]string, 0, 2)
	if issue := metricIssue(mem.MetricStates, "virtual"); issue != "" {
		panels = append(panels, m.renderMetricStatePanel("Physical Memory", "Unavailable", issue, true))
	} else {
		memBar := widgets.NewBarGauge()
		memBar.Value = mem.UsagePercent
		memBar.Width = metricBarWidth(m.width)
		memBar.ShowPercent = true
		memBar.ColorFunc = func(v float64) string {
			switch {
			case v >= 90:
				return "#BF616A"
			case v >= 70:
				return "#EBCB8B"
			default:
				return "#A3BE8C"
			}
		}
		stats := fmt.Sprintf("  Total: %s    Used: %s    Available: %s",
			collector.FormatBytes(mem.TotalBytes), collector.FormatBytes(mem.UsedBytes), collector.FormatBytes(mem.AvailableBytes))
		if m.width < 64 {
			stats = fmt.Sprintf("  Total %s · Used %s\n  Available %s",
				collector.FormatBytes(mem.TotalBytes), collector.FormatBytes(mem.UsedBytes), collector.FormatBytes(mem.AvailableBytes))
		}
		panels = append(panels, m.panelStyle.Width(panelWidth).Render(
			lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Physical Memory "), "", memBar.Render(), stats)))
	}

	if issue := metricIssue(mem.MetricStates, "swap"); issue != "" {
		panels = append(panels, m.renderMetricStatePanel("Swap", "Unavailable", issue, true))
	} else if mem.SwapTotal == 0 {
		panels = append(panels, m.panelStyle.Width(panelWidth).Render(
			lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Swap "), "", "  No swap configured · observed total 0 B")))
	} else {
		swapBar := widgets.NewBarGauge()
		swapBar.Value = float64(mem.SwapUsed) / float64(mem.SwapTotal) * 100
		swapBar.Width = metricBarWidth(m.width)
		swapBar.ShowPercent = true
		stats := fmt.Sprintf("  Total: %s    Used: %s    Free: %s",
			collector.FormatBytes(mem.SwapTotal), collector.FormatBytes(mem.SwapUsed), collector.FormatBytes(mem.SwapFree))
		if m.width < 64 {
			stats = fmt.Sprintf("  Total %s · Used %s\n  Free %s",
				collector.FormatBytes(mem.SwapTotal), collector.FormatBytes(mem.SwapUsed), collector.FormatBytes(mem.SwapFree))
		}
		panels = append(panels, m.panelStyle.Width(panelWidth).Render(
			lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Swap "), "", swapBar.Render(), stats)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, intersperseMetricPanels(panels)...)
}

func intersperseMetricPanels(panels []string) []string {
	if len(panels) < 2 {
		return panels
	}
	out := make([]string, 0, len(panels)*2-1)
	for i, panel := range panels {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, panel)
	}
	return out
}
