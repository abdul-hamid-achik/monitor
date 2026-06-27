package v2

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
	"github.com/abdul-hamid-achik/monitor/internal/temperature"
)

type viewID int

const (
	viewOverview viewID = iota
	viewCPU
	viewMemory
	viewTemperature
	viewDisk
	viewNetwork
	viewProcesses
	viewSettings
)

type Model struct {
	ctx       context.Context
	cancel    context.CancelFunc
	collector *collector.Collector
	unsubscribe func()

	width, height int
	ready         bool
	quitting      bool
	view          viewID

	settings *config.Settings

	last collector.SystemInfo

	titleStyle  lipgloss.Style
	panelStyle  lipgloss.Style
	statusStyle lipgloss.Style
	tabActive   lipgloss.Style
	tabInactive lipgloss.Style

	processTable    *table.Model
	selectedPids    map[int32]bool
	sortBy          string
	sortAsc         bool
	processSearch   bool
	searchQuery     string
	showKillConfirm bool
	forceKill       bool
	killConf        kill.Confirmation
}

func NewModel() Model {
	ctx, cancel := context.WithCancel(context.Background())
	c := collector.New(collector.Options{Interval: time.Second, HistorySize: 60})

	ts := temperature.New(ctx, temperature.Options{
		Interval: 5 * time.Second,
		Logf:     func(string, ...any) {},
	})
	c.WithTemperatureHook(func() (float64, float64, float64, float64, float64, float64, int, string, string, bool) {
		r := ts.Latest()
		return r.CPUPackage, r.CPUCores, r.GPU, r.ANE, r.Battery, r.Ambient, r.FanRPM, r.FanMode, string(r.Source), r.Available
	})

	panelStyle := lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(lipgloss.Color("#3B4252")).
		Padding(0, 1)

	m := Model{
		ctx:          ctx,
		cancel:       cancel,
		collector:    c,
		settings:     config.Default(),
		view:         viewOverview,
		selectedPids: make(map[int32]bool),
		sortBy:       "cpu",
		sortAsc:      false,
		titleStyle:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#88C0D0")),
		panelStyle:   panelStyle,
		statusStyle: lipgloss.NewStyle().
			Foreground(lipgloss.Color("#D8DEE9")).
			Background(lipgloss.Color("#2E3440")).
			Bold(true),
		tabActive:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#88C0D0")).Padding(0, 1),
		tabInactive: lipgloss.NewStyle().Foreground(lipgloss.Color("#4C566A")).Padding(0, 1),
	}
	m.setupProcessTable()
	return m
}

func (m Model) Init() tea.Cmd {
	m.unsubscribe = m.collector.Subscribe(func(ev collector.Event) {})
	return tea.Batch(m.tickCmd(), m.startCollectorCmd())
}

func (m Model) tickCmd() tea.Cmd {
	return tea.Tick(time.Second, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) startCollectorCmd() tea.Cmd {
	return func() tea.Msg {
		_ = m.collector.Run(m.ctx)
		return nil
	}
}

type tickMsg time.Time

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		if m.processTable != nil {
			overhead := 10
			if m.height-overhead > 5 {
				m.processTable.SetHeight(m.height - overhead)
			}
			m.processTable.SetWidth(m.width - 6)
		}
		return m, nil
	case tickMsg:
		m.last = m.collector.Snapshot()
		if m.view == viewProcesses {
			m.updateProcessTable()
		}
		return m, m.tickCmd()
	case tea.KeyPressMsg:
		if m.showKillConfirm {
			return m.handleKillConfirmKeys(msg)
		}
		// While the process search prompt is active, every keystroke is
		// search input — route it to the per-tab handler BEFORE the global
		// navigation/quit shortcuts, so typing 'q', 'l', a digit, etc.
		// edits the query instead of quitting or switching tabs.
		if m.processSearch {
			return m.handleProcessKeys(msg)
		}
		switch msg.Keystroke() {
		case "q", "ctrl+c", "esc":
			m.quitting = true
			if m.unsubscribe != nil {
				m.unsubscribe()
			}
			m.cancel()
			return m, tea.Quit
		case "tab", "right", "l":
			m.view = (m.view + 1) % 8
			return m, nil
		case "shift+tab", "left", "h":
			m.view = (m.view + 7) % 8
			return m, nil
		case "1":
			m.view = viewOverview
			return m, nil
		case "2":
			m.view = viewCPU
			return m, nil
		case "3":
			m.view = viewMemory
			return m, nil
		case "4":
			m.view = viewTemperature
			return m, nil
		case "5":
			m.view = viewDisk
			return m, nil
		case "6":
			m.view = viewNetwork
			return m, nil
		case "7":
			m.view = viewProcesses
			return m, nil
		case "8":
			m.view = viewSettings
			return m, nil
		}
		if m.view == viewProcesses {
			return m.handleProcessKeys(msg)
		}
		return m, nil
	default:
		if m.view == viewProcesses && !m.showKillConfirm && m.processTable != nil {
			updated, cmd := m.processTable.Update(msg)
			m.processTable = &updated
			return m, cmd
		}
		return m, nil
	}
}

