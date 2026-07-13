package widgets

import (
	"fmt"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
)

// Sparkline renders a sparkline graph from data points
type Sparkline struct {
	Data       []float64
	Width      int
	Height     int
	Min        float64
	Max        float64
	AutoScale  bool
	Color      string
	ShowAxis   bool
	ShowLabels bool
}

// Pre-defined styles to avoid allocations in hot loops
var (
	sparklineStyleCache = make(map[string]lipgloss.Style)
	sparklineGlyphCache = make(map[string][]string)
	barFilledStyleCache = make(map[string]lipgloss.Style)
	barEmptyStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#607089"))
)

func getSparklineStyle(color string) lipgloss.Style {
	if style, ok := sparklineStyleCache[color]; ok {
		return style
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	sparklineStyleCache[color] = style
	return style
}

func getBarFilledStyle(color string) lipgloss.Style {
	if style, ok := barFilledStyleCache[color]; ok {
		return style
	}
	style := lipgloss.NewStyle().Foreground(lipgloss.Color(color))
	barFilledStyleCache[color] = style
	return style
}

func getSparklineGlyphs(color string) []string {
	if glyphs, ok := sparklineGlyphCache[color]; ok {
		return glyphs
	}

	style := getSparklineStyle(color)
	glyphs := make([]string, len(SparklineChars))
	for i, char := range SparklineChars {
		glyphs[i] = style.Render(char)
	}
	sparklineGlyphCache[color] = glyphs
	return glyphs
}

var SparklineChars = []string{" ", "▁", "▂", "▃", "▄", "▅", "▆", "▇", "█"}

// NewSparkline creates a new sparkline widget
func NewSparkline() *Sparkline {
	return &Sparkline{
		Width:     40,
		Height:    5,
		AutoScale: true,
		Color:     "#88C0D0", // Nord blue
		ShowAxis:  true,
	}
}

// Render renders the sparkline
func (s *Sparkline) Render() string {
	if len(s.Data) == 0 || s.Width <= 0 || s.Height <= 0 {
		return ""
	}

	// Calculate min/max
	min, max := s.Min, s.Max
	if s.AutoScale {
		min, max = getMinMax(s.Data)
		// Add some padding
		range_ := max - min
		if range_ > 0 {
			min -= range_ * 0.05
			max += range_ * 0.05
		} else {
			min = 0
			max = 1
		}
	}

	if max == min {
		max = min + 1
	}

	// Sample data to fit width
	sampled := sampleData(s.Data, s.Width)
	if len(sampled) == 0 {
		return ""
	}
	glyphs := getSparklineGlyphs(s.Color)
	builders := make([]strings.Builder, s.Height)

	// Generate sparkline characters
	full := glyphs[len(SparklineChars)-1] // █, styled
	for x := 0; x < len(sampled) && x < s.Width; x++ {
		value := sampled[x]
		normalized := (value - min) / (max - min)
		if normalized < 0 {
			normalized = 0
		}
		if normalized > 1 {
			normalized = 1
		}

		// Scale the fill to Height (not the fixed 0-8 glyph scale): the
		// bottom `fullRows` rows are full blocks, and the next row up uses a
		// partial glyph for the fractional remainder. This keeps mid-range
		// values distinguishable at any Height (Height=1 == a classic
		// single-row sparkline glyph).
		exact := normalized * float64(s.Height)
		fullRows := int(exact)
		partialIdx := int((exact - float64(fullRows)) * float64(len(SparklineChars)-1))
		if partialIdx >= len(SparklineChars) {
			partialIdx = len(SparklineChars) - 1
		}

		// Fill from bottom up.
		for y := 0; y < s.Height; y++ {
			lineIndex := s.Height - 1 - y
			switch {
			case y < fullRows:
				builders[lineIndex].WriteString(full)
			case y == fullRows && partialIdx > 0:
				builders[lineIndex].WriteString(glyphs[partialIdx])
			default:
				builders[lineIndex].WriteByte(' ')
			}
		}
	}

	lines := make([]string, s.Height)
	padWidth := s.Width - minInt(len(sampled), s.Width)
	for i := range builders {
		if padWidth > 0 {
			builders[i].WriteString(strings.Repeat(" ", padWidth))
		}
		lines[i] = builders[i].String()
	}

	// Add axis labels if enabled
	if s.ShowLabels {
		maxLabel := fmt.Sprintf("%6.1f", max)
		minLabel := fmt.Sprintf("%6.1f", min)

		if s.ShowAxis {
			for i := range lines {
				switch i {
				case 0:
					lines[i] = maxLabel + " │" + lines[i]
				case s.Height - 1:
					lines[i] = minLabel + " │" + lines[i]
				default:
					lines[i] = strings.Repeat(" ", len(maxLabel)) + " │" + lines[i]
				}
			}
		}
	}

	return strings.Join(lines, "\n")
}

// sampleData reduces data points to fit the specified width
func sampleData(data []float64, width int) []float64 {
	if width <= 0 {
		return nil
	}
	if len(data) <= width {
		return data
	}

	result := make([]float64, width)
	step := float64(len(data)) / float64(width)

	for i := 0; i < width; i++ {
		start := int(math.Floor(float64(i) * step))
		end := int(math.Floor(float64(i+1) * step))
		if end > len(data) {
			end = len(data)
		}

		// Take the max value in the range for better visibility
		max := data[start]
		for j := start + 1; j < end; j++ {
			if data[j] > max {
				max = data[j]
			}
		}
		result[i] = max
	}

	return result
}

// getMinMax returns the minimum and maximum values in the data
func getMinMax(data []float64) (float64, float64) {
	if len(data) == 0 {
		return 0, 1
	}

	min, max := data[0], data[0]
	for _, v := range data {
		if v < min {
			min = v
		}
		if v > max {
			max = v
		}
	}
	return min, max
}

// MultiSparkline renders multiple sparklines stacked vertically
type MultiSparkline struct {
	Data   [][]float64
	Labels []string
	Width  int
	Colors []string
	Max    float64
	Min    float64
}

// NewMultiSparkline creates a new multi-sparkline widget
func NewMultiSparkline() *MultiSparkline {
	return &MultiSparkline{
		Width: 40,
	}
}

// Render renders multiple sparklines
func (m *MultiSparkline) Render() string {
	if len(m.Data) == 0 {
		return ""
	}

	// Calculate global min/max if not explicitly set. Seed from the first
	// non-empty series, then take the unconditional min/max — comparing
	// against a zero starting value would discard a true global minimum of 0.
	min, max := m.Min, m.Max
	if max == 0 {
		seeded := false
		for _, data := range m.Data {
			if len(data) == 0 {
				continue
			}
			dMin, dMax := getMinMax(data)
			if !seeded {
				min, max, seeded = dMin, dMax, true
				continue
			}
			if dMin < min {
				min = dMin
			}
			if dMax > max {
				max = dMax
			}
		}
	}

	if max == min {
		max = min + 1
	}

	var lines []string

	for i, data := range m.Data {
		spark := &Sparkline{
			Data:      data,
			Width:     m.Width,
			Height:    1,
			Min:       min,
			Max:       max,
			AutoScale: false,
			ShowAxis:  false,
		}

		if i < len(m.Colors) {
			spark.Color = m.Colors[i]
		} else {
			spark.Color = "#88C0D0"
		}

		line := spark.Render()

		// Add label if available
		if i < len(m.Labels) && m.Labels[i] != "" {
			label := lipgloss.NewStyle().Width(12).Align(lipgloss.Right).Render(m.Labels[i] + ":")
			line = label + " " + line
		}

		lines = append(lines, line)
	}

	return strings.Join(lines, "\n")
}

// BarGauge renders a horizontal bar gauge
type BarGauge struct {
	Value       float64
	Max         float64
	Width       int
	ShowValue   bool
	ShowPercent bool
	ColorFunc   func(float64) string
}

// NewBarGauge creates a new bar gauge widget
func NewBarGauge() *BarGauge {
	return &BarGauge{
		Width:     20,
		Max:       100,
		ShowValue: true,
	}
}

// Render renders the bar gauge
func (b *BarGauge) Render() string {
	if b.Width <= 0 {
		return ""
	}
	max := b.Max
	if max <= 0 || math.IsNaN(max) || math.IsInf(max, 0) {
		max = 1
	}
	value := b.Value
	if math.IsNaN(value) || math.IsInf(value, 0) {
		value = 0
	}

	percent := (value / max) * 100
	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}

	filled := int(float64(b.Width) * (percent / 100.0))
	if filled > b.Width {
		filled = b.Width
	}

	// Determine color
	color := "#88C0D0"
	if b.ColorFunc != nil {
		color = b.ColorFunc(value)
	}

	result := renderGaugeSegments(color, filled, b.Width-filled)

	if b.ShowPercent {
		result += fmt.Sprintf(" %5.1f%%", percent)
	} else if b.ShowValue {
		result += fmt.Sprintf(" %6.1f", value)
	}

	return result
}

