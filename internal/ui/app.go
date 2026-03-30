package ui

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/charmbracelet/bubbles/help"
	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/table"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"github.com/monitor/monitor/internal/config"
	"github.com/monitor/monitor/internal/system"
	"github.com/monitor/monitor/internal/widgets"
)

// Tab represents a view tab
type Tab int

const (
	TabOverview Tab = iota
	TabCPU
	TabMemory
	TabDisk
	TabNetwork
	TabProcesses
	TabTemperature
	TabSettings
)

// Tab names
var TabNames = []string{
	"Overview",
	"CPU",
	"Memory",
	"Disk",
	"Network",
	"Processes",
	"Temperature",
	"Settings",
}

type processColumnSpec struct {
	title  string
	width  int
	sortBy string
	defAsc bool
}

var processTableSpecs = []processColumnSpec{
	{title: "PID", width: 8, sortBy: "pid"},
	{title: "Name", width: 22, sortBy: "name", defAsc: true},
	{title: "CPU%", width: 7, sortBy: "cpu"},
	{title: "Memory", width: 10, sortBy: "memory"},
	{title: "I/O", width: 10, sortBy: "io"},
	{title: "Threads", width: 7, sortBy: "threads"},
	{title: "User", width: 12, sortBy: "user", defAsc: true},
}

type processLayout struct {
	panelX     int
	panelY     int
	tableX     int
	tableY     int
	tableLines []string
}

type processViewData struct {
	displayed     []system.ProcessInfo
	topByCPU      []system.ProcessInfo
	totalFiltered int
	ready         bool
}

// Context menu state
type ContextMenuState int

const (
	ContextMenuNone ContextMenuState = iota
	ContextMenuProcess
)

// Msg types
type systemInfoMsg struct {
	info system.SystemInfo
}

type tickMsg time.Time

// Model is the main application model
type Model struct {
	// State
	ctx        context.Context
	collector  *system.Collector
	width      int
	height     int
	ready      bool
	quitting   bool
	lastUpdate time.Time

	// Navigation
	activeTab   Tab
	showingHelp bool

	// System info
	systemInfo  system.SystemInfo
	processData processViewData

	// Components
	processTable table.Model
	help         help.Model
	spinner      spinner.Model

	// Process management
	selectedPids     map[int32]bool
	lastSelectedPid  int32
	sortBy           string
	sortAsc          bool
	contextMenuState ContextMenuState
	contextMenuX     int
	contextMenuY     int
	contextMenuPid   int32
	contextMenuName  string

	// Kill confirmation
	showKillConfirm  bool
	killConfirmation system.KillConfirmation
	forceKill        bool

	// Settings reset confirmation
	showResetConfirm bool

	// Mouse tracking
	mouseEnabled bool

	// Settings
	settings             *config.Settings
	selectedSetting      int
	statusMessage        string
	statusMessageIsError bool
	statusMessageTime    time.Time

	// Layout tracking for click detection
	tabBounds []struct{ start, end int }

	// Process search/filter
	processSearchMode  bool
	processSearchQuery string
}

// keyMap defines keyboard shortcuts
type keyMap struct {
	Quit      key.Binding
	NextTab   key.Binding
	PrevTab   key.Binding
	Refresh   key.Binding
	Help      key.Binding
	Kill      key.Binding
	ForceKill key.Binding
	SortCPU   key.Binding
	SortMem   key.Binding
	Up        key.Binding
	Down      key.Binding
	PageUp    key.Binding
	PageDown  key.Binding
	Home      key.Binding
	End       key.Binding
	Enter     key.Binding
	Escape    key.Binding
	SelectAll key.Binding
	ClearSel  key.Binding
	Search    key.Binding
	Export    key.Binding
}

func (k keyMap) ShortHelp() []key.Binding {
	return []key.Binding{k.Quit, k.NextTab, k.PrevTab, k.Refresh, k.Help}
}

func (k keyMap) FullHelp() [][]key.Binding {
	return [][]key.Binding{
		{k.Quit, k.NextTab, k.PrevTab, k.Refresh},
		{k.Help, k.Kill, k.ForceKill},
		{k.SortCPU, k.SortMem},
		{k.Up, k.Down, k.PageUp, k.PageDown},
		{k.Enter, k.Escape},
		{k.SelectAll, k.ClearSel},
	}
}

var keys = keyMap{
	Quit:      key.NewBinding(key.WithKeys("q", "ctrl+c"), key.WithHelp("q", "quit")),
	NextTab:   key.NewBinding(key.WithKeys("tab", "right", "l"), key.WithHelp("→", "next tab")),
	PrevTab:   key.NewBinding(key.WithKeys("shift+tab", "left", "h"), key.WithHelp("←", "prev tab")),
	Refresh:   key.NewBinding(key.WithKeys("r", "R"), key.WithHelp("r", "refresh")),
	Help:      key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
	Kill:      key.NewBinding(key.WithKeys("k", "K"), key.WithHelp("k", "kill (SIGTERM)")),
	ForceKill: key.NewBinding(key.WithKeys("x", "X"), key.WithHelp("x", "force kill (SIGKILL)")),
	SortCPU:   key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "sort by CPU")),
	SortMem:   key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "sort by Memory")),
	Up:        key.NewBinding(key.WithKeys("up", "w"), key.WithHelp("↑", "up")),
	Down:      key.NewBinding(key.WithKeys("down", "s"), key.WithHelp("↓", "down")),
	PageUp:    key.NewBinding(key.WithKeys("pgup", "b"), key.WithHelp("pgup", "page up")),
	PageDown:  key.NewBinding(key.WithKeys("pgdown", "f"), key.WithHelp("pgdn", "page down")),
	Home:      key.NewBinding(key.WithKeys("home", "g"), key.WithHelp("home", "go to top")),
	End:       key.NewBinding(key.WithKeys("end", "G"), key.WithHelp("end", "go to bottom")),
	Enter:     key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
	Escape:    key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "close menu")),
	SelectAll: key.NewBinding(key.WithKeys("ctrl+a"), key.WithHelp("ctrl+a", "select all")),
	ClearSel:  key.NewBinding(key.WithKeys("ctrl+d"), key.WithHelp("ctrl+d", "clear selection")),
	Search:    key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
	Export:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "export to JSON")),
}

func NewModel() Model {
	ctx := context.Background()
	m := Model{
		ctx:          ctx,
		collector:    system.NewCollector(),
		activeTab:    TabOverview,
		help:         help.New(),
		selectedPids: make(map[int32]bool),
		mouseEnabled: true,
		spinner:      spinner.New(),
		sortBy:       "cpu",
		sortAsc:      false,
		tabBounds:    make([]struct{ start, end int }, len(TabNames)),
	}
	m.setupProcessTable()
	m.calculateTabBounds()

	settings, err := config.Load()
	if err != nil {
		settings = config.Default()
	}
	m.settings = settings
	m.applySettings()

	return m
}

func (m *Model) applySettings() {
	if m.settings == nil {
		m.settings = config.Default()
	}
	m.mouseEnabled = m.settings.MouseEnabled
	m.syncCollectorProcessSettings()
}

func (m *Model) refreshProcessData() {
	data := m.buildProcessViewData()
	data.ready = true
	m.processData = data
}

func processRefreshIntervalFor(updateInterval time.Duration, activeTab Tab) time.Duration {
	if updateInterval <= 0 {
		updateInterval = config.DefaultSettings.UpdateInterval
	}

	interval := updateInterval * 4
	minInterval := 3 * time.Second
	maxInterval := 8 * time.Second
	if activeTab == TabProcesses {
		interval = updateInterval * 2
		minInterval = time.Second
		maxInterval = 3 * time.Second
	}

	return clampDuration(interval, minInterval, maxInterval)
}

func clampDuration(value, minValue, maxValue time.Duration) time.Duration {
	if value < minValue {
		return minValue
	}
	if value > maxValue {
		return maxValue
	}
	return value
}

func (m *Model) syncCollectorProcessSettings() {
	if m.collector == nil {
		return
	}

	updateInterval := config.DefaultSettings.UpdateInterval
	if m.settings != nil && m.settings.UpdateInterval > 0 {
		updateInterval = m.settings.UpdateInterval
	}

	m.collector.SetProcessCollectionOptions(
		processRefreshIntervalFor(updateInterval, m.activeTab),
		m.activeTab == TabProcesses,
	)
}

func (m *Model) activateTab(tab Tab) tea.Cmd {
	if m.activeTab == tab {
		return nil
	}

	m.activeTab = tab
	m.calculateTabBounds()
	m.syncCollectorProcessSettings()
	if tab == TabProcesses {
		m.updateProcessTable()
		return m.fetchSystemInfo()
	}
	return nil
}

func (m *Model) setupProcessTable() {
	columns := make([]table.Column, 0, len(processTableSpecs))
	for _, spec := range processTableSpecs {
		columns = append(columns, table.Column{Title: spec.title, Width: spec.width})
	}
	m.processTable = table.New(table.WithColumns(columns), table.WithHeight(20), table.WithFocused(true))
	s := table.DefaultStyles()
	s.Header = s.Header.BorderStyle(lipgloss.NormalBorder()).BorderForeground(lipgloss.Color(Nord9)).BorderBottom(true).Bold(true).Foreground(lipgloss.Color(Nord8))
	s.Selected = s.Selected.Foreground(lipgloss.Color(Nord0)).Background(lipgloss.Color(Nord8)).Bold(true)
	m.processTable.SetStyles(s)
}

