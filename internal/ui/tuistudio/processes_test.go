package tuistudio

import (
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/tuimark"
)

func fire(t *testing.T, s *studio, action string, ev tuimark.Event) {
	t.Helper()
	h, ok := s.handlers()[action]
	if !ok {
		t.Fatalf("no handler for action %q", action)
	}
	ev.Action = action
	if ev.Keys == nil {
		ev.Keys = map[string]any{}
	}
	if err := h(ev); err != nil {
		t.Fatalf("%s: %v", action, err)
	}
}

func screen(t *testing.T, s *studio, cols, rows int) string {
	t.Helper()
	d, err := s.ui.Dump(cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	if !d.OK {
		t.Fatalf("dump not ok, errors=%v", d.Errors)
	}
	return strings.Join(d.Grid, "\n")
}

func waitUntil(t *testing.T, timeout time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		if cond() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("condition not met within %s", timeout)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

// TestSortCPUAndMem matches the Tuimark example fixture's own contract:
// pressing the same sort key again reverses direction and flips the
// header arrow; the other key sorts by that column instead.
func TestSortCPUAndMem(t *testing.T) {
	s := newFixtureStudio(t)
	must(t, s.ui.Set("view", "processes"))
	fire(t, s, "sort_cpu", tuimark.Event{})
	sc := screen(t, s, 120, 20)
	if !strings.Contains(sc, "CPU%▼") {
		t.Errorf("expected descending cpu arrow:\n%s", sc)
	}

	fire(t, s, "sort_cpu", tuimark.Event{})
	sc = screen(t, s, 120, 20)
	if !strings.Contains(sc, "CPU%▲") {
		t.Errorf("expected ascending cpu arrow after pressing c twice:\n%s", sc)
	}

	fire(t, s, "sort_mem", tuimark.Event{})
	sc = screen(t, s, 120, 20)
	if !strings.Contains(sc, "MEM▼") || strings.Contains(sc, "CPU%▲") {
		t.Errorf("expected memory sort to replace the cpu arrow:\n%s", sc)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// TestFilterNarrowsAndCancelRestores matches examples/monitor's filter
// contract: the query narrows the table by name/pid/user, and cancel
// restores both the full list and the saved query.
func TestFilterNarrowsAndCancelRestores(t *testing.T) {
	s := newFixtureStudio(t)
	must(t, s.ui.Set("view", "processes"))
	fire(t, s, "filter_open", tuimark.Event{})
	fire(t, s, "filter", tuimark.Event{Value: "agent"})
	sc := screen(t, s, 120, 20)
	if !strings.Contains(sc, "monitor-agent") || strings.Contains(sc, "WindowServer") {
		t.Errorf("expected only monitor-agent to match \"agent\":\n%s", sc)
	}
	fire(t, s, "filter_cancel", tuimark.Event{})
	sc = screen(t, s, 120, 20)
	if !strings.Contains(sc, "WindowServer") {
		t.Errorf("expected the full list restored after cancel:\n%s", sc)
	}
}

// TestKillRefusesProtectedProcess is the safety-critical path: a
// protected process (launchd, pid 1) must never be signaled, even after
// an explicit y confirm, matching internal/kill's CheckSafety/pidRefused
// gate the CLI and MCP also honor.
func TestKillRefusesProtectedProcess(t *testing.T) {
	s := newFixtureStudio(t)
	must(t, s.ui.Set("view", "processes"))
	must(t, s.publishProcs())
	fire(t, s, "cursor_moved", tuimark.Event{Keys: map[string]any{"p": 1.0}}) // launchd
	fire(t, s, "kill_force_ask", tuimark.Event{})

	sc := screen(t, s, 100, 24)
	if !strings.Contains(sc, "[BLOCKED ]") || !strings.Contains(sc, "launchd") {
		t.Fatalf("expected launchd listed as blocked:\n%s", sc)
	}
	fire(t, s, "kill_confirm", tuimark.Event{})

	// Give the (safe, no-op-for-protected) async verify goroutine a moment,
	// then assert launchd was never removed from the fixture's process list.
	waitUntil(t, time.Second, func() bool {
		s.mu.Lock()
		defer s.mu.Unlock()
		return !s.showKill
	})
	if _, ok := s.findProcess(1); !ok {
		t.Fatal("launchd (pid 1, protected) was removed: the kill safety gate was bypassed")
	}
}

// TestKillEligibleProcessAndSparesProtected marks one protected and one
// eligible process, confirms the kill, and checks that only the eligible
// one is actually terminated (fixtureKiller.Verify removes it from the
// fixture; nothing real is ever signaled).
func TestKillEligibleProcessAndSparesProtected(t *testing.T) {
	s := newFixtureStudio(t)
	must(t, s.ui.Set("view", "processes"))
	must(t, s.publishProcs())
	fire(t, s, "marks_changed", tuimark.Event{Value: []any{1.0, 7744.0}}) // launchd + Code Helper
	fire(t, s, "kill_ask", tuimark.Event{})

	sc := screen(t, s, 100, 24)
	if !strings.Contains(sc, "[BLOCKED ]") || !strings.Contains(sc, "[ELIGIBLE]") {
		t.Fatalf("expected one blocked and one eligible row:\n%s", sc)
	}
	fire(t, s, "kill_confirm", tuimark.Event{})

	waitUntil(t, time.Second, func() bool {
		_, ok := s.findProcess(7744)
		return !ok
	})
	if _, ok := s.findProcess(1); !ok {
		t.Fatal("protected launchd (pid 1) must survive")
	}
}

// TestDiagnoseTracksPinnedPIDUntilItVanishes matches examples/monitor's
// contract: the detail stays pinned to its pid; once that pid leaves the
// snapshot (here, the fixture's ephemeral "demo-worker" after 3 ticks) the
// detail reports it is no longer present, and a refresh keeps saying so.
func TestDiagnoseTracksPinnedPIDUntilItVanishes(t *testing.T) {
	s := newFixtureStudio(t)
	must(t, s.ui.Set("view", "processes"))
	must(t, s.publishProcs())
	fire(t, s, "cursor_moved", tuimark.Event{Keys: map[string]any{"p": 11077.0}}) // demo-worker
	fire(t, s, "diagnose", tuimark.Event{})

	sc := screen(t, s, 100, 24)
	if !strings.Contains(sc, "process detail · demo-worker · pid 11077") {
		t.Fatalf("expected the demo-worker detail open:\n%s", sc)
	}

	for i := 0; i < 3; i++ {
		must(t, s.refreshNow())
	}
	fire(t, s, "detail_refresh", tuimark.Event{})
	sc = screen(t, s, 100, 24)
	if !strings.Contains(sc, "no longer present") {
		t.Fatalf("expected the vanished-pid message:\n%s", sc)
	}

	fire(t, s, "detail_close", tuimark.Event{})
	sc = screen(t, s, 100, 24)
	if strings.Contains(sc, "no longer present") {
		t.Fatalf("expected the detail modal closed:\n%s", sc)
	}
}
