package studio

import (
	"context"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/history"
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
	viewTrends

	viewCount = iota // number of tabs
)

type Model struct {
	ctx       context.Context
	cancel    context.CancelFunc
	collector *collector.Collector
	analyzer  *analyzer.Engine

	width, height  int
	ready          bool
	quitting       bool
	view           viewID
	paused         bool
	helpVisible    bool
	darkBackground bool

	settings *config.Settings

	last   collector.SystemInfo
	alerts []collector.Alert

	titleStyle  lipgloss.Style
	panelStyle  lipgloss.Style
	statusStyle lipgloss.Style
	tabActive   lipgloss.Style
	tabInactive lipgloss.Style
	theme       studioTheme

	processTable         *table.Model
	selectedPids         map[int32]bool
	sortBy               string
	sortAsc              bool
	processSearch        bool
	searchQuery          string
	searchBefore         string
	processDetailVisible bool
	processDetailPID     int32
	showKillConfirm      bool
	forceKill            bool
	killConf             kill.Confirmation

	settingsCursor int
	settingsSaved  bool
	settingsDirty  bool
	settingsErr    string

	// killNotice is a transient status-bar message (e.g. when a confirmed kill
	// spared protected/system PIDs), shown for killNoticeTicks ticks so the TUI
	// reports the refusal like the CLI/MCP do instead of silently sparing them.
	killNotice      string
	killNoticeTicks int

	// Trends-tab data, cached off the render path. The history store is opened
	// and scanned in Update (throttled), never in View(), so a frame never does
	// blocking disk I/O. trendsErr=="norec" means no store exists yet.
	trends    []trendSeries
	trendsErr string
	trendsAt  time.Time
}

type trendSeries struct {
	metric string
	pts    []history.Point
}

// Options controls optional Studio integrations. The zero value preserves the
// interactive defaults.
type Options struct {
	// DisableTemperatureSource prevents Studio from starting the privileged
	// powermetrics-backed source. The collector's built-in estimate remains
	// available, matching the non-TUI --no-temperature-source behavior.
	DisableTemperatureSource bool
	// Reloader, when non-nil, is attached to the running Bubble Tea program so
	// external `monitor reload` requests can inject a real refresh message.
	Reloader *ProgramReloader
}

func NewModel() Model {
	return NewModelWithOptions(Options{})
}

// NewModelWithOptions builds a Studio model while allowing CLI-global policy
// (such as --no-temperature-source) to be honored before subprocesses start.
func NewModelWithOptions(opts Options) Model {
	ctx, cancel := context.WithCancel(context.Background())

	// Load the user's saved settings (falls back to defaults on any error) so
	// the persisted UpdateInterval drives the collector's sample rate.
	settings, err := config.Load()
	if err != nil || settings == nil {
		settings = config.Default()
	}
	interval := settings.UpdateInterval
	if interval <= 0 {
		interval = time.Second
	}
	c := collector.New(collector.Options{Interval: interval, HistorySize: 60})
	engine := analyzer.NewEngine()
	engine.AddRule(&analyzer.CPUSpikeRule{})
	engine.AddRule(&analyzer.RSSGrowthRule{})
	engine.AddRule(&analyzer.DiskFillRule{})
	engine.AddRule(&analyzer.SwapPressureRule{})
	engine.AddRule(&analyzer.ZombieRule{})

	if !opts.DisableTemperatureSource {
		ts := temperature.New(ctx, temperature.Options{
			Interval: 5 * time.Second,
			Logf:     func(string, ...any) {},
		})
		c.WithTemperatureHook(func() (float64, float64, float64, float64, float64, float64, int, string, string, bool) {
			r := ts.Latest()
			return r.CPUPackage, r.CPUCores, r.GPU, r.ANE, r.Battery, r.Ambient, r.FanRPM, r.FanMode, string(r.Source), r.Available
		})
	}

	m := Model{
		ctx:          ctx,
		cancel:       cancel,
		collector:    c,
		analyzer:     engine,
		settings:     settings,
		view:         viewOverview,
		selectedPids: make(map[int32]bool),
		sortBy:       "cpu",
		sortAsc:      false,
	}
	m.applyTheme(true)
	m.setupProcessTable()
	return m
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.tickCmd(), m.startCollectorCmd(), tea.RequestBackgroundColor)
}