func (m Model) processViewTitle() string {
	if len(m.selectedPids) > 0 {
		return fmt.Sprintf(" Processes - %d selected │ Space:toggle Enter:menu k:kill x:force-kill ", len(m.selectedPids))
	}
	return " Processes - Click header to sort, Right-click for menu "
}

func (m Model) renderProcessesPanel() string {
	var content []string
	content = append(content, PanelTitleStyle.Render(m.processViewTitle()))

	// Add search input if in search mode
	if m.processSearchMode {
		searchStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(Nord8))
		queryStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(Nord4))
		content = append(content, searchStyle.Render(" Search: ")+queryStyle.Render(m.processSearchQuery)+"_")
	}

	content = append(content, "", m.processTable.View())

	return PanelStyle.Width(m.width - 4).Render(
		lipgloss.JoinVertical(lipgloss.Left, content...),
	)
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.tickCmd(), m.fetchSystemInfo(), m.spinner.Tick)
}

func (m Model) tickCmd() tea.Cmd {
	interval := time.Second
	if m.settings != nil {
		interval = m.settings.UpdateInterval
	}
	return tea.Tick(interval, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m Model) fetchSystemInfo() tea.Cmd {
	return func() tea.Msg {
		info := m.collector.Collect(context.Background())
		return systemInfoMsg{info: info}
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		return m.handleKeyPress(msg)
	case tea.MouseMsg:
		return m.handleMouse(msg)
	case tea.WindowSizeMsg:
		return m.handleWindowSize(msg)
	case tickMsg:
		return m.handleTick(msg)
	case systemInfoMsg:
		return m.handleSystemInfo(msg)
	default:
		if !m.showKillConfirm && m.contextMenuState == ContextMenuNone {
			switch m.activeTab {
			case TabProcesses:
				var cmd tea.Cmd
				m.processTable, cmd = m.processTable.Update(msg)
				return m, cmd
			}
		}
		return m, nil
	}
}

func (m Model) handleKeyPress(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.contextMenuState != ContextMenuNone {
		return m.handleContextMenuKeys(msg)
	}
	switch {
	case key.Matches(msg, keys.Quit):
		m.quitting = true
		return m, tea.Quit
	case key.Matches(msg, keys.Help):
		m.showingHelp = !m.showingHelp
		return m, nil
	case key.Matches(msg, keys.Refresh):
		return m, m.fetchSystemInfo()
	case key.Matches(msg, keys.Escape):
		if m.processSearchMode {
			m.processSearchMode = false
			m.processSearchQuery = ""
			m.refreshProcessData()
			m.updateProcessTable()
			return m, nil
		}
		if len(m.selectedPids) > 0 {
			m.selectedPids = make(map[int32]bool)
			m.lastSelectedPid = 0
			m.updateProcessTable()
			return m, nil
		}
		m.contextMenuState = ContextMenuNone
		return m, nil
	}
	if m.showKillConfirm {
		return m.handleKillConfirmKeys(msg)
	}
	if m.activeTab == TabSettings {
		switch msg.String() {
		case "1", "2", "3", "4":
			return m.handleSettingsKeys(msg)
		}
	}
	switch msg.String() {
	case "1":
		return m, m.activateTab(TabOverview)
	case "2":
		return m, m.activateTab(TabCPU)
	case "3":
		return m, m.activateTab(TabMemory)
	case "4":
		return m, m.activateTab(TabDisk)
	case "5":
		return m, m.activateTab(TabNetwork)
	case "6":
		return m, m.activateTab(TabProcesses)
	case "7":
		return m, m.activateTab(TabTemperature)
	case "8":
		return m, m.activateTab(TabSettings)
	}
	switch {
	case key.Matches(msg, keys.NextTab):
		nextTab := m.activeTab + 1
		if nextTab > TabSettings {
			nextTab = TabOverview
		}
		return m, m.activateTab(nextTab)
	case key.Matches(msg, keys.PrevTab):
		prevTab := TabSettings
		if m.activeTab > 0 {
			prevTab = m.activeTab - 1
		}
		return m, m.activateTab(prevTab)
	}
	switch m.activeTab {
	case TabProcesses:
		// Handle search mode text input first
		if m.processSearchMode {
			switch msg.Type {
			case tea.KeyBackspace:
				if len(m.processSearchQuery) > 0 {
					m.processSearchQuery = m.processSearchQuery[:len(m.processSearchQuery)-1]
					m.refreshProcessData()
					m.updateProcessTable()
				}
				return m, nil
			case tea.KeyRunes:
				// Only accept printable characters
				if len(msg.Runes) > 0 && msg.Runes[0] >= 32 && msg.Runes[0] < 127 {
					m.processSearchQuery += string(msg.Runes)
					m.refreshProcessData()
					m.updateProcessTable()
				}
				return m, nil
			}
		}
		return m.handleProcessKeys(msg)
	case TabSettings:
		return m.handleSettingsKeys(msg)
	}
	return m, nil
}

func (m Model) handleContextMenuKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.contextMenuState = ContextMenuNone
		return m, nil
	case "1":
		m.openContextMenuKill(false)
		return m, nil
	case "2":
		m.openContextMenuKill(true)
		return m, nil
	case "3", "c", "C":
		if err := clipboard.WriteAll(fmt.Sprintf("%d", m.contextMenuPid)); err != nil {
			m.setStatusMessage(fmt.Sprintf("Copy failed: %v", err), true)
		} else {
			m.setStatusMessage("PID copied to clipboard", false)
		}
		m.contextMenuState = ContextMenuNone
		return m, nil
	case "4", "n", "N":
		if err := clipboard.WriteAll(m.contextMenuName); err != nil {
			m.setStatusMessage(fmt.Sprintf("Copy failed: %v", err), true)
		} else {
			m.setStatusMessage("Process name copied to clipboard", false)
		}
		m.contextMenuState = ContextMenuNone
		return m, nil
	case "enter", "k", "K":
		m.openContextMenuKill(false)
		return m, nil
	}
	return m, nil
}

func (m Model) handleProcessKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, keys.SelectAll):
		m.selectAllProcesses()
		m.updateProcessTable()
		return m, nil
	case key.Matches(msg, keys.ClearSel):
		m.selectedPids = make(map[int32]bool)
		m.lastSelectedPid = 0
		m.updateProcessTable()
		return m, nil
	case msg.String() == " " || msg.String() == "space":
		pid, ok := m.selectedRowPID()
		if !ok {
			return m, nil
		}
		if m.selectedPids[pid] {
			delete(m.selectedPids, pid)
			if m.lastSelectedPid == pid {
				m.lastSelectedPid = 0
			}
		} else {
			m.selectedPids[pid] = true
			m.lastSelectedPid = pid
		}
		m.updateProcessTable()
		return m, nil
	case key.Matches(msg, keys.Kill):
		// Kill all selected processes, or the cursor row if none selected
		if len(m.selectedPids) == 0 {
			if pid, ok := m.selectedRowPID(); ok {
				m.selectedPids = map[int32]bool{pid: true}
				m.lastSelectedPid = pid
			}
		}
		if len(m.selectedPids) > 0 {
			m.forceKill = false
			m.killConfirmation = system.CheckKillSafety(m.getSelectedPidsSlice())
			m.showKillConfirm = true
		}
		return m, nil
	case key.Matches(msg, keys.ForceKill):
		if len(m.selectedPids) == 0 {
			if pid, ok := m.selectedRowPID(); ok {
				m.selectedPids = map[int32]bool{pid: true}
				m.lastSelectedPid = pid
			}
		}
		if len(m.selectedPids) > 0 {
			m.forceKill = true
			m.killConfirmation = system.CheckKillSafety(m.getSelectedPidsSlice())
			m.showKillConfirm = true
		}
		return m, nil
	case key.Matches(msg, keys.SortCPU):
		if m.sortBy == "cpu" {
			m.sortAsc = !m.sortAsc
		} else {
			m.sortBy = "cpu"
			m.sortAsc = false
		}
		m.sortProcesses()
		return m, nil
	case key.Matches(msg, keys.SortMem):
		if m.sortBy == "memory" {
			m.sortAsc = !m.sortAsc
		} else {
			m.sortBy = "memory"
			m.sortAsc = false
		}
		m.sortProcesses()
		return m, nil
	case key.Matches(msg, keys.Export):
		m.exportProcesses()
		return m, nil
	case key.Matches(msg, keys.Search):
		m.processSearchMode = !m.processSearchMode
		if !m.processSearchMode {
			m.processSearchQuery = ""
		}
		m.refreshProcessData()
		m.updateProcessTable()
		return m, nil
	case key.Matches(msg, keys.Enter):
		if row := m.processTable.SelectedRow(); len(row) > 0 {
			pid, ok := m.selectedRowPID()
			if !ok {
				return m, nil
			}
			name := row[1]
			cursor := m.processTable.Cursor()
			displayed := m.displayProcesses()
			if cursor >= 0 && cursor < len(displayed) {
				name = displayed[cursor].Name
			}
			m.contextMenuState = ContextMenuProcess
			m.contextMenuPid = pid
			m.contextMenuName = name
			m.clampContextMenuPosition(30, 6+cursor)
		}
		return m, nil
	}
	return m, nil
}

