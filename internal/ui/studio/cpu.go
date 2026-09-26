package studio

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

func (m Model) renderCPU() string {
	if m.last.LastUpdate.IsZero() {
		return m.panel(maxInt(20, m.width),
			m.titleStyle.Render(" CPU ")+"\n\n  Waiting for the first CPU sample…",
		)
	}

	cpu := m.last.CPU
	sparkHeight := 6
	if m.height < 32 {
		sparkHeight = 4
	}
	if m.height < 24 {
		sparkHeight = 2
	}
	historyBody := m.renderCPUHistory(cpu, sparkHeight)
	sparkline := m.panel(maxInt(20, m.width),
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" CPU Usage History "), historyBody),
	)

	wide := m.width >= 92
	statsWidth := 36
	coresWidth := m.width
	if wide {
		coresWidth = m.width - statsWidth - 1
	} else {
		statsWidth = m.width
	}
	if coresWidth < 28 {
		coresWidth = 28
	}
	maxCoreRows := m.height - sparkHeight - 12
	if !wide {
		// On a stacked layout reserve room for the statistics panel.
		maxCoreRows -= 9
	}
	shortStack := !wide && m.height < 30
	if shortStack {
		maxCoreRows = 2
		if m.height < 20 {
			maxCoreRows = 1
		}
	} else if maxCoreRows < 3 {
		maxCoreRows = 3
	}

	coreBody := m.renderCoreGrid(cpu, coresWidth, maxCoreRows)
	coreParts := []string{m.titleStyle.Render(" Per-Core Usage "), coreBody}
	if shortStack {
		clock := ""
		if cpu.FrequencyMHz >= 100 {
			clock = fmt.Sprintf(" · %.2f GHz", cpu.FrequencyMHz/1000)
		}
		summary := fmt.Sprintf("  %.1f%% total%s · %d cores / %d threads",
			cpu.UsagePercent, clock, cpu.CoreCount, cpu.ThreadCount)
		if issue := metricIssue(cpu.MetricStates, "info"); issue != "" {
			coreParts = append(coreParts, "  CPU info unavailable · "+issue)
			summary = fmt.Sprintf("  %.1f%% total · CPU info unavailable", cpu.UsagePercent)
		}
		if issue := metricIssue(cpu.MetricStates, "load_average"); issue != "" {
			coreParts = append(coreParts, fitText("  Load unavailable · "+issue, maxInt(1, coresWidth-m.panelStyle.GetHorizontalFrameSize())))
		}
		coreParts = append(coreParts, "", fitText(summary, maxInt(1, coresWidth-m.panelStyle.GetHorizontalFrameSize())))
	}
	coresPanel := m.panel(coresWidth,
		lipgloss.JoinVertical(lipgloss.Left, coreParts...),
	)
	statsPanel := m.panel(statsWidth,
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Statistics "), m.renderCPUStats(cpu, statsWidth)),
	)

	bottom := lipgloss.JoinVertical(lipgloss.Left, coresPanel, statsPanel)
	if wide {
		bottom = lipgloss.JoinHorizontal(lipgloss.Top, coresPanel, " ", statsPanel)
	} else if shortStack {
		return lipgloss.JoinVertical(lipgloss.Left, sparkline, "", coresPanel)
	}
	// Give the history chart whatever height the panels below leave free
	// (capped, so a tall terminal does not turn it into a wall of blocks).
	budget := m.height - 4 // header (3 rows) + footer
	if spare := budget - lipgloss.Height(sparkline) - 1 - lipgloss.Height(bottom); spare > 0 {
		grown := sparkHeight + minInt(spare, 24)
		sparkline = m.panel(maxInt(20, m.width),
			lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" CPU Usage History "), m.renderCPUHistory(cpu, grown)),
		)
	}
	return lipgloss.JoinVertical(lipgloss.Left, sparkline, "", bottom)
}

func (m Model) renderCPUHistory(cpu collector.CPUInfo, height int) string {
	if issue := metricIssue(cpu.MetricStates, "usage"); issue != "" {
		return "  CPU history unavailable · " + issue
	}
	if len(cpu.History) == 0 {
		return "  No CPU history yet · samples appear after the next refresh."
	}
	spark := widgets.NewSparkline()
	spark.Data = cpu.History
	spark.Width = maxInt(8, m.width-4)
	spark.Height = height
	spark.Min = 0
	spark.Max = 100
	spark.AutoScale = false
	spark.Color = m.theme.hex(m.theme.Accent)
	spark.AlignRight = true
	spark.Capacity = studioHistorySize
	return spark.Render()
}

