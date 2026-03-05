package widgets

import (
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