func (m Model) handleSettingsKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	// Handle reset confirmation keys first
	if m.showResetConfirm {
		switch msg.String() {
		case "y", "Y":
			m.settings = config.Default()
			m.saveSettings()
			m.applySettings()
			m.refreshProcessData()
			m.updateProcessTable()
			m.showResetConfirm = false
			m.setStatusMessage("Settings reset to defaults", false)
			return m, nil
		case "n", "N", "esc":
			m.showResetConfirm = false
			return m, nil
		}
		return m, nil
	}

	numSettings := 6
	switch msg.String() {
	case "up", "w":
		if m.selectedSetting > 0 {
			m.selectedSetting--
		}
		return m, nil
	case "down", "s":
		if m.selectedSetting < numSettings-1 {
			m.selectedSetting++
		}
		return m, nil
	case "1":
		m.selectedSetting = 0
		return m, nil
	case "2":
		m.selectedSetting = 1
		return m, nil
	case "3":
		m.selectedSetting = 2
		return m, nil
	case "4":
		m.selectedSetting = 3
		return m, nil
	case "5":
		m.selectedSetting = 4
		return m, nil
	case "6":
		m.selectedSetting = 5
		return m, nil
	case "right", "l", "enter", " ":
		m.changeSetting(m.selectedSetting)
		return m, nil
	case "left", "h":
		m.changeSettingPrev(m.selectedSetting)
		return m, nil
	case "r", "R":
		m.showResetConfirm = true
		return m, nil
	}
	return m, nil
}

func (m *Model) changeSetting(idx int) {
	m.cycleSetting(idx, 1)
}

func (m *Model) changeSettingPrev(idx int) {
	m.cycleSetting(idx, -1)
}

func (m *Model) cycleSetting(idx int, direction int) {
	if m.settings == nil {
		return
	}
	switch idx {
	case 0:
		intervals := []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second}
		currentIdx := 0
		for i, iv := range intervals {
			if m.settings.UpdateInterval == iv {
				currentIdx = i
				break
			}
		}
		m.settings.UpdateInterval = intervals[(currentIdx+direction+len(intervals))%len(intervals)]
	case 1:
		if m.settings.TemperatureUnit == "C" {
			m.settings.TemperatureUnit = "F"
		} else {
			m.settings.TemperatureUnit = "C"
		}
	case 2:
		m.settings.ShowSystemProcesses = !m.settings.ShowSystemProcesses
	case 3:
		maxes := []int{20, 50, 100, 200}
		currentIdx := 0
		for i, mx := range maxes {
			if m.settings.MaxProcesses == mx {
				currentIdx = i
				break
			}
		}
		m.settings.MaxProcesses = maxes[(currentIdx+direction+len(maxes))%len(maxes)]
	case 4:
		// CPU Alert Threshold: OFF, 50%, 70%, 80%, 90%, 95%
		thresholds := []float64{0, 50, 70, 80, 90, 95}
		currentIdx := 0
		for i, th := range thresholds {
			if m.settings.CPUAlertThreshold == th {
				currentIdx = i
				break
			}
		}
		m.settings.CPUAlertThreshold = thresholds[(currentIdx+direction+len(thresholds))%len(thresholds)]
	case 5:
		// Memory Alert Threshold: OFF, 50%, 70%, 80%, 90%, 95%
		thresholds := []float64{0, 50, 70, 80, 90, 95}
		currentIdx := 0
		for i, th := range thresholds {
			if m.settings.MemoryAlertThreshold == th {
				currentIdx = i
				break
			}
		}
		m.settings.MemoryAlertThreshold = thresholds[(currentIdx+direction+len(thresholds))%len(thresholds)]
	}
	m.saveSettings()
	m.applySettings()
	m.refreshProcessData()
	m.updateProcessTable()
}

func (m *Model) saveSettings() {
	if m.settings == nil {
		return
	}
	if err := m.settings.Save(); err != nil {
		m.setStatusMessage(fmt.Sprintf("Failed to save settings: %v", err), true)
		return
	}
	m.setStatusMessage("Settings saved", false)
}

func (m *Model) openContextMenuKill(force bool) {
	if m.contextMenuState != ContextMenuProcess {
		return
	}

	m.selectedPids = map[int32]bool{m.contextMenuPid: true}
	m.lastSelectedPid = m.contextMenuPid
	m.forceKill = force
	m.killConfirmation = system.CheckKillSafety(m.getSelectedPidsSlice())
	m.showKillConfirm = true
	m.contextMenuState = ContextMenuNone
	m.updateProcessTable()
}

func (m *Model) processLayout() processLayout {
	const (
		processPanelY        = 1
		processPanelBorder   = 1
		processPanelPaddingX = 2
		processPanelPaddingY = 1
	)

	titleHeight := strings.Count(PanelTitleStyle.Render(m.processViewTitle()), "\n") + 1
	panel := m.renderProcessesPanel()
	panelWidth := lipgloss.Width(panel)
	panelX := 0
	if m.width > panelWidth {
		panelX = (m.width - panelWidth) / 2
	}

	return processLayout{
		panelX:     panelX,
		panelY:     processPanelY,
		tableX:     panelX + processPanelBorder + processPanelPaddingX,
		tableY:     processPanelY + processPanelBorder + processPanelPaddingY + titleHeight + 1,
		tableLines: strings.Split(m.processTable.View(), "\n"),
	}
}

func (m Model) processColumnHit(x int) (string, bool) {
	columnEnd := 0
	for _, spec := range processTableSpecs {
		columnEnd += spec.width + 2
		if x < columnEnd {
			return spec.sortBy, spec.defAsc
		}
	}
	return "cpu", false
}

func (m Model) processRowHit(layout processLayout, x, y int) (int, bool) {
	const tableHeaderLines = 2

	rowIndex := y - (layout.tableY + tableHeaderLines)
	if rowIndex < 0 {
		return 0, false
	}

	lineIndex := tableHeaderLines + rowIndex
	if lineIndex < 0 || lineIndex >= len(layout.tableLines) {
		return 0, false
	}

	rowWidth := ansi.StringWidth(layout.tableLines[lineIndex])
	if x < layout.tableX || x >= layout.tableX+rowWidth {
		return 0, false
	}

	return rowIndex, true
}

func (m Model) handleContextMenuMouse(msg tea.MouseMsg) (Model, tea.Cmd) {
	menu := m.renderContextMenu()
	menuLines := strings.Split(menu, "\n")
	menuWidth := 0
	for _, line := range menuLines {
		menuWidth = max(menuWidth, ansi.StringWidth(line))
	}
	menuHeight := len(menuLines)

	if msg.X < m.contextMenuX || msg.X >= m.contextMenuX+menuWidth || msg.Y < m.contextMenuY || msg.Y >= m.contextMenuY+menuHeight {
		m.contextMenuState = ContextMenuNone
		return m, nil
	}

	line := menuLines[msg.Y-m.contextMenuY]
	switch {
	case strings.Contains(line, "[1] Kill"):
		m.openContextMenuKill(false)
	case strings.Contains(line, "[2] Force Kill"):
		m.openContextMenuKill(true)
	case strings.Contains(line, "[3] Copy PID"):
		if err := clipboard.WriteAll(fmt.Sprintf("%d", m.contextMenuPid)); err != nil {
			m.setStatusMessage(fmt.Sprintf("Copy failed: %v", err), true)
		} else {
			m.setStatusMessage("PID copied to clipboard", false)
		}
		m.contextMenuState = ContextMenuNone
	case strings.Contains(line, "[4] Copy Name"):
		if err := clipboard.WriteAll(m.contextMenuName); err != nil {
			m.setStatusMessage(fmt.Sprintf("Copy failed: %v", err), true)
		} else {
			m.setStatusMessage("Process name copied to clipboard", false)
		}
		m.contextMenuState = ContextMenuNone
	case strings.Contains(line, "[Esc] Close"):
		m.contextMenuState = ContextMenuNone
	}

	return m, nil
}

func (m *Model) clampContextMenuPosition(x, y int) {
	menuLines := strings.Split(m.renderContextMenu(), "\n")
	menuWidth := 0
	for _, line := range menuLines {
		menuWidth = max(menuWidth, ansi.StringWidth(line))
	}
	menuHeight := len(menuLines)

	maxX := max(0, m.width-menuWidth)
	maxY := max(0, m.height-menuHeight-1)

	if x < 0 {
		x = 0
	}
	if y < 0 {
		y = 0
	}
	if x > maxX {
		x = maxX
	}
	if y > maxY {
		y = maxY
	}

	m.contextMenuX = x
	m.contextMenuY = y
}

func (m Model) handleKillConfirmKeys(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "esc", "n", "N":
		m.showKillConfirm = false
		m.selectedPids = make(map[int32]bool)
		m.forceKill = false
		m.updateProcessTable()
		return m, nil
	case "y", "Y":
		killed := 0
		skipped := 0
		killErrors := []string{}
		for pid := range m.selectedPids {
			protected := false
			for _, p := range m.killConfirmation.Processes {
				if p.PID == pid && p.IsProtected {
					protected = true
					break
				}
			}
			if !protected {
				if err := system.KillProcess(pid, m.forceKill); err != nil {
					killErrors = append(killErrors, fmt.Sprintf("PID %d: %v", pid, err))
					continue
				}
				killed++
				continue
			}
			skipped++
		}
		m.setKillStatus(killed, skipped, killErrors)
		m.showKillConfirm = false
		m.selectedPids = make(map[int32]bool)
		m.forceKill = false
		m.updateProcessTable()
		return m, m.fetchSystemInfo()
	}
	return m, nil
}

