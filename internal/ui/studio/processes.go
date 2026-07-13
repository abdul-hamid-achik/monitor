package studio

import (
	"fmt"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/table"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
)

type processColumnSpec struct {
	title  string
	width  int
	sortBy string
}

var processTableSpecs = []processColumnSpec{
	{"PID", 8, "pid"},
	{"Name", 22, "name"},
	{"CPU%", 7, "cpu"},
	{"Memory", 10, "memory"},
	{"I/O", 10, "io"},
	{"Threads", 7, "threads"},
	{"User", 12, "user"},
}

type processKeyMap struct {
	Kill      key.Binding
	ForceKill key.Binding
	SortCPU   key.Binding
	SortMem   key.Binding
	SelectAll key.Binding
	ClearSel  key.Binding
	Search    key.Binding
}

type killBatchResultMsg struct {
	results  []kill.Result
	failures []string
	spared   []string
}

var procKeys = processKeyMap{
	Kill:      key.NewBinding(key.WithKeys("k")),
	ForceKill: key.NewBinding(key.WithKeys("x")),
	SortCPU:   key.NewBinding(key.WithKeys("c")),
	SortMem:   key.NewBinding(key.WithKeys("m")),
	SelectAll: key.NewBinding(key.WithKeys("ctrl+a")),
	ClearSel:  key.NewBinding(key.WithKeys("ctrl+d")),
	Search:    key.NewBinding(key.WithKeys("/")),
}

func (m *Model) setupProcessTable() {
	tbl := table.New(table.WithHeight(20))
	m.processTable = &tbl
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).Foreground(lipgloss.Color("#88C0D0"))
	s.Selected = s.Selected.Foreground(lipgloss.Color("#2E3440")).Background(lipgloss.Color("#88C0D0")).Bold(true)
	m.processTable.SetStyles(s)
	m.configureProcessTable()
}

// processSpecsForWidth progressively removes secondary columns and assigns
// remaining room to process names. The table is useful down to about 32 cells
// instead of retaining a 76-cell fixed layout that gets clipped.
func processSpecsForWidth(width int) []processColumnSpec {
	if width <= 0 {
		width = 120
	}
	var specs []processColumnSpec
	switch {
	case width >= 100:
		specs = append(specs, processTableSpecs...)
	case width >= 78:
		specs = []processColumnSpec{
			{"PID", 7, "pid"}, {"Name", 22, "name"}, {"CPU%", 7, "cpu"},
			{"Memory", 10, "memory"}, {"Threads", 7, "threads"},
		}
	case width >= 58:
		specs = []processColumnSpec{
			{"PID", 7, "pid"}, {"Name", 20, "name"}, {"CPU%", 7, "cpu"}, {"Memory", 10, "memory"},
		}
	default:
		specs = []processColumnSpec{
			{"PID", 7, "pid"}, {"Name", 14, "name"}, {"CPU%", 7, "cpu"},
		}
	}
	available := width - 10
	fixed := 0
	nameIdx := -1
	for i, spec := range specs {
		if spec.sortBy == "name" {
			nameIdx = i
			continue
		}
		fixed += spec.width
	}
	if nameIdx >= 0 {
		nameWidth := available - fixed
		if nameWidth < 10 {
			nameWidth = 10
		}
		if nameWidth > 32 {
			nameWidth = 32
		}
		specs[nameIdx].width = nameWidth
	}
	return specs
}

func (m *Model) configureProcessTable() {
	if m.processTable == nil {
		return
	}
	specs := processSpecsForWidth(m.width)
	cols := make([]table.Column, 0, len(specs))
	for _, spec := range specs {
		cols = append(cols, table.Column{Title: spec.title, Width: spec.width})
	}
	m.processTable.SetColumns(cols)
}

func (m *Model) updateProcessTable() {
	if m.processTable == nil {
		return
	}
	procs := m.filteredProcesses()
	sort.SliceStable(procs, func(i, j int) bool {
		cmp := m.compareProcesses(procs[i], procs[j])
		if m.sortAsc {
			return cmp < 0
		}
		return cmp > 0
	})
	maxProcs := config.DefaultSettings.MaxProcesses
	if m.settings != nil && m.settings.MaxProcesses > 0 {
		maxProcs = m.settings.MaxProcesses
	}
	if maxProcs > 0 && len(procs) > maxProcs {
		procs = procs[:maxProcs]
	}
	specs := processSpecsForWidth(m.width)
	rows := make([]table.Row, 0, len(procs))
	for _, p := range procs {
		row := make(table.Row, 0, len(specs))
		for _, spec := range specs {
			row = append(row, m.processCell(p, spec))
		}
		rows = append(rows, row)
	}
	// Always set rows — an empty slice is valid and clears stale rows so
	// a filter that matches nothing renders the empty-state message
	// instead of the previously displayed processes.
	m.processTable.SetRows(rows)
}

