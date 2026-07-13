package studio

import (
	"context"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
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

// TestTabRendersWithData populates a full SystemInfo and asserts each tab
// renders its data branches (the CPU per-core loop, network byte formatting,
// and the disk-partition loop are skipped by the empty-data edge-case test).
func TestTabRendersWithData(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	fm := updated.(Model)
	fm.last.LastUpdate = time.Now()
	fm.last.CPU = collector.CPUInfo{
		UsagePercent: 55.5,
		PerCoreUsage: []float64{10, 90, 45, 70},
		CoreCount:    4, ThreadCount: 8, FrequencyMHz: 3200, LoadAvg1: 1.5,
		History: []float64{10, 20, 30, 40, 55},
	}
	fm.last.Memory = collector.MemoryInfo{
		TotalBytes: 16 << 30, UsedBytes: 8 << 30, UsagePercent: 50,
		SwapTotal: 4 << 30, SwapUsed: 1 << 30, History: []float64{40, 45, 50},
	}
	fm.last.Disk = collector.DiskInfo{
		Partitions:  []collector.DiskPartitionInfo{{MountPoint: "/spectest", UsagePercent: 73, TotalBytes: 500 << 30, UsedBytes: 365 << 30}},
		ReadPerSec:  2 << 20,
		WritePerSec: 1 << 20,
	}
	fm.last.Network = collector.NetworkInfo{
		BytesRecvPerSec: 5 << 20, BytesSentPerSec: 2 << 20,
		DownloadHistory: []float64{1, 2, 5}, UploadHistory: []float64{1, 1, 2},
	}

	cases := []struct {
		view viewID
		want string
	}{
		{viewCPU, "Core 0"},       // per-core bar loop
		{viewNetwork, "Download"}, // network panel
		{viewNetwork, "Upload"},
		{viewDisk, "/spectest"}, // partition loop renders the mount point
	}
	for _, c := range cases {
		fm.view = c.view
		body := fm.View().Content
		if !strings.Contains(body, c.want) {
			t.Errorf("view %d render missing %q; got:\n%s", c.view, c.want, body)
		}
	}
}

// TestPidRefusedMatchesCLIAndMCP locks the kill-safety invariant: the TUI
// confirm handler must refuse BOTH protected and system PIDs, like the CLI
// (kill.go) and MCP (server.go) — not just protected ones. Regression for the
// divergence where a system-owned non-protected PID was killable in the TUI.
func TestPidRefusedMatchesCLIAndMCP(t *testing.T) {
	m := NewModel()
	m.killConf.Processes = []collector.ProcessInfo{
		{PID: 1, Name: "launchd", IsProtected: true, IsSystem: false},
		{PID: 50, Name: "systemd-ish", IsProtected: false, IsSystem: true}, // system, NOT protected
		{PID: 4242, Name: "myapp", IsProtected: false, IsSystem: false},
	}
	cases := map[int32]bool{
		1:    true,  // protected -> refused
		50:   true,  // system -> refused (the bug: this used to be killable)
		4242: false, // ordinary user process -> killable
		9999: false, // unknown pid (not in confirmation) -> not refused
	}
	for pid, want := range cases {
		if got := m.pidRefused(pid); got != want {
			t.Errorf("pidRefused(%d) = %v, want %v", pid, got, want)
		}
	}
}

// TestKillConfirmNoticeOnSpared verifies the TUI reports spared protected/
// system PIDs in a transient notice (parity with the CLI refusal line / MCP
// refused payload) rather than silently skipping them. Both selected PIDs are
// refused, so no real process is signaled.
func TestKillConfirmNoticeOnSpared(t *testing.T) {
	m := NewModel()
	m.showKillConfirm = true
	m.killConf.Processes = []collector.ProcessInfo{
		{PID: 1, Name: "launchd", IsProtected: true},
		{PID: 50, Name: "sysd", IsSystem: true},
	}
	m.selectedPids = map[int32]bool{1: true, 50: true}

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "y", Code: 'y'}))
	fm := updated.(Model)
	if fm.killNotice == "" {
		t.Fatal("confirming a kill of only protected/system PIDs should set a notice")
	}
	if !strings.Contains(fm.killNotice, "launchd") || !strings.Contains(fm.killNotice, "Spared 2") {
		t.Errorf("notice = %q, want spared count + process names", fm.killNotice)
	}
	if fm.killNoticeTicks <= 0 {
		t.Error("notice should have a positive tick countdown")
	}
	// The notice appears in the rendered status bar.
	fm.ready = true
	fm.width, fm.height = 120, 40
	if !strings.Contains(fm.View().Content, "Spared 2") {
		t.Error("status bar should show the spared notice")
	}
	// And clears after its ticks elapse.
	n := fm.killNoticeTicks + 1
	for i := 0; i < n; i++ {
		u, _ := fm.Update(tickMsg(time.Now()))
		fm = u.(Model)
	}
	if fm.killNotice != "" {
		t.Errorf("notice should clear after its ticks; still %q", fm.killNotice)
	}
}