func (m Model) handleMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if !m.mouseEnabled {
		return m, nil
	}
	if m.contextMenuState != ContextMenuNone && msg.Action == tea.MouseActionPress {
		return m.handleContextMenuMouse(msg)
	}
	if msg.Action == tea.MouseActionPress {
		switch msg.Button {
		case tea.MouseButtonWheelUp:
			if m.activeTab == TabProcesses {
				row := m.processTable.Cursor()
				if row > 0 {
					m.processTable.SetCursor(row - 1)
				}
			}
			return m, nil
		case tea.MouseButtonWheelDown:
			if m.activeTab == TabProcesses {
				row := m.processTable.Cursor()
				if row < len(m.processTable.Rows())-1 {
					m.processTable.SetCursor(row + 1)
				}
			}
			return m, nil
		}
	}
	if msg.Action == tea.MouseActionPress {
		// Use pre-calculated tabBounds for accurate click detection
		if msg.Y == 0 && m.width > 0 && len(m.tabBounds) == len(TabNames) {
			for i, bounds := range m.tabBounds {
				if msg.X >= bounds.start && msg.X < bounds.end {
					return m, m.activateTab(Tab(i))
				}
			}
		}
		if m.activeTab == TabProcesses {
			layout := m.processLayout()
			if len(layout.tableLines) > 0 && msg.Y == layout.tableY {
				headerWidth := ansi.StringWidth(layout.tableLines[0])
				if msg.X >= layout.tableX && msg.X < layout.tableX+headerWidth {
					sortCol, defAsc := m.processColumnHit(msg.X - layout.tableX)
					if m.sortBy == sortCol {
						m.sortAsc = !m.sortAsc
					} else {
						m.sortBy = sortCol
						m.sortAsc = defAsc
					}
					m.sortProcesses()
					return m, nil
				}
			}

			cursorIndex, ok := m.processRowHit(layout, msg.X, msg.Y)
			displayed := m.displayProcesses()
			if ok && cursorIndex < len(displayed) {
				clickedProc := displayed[cursorIndex]
				hasShift, hasCtrl := msg.Shift, msg.Ctrl || msg.Alt
				switch {
				case hasShift && m.lastSelectedPid != 0:
					m.selectedPids = make(map[int32]bool)
					m.selectRange(m.lastSelectedPid, clickedProc.PID)
					m.processTable.SetCursor(cursorIndex)
				case hasCtrl:
					if m.selectedPids[clickedProc.PID] {
						delete(m.selectedPids, clickedProc.PID)
					} else {
						m.selectedPids[clickedProc.PID] = true
						m.lastSelectedPid = clickedProc.PID
					}
					m.processTable.SetCursor(cursorIndex)
				default:
					m.selectedPids = make(map[int32]bool)
					m.selectedPids[clickedProc.PID] = true
					m.lastSelectedPid = clickedProc.PID
					m.processTable.SetCursor(cursorIndex)
				}
				m.updateProcessTable()
				if msg.Button == tea.MouseButtonRight {
					m.contextMenuState = ContextMenuProcess
					m.contextMenuPid = clickedProc.PID
					m.contextMenuName = clickedProc.Name
					m.clampContextMenuPosition(msg.X-25, msg.Y+1)
				}
				return m, nil
			}
		}
	}
	return m, nil
}

func (m Model) handleWindowSize(msg tea.WindowSizeMsg) (tea.Model, tea.Cmd) {
	m.width, m.height, m.ready = msg.Width, msg.Height, true
	if m.width < 60 {
		m.width = 60
	}
	if m.height < 20 {
		m.height = 20
	}
	fixedOverhead := 10
	if m.showingHelp {
		fixedOverhead += 6
	}
	availableHeight := m.height - fixedOverhead
	if availableHeight < 5 {
		availableHeight = 5
	}
	tableWidth := max(60, m.width-6)
	m.processTable.SetHeight(availableHeight)
	m.processTable.SetWidth(tableWidth)
	m.calculateTabBounds()
	return m, nil
}

func (m Model) handleTick(msg tickMsg) (tea.Model, tea.Cmd) {
	m.lastUpdate = time.Time(msg)
	// Clear status message after 5 seconds
	if m.statusMessage != "" && !m.statusMessageTime.IsZero() {
		if time.Since(m.statusMessageTime) > 5*time.Second {
			m.statusMessage = ""
			m.statusMessageTime = time.Time{}
		}
	}
	return m, tea.Batch(m.tickCmd(), m.fetchSystemInfo(), m.spinner.Tick)
}

func (m Model) handleSystemInfo(msg systemInfoMsg) (tea.Model, tea.Cmd) {
	processDataChanged := !msg.info.ProcessesLastUpdate.Equal(m.systemInfo.ProcessesLastUpdate)
	m.systemInfo = msg.info
	if processDataChanged {
		m.refreshProcessData()
	}
	if m.activeTab == TabProcesses && processDataChanged {
		m.updateProcessTable()
	}

	// Check alert thresholds
	m.checkAlertThresholds()

	return m, nil
}

// checkAlertThresholds checks if CPU or memory usage exceeds configured thresholds
func (m *Model) checkAlertThresholds() {
	if m.settings == nil {
		return
	}

	// Check CPU threshold
	if m.settings.CPUAlertThreshold > 0 && m.systemInfo.CPU.UsagePercent >= m.settings.CPUAlertThreshold {
		m.setStatusMessage(fmt.Sprintf("⚠️  CPU Alert: %.1f%% exceeds threshold (%.0f%%)",
			m.systemInfo.CPU.UsagePercent, m.settings.CPUAlertThreshold), true)
	}

	// Check Memory threshold
	if m.settings.MemoryAlertThreshold > 0 && m.systemInfo.Memory.UsagePercent >= m.settings.MemoryAlertThreshold {
		m.setStatusMessage(fmt.Sprintf("⚠️  Memory Alert: %.1f%% exceeds threshold (%.0f%%)",
			m.systemInfo.Memory.UsagePercent, m.settings.MemoryAlertThreshold), true)
	}
}

func (m *Model) sortProcesses() {
	m.refreshProcessData()
	m.updateProcessTable()
}

func (m *Model) updateProcessTable() {
	procs := m.displayProcesses()
	rows := make([]table.Row, 0, len(procs))
	for _, p := range procs {
		name := truncate(p.Name, 20)
		if m.selectedPids[p.PID] {
			name = "▸ " + name
		} else {
			name = "  " + name
		}
		rows = append(rows, table.Row{
			fmt.Sprintf("%d", p.PID),
			name,
			fmt.Sprintf("%.1f", p.CPUPercent),
			system.FormatBytes(p.Memory),
			system.FormatBytes(p.IOReadBytes + p.IOWriteBytes),
			fmt.Sprintf("%d", p.Threads),
			truncate(p.User, 12),
		})
	}
	m.processTable.SetRows(rows)
	if len(rows) == 0 {
		m.processTable.SetCursor(0)
		return
	}
	if cursor := m.processTable.Cursor(); cursor >= len(rows) {
		m.processTable.SetCursor(len(rows) - 1)
	}
}

func (m Model) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}
	if !m.ready {
		return "Initializing...\n"
	}
	width, height := m.width, m.height
	if width < 60 {
		width = 60
	}
	if height < 20 {
		height = 20
	}
	headerHeight, statusBarHeight := 1, 1
	helpHeight := 0
	if m.showingHelp {
		helpHeight = 6
	}
	// Overlays (context menu, kill confirm) are rendered on top of content via placeOverlay/lipgloss.Place,
	// so they must NOT reduce availableContentHeight — otherwise the layout shifts when they appear.
	availableContentHeight := height - headerHeight - statusBarHeight - helpHeight - 2
	if availableContentHeight < 5 {
		availableContentHeight = 5
	}
	var contentBuilder strings.Builder
	contentBuilder.WriteString(m.renderHeader())
	contentBuilder.WriteString("\n")
	content := m.renderActiveTab()
	contentBuilder.WriteString(content)
	contentLines := strings.Count(content, "\n") + 1
	paddingNeeded := availableContentHeight - contentLines
	if paddingNeeded > 0 {
		for i := 0; i < paddingNeeded; i++ {
			contentBuilder.WriteString("\n")
		}
	}
	baseContent := contentBuilder.String()
	if m.contextMenuState != ContextMenuNone {
		menu := m.renderContextMenu()
		// Place overlay at the click position using ANSI-aware splicing
		baseContent = placeOverlay(baseContent, menu, m.contextMenuX, m.contextMenuY)
	}
	if m.showKillConfirm {
		dialog := m.renderKillConfirmation()
		baseContent = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
	}
	if m.showResetConfirm {
		dialog := m.renderResetConfirmation()
		baseContent = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
	}
	renderedLines := strings.Count(baseContent, "\n") + 1
	remainingLines := m.height - renderedLines - 1
	var finalBuilder strings.Builder
	finalBuilder.WriteString(baseContent)
	if remainingLines > 0 {
		for i := 0; i < remainingLines; i++ {
			finalBuilder.WriteString("\n")
		}
	}
	var b strings.Builder
	b.WriteString(finalBuilder.String())
	b.WriteString(m.renderStatusBar())
	if m.showingHelp {
		b.WriteString("\n")
		b.WriteString(m.help.View(keys))
	}
	return b.String()
}

func (m Model) renderActiveTab() string {
	switch m.activeTab {
	case TabOverview:
		return m.renderOverview()
	case TabCPU:
		return m.renderCPUView()
	case TabMemory:
		return m.renderMemoryView()
	case TabDisk:
		return m.renderDiskView()
	case TabNetwork:
		return m.renderNetworkView()
	case TabProcesses:
		return m.renderProcessesView()
	case TabTemperature:
		return m.renderTemperatureView()
	case TabSettings:
		return m.renderSettingsView()
	default:
		return "Unknown tab"
	}
}

