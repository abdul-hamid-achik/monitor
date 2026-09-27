package tuistudio

import (
	"fmt"
	"time"

	"github.com/abdul-hamid-achik/tuimark"
)

// settingSpec is one row of the Settings tab: its store key and label.
// The cycle order matches internal/ui/studio/settings.go's cycleSetting so
// the two front-ends offer the same values in the same order.
type settingSpec struct{ key, label string }

var settingSpecs = []settingSpec{
	{"update_interval", "update interval"},
	{"temperature_unit", "temperature unit"},
	{"show_system_processes", "system processes"},
	{"max_processes", "max processes"},
	{"mouse_enabled", "mouse"},
	{"cpu_alert_threshold", "cpu alert"},
	{"memory_alert_threshold", "memory alert"},
}

var (
	updateIntervalOptions = []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second, 5 * time.Second}
	maxProcessesOptions   = []int{20, 50, 100, 200}
	thresholdOptions      = []float64{0, 50, 70, 80, 90}
)

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

// settingValue formats one row's current display value. Formatting (the
// leading °, on/off, OFF for a disabled threshold) is host-side derived
// data, never document logic.
func (s *studio) settingValue(key string) string {
	cfg := s.settings
	switch key {
	case "update_interval":
		return cfg.UpdateInterval.String()
	case "temperature_unit":
		return "°" + cfg.TemperatureUnit
	case "show_system_processes":
		return onOff(cfg.ShowSystemProcesses)
	case "max_processes":
		return fmt.Sprintf("%d", cfg.MaxProcesses)
	case "mouse_enabled":
		return onOff(cfg.MouseEnabled)
	case "cpu_alert_threshold":
		return thresholdLabel(cfg.CPUAlertThreshold)
	case "memory_alert_threshold":
		return thresholdLabel(cfg.MemoryAlertThreshold)
	}
	return ""
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func thresholdLabel(v float64) string {
	if v <= 0 {
		return "OFF"
	}
	return fmt.Sprintf("%.0f%%", v)
}

func (s *studio) settingsRows() []map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	rows := make([]map[string]any, len(settingSpecs))
	for i, spec := range settingSpecs {
		rows[i] = map[string]any{
			"key": spec.key, "label": spec.label, "value": s.settingValue(spec.key),
			"dirty": s.settingsDirty[spec.key], "error": false,
		}
	}
	return rows
}

func settingsRowsToAny(rows []map[string]any) []any {
	arr := make([]any, len(rows))
	for i, r := range rows {
		arr[i] = r
	}
	return arr
}

func (s *studio) publishSettings() error {
	return s.ui.Set("settings", settingsRowsToAny(s.settingsRows()))
}

// cycleSetting advances one row's value by dir (+1 / -1), matching
// internal/ui/studio/settings.go's cycleSetting. Nothing reaches disk
// until settings_save; mouse_enabled additionally flips the document's
// live "mouse_enabled" attribute right away, same as flipping it in the
// Bubble Tea studio turns the terminal's own mouse reporting off.
func (s *studio) cycleSetting(key string, dir int) error {
	s.mu.Lock()
	cfg := s.settings
	switch key {
	case "update_interval":
		before := cfg.UpdateInterval
		cfg.UpdateInterval = cycle(cfg.UpdateInterval, updateIntervalOptions, dir)
		if cfg.UpdateInterval != before {
			s.src.SetInterval(cfg.UpdateInterval)
		}
	case "temperature_unit":
		if cfg.TemperatureUnit == "C" {
			cfg.TemperatureUnit = "F"
		} else {
			cfg.TemperatureUnit = "C"
		}
	case "show_system_processes":
		cfg.ShowSystemProcesses = !cfg.ShowSystemProcesses
	case "max_processes":
		cfg.MaxProcesses = cycle(cfg.MaxProcesses, maxProcessesOptions, dir)
	case "mouse_enabled":
		cfg.MouseEnabled = !cfg.MouseEnabled
	case "cpu_alert_threshold":
		cfg.CPUAlertThreshold = cycle(cfg.CPUAlertThreshold, thresholdOptions, dir)
	case "memory_alert_threshold":
		cfg.MemoryAlertThreshold = cycle(cfg.MemoryAlertThreshold, thresholdOptions, dir)
	default:
		s.mu.Unlock()
		return nil
	}
	if s.settingsDirty == nil {
		s.settingsDirty = map[string]bool{}
	}
	s.settingsDirty[key] = true
	mouseEnabled := cfg.MouseEnabled
	unit := cfg.TemperatureUnit
	temp := s.last.Temperature
	s.mu.Unlock()

	errs := []error{s.publishSettings(), s.ui.Set("settings_status", ""), s.ui.Set("settings_saved", false)}
	if key == "mouse_enabled" {
		errs = append(errs, s.ui.Set("mouse_enabled", mouseEnabled))
	}
	if key == "temperature_unit" {
		// The Bubble Tea studio formats temperatures at render time, so
		// the new unit shows at once; republish the last reading in it.
		errs = append(errs, s.ui.Set("thermal", thermalView(temp, unit)))
	}
	return joinErrs(errs)
}

func (s *studio) settingsHandlers() map[string]tuimark.Handler {
	hs := map[string]tuimark.Handler{}
	on := func(name string, h tuimark.Handler) { hs[name] = h }

	on("setting_next", func(ev tuimark.Event) error {
		key, _ := ev.Keys["s"].(string)
		return s.cycleSetting(key, 1)
	})
	on("setting_prev", func(ev tuimark.Event) error {
		key, _ := ev.Keys["s"].(string)
		return s.cycleSetting(key, -1)
	})
	on("settings_save", func(tuimark.Event) error { return s.saveSettings() })
	return hs
}

func (s *studio) saveSettings() error {
	s.mu.Lock()
	n := 0
	for _, dirty := range s.settingsDirty {
		if dirty {
			n++
		}
	}
	cfg := s.settings
	s.mu.Unlock()

	if err := s.settingsStore.Save(cfg); err != nil {
		return joinErrs([]error{
			s.ui.Set("settings_status", err.Error()),
			s.ui.Set("settings_saved", false),
			s.ui.Set("settings_failed", true),
		})
	}
	s.mu.Lock()
	s.settingsDirty = map[string]bool{}
	s.mu.Unlock()
	return joinErrs([]error{
		s.publishSettings(),
		s.ui.Set("settings_status", fmt.Sprintf("saved: %d changed", n)),
		s.ui.Set("settings_saved", true),
		s.ui.Set("settings_failed", false),
	})
}
