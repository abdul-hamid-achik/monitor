package tuistudio

import (
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/tuimark"
)

// TestTrendsAvailableShowsStats matches examples/monitor's Trends panel
// contract: entering the tab (view_changed) refreshes the cache and shows
// a stat line per metric.
func TestTrendsAvailableShowsStats(t *testing.T) {
	s := newFixtureStudio(t)
	must(t, s.ui.Set("view", "trends"))
	fire(t, s, "view_changed", tuimark.Event{Value: "trends"})
	sc := screen(t, s, 120, 24)
	if !strings.Contains(sc, "cpu usage") || !strings.Contains(sc, "memory usage") {
		t.Fatalf("expected both trend panels:\n%s", sc)
	}
	if !strings.Contains(sc, "samples 60") {
		t.Errorf("expected a populated stat line:\n%s", sc)
	}
}

// TestTrendsNoStoreYetReportsIt matches the Bubble Tea studio's "norec"
// sentinel: with no history store, the tab says so instead of showing
// stale or invented data.
func TestTrendsNoStoreYetReportsIt(t *testing.T) {
	s := newFixtureStudio(t)
	s.history = fixtureHistoryReader{noData: true}
	must(t, s.ui.Set("view", "trends"))
	fire(t, s, "view_changed", tuimark.Event{Value: "trends"})
	sc := screen(t, s, 120, 24)
	if !strings.Contains(sc, "No recorded history yet") {
		t.Fatalf("expected the no-history message:\n%s", sc)
	}
}
