package studio

import (
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func TestProgramReloaderRejectsInactiveStudio(t *testing.T) {
	if err := NewProgramReloader().Reload(); err == nil {
		t.Fatal("inactive Studio reload should report an error")
	}
}

func TestExternalReloadRefreshesModel(t *testing.T) {
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	defer m.cancel()
	m.collector.Collect(m.ctx)
	m.last = collector.SystemInfo{}

	updated, _ := m.Update(externalReloadMsg{})
	m = updated.(Model)
	if m.last.LastUpdate.IsZero() || time.Since(m.last.LastUpdate) > time.Minute {
		t.Fatalf("external reload did not apply the latest collector snapshot: %v", m.last.LastUpdate)
	}
}