func (m Model) renderHeader() string {
	var tabs []string
	for i, tabName := range TabNames {
		style := TabInactiveStyle
		if Tab(i) == m.activeTab {
			style = TabActiveStyle
		}
		// Add number hint for tab switching
		tabs = append(tabs, style.Render(fmt.Sprintf(" %d:%s ", i+1, tabName)))
	}
	tabsRow := lipgloss.JoinHorizontal(lipgloss.Left, tabs...)
	titleWidth := lipgloss.Width(TitleStyle.Render(" MONITOR "))
	tabsWidth := lipgloss.Width(tabsRow)
	availableWidth := m.width - titleWidth - tabsWidth - 4
	if availableWidth < 15 {
		header := lipgloss.JoinHorizontal(lipgloss.Top, TitleStyle.Render(" MONITOR "), tabsRow)
		return lipgloss.NewStyle().Width(m.width).Render(header)
	}
	sysInfo := time.Now().Format("15:04:05")
	sysInfoStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(Nord4)).Align(lipgloss.Right).Width(availableWidth)
	header := lipgloss.JoinHorizontal(lipgloss.Top, TitleStyle.Render(" MONITOR "), tabsRow, sysInfoStyle.Render(sysInfo))
	return lipgloss.NewStyle().Width(m.width).Render(header)
}

func (m *Model) calculateTabBounds() {
	var tabs []string
	for i, tabName := range TabNames {
		style := TabInactiveStyle
		if Tab(i) == m.activeTab {
			style = TabActiveStyle
		}
		tabs = append(tabs, style.Render(" "+tabName+" "))
	}
	titleWidth := lipgloss.Width(TitleStyle.Render(" MONITOR "))
	m.tabBounds = make([]struct{ start, end int }, len(TabNames))
	tabStart := titleWidth
	for i := range TabNames {
		tabWidth := lipgloss.Width(tabs[i])
		m.tabBounds[i].start, m.tabBounds[i].end = tabStart, tabStart+tabWidth
		tabStart += tabWidth
	}
}

func (m Model) renderProcessesView() string {
	panel := m.renderProcessesPanel()
	return lipgloss.PlaceHorizontal(m.width, lipgloss.Center, panel)
}

func (m Model) renderContextMenu() string {
	if m.contextMenuState == ContextMenuNone {
		return ""
	}
	var menuItems []string
	menuItems = append(menuItems, lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color(Nord8)).Width(28).Align(lipgloss.Center).Render(truncate(m.contextMenuName, 26)))
	menuItems = append(menuItems, lipgloss.NewStyle().Foreground(lipgloss.Color(Nord4)).Width(28).Align(lipgloss.Center).Render(fmt.Sprintf("PID: %d", m.contextMenuPid)))
	menuItems = append(menuItems, strings.Repeat("─", 28))
	menuItems = append(menuItems, lipgloss.NewStyle().Foreground(lipgloss.Color(Nord14)).Render(" [1] Kill (SIGTERM)"))
	menuItems = append(menuItems, lipgloss.NewStyle().Foreground(lipgloss.Color(Nord11)).Render(" [2] Force Kill"))
	menuItems = append(menuItems, lipgloss.NewStyle().Foreground(lipgloss.Color(Nord8)).Render(" [3] Copy PID"))
	menuItems = append(menuItems, lipgloss.NewStyle().Foreground(lipgloss.Color(Nord8)).Render(" [4] Copy Name"))
	menuItems = append(menuItems, strings.Repeat("─", 28))
	menuItems = append(menuItems, lipgloss.NewStyle().Foreground(lipgloss.Color(Nord4)).Render(" [Esc] Close"))
	menuContent := strings.Join(menuItems, "\n")
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color(Nord8)).Background(lipgloss.Color(Nord1)).Padding(1, 1).Width(30).Render(menuContent)
}

func (m Model) renderOverview() string {
	panelWidth := (m.width - 6) / 2
	if panelWidth < 30 {
		panelWidth = 30
	}
	cpuGauge := widgets.NewBarGauge()
	cpuGauge.Value, cpuGauge.Width = m.systemInfo.CPU.UsagePercent, panelWidth-10
	cpuGauge.ColorFunc = func(v float64) string {
		if v >= 80 {
			return Nord11
		} else if v >= 50 {
			return Nord12
		}
		return Nord14
	}
	cpuPanel := PanelStyle.Width(panelWidth).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" CPU "), "", cpuGauge.Render(), fmt.Sprintf("  %.2f GHz  │  %d cores  │  %d threads", m.systemInfo.CPU.FrequencyMHz/1000, m.systemInfo.CPU.CoreCount, m.systemInfo.CPU.ThreadCount)))
	memGauge := widgets.NewBarGauge()
	memGauge.Value, memGauge.Width = m.systemInfo.Memory.UsagePercent, panelWidth-10
	memGauge.ColorFunc = func(v float64) string {
		if v >= 90 {
			return Nord11
		} else if v >= 70 {
			return Nord12
		}
		return Nord14
	}
	memPanel := PanelStyle.Width(panelWidth).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Memory "), "", memGauge.Render(), fmt.Sprintf("  %s / %s  │  %s swap", system.FormatBytes(m.systemInfo.Memory.UsedBytes), system.FormatBytes(m.systemInfo.Memory.TotalBytes), system.FormatBytes(m.systemInfo.Memory.SwapUsed))))
	tempStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(TemperatureColor(m.systemInfo.Temperature.CPUPackage)))
	tempPanel := PanelStyle.Width(panelWidth).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Temperature "), "", tempStyle.Render(fmt.Sprintf("  CPU: %s*  │  GPU: %s*  │  ANE: %s*", m.formatTemp(m.systemInfo.Temperature.CPUPackage), m.formatTemp(m.systemInfo.Temperature.GPU), m.formatTemp(m.systemInfo.Temperature.ANE))), fmt.Sprintf("  Fan: %d RPM (%s) - estimated", m.systemInfo.Temperature.FanRPM, m.systemInfo.Temperature.FanMode)))
	netPanel := PanelStyle.Width(panelWidth).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Network "), "", fmt.Sprintf("  ↓ %s/s    ↑ %s/s", system.FormatBytes(m.systemInfo.Network.BytesRecvPerSec), system.FormatBytes(m.systemInfo.Network.BytesSentPerSec)), fmt.Sprintf("  Total: ↓ %s    ↑ %s", system.FormatBytes(m.systemInfo.Network.BytesRecv), system.FormatBytes(m.systemInfo.Network.BytesSent))))
	topProcs := m.renderTopProcesses(8)
	topRow := lipgloss.JoinHorizontal(lipgloss.Top, cpuPanel, memPanel)
	midRow := lipgloss.JoinHorizontal(lipgloss.Top, tempPanel, netPanel)
	return lipgloss.JoinVertical(lipgloss.Left, topRow, "\n", midRow, "\n", topProcs)
}

func (m Model) renderTopProcesses(n int) string {
	var rows []string
	header := lipgloss.NewStyle().Foreground(lipgloss.Color(Nord8)).Bold(true).Render(fmt.Sprintf("  %-8s %-30s %-8s %-12s %-8s %-15s", "PID", "Name", "CPU%", "Memory", "Threads", "User"))
	rows = append(rows, header)
	rows = append(rows, strings.Repeat("─", m.width-8))
	for _, p := range m.topProcesses(n) {
		row := fmt.Sprintf("  %-8s %-30s %-8s %-12s %-8s %-15s", fmt.Sprintf("%d", p.PID), truncate(p.Name, 30), fmt.Sprintf("%.1f", p.CPUPercent), system.FormatBytes(p.Memory), fmt.Sprintf("%d", p.Threads), truncate(p.User, 15))
		rows = append(rows, lipgloss.NewStyle().Foreground(lipgloss.Color(Nord4)).Render(row))
	}
	if len(rows) == 2 {
		rows = append(rows, lipgloss.NewStyle().Foreground(lipgloss.Color(Nord13)).Render("  No processes match the current settings"))
	}
	return PanelStyle.Width(m.width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Top Processes "), "", strings.Join(rows, "\n")))
}

func (m Model) renderCPUView() string {
	spark := widgets.NewSparkline()
	spark.Data, spark.Width, spark.Height, spark.Color = m.systemInfo.CPU.History, m.width-20, 8, Nord8
	sparklineRender := PanelStyle.Width(m.width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" CPU Usage History "), "", spark.Render()))
	var coreBars []string
	for i, usage := range m.systemInfo.CPU.PerCoreUsage {
		if i >= 8 {
			break
		}
		bar := widgets.NewBarGauge()
		bar.Value, bar.Width, bar.ShowPercent = usage, 25, true
		bar.ColorFunc = func(v float64) string {
			if v >= 80 {
				return Nord11
			} else if v >= 50 {
				return Nord12
			}
			return Nord14
		}
		coreBars = append(coreBars, fmt.Sprintf("  Core %d: %s", i, bar.Render()))
	}
	coresPanel := PanelStyle.Width((m.width - 6) / 2).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Per-Core Usage "), "", strings.Join(coreBars, "\n")))
	statsPanel := PanelStyle.Width((m.width - 6) / 2).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Statistics "), "", fmt.Sprintf("  Usage: %.1f%%", m.systemInfo.CPU.UsagePercent), fmt.Sprintf("  Frequency: %.2f GHz", m.systemInfo.CPU.FrequencyMHz/1000), fmt.Sprintf("  Cores: %d", m.systemInfo.CPU.CoreCount), fmt.Sprintf("  Threads: %d", m.systemInfo.CPU.ThreadCount)))
	return lipgloss.JoinVertical(lipgloss.Left, sparklineRender, "\n", lipgloss.JoinHorizontal(lipgloss.Top, coresPanel, statsPanel))
}

