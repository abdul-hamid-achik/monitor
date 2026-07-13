package config

import (
	"math"
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

func TestPathUsesMonitorConfigDirectory(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	got, err := Path()
	if err != nil {
		t.Fatalf("Path: %v", err)
	}
	want := filepath.Join(home, ".config", "monitor", "config.json")
	if got != want {
		t.Fatalf("Path = %q, want %q", got, want)
	}
	if info, err := os.Stat(filepath.Dir(got)); err != nil || !info.IsDir() {
		t.Fatalf("Path should create its parent directory: info=%v err=%v", info, err)
	}
}

func TestSettingsValidate(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Settings)
	}{
		{"zero interval", func(s *Settings) { s.UpdateInterval = 0 }},
		{"bad unit", func(s *Settings) { s.TemperatureUnit = "K" }},
		{"zero processes", func(s *Settings) { s.MaxProcesses = 0 }},
		{"too many processes", func(s *Settings) { s.MaxProcesses = 1001 }},
		{"negative cpu", func(s *Settings) { s.CPUAlertThreshold = -1 }},
		{"cpu over 100", func(s *Settings) { s.CPUAlertThreshold = 101 }},
		{"nan memory", func(s *Settings) { s.MemoryAlertThreshold = math.NaN() }},
	}
	if err := Default().Validate(); err != nil {
		t.Fatalf("defaults should validate: %v", err)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			settings := Default()
			tt.mutate(settings)
			if err := settings.Validate(); err == nil {
				t.Fatalf("Validate should reject %+v", settings)
			}
		})
	}
}

func TestSaveRejectsInvalidSettingsWithoutReplacingFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	settings := Default()
	settings.MaxProcesses = 25
	if err := settings.Save(); err != nil {
		t.Fatalf("initial Save: %v", err)
	}
	settings.MaxProcesses = 5000
	if err := settings.Save(); err == nil {
		t.Fatal("Save should reject invalid settings")
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.MaxProcesses != 25 {
		t.Fatalf("invalid Save replaced prior file: max_processes=%d", got.MaxProcesses)
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
	// Invalid enum/ranges fall back field-by-field; an unrelated valid value
	// must still be applied from the same file.
	body := `{"temperature_unit":"K","max_processes":5000,"cpu_alert_threshold":150,"memory_alert_threshold":-10,"mouse_enabled":false}`
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
		t.Errorf("CPUAlertThreshold=%v, want default %v (>100 rejected)", got.CPUAlertThreshold, DefaultSettings.CPUAlertThreshold)
	}
	if got.MemoryAlertThreshold != DefaultSettings.MemoryAlertThreshold {
		t.Errorf("MemoryAlertThreshold=%v, want default %v (negative rejected)", got.MemoryAlertThreshold, DefaultSettings.MemoryAlertThreshold)
	}
	if got.TemperatureUnit != DefaultSettings.TemperatureUnit {
		t.Errorf("TemperatureUnit=%q, want default %q (unknown unit rejected)", got.TemperatureUnit, DefaultSettings.TemperatureUnit)
	}
	if got.MouseEnabled {
		t.Error("valid mouse_enabled=false should still apply when sibling fields are invalid")
	}
}

func TestLoadNormalizesTemperatureUnit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	dir := filepath.Join(home, ".config", "monitor")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(`{"temperature_unit":" f "}`), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.TemperatureUnit != "F" {
		t.Fatalf("TemperatureUnit=%q, want normalized F", got.TemperatureUnit)
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
