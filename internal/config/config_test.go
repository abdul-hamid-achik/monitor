package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	s := Default()
	s.TemperatureUnit = "F"
	s.MaxProcesses = 200
	s.ShowSystemProcesses = true
	s.MouseEnabled = false
	s.CPUAlertThreshold = 85
	s.MemoryAlertThreshold = 90
	s.UpdateInterval = 2 * time.Second
	if err := s.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if *got != *s {
		t.Errorf("round-trip mismatch:\n got  %+v\n want %+v", *got, *s)
	}
}

func TestLoadMalformedJSONFallsBackToDefaults(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "monitor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load should not error on malformed JSON; got %v", err)
	}
	if *got != DefaultSettings {
		t.Errorf("malformed JSON should fall back to defaults; got %+v", *got)
	}
}

func TestLoadClampsOutOfRangeValues(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "monitor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	// MaxProcesses >1000 and a negative threshold must be rejected for defaults.
	body := `{"max_processes":5000,"cpu_alert_threshold":-10}`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.MaxProcesses != DefaultSettings.MaxProcesses {
		t.Errorf("MaxProcesses=%d, want default %d (out-of-range rejected)", got.MaxProcesses, DefaultSettings.MaxProcesses)
	}
	if got.CPUAlertThreshold != DefaultSettings.CPUAlertThreshold {
		t.Errorf("CPUAlertThreshold=%v, want default %v (negative rejected)", got.CPUAlertThreshold, DefaultSettings.CPUAlertThreshold)
	}
}

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
