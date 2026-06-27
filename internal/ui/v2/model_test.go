package v2

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func TestNewModelBasics(t *testing.T) {
	m := NewModel()
	if m.ready { t.Fatal("fresh model should not be ready") }
	if m.quitting { t.Fatal("fresh model should not be quitting") }
	if m.collector == nil { t.Fatal("model should have a collector") }
	if m.processTable == nil { t.Fatal("model should have a process table") }
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
	if !v.AltScreen { t.Fatal("initializing view must use AltScreen") }
	if !strings.Contains(v.Content, "Initializing") {
		t.Fatalf("initializing view should contain 'Initializing'; got %q", v.Content)
	}
}

func TestHandleKeyQuit(t *testing.T) {
	for _, key := range []string{"q", "ctrl+c", "esc"} {
		m := NewModel()
		updated, cmd := m.Update(tea.KeyPressMsg(tea.Key{Text: key, Code: 'q'}))
		fm := updated.(Model)
		if !fm.quitting { t.Errorf("Update(%s) should set quitting", key) }
		if cmd == nil { t.Errorf("Update(%s) should return a tea.Quit cmd", key) }
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
	for start := viewID(0); start < 8; start++ {
		m := NewModel()
		m.view = start
		for i := 0; i < 8; i++ {
			updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
			m = updated.(Model)
		}
		if m.view != start {
			t.Errorf("tab x8 from %d: ended at %d, want %d", start, m.view, start)
		}
		for i := 0; i < 8; i++ {
			updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
			m = updated.(Model)
		}
		if m.view != start {
			t.Errorf("left x8 from %d: ended at %d, want %d", start, m.view, start)
		}
	}
}

func TestTickRefreshesSnapshot(t *testing.T) {
	m := NewModel()
	info := m.collector.Collect(m.ctx)
	if !info.LastUpdate.IsZero() { m.last = info }
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
		if body == "" { t.Errorf("view %d should render non-empty content", view) }
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
	if body == "" { t.Fatalf("Processes tab should render with empty list") }
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
	if !strings.Contains(body, "Processes") { t.Errorf("should contain 'Processes'") }
	if !strings.Contains(body, "test") { t.Errorf("should contain process name 'test'") }
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