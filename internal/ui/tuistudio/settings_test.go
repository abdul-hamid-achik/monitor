package tuistudio

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/tuimark"
)

// TestSettingsCycleDirtyThenSave exercises enter/space (next), "-" (back),
// and "s" (save), matching internal/ui/studio/settings.go's cycleSetting
// order and internal/config.Settings' own field semantics.
func TestSettingsCycleDirtyThenSave(t *testing.T) {
	s := newFixtureStudio(t)
	must(t, s.ui.Set("view", "settings"))

	fire(t, s, "setting_next", tuimark.Event{Keys: map[string]any{"s": "mouse_enabled"}})
	sc := screen(t, s, 100, 24)
	if !regexp.MustCompile(`mouse\s+off`).MatchString(sc) {
		t.Errorf("expected mouse to read off after one cycle:\n%s", sc)
	}
	if s.settings.MouseEnabled {
		t.Error("settings.MouseEnabled should be false after cycling it off")
	}

	// The default update_interval is 1s (options: 500ms, 1s, 2s, 5s); one
	// step back is 500ms.
	fire(t, s, "setting_prev", tuimark.Event{Keys: map[string]any{"s": "update_interval"}})
	if s.settings.UpdateInterval != 500*time.Millisecond {
		t.Errorf("update_interval should cycle back to 500ms, got %s", s.settings.UpdateInterval)
	}

	fire(t, s, "settings_save", tuimark.Event{})
	sc = screen(t, s, 100, 24)
	if !strings.Contains(sc, "saved: 2 changed") {
		t.Errorf("expected a save status naming 2 changed settings:\n%s", sc)
	}

	// The fixture settings store never touches disk; it only remembers the
	// value in memory, so a test (or a glyphrun spec) can never write to a
	// real user's ~/.config/monitor/config.json.
	store, ok := s.settingsStore.(*fixtureSettingsStore)
	if !ok {
		t.Fatal("expected the fixture settings store in fixture mode")
	}
	if store.last == nil || store.last.UpdateInterval != 500*time.Millisecond {
		t.Errorf("expected the fixture store to have recorded the saved settings, got %+v", store.last)
	}
}

// TestMouseSettingTogglesDocumentAttribute checks that switching the mouse
// setting off successfully writes the document's own "mouse_enabled"
// attribute path (<tui mouse="mouse_enabled">) with no error. Dump/Validate
// never touch terminal I/O, so whether mouse reporting is actually off is
// only observable by driving a live PTY; specs/tuistudio_settings.yml
// covers that (a click does nothing once the setting is off), mirroring
// examples/monitor's monitor_settings.yml glyphrun spec.
func TestMouseSettingTogglesDocumentAttribute(t *testing.T) {
	s := newFixtureStudio(t)
	fire(t, s, "setting_next", tuimark.Event{Keys: map[string]any{"s": "mouse_enabled"}})
	if s.settings.MouseEnabled {
		t.Error("expected settings.MouseEnabled to be false after cycling it")
	}
	if _, err := s.ui.Dump(80, 24); err != nil {
		t.Fatal(err)
	}
}

// TestUpdateIntervalChangeReachesTheMetricSource confirms cycling the
// update interval calls through to the metric source's SetInterval, the
// same live-reconfiguration internal/collector.Collector.SetInterval
// documents.
func TestUpdateIntervalChangeReachesTheMetricSource(t *testing.T) {
	s := newFixtureStudio(t)
	fx, ok := s.src.(*fixtureSource)
	if !ok {
		t.Fatal("expected the fixture metric source in fixture mode")
	}
	fire(t, s, "setting_next", tuimark.Event{Keys: map[string]any{"s": "update_interval"}})
	fx.mu.Lock()
	got := fx.interval
	fx.mu.Unlock()
	if got != 2*time.Second {
		t.Errorf("expected the fixture source interval to become 2s, got %s", got)
	}
}
