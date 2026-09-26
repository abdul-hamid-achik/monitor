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

// diskPartitionLimit is how many volume rows fit while leaving the Disk I/O
// panel fully visible below: the content area (height minus the 3-row header
// and the footer) less the I/O panel (~7 rows), the gap, this panel's
// borders and its "+N more" line.
func diskPartitionLimit(width, height int) int {
	if width < 64 || (height > 0 && height < 22) {
		return 3
	}
	if height <= 0 {
		return 12
	}
	return maxInt(3, minInt(12, height-4-7-1-2-1))
}

// diskPartitionLine renders one volume as aligned columns: mount (padded to
// mountWidth so every bar starts in the same column), usage bar, percent,
// and used/total -- the layout of the site's Studio replica.
func (m Model) diskPartitionLine(partition collector.DiskPartitionInfo, panelWidth, mountWidth int, narrow bool) string {
	innerWidth := panelWidth - 4
	pct := lipgloss.NewStyle().Foreground(lipgloss.Color(m.theme.gaugeHex(partition.UsagePercent, 70, 90))).
		Render(fmt.Sprintf("%5.1f%%", partition.UsagePercent))
	if narrow {
		stats := pct + " " + m.muted(collector.FormatBytes(partition.TotalBytes))
		width := maxInt(5, innerWidth-lipgloss.Width(stats)-3)
		return "  " + padRight(fitMetricText(partition.MountPoint, width), width) + " " + stats
	}
	sizes := m.muted(fmt.Sprintf("%9s / %-9s", collector.FormatBytes(partition.UsedBytes), collector.FormatBytes(partition.TotalBytes)))
	barWidth := innerWidth - mountWidth - lipgloss.Width(pct) - lipgloss.Width(sizes) - 6
	if barWidth > 32 {
		barWidth = 32
	}
	if barWidth < 6 {
		barWidth = 6
	}
	return "  " + padRight(fitMetricText(partition.MountPoint, mountWidth), mountWidth) + "  " +
		m.gauge(partition.UsagePercent, barWidth, 70, 90) + " " + pct + "  " + sizes
}

// diskMountWidth is the mount column width: the longest visible mount point,
// capped so the bar and figures keep at least half the row.
func diskMountWidth(partitions []collector.DiskPartitionInfo, panelWidth int) int {
	widest := 1
	for _, p := range partitions {
		widest = maxInt(widest, len([]rune(p.MountPoint)))
	}
	return minInt(widest, maxInt(8, (panelWidth-4)/2-4))
}

func padRight(s string, width int) string {
	if pad := width - lipgloss.Width(s); pad > 0 {
		return s + strings.Repeat(" ", pad)
	}
	return s
}

func (m Model) diskRateLines(read, write string, narrow bool) []string {
	if narrow {
		return []string{m.kv("read", 6, read), m.kv("write", 6, write)}
	}
	return []string{m.statLine(false, [2]string{"read", m.bigValue(read)}, [2]string{"write", m.bigValue(write)})}
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
			mountWidth := diskMountWidth(partitions[:visible], panelWidth)
			for _, partition := range partitions[:visible] {
				lines = append(lines, m.diskPartitionLine(partition, panelWidth, mountWidth, narrow))
			}
			if hidden := len(partitions) - visible; hidden > 0 {
				if narrow {
					lines = append(lines, fmt.Sprintf("  +%d more volumes", hidden))
				} else {
					lines = append(lines, "  "+m.muted(fmt.Sprintf("+%d more volumes · enlarge terminal", hidden)))
				}
			}
		}
		panels = append(panels, m.panel(panelWidth, lipgloss.JoinVertical(lipgloss.Left, lines...)))
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
		lines = append(lines, m.diskRateLines(
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
			history.Colors = []string{m.theme.hex(m.theme.Accent), m.theme.hex(m.theme.Good)}
			history.AlignRight = true
			history.Capacity = studioHistorySize
			history.LabelColor = m.theme.hex(m.theme.Muted)
			history.Width = panelWidth - 18
			if history.Width < 8 {
				history.Width = 8
			}
			lines = append(lines, "", "  "+m.muted("recent rates · shared scale"), history.Render())
		}
		panels = append(panels, m.panel(panelWidth, lipgloss.JoinVertical(lipgloss.Left, lines...)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, intersperseMetricPanels(panels)...)
}