func (m Model) renderMemoryView() string {
	memBar := widgets.NewBarGauge()
	memBar.Value, memBar.Width = m.systemInfo.Memory.UsagePercent, m.width-20
	memBar.ShowPercent = true
	memBar.ColorFunc = func(v float64) string {
		if v >= 90 {
			return Nord11
		} else if v >= 70 {
			return Nord12
		}
		return Nord14
	}
	swapBar := widgets.NewBarGauge()
	if m.systemInfo.Memory.SwapTotal > 0 {
		swapBar.Value = float64(m.systemInfo.Memory.SwapUsed) / float64(m.systemInfo.Memory.SwapTotal) * 100
	}
	swapBar.Width, swapBar.ShowPercent = m.width-20, true
	return lipgloss.JoinVertical(lipgloss.Left,
		PanelStyle.Width(m.width-4).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Physical Memory "), "", memBar.Render(), fmt.Sprintf("  Total: %s    Used: %s    Available: %s", system.FormatBytes(m.systemInfo.Memory.TotalBytes), system.FormatBytes(m.systemInfo.Memory.UsedBytes), system.FormatBytes(m.systemInfo.Memory.AvailableBytes)))),
		"\n",
		PanelStyle.Width(m.width-4).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Swap "), "", swapBar.Render(), fmt.Sprintf("  Total: %s    Used: %s    Free: %s", system.FormatBytes(m.systemInfo.Memory.SwapTotal), system.FormatBytes(m.systemInfo.Memory.SwapUsed), system.FormatBytes(m.systemInfo.Memory.SwapFree)))),
	)
}

func (m Model) renderDiskView() string {
	var partitionLines []string
	for _, partition := range m.systemInfo.Disk.Partitions {
		if partition.MountPoint == "" {
			continue
		}

		bar := widgets.NewBarGauge()
		bar.Value = partition.UsagePercent
		bar.Width = 25
		bar.ShowPercent = true
		bar.ColorFunc = func(v float64) string {
			if v >= 90 {
				return Nord11
			} else if v >= 70 {
				return Nord12
			}
			return Nord14
		}

		line := fmt.Sprintf("  %-15s %s  %s / %s",
			partition.MountPoint,
			bar.Render(),
			system.FormatBytes(partition.UsedBytes),
			system.FormatBytes(partition.TotalBytes),
		)
		partitionLines = append(partitionLines, line)
	}

	spark := widgets.NewSparkline()
	spark.Data, spark.Width, spark.Height, spark.Color = m.systemInfo.Disk.ReadHistory, m.width-20, 6, Nord8

	return lipgloss.JoinVertical(lipgloss.Left,
		PanelStyle.Width(m.width-4).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Disk Usage "), "", strings.Join(partitionLines, "\n"))),
		"\n",
		PanelStyle.Width(m.width-4).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Disk I/O "), "",
			spark.Render(),
			fmt.Sprintf("  Read: %s/s    Write: %s/s", system.FormatBytes(m.systemInfo.Disk.ReadPerSec), system.FormatBytes(m.systemInfo.Disk.WritePerSec)),
		)),
	)
}

func (m Model) renderNetworkView() string {
	panelWidth := (m.width - 6) / 2
	if panelWidth < 40 {
		panelWidth = 40
	}

	downloadBar := widgets.NewBarGauge()
	downloadBar.Value = 0
	downloadBar.Width = panelWidth - 10
	downloadBar.ShowPercent = false
	downloadBar.ColorFunc = func(v float64) string { return Nord8 }
	downloadSpeed := float64(m.systemInfo.Network.BytesRecvPerSec)
	downloadBar.Value = downloadSpeed / (1024 * 1024)
	downloadBar.Max = 100

	uploadBar := widgets.NewBarGauge()
	uploadBar.Value = 0
	uploadBar.Width = panelWidth - 10
	uploadBar.ShowPercent = false
	uploadBar.ColorFunc = func(v float64) string { return Nord14 }
	uploadSpeed := float64(m.systemInfo.Network.BytesSentPerSec)
	uploadBar.Value = uploadSpeed / (1024 * 1024)
	uploadBar.Max = 100

	downloadPanel := PanelStyle.Width(panelWidth).Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			PanelTitleStyle.Render(" Download "),
			"",
			lipgloss.NewStyle().Foreground(lipgloss.Color(Nord8)).Bold(true).Render(" ↓ ")+downloadBar.Render()+" "+system.FormatBytes(m.systemInfo.Network.BytesRecvPerSec)+"/s",
		),
	)

	uploadPanel := PanelStyle.Width(panelWidth).Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			PanelTitleStyle.Render(" Upload "),
			"",
			lipgloss.NewStyle().Foreground(lipgloss.Color(Nord14)).Bold(true).Render(" ↑ ")+uploadBar.Render()+" "+system.FormatBytes(m.systemInfo.Network.BytesSentPerSec)+"/s",
		),
	)

	speedHistory := widgets.NewSparkline()
	speedHistory.Width = m.width - 20
	speedHistory.Height = 8
	speedHistory.ShowAxis = true

	if len(m.systemInfo.Network.DownloadHistory) > 0 || len(m.systemInfo.Network.UploadHistory) > 0 {
		combined := make([]float64, 0, len(m.systemInfo.Network.DownloadHistory)+len(m.systemInfo.Network.UploadHistory))
		maxLen := len(m.systemInfo.Network.DownloadHistory)
		if len(m.systemInfo.Network.UploadHistory) > maxLen {
			maxLen = len(m.systemInfo.Network.UploadHistory)
		}
		for i := 0; i < maxLen; i++ {
			var val float64
			if i < len(m.systemInfo.Network.DownloadHistory) {
				val = m.systemInfo.Network.DownloadHistory[i]
			}
			if i < len(m.systemInfo.Network.UploadHistory) && m.systemInfo.Network.UploadHistory[i] > val {
				val = m.systemInfo.Network.UploadHistory[i]
			}
			combined = append(combined, val/(1024*1024))
		}
		speedHistory.Data = combined
		speedHistory.Color = Nord8
	}
	historyPanel := PanelStyle.Width(m.width - 4).Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			PanelTitleStyle.Render(" Speed History "),
			"",
			speedHistory.Render(),
		),
	)

	totalPanel := PanelStyle.Width(panelWidth).Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			PanelTitleStyle.Render(" Total Transfer "),
			"",
			lipgloss.NewStyle().Foreground(lipgloss.Color(Nord8)).Render(" ↓ "+system.FormatBytes(m.systemInfo.Network.BytesRecv)),
			lipgloss.NewStyle().Foreground(lipgloss.Color(Nord14)).Render(" ↑ "+system.FormatBytes(m.systemInfo.Network.BytesSent)),
		),
	)

	packetsPanel := PanelStyle.Width(panelWidth).Render(
		lipgloss.JoinVertical(
			lipgloss.Left,
			PanelTitleStyle.Render(" Packets "),
			"",
			lipgloss.NewStyle().Foreground(lipgloss.Color(Nord8)).Render(" ↓ "+formatNumber(m.systemInfo.Network.PacketsRecv)),
			lipgloss.NewStyle().Foreground(lipgloss.Color(Nord14)).Render(" ↑ "+formatNumber(m.systemInfo.Network.PacketsSent)),
		),
	)

	topRow := lipgloss.JoinHorizontal(lipgloss.Top, downloadPanel, uploadPanel)
	midRow := lipgloss.JoinHorizontal(lipgloss.Top, totalPanel, packetsPanel)

	return lipgloss.JoinVertical(lipgloss.Left, historyPanel, "\n", topRow, "\n", midRow)
}

func formatNumber(n uint64) string {
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

func (m Model) renderTemperatureView() string {
	tempSpark := widgets.NewSparkline()
	tempSpark.Data, tempSpark.Width, tempSpark.Height, tempSpark.Color = m.systemInfo.Temperature.History, m.width-20, 6, Nord12
	sensors := []string{
		fmt.Sprintf("  CPU Package:  %s  %s (estimated)", m.formatTemp(m.systemInfo.Temperature.CPUPackage), getTempStatus(m.systemInfo.Temperature.CPUPackage)),
		fmt.Sprintf("  CPU Cores:    %s  %s (estimated)", m.formatTemp(m.systemInfo.Temperature.CPUCores), getTempStatus(m.systemInfo.Temperature.CPUCores)),
		fmt.Sprintf("  GPU:          %s  %s (estimated)", m.formatTemp(m.systemInfo.Temperature.GPU), getTempStatus(m.systemInfo.Temperature.GPU)),
		fmt.Sprintf("  ANE:          %s  %s (estimated)", m.formatTemp(m.systemInfo.Temperature.ANE), getTempStatus(m.systemInfo.Temperature.ANE)),
		fmt.Sprintf("  Battery:      %s  %s (estimated)", m.formatTemp(m.systemInfo.Temperature.Battery), getTempStatus(m.systemInfo.Temperature.Battery)),
	}
	sensorsPanel := PanelStyle.Width((m.width - 6) / 2).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Sensor Readings "), "", strings.Join(sensors, "\n")))
	fanPanel := PanelStyle.Width((m.width - 6) / 2).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Fan Telemetry "), "", fmt.Sprintf("  Speed: %d RPM (estimated)", m.systemInfo.Temperature.FanRPM), fmt.Sprintf("  Mode: %s", m.systemInfo.Temperature.FanMode), "  Max: 6000 RPM"))
	historyPanel := PanelStyle.Width(m.width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, PanelTitleStyle.Render(" Temperature History "), "", tempSpark.Render()))
	return lipgloss.JoinVertical(lipgloss.Left, historyPanel, "\n", lipgloss.JoinHorizontal(lipgloss.Top, sensorsPanel, fanPanel))
}