func (m Model) processCell(p collector.ProcessInfo, spec processColumnSpec) string {
	switch spec.sortBy {
	case "pid":
		return fmt.Sprintf("%d", p.PID)
	case "name":
		prefix := "  "
		if m.selectedPids[p.PID] {
			prefix = "▸ "
		}
		return prefix + truncateStr(p.Name, spec.width-2)
	case "cpu":
		return fmt.Sprintf("%.1f", p.CPUPercent)
	case "memory":
		return collector.FormatBytes(p.Memory)
	case "io":
		return collector.FormatBytes(p.IOReadBytes + p.IOWriteBytes)
	case "threads":
		return fmt.Sprintf("%d", p.Threads)
	case "user":
		return truncateStr(p.User, spec.width)
	default:
		return ""
	}
}

func (m Model) filteredProcesses() []collector.ProcessInfo {
	showSys := config.DefaultSettings.ShowSystemProcesses
	if m.settings != nil {
		showSys = m.settings.ShowSystemProcesses
	}
	out := make([]collector.ProcessInfo, 0, len(m.last.Processes))
	for _, p := range m.last.Processes {
		if !showSys && p.IsSystem {
			continue
		}
		if m.searchQuery != "" {
			haystack := fmt.Sprintf("%s %d %s", p.Name, p.PID, p.User)
			if !strings.Contains(strings.ToLower(haystack), strings.ToLower(m.searchQuery)) {
				continue
			}
		}
		out = append(out, p)
	}
	return out
}

func (m *Model) compareProcesses(a, b collector.ProcessInfo) int {
	switch m.sortBy {
	case "pid":
		return cmpInt32(a.PID, b.PID)
	case "name":
		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	case "memory":
		return cmpUint64(a.Memory, b.Memory)
	case "threads":
		return cmpInt32(a.Threads, b.Threads)
	case "user":
		return strings.Compare(strings.ToLower(a.User), strings.ToLower(b.User))
	case "io":
		return cmpUint64(a.IOReadBytes+a.IOWriteBytes, b.IOReadBytes+b.IOWriteBytes)
	default:
		return cmpFloat64(a.CPUPercent, b.CPUPercent)
	}
}

func cmpFloat64(a, b float64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
func cmpUint64(a, b uint64) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
}
func cmpInt32(a, b int32) int {
	if a < b {
		return -1
	}
	if a > b {
		return 1
	}
	return 0
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

func (m *Model) getSelectedPidsSlice() []int32 {
	pids := make([]int32, 0, len(m.selectedPids))
	for pid := range m.selectedPids {
		pids = append(pids, pid)
	}
	return pids
}

func (m Model) handleProcessKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.processTable == nil {
		return m, nil
	}
	if m.processSearch {
		switch msg.Keystroke() {
		case "enter":
			m.processSearch = false
			m.searchBefore = m.searchQuery
			return m, nil
		case "ctrl+u":
			m.searchQuery = ""
			m.updateProcessTable()
			return m, nil
		case "backspace":
			runes := []rune(m.searchQuery)
			if len(runes) > 0 {
				m.searchQuery = string(runes[:len(runes)-1])
				m.updateProcessTable()
			}
			return m, nil
		case "esc":
			m.processSearch = false
			m.searchQuery = m.searchBefore
			m.updateProcessTable()
			return m, nil
		}
		if msg.Text != "" {
			m.searchQuery += msg.Text
			m.updateProcessTable()
		}
		return m, nil
	}
	switch {
	case msg.Keystroke() == "enter":
		pid, ok := m.selectedRowPID()
		if !ok {
			return m, nil
		}
		m.processDetailPID = pid
		m.processDetailVisible = true
		return m, nil
	case key.Matches(msg, procKeys.SelectAll):
		for _, p := range m.filteredProcesses() {
			m.selectedPids[p.PID] = true
		}
		m.updateProcessTable()
		return m, nil
	case key.Matches(msg, procKeys.ClearSel):
		m.selectedPids = make(map[int32]bool)
		m.updateProcessTable()
		return m, nil
	case msg.Keystroke() == " ":
		pid, ok := m.selectedRowPID()
		if !ok {
			return m, nil
		}
		if m.selectedPids[pid] {
			delete(m.selectedPids, pid)
		} else {
			m.selectedPids[pid] = true
		}
		m.updateProcessTable()
		return m, nil
	case key.Matches(msg, procKeys.Kill):
		if len(m.selectedPids) == 0 {
			if pid, ok := m.selectedRowPID(); ok {
				m.selectedPids[pid] = true
			}
		}
		if len(m.selectedPids) > 0 {
			m.forceKill = false
			m.killConf = kill.CheckSafety(m.getSelectedPidsSlice())
			m.showKillConfirm = true
		}
		return m, nil
	case key.Matches(msg, procKeys.ForceKill):
		if len(m.selectedPids) == 0 {
			if pid, ok := m.selectedRowPID(); ok {
				m.selectedPids[pid] = true
			}
		}
		if len(m.selectedPids) > 0 {
			m.forceKill = true
			m.killConf = kill.CheckSafety(m.getSelectedPidsSlice())
			m.showKillConfirm = true
		}
		return m, nil
	case key.Matches(msg, procKeys.SortCPU):
		if m.sortBy == "cpu" {
			m.sortAsc = !m.sortAsc
		} else {
			m.sortBy = "cpu"
			m.sortAsc = false
		}
		m.updateProcessTable()
		return m, nil
	case key.Matches(msg, procKeys.SortMem):
		if m.sortBy == "memory" {
			m.sortAsc = !m.sortAsc
		} else {
			m.sortBy = "memory"
			m.sortAsc = false
		}
		m.updateProcessTable()
		return m, nil
	case key.Matches(msg, procKeys.Search):
		m.searchBefore = m.searchQuery
		m.processSearch = true
		m.updateProcessTable()
		return m, nil
	}
	updated, cmd := m.processTable.Update(msg)
	m.processTable = &updated
	return m, cmd
}