func TestKillResultFeedback(t *testing.T) {
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	updated, _ := m.Update(killBatchResultMsg{
		results: []kill.Result{
			{PID: 10, Outcome: kill.OutcomeTerminated},
			{PID: 11, Outcome: kill.OutcomeStillRunning},
		},
		failures: []string{"pid 12: permission denied"},
		spared:   []string{"launchd"},
	})
	m = updated.(Model)
	for _, want := range []string{"terminated 1", "still running 1", "failed 1", "spared 1"} {
		if !strings.Contains(m.killNotice, want) {
			t.Errorf("verified kill notice %q missing %q", m.killNotice, want)
		}
	}
	if m.killNoticeTicks <= 0 {
		t.Error("verified kill feedback should remain visible for several ticks")
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
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	m.settings = config.Default()
	m.view = viewSettings
	m.settings.UpdateInterval = 5 * time.Second
	c := m.collector
	c.SetInterval(5 * time.Second)
	events := make(chan struct{}, 1)
	unsubscribe := c.Subscribe(func(collector.Event) {
		select {
		case events <- struct{}{}:
		default:
		}
	})
	defer unsubscribe()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	// 5s cycles to 500ms. The active collector should reset immediately, not
	// retain the five-second ticker it started with.
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(Model)
	if m.settings.UpdateInterval != 500*time.Millisecond {
		t.Fatalf("cycled update interval = %v, want 500ms", m.settings.UpdateInterval)
	}
	select {
	case <-events:
		// A sample inside two seconds proves the running five-second ticker was
		// reset by the Settings edit.
	case <-time.After(2 * time.Second):
		t.Fatal("live collector did not adopt the updated 500ms interval")
	}
}

func TestResponsiveHeaderLayoutsAndHitTargets(t *testing.T) {
	m := NewModel()
	m.width = 140
	if got := m.headerLayout(); len(got.labels) != int(viewCount) || got.labels[3] != "4:Temperature" {
		t.Fatalf("wide header should use all full labels; got %#v", got.labels)
	}

	m.width = 80
	if got := m.headerLayout(); len(got.labels) != int(viewCount) || got.labels[3] != "4:Tmp" {
		t.Fatalf("80-column header should use all compact labels; got %#v", got.labels)
	}
	if got := lipgloss.Width(m.renderHeader()); got > m.width {
		t.Fatalf("compact header width = %d, terminal width = %d", got, m.width)
	}

	m.width = 40
	m.view = viewProcesses
	layout := m.headerLayout()
	if len(layout.views) != 1 || layout.views[0] != viewProcesses {
		t.Fatalf("narrow header should retain active process tab; got %#v", layout.views)
	}
	x := m.titleWidth() + 1
	if got, ok := m.headerTabAt(x); !ok || got != viewProcesses {
		t.Fatalf("narrow active-tab hit target = (%d, %v), want Processes", got, ok)
	}
	if got := lipgloss.Width(m.renderHeader()); got > m.width {
		t.Fatalf("narrow header width = %d, terminal width = %d", got, m.width)
	}
}

func TestHelpOverlayIsContextAwareAndModal(t *testing.T) {
	m := NewModel()
	m.ready = true
	m.width, m.height = 90, 30
	m.view = viewProcesses

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "?", Code: '?'}))
	m = updated.(Model)
	if !m.helpVisible {
		t.Fatal("? should open help")
	}
	content := m.View().Content
	for _, want := range []string{"Keyboard Help", "pause or resume", "force-kill", "Press ? or Esc"} {
		if !strings.Contains(content, want) {
			t.Errorf("process help missing %q", want)
		}
	}
	// Navigation is inert while the modal is open.
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(Model)
	if m.view != viewProcesses {
		t.Error("tab should not switch views behind help")
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m = updated.(Model)
	if m.helpVisible {
		t.Fatal("Esc should close help")
	}
}

