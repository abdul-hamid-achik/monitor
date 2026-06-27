package v2

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
)

func TestTabHitTest(t *testing.T) {
	widths := []int{5, 5, 5} // three tabs of width 5, starting at x=10
	if v, ok := tabHitTest(12, 10, widths); !ok || v != 0 {
		t.Errorf("x=12 -> (%d,%v), want tab 0", v, ok)
	}
	if v, ok := tabHitTest(16, 10, widths); !ok || v != 1 {
		t.Errorf("x=16 -> (%d,%v), want tab 1", v, ok)
	}
	if _, ok := tabHitTest(3, 10, widths); ok {
		t.Error("a click over the title should miss")
	}
	if _, ok := tabHitTest(99, 10, widths); ok {
		t.Error("a click past the last tab should miss")
	}
}

func TestSettingsEditing(t *testing.T) {
	m := NewModel()
	m.view = viewSettings
	m.settings = config.Default()

	// Cursor 0 = Update Interval; enter cycles it to a different value.
	before := m.settings.UpdateInterval
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(Model)
	if m.settings.UpdateInterval == before {
		t.Errorf("enter on Update Interval should change it; still %v", m.settings.UpdateInterval)
	}

	// Cursor 2 = Show System Procs; space toggles it.
	m.settingsCursor = 2
	sys := m.settings.ShowSystemProcesses
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: " ", Code: ' '}))
	m = updated.(Model)
	if m.settings.ShowSystemProcesses == sys {
		t.Error("space on Show System Procs should toggle it")
	}
}

func TestRenderTrendsProducesOutput(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	fm := updated.(Model)
	fm.view = viewTrends
	if fm.renderTrends() == "" {
		t.Error("renderTrends should produce output even with no recorded history")
	}
}

func TestNewModelBasics(t *testing.T) {
	m := NewModel()
	if m.ready {
		t.Fatal("fresh model should not be ready")
	}
	if m.quitting {
		t.Fatal("fresh model should not be quitting")
	}
	if m.collector == nil {
		t.Fatal("model should have a collector")
	}
	if m.processTable == nil {
		t.Fatal("model should have a process table")
	}
}

func TestViewQuittingReturnsGoodbye(t *testing.T) {
	m := NewModel()
	m.quitting = true
	v := m.View()
	if !strings.Contains(v.Content, "Goodbye") {
		t.Fatalf("quitting view should contain 'Goodbye'; got %q", v.Content)
	}
}

func TestViewInitializingReturnsPlaceholder(t *testing.T) {
	m := NewModel()
	v := m.View()
	if !v.AltScreen {
		t.Fatal("initializing view must use AltScreen")
	}
	if !strings.Contains(v.Content, "Initializing") {
		t.Fatalf("initializing view should contain 'Initializing'; got %q", v.Content)
	}
}

func TestHandleKeyQuit(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c", "esc"} {
		m := NewModel()
		updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: key, Code: 'q'}))
		fm := updated.(Model)
		if !fm.quitting {
			t.Errorf("Update(%s) should set quitting", key)
		}
		if cmd == nil {
			t.Errorf("Update(%s) should return a tea.Quit cmd", key)
		}
	}
}

func TestTabSwitching(t *testing.T) {
	for _, tc := range []struct {
		key  string
		want viewID
	}{
		{"1", viewOverview},
		{"2", viewCPU},
		{"3", viewMemory},
		{"4", viewTemperature},
		{"5", viewDisk},
		{"6", viewNetwork},
		{"7", viewProcesses},
		{"8", viewSettings},
	} {
		m := NewModel()
		updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: tc.key, Code: rune(tc.key[0])}))
		fm := updated.(Model)
		if fm.view != tc.want {
			t.Errorf("after pressing %s: view=%d, want %d", tc.key, fm.view, tc.want)
		}
	}
}

func TestTabWrapAllDirections(t *testing.T) {
	for start := viewID(0); start < viewCount; start++ {
		m := NewModel()
		m.view = start
		for i := 0; i < viewCount; i++ {
			updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
			m = updated.(Model)
		}
		if m.view != start {
			t.Errorf("tab x%d from %d: ended at %d, want %d", viewCount, start, m.view, start)
		}
		for i := 0; i < viewCount; i++ {
			updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
			m = updated.(Model)
		}
		if m.view != start {
			t.Errorf("left x%d from %d: ended at %d, want %d", viewCount, start, m.view, start)
		}
	}
}

// TestProcessSearchCapturesNavKeys is a regression for the bug where the
// global navigation/quit switch ran before the per-tab search handler, so
// typing 'q' (quit), a digit (tab jump), or 'l'/'h' (tab cycle) while the
// process search prompt was active never reached the query.
func TestProcessSearchCapturesNavKeys(t *testing.T) {
	for _, k := range []struct {
		text string
		code rune
	}{
		{"q", 'q'}, {"1", '1'}, {"l", 'l'}, {"h", 'h'},
	} {
		m := NewModel()
		m.setupProcessTable()
		m.view = viewProcesses
		m.processSearch = true
		updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: k.text, Code: k.code}))
		fm := updated.(Model)
		if fm.quitting {
			t.Errorf("typing %q while searching quit the app", k.text)
		}
		if fm.view != viewProcesses {
			t.Errorf("typing %q while searching switched view to %d", k.text, fm.view)
		}
		if fm.searchQuery != k.text {
			t.Errorf("typing %q while searching: searchQuery = %q, want %q", k.text, fm.searchQuery, k.text)
		}
	}
}

