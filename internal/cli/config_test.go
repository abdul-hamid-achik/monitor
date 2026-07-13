package cli

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/config"
)

func TestConfigSetGetAndReset(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	set := newConfigSetCmd()
	var setOut bytes.Buffer
	set.SetOut(&setOut)
	set.SetArgs([]string{"max-processes", "125"})
	if err := set.Execute(); err != nil {
		t.Fatalf("config set: %v", err)
	}
	settings, err := config.Load()
	if err != nil {
		t.Fatalf("config Load: %v", err)
	}
	if settings.MaxProcesses != 125 {
		t.Fatalf("max_processes = %d, want 125", settings.MaxProcesses)
	}
	if !strings.Contains(setOut.String(), "max_processes = 125") {
		t.Fatalf("set output = %q", setOut.String())
	}

	get := newConfigGetCmd()
	var getOut bytes.Buffer
	get.SetOut(&getOut)
	get.SetArgs([]string{"max_processes"})
	if err := get.Execute(); err != nil {
		t.Fatalf("config get: %v", err)
	}
	if strings.TrimSpace(getOut.String()) != "125" {
		t.Fatalf("get output = %q, want 125", getOut.String())
	}

	refused := newConfigResetCmd()
	refused.SetArgs(nil)
	if err := refused.Execute(); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("reset without confirmation = %v, want --yes refusal", err)
	}

	reset := newConfigResetCmd()
	reset.SetOut(&bytes.Buffer{})
	reset.SetArgs([]string{"--yes"})
	if err := reset.Execute(); err != nil {
		t.Fatalf("config reset --yes: %v", err)
	}
	settings, err = config.Load()
	if err != nil {
		t.Fatalf("config Load after reset: %v", err)
	}
	if *settings != config.DefaultSettings {
		t.Fatalf("reset settings = %+v, want defaults %+v", *settings, config.DefaultSettings)
	}
}

func TestSetConfigValueValidatesEveryType(t *testing.T) {
	settings := config.Default()
	valid := map[string]string{
		"update_interval":        "250ms",
		"temperature_unit":       "f",
		"show_system_processes":  "true",
		"max_processes":          "75",
		"mouse_enabled":          "false",
		"cpu_alert_threshold":    "85.5",
		"memory_alert_threshold": "92",
	}
	for key, value := range valid {
		if err := setConfigValue(settings, key, value); err != nil {
			t.Fatalf("setConfigValue(%s, %s): %v", key, value, err)
		}
	}
	if settings.UpdateInterval != 250*time.Millisecond || settings.TemperatureUnit != "F" ||
		!settings.ShowSystemProcesses || settings.MaxProcesses != 75 || settings.MouseEnabled ||
		settings.CPUAlertThreshold != 85.5 || settings.MemoryAlertThreshold != 92 {
		t.Fatalf("settings not updated correctly: %+v", settings)
	}

	invalid := map[string]string{
		"update_interval":        "now",
		"temperature_unit":       "K",
		"show_system_processes":  "sometimes",
		"max_processes":          "1001",
		"mouse_enabled":          "maybe",
		"cpu_alert_threshold":    "NaN",
		"memory_alert_threshold": "101",
		"unknown":                "value",
	}
	for key, value := range invalid {
		fresh := config.Default()
		if err := setConfigValue(fresh, key, value); err == nil {
			t.Errorf("setConfigValue(%s, %s) should fail", key, value)
		}
	}
}

func TestConfigCommandTree(t *testing.T) {
	cmd := newConfigCmd()
	want := map[string]bool{"show": false, "get": false, "set": false, "path": false, "reset": false}
	for _, sub := range cmd.Commands() {
		if _, ok := want[sub.Name()]; ok {
			want[sub.Name()] = true
		}
		if sub.Flags().Lookup("json") == nil {
			t.Errorf("config %s should support --json", sub.Name())
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("missing config subcommand %q", name)
		}
	}
}