// renderCoreGrid uses terminal width for multiple columns and terminal height
// for a row budget. This exposes all common Apple Silicon core counts instead
// of silently truncating at eight, while still stating when a small terminal
// cannot show every core.
func (m Model) renderCoreGrid(cpu collector.CPUInfo, width, maxRows int) string {
	if issue := metricIssue(cpu.MetricStates, "per_core"); issue != "" {
		return "  Per-core telemetry unavailable · " + issue
	}
	if len(cpu.PerCoreUsage) == 0 {
		return "  No per-core samples yet · press r to refresh."
	}

	columns := width / 36
	if columns < 1 {
		columns = 1
	}
	if columns > 4 {
		columns = 4
	}
	visible := len(cpu.PerCoreUsage)
	capacity := columns * maxRows
	if visible > capacity {
		visible = capacity
	}
	rows := (visible + columns - 1) / columns
	cellWidth := width / columns
	// Leave generous slack for the styled bar plus the numeric suffix. Some
	// terminal renderers account for ANSI resets conservatively at exact-width
	// boundaries, which otherwise wraps the percentage onto a second row.
	barWidth := cellWidth - 25
	if barWidth < 5 {
		barWidth = 5
	}
	if barWidth > 25 {
		barWidth = 25
	}

	lines := make([]string, 0, rows+1)
	for row := 0; row < rows; row++ {
		cells := make([]string, 0, columns)
		for column := 0; column < columns; column++ {
			i := column*rows + row
			if i >= visible {
				continue
			}
			bar := widgets.NewBarGauge()
			bar.Value = cpu.PerCoreUsage[i]
			bar.Width = barWidth
			bar.ShowPercent = true
			bar.ColorFunc = func(v float64) string { return m.theme.gaugeHex(v, 50, 80) }
			bar.TrackColor = m.theme.hex(m.theme.Border)
			cell := "  " + m.muted(fmt.Sprintf("Core %-2d", i)) + " " + bar.Render()
			cells = append(cells, lipgloss.NewStyle().Width(cellWidth).Render(cell))
		}
		lines = append(lines, lipgloss.JoinHorizontal(lipgloss.Top, cells...))
	}
	if hidden := len(cpu.PerCoreUsage) - visible; hidden > 0 {
		note := fmt.Sprintf("  +%d cores hidden · enlarge the terminal to inspect them", hidden)
		if width < 50 {
			note = fmt.Sprintf("  +%d cores hidden", hidden)
		}
		lines = append(lines, note)
	}
	return strings.Join(lines, "\n")
}

func (m Model) renderCPUStats(cpu collector.CPUInfo, width int) string {
	usage := m.bigValue(fmt.Sprintf("%.1f%%", cpu.UsagePercent))
	if issue := metricIssue(cpu.MetricStates, "usage"); issue != "" {
		usage = "unavailable · " + issue
	}
	// Some hosts report 0 or a tiny placeholder (4 MHz) through gopsutil;
	// like the Overview KPI, treat anything implausible as unavailable
	// rather than presenting it as a real clock.
	frequency := fmt.Sprintf("%.2f GHz", cpu.FrequencyMHz/1000)
	if cpu.FrequencyMHz < 100 {
		frequency = m.muted("unavailable")
	}
	threads := fmt.Sprintf("%d", cpu.ThreadCount)
	if issue := metricIssue(cpu.MetricStates, "info"); issue != "" {
		frequency = "unavailable · " + issue
		threads = "unavailable · " + issue
	}
	cores := fmt.Sprintf("%d", cpu.CoreCount)
	if issue := metricIssue(cpu.MetricStates, "per_core"); issue != "" {
		cores = "unavailable · " + issue
	}
	lines := []string{
		m.kv("usage", 9, usage),
		m.kv("clock", 9, frequency),
		m.kv("cores", 9, cores),
		m.kv("threads", 9, threads),
	}
	if issue := metricIssue(cpu.MetricStates, "load_average"); issue != "" {
		// A long platform reason wraps under the value column instead of
		// running off the panel edge.
		wrapped := wrapDetailValue(issue, maxInt(12, width-16))
		lines = append(lines, m.kv("load", 9, m.muted("unavailable")))
		for _, line := range wrapped {
			lines = append(lines, "            "+m.muted(line))
		}
	} else {
		lines = append(lines, m.kv("load", 9, fmt.Sprintf("%.2f  %.2f  %.2f", cpu.LoadAvg1, cpu.LoadAvg5, cpu.LoadAvg15)))
	}
	return strings.Join(lines, "\n")
}

func metricIssue(statuses map[string]collector.MetricStatus, key string) string {
	status, ok := statuses[key]
	if !ok || status.State == "" || status.State == collector.MetricObserved {
		return ""
	}
	reason := strings.TrimSpace(status.Reason)
	if reason != "" {
		return reason
	}
	return string(status.State)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
