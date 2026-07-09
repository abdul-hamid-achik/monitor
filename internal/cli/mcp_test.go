package cli

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// leakyCollector returns a collect func for analyzeWindow tests: pid leak
// gains 2MB of RSS per call with flat CPU (a textbook memory_leak pattern
// per internal/analyzer/diagnosis.go), while pid spin holds flat RSS with
// CPU pinned high (a textbook cpu_spin pattern). Each call bumps an internal
// counter, so RSS is a clean linear ramp regardless of how many samples the
// window ends up taking.
func leakyCollector() func(context.Context) collector.SystemInfo {
	const leak, spin = int32(100), int32(200)
	n := 0
	return func(context.Context) collector.SystemInfo {
		n++
		return collector.SystemInfo{
			LastUpdate: time.Now(),
			Processes: []collector.ProcessInfo{
				{PID: leak, Name: "leaky", Memory: uint64(100_000_000 + n*2_000_000), CPUPercent: 10},
				{PID: spin, Name: "spinner", Memory: 100_000_000, CPUPercent: 95},
			},
		}
	}
}

// TestAnalyzeWindowSystemWide verifies pid==0 diagnoses every PID present in
// the window (memory_leak for the leaker, cpu_spin for the spinner).
func TestAnalyzeWindowSystemWide(t *testing.T) {
	res, err := analyzeWindow(context.Background(), leakyCollector(), 40*time.Millisecond, 5*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("analyzeWindow: %v", err)
	}
	if res.Samples < 4 {
		t.Fatalf("Samples = %d, want >= 4 (diagMinSamples)", res.Samples)
	}
	if len(res.Diagnoses) != 2 {
		t.Fatalf("expected 2 diagnoses (leak + spin), got %d: %+v", len(res.Diagnoses), res.Diagnoses)
	}
	var sawLeak, sawSpin bool
	for _, d := range res.Diagnoses {
		if strings.Contains(d.Summary, "memory leak") {
			sawLeak = true
		}
		if strings.Contains(d.Summary, "spin/hot loop") {
			sawSpin = true
		}
	}
	if !sawLeak || !sawSpin {
		t.Errorf("expected both a memory_leak and a cpu_spin diagnosis; got %+v", res.Diagnoses)
	}
}

// TestAnalyzeWindowFocusesOnPID verifies a non-zero pid drops every other
// process's findings (the boundary contract: "pid == 0: system-wide; else
// drop process-scoped findings for other PIDs").
func TestAnalyzeWindowFocusesOnPID(t *testing.T) {
	res, err := analyzeWindow(context.Background(), leakyCollector(), 40*time.Millisecond, 5*time.Millisecond, 100)
	if err != nil {
		t.Fatalf("analyzeWindow: %v", err)
	}
	if len(res.Diagnoses) != 1 {
		t.Fatalf("expected exactly 1 diagnosis scoped to pid 100, got %d: %+v", len(res.Diagnoses), res.Diagnoses)
	}
	if !strings.Contains(res.Diagnoses[0].Summary, "pid 100") {
		t.Errorf("diagnosis should name pid 100; got %q", res.Diagnoses[0].Summary)
	}
	if !strings.Contains(res.Diagnoses[0].Summary, "memory leak") {
		t.Errorf("pid 100 (leaky) should be diagnosed as a memory leak; got %q", res.Diagnoses[0].Summary)
	}
}

// TestAnalyzeWindowNoAnomalies verifies a flat, quiet system produces no
// diagnoses (empty, not nil-vs-empty is the handler's job — this only checks
// the engine layer).
func TestAnalyzeWindowNoAnomalies(t *testing.T) {
	collect := func(context.Context) collector.SystemInfo {
		return collector.SystemInfo{
			LastUpdate: time.Now(),
			Processes:  []collector.ProcessInfo{{PID: 1, Name: "quiet", Memory: 100_000_000, CPUPercent: 2}},
		}
	}
	res, err := analyzeWindow(context.Background(), collect, 40*time.Millisecond, 5*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("analyzeWindow: %v", err)
	}
	if len(res.Diagnoses) != 0 {
		t.Errorf("expected no diagnoses for a flat system, got %+v", res.Diagnoses)
	}
}

// TestAnalyzeWindowContextCancelled verifies a cancelled context aborts the
// sampling loop with ctx.Err() instead of running to the deadline.
func TestAnalyzeWindowContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	collect := func(context.Context) collector.SystemInfo {
		calls++
		if calls == 2 {
			cancel()
		}
		return collector.SystemInfo{LastUpdate: time.Now()}
	}
	_, err := analyzeWindow(ctx, collect, time.Hour, time.Millisecond, 0)
	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}
