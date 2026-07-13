package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Settings struct {
	UpdateInterval      time.Duration `json:"update_interval"`
	TemperatureUnit     string        `json:"temperature_unit"`
	ShowSystemProcesses bool          `json:"show_system_processes"`
	MaxProcesses        int           `json:"max_processes"`
	MouseEnabled        bool          `json:"mouse_enabled"`
	// Alert thresholds (0 = disabled)
	CPUAlertThreshold    float64 `json:"cpu_alert_threshold"`
	MemoryAlertThreshold float64 `json:"memory_alert_threshold"`
}

type settingsFile struct {
	UpdateInterval       *time.Duration `json:"update_interval"`
	TemperatureUnit      *string        `json:"temperature_unit"`
	ShowSystemProcesses  *bool          `json:"show_system_processes"`
	MaxProcesses         *int           `json:"max_processes"`
	MouseEnabled         *bool          `json:"mouse_enabled"`
	CPUAlertThreshold    *float64       `json:"cpu_alert_threshold"`
	MemoryAlertThreshold *float64       `json:"memory_alert_threshold"`
}

var DefaultSettings = Settings{
	UpdateInterval:       time.Second,
	TemperatureUnit:      "C",
	ShowSystemProcesses:  false,
	MaxProcesses:         50,
	MouseEnabled:         true,
	CPUAlertThreshold:    0, // Disabled by default
	MemoryAlertThreshold: 0, // Disabled by default
}

func Default() *Settings {
	settings := DefaultSettings
	return &settings
}

func configDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".config", "monitor")
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

func configPath() (string, error) {
	dir, err := configDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.json"), nil
}

// Path returns the absolute path of Monitor's settings file. It creates the
// parent directory just like Load and Save, so callers can safely present the
// path as a location the user may edit or watch.
func Path() (string, error) {
	return configPath()
}

// Validate checks settings before they are persisted. Load remains tolerant
// of older or partially invalid files by applying defaults field-by-field;
// Save is strict so the CLI and TUI never write values they cannot read back.
func (s Settings) Validate() error {
	if s.UpdateInterval <= 0 {
		return fmt.Errorf("update_interval must be greater than zero")
	}
	unit := strings.ToUpper(strings.TrimSpace(s.TemperatureUnit))
	if unit != "C" && unit != "F" {
		return fmt.Errorf("temperature_unit must be C or F")
	}
	if s.MaxProcesses < 1 || s.MaxProcesses > 1000 {
		return fmt.Errorf("max_processes must be between 1 and 1000")
	}
	if math.IsNaN(s.CPUAlertThreshold) || math.IsInf(s.CPUAlertThreshold, 0) ||
		s.CPUAlertThreshold < 0 || s.CPUAlertThreshold > 100 {
		return fmt.Errorf("cpu_alert_threshold must be between 0 and 100")
	}
	if math.IsNaN(s.MemoryAlertThreshold) || math.IsInf(s.MemoryAlertThreshold, 0) ||
		s.MemoryAlertThreshold < 0 || s.MemoryAlertThreshold > 100 {
		return fmt.Errorf("memory_alert_threshold must be between 0 and 100")
	}
	return nil
}

func Load() (*Settings, error) {
	path, err := configPath()
	if err != nil {
		return Default(), nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Default(), nil
		}
		return nil, err
	}
	var file settingsFile
	if err := json.Unmarshal(data, &file); err != nil {
		return Default(), nil
	}

	settings := DefaultSettings
	if file.UpdateInterval != nil && *file.UpdateInterval > 0 {
		settings.UpdateInterval = *file.UpdateInterval
	}
	if file.TemperatureUnit != nil {
		unit := strings.ToUpper(strings.TrimSpace(*file.TemperatureUnit))
		if unit == "C" || unit == "F" {
			settings.TemperatureUnit = unit
		}
	}
	if file.ShowSystemProcesses != nil {
		settings.ShowSystemProcesses = *file.ShowSystemProcesses
	}
	if file.MaxProcesses != nil && *file.MaxProcesses > 0 && *file.MaxProcesses <= 1000 {
		settings.MaxProcesses = *file.MaxProcesses
	}
	if file.MouseEnabled != nil {
		settings.MouseEnabled = *file.MouseEnabled
	}
	if file.CPUAlertThreshold != nil && !math.IsNaN(*file.CPUAlertThreshold) &&
		!math.IsInf(*file.CPUAlertThreshold, 0) && *file.CPUAlertThreshold >= 0 && *file.CPUAlertThreshold <= 100 {
		settings.CPUAlertThreshold = *file.CPUAlertThreshold
	}
	if file.MemoryAlertThreshold != nil && !math.IsNaN(*file.MemoryAlertThreshold) &&
		!math.IsInf(*file.MemoryAlertThreshold, 0) && *file.MemoryAlertThreshold >= 0 && *file.MemoryAlertThreshold <= 100 {
		settings.MemoryAlertThreshold = *file.MemoryAlertThreshold
	}
	return &settings, nil
}

func (s *Settings) Save() error {
	if s == nil {
		return fmt.Errorf("settings cannot be nil")
	}
	// Normalize the only case-insensitive enum before serializing it.
	s.TemperatureUnit = strings.ToUpper(strings.TrimSpace(s.TemperatureUnit))
	if err := s.Validate(); err != nil {
		return err
	}
	path, err := configPath()
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Write to a temp file in the same directory, then rename. os.Rename is
	// atomic, so a crash or full disk mid-write can't leave a truncated
	// config.json that Load would silently discard in favor of defaults.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.json")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(data); err != nil {
		return errors.Join(err, tmp.Close(), os.Remove(tmpName))
	}
	if err := tmp.Close(); err != nil {
		return errors.Join(err, os.Remove(tmpName))
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		return errors.Join(err, os.Remove(tmpName))
	}
	if err := os.Rename(tmpName, path); err != nil {
		return errors.Join(err, os.Remove(tmpName))
	}
	return nil
}
