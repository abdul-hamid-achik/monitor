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

// studioHistorySize is how many samples the live sparklines keep: one minute
// at the default 1s interval. Charts stretch this many samples across their
// width (Sparkline.Capacity) so a full buffer fills the panel.
const studioHistorySize = 60

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
	c := collector.New(collector.Options{Interval: interval, HistorySize: studioHistorySize})
	// NewDefaultEngine is the single rule-set source shared with `monitor
	// watch` and the MCP/CLI analyze window (bug 17: Studio's engine used to
	// have no ThresholdRule, so the config.json alert thresholds had no
	// effect here even though watch honored them).
	engine := analyzer.NewDefaultEngine(*settings)

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
			m.setView((m.view + 1) % viewCount)
			return m, nil
		case "shift+tab", "left", "h":
			m.setView((m.view + viewCount - 1) % viewCount)
			return m, nil
		case "1":
			m.setView(viewOverview)
			return m, nil
		case "2":
			m.setView(viewCPU)
			return m, nil
		case "3":
			m.setView(viewMemory)
			return m, nil
		case "4":
			m.setView(viewTemperature)
			return m, nil
		case "5":
			m.setView(viewDisk)
			return m, nil
		case "6":
			m.setView(viewNetwork)
			return m, nil
		case "7":
			m.setView(viewProcesses)
			return m, nil
		case "8":
			m.setView(viewSettings)
			return m, nil
		case "9":
			m.setView(viewTrends)
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
				m.setView(v)
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
	hints := [][2]string{{"tab", "switch"}, {"p", "pause"}, {"r", "refresh"}, {"?", "help"}, {"q", "quit"}}
	switch m.view {
	case viewProcesses:
		hints = [][2]string{{"↑↓", "move"}, {"enter", "inspect"}, {"/", "filter"}, {"K", "terminate"}, {"?", "help"}}
	case viewSettings:
		hints = [][2]string{{"↑↓", "select"}, {"enter", "change"}, {"s", "save"}, {"?", "help"}}
	case viewTrends:
		hints = [][2]string{{"r", "reload history"}, {"tab", "switch"}, {"?", "help"}, {"q", "quit"}}
	}
	// Keycaps: the key in the accent, its action muted -- the same pairing
	// the docs site uses for its <kbd> hints.
	key := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent)
	action := lipgloss.NewStyle().Foreground(m.theme.Muted)
	parts := make([]string, 0, len(hints))
	for _, h := range hints {
		parts = append(parts, key.Render(h[0])+" "+action.Render(h[1]))
	}
	left := " " + strings.Join(parts, "   ") + " "
	right := ""
	if m.settings != nil && m.settings.UpdateInterval > 0 {
		right = action.Render(fmt.Sprintf("every %s ", m.settings.UpdateInterval))
	}
	if m.width >= 80 {
		return joinEnds(m.width, left, right)
	}
	return operationalFit(left, m.width)
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
	// Brand row: the same "◆ monitor" wordmark and "● live" pulse the docs
	// site draws on its terminal frames.
	left := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent).Render(" ◆ ") +
		lipgloss.NewStyle().Bold(true).Foreground(m.theme.Text).Render("monitor")
	if m.width >= 58 {
		left += lipgloss.NewStyle().Foreground(m.theme.Muted).Render("  " + fitText(host, 24))
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
	right := lipgloss.NewStyle().Foreground(stateColor).Render(stateSummary + " ")
	identityRow := lipgloss.NewStyle().
		Width(maxInt(1, m.width)).
		Foreground(m.theme.Text).
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
		Render(navigation)
	rule := lipgloss.NewStyle().Foreground(m.theme.Border).Render(strings.Repeat("─", maxInt(1, m.width)))
	return lipgloss.JoinVertical(lipgloss.Left, identityRow, navigation, rule)
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

func (m Model) tempBadge(source string) string {
	switch source {
	case "powermetrics":
		return lipgloss.NewStyle().Foreground(m.theme.Good).Render(" ● real")
	default:
		return lipgloss.NewStyle().Foreground(m.theme.Muted).Render(" ● est")
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

func (m Model) renderOverview() string {
	return m.renderOperationalOverview()
}

func (m Model) renderSettings() string {
	if m.settings == nil {
		return m.panel(m.width, m.titleStyle.Render(" Settings ")+"\n\nNo settings loaded.")
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
	settings := [][2]string{
		{"Update interval", s.UpdateInterval.String()},
		{"Temperature unit", "°" + s.TemperatureUnit},
		{"System processes", showSys},
		{"Max processes", fmt.Sprintf("%d", s.MaxProcesses)},
		{"Mouse", fmt.Sprintf("%t", s.MouseEnabled)},
		{"CPU alert", cpuAlert},
		{"Memory alert", memAlert},
	}
	// The selected row gets Studio's "▸" pointer and the accent; the rest
	// pair a muted label with a bright value, as every other panel does.
	sel := lipgloss.NewStyle().Foreground(m.theme.Accent).Bold(true)
	rows := make([]string, len(settings))
	for i, kv := range settings {
		if i == m.settingsCursor {
			rows[i] = sel.Render(fmt.Sprintf("▸ %-18s %s", kv[0], kv[1]))
			continue
		}
		rows[i] = m.kv(kv[0], 18, kv[1])
	}
	body := strings.Join(rows, "\n")
	key := lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent)
	hint := "  " + key.Render("↑↓") + m.muted(" select   ") + key.Render("enter") + m.muted(" change   ") +
		key.Render("-") + m.muted(" back   ") + key.Render("s") + m.muted(" save")
	if m.settingsSaved {
		hint += lipgloss.NewStyle().Foreground(m.theme.Good).Render("   ✓ saved")
	} else if m.settingsErr != "" {
		hint += lipgloss.NewStyle().Foreground(m.theme.Critical).Render("   ⚠ " + m.settingsErr)
	} else if m.settingsDirty {
		hint += lipgloss.NewStyle().Foreground(m.theme.Warning).Render("   ● unsaved")
	}
	footer := "\n" + hint
	return m.panel(m.width, m.titleStyle.Render(" Settings ")+"\n"+body+"\n"+footer)
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

	badge := m.tempBadge(temp.Source)
	contentWidth := metricPanelWidth(m.width) - m.panelStyle.GetHorizontalFrameSize()
	barWidth := minInt(40, contentWidth-36)
	sensor := func(label string, c float64) string {
		state := lipgloss.NewStyle().Foreground(m.theme.Good)
		switch {
		case c >= 85:
			state = lipgloss.NewStyle().Foreground(m.theme.Critical)
		case c >= 70:
			state = lipgloss.NewStyle().Foreground(m.theme.Warning)
		}
		if barWidth < 6 {
			return operationalFit(m.kv(label, 12, m.formatTemp(c))+" "+state.Render(gaugeLabel(c)), contentWidth)
		}
		// The bar spans 0-100 °C whatever the display unit.
		return m.kv(label, 12, m.formatTemp(c)) + "  " + m.gauge(c, barWidth, 70, 85) + "  " + state.Render(gaugeLabel(c))
	}
	rows := []string{m.titleStyle.Render(" Sensor Readings "), m.kv("source", 12, strings.TrimSpace(badge)),
		"",
		sensor("CPU Package", temp.CPUPackage),
		sensor("CPU Cores", temp.CPUCores),
		sensor("GPU", temp.GPU),
		sensor("ANE", temp.ANE),
		sensor("Battery", temp.Battery),
	}
	fan := m.kv("Fan", 12, m.muted("unavailable · restricted SMC keys"))
	if temp.FanRPM > 0 || temp.FanMode != "" {
		fan = m.kv("Fan", 12, fmt.Sprintf("%d RPM · %s", temp.FanRPM, temp.FanMode))
	}
	rows = append(rows, "", operationalFit(fan, contentWidth))
	return m.panel(metricPanelWidth(m.width), strings.Join(rows, "\n"))
}

// setView switches tabs and brings the new tab's cached state up to date
// with the latest snapshot. The process table and the Trends cache are
// otherwise only refreshed on the next tick while their tab is visible, so
// entering them would show stale (or no) rows for up to an interval.
func (m *Model) setView(v viewID) {
	m.view = v
	switch v {
	case viewProcesses:
		m.updateProcessTable()
	case viewTrends:
		if m.trendsAt.IsZero() || time.Since(m.trendsAt) > 5*time.Second {
			m.refreshTrends()
		}
	}
}
