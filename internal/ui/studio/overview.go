package studio

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

const operationalCompactWidth = 72

// renderOperationalOverview renders a dense, truthful system cockpit. It is
// intentionally separate from renderOverview so it can be integrated without
// coupling the responsive layout to the application's frame chrome.
func (m Model) renderOperationalOverview() string {
	width := m.width
	if width < 1 {
		width = 1
	}
	if m.last.LastUpdate.IsZero() {
		return operationalPanel(m, width,
			m.titleStyle.Render(" OVERVIEW "),
			"Collecting the first system sample...",
		)
	}

	compact := width < operationalCompactWidth || m.height < 22
	if compact {
		return m.renderCompactOperationalOverview(width)
	}

	kpis := m.operationalKPIs()
	kpiPanels := make([]string, 0, len(kpis))
	for i, panelWidth := range operationalColumns(width, len(kpis), 1) {
		kpiPanels = append(kpiPanels, operationalKPIPanel(m, panelWidth, kpis[i]))
	}
	kpiRow := lipgloss.JoinHorizontal(lipgloss.Top, interleaveOperational(kpiPanels, " ")...)

	leftWidth := (width - 1) * 3 / 5
	rightWidth := width - 1 - leftWidth
	activity := m.renderOperationalActivity(leftWidth)
	processes := m.renderOperationalProcesses(rightWidth, 3)
	workspace := lipgloss.JoinHorizontal(lipgloss.Top, activity, " ", processes)
	rail := m.renderOperationalRail(width, false)

	return lipgloss.JoinVertical(lipgloss.Left, kpiRow, "", workspace, "", rail)
}

func (m Model) renderCompactOperationalOverview(width int) string {
	kpis := m.operationalKPIs()
	frame := m.panelStyle.GetHorizontalFrameSize()
	contentWidth := maxInt(1, width-frame)
	summary := []string{
		m.titleStyle.Render(" SYSTEM SNAPSHOT "),
		operationalFit(fmt.Sprintf("CPU %s | MEMORY %s", kpis[0].value, kpis[1].value), contentWidth),
		operationalFit(fmt.Sprintf("THERMAL %s %s | DISK %s", kpis[2].value, kpis[2].compactNote, kpis[3].value), contentWidth),
	}

	return lipgloss.JoinVertical(lipgloss.Left,
		operationalPanel(m, width, summary...),
		m.renderOperationalActivity(width),
		m.renderOperationalProcesses(width, 2),
		m.renderOperationalRail(width, true),
	)
}

type operationalKPI struct {
	label       string
	value       string
	note        string
	compactNote string
}

func (m Model) operationalKPIs() []operationalKPI {
	cpuValue := fmt.Sprintf("%.1f%%", m.last.CPU.UsagePercent)
	cpuNote := fmt.Sprintf("%d cores", m.last.CPU.CoreCount)
	// Some macOS hosts report a tiny placeholder frequency (for example,
	// 4 MHz) through gopsutil. Omit values that cannot plausibly describe a
	// modern CPU instead of presenting false precision as 0.00 GHz.
	if m.last.CPU.FrequencyMHz >= 100 {
		cpuNote += fmt.Sprintf(" | %.2f GHz", m.last.CPU.FrequencyMHz/1000)
	}
	if issue := metricIssue(m.last.CPU.MetricStates, "usage"); issue != "" {
		cpuValue, cpuNote = "unavailable", issue
	}

	mem := m.last.Memory
	memValue := fmt.Sprintf("%.1f%%", mem.UsagePercent)
	memUsed, memTotal := mem.UsedBytes, mem.TotalBytes
	memSuffix := ""
	if m.last.Cgroup.Limited && m.last.Cgroup.MemLimitBytes > 0 {
		memUsed, memTotal = m.last.Cgroup.MemUsageBytes, m.last.Cgroup.MemLimitBytes
		memSuffix = " | cgroup"
	}
	memNote := fmt.Sprintf("%s / %s%s", collector.FormatBytes(memUsed), collector.FormatBytes(memTotal), memSuffix)
	if issue := metricIssue(mem.MetricStates, "virtual"); issue != "" {
		memValue, memNote = "unavailable", issue
	}

	tempValue, tempNote, tempCompact := m.operationalTemperatureSummary()
	diskValue, diskNote := m.operationalDiskSummary()

	return []operationalKPI{
		{label: "CPU", value: cpuValue, note: cpuNote},
		{label: "MEMORY", value: memValue, note: memNote},
		{label: "THERMAL", value: tempValue, note: tempNote, compactNote: tempCompact},
		{label: "DISK", value: diskValue, note: diskNote},
	}
}

