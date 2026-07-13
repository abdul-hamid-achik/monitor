package studio

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
)

func processDetailFixture(t *testing.T) Model {
	t.Helper()
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	t.Cleanup(m.cancel)
	m.ready = true
	m.width, m.height = 100, 32
	m.view = viewProcesses
	m.settings = config.Default()
	m.last.LastUpdate = time.Now()
	m.last.Processes = []collector.ProcessInfo{{
		PID: 42, Name: "worker", User: "alice", Status: "sleeping", Parent: 1,
		CPUPercent: 12.5, Memory: 256 << 20, MemoryPercent: 1.6, Threads: 7,
		IOReadBytes: 8 << 20, IOWriteBytes: 2 << 20,
	}}
	m.updateProcessTable()
	return m
}

func TestProcessDetailOpensFromCurrentRow(t *testing.T) {
	m := processDetailFixture(t)
	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	m = updated.(Model)
	if !m.processDetailVisible || m.processDetailPID != 42 {
		t.Fatalf("Enter should pin detail to PID 42; visible=%v pid=%d", m.processDetailVisible, m.processDetailPID)
	}
	content := m.View().Content
	for _, want := range []string{
		"Process Detail", "worker", "alice", "sleeping", "PID 1",
		"12.5%", "RSS", "I/O read", "Threads", "Protection", "USER",
		"Enter / Esc / q close", "? help",
	} {
		if !strings.Contains(content, want) {
			t.Errorf("process detail missing %q; got:\n%s", want, content)
		}
	}
}

func TestProcessDetailIsModalAndHasLayeredHelp(t *testing.T) {
	m := processDetailFixture(t)
	m.processDetailVisible = true
	m.processDetailPID = 42

	updated, _ := m.Update(tea.KeyPressMsg(tea.Key{Text: "k", Code: 'k'}))
	m = updated.(Model)
	if m.showKillConfirm || !m.processDetailVisible {
		t.Fatal("table kill shortcut must not leak through process detail")
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	m = updated.(Model)
	if m.view != viewProcesses {
		t.Fatal("tab navigation must not leak through process detail")
	}

	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Text: "?", Code: '?'}))
	m = updated.(Model)
	if !m.helpVisible || !m.processDetailVisible {
		t.Fatal("? should layer help over the still-open process detail")
	}
	if got := m.View().Content; !strings.Contains(got, "close process detail") {
		t.Fatalf("detail help should explain how to close it; got:\n%s", got)
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m = updated.(Model)
	if m.helpVisible || !m.processDetailVisible {
		t.Fatal("first Esc should close help and return to process detail")
	}
	updated, _ = m.Update(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m = updated.(Model)
	if m.processDetailVisible || m.quitting {
		t.Fatal("second Esc should close process detail without quitting Studio")
	}
}

func TestProcessDetailExplainsUnavailableMetrics(t *testing.T) {
	m := processDetailFixture(t)
	p := &m.last.Processes[0]
	p.Status = ""
	p.MetricStates = map[string]collector.MetricStatus{
		"status":  {State: collector.MetricUnavailable, Reason: "permission denied"},
		"cpu":     {State: collector.MetricUnsupported, Reason: "platform does not expose per-process CPU"},
		"io_read": {State: collector.MetricUnavailable, Reason: "requires elevated privileges"},
	}
	// The stacked responsive layout keeps long diagnostic reasons in one
	// reading order, making their presence straightforward to assert.
	m.width = 70
	m.processDetailVisible = true
	m.processDetailPID = p.PID
	content := normalizedStudioText(m.renderProcessDetail())
	for _, reason := range []string{"permission denied", "platform does not expose per-process CPU", "requires elevated privileges"} {
		if !strings.Contains(content, "unavailable · "+reason) {
			t.Errorf("detail should surface metric reason %q; got:\n%s", reason, content)
		}
	}
}

func normalizedStudioText(content string) string {
	content = strings.Map(func(r rune) rune {
		switch r {
		case '│', '─', '╭', '╮', '╰', '╯':
			return -1
		default:
			return r
		}
	}, ansi.Strip(content))
	content = strings.Join(strings.Fields(content), " ")
	// Lipgloss wraps a hyphenated word after the hyphen; restore the reading
	// order so assertions describe the visible phrase rather than line breaks.
	return strings.ReplaceAll(content, "- ", "-")
}

func TestProcessDetailHandlesExitedPIDAndNarrowTerminal(t *testing.T) {
	m := processDetailFixture(t)
	m.width, m.height = 48, 24
	m.processDetailVisible = true
	m.processDetailPID = 999
	content := m.renderProcessDetail()
	if !strings.Contains(content, "no longer present") || !strings.Contains(content, "may have exited") {
		t.Fatalf("missing exited-process guidance; got:\n%s", content)
	}
	if width := lipgloss.Width(content); width > m.width {
		t.Fatalf("narrow detail width=%d exceeds terminal width=%d", width, m.width)
	}
	if height := lipgloss.Height(content); height != m.height-2 {
		t.Fatalf("detail height=%d, want content height=%d", height, m.height-2)
	}
}

func TestProcessDetailShowsProtectionPolicy(t *testing.T) {
	cases := []struct {
		name      string
		protected bool
		system    bool
		want      string
	}{
		{"protected", true, false, "PROTECTED"},
		{"system", false, true, "SYSTEM"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := processDetailFixture(t)
			m.last.Processes[0].IsProtected = tc.protected
			m.last.Processes[0].IsSystem = tc.system
			m.processDetailVisible = true
			m.processDetailPID = 42
			if got := m.renderProcessDetail(); !strings.Contains(got, tc.want) || !strings.Contains(got, "termination is blocked") {
				t.Fatalf("safety policy missing for %s; got:\n%s", tc.name, got)
			}
		})
	}
}
