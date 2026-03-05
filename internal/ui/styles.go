package ui

import (
	"github.com/charmbracelet/lipgloss"
)

// Nord Theme Colors (Dark Only)
// Polar Night - Background colors
var (
	Nord0 = "#2E3440" // Base background
	Nord1 = "#3B4252" // Secondary background
	Nord2 = "#434C5E" // Highlight/background elements
	Nord3 = "#4C566A" // Borders, separators
)

// Snow Storm - Foreground colors
var (
	Nord4 = "#D8DEE9" // Main text
	Nord5 = "#E5E9F0" // Bright text
	Nord6 = "#ECEFF4" // Bold/important text
)

// Frost - Accent colors (cool)
var (
	Nord7  = "#8FBCBB" // Cyan
	Nord8  = "#88C0D0" // Blue
	Nord9  = "#81A1C1" // Light blue
	Nord10 = "#5E81AC" // Dark blue
)

// Aurora - Accent colors (warm)
var (
	Nord11 = "#BF616A" // Red (errors, critical, high CPU)
	Nord12 = "#D08770" // Orange (warnings, medium load)
	Nord13 = "#EBCB8B" // Yellow (caution)
	Nord14 = "#A3BE8C" // Green (normal, safe, low load)
	Nord15 = "#B48EAD" // Purple (special indicators)
)

// Common Styles
var (
	// Base styles
	BaseStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Nord4)).
			Background(lipgloss.Color(Nord0))

	// Title styles - HIGH VISIBILITY
	TitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(Nord0)).
			Background(lipgloss.Color(Nord8)).
			Padding(0, 1)

	// Header styles
	HeaderStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(Nord8))

	// Panel styles - VISIBLE BORDERS
	PanelStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color(Nord3)).
			Padding(1, 2).
			Margin(0, 0)

	PanelTitleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color(Nord8)).
			MarginBottom(1)

	// Status styles
	StatusNormalStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(Nord14)) // Green

	StatusWarningStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(Nord13)) // Yellow

	StatusCriticalStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(Nord11)) // Red

	// Selection styles
	SelectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Nord0)).
			Background(lipgloss.Color(Nord8)).
			Bold(true)

	// Tab styles - HIGH CONTRAST
	TabActiveStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Nord0)).
			Background(lipgloss.Color(Nord8)).
			Bold(true).
			Padding(0, 1)

	TabInactiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(Nord4)).
				Background(lipgloss.Color(Nord1)).
				Padding(0, 1)

	// Button styles
	ButtonStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Nord4)).
			Background(lipgloss.Color(Nord2)).
			Padding(0, 1)

	ButtonActiveStyle = lipgloss.NewStyle().
				Foreground(lipgloss.Color(Nord0)).
				Background(lipgloss.Color(Nord8)).
				Padding(0, 1)

	// Help text style
	HelpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color(Nord9)).
			Italic(true)

	// Border styles
	ActiveBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.ThickBorder()).
				BorderForeground(lipgloss.Color(Nord8))

	InactiveBorderStyle = lipgloss.NewStyle().
				Border(lipgloss.NormalBorder()).
				BorderForeground(lipgloss.Color(Nord3))
)

// CPU Load Color returns color based on CPU usage percentage
func CPULoadColor(percentage float64) string {
	if percentage >= 80 {
		return Nord11 // Red - Critical
	} else if percentage >= 50 {
		return Nord12 // Orange - High
	} else if percentage >= 30 {
		return Nord13 // Yellow - Medium
	}
	return Nord14 // Green - Normal
}

// Memory Load Color returns color based on memory usage percentage
func MemoryLoadColor(percentage float64) string {
	if percentage >= 90 {
		return Nord11 // Red - Critical
	} else if percentage >= 70 {
		return Nord12 // Orange - High
	} else if percentage >= 50 {
		return Nord13 // Yellow - Medium
	}
	return Nord14 // Green - Normal
}

// Temperature Color returns color based on temperature in Celsius
func TemperatureColor(tempCelsius float64) string {
	if tempCelsius >= 85 {
		return Nord11 // Red - Critical
	} else if tempCelsius >= 70 {
		return Nord12 // Orange - High
	} else if tempCelsius >= 60 {
		return Nord13 // Yellow - Warm
	}
	return Nord14 // Green - Normal
}

// CreateProgressbar creates a styled progress bar string
func CreateProgressbar(percentage float64, width int, colorFunc func(float64) string) string {
	filled := int(float64(width) * (percentage / 100.0))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}

	color := colorFunc(percentage)
	filledChar := lipgloss.NewStyle().Foreground(lipgloss.Color(color)).Render("▓")
	emptyChar := lipgloss.NewStyle().Foreground(lipgloss.Color(Nord3)).Render("░")

	bar := ""
	for i := 0; i < width; i++ {
		if i < filled {
			bar += filledChar
		} else {
			bar += emptyChar
		}
	}

	return bar
}

// CreateSimpleProgressbar creates a simple progress bar without styling
func CreateSimpleProgressbar(percentage float64, width int) string {
	return CreateProgressbar(percentage, width, MemoryLoadColor)
}
