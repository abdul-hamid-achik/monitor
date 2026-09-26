package studio

import (
	"image/color"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

func TestStudioThemeHasDistinctLightAndDarkRoles(t *testing.T) {
	dark := newStudioTheme(true)
	light := newStudioTheme(false)
	for name, pair := range map[string][2]color.Color{
		"background": {dark.Background, light.Background},
		"text":       {dark.Text, light.Text},
		"muted":      {dark.Muted, light.Muted},
		"accent":     {dark.Accent, light.Accent},
		"border":     {dark.Border, light.Border},
	} {
		if sameColor(pair[0], pair[1]) {
			t.Errorf("%s role must adapt between dark and light terminals", name)
		}
	}
}

func TestThemeHexRoundTripsRoles(t *testing.T) {
	for _, dark := range []bool{true, false} {
		th := newStudioTheme(dark)
		for name, role := range map[string]color.Color{
			"accent": th.Accent, "good": th.Good,
			"warning": th.Warning, "critical": th.Critical,
		} {
			got := th.hex(role)
			if len(got) != 7 || got[0] != '#' {
				t.Errorf("dark=%v %s hex = %q, want #rrggbb", dark, name, got)
			}
		}
	}
}

func TestThemeGaugeHexFollowsThresholds(t *testing.T) {
	th := newStudioTheme(true)
	cases := []struct {
		v, warn, crit float64
		role          color.Color
	}{
		{95, 70, 90, th.Critical},
		{90, 70, 90, th.Critical},
		{89, 70, 90, th.Warning},
		{70, 70, 90, th.Warning},
		{69, 70, 90, th.Good},
		{0, 70, 90, th.Good},
	}
	for _, c := range cases {
		if got, want := th.gaugeHex(c.v, c.warn, c.crit), th.hex(c.role); got != want {
			t.Errorf("gaugeHex(%v, %v, %v) = %s, want %s", c.v, c.warn, c.crit, got, want)
		}
	}
}

func TestBackgroundColorMessageRebuildsStudioStyles(t *testing.T) {
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	t.Cleanup(m.cancel)
	darkText := m.theme.Text

	updated, _ := m.Update(tea.BackgroundColorMsg{Color: color.RGBA{R: 255, G: 255, B: 255, A: 255}})
	m = updated.(Model)
	if m.darkBackground {
		t.Fatal("a white terminal background should select the light palette")
	}
	if sameColor(darkText, m.theme.Text) {
		t.Fatal("background response did not rebuild semantic styles")
	}
}

func TestShellHeaderFitsTargetTerminalSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{120, 40}, {80, 24}, {60, 20}} {
		m := operationalOverviewFixture(t, size.width, size.height)
		if got := lipgloss.Height(m.renderHeader()); got != 3 {
			t.Fatalf("%dx%d header height = %d, want 3 (brand, tabs, rule)", size.width, size.height, got)
		}
		frame := m.View().Content
		if got := lipgloss.Width(frame); got > size.width {
			t.Fatalf("%dx%d frame width = %d", size.width, size.height, got)
		}
		if got := lipgloss.Height(frame); got != size.height {
			t.Fatalf("%dx%d frame height = %d", size.width, size.height, got)
		}
	}
}

func sameColor(a, b color.Color) bool {
	ar, ag, ab, aa := a.RGBA()
	br, bg, bb, ba := b.RGBA()
	return ar == br && ag == bg && ab == bb && aa == ba
}

func TestPanelLiftsTitleIntoTopBorder(t *testing.T) {
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	t.Cleanup(m.cancel)
	for _, tc := range []struct {
		name  string
		width int
		body  string
		top   string
		lines int
	}{
		{"title and blank dropped", 30, m.titleStyle.Render(" CPU ") + "\n\n  82.3%", "╭─ CPU ─", 3},
		{"title only", 30, m.titleStyle.Render(" Swap "), "╭─ Swap ─", 3},
		{"long title truncated", 14, m.titleStyle.Render(" Per-Core Usage History "), "╭─ Per-Cor… ─", 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := m.panel(tc.width, tc.body)
			lines := strings.Split(ansi.Strip(got), "\n")
			if !strings.HasPrefix(lines[0], tc.top) || !strings.HasSuffix(lines[0], "╮") {
				t.Fatalf("top border = %q, want prefix %q and a ╮ corner", lines[0], tc.top)
			}
			if len(lines) != tc.lines {
				t.Fatalf("panel has %d lines, want %d:\n%s", len(lines), tc.lines, strings.Join(lines, "\n"))
			}
			for i, line := range strings.Split(got, "\n") {
				if w := lipgloss.Width(line); w != tc.width {
					t.Fatalf("line %d width = %d, want %d", i, w, tc.width)
				}
			}
		})
	}
}
