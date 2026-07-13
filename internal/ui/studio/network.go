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

func directionalMetricLines(label, down, up string, narrow bool) []string {
	if narrow {
		return []string{"  " + label + " ↓ " + down, "  " + label + " ↑ " + up}
	}
	return []string{fmt.Sprintf("  %s: ↓ %s    ↑ %s", label, down, up)}
}

func networkRateLines(download, upload string, narrow bool) []string {
	if narrow {
		return []string{"  Download " + download, "  Upload   " + upload}
	}
	return []string{fmt.Sprintf("  Download: %s    Upload: %s", download, upload)}
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
	lines := []string{m.titleStyle.Render(" Network "), ""}
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
		lines = append(lines, networkRateLines(
			collector.FormatBytes(net.BytesRecvPerSec)+"/s",
			collector.FormatBytes(net.BytesSentPerSec)+"/s", narrow)...)
		if len(net.DownloadHistory) == 0 && len(net.UploadHistory) == 0 {
			lines = append(lines, "", "  History: collecting samples…")
		} else {
			history := widgets.NewMultiSparkline()
			history.Data = [][]float64{net.DownloadHistory, net.UploadHistory}
			history.Labels = []string{"download", "upload"}
			history.Colors = []string{"#88C0D0", "#A3BE8C"}
			history.Width = panelWidth - 18
			if history.Width < 8 {
				history.Width = 8
			}
			lines = append(lines, "", "  Recent rates · shared scale", history.Render())
		}
	}

	lines = append(lines, "")
	lines = append(lines, directionalMetricLines("Total",
		collector.FormatBytes(net.BytesRecv), collector.FormatBytes(net.BytesSent), narrow)...)
	lines = append(lines, directionalMetricLines("Packets",
		formatNumberShort(net.PacketsRecv), formatNumberShort(net.PacketsSent), narrow)...)
	return m.panelStyle.Width(panelWidth).Render(lipgloss.JoinVertical(lipgloss.Left, lines...))
}
