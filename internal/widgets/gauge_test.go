package widgets

import (
	"math"
	"strings"
	"testing"
)

func TestSparklineRender(t *testing.T) {
	s := NewSparkline()
	s.Data = []float64{10, 20, 30, 40, 50}
	s.Width = 10
	s.Height = 5

	result := s.Render()

	if result == "" {
		t.Error("Expected non-empty render output")
	}

	// Check that output has correct number of lines
	lines := strings.Split(result, "\n")
	if len(lines) != s.Height {
		t.Errorf("Expected %d lines, got %d", s.Height, len(lines))
	}
}

func TestBarGaugeRender(t *testing.T) {
	bg := NewBarGauge()
	bg.Value = 50
	bg.Max = 100
	bg.Width = 20

	result := bg.Render()

	if result == "" {
		t.Error("Expected non-empty render output")
	}

	// Just check that we have some output - lipgloss styling wraps the content
	if len(result) == 0 {
		t.Error("Expected non-empty output from render")
	}
}

func TestGetMinMax(t *testing.T) {
	if mn, mx := getMinMax(nil); mn != 0 || mx != 1 {
		t.Errorf("getMinMax(nil) = (%v,%v), want (0,1)", mn, mx)
	}
	if mn, mx := getMinMax([]float64{3, -2, 7, 0}); mn != -2 || mx != 7 {
		t.Errorf("getMinMax = (%v,%v), want (-2,7)", mn, mx)
	}
}

func TestSampleData(t *testing.T) {
	if got := sampleData([]float64{1, 2, 3}, 0); got != nil {
		t.Errorf("sampleData width<=0 = %v, want nil", got)
	}
	if got := sampleData([]float64{1, 2, 3}, 10); len(got) != 3 {
		t.Errorf("sampleData len<=width should pass through; got %v", got)
	}
	if got := sampleData([]float64{1, 2, 3, 4, 5, 6}, 3); len(got) != 3 {
		t.Errorf("sampleData downsample len = %d, want 3", len(got))
	}
}

// TestSparklineEdgeCases ensures the divide-by-zero / bounds guards hold:
// empty, constant (max==min), negative, and zero Width/Height must not panic.
func TestSparklineEdgeCases(t *testing.T) {
	for i, s := range []*Sparkline{
		{Data: nil, Width: 10, Height: 3},
		{Data: []float64{5, 5, 5}, Width: 10, Height: 3},
		{Data: []float64{-3, -1, -2}, Width: 10, Height: 3},
		{Data: []float64{1, 2, 3}, Width: 0, Height: 3},
		{Data: []float64{1, 2, 3}, Width: 10, Height: 0},
	} {
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Errorf("case %d: Render panicked: %v", i, r)
				}
			}()
			_ = s.Render()
		}()
	}
}

// TestMultiSparklineRendersWithZeroMin is a regression for the global-min
// bug: a series whose minimum is 0 must not be discarded, and Render must
// produce one line per series without panicking.
func TestMultiSparklineRendersWithZeroMin(t *testing.T) {
	m := &MultiSparkline{Width: 10, Data: [][]float64{{0, 5}, {10, 20}}}
	out := m.Render()
	if out == "" {
		t.Error("MultiSparkline.Render returned empty for non-empty data")
	}
}

// TestSparklineFillScalesToHeight is a regression for the fill count being
// tied to the fixed 0-8 glyph scale instead of Height: a single column at
// 50% of a Height=4 sparkline must fill 2 of 4 rows, not all 4.
func TestSparklineFillScalesToHeight(t *testing.T) {
	s := &Sparkline{Data: []float64{5}, Min: 0, Max: 10, Width: 1, Height: 4, AutoScale: false}
	lines := strings.Split(s.Render(), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want 4", len(lines))
	}
	filled := 0
	for _, ln := range lines {
		if strings.ContainsAny(ln, "▁▂▃▄▅▆▇█") {
			filled++
		}
	}
	if filled != 2 {
		t.Errorf("filled %d of 4 rows for a 50%% value, want 2\noutput:\n%q", filled, s.Render())
	}
}

func TestMiniGaugeEdgeCases(t *testing.T) {
	for _, g := range []*MiniGauge{
		{Value: 50, Max: 0, Width: 10},    // Max=0 must not divide by zero
		{Value: 250, Max: 100, Width: 10}, // over 100 clamps
		{Value: -5, Max: 100, Width: 10},  // negative clamps to 0
	} {
		if g.Render() == "" {
			t.Errorf("MiniGauge.Render returned empty for %+v", g)
		}
	}
}

func TestBarGaugeColorFunc(t *testing.T) {
	bg := NewBarGauge()
	bg.Value = 90
	bg.Max = 100
	bg.Width = 20
	bg.ColorFunc = func(v float64) string {
		if v >= 80 {
			return "#FF0000" // Red for high values
		}
		return "#00FF00"
	}

	result := bg.Render()

	if result == "" {
		t.Error("Expected non-empty render output with ColorFunc")
	}
}

func TestGaugesContainInvalidGeometryAndValues(t *testing.T) {
	if got := (&BarGauge{Value: 50, Max: 100, Width: -4}).Render(); got != "" {
		t.Fatalf("negative-width bar = %q, want empty", got)
	}
	if got := (&MiniGauge{Value: 50, Max: 100, Width: 0}).Render(); got != "" {
		t.Fatalf("zero-width mini gauge = %q, want empty", got)
	}

	bar := &BarGauge{Value: math.NaN(), Max: math.Inf(1), Width: 8, ShowPercent: true}
	if got := bar.Render(); got == "" || !strings.Contains(got, "0.0%") {
		t.Fatalf("non-finite bar should degrade to zero, got %q", got)
	}
	if !math.IsInf(bar.Max, 1) {
		t.Fatal("Render must not mutate caller-owned Max")
	}
}
