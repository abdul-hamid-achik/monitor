package tuistudio

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/tuimark"
)

func newFixtureStudio(t *testing.T) *studio {
	t.Helper()
	s, err := newStudio(context.Background(), Options{Fixture: true})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSmokeValidateAndDump(t *testing.T) {
	s := newFixtureStudio(t)
	for _, d := range s.ui.Validate() {
		t.Errorf("diagnostic: %+v", d)
	}
	for _, cols := range []int{80, 120} {
		d, err := s.ui.Dump(cols, 30)
		if err != nil {
			t.Fatalf("dump %d: %v", cols, err)
		}
		if len(d.Errors) > 0 || !d.OK {
			t.Errorf("dump %d errors=%v ok=%v", cols, d.Errors, d.OK)
		}
	}
}

// TestSmokeEveryTabAtEveryWidth walks every tab at 40/80/120 columns and
// fails on any layout/bind error or warning: since Tuimark only lays out
// the active tab, Validate() alone does not exercise the inactive ones.
func TestSmokeEveryTabAtEveryWidth(t *testing.T) {
	s := newFixtureStudio(t)
	views := []string{"overview", "cpu", "memory", "thermal", "disk", "network", "processes", "settings", "trends"}
	if err := s.publishProcs(); err != nil {
		t.Fatal(err)
	}
	for _, view := range views {
		if err := s.ui.Set("view", view); err != nil {
			t.Fatalf("set view %s: %v", view, err)
		}
		for _, cols := range []int{40, 80, 120} {
			d, err := s.ui.Dump(cols, 30)
			if err != nil {
				t.Fatalf("dump %s@%d: %v", view, cols, err)
			}
			if len(d.Errors) > 0 || !d.OK {
				t.Errorf("dump %s@%d errors=%v ok=%v", view, cols, d.Errors, d.OK)
			}
		}
	}
}

// TestRunWithoutTerminalReportsOnStderr: with no terminal on stdin, Run
// fails, and like the Bubble Tea studio the failure is printed, since the
// command itself exits 0.
func TestRunWithoutTerminalReportsOnStderr(t *testing.T) {
	devnull, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	defer devnull.Close()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	oldIn, oldErr := os.Stdin, os.Stderr
	os.Stdin, os.Stderr = devnull, w
	runErr := RunWithOptions(Options{Fixture: true})
	os.Stdin, os.Stderr = oldIn, oldErr
	w.Close()
	out, _ := io.ReadAll(r)

	if runErr == nil {
		t.Fatal("RunWithOptions succeeded without a terminal")
	}
	if !strings.Contains(string(out), "Error running monitor studio --tuimark:") {
		t.Fatalf("stderr = %q, want the failure reported", out)
	}
}

// TestNoClippingInAnyTab is the monitor-side half of SPEC v0.3 §21 test 90
// (tuimark's own examples/monitor carries the fixture half, test 89), and
// the diagnostics gate of the 0.3b port (tuimark SPEC §30.6): Validate()
// reports nothing at all, in each of the 9 tabs, under both themes, at the
// 40, 80, and 120 columns it lays out. studio.tui is a version="3"
// document, so that includes L008 (a text cut without an ellipsis) and
// L009 (a node cutting a child on an axis it does not scroll), and the
// 0.3b vocabulary's own checks (keymap when groups, scale, priority,
// row-gap). Validate lays out only the active tab, so each of the 9 is
// made active through the tabs' bind (view), matching tuimark's
// TestNoClippingInAnyTab.
func TestNoClippingInAnyTab(t *testing.T) {
	s := newFixtureStudio(t)
	if err := s.publishProcs(); err != nil {
		t.Fatal(err)
	}
	tabs := []string{"overview", "cpu", "memory", "thermal", "disk", "network", "processes", "settings", "trends"}
	for _, theme := range []string{"dark", "light"} {
		if err := s.ui.Set("@theme", theme); err != nil {
			t.Fatal(err)
		}
		for _, tab := range tabs {
			if err := s.ui.Set("view", tab); err != nil {
				t.Fatal(err)
			}
			for _, d := range s.ui.Validate() {
				t.Errorf("%s, tab %s: %s", theme, tab, d)
			}
		}
	}
}

// TestOverviewPanelsAreNotClipped: the KPI grid takes the height its rows
// need (it once had a fixed height that cut the second row at 80
// columns). Where that is more than half the tab, as at 40 columns, it
// scrolls (max-height: 50%; overflow: scroll) instead of pushing #lower
// off the screen. At 120x30 it is exactly one row of panels, so no blank
// band opens between it and #lower. Each panel is a border plus two
// lines, never squashed.
func TestOverviewPanelsAreNotClipped(t *testing.T) {
	s := newFixtureStudio(t)
	for _, sz := range [][2]int{{120, 30}, {100, 30}, {80, 24}, {60, 30}, {40, 30}, {40, 24}} {
		d, err := s.ui.Dump(sz[0], sz[1])
		if err != nil {
			t.Fatal(err)
		}
		var grid, lower *tuimark.DumpNode
		for i := range d.Nodes {
			switch d.Nodes[i].ID {
			case "panels":
				grid = &d.Nodes[i]
			case "lower":
				lower = &d.Nodes[i]
			}
		}
		if grid == nil || lower == nil {
			t.Fatalf("%dx%d: no #panels or #lower node", sz[0], sz[1])
		}
		// The panels lie in the grid's scroll extent, which is its own
		// height when everything fits.
		extent := grid.H
		if grid.Scroll != nil && grid.Scroll.H != nil && *grid.Scroll.H > extent {
			extent = *grid.Scroll.H
		}
		n := 0
		for _, p := range d.Nodes {
			// The four KPI panels are the boxes whose only class is "panel";
			// #lower's boxes add "activity" or "top".
			if p.Tag != "box" || len(p.Classes) != 1 || p.Classes[0] != "panel" {
				continue
			}
			n++
			if p.H != 4 || p.Y < grid.Y || p.Y+p.H > grid.Y+extent {
				t.Errorf("%dx%d: panel at %d,%d is %dx%d in #panels %d+%d", sz[0], sz[1], p.X, p.Y, p.W, p.H, grid.Y, extent)
			}
		}
		if n != 4 {
			t.Errorf("%dx%d: %d panels in #panels, want 4", sz[0], sz[1], n)
		}
		if lower.Y != grid.Y+grid.H+1 {
			t.Errorf("%dx%d: #lower starts at row %d, want right under #panels (%d)", sz[0], sz[1], lower.Y, grid.Y+grid.H+1)
		}
		if sz == [2]int{120, 30} && grid.H != 4 {
			t.Errorf("120x30: #panels is %d rows, want 4 (one row of panels, no blank band)", grid.H)
		}
	}
}

func TestSmokeEveryActionHasAHandler(t *testing.T) {
	s := newFixtureStudio(t)
	for _, a := range s.ui.Catalog() {
		if a.Builtin {
			continue
		}
		if _, ok := s.handlers()[a.Name]; !ok {
			t.Errorf("no handler for action %q (%v)", a.Name, a.Sources)
		}
	}
}

func TestSmokeProcessesVisible(t *testing.T) {
	s := newFixtureStudio(t)
	if err := s.ui.Set("view", "processes"); err != nil {
		t.Fatal(err)
	}
	if err := s.publishProcs(); err != nil {
		t.Fatal(err)
	}
	d, err := s.ui.Dump(120, 30)
	if err != nil {
		t.Fatal(err)
	}
	screen := strings.Join(d.Grid, "\n")
	if !strings.Contains(screen, "WindowServer") || !strings.Contains(screen, "monitor-agent") {
		t.Errorf("expected fixture processes in the table:\n%s", screen)
	}
	// launchd is root-owned (system), hidden by default...
	if strings.Contains(screen, "launchd") {
		t.Errorf("launchd (a system process) should be hidden by default:\n%s", screen)
	}
	// ...until show_system_processes is toggled on.
	if err := s.cycleSetting("show_system_processes", 1); err != nil {
		t.Fatal(err)
	}
	if err := s.publishProcs(); err != nil {
		t.Fatal(err)
	}
	d, err = s.ui.Dump(120, 30)
	if err != nil {
		t.Fatal(err)
	}
	screen = strings.Join(d.Grid, "\n")
	if !strings.Contains(screen, "launchd") {
		t.Errorf("launchd should be visible once show_system_processes is on:\n%s", screen)
	}
}

// TestCoreRowsAreAdjacent: the per-core grid separates its columns with
// gap: 1 but its rows with row-gap: 0 (tuimark 0.3b), so the core rows
// touch, as the Bubble Tea studio draws them, and at 40 columns the 8
// fixture cores in one column are 8 rows tall instead of 15, which fits
// #cores-view at 40x24 without scrolling.
func TestCoreRowsAreAdjacent(t *testing.T) {
	s := newFixtureStudio(t)
	must(t, s.ui.Set("view", "cpu"))
	for _, sz := range [][2]int{{40, 24}, {80, 24}, {120, 30}} {
		d, err := s.ui.Dump(sz[0], sz[1])
		if err != nil {
			t.Fatal(err)
		}
		var ys []int
		var grid, view *tuimark.DumpNode
		for i, n := range d.Nodes {
			switch {
			case n.ID == "cores":
				grid = &d.Nodes[i]
			case n.ID == "cores-view":
				view = &d.Nodes[i]
			case n.Tag == "row" && len(n.Classes) > 0 && n.Classes[0] == "core":
				if len(ys) == 0 || ys[len(ys)-1] != n.Y {
					ys = append(ys, n.Y)
				}
			}
		}
		if grid == nil || view == nil || len(ys) == 0 {
			t.Fatalf("%dx%d: no #cores, #cores-view, or core rows", sz[0], sz[1])
		}
		for i := 1; i < len(ys); i++ {
			if ys[i] != ys[i-1]+1 {
				t.Errorf("%dx%d: core rows at %v, want adjacent rows", sz[0], sz[1], ys)
				break
			}
		}
		if grid.H != len(ys) {
			t.Errorf("%dx%d: #cores is %d rows tall for %d rows of cores", sz[0], sz[1], grid.H, len(ys))
		}
		if sz == [2]int{40, 24} && (view.Scroll == nil || view.Scroll.H == nil || *view.Scroll.H > view.H-2) {
			t.Errorf("40x24: #cores-view (%d rows, border included) scrolls: %+v", view.H, view.Scroll)
		}
	}
}
