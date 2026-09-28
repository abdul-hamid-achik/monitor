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
// (tuimark's own examples/monitor carries the fixture half, test 89):
// studio.tui is a version="3" document, so Validate() reports L008 (a text
// cut without an ellipsis) and L009 (a node cutting a child on an axis it
// does not scroll) at 40, 80, and 120 columns. Validate lays out only the
// active tab, so each of the 9 is made active through the tabs' bind
// (view), under both themes, matching tuimark's TestNoClippingInAnyTab.
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
				if d.Code == "L008" || d.Code == "L009" {
					t.Errorf("%s, tab %s: %s", theme, tab, d)
				}
			}
		}
	}
}

// TestOverviewPanelsAreNotClipped: the KPI grid takes the height its rows
// need at every width (it once had a fixed height that cut the second
// row at 80 columns). Each panel is a border plus two lines.
func TestOverviewPanelsAreNotClipped(t *testing.T) {
	s := newFixtureStudio(t)
	for _, sz := range [][2]int{{120, 30}, {100, 30}, {80, 24}, {60, 30}, {40, 30}} {
		d, err := s.ui.Dump(sz[0], sz[1])
		if err != nil {
			t.Fatal(err)
		}
		var grid *tuimark.DumpNode
		for i := range d.Nodes {
			if d.Nodes[i].ID == "panels" {
				grid = &d.Nodes[i]
			}
		}
		if grid == nil {
			t.Fatalf("%dx%d: no #panels node", sz[0], sz[1])
		}
		n := 0
		for _, p := range d.Nodes {
			if p.Tag != "box" || p.Y < grid.Y || p.Y >= grid.Y+grid.H || p.ID == "panels" {
				continue
			}
			n++
			if p.H != 4 || p.Y+p.H > grid.Y+grid.H {
				t.Errorf("%dx%d: panel at %d,%d is %dx%d inside #panels %d+%d", sz[0], sz[1], p.X, p.Y, p.W, p.H, grid.Y, grid.H)
			}
		}
		if n != 4 {
			t.Errorf("%dx%d: %d panels inside #panels, want 4", sz[0], sz[1], n)
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
