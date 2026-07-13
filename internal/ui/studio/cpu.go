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
		return m.panelStyle.Width(maxInt(20, m.width-4)).Render(
			m.titleStyle.Render(" CPU ") + "\n\n  Waiting for the first CPU sample…",
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
	sparkline := m.panelStyle.Width(maxInt(20, m.width-4)).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" CPU Usage History "), "", historyBody),
	)

	wide := m.width >= 92
	statsWidth := 31
	coresWidth := m.width - 4
	if wide {
		coresWidth = m.width - statsWidth - 7
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
	coreParts := []string{m.titleStyle.Render(" Per-Core Usage "), "", coreBody}
	if shortStack {
		summary := fmt.Sprintf("  %.1f%% total · %.2f GHz · %d cores / %d threads",
			cpu.UsagePercent, cpu.FrequencyMHz/1000, cpu.CoreCount, cpu.ThreadCount)
		if issue := metricIssue(cpu.MetricStates, "info"); issue != "" {
			coreParts = append(coreParts, "  CPU info unavailable · "+issue)
			summary = fmt.Sprintf("  %.1f%% total · CPU info unavailable", cpu.UsagePercent)
		}
		if issue := metricIssue(cpu.MetricStates, "load_average"); issue != "" {
			coreParts = append(coreParts, "  Load unavailable · "+issue)
		}
		coreParts = append(coreParts, "", fitText(summary, maxInt(1, coresWidth-m.panelStyle.GetHorizontalFrameSize())))
	}
	coresPanel := m.panelStyle.Width(coresWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, coreParts...),
	)
	statsPanel := m.panelStyle.Width(statsWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Statistics "), "", m.renderCPUStats(cpu)),
	)

	bottom := lipgloss.JoinVertical(lipgloss.Left, coresPanel, statsPanel)
	if wide {
		bottom = lipgloss.JoinHorizontal(lipgloss.Top, coresPanel, statsPanel)
	} else if shortStack {
		return lipgloss.JoinVertical(lipgloss.Left, sparkline, "", coresPanel)
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
	spark.Width = maxInt(8, m.width-20)
	spark.Height = height
	spark.Min = 0
	spark.Max = 100
	spark.AutoScale = false
	spark.Color = "#88C0D0"
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
			bar.ColorFunc = cpuGaugeColor
			cell := fmt.Sprintf("  Core %-2d %s", i, bar.Render())
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

func (m Model) renderCPUStats(cpu collector.CPUInfo) string {
	usage := fmt.Sprintf("%.1f%%", cpu.UsagePercent)
	if issue := metricIssue(cpu.MetricStates, "usage"); issue != "" {
		usage = "unavailable · " + issue
	}
	frequency := fmt.Sprintf("%.2f GHz", cpu.FrequencyMHz/1000)
	threads := fmt.Sprintf("%d", cpu.ThreadCount)
	if issue := metricIssue(cpu.MetricStates, "info"); issue != "" {
		frequency = "unavailable · " + issue
		threads = "unavailable · " + issue
	}
	cores := fmt.Sprintf("%d", cpu.CoreCount)
	if issue := metricIssue(cpu.MetricStates, "per_core"); issue != "" {
		cores = "unavailable · " + issue
	}
	load := fmt.Sprintf("%.2f / %.2f / %.2f", cpu.LoadAvg1, cpu.LoadAvg5, cpu.LoadAvg15)
	if issue := metricIssue(cpu.MetricStates, "load_average"); issue != "" {
		load = "unavailable · " + issue
	}
	return strings.Join([]string{
		"  Usage: " + usage,
		"  Frequency: " + frequency,
		"  Cores: " + cores,
		"  Threads: " + threads,
		"  Load (1/5/15): " + load,
	}, "\n")
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

func cpuGaugeColor(v float64) string {
	if v >= 80 {
		return "#BF616A"
	}
	if v >= 50 {
		return "#EBCB8B"
	}
	return "#A3BE8C"
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
