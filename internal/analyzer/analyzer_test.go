package analyzer

import (
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func TestEngineObservesWithoutRules(t *testing.T) {
	e := NewEngine()
	if alerts := e.Observe(collector.Event{Timestamp: time.Now()}); len(alerts) != 0 {
		t.Errorf("expected 0 alerts, got %d", len(alerts))
	}
}

func TestCPUSpikeRule(t *testing.T) {
	r := &CPUSpikeRule{Factor: 2.0}
	e := NewEngine()
	e.AddRule(r)

	// baseline 50% — threshold is 2*50 = 100
	ev := collector.Event{
		Timestamp: time.Now(),
		Processes: []collector.ProcessInfo{
			{PID: 1, Name: "low", CPUPercent: 10},
			{PID: 2, Name: "spike", CPUPercent: 95},
		},
	}
	alerts := e.Observe(ev)
	// 95 is not > 100, so no alert expected
	if len(alerts) != 0 {
		t.Errorf("expected 0 alerts at 95%%, got %d", len(alerts))
	}

	ev2 := collector.Event{
		Timestamp: time.Now(),
		Processes: []collector.ProcessInfo{
			{PID: 2, Name: "spike", CPUPercent: 110},
		},
	}
	alerts = e.Observe(ev2)
	if len(alerts) != 1 {
		t.Fatalf("expected 1 alert, got %d", len(alerts))
	}
	if alerts[0].Rule != "cpu_spike" {
		t.Errorf("wrong rule: %s", alerts[0].Rule)
	}
}

func TestRSSGrowthRule(t *testing.T) {
	r := &RSSGrowthRule{MinBytesPerSec: 1}
	e := NewEngine()
	e.AddRule(r)

	pid := int32(42)
	for i := 0; i < 5; i++ {
		e.Observe(collector.Event{
			Timestamp: time.Now().Add(time.Duration(i) * time.Second),
			Processes: []collector.ProcessInfo{
				{PID: pid, Name: "grower", Memory: uint64(100_000 * (i + 1))},
			},
		})
	}
	alerts := e.Observe(collector.Event{
		Timestamp: time.Now(),
		Processes: []collector.ProcessInfo{
			{PID: pid, Name: "grower", Memory: 700_000},
		},
	})
	if len(alerts) == 0 {
		t.Fatal("expected at least one rss_growth alert")
	}
	if alerts[0].Rule != "rss_growth" {
		t.Errorf("wrong rule: %s", alerts[0].Rule)
	}
}

func TestLinearRegressionSlope(t *testing.T) {
	slope, r2 := linearRegression([]uint64{100, 200, 300, 400, 500})
	if slope < 99 || slope > 101 {
		t.Errorf("slope = %f, want ~100", slope)
	}
	if r2 < 0.99 {
		t.Errorf("r² = %f, want ~1.0", r2)
	}
}

func TestLinearRegressionConstant(t *testing.T) {
	_, r2 := linearRegression([]uint64{500, 500, 500})
	if r2 != 0 {
		t.Errorf("r² of constant = %f, want 0", r2)
	}
}

func TestLinearRegressionShort(t *testing.T) {
	slope, r2 := linearRegression([]uint64{1})
	if slope != 0 || r2 != 0 {
		t.Errorf("single-point regression should be zero")
	}
}

func TestHistoryTrimsToMax(t *testing.T) {
	h := NewHistory(3)
	for i := 0; i < 5; i++ {
		h.Push(collector.Event{Timestamp: time.Now(), Processes: []collector.ProcessInfo{
			{PID: 1, Memory: uint64(i)},
		}})
	}
	if got := len(h.RSSForPID(1)); got != 3 {
		t.Errorf("history len = %d, want 3 (capped)", got)
	}
}