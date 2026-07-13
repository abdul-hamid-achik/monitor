package studio

import (
	"fmt"

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

func (m Model) renderNetwork() string {
	net := m.last.Network
	panelWidth := (m.width - 6) / 2
	stacked := m.width < 88
	if stacked {
		panelWidth = m.width - 4
	}
	if panelWidth < 20 {
		panelWidth = 20
	}
	downloadBar := widgets.NewBarGauge()
	downloadBar.Value = float64(net.BytesRecvPerSec) / (1024 * 1024)
	downloadBar.Width = panelWidth - 10
	downloadBar.Max = 100
	downloadBar.ColorFunc = func(float64) string { return "#88C0D0" }
	uploadBar := widgets.NewBarGauge()
	uploadBar.Value = float64(net.BytesSentPerSec) / (1024 * 1024)
	uploadBar.Width = panelWidth - 10
	uploadBar.Max = 100
	uploadBar.ColorFunc = func(float64) string { return "#A3BE8C" }
	downloadPanel := m.panelStyle.Width(panelWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Download "), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("#88C0D0")).Bold(true).Render(" ↓ ")+downloadBar.Render()+" "+collector.FormatBytes(net.BytesRecvPerSec)+"/s"))
	uploadPanel := m.panelStyle.Width(panelWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Upload "), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("#A3BE8C")).Bold(true).Render(" ↑ ")+uploadBar.Render()+" "+collector.FormatBytes(net.BytesSentPerSec)+"/s"))
	speedHistory := widgets.NewSparkline()
	speedHistory.Width = m.width - 20
	speedHistory.Height = 8
	speedHistory.ShowAxis = true
	if len(net.DownloadHistory) > 0 || len(net.UploadHistory) > 0 {
		combined := make([]float64, 0)
		maxLen := len(net.DownloadHistory)
		if len(net.UploadHistory) > maxLen {
			maxLen = len(net.UploadHistory)
		}
		for i := 0; i < maxLen; i++ {
			var val float64
			if i < len(net.DownloadHistory) {
				val = net.DownloadHistory[i]
			}
			if i < len(net.UploadHistory) && net.UploadHistory[i] > val {
				val = net.UploadHistory[i]
			}
			combined = append(combined, val/(1024*1024))
		}
		speedHistory.Data = combined
		speedHistory.Color = "#88C0D0"
	}
	historyPanel := m.panelStyle.Width(m.width - 4).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Speed History "), "", speedHistory.Render()))
	totalPanel := m.panelStyle.Width(panelWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Total Transfer "), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("#88C0D0")).Render(" ↓ "+collector.FormatBytes(net.BytesRecv)),
			lipgloss.NewStyle().Foreground(lipgloss.Color("#A3BE8C")).Render(" ↑ "+collector.FormatBytes(net.BytesSent))))
	packetsPanel := m.panelStyle.Width(panelWidth).Render(
		lipgloss.JoinVertical(lipgloss.Left, m.titleStyle.Render(" Packets "), "",
			lipgloss.NewStyle().Foreground(lipgloss.Color("#88C0D0")).Render(" ↓ "+formatNumberShort(net.PacketsRecv)),
			lipgloss.NewStyle().Foreground(lipgloss.Color("#A3BE8C")).Render(" ↑ "+formatNumberShort(net.PacketsSent))))
	speedPanels := downloadPanel + " " + uploadPanel
	summaryPanels := totalPanel + " " + packetsPanel
	if stacked {
		speedPanels = lipgloss.JoinVertical(lipgloss.Left, downloadPanel, uploadPanel)
		summaryPanels = lipgloss.JoinVertical(lipgloss.Left, totalPanel, packetsPanel)
	}
	return lipgloss.JoinVertical(lipgloss.Left, historyPanel, "", speedPanels, "", summaryPanels)
}
