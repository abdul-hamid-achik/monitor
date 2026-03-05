package widgets

import (
	"fmt"
	"math"
	"strings"

	"github.com/charmbracelet/lipgloss"
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
	barFilledStyleCache = make(map[string]lipgloss.Style)
	barEmptyStyle       = lipgloss.NewStyle().Foreground(lipgloss.Color("#434C5E")) // Nord2
	defaultSparkColor   = "#88C0D0"                                                  // Nord8
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
	if len(s.Data) == 0 {
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

	// Create the graph lines
	lines := make([]string, s.Height)
	for i := range lines {
		lines[i] = ""
	}

	// Sample data to fit width
	sampled := sampleData(s.Data, s.Width)

	// Generate sparkline characters
	for x := 0; x < len(sampled) && x < s.Width; x++ {
		value := sampled[x]
		normalized := (value - min) / (max - min)
		
		// Map to character index (0-8)
		charIndex := int(normalized * float64(len(SparklineChars)-1))
		if charIndex < 0 {
			charIndex = 0
		}
		if charIndex >= len(SparklineChars) {
			charIndex = len(SparklineChars) - 1
		}

		char := SparklineChars[charIndex]
		styledChar := getSparklineStyle(s.Color).Render(char)

		// Fill from bottom up
		for y := 0; y < s.Height; y++ {
			lineIndex := s.Height - 1 - y
			if y < charIndex {
				lines[lineIndex] += styledChar
			} else {
				lines[lineIndex] += " "
			}
		}
	}

	// Pad lines to width
	for i := range lines {
		for len(lines[i]) < s.Width {
			lines[i] += " "
		}
	}

	// Add axis labels if enabled
	if s.ShowLabels {
		maxLabel := fmt.Sprintf("%6.1f", max)
		minLabel := fmt.Sprintf("%6.1f", min)
		
		if s.ShowAxis {
			for i := range lines {
				if i == 0 {
					lines[i] = maxLabel + " │" + lines[i]
				} else if i == s.Height-1 {
					lines[i] = minLabel + " │" + lines[i]
				} else {
					lines[i] = strings.Repeat(" ", len(maxLabel)) + " │" + lines[i]
				}
			}
		}
	}

	return strings.Join(lines, "\n")
}

// sampleData reduces data points to fit the specified width
func sampleData(data []float64, width int) []float64 {
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
	Data    [][]float64
	Labels  []string
	Width   int
	Colors  []string
	Max     float64
	Min     float64
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

	// Calculate global min/max if not set
	min, max := m.Min, m.Max
	if max == 0 {
		for _, data := range m.Data {
			dMin, dMax := getMinMax(data)
			if dMin < min || min == 0 {
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
	Value      float64
	Max        float64
	Width      int
	ShowValue  bool
	ShowPercent bool
	ColorFunc  func(float64) string
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
	if b.Max == 0 {
		b.Max = 1
	}

	percent := (b.Value / b.Max) * 100
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
		color = b.ColorFunc(b.Value)
	}

	var bar strings.Builder
	for i := 0; i < b.Width; i++ {
		if i < filled {
			bar.WriteString(getBarFilledStyle(color).Render("▓"))
		} else {
			bar.WriteString(barEmptyStyle.Render("░"))
		}
	}

	result := bar.String()

	if b.ShowPercent {
		result += fmt.Sprintf(" %5.1f%%", percent)
	} else if b.ShowValue {
		result += fmt.Sprintf(" %6.1f", b.Value)
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
	if m.Max == 0 {
		m.Max = 1
	}

	percent := (m.Value / m.Max) * 100
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

	var bar strings.Builder
	for i := 0; i < m.Width; i++ {
		if i < filled {
			bar.WriteString(getBarFilledStyle(m.Color).Render("▓"))
		} else {
			bar.WriteString(barEmptyStyle.Render("░"))
		}
	}

	if m.ShowValue {
		unit := m.Unit
		if unit != "" {
			unit = " " + unit
		}
		return fmt.Sprintf("%s %6.1f%s", bar.String(), m.Value, unit)
	}

	return bar.String()
}