func (m Model) operationalTemperatureSummary() (value, note, compact string) {
	temp := m.last.Temperature
	if issue := operationalStatusIssue(temp.State); issue != "" {
		return "unavailable", issue, ""
	}
	if !temp.Available {
		return "unavailable", "no sensor reading", ""
	}

	value = fmt.Sprintf("%.1f C", temp.CPUPackage)
	if m.settings != nil && m.settings.TemperatureUnit == "F" {
		value = fmt.Sprintf("%.1f F", temp.CPUPackage*9/5+32)
	}
	switch temp.Source {
	case "powermetrics":
		return value, "real sensor", "real"
	case "estimated":
		return value, "estimated from CPU load", "est"
	default:
		return value, "source not reported", "unknown"
	}
}

func (m Model) operationalDiskSummary() (string, string) {
	if issue := metricIssue(m.last.Disk.MetricStates, "partitions"); issue != "" {
		return "unavailable", issue
	}
	if len(m.last.Disk.Partitions) == 0 {
		return "unavailable", "no mounted volumes"
	}
	partition := m.last.Disk.Partitions[0]
	for _, candidate := range m.last.Disk.Partitions {
		// The startup volume is the most useful single disk health signal.
		// macOS also exposes pseudo mounts such as /dev at 100% of a few KiB;
		// choosing the numerically fullest mount makes those look like a full
		// system disk.
		if candidate.MountPoint == "/" {
			partition = candidate
			break
		}
		if isOperationalDisk(candidate) && (!isOperationalDisk(partition) || candidate.UsagePercent > partition.UsagePercent) {
			partition = candidate
		}
	}
	mount := partition.MountPoint
	if strings.TrimSpace(mount) == "" {
		mount = partition.Device
	}
	return fmt.Sprintf("%.1f%%", partition.UsagePercent), fmt.Sprintf("%s | %s used", mount, collector.FormatBytes(partition.UsedBytes))
}

func isOperationalDisk(partition collector.DiskPartitionInfo) bool {
	if partition.TotalBytes == 0 {
		return false
	}
	switch strings.ToLower(partition.Filesystem) {
	case "autofs", "devfs", "proc", "sysfs", "tmpfs":
		return false
	default:
		return true
	}
}

func operationalKPIPanel(m Model, width int, kpi operationalKPI) string {
	contentWidth := maxInt(1, width-m.panelStyle.GetHorizontalFrameSize())
	valueStyle := lipgloss.NewStyle().Bold(true)
	return operationalPanel(m, width,
		operationalFit(m.titleStyle.Render(" "+kpi.label+" "), contentWidth),
		operationalFit(valueStyle.Render(kpi.value), contentWidth),
		operationalFit(kpi.note, contentWidth),
	)
}

func (m Model) renderOperationalActivity(width int) string {
	contentWidth := maxInt(1, width-m.panelStyle.GetHorizontalFrameSize())
	return operationalPanel(m, width,
		operationalFit(m.titleStyle.Render(" ACTIVITY | 60s "), contentWidth),
		m.operationalHistoryLine("CPU", m.last.CPU.History, m.last.CPU.UsagePercent, metricIssue(m.last.CPU.MetricStates, "usage"), contentWidth),
		m.operationalHistoryLine("MEM", m.last.Memory.History, m.last.Memory.UsagePercent, metricIssue(m.last.Memory.MetricStates, "virtual"), contentWidth),
	)
}

func (m Model) operationalHistoryLine(label string, data []float64, current float64, issue string, width int) string {
	if issue != "" {
		return operationalFit(label+" unavailable | "+issue, width)
	}
	if len(data) == 0 {
		return operationalFit(label+" no samples yet", width)
	}

	// Keep a few cells of slack because styled block glyphs and panel padding
	// can consume terminal cells differently across renderers.
	chartWidth := width - 15
	if chartWidth < 1 {
		return operationalFit(fmt.Sprintf("%s %.1f%%", label, current), width)
	}
	spark := widgets.NewSparkline()
	spark.Data = data
	spark.Width = chartWidth
	spark.Height = 1
	spark.Min = 0
	spark.Max = 100
	spark.AutoScale = false
	spark.ShowAxis = false
	return operationalFit(fmt.Sprintf("%-3s %s %5.1f%%", label, spark.Render(), current), width)
}

func (m Model) renderOperationalProcesses(width, count int) string {
	contentWidth := maxInt(1, width-m.panelStyle.GetHorizontalFrameSize())
	lines := []string{operationalFit(m.titleStyle.Render(" TOP CPU PROCESSES "), contentWidth)}
	if issue := operationalStatusIssue(m.last.ProcessesState); issue != "" {
		lines = append(lines, operationalFit("Unavailable | "+issue, contentWidth))
		for len(lines) < count+1 {
			lines = append(lines, "")
		}
		return operationalPanel(m, width, lines...)
	}

	processes := append([]collector.ProcessInfo(nil), m.last.Processes...)
	sort.SliceStable(processes, func(i, j int) bool {
		if processes[i].CPUPercent == processes[j].CPUPercent {
			return processes[i].PID < processes[j].PID
		}
		return processes[i].CPUPercent > processes[j].CPUPercent
	})
	if len(processes) == 0 {
		lines = append(lines, "No process samples yet")
	} else {
		if len(processes) < count {
			count = len(processes)
		}
		nameWidth := contentWidth - 20
		if nameWidth < 4 {
			nameWidth = 4
		}
		for _, process := range processes[:count] {
			name := truncateStr(process.Name, nameWidth)
			line := fmt.Sprintf("%5d  %-*s %5.1f%%", process.PID, nameWidth, name, process.CPUPercent)
			lines = append(lines, operationalFit(line, contentWidth))
		}
	}
	for len(lines) < count+1 {
		lines = append(lines, "")
	}
	return operationalPanel(m, width, lines...)
}

