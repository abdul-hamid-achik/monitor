package studio

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

func visiblePartitions(partitions []collector.DiskPartitionInfo) []collector.DiskPartitionInfo {
	out := make([]collector.DiskPartitionInfo, 0, len(partitions))
	for _, partition := range partitions {
		if partition.MountPoint != "" {
			out = append(out, partition)
		}
	}
	return out
}

func diskPartitionLimit(width, height int) int {
	if width < 64 || (height > 0 && height < 22) {
		return 3
	}
	if height > 0 && height <= 24 {
		return 6
	}
	return 12
}

func diskPartitionLine(partition collector.DiskPartitionInfo, panelWidth int, narrow bool) string {
	innerWidth := panelWidth - 4
	if narrow {
		stats := fmt.Sprintf("%5.1f%% · %s", partition.UsagePercent, collector.FormatBytes(partition.TotalBytes))
		mountWidth := innerWidth - len([]rune(stats)) - 3
		if mountWidth < 5 {
			mountWidth = 5
		}
		return "  " + fitMetricText(partition.MountPoint, mountWidth) + " " + stats
	}

	bar := widgets.NewBarGauge()
	bar.Value = partition.UsagePercent
	bar.Width = 12
	bar.ShowValue = false
	bar.ColorFunc = func(v float64) string {
		switch {
		case v >= 90:
			return "#BF616A"
		case v >= 70:
			return "#EBCB8B"
		default:
			return "#A3BE8C"
		}
	}
	stats := fmt.Sprintf("%5.1f%% %s/%s", partition.UsagePercent,
		collector.FormatBytes(partition.UsedBytes), collector.FormatBytes(partition.TotalBytes))
	mountWidth := innerWidth - lipgloss.Width(bar.Render()) - len([]rune(stats)) - 4
	if mountWidth < 8 {
		mountWidth = 8
	}
	return "  " + fitMetricText(partition.MountPoint, mountWidth) + " " + bar.Render() + " " + stats
}

func diskRateLines(read, write string, narrow bool) []string {
	if narrow {
		return []string{"  Read  " + read, "  Write " + write}
	}
	return []string{fmt.Sprintf("  Read: %s    Write: %s", read, write)}
}

func (m Model) renderDisk() string {
	if m.last.LastUpdate.IsZero() {
		return m.renderMetricStatePanel("Disk", "Waiting for first disk sample…", "", false)
	}

	disk := m.last.Disk
	panelWidth := metricPanelWidth(m.width)
	narrow := m.width < 64
	partitionIssue := metricIssue(disk.MetricStates, "partitions")
	rateIssue := metricIssue(disk.MetricStates, "rate")
	if rateIssue == "" {
		rateIssue = metricIssue(disk.MetricStates, "io")
	}
	if partitionIssue != "" && rateIssue != "" {
		reason := partitionIssue
		if rateIssue != partitionIssue {
			reason += "; I/O: " + rateIssue
		}
		return m.renderMetricStatePanel("Disk", "Unavailable", reason, true)
	}
	panels := make([]string, 0, 2)
	if partitionIssue != "" {
		panels = append(panels, m.renderMetricStatePanel("Disk Usage", "Unavailable", partitionIssue, true))
	} else {
		partitions := visiblePartitions(disk.Partitions)
		lines := []string{m.titleStyle.Render(" Disk Usage ")}
		if !narrow {
			lines = append(lines, "")
		}
		if len(partitions) == 0 {
			lines = append(lines, "  No mounted volumes were returned.", "  Press r to retry; monitor doctor checks collector health.")
		} else {
			limit := diskPartitionLimit(m.width, m.height)
			if narrow && rateIssue != "" {
				limit = 1
			}
			visible := len(partitions)
			if visible > limit {
				visible = limit
			}
			for _, partition := range partitions[:visible] {
				lines = append(lines, diskPartitionLine(partition, panelWidth, narrow))
			}
			if hidden := len(partitions) - visible; hidden > 0 {
				if narrow {
					lines = append(lines, fmt.Sprintf("  +%d more volumes", hidden))
				} else {
					lines = append(lines, fmt.Sprintf("  +%d more volumes · enlarge terminal", hidden))
				}
			}
		}
		panels = append(panels, m.panelStyle.Width(panelWidth).Render(lipgloss.JoinVertical(lipgloss.Left, lines...)))
	}

	if rateIssue != "" {
		state := "Rates unavailable"
		displayReason := rateIssue
		if strings.Contains(strings.ToLower(rateIssue), "first sample") {
			state = "Rates waiting for the next sample"
			displayReason = "No prior counter yet."
		}
		panels = append(panels, m.renderMetricStatePanel("Disk I/O", state, displayReason, true))
	} else {
		lines := []string{m.titleStyle.Render(" Disk I/O ")}
		if !narrow {
			lines = append(lines, "")
		}
		lines = append(lines, diskRateLines(
			collector.FormatBytes(disk.ReadPerSec)+"/s",
			collector.FormatBytes(disk.WritePerSec)+"/s", narrow)...)
		if narrow || (m.height > 0 && m.height < 22) {
			lines = append(lines, fmt.Sprintf("  History: R%d · W%d samples", len(disk.ReadHistory), len(disk.WriteHistory)))
		} else if len(disk.ReadHistory) == 0 && len(disk.WriteHistory) == 0 {
			lines = append(lines, "", "  History: collecting samples…")
		} else {
			history := widgets.NewMultiSparkline()
			history.Data = [][]float64{disk.ReadHistory, disk.WriteHistory}
			history.Labels = []string{"read", "write"}
			history.Colors = []string{"#88C0D0", "#A3BE8C"}
			history.Width = panelWidth - 18
			if history.Width < 8 {
				history.Width = 8
			}
			lines = append(lines, "", "  Recent rates · shared scale", history.Render())
		}
		panels = append(panels, m.panelStyle.Width(panelWidth).Render(lipgloss.JoinVertical(lipgloss.Left, lines...)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, intersperseMetricPanels(panels)...)
}
