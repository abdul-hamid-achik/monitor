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
	// Destructive actions deliberately require Shift so the table's standard
	// j/k navigation can never be mistaken for a termination request.
	Kill:      key.NewBinding(key.WithKeys("K")),
	ForceKill: key.NewBinding(key.WithKeys("X")),
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
	s.Header = s.Header.Bold(true).Foreground(m.theme.Accent)
	s.Selected = s.Selected.Foreground(m.theme.SelectedFG).Background(m.theme.Accent).Bold(true)
	m.processTable.SetStyles(s)
	m.processTable.Focus()
	m.configureProcessTable()
}

// processViewData is the single source of truth for process-table membership.
// matched contains every process accepted by the system-process and text
// filters; shown is the sorted, MaxProcesses-bounded subset that the user can
// actually see and act on with select-all.
type processViewData struct {
	matched []collector.ProcessInfo
	shown   []collector.ProcessInfo
}

func (m Model) currentProcessView() processViewData {
	matched := m.filteredProcesses()
	sort.SliceStable(matched, func(i, j int) bool {
		cmp := m.compareProcesses(matched[i], matched[j])
		if m.sortAsc {
			return cmp < 0
		}
		return cmp > 0
	})

	shown := matched
	maxProcs := config.DefaultSettings.MaxProcesses
	if m.settings != nil && m.settings.MaxProcesses > 0 {
		maxProcs = m.settings.MaxProcesses
	}
	if maxProcs > 0 && len(shown) > maxProcs {
		shown = shown[:maxProcs]
	}
	return processViewData{matched: matched, shown: shown}
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
	selectedPID, preserveSelection := m.selectedRowPID()
	specs := processSpecsForWidth(m.width)
	cols := make([]table.Column, 0, len(specs))
	for _, spec := range specs {
		cols = append(cols, table.Column{Title: spec.title, Width: spec.width})
	}
	// Bubbles renders its viewport immediately in SetColumns. Old rows may have
	// more cells than the new responsive schema, and renderRow indexes columns
	// by row-cell index. Clear the old schema first, then rebuild matching rows.
	m.processTable.SetRows(nil)
	m.processTable.SetColumns(cols)
	m.updateProcessTable()

	rows := m.processTable.Rows()
	if len(rows) == 0 {
		return
	}
	cursor := 0
	if preserveSelection {
		for i, row := range rows {
			if len(row) == 0 {
				continue
			}
			var pid int32
			if _, err := fmt.Sscanf(row[0], "%d", &pid); err == nil && pid == selectedPID {
				cursor = i
				break
			}
		}
	}
	m.processTable.SetCursor(cursor)
}

