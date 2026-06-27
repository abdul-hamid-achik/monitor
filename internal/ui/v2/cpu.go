package v2

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

func (m Model) renderCPU() string {
	cpu := m.last.CPU
	spark := widgets.NewSparkline()
	spark.Data = cpu.History
	spark.Width = m.width - 20
	spark.Height = 8
	spark.Color = "#88C0D0"
	sparkline := m.panelStyle.Width(m.width - 4).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" CPU Usage History "), "", spark.Render()))
	var coreBars []string
	limit := 8
	if len(cpu.PerCoreUsage) < limit {
		limit = len(cpu.PerCoreUsage)
	}
	for i := 0; i < limit; i++ {
		bar := widgets.NewBarGauge()
		bar.Value = cpu.PerCoreUsage[i]
		bar.Width = 25
		bar.ShowPercent = true
		bar.ColorFunc = func(v float64) string {
			if v >= 80 {
				return "#BF616A"
			}
			if v >= 50 {
				return "#EBCB8B"
			}
			return "#A3BE8C"
		}
		coreBars = append(coreBars, fmt.Sprintf("  Core %d: %s", i, bar.Render()))
	}
	coresPanel := m.panelStyle.Width((m.width - 6) / 2).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Per-Core Usage "), "", strings.Join(coreBars, "\n")))
	statsPanel := m.panelStyle.Width((m.width - 6) / 2).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Statistics "), "",
			fmt.Sprintf("  Usage: %.1f%%", cpu.UsagePercent),
			fmt.Sprintf("  Frequency: %.2f GHz", cpu.FrequencyMHz/1000),
			fmt.Sprintf("  Cores: %d", cpu.CoreCount),
			fmt.Sprintf("  Threads: %d", cpu.ThreadCount)))
	return lipgloss.JoinVertical(lipgloss.Left, sparkline, "", lipgloss.JoinHorizontal(lipgloss.Top, coresPanel, statsPanel))
}