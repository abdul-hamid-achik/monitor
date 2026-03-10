package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/monitor/monitor/internal/config"
	"github.com/monitor/monitor/internal/system"
)

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"hello", 10, "hello"},
		{"hello world", 8, "hello..."},
		{"日本語テスト", 5, "日本..."},
		{"test", 3, "test"}, // maxLen <= 3 returns as-is
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, expected %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}

func TestGetSelectedPidsSlice(t *testing.T) {
	m := &Model{
		selectedPids: map[int32]bool{
			1: true,
			2: true,
			3: true,
		},
	}

	pids := m.getSelectedPidsSlice()

	if len(pids) != 3 {
		t.Errorf("Expected 3 PIDs, got %d", len(pids))
	}

	// Check that all PIDs are present
	pidMap := make(map[int32]bool)
	for _, pid := range pids {
		pidMap[pid] = true
	}

	for i := int32(1); i <= 3; i++ {
		if !pidMap[i] {
			t.Errorf("Expected PID %d to be in slice", i)
		}
	}
}

func TestSelectRange(t *testing.T) {
	m := &Model{
		selectedPids: make(map[int32]bool),
		systemInfo: system.SystemInfo{
			Processes: []system.ProcessInfo{
				{PID: 100, Name: "proc1"},
				{PID: 200, Name: "proc2"},
				{PID: 300, Name: "proc3"},
				{PID: 400, Name: "proc4"},
				{PID: 500, Name: "proc5"},
			},
		},
	}

	// Select range from 200 to 400
	m.selectRange(200, 400)

	// Should select PIDs 200, 300, 400
	if !m.selectedPids[200] {
		t.Error("Expected PID 200 to be selected")
	}
	if !m.selectedPids[300] {
		t.Error("Expected PID 300 to be selected")
	}
	if !m.selectedPids[400] {
		t.Error("Expected PID 400 to be selected")
	}
	if m.selectedPids[100] {
		t.Error("PID 100 should not be selected")
	}
	if m.selectedPids[500] {
		t.Error("PID 500 should not be selected")
	}
}

func TestSelectAllProcesses(t *testing.T) {
	m := &Model{
		selectedPids: make(map[int32]bool),
		systemInfo: system.SystemInfo{
			Processes: []system.ProcessInfo{
				{PID: 1, Name: "proc1"},
				{PID: 2, Name: "proc2"},
				{PID: 3, Name: "proc3"},
			},
		},
	}

	m.selectAllProcesses()

	if len(m.selectedPids) != 3 {
		t.Errorf("Expected 3 selected processes, got %d", len(m.selectedPids))
	}

	for i := int32(1); i <= 3; i++ {
		if !m.selectedPids[i] {
			t.Errorf("Expected PID %d to be selected", i)
		}
	}
}

func TestUpdateProcessTableAppliesProcessSettings(t *testing.T) {
	m := &Model{
		selectedPids: make(map[int32]bool),
		settings: &config.Settings{
			ShowSystemProcesses: false,
			MaxProcesses:        1,
		},
		sortBy:  "cpu",
		sortAsc: false,
		systemInfo: system.SystemInfo{
			Processes: []system.ProcessInfo{
				{PID: 1, Name: "kernel_task", CPUPercent: 95, IsSystem: true},
				{PID: 2, Name: "user-high", CPUPercent: 42},
				{PID: 3, Name: "user-low", CPUPercent: 10},
			},
		},
	}

	m.setupProcessTable()
	m.updateProcessTable()

	rows := m.processTable.Rows()
	if len(rows) != 1 {
		t.Fatalf("Expected 1 visible row, got %d", len(rows))
	}
	if rows[0][0] != "2" {
		t.Fatalf("Expected highest-CPU non-system process to remain visible, got PID %s", rows[0][0])
	}
}

