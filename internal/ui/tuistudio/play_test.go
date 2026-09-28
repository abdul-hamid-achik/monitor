package tuistudio

import (
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/tuimark"
)

// SPEC v0.3 §21 test 90 (monitor acceptance): key-driven flows are tested
// through the public App.Play (0.3a) instead of firing a handler by hand
// to simulate what a key would do. Play drives the real keymap dispatch
// and calls the handlers newFixtureStudio's newStudio already registered,
// exactly as Run would from the same input, so these tests exercise the
// same path a real terminal session does — the numbered tab keys, the "K"/
// "n" kill-confirmation keys, and "/" typing, none of which any handler-
// level test previously drove through the keymap itself.

// TestPlayKillFlow: "7" focuses #procs, "ctrl+a" marks every visible row,
// and "K" (when="#procs:focus") fires kill_ask, opening the confirmation
// with focus moved to the modal (kept from the Bubble Tea studio: a
// focused "yes" button would confirm the kill on enter or space, so both
// buttons are focusable="false" and enter/space fire nothing while it is
// open). "n" cancels: the confirmation closes and processes.go's
// kill_cancel handler drops the marks, so no row is left checked.
func TestPlayKillFlow(t *testing.T) {
	s := newFixtureStudio(t)
	opts := tuimark.PlayOptions{Cols: 100, Rows: 24}

	res, err := s.ui.Play(opts, "7", "ctrl+a", "K")
	if err != nil {
		t.Fatalf("7 ctrl+a K: %v", err)
	}
	if res.Dump.Focus == nil || *res.Dump.Focus != "kill" {
		focus := "<none>"
		if res.Dump.Focus != nil {
			focus = *res.Dump.Focus
		}
		t.Fatalf("K did not open the confirmation with the modal focused: focus=%s events=%+v", focus, res.Events)
	}

	res, err = s.ui.Play(opts, "enter", "space")
	if err != nil {
		t.Fatalf("enter space: %v", err)
	}
	if len(res.Events) != 0 {
		t.Errorf("enter/space with the confirmation open fired %+v, want none", res.Events)
	}

	res, err = s.ui.Play(opts, "n")
	if err != nil {
		t.Fatalf("n: %v", err)
	}
	if res.Dump.Focus != nil && *res.Dump.Focus == "kill" {
		t.Fatal("n did not close the confirmation")
	}
	for _, node := range res.Dump.Nodes {
		if node.Checked {
			t.Fatalf("n did not clear the marks: row %s is still checked", node.ID)
		}
	}
}

// TestPlayTabSwitchingByNumber drives every "1".."9" key of the keymap
// (each a switch-to bound when="#app") and checks that it both writes the
// tabs' bound "view" and actually activates that tab (Validate/Dump lay
// out only the active tab, so its own node round-trips through the dump
// only while it is current).
func TestPlayTabSwitchingByNumber(t *testing.T) {
	s := newFixtureStudio(t)
	opts := tuimark.PlayOptions{Cols: 120, Rows: 30}
	tabs := []string{"overview", "cpu", "memory", "thermal", "disk", "network", "processes", "settings", "trends"}

	for i, id := range tabs {
		key := string(rune('1' + i))
		res, err := s.ui.Play(opts, key)
		if err != nil {
			t.Fatalf("%s: %v", key, err)
		}
		if view, _ := s.ui.Get("view"); view != id {
			t.Errorf("pressing %s: view = %v, want %q", key, view, id)
		}
		active := false
		for _, node := range res.Dump.Nodes {
			if node.ID == id && node.Tag == "tab" {
				active = true
			}
		}
		if !active {
			t.Errorf("pressing %s: tab %q is not the laid-out (active) tab", key, id)
		}
	}
}

// TestPlayFilterTyping: "7" focuses #procs, "/" (when="#procs:focus")
// opens the filter row and focuses #filter, and "text:agent" types into
// it, firing processes.go's "filter" handler (on:change) and narrowing
// the table the same way TestFilterNarrowsAndCancelRestores does at the
// handler level — here through the real "/" key and typed text instead.
// "tab" (bound to filter_apply while #filter is focused) returns focus to
// the table with the query still applied.
func TestPlayFilterTyping(t *testing.T) {
	s := newFixtureStudio(t)
	opts := tuimark.PlayOptions{Cols: 120, Rows: 30}

	res, err := s.ui.Play(opts, "7", "/", "text:agent")
	if err != nil {
		t.Fatalf("7 / text:agent: %v", err)
	}
	screen := strings.Join(res.Dump.Grid, "\n")
	if !strings.Contains(screen, "monitor-agent") || strings.Contains(screen, "WindowServer") {
		t.Fatalf("typing \"agent\" did not narrow the table to monitor-agent:\n%s", screen)
	}

	res, err = s.ui.Play(opts, "tab")
	if err != nil {
		t.Fatalf("tab: %v", err)
	}
	if res.Dump.Focus == nil || *res.Dump.Focus != "procs" {
		focus := "<none>"
		if res.Dump.Focus != nil {
			focus = *res.Dump.Focus
		}
		t.Fatalf("tab did not return focus to #procs: focus=%s", focus)
	}
}