// MiniGauge renders a compact gauge with value
type MiniGauge struct {
	Value     float64
	Max       float64
	Width     int
	Unit      string
	Color     string
	ShowValue bool
}

// NewMiniGauge creates a new mini gauge
func NewMiniGauge() *MiniGauge {
	return &MiniGauge{
		Width:     15,
		Max:       100,
		Unit:      "",
		Color:     "#88C0D0",
		ShowValue: true,
	}
}

// Render renders the mini gauge
func (m *MiniGauge) Render() string {
	if m.Width <= 0 {
		return ""
	}
	max := m.Max
	if max <= 0 || math.IsNaN(max) || math.IsInf(max, 0) {
		max = 1
	}
	value := m.Value
	if math.IsNaN(value) || math.IsInf(value, 0) {
		value = 0
	}

	percent := (value / max) * 100
	if percent > 100 {
		percent = 100
	}
	if percent < 0 {
		percent = 0
	}

	filled := int(float64(m.Width) * (percent / 100.0))
	if filled > m.Width {
		filled = m.Width
	}

	bar := renderGaugeSegments(m.Color, filled, m.Width-filled)

	if m.ShowValue {
		unit := m.Unit
		if unit != "" {
			unit = " " + unit
		}
		return fmt.Sprintf("%s %6.1f%s", bar, value, unit)
	}

	return bar
}

func renderGaugeSegments(color string, filledCount, emptyCount int) string {
	if filledCount < 0 {
		filledCount = 0
	}
	if emptyCount < 0 {
		emptyCount = 0
	}
	var builder strings.Builder
	if filledCount > 0 {
		builder.WriteString(getBarFilledStyle(color).Render(strings.Repeat("▓", filledCount)))
	}
	if emptyCount > 0 {
		builder.WriteString(barEmptyStyle.Render(strings.Repeat("░", emptyCount)))
	}
	return builder.String()
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