func (m Model) View() tea.View {
	if m.quitting {
		return tea.NewView("Goodbye!\n")
	}
	if !m.ready {
		v := tea.NewView("Initializing...\n")
		v.AltScreen = true
		return v
	}
	body := m.render()
	v := tea.NewView(body)
	v.AltScreen = true
	return v
}

func (m Model) render() string {
	header := m.renderHeader()
	var content string
	switch m.view {
	case viewOverview:
		content = m.renderOverview()
	case viewCPU:
		content = m.renderCPU()
	case viewMemory:
		content = m.renderMemory()
	case viewTemperature:
		content = m.renderTemperature()
	case viewDisk:
		content = m.renderDisk()
	case viewNetwork:
		content = m.renderNetwork()
	case viewProcesses:
		content = m.renderProcesses()
	case viewSettings:
		content = m.renderSettings()
	default:
		content = "Unknown view"
	}
	status := m.statusStyle.Width(m.width).Render(
		fmt.Sprintf(" CPU %.1f%%  │  Mem %.1f%%  │  Update %s  │  1-8: tabs  │  q: quit ",
			m.last.CPU.UsagePercent, m.last.Memory.UsagePercent, m.last.LastUpdate.Format("15:04:05")))
	return strings.Join([]string{header, content, status}, "\n")
}

func (m Model) renderHeader() string {
	overview := m.tabInactive.Render(" 1:Overview ")
	cpu := m.tabInactive.Render(" 2:CPU ")
	memory := m.tabInactive.Render(" 3:Memory ")
	temperature := m.tabInactive.Render(" 4:Temperature ")
	disk := m.tabInactive.Render(" 5:Disk ")
	network := m.tabInactive.Render(" 6:Network ")
	processes := m.tabInactive.Render(" 7:Processes ")
	settings := m.tabInactive.Render(" 8:Settings ")
	if m.view == viewOverview {
		overview = m.tabActive.Render(" 1:Overview ")
	}
	if m.view == viewCPU {
		cpu = m.tabActive.Render(" 2:CPU ")
	}
	if m.view == viewMemory {
		memory = m.tabActive.Render(" 3:Memory ")
	}
	if m.view == viewTemperature {
		temperature = m.tabActive.Render(" 4:Temperature ")
	}
	if m.view == viewDisk {
		disk = m.tabActive.Render(" 5:Disk ")
	}
	if m.view == viewNetwork {
		network = m.tabActive.Render(" 6:Network ")
	}
	if m.view == viewProcesses {
		processes = m.tabActive.Render(" 7:Processes ")
	}
	if m.view == viewSettings {
		settings = m.tabActive.Render(" 8:Settings ")
	}
	title := m.titleStyle.Render(fmt.Sprintf(" MONITOR (v2)  %s ", m.last.LastUpdate.Format("15:04:05")))
	tabs := lipgloss.JoinHorizontal(lipgloss.Top, overview, cpu, memory, temperature, disk, network, processes, settings)
	return lipgloss.JoinHorizontal(lipgloss.Top, title, tabs)
}

func tempBadge(source string) string {
	switch source {
	case "powermetrics":
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#A3BE8C")).Render(" ● real")
	default:
		return lipgloss.NewStyle().Foreground(lipgloss.Color("#4C566A")).Render(" ● est")
	}
}

func gaugeLabel(c float64) string {
	switch {
	case c >= 85:
		return "Hot"
	case c >= 70:
		return "Warm"
	default:
		return "Normal"
	}
}

func (m Model) renderGauge(value float64, width int) string {
	if width < 5 {
		width = 5
	}
	filled := int(value / 100 * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	bar := strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
	style := lipgloss.NewStyle().Foreground(lipgloss.Color("#A3BE8C"))
	if value >= 80 {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color("#BF616A"))
	} else if value >= 50 {
		style = lipgloss.NewStyle().Foreground(lipgloss.Color("#EBCB8B"))
	}
	return fmt.Sprintf("  %s  %5.1f%%", style.Render(bar), value)
}

