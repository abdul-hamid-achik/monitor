package studio

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

func (m Model) renderDisk() string {
	var partitionLines []string
	for _, partition := range m.last.Disk.Partitions {
		if partition.MountPoint == "" {
			continue
		}
		bar := widgets.NewBarGauge()
		bar.Value = partition.UsagePercent
		bar.Width = 25
		bar.ShowPercent = true
		bar.ColorFunc = func(v float64) string {
			if v >= 90 {
				return "#BF616A"
			}
			if v >= 70 {
				return "#EBCB8B"
			}
			return "#A3BE8C"
		}
		line := fmt.Sprintf("  %-15s %s  %s / %s",
			partition.MountPoint, bar.Render(),
			collector.FormatBytes(partition.UsedBytes),
			collector.FormatBytes(partition.TotalBytes))
		partitionLines = append(partitionLines, line)
	}
	spark := widgets.NewSparkline()
	spark.Data = m.last.Disk.ReadHistory
	spark.Width = m.width - 20
	spark.Height = 6
	spark.Color = "#88C0D0"
	usagePanel := m.panelStyle.Width(m.width - 4).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Disk Usage "), "", strings.Join(partitionLines, "\n")))
	ioPanel := m.panelStyle.Width(m.width - 4).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Disk I/O "), "", spark.Render(),
			fmt.Sprintf("  Read: %s/s    Write: %s/s", collector.FormatBytes(m.last.Disk.ReadPerSec), collector.FormatBytes(m.last.Disk.WritePerSec))))
	return lipgloss.JoinVertical(lipgloss.Left, usagePanel, "", ioPanel)
}