func (m Model) renderSettingsView() string {
	var lines []string

	cursor := " "
	if m.selectedSetting == 0 {
		cursor = "▶"
	}
	intervalVal := "1s"
	if m.settings != nil {
		switch m.settings.UpdateInterval {
		case 500 * time.Millisecond:
			intervalVal = "500ms"
		case time.Second:
			intervalVal = "1s"
		case 2 * time.Second:
			intervalVal = "2s"
		case 5 * time.Second:
			intervalVal = "5s"
		}
	}
	lines = append(lines, fmt.Sprintf("  %s [1] Update Interval:  [%s]", cursor, intervalVal))

	cursor = " "
	if m.selectedSetting == 1 {
		cursor = "▶"
	}
	tempUnit := "°C"
	if m.settings != nil && m.settings.TemperatureUnit == "F" {
		tempUnit = "°F"
	}
	lines = append(lines, fmt.Sprintf("  %s [2] Temperature Unit: [%s]", cursor, tempUnit))

	cursor = " "
	if m.selectedSetting == 2 {
		cursor = "▶"
	}
	showSys := "OFF"
	showSysColor := Nord3
	if m.settings != nil && m.settings.ShowSystemProcesses {
		showSys = "ON "
		showSysColor = Nord14
	}
	lines = append(lines, fmt.Sprintf("  %s [3] Show System Procs:[%s]", cursor, lipgloss.NewStyle().Foreground(lipgloss.Color(showSysColor)).Render(showSys)))

	cursor = " "
	if m.selectedSetting == 3 {
		cursor = "▶"
	}
	maxProcs := "50"
	if m.settings != nil {
		switch m.settings.MaxProcesses {
		case 20:
			maxProcs = "20"
		case 50:
			maxProcs = "50"
		case 100:
			maxProcs = "100"
		case 200:
			maxProcs = "200"
		}
	}
	lines = append(lines, fmt.Sprintf("  %s [4] Max Processes:    [%s]", cursor, maxProcs))

	cursor = " "
	if m.selectedSetting == 4 {
		cursor = "▶"
	}
	cpuAlert := "OFF"
	if m.settings != nil && m.settings.CPUAlertThreshold > 0 {
		cpuAlert = fmt.Sprintf("%.0f%%", m.settings.CPUAlertThreshold)
	}
	lines = append(lines, fmt.Sprintf("  %s [5] CPU Alert:        [%s]", cursor, cpuAlert))

	cursor = " "
	if m.selectedSetting == 5 {
		cursor = "▶"
	}
	memAlert := "OFF"
	if m.settings != nil && m.settings.MemoryAlertThreshold > 0 {
		memAlert = fmt.Sprintf("%.0f%%", m.settings.MemoryAlertThreshold)
	}
	lines = append(lines, fmt.Sprintf("  %s [6] Memory Alert:     [%s]", cursor, memAlert))

	lines = append(lines, "")
	lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color(Nord6)).Render("  ↑/↓ or j/k: Navigate  ←/→ or Enter: Change value"))
	lines = append(lines, lipgloss.NewStyle().Foreground(lipgloss.Color(Nord6)).Render("  r: Reset to defaults"))

	return PanelStyle.Width(m.width - 4).Render(strings.Join(lines, "\n"))
}

func (m Model) renderKillConfirmation() string {
	var lines []string
	killType := "TERMINATE (SIGTERM)"
	if m.forceKill {
		killType = "FORCE KILL (SIGKILL)"
	}
	lines = append(lines, fmt.Sprintf("⚠️  %s CONFIRMATION", killType))
	lines = append(lines, "")
	lines = append(lines, fmt.Sprintf("  You are about to terminate %d process(es):", len(m.killConfirmation.Processes)))
	lines = append(lines, "")
	for _, p := range m.killConfirmation.Processes {
		safety := "✓ OK"
		if p.IsProtected {
			safety = "🛑 CRITICAL"
		} else if p.IsSystem {
			safety = "⚠️  CAUTION"
		}
		lines = append(lines, fmt.Sprintf("    PID %d: %s (%s)", p.PID, p.Name, safety))
	}
	if len(m.killConfirmation.SafetyWarnings) > 0 {
		lines = append(lines, "")
		lines = append(lines, "  Warnings:")
		for _, w := range m.killConfirmation.SafetyWarnings {
			lines = append(lines, fmt.Sprintf("    ⚠️  %s", w))
		}
	}
	lines = append(lines, "")
	if m.forceKill {
		lines = append(lines, "  ⚠️  FORCE KILL will not allow the process to clean up!")
		lines = append(lines, "  Press 'y' to FORCE KILL, 'n' to cancel")
	} else {
		lines = append(lines, "  Press 'y' to confirm, 'n' to cancel")
	}
	dialog := lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color(Nord11)).Padding(1, 2).Render(strings.Join(lines, "\n"))
	return dialog
}

func (m Model) renderResetConfirmation() string {
	var lines []string
	lines = append(lines, "⚠️  RESET SETTINGS CONFIRMATION")
	lines = append(lines, "")
	lines = append(lines, "  You are about to reset all settings to their")
	lines = append(lines, "  default values.")
	lines = append(lines, "")
	lines = append(lines, "  This will affect:")
	lines = append(lines, "    • Update Interval")
	lines = append(lines, "    • Temperature Unit")
	lines = append(lines, "    • Show System Processes")
	lines = append(lines, "    • Max Processes Displayed")
	lines = append(lines, "")
	lines = append(lines, "  Press 'y' to confirm reset, 'n' to cancel")
	dialog := lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color(Nord12)).Padding(1, 2).Render(strings.Join(lines, "\n"))
	return dialog
}

func (m Model) renderStatusBar() string {
	displayedProcesses, totalProcesses := m.processCounts()
	processCount := fmt.Sprintf("Processes: %d", displayedProcesses)
	if displayedProcesses != totalProcesses {
		processCount = fmt.Sprintf("Processes: %d/%d", displayedProcesses, totalProcesses)
	}
	sortIndicator := ""
	if m.activeTab == TabProcesses {
		sortOrder := "↓"
		if m.sortAsc {
			sortOrder = "↑"
		}
		sortIndicator = fmt.Sprintf(" │ Sort: %s %s", m.sortLabel(), sortOrder)
	}
	selCount := ""
	if len(m.selectedPids) > 0 {
		selCount = fmt.Sprintf(" │ Selected: %d", len(m.selectedPids))
	}
	searchIndicator := ""
	if m.activeTab == TabProcesses && m.processSearchMode {
		searchIndicator = fmt.Sprintf(" │ Search: %d/%d", displayedProcesses, totalProcesses)
	}
	message := ""
	if m.statusMessage != "" {
		messageStyle := lipgloss.NewStyle().Foreground(lipgloss.Color(Nord14)).Bold(true)
		if m.statusMessageIsError {
			messageStyle = lipgloss.NewStyle().Foreground(lipgloss.Color(Nord11)).Bold(true)
		}
		message = messageStyle.Render(m.statusMessage) + " │ "
	}
	lastUpdate := m.lastUpdate
	if !m.systemInfo.LastUpdate.IsZero() {
		lastUpdate = m.systemInfo.LastUpdate
	}
	status := fmt.Sprintf(" %s%s  │  CPU: %.1f%%  │  Memory: %.1f%%  │  Update: %s  │  Mouse: %s%s%s%s", message, processCount, m.systemInfo.CPU.UsagePercent, m.systemInfo.Memory.UsagePercent, lastUpdate.Format("15:04:05"), map[bool]string{true: "ON", false: "OFF"}[m.mouseEnabled], sortIndicator, selCount, searchIndicator)
	return lipgloss.NewStyle().Foreground(lipgloss.Color(Nord6)).Background(lipgloss.Color(Nord2)).Bold(true).Width(m.width).Render(status)
}

func (m Model) filteredProcesses() []system.ProcessInfo {
	showSystemProcesses := config.DefaultSettings.ShowSystemProcesses
	if m.settings != nil {
		showSystemProcesses = m.settings.ShowSystemProcesses
	}

	procs := make([]system.ProcessInfo, 0, len(m.systemInfo.Processes))
	for _, proc := range m.systemInfo.Processes {
		if !showSystemProcesses && proc.IsSystem {
			continue
		}
		// Apply search filter
		if m.processSearchQuery != "" {
			if !strings.Contains(strings.ToLower(proc.Name), strings.ToLower(m.processSearchQuery)) {
				continue
			}
		}
		procs = append(procs, proc)
	}

	return procs
}

func (m Model) buildProcessViewData() processViewData {
	filtered := m.filteredProcesses()
	data := processViewData{
		totalFiltered: len(filtered),
	}

	displayed := append([]system.ProcessInfo(nil), filtered...)
	m.sortProcessSlice(displayed)

	maxProcesses := config.DefaultSettings.MaxProcesses
	if m.settings != nil && m.settings.MaxProcesses > 0 {
		maxProcesses = m.settings.MaxProcesses
	}
	if maxProcesses > 0 && len(displayed) > maxProcesses {
		displayed = displayed[:maxProcesses]
	}
	data.displayed = displayed

	topByCPU := append([]system.ProcessInfo(nil), filtered...)
	sort.SliceStable(topByCPU, func(i, j int) bool {
		cmp := compareFloat64(topByCPU[i].CPUPercent, topByCPU[j].CPUPercent)
		if cmp == 0 {
			return topByCPU[i].PID < topByCPU[j].PID
		}
		return cmp > 0
	})
	data.topByCPU = topByCPU

	return data
}