func (m Model) renderOverview() string {
	if m.last.LastUpdate.IsZero() {
		return m.panelStyle.Render(m.titleStyle.Render(" MONITOR (v2) ") + "\n\nWaiting for first tick…")
	}
	cpu := m.last.CPU
	mem := m.last.Memory
	net := m.last.Network
	cpuPanel := m.panelStyle.Width(m.width/2 - 2).Render(
		m.titleStyle.Render(" CPU ") + "\n\n" +
			m.renderGauge(cpu.UsagePercent, m.width/2-10) +
			fmt.Sprintf("\n  %.2f GHz  │  %d cores  │  %d threads", cpu.FrequencyMHz/1000, cpu.CoreCount, cpu.ThreadCount))
	memPanel := m.panelStyle.Width(m.width/2 - 2).Render(
		m.titleStyle.Render(" Memory ") + "\n\n" +
			m.renderGauge(mem.UsagePercent, m.width/2-10) +
			fmt.Sprintf("\n  %s / %s  │  swap %s", collector.FormatBytes(mem.UsedBytes), collector.FormatBytes(mem.TotalBytes), collector.FormatBytes(mem.SwapUsed)))
	netPanel := m.panelStyle.Width(m.width-4).Render(
		m.titleStyle.Render(" Network ") + "\n\n" +
			fmt.Sprintf("  ↓ %s/s    ↑ %s/s", collector.FormatBytes(net.BytesRecvPerSec), collector.FormatBytes(net.BytesSentPerSec)) +
			fmt.Sprintf("\n  Total: ↓ %s    ↑ %s", collector.FormatBytes(net.BytesRecv), collector.FormatBytes(net.BytesSent)))
	return strings.Join([]string{cpuPanel + "  " + memPanel, netPanel}, "\n")
}

func (m Model) renderSettings() string {
	if m.settings == nil {
		return m.panelStyle.Width(m.width - 4).Render(m.titleStyle.Render(" Settings ") + "\n\nNo settings loaded.")
	}
	s := m.settings
	showSys := "OFF"
	if s.ShowSystemProcesses {
		showSys = "ON"
	}
	cpuAlert := "OFF"
	if s.CPUAlertThreshold > 0 {
		cpuAlert = fmt.Sprintf("%.0f%%", s.CPUAlertThreshold)
	}
	memAlert := "OFF"
	if s.MemoryAlertThreshold > 0 {
		memAlert = fmt.Sprintf("%.0f%%", s.MemoryAlertThreshold)
	}
	rows := []string{
		fmt.Sprintf("  Update Interval:      %s", s.UpdateInterval),
		fmt.Sprintf("  Temperature Unit:     °%s", s.TemperatureUnit),
		fmt.Sprintf("  Show System Procs:    %s", showSys),
		fmt.Sprintf("  Max Processes:        %d", s.MaxProcesses),
		fmt.Sprintf("  Mouse Enabled:        %t", s.MouseEnabled),
		fmt.Sprintf("  CPU Alert Threshold:  %s", cpuAlert),
		fmt.Sprintf("  Memory Alert Threshold: %s", memAlert),
	}
	body := strings.Join(rows, "\n")
	footer := "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("#4C566A")).Render("  (read-only in v2; edit via config file)")
	return m.panelStyle.Width(m.width - 4).Render(m.titleStyle.Render(" Settings ") + "\n\n" + body + footer)
}

func (m Model) renderTemperature() string {
	if m.last.LastUpdate.IsZero() {
		return m.panelStyle.Render(m.titleStyle.Render(" Temperature ") + "\n\nWaiting for first tick…")
	}
	temp := m.last.Temperature
	badge := tempBadge(temp.Source)
	rows := []string{
		fmt.Sprintf("  CPU Package:  %5.1f°C  %s%s", temp.CPUPackage, gaugeLabel(temp.CPUPackage), badge),
		fmt.Sprintf("  CPU Cores:    %5.1f°C  %s%s", temp.CPUCores, gaugeLabel(temp.CPUCores), badge),
		fmt.Sprintf("  GPU:          %5.1f°C  %s%s", temp.GPU, gaugeLabel(temp.GPU), badge),
		fmt.Sprintf("  ANE:          %5.1f°C  %s%s", temp.ANE, gaugeLabel(temp.ANE), badge),
		fmt.Sprintf("  Battery:      %5.1f°C  %s%s", temp.Battery, gaugeLabel(temp.Battery), badge),
	}
	fan := "  Fan telemetry unavailable on Apple Silicon (SMC keys restricted)"
	if temp.FanRPM > 0 || temp.FanMode != "" {
		fan = fmt.Sprintf("  Fan: %d RPM  mode=%s", temp.FanRPM, temp.FanMode)
	}
	body := strings.Join(rows, "\n") + "\n\n" + fan
	return m.panelStyle.Width(m.width - 4).Render(m.titleStyle.Render(" Sensor Readings ") + "\n\n" + body)
}