package studio

import (
	"image/color"
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
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

func TestTwoRowShellFitsTargetTerminalSizes(t *testing.T) {
	for _, size := range []struct{ width, height int }{{120, 40}, {80, 24}, {60, 20}} {
		m := operationalOverviewFixture(t, size.width, size.height)
		if got := lipgloss.Height(m.renderHeader()); got != 2 {
			t.Fatalf("%dx%d header height = %d, want 2", size.width, size.height, got)
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
