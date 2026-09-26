package studio

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

func metricPanelWidth(width int) int {
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
	return m.panel(panelWidth, strings.Join(lines, "\n"))
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
		barWidth := maxInt(8, panelWidth-4)
		lines := []string{
			m.titleStyle.Render(" Physical Memory "),
			"  " + m.bigValue(fmt.Sprintf("%.1f%%", mem.UsagePercent)) + "  " +
				m.muted(fmt.Sprintf("%s of %s", collector.FormatBytes(mem.UsedBytes), collector.FormatBytes(mem.TotalBytes))),
			"  " + m.gauge(mem.UsagePercent, barWidth-2, 70, 90),
			m.statLine(m.width < 64,
				[2]string{"total", collector.FormatBytes(mem.TotalBytes)},
				[2]string{"used", collector.FormatBytes(mem.UsedBytes)},
				[2]string{"available", collector.FormatBytes(mem.AvailableBytes)}),
		}
		if len(mem.History) > 1 && m.height >= 28 {
			spark := widgets.NewSparkline()
			spark.Data = mem.History
			spark.Width = barWidth - 2
			spark.Height = 3
			spark.Min, spark.Max, spark.AutoScale = 0, 100, false
			spark.Color = m.theme.hex(m.theme.Accent)
			spark.AlignRight = true
			spark.Capacity = studioHistorySize
			lines = append(lines, "", "  "+m.muted("recent"), indentLines(spark.Render(), "  "))
		}
		panels = append(panels, m.panel(panelWidth, strings.Join(lines, "\n")))
	}

	if issue := metricIssue(mem.MetricStates, "swap"); issue != "" {
		panels = append(panels, m.renderMetricStatePanel("Swap", "Unavailable", issue, true))
	} else if mem.SwapTotal == 0 {
		panels = append(panels, m.panel(panelWidth,
			lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Swap "), "  "+m.muted("No swap configured · observed total 0 B"))))
	} else {
		swapPercent := float64(mem.SwapUsed) / float64(mem.SwapTotal) * 100
		lines := []string{
			m.titleStyle.Render(" Swap "),
			"  " + m.bigValue(fmt.Sprintf("%.1f%%", swapPercent)) + "  " +
				m.muted(fmt.Sprintf("%s of %s", collector.FormatBytes(mem.SwapUsed), collector.FormatBytes(mem.SwapTotal))),
			"  " + m.gauge(swapPercent, maxInt(8, panelWidth-6), 50, 80),
			m.statLine(m.width < 64,
				[2]string{"total", collector.FormatBytes(mem.SwapTotal)},
				[2]string{"used", collector.FormatBytes(mem.SwapUsed)},
				[2]string{"free", collector.FormatBytes(mem.SwapFree)}),
		}
		panels = append(panels, m.panel(panelWidth, strings.Join(lines, "\n")))
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
