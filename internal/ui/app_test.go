package ui

import (
	"testing"

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