func (m Model) tickCmd() tea.Cmd {
	// Honor the user's UpdateInterval; re-read each tick so a live change in
	// Settings takes effect on the next tick.
	interval := time.Second
	if m.settings != nil && m.settings.UpdateInterval > 0 {
		interval = m.settings.UpdateInterval
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg {
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
type externalReloadMsg struct{}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.BackgroundColorMsg:
		m.applyTheme(msg.IsDark())
		return m, nil
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		if m.processTable != nil {
			tableHeight := m.height - 11
			if tableHeight < 1 {
				tableHeight = 1
			}
			tableWidth := m.width - 6
			if tableWidth < 1 {
				tableWidth = 1
			}
			m.processTable.SetHeight(tableHeight)
			m.processTable.SetWidth(tableWidth)
			m.configureProcessTable()
		}
		return m, nil
	case tickMsg:
		if !m.paused {
			m.last = m.collector.Snapshot()
			m.observeSnapshot()
			if m.view == viewProcesses {
				m.updateProcessTable()
			}
		}
		// Expire the transient kill notice after a few ticks.
		if m.killNoticeTicks > 0 {
			m.killNoticeTicks--
			if m.killNoticeTicks == 0 {
				m.killNotice = ""
			}
		}
		// Refresh the Trends cache off the render path, throttled to every 5s
		// while the tab is visible, so View() never blocks on disk I/O.
		if m.view == viewTrends && (m.trendsAt.IsZero() || time.Since(m.trendsAt) > 5*time.Second) {
			m.refreshTrends()
		}
		return m, m.tickCmd()
	case externalReloadMsg:
		m.refreshNow()
		return m, nil
	case killBatchResultMsg:
		m.killNotice = formatKillBatchResult(msg)
		m.killNoticeTicks = 6
		// A verified kill changes the process list; take the latest published
		// snapshot and redraw now rather than waiting for another UI tick.
		if !m.paused {
			m.last = m.collector.Snapshot()
			m.observeSnapshot()
			if m.view == viewProcesses {
				m.updateProcessTable()
			}
		}
		return m, nil
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
		if m.helpVisible {
			switch msg.Keystroke() {
			case "?", "esc", "q":
				m.helpVisible = false
				return m, nil
			case "ctrl+c":
				m.quitting = true
				m.cancel()
				return m, tea.Quit
			default:
				return m, nil
			}
		}
		// Process details are modal: navigation and destructive shortcuts do
		// not leak through to the table while the panel is open. The inspected
		// PID remains pinned even if live sorting changes the row beneath it.
		if m.processDetailVisible {
			switch msg.Keystroke() {
			case "enter", "esc", "q":
				m.processDetailVisible = false
				m.processDetailPID = 0
				return m, nil
			case "?":
				m.helpVisible = true
				return m, nil
			case "r":
				m.refreshNow()
				return m, nil
			case "ctrl+c":
				m.quitting = true
				m.cancel()
				return m, tea.Quit
			default:
				return m, nil
			}
		}
		switch msg.Keystroke() {
		case "q", "ctrl+c":
			m.quitting = true
			m.cancel()
			return m, tea.Quit
		case "?":
			m.helpVisible = true
			return m, nil
		case "p":
			m.paused = !m.paused
			return m, nil
		case "r":
			m.refreshNow()
			return m, nil
		case "tab", "right", "l":
			m.view = (m.view + 1) % viewCount
			return m, nil
		case "shift+tab", "left", "h":
			m.view = (m.view + viewCount - 1) % viewCount
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
		case "9":
			m.view = viewTrends
			return m, nil
		}
		if m.view == viewSettings {
			return m.handleSettingsKeys(msg)
		}
		if m.view == viewProcesses {
			return m.handleProcessKeys(msg)
		}
		return m, nil
	case tea.MouseWheelMsg:
		if m.view == viewProcesses && m.processTable != nil {
			updated, cmd := m.processTable.Update(msg)
			m.processTable = &updated
			return m, cmd
		}
		return m, nil
	case tea.MouseClickMsg:
		// The identity bar occupies row 0; navigation lives on row 1.
		if msg.Mouse().Y == 1 {
			if v, ok := m.headerTabAt(msg.Mouse().X); ok {
				m.view = v
				return m, nil
			}
		}
		if m.view == viewProcesses && m.processTable != nil {
			updated, cmd := m.processTable.Update(msg)
			m.processTable = &updated
			return m, cmd
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

func (m *Model) refreshNow() {
	m.last = m.collector.Snapshot()
	m.observeSnapshot()
	if m.view == viewProcesses {
		m.updateProcessTable()
	}
	if m.view == viewTrends {
		m.refreshTrends()
	}
}

func (m *Model) observeSnapshot() {
	if m.analyzer == nil || m.last.LastUpdate.IsZero() {
		return
	}
	m.alerts = m.analyzer.Observe(collector.Event{
		Timestamp: m.last.LastUpdate,
		Hostname:  m.last.Hostname,
		CPU:       m.last.CPU,
		Memory:    m.last.Memory,
		Network:   m.last.Network,
		Disk:      m.last.Disk,
		Processes: m.last.Processes,
	})
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
	v.BackgroundColor = m.theme.Background
	v.ForegroundColor = m.theme.Text
	v.WindowTitle = "Monitor Studio"
	if m.settings != nil && m.settings.MouseEnabled {
		v.MouseMode = tea.MouseModeCellMotion
	}
	return v
}

func (m Model) render() string {
	if m.helpVisible {
		return m.renderHelp()
	}
	if m.width < 32 || m.height < 8 {
		return m.renderTinyFrame()
	}
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
	case viewTrends:
		content = m.renderTrends()
	default:
		content = "Unknown view"
	}
	footerText := ""
	if m.killNotice != "" {
		footerText = " ! " + m.killNotice + " "
	} else {
		footerText = m.footerText()
	}
	statusWidth := m.width
	if statusWidth < 1 {
		statusWidth = 1
	}
	status := m.statusStyle.Width(statusWidth).Render(footerText)
	contentHeight := m.height - lipgloss.Height(header) - 1
	if contentHeight < 1 {
		contentHeight = 1
	}
	content = lipgloss.NewStyle().Height(contentHeight).MaxHeight(contentHeight).Render(content)
	return strings.Join([]string{header, content, status}, "\n")
}

func (m Model) collectionState() (state, sampled string) {
	state = "LIVE"
	sampled = "waiting"
	if !m.last.LastUpdate.IsZero() {
		sampled = m.last.LastUpdate.Format("15:04:05")
	}
	if m.paused {
		state = "PAUSED"
	} else if m.last.LastUpdate.IsZero() {
		state = "WAITING"
	} else {
		staleAfter := 3 * time.Second
		if m.settings != nil && 2*m.settings.UpdateInterval > staleAfter {
			staleAfter = 2 * m.settings.UpdateInterval
		}
		if time.Since(m.last.LastUpdate) > staleAfter {
			state = "STALE"
		}
	}
	return state, sampled
}

// statusText is the collection-state summary used by the identity row. The
// explicit word keeps state legible when color is unavailable.
func (m Model) statusText() string {
	state, sampled := m.collectionState()
	return fmt.Sprintf("● %s · sampled %s", state, sampled)
}

func (m Model) footerText() string {
	left := " Tab/←→ switch · p pause · r refresh · ? help · q quit "
	switch m.view {
	case viewProcesses:
		left = " ↑/↓ move · Enter inspect · / filter · K terminate · ? help "
	case viewSettings:
		left = " ↑/↓ select · Enter change · s save · ? help "
	case viewTrends:
		left = " r reload history · Tab switch · ? help · q quit "
	}
	right := ""
	if m.settings != nil && m.settings.UpdateInterval > 0 {
		right = fmt.Sprintf("every %s ", m.settings.UpdateInterval)
	}
	if m.width >= 80 {
		return joinEnds(m.width, left, right)
	}
	return fitText(left, m.width)
}

// thresholdMarks returns a "!" for CPU / memory when the live value meets or
// exceeds the configured alert threshold (0 disables the check), giving the
// Settings tab's threshold rows a visible effect.
func (m Model) thresholdMarks() (cpu, mem string) {
	if m.settings == nil {
		return "", ""
	}
	if m.settings.CPUAlertThreshold > 0 && m.last.CPU.UsagePercent >= m.settings.CPUAlertThreshold {
		cpu = "!"
	}
	if m.settings.MemoryAlertThreshold > 0 && m.last.Memory.UsagePercent >= m.settings.MemoryAlertThreshold {
		mem = "!"
	}
	return cpu, mem
}

func (m Model) renderHeader() string {
	state, _ := m.collectionState()
	host := strings.TrimSpace(m.last.Hostname)
	if host == "" {
		host = "local"
	}
	identity := " MONITOR"
	if m.width >= 58 {
		identity += " · " + fitText(host, 24)
	}
	stateSummary := "● " + state
	if m.width >= 46 {
		stateSummary = m.statusText()
	}
	stateColor := m.theme.Good
	if state == "PAUSED" || state == "STALE" {
		stateColor = m.theme.Warning
	} else if state == "WAITING" {
		stateColor = m.theme.Muted
	}
	left := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent).Render(identity)
	right := lipgloss.NewStyle().Bold(true).Foreground(stateColor).Render(stateSummary + " ")
	identityRow := lipgloss.NewStyle().
		Width(maxInt(1, m.width)).
		Foreground(m.theme.Text).
		Background(m.theme.SurfaceAlt).
		Render(joinEnds(m.width, left, right))

	layout := m.headerLayout()
	tabs := make([]string, 0, len(layout.labels))
	for i, label := range layout.labels {
		if layout.views[i] == m.view {
			// The pointer is a non-color active-state cue for monochrome and
			// low-contrast terminals.
			tabs = append(tabs, m.headerTabStyle(true, layout.compact).Render("▸"+label))
		} else {
			tabs = append(tabs, m.headerTabStyle(false, layout.compact).Render(" "+label))
		}
	}
	navigation := lipgloss.JoinHorizontal(lipgloss.Top, m.titleStyle.Render(layout.title), lipgloss.JoinHorizontal(lipgloss.Top, tabs...))
	navigation = lipgloss.NewStyle().
		Width(maxInt(1, m.width)).
		Foreground(m.theme.Text).
		Background(m.theme.Surface).
		Render(navigation)
	return lipgloss.JoinVertical(lipgloss.Left, identityRow, navigation)
}

func (m Model) renderTinyFrame() string {
	if m.width <= 0 || m.height <= 0 {
		return ""
	}
	state, _ := m.collectionState()
	lines := []string{fitText("MONITOR · "+state, m.width)}
	if m.height > 1 {
		lines = append(lines, fitText("Enlarge terminal", m.width))
	}
	if m.height > 2 {
		lines = append(lines, fitText("? help · q quit", m.width))
	}
	for len(lines) < m.height {
		lines = append(lines, "")
	}
	return strings.Join(lines[:m.height], "\n")
}

func joinEnds(width int, left, right string) string {
	if width <= 0 {
		return ""
	}
	gap := width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		if lipgloss.Width(right) >= width {
			return fitText(right, width)
		}
		return fitText(left, width-lipgloss.Width(right)) + right
	}
	return left + strings.Repeat(" ", gap) + right
}

func fitText(value string, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(value)
	if len(runes) <= width {
		return value
	}
	if width == 1 {
		return string(runes[:1])
	}
	return string(runes[:width-1]) + "…"
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
	return m.renderOperationalOverview()
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
	// Mark the selected row.
	sel := lipgloss.NewStyle().Foreground(lipgloss.Color("#88C0D0")).Bold(true)
	for i := range rows {
		if i == m.settingsCursor {
			rows[i] = sel.Render("▸" + rows[i][1:])
		}
	}
	body := strings.Join(rows, "\n")
	hint := "  ↑/↓ select  ·  enter/space change  ·  - back  ·  s save"
	if m.settingsSaved {
		hint += "   ✓ saved"
	} else if m.settingsErr != "" {
		hint += "   ⚠ " + m.settingsErr
	} else if m.settingsDirty {
		hint += "   ● unsaved"
	}
	footer := "\n" + lipgloss.NewStyle().Foreground(lipgloss.Color("#4C566A")).Render(hint)
	return m.panelStyle.Width(m.width - 4).Render(m.titleStyle.Render(" Settings ") + "\n\n" + body + footer)
}

// formatTemp renders a Celsius reading honoring the configured TemperatureUnit
// (°C default, °F when the user selected Fahrenheit in Settings).
func (m Model) formatTemp(c float64) string {
	if m.settings != nil && m.settings.TemperatureUnit == "F" {
		return fmt.Sprintf("%5.1f°F", c*9/5+32)
	}
	return fmt.Sprintf("%5.1f°C", c)
}

func (m Model) renderTemperature() string {
	if m.last.LastUpdate.IsZero() {
		return m.renderMetricStatePanel("Temperature", "Waiting for first temperature sample…", "", false)
	}
	temp := m.last.Temperature
	if temp.State.State != "" && temp.State.State != collector.MetricObserved {
		reason := strings.TrimSpace(temp.State.Reason)
		if reason == "" {
			reason = string(temp.State.State)
		}
		return m.renderMetricStatePanel("Temperature", "Unavailable", reason, true)
	}

	badge := tempBadge(temp.Source)
	rows := []string{m.titleStyle.Render(" Sensor Readings "), "", "  Source:" + badge,
		fmt.Sprintf("  CPU Package  %s · %s", m.formatTemp(temp.CPUPackage), gaugeLabel(temp.CPUPackage)),
		fmt.Sprintf("  CPU Cores    %s · %s", m.formatTemp(temp.CPUCores), gaugeLabel(temp.CPUCores)),
		fmt.Sprintf("  GPU          %s · %s", m.formatTemp(temp.GPU), gaugeLabel(temp.GPU)),
		fmt.Sprintf("  ANE          %s · %s", m.formatTemp(temp.ANE), gaugeLabel(temp.ANE)),
		fmt.Sprintf("  Battery      %s · %s", m.formatTemp(temp.Battery), gaugeLabel(temp.Battery)),
	}
	fan := "  Fan unavailable · restricted SMC keys"
	if temp.FanRPM > 0 || temp.FanMode != "" {
		fan = fmt.Sprintf("  Fan          %d RPM · %s", temp.FanRPM, temp.FanMode)
	}
	rows = append(rows, "", fan)
	return m.panelStyle.Width(metricPanelWidth(m.width)).Render(strings.Join(rows, "\n"))
}
