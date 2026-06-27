package v2

import (
	"fmt"

	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

func (m Model) renderMemory() string {
	mem := m.last.Memory
	memBar := widgets.NewBarGauge()
	memBar.Value = mem.UsagePercent
	memBar.Width = m.width - 20
	memBar.ShowPercent = true
	memBar.ColorFunc = func(v float64) string {
		if v >= 90 {
			return "#BF616A"
		}
		if v >= 70 {
			return "#EBCB8B"
		}
		return "#A3BE8C"
	}
	swapBar := widgets.NewBarGauge()
	if mem.SwapTotal > 0 {
		swapBar.Value = float64(mem.SwapUsed) / float64(mem.SwapTotal) * 100
	}
	swapBar.Width = m.width - 20
	swapBar.ShowPercent = true
	memPanel := m.panelStyle.Width(m.width - 4).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Physical Memory "), "", memBar.Render(),
			fmt.Sprintf("  Total: %s    Used: %s    Available: %s",
				collector.FormatBytes(mem.TotalBytes), collector.FormatBytes(mem.UsedBytes), collector.FormatBytes(mem.AvailableBytes))))
	swapPanel := m.panelStyle.Width(m.width - 4).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Swap "), "", swapBar.Render(),
			fmt.Sprintf("  Total: %s    Used: %s    Free: %s",
				collector.FormatBytes(mem.SwapTotal), collector.FormatBytes(mem.SwapUsed), collector.FormatBytes(mem.SwapFree))))
	return lipgloss.JoinVertical(lipgloss.Left, memPanel, "", swapPanel)
}
