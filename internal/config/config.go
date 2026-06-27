package config

import (
	"encoding/json"
	"os"
	"path/filepath"
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
	if file.TemperatureUnit != nil && *file.TemperatureUnit != "" {
		settings.TemperatureUnit = *file.TemperatureUnit
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
	if file.CPUAlertThreshold != nil && *file.CPUAlertThreshold >= 0 {
		settings.CPUAlertThreshold = *file.CPUAlertThreshold
	}
	if file.MemoryAlertThreshold != nil && *file.MemoryAlertThreshold >= 0 {
		settings.MemoryAlertThreshold = *file.MemoryAlertThreshold
	}
	return &settings, nil
}

func (s *Settings) Save() error {
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
		tmp.Close()
		os.Remove(tmpName)
		return err
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		os.Remove(tmpName)
		return err
	}
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		return err
	}
	return nil
}