// pidRefused reports whether pid is protected OR system per the shared safety
// classification (kill.CheckSafety), and so must not be terminated even after
// the confirm dialog. This matches the CLI and MCP gate (both refuse
// HasProtected || HasSystem) so the TUI can't be the surface that drifts.
func (m Model) pidRefused(pid int32) bool {
	for _, p := range m.killConf.Processes {
		if p.PID == pid {
			return p.IsProtected || p.IsSystem
		}
	}
	return false
}

// procName returns the process name for pid from the kill confirmation, or a
// "pid N" fallback.
func (m Model) procName(pid int32) string {
	for _, p := range m.killConf.Processes {
		if p.PID == pid && p.Name != "" {
			return p.Name
		}
	}
	return fmt.Sprintf("pid %d", pid)
}

func (m Model) handleKillConfirmKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Keystroke() {
	case "esc", "n":
		m.showKillConfirm = false
		m.selectedPids = make(map[int32]bool)
		m.forceKill = false
		m.updateProcessTable()
		return m, nil
	case "y":
		var spared []string
		var attempted []int32
		for pid := range m.selectedPids {
			if m.pidRefused(pid) {
				spared = append(spared, m.procName(pid))
				continue
			}
			attempted = append(attempted, pid)
		}
		sort.Slice(attempted, func(i, j int) bool { return attempted[i] < attempted[j] })
		sort.Strings(spared)
		// Report both the immediate safety decision and the asynchronous
		// verification phase. KillVerified can wait up to two seconds, so it
		// runs in a command rather than blocking Update and freezing the TUI.
		if len(spared) > 0 {
			m.killNotice = fmt.Sprintf("Spared %d protected/system process(es): %s", len(spared), strings.Join(spared, ", "))
			m.killNoticeTicks = 4
		}
		if len(attempted) > 0 {
			m.killNotice = fmt.Sprintf("Verifying termination of %d process(es)…", len(attempted))
			if len(spared) > 0 {
				m.killNotice += fmt.Sprintf("; spared %d", len(spared))
			}
			m.killNoticeTicks = 30
		}
		force := m.forceKill
		m.showKillConfirm = false
		m.selectedPids = make(map[int32]bool)
		m.forceKill = false
		m.updateProcessTable()
		if len(attempted) > 0 {
			return m, verifyKillsCmd(attempted, force, spared)
		}
		return m, nil
	}
	return m, nil
}

func verifyKillsCmd(pids []int32, force bool, spared []string) tea.Cmd {
	return func() tea.Msg {
		msg := killBatchResultMsg{spared: append([]string(nil), spared...)}
		for _, pid := range pids {
			result, err := kill.KillVerified(pid, force)
			if err != nil {
				msg.failures = append(msg.failures, fmt.Sprintf("pid %d: %v", pid, err))
				continue
			}
			msg.results = append(msg.results, result)
		}
		return msg
	}
}

