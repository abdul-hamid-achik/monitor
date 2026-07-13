package studio

import (
	"fmt"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
)

func processSafetyFixture(t *testing.T, count, limit int) Model {
	t.Helper()
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	t.Cleanup(m.cancel)
	m.ready = true
	m.width, m.height = 120, 40
	m.view = viewProcesses
	m.settings = config.Default()
	m.settings.MaxProcesses = limit
	m.last.LastUpdate = time.Now()
	m.last.Processes = make([]collector.ProcessInfo, count)
	for i := range m.last.Processes {
		m.last.Processes[i] = collector.ProcessInfo{
			PID:        int32(10_000 + i),
			Name:       fmt.Sprintf("worker-%04d", i),
			CPUPercent: float64(count - i),
			User:       "tester",
		}
	}
	m.updateProcessTable()
	return m
}

func TestProcessViewCapsRowsAndSelectAllToShownProcesses(t *testing.T) {
	m := processSafetyFixture(t, 1000, 50)
	data := m.currentProcessView()
	if got := len(data.matched); got != 1000 {
		t.Fatalf("matched processes = %d, want 1000", got)
	}
	if got := len(data.shown); got != 50 {
		t.Fatalf("shown processes = %d, want 50", got)
	}
	if got := len(m.processTable.Rows()); got != 50 {
		t.Fatalf("table rows = %d, want 50", got)
	}

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: 'a', Mod: tea.ModCtrl}))
	m = updated.(Model)
	if got := len(m.selectedPids); got != 50 {
		t.Fatalf("Ctrl+A selected %d processes, want only the 50 shown rows", got)
	}
	for _, p := range data.shown {
		if !m.selectedPids[p.PID] {
			t.Errorf("shown PID %d was not selected", p.PID)
		}
	}
	for _, p := range data.matched[50:] {
		if m.selectedPids[p.PID] {
			t.Fatalf("hidden PID %d must not be selected", p.PID)
		}
	}

	content := m.renderProcesses()
	if !strings.Contains(content, "50 shown") || !strings.Contains(content, "1000 matched") {
		t.Fatalf("capped process title must report shown and matched counts; got:\n%s", content)
	}
}

func TestProcessDestructiveKeysRequireUppercase(t *testing.T) {
	m := processSafetyFixture(t, 3, 50)
	m.processTable.SetCursor(1)

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "k", Code: 'k'}))
	m = updated.(Model)
	if got := m.processTable.Cursor(); got != 0 {
		t.Fatalf("lowercase k should navigate up; cursor=%d, want 0", got)
	}
	if m.showKillConfirm {
		t.Fatal("lowercase k must not open a kill confirmation")
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "j", Code: 'j'}))
	m = updated.(Model)
	if got := m.processTable.Cursor(); got != 1 {
		t.Fatalf("lowercase j should navigate down; cursor=%d, want 1", got)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
	m = updated.(Model)
	if m.showKillConfirm {
		t.Fatal("lowercase x must not open a force-kill confirmation")
	}

	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "K", Code: 'K'}))
	m = updated.(Model)
	if !m.showKillConfirm || m.forceKill {
		t.Fatalf("uppercase K should request normal termination; confirm=%v force=%v", m.showKillConfirm, m.forceKill)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "n", Code: 'n'}))
	m = updated.(Model)
	if m.showKillConfirm {
		t.Fatal("n should cancel the termination confirmation")
	}

	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "X", Code: 'X'}))
	m = updated.(Model)
	if !m.showKillConfirm || !m.forceKill {
		t.Fatalf("uppercase X should request force kill; confirm=%v force=%v", m.showKillConfirm, m.forceKill)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m = updated.(Model)
	if m.showKillConfirm || m.forceKill {
		t.Fatal("Esc should cancel and clear the force-kill confirmation")
	}
}

func TestProcessTableWideToNarrowResizeRebuildsRowSchema(t *testing.T) {
	m := processSafetyFixture(t, 12, 50)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	m = updated.(Model)
	if got := len(m.processTable.Columns()); got != 7 {
		t.Fatalf("wide table columns = %d, want 7", got)
	}
	m.processTable.SetCursor(4)
	wantPID, ok := m.selectedRowPID()
	if !ok {
		t.Fatal("wide table should have a selected process row")
	}
	// Match the live path that exposed the panic: resize while a populated
	// Processes table sits behind its PID-pinned detail panel.
	m.processDetailVisible = true
	m.processDetailPID = wantPID

	updated, _ = m.Update(tea.WindowSizeMsg{Width: 40, Height: 18})
	m = updated.(Model)
	if got := len(m.processTable.Columns()); got != 3 {
		t.Fatalf("narrow table columns = %d, want 3", got)
	}
	if got := len(m.processTable.Rows()); got != 12 {
		t.Fatalf("narrow table rows = %d, want 12", got)
	}
	for i, row := range m.processTable.Rows() {
		if got := len(row); got != 3 {
			t.Fatalf("narrow row %d has %d cells, want 3", i, got)
		}
	}
	if gotPID, selected := m.selectedRowPID(); !selected || gotPID != wantPID {
		t.Fatalf("resize selected PID = (%d, %v), want (%d, true)", gotPID, selected, wantPID)
	}
	// Rendering after the resize exercises Bubbles' viewport with the rebuilt
	// rows and columns, including the detail-overlay path.
	_ = m.View()
}

func TestKillConfirmationIsBoundedAndKeepsPromptVisible(t *testing.T) {
	processes := make([]collector.ProcessInfo, 1000)
	for i := range processes {
		processes[i] = collector.ProcessInfo{
			PID:  int32(20_000 + i),
			Name: fmt.Sprintf("very-long-worker-process-name-%04d", i),
		}
		if i%4 == 0 {
			processes[i].IsSystem = true
		}
	}

	for _, size := range []struct {
		width, height int
	}{{80, 24}, {40, 18}} {
		for _, force := range []bool{false, true} {
			name := fmt.Sprintf("%dx%d/force=%v", size.width, size.height, force)
			t.Run(name, func(t *testing.T) {
				m := NewModelWithOptions(Options{DisableTemperatureSource: true})
				t.Cleanup(m.cancel)
				m.width, m.height = size.width, size.height
				m.ready = true
				m.view = viewProcesses
				m.last.LastUpdate = time.Now()
				m.showKillConfirm = true
				m.forceKill = force
				m.killConf.Processes = processes

				dialog := m.renderKillConfirmation()
				if got := lipgloss.Width(dialog); got > size.width {
					t.Fatalf("dialog width=%d exceeds terminal width=%d", got, size.width)
				}
				if got, max := lipgloss.Height(dialog), m.processContentHeight(); got > max {
					t.Fatalf("dialog height=%d exceeds content height=%d", got, max)
				}
				for _, want := range []string{"750 eligible", "250 blocked", "[ELIGIBLE]", "[BLOCKED ]", "more not shown", "y confirm", "n/Esc cancel"} {
					if !strings.Contains(dialog, want) {
						t.Errorf("dialog missing %q; got:\n%s", want, dialog)
					}
				}
				for _, unwanted := range []string{"⚠", "🛓", "✓"} {
					if strings.Contains(dialog, unwanted) {
						t.Errorf("dialog contains variable-width emoji marker %q", unwanted)
					}
				}
				if content := m.renderProcesses(); !strings.Contains(content, "y confirm") || !strings.Contains(content, "n/Esc cancel") {
					t.Fatalf("placed confirmation clipped its prompt; got:\n%s", content)
				}
			})
		}
	}
}
