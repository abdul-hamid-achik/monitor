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
}

type settingsFile struct {
	UpdateInterval      *time.Duration `json:"update_interval"`
	TemperatureUnit     *string        `json:"temperature_unit"`
	ShowSystemProcesses *bool          `json:"show_system_processes"`
	MaxProcesses        *int           `json:"max_processes"`
	MouseEnabled        *bool          `json:"mouse_enabled"`
}

var DefaultSettings = Settings{
	UpdateInterval:      time.Second,
	TemperatureUnit:     "C",
	ShowSystemProcesses: false,
	MaxProcesses:        50,
	MouseEnabled:        true,
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
	if file.MaxProcesses != nil && *file.MaxProcesses > 0 {
		settings.MaxProcesses = *file.MaxProcesses
	}
	if file.MouseEnabled != nil {
		settings.MouseEnabled = *file.MouseEnabled
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
	return os.WriteFile(path, data, 0644)
}
