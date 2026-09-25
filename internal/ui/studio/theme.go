package studio

import (
	"fmt"
	"image/color"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"
)

// studioTheme gives every visual state a semantic role. The palette is
// selected after Bubble Tea reports the terminal background, with the dark
// variant used as a safe fallback during startup and in non-interactive tests.
//
// Both variants follow the docs site's own tokens
// (docs/.vitepress/theme/custom.css: --vp-c-bg/Text/brand, flattened
// --vp-c-divider for Border) and the TerminalMockup component's greens,
// ambers, and reds, so Studio reads as the same product as the website.
// Accent is the brand red in both modes -- like the site's own primary
// buttons -- which makes it a close neighbor of Critical by design; the
// two never share a role (Accent fills selections/titles, Critical only
// ever paints alert text and the kill dialog's border).
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
			Background: lipgloss.Color("#11161D"),
			Surface:    lipgloss.Color("#191F28"),
			SurfaceAlt: lipgloss.Color("#171D25"),
			Text:       lipgloss.Color("#F1F3F6"),
			Muted:      lipgloss.Color("#A6AEBA"),
			Accent:     lipgloss.Color("#FF8A80"),
			Border:     lipgloss.Color("#2C3137"),
			Good:       lipgloss.Color("#62B891"),
			Warning:    lipgloss.Color("#D6A94F"),
			Critical:   lipgloss.Color("#FF756D"),
			SelectedFG: lipgloss.Color("#20252D"),
		}
	}
	return studioTheme{
		Background: lipgloss.Color("#FBFAF8"),
		Surface:    lipgloss.Color("#FFFFFF"),
		SurfaceAlt: lipgloss.Color("#F6F4F1"),
		Text:       lipgloss.Color("#20252D"),
		Muted:      lipgloss.Color("#5D6570"),
		Accent:     lipgloss.Color("#B63838"),
		Border:     lipgloss.Color("#E1E0E0"),
		Good:       lipgloss.Color("#3C7658"),
		Warning:    lipgloss.Color("#8A6116"),
		Critical:   lipgloss.Color("#C02828"),
		SelectedFG: lipgloss.Color("#FFFFFF"),
	}
}

// hex renders a theme role as a "#rrggbb" string for the widgets that take
// plain color strings (sparklines, gauges, history series) instead of
// lipgloss styles, so those paint from the active palette too.
func (t studioTheme) hex(c color.Color) string {
	r, g, b, _ := c.RGBA()
	return fmt.Sprintf("#%02X%02X%02X", uint8(r>>8), uint8(g>>8), uint8(b>>8))
}

// gaugeHex returns the Critical/Warning/Good hex for a 0-100 gauge value,
// so bars follow the active palette's semantic roles instead of fixed
// hexes that only suit one terminal background.
func (t studioTheme) gaugeHex(v, warnAt, critAt float64) string {
	switch {
	case v >= critAt:
		return t.hex(t.Critical)
	case v >= warnAt:
		return t.hex(t.Warning)
	default:
		return t.hex(t.Good)
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
