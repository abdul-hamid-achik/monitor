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

// TestRateHistoriesShareOneScale: network and disk histories are bytes per
// second; the view draws them with min="0" max="100", so the host maps both
// series of a pair onto their shared min..max, like the Bubble Tea studio's
// MultiSparkline. Binding raw bytes filled every column to the top.
func TestRateHistoriesShareOneScale(t *testing.T) {
	got := sharedScale([]float64{200_000, 800_000}, []float64{100_000, 400_000})
	want := [][]any{{100.0 / 7, 100.0}, {0.0, 300.0 / 7}}
	for i := range want {
		for j := range want[i] {
			if d := got[i][j].(float64) - want[i][j].(float64); d > 1e-9 || d < -1e-9 {
				t.Fatalf("sharedScale = %v, want %v", got, want)
			}
		}
	}
	if flat := sharedScale([]float64{5, 5}); flat[0][0] != 0.0 || flat[0][1] != 0.0 {
		t.Fatalf("a flat series maps to %v, want zeros", flat)
	}

	s := newFixtureStudio(t)
	for i := 0; i < 20; i++ {
		must(t, s.refreshNow())
	}
	must(t, s.ui.Set("view", "network"))
	for _, line := range strings.Split(screen(t, s, 100, 24), "\n") {
		if strings.Contains(line, "upload") && strings.Count(line, "█") > 5 {
			t.Fatalf("the upload history is saturated (upload is the smaller series):\n%s", line)
		}
	}
}
