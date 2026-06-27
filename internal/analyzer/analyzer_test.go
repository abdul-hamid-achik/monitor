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

// TestOnAlertHookFiresPerAlert covers the incidents integration point: the
// hook must fire exactly once per alert, with matching data, and SetOnAlert(nil)
// must disable it.
func TestOnAlertHookFiresPerAlert(t *testing.T) {
	e := NewEngine()
	e.AddRule(&CPUSpikeRule{Factor: 1}) // threshold = 50%
	var got []collector.Alert
	e.SetOnAlert(func(_ collector.Event, a collector.Alert) { got = append(got, a) })

	ev := collector.Event{
		Timestamp: time.Now(),
		Processes: []collector.ProcessInfo{
			{PID: 1, Name: "hot", CPUPercent: 90},
			{PID: 2, Name: "cool", CPUPercent: 5},
		},
	}
	alerts := e.Observe(ev)
	if len(alerts) != 1 {
		t.Fatalf("Observe returned %d alerts, want 1", len(alerts))
	}
	if len(got) != 1 {
		t.Fatalf("hook fired %d times, want 1 (once per alert)", len(got))
	}
	if got[0].PID != 1 || got[0].Rule != "cpu_spike" {
		t.Errorf("hook alert = %+v, want PID 1 / cpu_spike", got[0])
	}

	e.SetOnAlert(nil)
	got = nil
	e.Observe(ev)
	if len(got) != 0 {
		t.Errorf("hook fired %d times after SetOnAlert(nil), want 0", len(got))
	}
}

func TestThresholdRule(t *testing.T) {
	r := &ThresholdRule{CPUPercent: 80, MemPercent: 90}
	// CPU over, memory under -> one cpu_threshold alert.
	ev := collector.Event{
		Timestamp: time.Now(),
		CPU:       collector.CPUInfo{UsagePercent: 85},
		Memory:    collector.MemoryInfo{UsagePercent: 50},
	}
	alerts := r.Evaluate(ev, nil)
	if len(alerts) != 1 || alerts[0].Rule != "cpu_threshold" {
		t.Fatalf("alerts = %+v, want one cpu_threshold", alerts)
	}
	// Both over -> two alerts.
	ev.Memory.UsagePercent = 95
	if got := r.Evaluate(ev, nil); len(got) != 2 {
		t.Errorf("both over: got %d alerts, want 2", len(got))
	}
	// A 0 threshold disables the check.
	if got := (&ThresholdRule{}).Evaluate(ev, nil); len(got) != 0 {
		t.Errorf("zero thresholds should not fire; got %+v", got)
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
	r := &RSSGrowthRule{MinBytesPerSample: 1}
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