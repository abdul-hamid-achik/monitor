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

var DefaultSettings = Settings{
	UpdateInterval:      time.Second,
	TemperatureUnit:     "C",
	ShowSystemProcesses: false,
	MaxProcesses:        50,
	MouseEnabled:        true,
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
		return &DefaultSettings, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &DefaultSettings, nil
		}
		return nil, err
	}
	var s Settings
	if err := json.Unmarshal(data, &s); err != nil {
		return &DefaultSettings, nil
	}
	if s.UpdateInterval == 0 {
		s.UpdateInterval = DefaultSettings.UpdateInterval
	}
	if s.TemperatureUnit == "" {
		s.TemperatureUnit = DefaultSettings.TemperatureUnit
	}
	if s.MaxProcesses == 0 {
		s.MaxProcesses = DefaultSettings.MaxProcesses
	}
	return &s, nil
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
