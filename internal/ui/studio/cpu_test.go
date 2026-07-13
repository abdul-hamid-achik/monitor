package studio

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func TestCPUGridRendersBeyondEightCores(t *testing.T) {
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	t.Cleanup(m.cancel)
	m.width, m.height = 120, 40
	m.last.LastUpdate = time.Now()
	m.last.CPU.PerCoreUsage = make([]float64, 20)
	for i := range m.last.CPU.PerCoreUsage {
		m.last.CPU.PerCoreUsage[i] = float64(i * 4)
	}
	content := m.renderCPU()
	for _, core := range []int{0, 8, 15, 19} {
		if !strings.Contains(content, fmt.Sprintf("Core %-2d", core)) {
			t.Errorf("responsive CPU grid should include core %d; got:\n%s", core, content)
		}
	}
	if strings.Contains(content, "cores hidden") {
		t.Fatalf("120x40 should fit 20 cores without truncation; got:\n%s", content)
	}
}

func TestCPUGridStatesWhenSpaceOrMetricsAreUnavailable(t *testing.T) {
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	t.Cleanup(m.cancel)
	m.width, m.height = 55, 24
	m.last.LastUpdate = time.Now()
	m.last.CPU.PerCoreUsage = make([]float64, 20)
	content := m.renderCPU()
	if !strings.Contains(content, "cores hidden") || !strings.Contains(content, "enlarge the terminal") {
		t.Fatalf("small CPU view should explain its core truncation; got:\n%s", content)
	}

	m.last.CPU.PerCoreUsage = nil
	m.last.CPU.MetricStates = map[string]collector.MetricStatus{
		"usage":        {State: collector.MetricUnavailable, Reason: "CPU sampler failed"},
		"per_core":     {State: collector.MetricUnsupported, Reason: "per-core counters are unsupported"},
		"info":         {State: collector.MetricUnavailable, Reason: "frequency unavailable"},
		"load_average": {State: collector.MetricUnsupported, Reason: "not exposed on this platform"},
	}
	content = normalizedStudioText(m.renderCPU())
	for _, reason := range []string{"CPU sampler failed", "per-core counters are unsupported", "frequency unavailable", "not exposed on this platform"} {
		if !strings.Contains(content, reason) {
			t.Errorf("CPU view should expose reason %q; got:\n%s", reason, content)
		}
	}
}

func TestCPUEmptyStatesGiveNextAction(t *testing.T) {
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	t.Cleanup(m.cancel)
	m.width, m.height = 100, 30
	m.last.LastUpdate = time.Now()
	content := m.renderCPU()
	for _, want := range []string{"No CPU history yet", "next refresh", "No per-core samples yet", "press r to refresh"} {
		if !strings.Contains(content, want) {
			t.Errorf("CPU empty state missing %q; got:\n%s", want, content)
		}
	}
}
