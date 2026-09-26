package studio

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

func formatNumberShort(n uint64) string {
	if n >= 1_000_000_000 {
		return fmt.Sprintf("%.2fB", float64(n)/1_000_000_000)
	}
	if n >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(n)/1_000_000)
	}
	if n >= 1_000 {
		return fmt.Sprintf("%.2fK", float64(n)/1_000)
	}
	return fmt.Sprintf("%d", n)
}

func (m Model) directionalMetricLines(label, down, up string, narrow bool) []string {
	if narrow {
		return []string{m.kv(label, 8, "↓ "+down), m.kv("", 8, "↑ "+up)}
	}
	return []string{m.kv(label, 8, fmt.Sprintf("↓ %-12s ↑ %s", down, up))}
}

func (m Model) networkRateLines(download, upload string, narrow bool) []string {
	if narrow {
		return []string{m.kv("Download", 8, download), m.kv("Upload", 8, upload)}
	}
	return []string{m.statLine(false, [2]string{"Download", m.bigValue(download)}, [2]string{"Upload", m.bigValue(upload)})}
}

func (m Model) renderNetwork() string {
	if m.last.LastUpdate.IsZero() {
		return m.renderMetricStatePanel("Network", "Waiting for first network sample…", "", false)
	}

	net := m.last.Network
	if issue := metricIssue(net.MetricStates, "io"); issue != "" {
		return m.renderMetricStatePanel("Network", "Unavailable", issue, true)
	}

	panelWidth := metricPanelWidth(m.width)
	narrow := m.width < 64
	lines := []string{m.titleStyle.Render(" Network ")}
	rateIssue := metricIssue(net.MetricStates, "rate")
	if rateIssue != "" {
		state := "Rates unavailable"
		displayReason := rateIssue
		if strings.Contains(strings.ToLower(rateIssue), "first sample") {
			state = "Rates waiting for the next sample"
			displayReason = "No prior counter yet."
		}
		lines = append(lines, "  "+state)
		for _, line := range wrapDetailValue(displayReason, maxInt(12, panelWidth-6)) {
			lines = append(lines, "  "+line)
		}
		if narrow {
			lines = append(lines, "  r retry · monitor doctor")
		} else {
			lines = append(lines, "  Press r to retry; monitor doctor checks collector health.")
		}
	} else {
		lines = append(lines, m.networkRateLines(
			collector.FormatBytes(net.BytesRecvPerSec)+"/s",
			collector.FormatBytes(net.BytesSentPerSec)+"/s", narrow)...)
		if len(net.DownloadHistory) == 0 && len(net.UploadHistory) == 0 {
			lines = append(lines, "", "  History: collecting samples…")
		} else {
			history := widgets.NewMultiSparkline()
			history.Data = [][]float64{net.DownloadHistory, net.UploadHistory}
			history.Labels = []string{"download", "upload"}
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
	}

	lines = append(lines, "")
	lines = append(lines, m.directionalMetricLines("Total",
		collector.FormatBytes(net.BytesRecv), collector.FormatBytes(net.BytesSent), narrow)...)
	lines = append(lines, m.directionalMetricLines("Packets",
		formatNumberShort(net.PacketsRecv), formatNumberShort(net.PacketsSent), narrow)...)
	return m.panel(panelWidth, lipgloss.JoinVertical(lipgloss.Left, lines...))
}