func formatKillBatchResult(msg killBatchResultMsg) string {
	terminated, running, unknown := 0, 0, 0
	for _, result := range msg.results {
		switch result.Outcome {
		case kill.OutcomeTerminated:
			terminated++
		case kill.OutcomeStillRunning:
			running++
		default:
			unknown++
		}
	}
	parts := make([]string, 0, 5)
	if terminated > 0 {
		parts = append(parts, fmt.Sprintf("terminated %d", terminated))
	}
	if running > 0 {
		parts = append(parts, fmt.Sprintf("still running %d (use x to force)", running))
	}
	if unknown > 0 {
		parts = append(parts, fmt.Sprintf("unverified %d", unknown))
	}
	if len(msg.failures) > 0 {
		parts = append(parts, fmt.Sprintf("failed %d: %s", len(msg.failures), strings.Join(msg.failures, "; ")))
	}
	if len(msg.spared) > 0 {
		parts = append(parts, fmt.Sprintf("spared %d protected/system", len(msg.spared)))
	}
	if len(parts) == 0 {
		return "No processes were terminated"
	}
	return strings.Join(parts, " · ")
}

func (m Model) renderProcesses() string {
	if m.processDetailVisible {
		return m.renderProcessDetail()
	}
	if m.last.LastUpdate.IsZero() {
		return m.panelStyle.Width(m.width - 4).Render(
			m.titleStyle.Render(" Processes ") +
				"\n\n  Waiting for process data…\n  The first snapshot normally arrives within one update interval.")
	}
	var content []string
	direction := "↓"
	if m.sortAsc {
		direction = "↑"
	}
	visible := len(m.filteredProcesses())
	title := fmt.Sprintf(" Processes · %d shown · sort %s%s ", visible, m.sortBy, direction)
	if len(m.selectedPids) > 0 {
		title += fmt.Sprintf("· %d selected ", len(m.selectedPids))
	}
	content = append(content, m.titleStyle.Render(title))
	if m.processSearch {
		content = append(content, lipgloss.NewStyle().Foreground(lipgloss.Color("#88C0D0")).Render(" Filter: ")+lipgloss.NewStyle().Foreground(lipgloss.Color("#D8DEE9")).Render(m.searchQuery)+"_  ·  enter apply  esc cancel  ctrl+u clear")
	} else if m.searchQuery != "" {
		content = append(content, fmt.Sprintf(" Filter: %q · %d match(es) · / edit", m.searchQuery, visible))
	} else {
		content = append(content, " ↑/↓ navigate  ·  enter details  ·  space select  ·  / filter  ·  c/m sort  ·  k/x terminate")
	}
	content = append(content, "")
	if m.processTable != nil && len(m.processTable.Rows()) > 0 {
		content = append(content, m.processTable.View())
	} else if m.last.ProcessesState.State != "" && m.last.ProcessesState.State != collector.MetricObserved {
		reason := strings.TrimSpace(m.last.ProcessesState.Reason)
		if reason == "" {
			reason = string(m.last.ProcessesState.State)
		}
		content = append(content,
			"  Process telemetry is unavailable · "+reason,
			"  Press r to retry the snapshot; monitor doctor can check collector health.",
		)
	} else if m.searchQuery != "" {
		content = append(content,
			"  No processes match the current filter.",
			"  Press / to edit or clear the filter.",
		)
	} else {
		content = append(content,
			"  No processes were returned by the latest snapshot.",
			"  Press r to refresh, or enable system processes in Settings.",
		)
	}
	panel := m.panelStyle.Width(m.width - 4).Render(lipgloss.JoinVertical(lipgloss.Left, content...))
	body := lipgloss.PlaceHorizontal(m.width, lipgloss.Center, panel)
	if m.showKillConfirm {
		dialog := m.renderKillConfirmation()
		body = lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, dialog)
	}
	return body
}

func (m Model) renderKillConfirmation() string {
	var lines []string
	killType := "TERMINATE (SIGTERM)"
	if m.forceKill {
		killType = "FORCE KILL (SIGKILL)"
	}
	lines = append(lines, fmt.Sprintf("⚠️  %s CONFIRMATION", killType), "")
	lines = append(lines, fmt.Sprintf("  You are about to terminate %d process(es):", len(m.killConf.Processes)), "")
	for _, p := range m.killConf.Processes {
		safety := "✓ OK"
		if p.IsProtected {
			safety = "🛓 CRITICAL"
		} else if p.IsSystem {
			safety = "⚠️  CAUTION"
		}
		lines = append(lines, fmt.Sprintf("    PID %d: %s (%s)", p.PID, p.Name, safety))
	}
	lines = append(lines, "")
	if m.forceKill {
		lines = append(lines, "  ⚠️  FORCE KILL will not allow the process to clean up!")
		lines = append(lines, "  Press 'y' to FORCE KILL, 'n' to cancel")
	} else {
		lines = append(lines, "  Press 'y' to confirm, 'n' to cancel")
	}
	return lipgloss.NewStyle().Border(lipgloss.ThickBorder()).BorderForeground(lipgloss.Color("#BF616A")).Padding(1, 2).Render(strings.Join(lines, "\n"))
}

func truncateStr(s string, maxLen int) string {
	if maxLen <= 3 {
		return s
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen-3]) + "..."
}