func (m *Model) updateProcessTable() {
	if m.processTable == nil {
		return
	}
	procs := m.currentProcessView().shown
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
	// Bubbles moves the cursor to -1 when rows become empty but does not move it
	// back when data later returns. Normalize it so Enter and navigation work
	// after startup, a cleared filter, or a responsive schema rebuild.
	if len(rows) > 0 && m.processTable.Cursor() < 0 {
		m.processTable.SetCursor(0)
	}
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
		for _, p := range m.currentProcessView().shown {
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
		parts = append(parts, fmt.Sprintf("still running %d (use X to force)", running))
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
	processes := m.currentProcessView()
	shown, matched := len(processes.shown), len(processes.matched)
	title := fmt.Sprintf(" Processes · %d shown", shown)
	if matched > shown {
		title += fmt.Sprintf(" · %d matched", matched)
	}
	title += fmt.Sprintf(" · sort %s%s ", m.sortBy, direction)
	if len(m.selectedPids) > 0 {
		title += fmt.Sprintf("· %d selected ", len(m.selectedPids))
	}
	content = append(content, m.titleStyle.Render(title))
	if m.processSearch {
		content = append(content, lipgloss.NewStyle().Foreground(m.theme.Accent).Render(" Filter: ")+lipgloss.NewStyle().Foreground(m.theme.Text).Render(m.searchQuery)+"_  ·  enter apply  esc cancel  ctrl+u clear")
	} else if m.searchQuery != "" {
		content = append(content, fmt.Sprintf(" Filter: %q · %d match(es) · / edit", m.searchQuery, matched))
	} else {
		content = append(content, " ↑/↓ or j/k navigate  ·  enter details  ·  space select  ·  / filter  ·  c/m sort  ·  K/X terminate")
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
		body = lipgloss.Place(m.width, m.processContentHeight(), lipgloss.Center, lipgloss.Center, dialog)
	}
	return body
}

func (m Model) processContentHeight() int {
	headerHeight := lipgloss.Height(m.renderHeader())
	if headerHeight < 1 {
		headerHeight = 1
	}
	height := m.height - headerHeight - 1 // footer
	if height < 1 {
		height = 1
	}
	return height
}

func (m Model) renderKillConfirmation() string {
	style := lipgloss.NewStyle().
		Border(lipgloss.ThickBorder()).
		BorderForeground(m.theme.Critical).
		Padding(1, 2)
	dialogWidth := m.width - 4
	if dialogWidth > 76 {
		dialogWidth = 76
	}
	if dialogWidth < style.GetHorizontalFrameSize()+1 {
		dialogWidth = style.GetHorizontalFrameSize() + 1
	}
	textWidth := dialogWidth - style.GetHorizontalFrameSize()

	eligible, blocked := 0, 0
	for _, p := range m.killConf.Processes {
		if p.IsProtected || p.IsSystem {
			blocked++
		} else {
			eligible++
		}
	}

	title := " TERMINATE (SIGTERM) CONFIRMATION "
	if m.forceKill {
		title = " FORCE KILL (SIGKILL) CONFIRMATION "
	}
	if textWidth < 38 {
		if m.forceKill {
			title = " FORCE KILL CONFIRMATION "
		} else {
			title = " TERMINATE CONFIRMATION "
		}
	}

	summary := fmt.Sprintf("  %d requested · %d eligible · %d blocked", len(m.killConf.Processes), eligible, blocked)
	if textWidth < 48 {
		summary = fmt.Sprintf("%d eligible · %d blocked", eligible, blocked)
	}
	prompt := "  y confirm terminate · n/Esc cancel"
	if m.forceKill {
		prompt = "  y confirm force kill · n/Esc cancel"
	}
	if textWidth < 42 {
		prompt = "  y confirm · n/Esc cancel"
	}

	prefix := []string{truncateStr(title, textWidth), "", truncateStr(summary, textWidth), ""}
	suffix := []string{"", truncateStr(prompt, textWidth)}
	if m.forceKill {
		warning := "  SIGKILL does not allow process cleanup"
		if textWidth < 42 {
			warning = "  SIGKILL: no cleanup"
		}
		suffix = []string{"", truncateStr(warning, textWidth), truncateStr(prompt, textWidth)}
	}

	// Border + padding consume the style's vertical frame; every remaining
	// content line is explicitly budgeted so the confirmation prompt cannot be
	// clipped below either the compact or full shell.
	textBudget := m.processContentHeight() - style.GetVerticalFrameSize()
	if textBudget < 1 {
		textBudget = 1
	}
	lines := make([]string, 0, textBudget)
	minimum := len(prefix) + len(suffix)
	if textBudget < minimum {
		compact := []string{truncateStr(summary, textWidth)}
		if len(m.killConf.Processes) > 0 {
			compact = append(compact, truncateStr(fmt.Sprintf("... %d process row(s) hidden", len(m.killConf.Processes)), textWidth))
		}
		compact = append(compact, truncateStr(prompt, textWidth))
		if len(compact) > textBudget {
			compact = compact[len(compact)-textBudget:]
		}
		lines = append(lines, compact...)
	} else {
		lines = append(lines, prefix...)
		rowBudget := textBudget - minimum
		visibleRows := len(m.killConf.Processes)
		if visibleRows > rowBudget {
			// Reserve one row to make truncation explicit.
			visibleRows = rowBudget - 1
			if visibleRows < 0 {
				visibleRows = 0
			}
		}
		for _, p := range m.killConf.Processes[:visibleRows] {
			lines = append(lines, killConfirmationProcessLine(p, textWidth))
		}
		if omitted := len(m.killConf.Processes) - visibleRows; omitted > 0 {
			lines = append(lines, truncateStr(fmt.Sprintf("  ... %d more not shown", omitted), textWidth))
		}
		lines = append(lines, suffix...)
	}

	return style.Width(dialogWidth).Render(strings.Join(lines, "\n"))
}

func killConfirmationProcessLine(p collector.ProcessInfo, width int) string {
	label := "ELIGIBLE"
	if p.IsProtected || p.IsSystem {
		label = "BLOCKED "
	}
	prefix := fmt.Sprintf("  [%-8s] PID %6d ", label, p.PID)
	return prefix + truncateStr(p.Name, width-len([]rune(prefix)))
}

func truncateStr(s string, maxLen int) string {
	runes := []rune(s)
	if maxLen <= 0 {
		return ""
	}
	if len(runes) <= maxLen {
		return s
	}
	if maxLen <= 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