func TestPauseFreezesSamplesAndStatus(t *testing.T) {
	m := NewModel()
	m.width = 100
	m.last.LastUpdate = time.Now()
	m.last.CPU.UsagePercent = 37
	m.paused = true

	updated, _ := m.Update(tickMsg(time.Now()))
	m = updated.(Model)
	if m.last.CPU.UsagePercent != 37 {
		t.Fatalf("paused tick changed CPU sample to %.1f", m.last.CPU.UsagePercent)
	}
	if status := m.statusText(); !strings.Contains(status, "PAUSED") || !strings.Contains(status, m.last.LastUpdate.Format("15:04:05")) {
		t.Fatalf("paused status lacks state/sample time: %q", status)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "p", Code: 'p'}))
	m = updated.(Model)
	if m.paused {
		t.Fatal("p should resume a paused model")
	}
}

func TestProcessTableRespondsToTerminalWidth(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	m = updated.(Model)
	if got := len(m.processTable.Columns()); got != 7 {
		t.Fatalf("wide process table columns = %d, want 7", got)
	}
	updated, _ = m.Update(tea.WindowSizeMsg{Width: 48, Height: 30})
	m = updated.(Model)
	if got := len(m.processTable.Columns()); got != 3 {
		t.Fatalf("narrow process table columns = %d, want 3", got)
	}
	if got := m.processTable.Columns()[1].Width; got < 10 {
		t.Fatalf("responsive name column width = %d, want at least 10", got)
	}
}

func TestProcessSearchApplyCancelClearAndMetadata(t *testing.T) {
	m := NewModel()
	m.view = viewProcesses
	m.last.Processes = []collector.ProcessInfo{
		{PID: 101, Name: "alpha", User: "alice"},
		{PID: 202, Name: "beta", User: "bob"},
	}
	m.searchQuery = "alpha"

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "/", Code: '/'}))
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m = updated.(Model)
	if m.searchQuery != "alpha" || m.processSearch {
		t.Fatalf("Esc should restore prior filter; query=%q active=%v", m.searchQuery, m.processSearch)
	}

	m.searchQuery = ""
	m.searchBefore = ""
	m.processSearch = true
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "bob", Code: 'b'}))
	m = updated.(Model)
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(Model)
	if m.processSearch || len(m.filteredProcesses()) != 1 || m.filteredProcesses()[0].PID != 202 {
		t.Fatalf("Enter should apply user-name filter; query=%q matches=%v", m.searchQuery, m.filteredProcesses())
	}

	m.processSearch = true
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: 'u', Mod: tea.ModCtrl}))
	m = updated.(Model)
	if m.searchQuery != "" || len(m.filteredProcesses()) != 2 {
		t.Fatalf("Ctrl+U should clear filter; query=%q matches=%d", m.searchQuery, len(m.filteredProcesses()))
	}
}

func TestResponsiveFrameAndStackedOverview(t *testing.T) {
	m := NewModel()
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 70, Height: 24})
	m = updated.(Model)
	m.last.LastUpdate = time.Now()
	m.last.CPU.CoreCount = 8
	m.last.CPU.ThreadCount = 8
	m.last.Memory.TotalBytes = 16 << 30

	if got := lipgloss.Width(m.renderOverview()); got > m.width {
		t.Fatalf("stacked overview width = %d, terminal width = %d", got, m.width)
	}
	if got := lipgloss.Height(m.View().Content); got != m.height {
		t.Fatalf("rendered frame height = %d, terminal height = %d", got, m.height)
	}
}

func TestNewModelWithDisabledTemperatureSource(t *testing.T) {
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	if m.collector == nil {
		t.Fatal("disabled temperature source should still create the system collector")
	}
	m.cancel()
}
