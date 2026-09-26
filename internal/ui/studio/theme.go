package studio

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/bubbles/v2/table"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/abdul-hamid-achik/monitor/internal/widgets"
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
			Border:     lipgloss.Color("#3A414B"),
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
		Border:     lipgloss.Color("#D9D6D1"),
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
		Foreground(m.theme.Muted)
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

// panel renders body inside the Studio frame and lifts its first line -- by
// convention the panel title -- into the top border:
//
//	╭─ Per-Core Usage ─────────╮
//	│ ...                      │
//
// It is the titled-frame motif the docs site draws around its terminal
// panels, so the TUI and the website share one visual vocabulary. A blank
// line directly under the title is dropped: the border already separates
// them.
func (m Model) panel(width int, body string) string {
	title, rest, _ := strings.Cut(body, "\n")
	rest = strings.TrimPrefix(rest, "\n")
	frame := m.panelStyle.Width(width).BorderTop(false).Render(rest)
	inner := lipgloss.Width(frame) - 2
	if inner < 1 {
		return m.panelStyle.Width(width).Render(body)
	}
	border := lipgloss.NewStyle().Foreground(m.theme.Border)
	label := strings.TrimSpace(ansi.Strip(title))
	if label == "" {
		return border.Render("╭"+strings.Repeat("─", inner)+"╮") + "\n" + frame
	}
	label = m.titleStyle.Render(ansi.Truncate(" "+label+" ", maxInt(0, inner-2), "… "))
	fill := maxInt(0, inner-1-lipgloss.Width(label))
	top := border.Render("╭─") + label + border.Render(strings.Repeat("─", fill)+"╮")
	return top + "\n" + frame
}

// muted renders secondary text -- labels, units, hints -- in the Muted role,
// the site's --vp-c-text-2 pairing of dim label and bright value.
func (m Model) muted(s string) string {
	return lipgloss.NewStyle().Foreground(m.theme.Muted).Render(s)
}

// kv renders one "label  value" row with the label muted and padded to
// width, indented like the rest of a panel body.
func (m Model) kv(label string, width int, value string) string {
	if pad := width - lipgloss.Width(label); pad > 0 {
		label += strings.Repeat(" ", pad)
	}
	return "  " + m.muted(label) + " " + value
}

// gauge renders a bare heavy-rule bar coloured by the Good/Warning/Critical
// thresholds, with the unfilled track in the frame Border colour.
func (m Model) gauge(value float64, width int, warnAt, critAt float64) string {
	bar := widgets.NewBarGauge()
	bar.Value = value
	bar.Width = width
	bar.ShowValue = false
	bar.ColorFunc = func(v float64) string { return m.theme.gaugeHex(v, warnAt, critAt) }
	bar.TrackColor = m.theme.hex(m.theme.Border)
	return bar.Render()
}

// bigValue renders a panel's headline figure: bold, in the Text role.
func (m Model) bigValue(s string) string {
	return lipgloss.NewStyle().Bold(true).Foreground(m.theme.Text).Render(s)
}

// statLine renders "label value" pairs on one indented row (or one per row
// when narrow), labels muted like the site's stat captions.
func (m Model) statLine(narrow bool, pairs ...[2]string) string {
	parts := make([]string, 0, len(pairs))
	for _, p := range pairs {
		parts = append(parts, m.muted(p[0])+" "+p[1])
	}
	if narrow {
		return "  " + strings.Join(parts, "\n  ")
	}
	return "  " + strings.Join(parts, "    ")
}

// indentLines prefixes every line of a multi-line block.
func indentLines(block, prefix string) string {
	return prefix + strings.ReplaceAll(block, "\n", "\n"+prefix)
}