func (m Model) currentProcessViewData() processViewData {
	if m.processData.ready {
		return m.processData
	}

	data := m.buildProcessViewData()
	data.ready = true
	return data
}

func (m Model) displayProcesses() []system.ProcessInfo {
	return m.currentProcessViewData().displayed
}

func (m Model) topProcesses(limit int) []system.ProcessInfo {
	procs := m.currentProcessViewData().topByCPU
	if limit > 0 && len(procs) > limit {
		procs = procs[:limit]
	}
	return procs
}

func (m Model) processCounts() (displayed int, total int) {
	data := m.currentProcessViewData()
	return len(data.displayed), data.totalFiltered
}

func (m Model) sortLabel() string {
	switch m.sortBy {
	case "pid":
		return "PID"
	case "name":
		return "Name"
	case "memory":
		return "Memory"
	case "threads":
		return "Threads"
	case "user":
		return "User"
	case "io":
		return "I/O"
	default:
		return "CPU"
	}
}

func (m Model) selectedRowPID() (int32, bool) {
	row := m.processTable.SelectedRow()
	if len(row) == 0 {
		return 0, false
	}

	var pid int32
	if _, err := fmt.Sscanf(row[0], "%d", &pid); err != nil {
		return 0, false
	}

	return pid, true
}

func (m *Model) sortProcessSlice(procs []system.ProcessInfo) {
	sort.SliceStable(procs, func(i, j int) bool {
		cmp := m.compareProcesses(procs[i], procs[j])
		if m.sortAsc {
			return cmp < 0
		}
		return cmp > 0
	})
}

func (m *Model) compareProcesses(left, right system.ProcessInfo) int {
	var cmp int
	switch m.sortBy {
	case "pid":
		cmp = compareInt32(left.PID, right.PID)
	case "name":
		cmp = strings.Compare(strings.ToLower(left.Name), strings.ToLower(right.Name))
	case "memory":
		cmp = compareUint64(left.Memory, right.Memory)
	case "threads":
		cmp = compareInt32(left.Threads, right.Threads)
	case "user":
		cmp = strings.Compare(strings.ToLower(left.User), strings.ToLower(right.User))
	case "io":
		cmp = compareUint64(left.IOReadBytes+left.IOWriteBytes, right.IOReadBytes+right.IOWriteBytes)
	default:
		cmp = compareFloat64(left.CPUPercent, right.CPUPercent)
	}
	if cmp == 0 {
		return compareInt32(left.PID, right.PID)
	}
	return cmp
}

func (m *Model) getSelectedPidsSlice() []int32 {
	pids := make([]int32, 0, len(m.selectedPids))
	for pid := range m.selectedPids {
		pids = append(pids, pid)
	}
	return pids
}

func (m *Model) selectAllProcesses() {
	m.selectedPids = make(map[int32]bool)
	displayed := m.displayProcesses()
	for _, p := range displayed {
		m.selectedPids[p.PID] = true
	}
	if len(displayed) > 0 {
		m.lastSelectedPid = displayed[0].PID
		return
	}
	m.lastSelectedPid = 0
}

func (m *Model) selectRange(startPid, endPid int32) {
	displayed := m.displayProcesses()
	startIdx, endIdx := -1, -1
	for i, p := range displayed {
		if p.PID == startPid {
			startIdx = i
		}
		if p.PID == endPid {
			endIdx = i
		}
	}
	if startIdx == -1 || endIdx == -1 {
		return
	}
	if startIdx > endIdx {
		startIdx, endIdx = endIdx, startIdx
	}
	for i := startIdx; i <= endIdx; i++ {
		m.selectedPids[displayed[i].PID] = true
	}
	m.lastSelectedPid = endPid
}

func truncate(s string, maxLen int) string {
	if maxLen <= 3 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}

func getTempStatus(temp float64) string {
	style := StatusNormalStyle
	if temp >= 85 {
		style = StatusCriticalStyle
	} else if temp >= 70 {
		style = StatusWarningStyle
	}
	return style.Render(getTempLabel(temp))
}

func getTempLabel(temp float64) string {
	if temp >= 85 {
		return "Critical"
	} else if temp >= 70 {
		return "High"
	} else if temp >= 60 {
		return "Warm"
	}
	return "Normal"
}

func (m *Model) formatTemp(celsius float64) string {
	if m.settings != nil && m.settings.TemperatureUnit == "F" {
		f := celsius*9/5 + 32
		return fmt.Sprintf("%.0f°F", f)
	}
	return fmt.Sprintf("%.0f°C", celsius)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func compareFloat64(left, right float64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareUint64(left, right uint64) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func compareInt32(left, right int32) int {
	switch {
	case left < right:
		return -1
	case left > right:
		return 1
	default:
		return 0
	}
}

func (m *Model) setStatusMessage(message string, isError bool) {
	m.statusMessage = message
	m.statusMessageIsError = isError
	m.statusMessageTime = time.Now()
}

func (m *Model) setKillStatus(killed, skipped int, killErrors []string) {
	parts := make([]string, 0, 3)
	if killed > 0 {
		action := "terminated"
		if m.forceKill {
			action = "force-killed"
		}
		parts = append(parts, fmt.Sprintf("%d process(es) %s", killed, action))
	}
	if skipped > 0 {
		parts = append(parts, fmt.Sprintf("%d protected skipped", skipped))
	}
	if len(killErrors) > 0 {
		parts = append(parts, fmt.Sprintf("%d failed: %s", len(killErrors), strings.Join(killErrors, "; ")))
	}
	if len(parts) == 0 {
		m.setStatusMessage("No process actions were applied", true)
		return
	}
	m.setStatusMessage(strings.Join(parts, " | "), len(killErrors) > 0 || skipped > 0 && killed == 0)
}

// exportProcesses exports the current process list to a JSON file
func (m *Model) exportProcesses() {
	if len(m.processData.displayed) == 0 {
		m.setStatusMessage("No processes to export", true)
		return
	}

	// Generate filename with timestamp
	timestamp := time.Now().Format("20060102-150405")
	filename := fmt.Sprintf("monitor-processes-%s.json", timestamp)

	// Get home directory
	home, err := os.UserHomeDir()
	if err != nil {
		m.setStatusMessage(fmt.Sprintf("Export failed: %v", err), true)
		return
	}

	// Create exports directory
	exportDir := filepath.Join(home, ".config", "monitor", "exports")
	if err := os.MkdirAll(exportDir, 0755); err != nil {
		m.setStatusMessage(fmt.Sprintf("Export failed: %v", err), true)
		return
	}

	// Build JSON export
	type processExport struct {
		PID           int32   `json:"pid"`
		Name          string  `json:"name"`
		CPUPercent    float64 `json:"cpu_percent"`
		Memory        uint64  `json:"memory_bytes"`
		MemoryPercent float64 `json:"memory_percent"`
		Threads       int32   `json:"threads"`
		User          string  `json:"user"`
		IsSystem      bool    `json:"is_system"`
		ExportedAt    string  `json:"exported_at"`
	}

	var exports []processExport
	now := time.Now().Format(time.RFC3339)
	for _, proc := range m.processData.displayed {
		exports = append(exports, processExport{
			PID:           proc.PID,
			Name:          proc.Name,
			CPUPercent:    proc.CPUPercent,
			Memory:        proc.Memory,
			MemoryPercent: proc.MemoryPercent,
			Threads:       proc.Threads,
			User:          proc.User,
			IsSystem:      proc.IsSystem,
			ExportedAt:    now,
		})
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(exports, "", "  ")
	if err != nil {
		m.setStatusMessage(fmt.Sprintf("Export failed: %v", err), true)
		return
	}

	// Write file
	fullPath := filepath.Join(exportDir, filename)
	if err := os.WriteFile(fullPath, data, 0644); err != nil {
		m.setStatusMessage(fmt.Sprintf("Export failed: %v", err), true)
		return
	}

	m.setStatusMessage(fmt.Sprintf("Exported %d processes to %s", len(exports), fullPath), false)
}

// placeOverlay renders overlay on top of background at position (x, y) using ANSI-aware string manipulation.
func placeOverlay(bg, overlay string, x, y int) string {
	bgLines := strings.Split(bg, "\n")
	olLines := strings.Split(overlay, "\n")
	if y < 0 {
		y = 0
	}
	for i, olLine := range olLines {
		lineIdx := y + i
		if lineIdx >= len(bgLines) {
			break
		}
		if lineIdx < 0 {
			continue
		}
		bgLine := bgLines[lineIdx]
		olWidth := ansi.StringWidth(olLine)
		// ANSI-safe: cut the left portion of background up to x
		left := ansi.Cut(bgLine, 0, x)
		// Pad left if background is shorter than x
		leftWidth := ansi.StringWidth(left)
		if leftWidth < x {
			left += strings.Repeat(" ", x-leftWidth)
		}
		// ANSI-safe: cut the right portion of background after the overlay
		right := ansi.Cut(bgLine, x+olWidth, math.MaxInt)
		bgLines[lineIdx] = left + olLine + right
	}
	return strings.Join(bgLines, "\n")
}
