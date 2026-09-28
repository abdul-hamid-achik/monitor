package tuistudio

import (
	"context"
	"reflect"
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

// sparkRows returns, in document order, the cells each laid-out sparkline
// paints at cols x rows (its row of the grid, from its x over its width).
func sparkRows(t *testing.T, s *studio, cols, rows int) []string {
	t.Helper()
	d, err := s.ui.Dump(cols, rows)
	if err != nil {
		t.Fatal(err)
	}
	if !d.OK || d.Wide {
		t.Fatalf("dump %dx%d: ok=%v wide=%v errors=%v", cols, rows, d.OK, d.Wide, d.Errors)
	}
	var out []string
	for _, n := range d.Nodes {
		if n.Tag != "sparkline" {
			continue
		}
		line := []rune(d.Grid[n.Y])
		out = append(out, string(line[n.X:n.X+n.W]))
	}
	return out
}

// TestRateHistoriesShareOneScale: network and disk histories are bytes per
// second and are bound raw. Each panel's two sparklines carry one scale
// group (scale="net_io", scale="disk_io"), so tuimark ranges both over the
// values they show together, like the Bubble Tea studio's MultiSparkline:
// the smaller series of a pair is drawn against the larger one's peak
// instead of filling its own row. Two differences from the host-side
// sharedScale this replaced are accepted (tuimark SPEC §30.6): the range
// covers only the last W values each sparkline shows, not the whole
// history, and a flat pair sits at half height (▄), not at 0.
func TestRateHistoriesShareOneScale(t *testing.T) {
	s := newFixtureStudio(t)
	info := s.last
	pairs := []struct {
		view, a, b string
		setA, setB *[]float64
	}{
		{"network", "network.down_hist", "network.up_hist", &info.Network.DownloadHistory, &info.Network.UploadHistory},
		{"disk", "disk.read_hist", "disk.write_hist", &info.Disk.ReadHistory, &info.Disk.WriteHistory},
	}
	for _, p := range pairs {
		*p.setA = []float64{200_000, 800_000}
		*p.setB = []float64{100_000, 400_000}
	}
	must(t, s.publishMetrics(info, nil))
	for _, p := range pairs {
		// The host binds bytes per second as they are: no 0-100 mapping.
		if got, _ := s.ui.Get(p.a); !reflect.DeepEqual(got, []any{200_000.0, 800_000.0}) {
			t.Errorf("%s = %v, want the raw history", p.a, got)
		}
		if got, _ := s.ui.Get(p.b); !reflect.DeepEqual(got, []any{100_000.0, 400_000.0}) {
			t.Errorf("%s = %v, want the raw history", p.b, got)
		}
		// One range for the pair, 100k..800k: 200k is level 1 (▁), 800k
		// level 8 (█), 100k level 0 (blank), 400k level 3 (▃), exactly
		// what sharedScale's 0-100 values drew under min="0" max="100".
		must(t, s.ui.Set("view", p.view))
		rows := sparkRows(t, s, 100, 24)
		if len(rows) != 2 || !strings.HasSuffix(rows[0], "▁█") || !strings.HasSuffix(rows[1], " ▃") {
			t.Errorf("%s at 100 columns: sparklines %q, want \"…▁█\" and \"… ▃\" on one scale", p.view, rows)
		}
	}

	// A flat pair (hi <= lo) sits at half height, where sharedScale drew 0.
	for _, p := range pairs {
		*p.setA = []float64{5, 5}
		*p.setB = []float64{5, 5}
	}
	must(t, s.publishMetrics(info, nil))
	for _, p := range pairs {
		must(t, s.ui.Set("view", p.view))
		for _, row := range sparkRows(t, s, 100, 24) {
			if !strings.HasSuffix(row, "▄▄") {
				t.Errorf("%s: flat sparkline %q, want it at half height (▄▄)", p.view, row)
			}
		}
	}

	// The range covers the values shown: a peak older than the last W
	// values sets it at 100 columns, where all 60 fit, and not at 40.
	hist := func(first float64) []float64 {
		h := make([]float64, studioHistorySize)
		for i := range h {
			h[i] = 100
		}
		h[0] = first
		return h
	}
	info.Network.DownloadHistory, info.Network.UploadHistory = hist(1000), hist(100)
	must(t, s.publishMetrics(info, nil))
	must(t, s.ui.Set("view", "network"))
	wide := sparkRows(t, s, 100, 24)
	if len(wide) != 2 || !strings.Contains(wide[0], "█") || strings.TrimSpace(wide[1]) != "" {
		t.Errorf("100 columns: sparklines %q, want the old peak as █ and the rest at the floor", wide)
	}
	narrow := sparkRows(t, s, 40, 24)
	for _, row := range narrow {
		if !strings.HasSuffix(row, "▄▄") || strings.Contains(row, "█") {
			t.Errorf("40 columns: sparkline %q, want the shown values flat (▄) with the peak out of view", row)
		}
	}

	s = newFixtureStudio(t)
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

// TestTemperatureUnitFollowsSettings: the Bubble Tea studio formats every
// reading in the configured unit; cycling it in Settings shows the new
// unit at once, without waiting for a sample.
func TestTemperatureUnitFollowsSettings(t *testing.T) {
	s := newFixtureStudio(t)
	must(t, s.ui.Set("view", "thermal"))
	c := s.last.Temperature.CPUPackage
	if sc := screen(t, s, 100, 30); !strings.Contains(sc, formatTemp(c, "C")) {
		t.Fatalf("want %q on the thermal tab:\n%s", formatTemp(c, "C"), sc)
	}
	must(t, s.cycleSetting("temperature_unit", 1))
	sc := screen(t, s, 100, 30)
	if want := formatTemp(c, "F"); !strings.Contains(sc, want) || strings.Contains(sc, formatTemp(c, "C")) {
		t.Fatalf("after switching to Fahrenheit want %q and no Celsius reading:\n%s", want, sc)
	}
	if got := formatTemp(100, "F"); got != "212.0 F" {
		t.Fatalf("formatTemp(100, F) = %q", got)
	}
}
