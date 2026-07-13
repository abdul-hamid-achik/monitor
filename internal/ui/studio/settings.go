package studio

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
		before := m.currentUpdateInterval()
		m.cycleSetting(1)
		m.applyCollectorInterval(before)
		m.settingsSaved = false
		m.settingsDirty = true
		m.settingsErr = ""
	case "-", "_":
		before := m.currentUpdateInterval()
		m.cycleSetting(-1)
		m.applyCollectorInterval(before)
		m.settingsSaved = false
		m.settingsDirty = true
		m.settingsErr = ""
	case "s":
		if m.settings != nil {
			if err := m.settings.Save(); err != nil {
				m.settingsSaved = false
				m.settingsErr = err.Error()
			} else {
				m.settingsSaved = true
				m.settingsDirty = false
				m.settingsErr = ""
			}
		}
	}
	return m, nil
}

func (m Model) currentUpdateInterval() time.Duration {
	if m.settings == nil {
		return 0
	}
	return m.settings.UpdateInterval
}

func (m Model) applyCollectorInterval(before time.Duration) {
	if m.settingsCursor == 0 && m.settings != nil && m.collector != nil && m.settings.UpdateInterval != before {
		m.collector.SetInterval(m.settings.UpdateInterval)
	}
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
