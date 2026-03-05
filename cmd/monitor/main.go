package main

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/monitor/monitor/internal/ui"
)

func main() {
	// Check if terminal supports true color
	if !lipgloss.HasDarkBackground() {
		fmt.Fprintln(os.Stderr, "Warning: This application is designed for dark terminals")
	}

	// Create and run the application
	model := ui.NewModel()
	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),        // Use alternate screen buffer
		tea.WithMouseAllMotion(),   // Enable full mouse support
		tea.WithANSICompressor(),   // Enable ANSI compression for better performance
	)

	if _, err := p.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "Error running monitor: %v\n", err)
		os.Exit(1)
	}
}
