package studio

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
)

// renderHelp provides a context-aware keyboard reference. It replaces the
// underlying frame while open so the text remains readable in small terminals
// and destructive process actions cannot be triggered through the dialog.
func (m Model) renderHelp() string {
	keyHelp := func(binding, description string) string {
		keyStyle := lipgloss.NewStyle().Foreground(m.theme.Warning).Bold(true)
		return fmt.Sprintf("  %-22s %s", keyStyle.Render(binding), description)
	}
	lines := []string{
		m.titleStyle.Render(" Keyboard Help "),
		"",
		keyHelp("← / h / shift+tab", "previous tab"),
		keyHelp("→ / l / tab", "next tab"),
		keyHelp("1 … 9", "jump directly to a tab"),
		keyHelp("p", "pause or resume live samples"),
		keyHelp("r", "refresh now"),
		keyHelp("q / ctrl+c", "quit Monitor"),
	}

	switch m.view {
	case viewProcesses:
		lines = append(lines,
			"",
			m.titleStyle.Render(" Processes "),
			keyHelp("↑/↓ or j/k", "move through rows"),
			keyHelp("enter", "open process diagnostics"),
			keyHelp("space", "select or deselect a process"),
			keyHelp("/", "edit the process filter"),
			keyHelp("c / m", "sort by CPU or memory"),
			keyHelp("ctrl+a / ctrl+d", "select all / clear selection"),
			keyHelp("K / X", "terminate / force-kill (confirms first)"),
		)
		if m.processDetailVisible {
			lines = append(lines,
				"",
				m.titleStyle.Render(" Process Detail "),
				keyHelp("r", "refresh the inspected PID"),
				keyHelp("enter / Esc / q", "close process detail"),
			)
		}
	case viewSettings:
		lines = append(lines,
			"",
			m.titleStyle.Render(" Settings "),
			keyHelp("↑/↓ or j/k", "choose a setting"),
			keyHelp("enter / space", "next value"),
			keyHelp("-", "previous value"),
			keyHelp("s", "save changes"),
		)
	case viewTrends:
		lines = append(lines, "", keyHelp("r", "reload recorded history now"))
	}

	lines = append(lines, "", lipgloss.NewStyle().Foreground(m.theme.Good).Render(" Press ? or Esc to close "))
	width := m.width - 8
	if width > 72 {
		width = 72
	}
	if width < 30 {
		width = 30
	}
	dialog := lipgloss.NewStyle().
		Border(lipgloss.DoubleBorder()).
		BorderForeground(m.theme.Accent).
		Padding(1, 2).
		Width(width).
		Render(strings.Join(lines, "\n"))
	placeWidth, placeHeight := m.width, m.height
	if placeWidth < 1 {
		placeWidth = width
	}
	if placeHeight < 1 {
		placeHeight = len(lines) + 4
	}
	return lipgloss.Place(placeWidth, placeHeight, lipgloss.Center, lipgloss.Center, dialog)
}
