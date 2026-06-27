package v2

import (
	"time"

	tea "charm.land/bubbletea/v2"
)

const settingsRows = 7

// handleSettingsKeys edits the Settings tab. Left/right/h/l are consumed by the
// global tab navigation, so editing uses up/down to select, enter/space to
// cycle a value forward, "-" to cycle back, and "s" to save.
func (m Model) handleSettingsKeys(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.Keystroke() {
	case "up", "k":
		m.settingsCursor = (m.settingsCursor + settingsRows - 1) % settingsRows
	case "down", "j":
		m.settingsCursor = (m.settingsCursor + 1) % settingsRows
	case "enter", " ", "space":
		m.cycleSetting(1)
		m.settingsSaved = false
	case "-", "_":
		m.cycleSetting(-1)
		m.settingsSaved = false
	case "s":
		if m.settings != nil && m.settings.Save() == nil {
			m.settingsSaved = true
		}
	}
	return m, nil
}

// cycleSetting advances the selected setting's value by dir (+1 / -1). It
// mutates the shared *config.Settings; nothing is persisted until "s".
func (m Model) cycleSetting(dir int) {
	s := m.settings
	if s == nil {
		return
	}
	switch m.settingsCursor {
	case 0:
		s.UpdateInterval = cycle(s.UpdateInterval, []time.Duration{
			500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second,
		}, dir)
	case 1:
		if s.TemperatureUnit == "C" {
			s.TemperatureUnit = "F"
		} else {
			s.TemperatureUnit = "C"
		}
	case 2:
		s.ShowSystemProcesses = !s.ShowSystemProcesses
	case 3:
		s.MaxProcesses = cycle(s.MaxProcesses, []int{20, 50, 100, 200}, dir)
	case 4:
		s.MouseEnabled = !s.MouseEnabled
	case 5:
		s.CPUAlertThreshold = cycle(s.CPUAlertThreshold, []float64{0, 50, 70, 80, 90}, dir)
	case 6:
		s.MemoryAlertThreshold = cycle(s.MemoryAlertThreshold, []float64{0, 50, 70, 80, 90}, dir)
	}
}

// cycle returns the option after cur (wrapping) in the direction dir. If cur is
// not among opts, it starts from the first option.
func cycle[T comparable](cur T, opts []T, dir int) T {
	idx := 0
	for i, o := range opts {
		if o == cur {
			idx = i
			break
		}
	}
	n := len(opts)
	return opts[((idx+dir)%n+n)%n]
}