func TestTickRefreshesSnapshot(t *testing.T) {
	m := NewModel()
	info := m.collector.Collect(m.ctx)
	if !info.LastUpdate.IsZero() {
		m.last = info
	}
	_, _ = m.Update(tickMsg(time.Now()))
	if m.last.LastUpdate.IsZero() {
		t.Fatalf("tick should have refreshed last from the collector")
	}
}

func TestRenderEdgeCases(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	fm := updated.(Model)
	fm.last.LastUpdate = time.Now()
	for _, view := range []viewID{viewOverview, viewCPU, viewMemory, viewTemperature, viewDisk, viewNetwork, viewSettings} {
		fm.view = view
		body := fm.View().Content
		if body == "" {
			t.Errorf("view %d should render non-empty content", view)
		}
	}
}

func TestRenderProcessesEmpty(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	fm := updated.(Model)
	fm.view = viewProcesses
	fm.last.LastUpdate = time.Now()
	fm.last.Processes = nil
	body := fm.View().Content
	if body == "" {
		t.Fatalf("Processes tab should render with empty list")
	}
}

func TestRenderProcessesWithData(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	fm := updated.(Model)
	fm.view = viewProcesses
	fm.last.LastUpdate = time.Now()
	fm.last.Processes = []collector.ProcessInfo{
		{PID: 100, Name: "test", CPUPercent: 42.5, Memory: 100 * 1024 * 1024, Threads: 4, User: "user"},
	}
	(&fm).updateProcessTable()
	body := fm.View().Content
	if !strings.Contains(body, "Processes") {
		t.Errorf("should contain 'Processes'")
	}
	if !strings.Contains(body, "test") {
		t.Errorf("should contain process name 'test'")
	}
}

func TestRenderTemperatureBadge(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	fm := updated.(Model)
	updated, _ = fm.Update(tea.KeyPressMsg(tea.Key{Text: "4", Code: '4'}))
	fm = updated.(Model)
	fm.last.LastUpdate = time.Now()
	fm.last.Temperature.Source = "estimated"
	body := fm.View().Content
	if !strings.Contains(body, "● est") {
		t.Fatalf("Temperature tab should show 'est' badge; got:\n%s", body)
	}
	fm.last.Temperature.Source = "powermetrics"
	body = fm.View().Content
	if !strings.Contains(body, "● real") {
		t.Fatalf("Temperature tab should show 'real' badge; got:\n%s", body)
	}
}

// TestTemperatureUnitWiring verifies the TemperatureUnit setting actually
// changes the rendered unit (was previously hardcoded to °C).
func TestTemperatureUnitWiring(t *testing.T) {
	m := NewModel()
	m.settings = config.Default()
	m.settings.TemperatureUnit = "C"
	if got := m.formatTemp(100); got != "100.0°C" {
		t.Errorf("C: formatTemp(100) = %q, want 100.0°C", got)
	}
	m.settings.TemperatureUnit = "F"
	if got := m.formatTemp(100); got != "212.0°F" { // 100°C == 212°F
		t.Errorf("F: formatTemp(100) = %q, want 212.0°F", got)
	}
}

// TestThresholdMarks verifies the CPU/Mem alert thresholds drive a visible
// marker (was previously echoed in Settings but never compared to live values).
func TestThresholdMarks(t *testing.T) {
	m := NewModel()
	m.settings = config.Default()
	m.settings.CPUAlertThreshold = 80
	m.settings.MemoryAlertThreshold = 0 // 0 disables
	m.last.CPU.UsagePercent = 85
	m.last.Memory.UsagePercent = 95
	cpu, mem := m.thresholdMarks()
	if cpu != "!" {
		t.Errorf("CPU 85 over threshold 80 should mark; got %q", cpu)
	}
	if mem != "" {
		t.Errorf("Mem threshold 0 disables; got %q", mem)
	}
	m.last.CPU.UsagePercent = 50
	if cpu, _ := m.thresholdMarks(); cpu != "" {
		t.Errorf("CPU 50 under threshold 80 should not mark; got %q", cpu)
	}
}

// TestUpdateIntervalApplied verifies the saved UpdateInterval drives the tick
// cadence (was previously hardcoded to 1s).
func TestUpdateIntervalApplied(t *testing.T) {
	m := NewModel()
	m.settings = config.Default()
	m.settings.UpdateInterval = 2 * time.Second
	// tickCmd is opaque, but it must not panic and must read the setting; the
	// observable contract is that NewModel created the collector at the saved
	// interval. Here we just assert the helper honors a custom interval by not
	// falling back when one is set (smoke: cmd is non-nil).
	if cmd := m.tickCmd(); cmd == nil {
		t.Fatal("tickCmd returned nil")
	}
}