func TestHandleKeyPressSettingsNumberShortcutsStayInSettings(t *testing.T) {
	m := Model{
		activeTab: TabSettings,
		settings:  config.Default(),
	}

	model, _ := m.handleKeyPress(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'4'}})
	updated := model.(Model)

	if updated.activeTab != TabSettings {
		t.Fatalf("Expected to remain on settings tab, got %v", updated.activeTab)
	}
	if updated.selectedSetting != 3 {
		t.Fatalf("Expected setting index 3 to be selected, got %d", updated.selectedSetting)
	}
}

func TestCalculateTabBoundsAreContiguous(t *testing.T) {
	m := Model{
		width:     140,
		activeTab: TabOverview,
	}

	m.calculateTabBounds()

	for i := 1; i < len(m.tabBounds); i++ {
		if m.tabBounds[i].start != m.tabBounds[i-1].end {
			t.Fatalf("Expected tab %d to start at %d, got %d", i, m.tabBounds[i-1].end, m.tabBounds[i].start)
		}
	}
}

func TestHandleMouseIgnoresRowClickOutsideTableBounds(t *testing.T) {
	m := newMouseTestModel()

	layout := m.processLayout()
	rowY := layout.tableY + 2

	model, _ := m.handleMouse(tea.MouseMsg{
		X:      0,
		Y:      rowY,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	updated := model.(Model)

	if len(updated.selectedPids) != 0 {
		t.Fatalf("Expected no row selection from gutter click, got %v", updated.selectedPids)
	}
}

func TestHandleMouseUsesCenteredHeaderHitbox(t *testing.T) {
	m := newMouseTestModel()

	layout := m.processLayout()
	nameColumnX := layout.tableX + processTableSpecs[0].width + 3

	model, _ := m.handleMouse(tea.MouseMsg{
		X:      nameColumnX,
		Y:      layout.tableY,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	updated := model.(Model)

	if updated.sortBy != "name" {
		t.Fatalf("Expected sortBy to switch to name, got %s", updated.sortBy)
	}
}

func TestHandleMouseContextMenuKillAction(t *testing.T) {
	m := newMouseTestModel()
	m.contextMenuState = ContextMenuProcess
	m.contextMenuPid = 42
	m.contextMenuName = "worker"
	m.contextMenuX = 10
	m.contextMenuY = 4

	menuLines := strings.Split(m.renderContextMenu(), "\n")
	killLine := -1
	for i, line := range menuLines {
		if strings.Contains(line, "[1] Kill") {
			killLine = i
			break
		}
	}
	if killLine == -1 {
		t.Fatal("Expected kill line in context menu")
	}

	model, _ := m.handleMouse(tea.MouseMsg{
		X:      m.contextMenuX + 2,
		Y:      m.contextMenuY + killLine,
		Button: tea.MouseButtonLeft,
		Action: tea.MouseActionPress,
	})
	updated := model.(Model)

	if !updated.showKillConfirm {
		t.Fatal("Expected kill confirmation to open from menu click")
	}
	if updated.contextMenuState != ContextMenuNone {
		t.Fatal("Expected context menu to close after kill click")
	}
	if !updated.selectedPids[42] {
		t.Fatal("Expected context menu PID to be selected")
	}
}

func newMouseTestModel() Model {
	m := Model{
		width:        120,
		height:       30,
		activeTab:    TabProcesses,
		mouseEnabled: true,
		settings:     config.Default(),
		selectedPids: make(map[int32]bool),
		sortBy:       "cpu",
		systemInfo: system.SystemInfo{
			Processes: []system.ProcessInfo{
				{PID: 42, Name: "worker", CPUPercent: 50, User: "abdul"},
				{PID: 24, Name: "helper", CPUPercent: 20, User: "abdul"},
			},
		},
	}
	m.setupProcessTable()
	m.processTable.SetWidth(max(60, m.width-6))
	m.processTable.SetHeight(20)
	m.calculateTabBounds()
	m.updateProcessTable()
	return m
}
