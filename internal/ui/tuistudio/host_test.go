package tuistudio

import (
	"context"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/tuimark"
)

func newFixtureStudioWithOptions(t *testing.T, opts Options) *studio {
	t.Helper()
	s, err := newStudio(context.Background(), opts)
	if err != nil {
		t.Fatal(err)
	}
	return s
}

// TestPauseStopsSamplesAndRefreshTakesOneNow matches the Bubble Tea
// studio: "p" flips LIVE/PAUSED, and while paused the tick handler skips
// sampling, but "r" still takes one immediately.
func TestPauseStopsSamplesAndRefreshTakesOneNow(t *testing.T) {
	s := newFixtureStudio(t)
	fire(t, s, "pause", tuimark.Event{})
	sc := screen(t, s, 100, 24)
	if !strings.Contains(sc, "‖ PAUSED") {
		t.Fatalf("expected the paused status label:\n%s", sc)
	}

	fx := s.src.(*fixtureSource)
	before := s.last

	// Simulate one automatic sample tick (what src.Subscribe's callback
	// does): the fixture itself advances, but applySample(force=false)
	// must leave s.last (what's actually displayed) untouched while paused.
	tickInfo := fx.sample()
	must(t, s.applySample(tickInfo, false))
	if s.last.LastUpdate != before.LastUpdate {
		t.Fatalf("a sample tick updated s.last despite being paused: before=%v after=%v", before.LastUpdate, s.last.LastUpdate)
	}

	// "r" (refreshNow) always takes one immediately, paused or not.
	must(t, s.refreshNow())
	if !s.last.LastUpdate.After(before.LastUpdate) {
		t.Fatalf("expected refreshNow to update s.last despite being paused: before=%v after=%v", before.LastUpdate, s.last.LastUpdate)
	}

	fire(t, s, "pause", tuimark.Event{})
	sc = screen(t, s, 100, 24)
	if !strings.Contains(sc, "● LIVE") {
		t.Fatalf("expected the live status label after unpausing:\n%s", sc)
	}
}

// TestFixtureNeverStartsARealTemperatureSubprocess: DisableTemperatureSource
// is irrelevant in fixture mode because fixture mode never wires a real
// temperature.Source at all (no `sudo powermetrics` subprocess, ever); its
// readings always come from temperature.Estimate.
func TestFixtureNeverStartsARealTemperatureSubprocess(t *testing.T) {
	for _, disable := range []bool{true, false} {
		s := newFixtureStudioWithOptions(t, Options{Fixture: true, DisableTemperatureSource: disable})
		info := s.last
		if info.Temperature.Source != "estimated" {
			t.Errorf("disable=%v: expected an estimated temperature reading in fixture mode, got %q", disable, info.Temperature.Source)
		}
	}
}
