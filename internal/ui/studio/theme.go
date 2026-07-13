package studio

import (
	"image/color"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"
)

// studioTheme gives every visual state a semantic role. The palette is
// selected after Bubble Tea reports the terminal background, with the dark
// variant used as a safe fallback during startup and in non-interactive tests.
type studioTheme struct {
	Background color.Color
	Surface    color.Color
	SurfaceAlt color.Color
	Text       color.Color
	Muted      color.Color
	Accent     color.Color
	Border     color.Color
	Good       color.Color
	Warning    color.Color
	Critical   color.Color
	SelectedFG color.Color
}

func newStudioTheme(dark bool) studioTheme {
	if dark {
		return studioTheme{
			Background: lipgloss.Color("#1D212B"),
			Surface:    lipgloss.Color("#242A35"),
			SurfaceAlt: lipgloss.Color("#2E3440"),
			Text:       lipgloss.Color("#ECEFF4"),
			Muted:      lipgloss.Color("#AAB4C3"),
			Accent:     lipgloss.Color("#88C0D0"),
			Border:     lipgloss.Color("#607089"),
			Good:       lipgloss.Color("#A3BE8C"),
			Warning:    lipgloss.Color("#EBCB8B"),
			Critical:   lipgloss.Color("#E78284"),
			SelectedFG: lipgloss.Color("#1D212B"),
		}
	}
	return studioTheme{
		Background: lipgloss.Color("#F7F8FA"),
		Surface:    lipgloss.Color("#FFFFFF"),
		SurfaceAlt: lipgloss.Color("#E8EDF3"),
		Text:       lipgloss.Color("#222A35"),
		Muted:      lipgloss.Color("#526074"),
		Accent:     lipgloss.Color("#24677C"),
		Border:     lipgloss.Color("#8290A3"),
		Good:       lipgloss.Color("#3F713D"),
		Warning:    lipgloss.Color("#765800"),
		Critical:   lipgloss.Color("#A33E48"),
		SelectedFG: lipgloss.Color("#FFFFFF"),
	}
}

func (m *Model) applyTheme(dark bool) {
	m.darkBackground = dark
	m.theme = newStudioTheme(dark)
	m.titleStyle = lipgloss.NewStyle().Bold(true).Foreground(m.theme.Accent)
	m.panelStyle = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(m.theme.Border).
		Foreground(m.theme.Text).
		Padding(0, 1)
	m.statusStyle = lipgloss.NewStyle().
		Foreground(m.theme.Text).
		Background(m.theme.SurfaceAlt)
	m.tabActive = lipgloss.NewStyle().
		Bold(true).
		Foreground(m.theme.SelectedFG).
		Background(m.theme.Accent).
		Padding(0, 1)
	m.tabInactive = lipgloss.NewStyle().
		Foreground(m.theme.Muted).
		Padding(0, 1)

	if m.processTable != nil {
		styles := table.DefaultStyles()
		styles.Header = styles.Header.Bold(true).Foreground(m.theme.Accent)
		styles.Selected = styles.Selected.
			Foreground(m.theme.SelectedFG).
			Background(m.theme.Accent).
			Bold(true)
		m.processTable.SetStyles(styles)
	}
}