func (m Model) renderOperationalRail(width int, compact bool) string {
	issues := m.operationalAttentionIssues()
	prefix, message := "OK", "Core telemetry is available"
	if len(issues) > 0 {
		prefix, message = "ATTENTION", issues[0]
		if len(issues) > 1 {
			message += fmt.Sprintf(" | +%d more", len(issues)-1)
		}
	}
	line := prefix + " | " + message
	if compact {
		return operationalFit(line, width)
	}
	contentWidth := maxInt(1, width-m.panelStyle.GetHorizontalFrameSize())
	return operationalPanel(m, width, operationalFit(line, contentWidth))
}

func (m Model) operationalAttentionIssues() []string {
	issues := make([]string, 0, 8)
	if len(m.alerts) > 0 {
		alert := m.alerts[len(m.alerts)-1]
		label := strings.ToUpper(strings.ReplaceAll(alert.Rule, "_", " "))
		if label == "" {
			label = "ANOMALY"
		}
		issues = append(issues, label+" | "+alert.Detail)
	}
	if m.settings != nil {
		if threshold := m.settings.CPUAlertThreshold; threshold > 0 && m.last.CPU.UsagePercent >= threshold {
			issues = append(issues, fmt.Sprintf("CPU %.1f%% >= %.0f%% threshold", m.last.CPU.UsagePercent, threshold))
		}
		if threshold := m.settings.MemoryAlertThreshold; threshold > 0 && m.last.Memory.UsagePercent >= threshold {
			issues = append(issues, fmt.Sprintf("memory %.1f%% >= %.0f%% threshold", m.last.Memory.UsagePercent, threshold))
		}
	}

	appendStatus := func(label, issue string) {
		if issue != "" {
			issues = append(issues, label+" unavailable | "+issue)
		}
	}
	appendStatus("capture", operationalStatusIssue(m.last.Capture))
	appendStatus("CPU", metricIssue(m.last.CPU.MetricStates, "usage"))
	appendStatus("memory", metricIssue(m.last.Memory.MetricStates, "virtual"))
	appendStatus("disk", metricIssue(m.last.Disk.MetricStates, "partitions"))
	appendStatus("network", metricIssue(m.last.Network.MetricStates, "rate"))
	appendStatus("processes", operationalStatusIssue(m.last.ProcessesState))
	appendStatus("thermal", operationalStatusIssue(m.last.Temperature.State))
	if m.last.Temperature.State.State == "" && !m.last.Temperature.Available {
		issues = append(issues, "thermal unavailable | no sensor reading")
	}
	return issues
}

func operationalStatusIssue(status collector.MetricStatus) string {
	if status.State == "" || status.State == collector.MetricObserved {
		return ""
	}
	if reason := strings.TrimSpace(status.Reason); reason != "" {
		return reason
	}
	return string(status.State)
}

func operationalPanel(m Model, totalWidth int, lines ...string) string {
	if totalWidth <= 0 {
		return ""
	}
	frame := m.panelStyle.GetHorizontalFrameSize()
	if totalWidth <= frame {
		return operationalFit(strings.Join(lines, " "), totalWidth)
	}
	contentWidth := totalWidth - frame
	for i := range lines {
		lines[i] = operationalFit(lines[i], contentWidth)
	}
	// Lipgloss v2 treats Width as the final rendered width, including the
	// style's border and padding. Lines are fitted to the remaining content
	// width above, while the panel itself receives the requested total width.
	return m.panelStyle.Width(totalWidth).Render(strings.Join(lines, "\n"))
}

func operationalColumns(total, count, gap int) []int {
	if count <= 0 {
		return nil
	}
	available := total - gap*(count-1)
	if available < count {
		available = count
	}
	base, remainder := available/count, available%count
	widths := make([]int, count)
	for i := range widths {
		widths[i] = base
		if i < remainder {
			widths[i]++
		}
	}
	return widths
}

func interleaveOperational(items []string, separator string) []string {
	if len(items) < 2 {
		return items
	}
	out := make([]string, 0, len(items)*2-1)
	for i, item := range items {
		if i > 0 {
			out = append(out, separator)
		}
		out = append(out, item)
	}
	return out
}

func operationalFit(line string, width int) string {
	if width <= 0 {
		return ""
	}
	if lipgloss.Width(line) <= width {
		return line
	}
	tail := ""
	if width >= 4 {
		tail = "..."
	}
	return ansi.Truncate(line, width, tail)
}
