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
	cols := make([]table.Column, 0, len(processTableSpecs))
	for _, spec := range processTableSpecs {
		cols = append(cols, table.Column{Title: spec.title, Width: spec.width})
	}
	tbl := table.New(table.WithColumns(cols), table.WithHeight(20))
	m.processTable = &tbl
	s := table.DefaultStyles()
	s.Header = s.Header.Bold(true).Foreground(lipgloss.Color("#88C0D0"))
	s.Selected = s.Selected.Foreground(lipgloss.Color("#2E3440")).Background(lipgloss.Color("#88C0D0")).Bold(true)
	m.processTable.SetStyles(s)
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
	rows := make([]table.Row, 0, len(procs))
	for _, p := range procs {
		name := truncateStr(p.Name, 20)
		if m.selectedPids[p.PID] {
			name = "▸ " + name
		} else {
			name = "  " + name
		}
		rows = append(rows, table.Row{
			fmt.Sprintf("%d", p.PID), name,
			fmt.Sprintf("%.1f", p.CPUPercent),
			collector.FormatBytes(p.Memory),
			collector.FormatBytes(p.IOReadBytes + p.IOWriteBytes),
			fmt.Sprintf("%d", p.Threads),
			truncateStr(p.User, 12),
		})
	}
	// Always set rows — an empty slice is valid and clears stale rows so
	// a filter that matches nothing renders the empty-state message
	// instead of the previously displayed processes.
	m.processTable.SetRows(rows)
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
		if m.searchQuery != "" && !strings.Contains(strings.ToLower(p.Name), strings.ToLower(m.searchQuery)) {
			continue
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
		switch msg.Code {
		case tea.KeyBackspace:
			if len(m.searchQuery) > 0 {
				m.searchQuery = m.searchQuery[:len(m.searchQuery)-1]
				m.updateProcessTable()
			}
			return m, nil
		case tea.KeyEscape:
			m.processSearch = false
			m.searchQuery = ""
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
		m.processSearch = !m.processSearch
		if !m.processSearch {
			m.searchQuery = ""
		}
		m.updateProcessTable()
		return m, nil
	}
	updated, cmd := m.processTable.Update(msg)
	m.processTable = &updated
	return m, cmd
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
		for pid := range m.selectedPids {
			protected := false
			for _, p := range m.killConf.Processes {
				if p.PID == pid && p.IsProtected {
					protected = true
					break
				}
			}
			if !protected {
				_ = kill.Kill(pid, m.forceKill)
			}
		}
		m.showKillConfirm = false
		m.selectedPids = make(map[int32]bool)
		m.forceKill = false
		m.updateProcessTable()
		return m, nil
	}
	return m, nil
}

func (m Model) renderProcesses() string {
	if m.last.LastUpdate.IsZero() {
		return m.panelStyle.Width(m.width - 4).Render(m.titleStyle.Render(" Processes ") + "\n\n  Waiting for process data…")
	}
	var content []string
	title := " Processes - k:kill x:force-kill "
	if len(m.selectedPids) > 0 {
		title = fmt.Sprintf(" Processes - %d selected │ k:kill x:force-kill ", len(m.selectedPids))
	}
	content = append(content, m.titleStyle.Render(title))
	if m.processSearch {
		content = append(content, lipgloss.NewStyle().Foreground(lipgloss.Color("#88C0D0")).Render(" Search: ")+lipgloss.NewStyle().Foreground(lipgloss.Color("#D8DEE9")).Render(m.searchQuery)+"_")
	}
	content = append(content, "")
	if m.processTable != nil && len(m.processTable.Rows()) > 0 {
		content = append(content, m.processTable.View())
	} else {
		content = append(content, "  No processes match the current filter")
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
