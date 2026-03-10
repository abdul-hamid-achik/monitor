package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultReturnsIndependentCopies(t *testing.T) {
	first := Default()
	second := Default()

	first.MouseEnabled = false

	if !second.MouseEnabled {
		t.Fatal("Expected Default to return independent copies")
	}
}

func TestLoadAppliesDefaultsForMissingFields(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	configDir := filepath.Join(home, ".config", "monitor")
	if err := os.MkdirAll(configDir, 0755); err != nil {
		t.Fatalf("Failed to create config dir: %v", err)
	}
	configPath := filepath.Join(configDir, "config.json")
	if err := os.WriteFile(configPath, []byte(`{"temperature_unit":"F"}`), 0644); err != nil {
		t.Fatalf("Failed to write config file: %v", err)
	}

	settings, err := Load()
	if err != nil {
		t.Fatalf("Load returned error: %v", err)
	}

	if settings.TemperatureUnit != "F" {
		t.Fatalf("Expected temperature unit F, got %s", settings.TemperatureUnit)
	}
	if !settings.MouseEnabled {
		t.Fatal("Expected missing mouse_enabled to default to true")
	}
	if settings.MaxProcesses != DefaultSettings.MaxProcesses {
		t.Fatalf("Expected MaxProcesses default %d, got %d", DefaultSettings.MaxProcesses, settings.MaxProcesses)
	}
}
