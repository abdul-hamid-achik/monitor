package cli

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
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

// TestAnalyzeWindowSurfacesAlerts is the wire regression for bug 17's second
// half: analyzeWindow's engine (NewDefaultEngine) runs the same rules
// `monitor watch` does, but analyzeWindow used to discard every Alert
// Observe returned and surface only the separate cross-signal Diagnose
// table. A zombie process is flat CPU/RSS (never a Diagnose finding) but
// must always raise a zombie_process Alert, so seeing it in res.Alerts
// proves the plain rule findings now reach the caller. It also covers the
// dedup-by-rule+PID requirement (a zombie present for the whole window would
// otherwise repeat once per sample) and the pid filter already covered for
// Diagnoses above.
func TestAnalyzeWindowSurfacesAlerts(t *testing.T) {
	const zombiePID = int32(300)
	collect := func(context.Context) collector.SystemInfo {
		return collector.SystemInfo{
			LastUpdate: time.Now(),
			Processes: []collector.ProcessInfo{
				{PID: zombiePID, Name: "reaper-orphan", Status: "Z", Memory: 0, CPUPercent: 0},
			},
		}
	}
	res, err := analyzeWindow(context.Background(), collect, 40*time.Millisecond, 5*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("analyzeWindow: %v", err)
	}
	if res.Samples < 4 {
		t.Fatalf("Samples = %d, want >= 4 samples to prove dedup across repeats", res.Samples)
	}
	if len(res.Alerts) != 1 {
		t.Fatalf("Alerts = %+v, want exactly 1 deduplicated zombie_process alert across %d samples", res.Alerts, res.Samples)
	}
	if res.Alerts[0].Rule != "zombie_process" || res.Alerts[0].PID != zombiePID {
		t.Errorf("Alerts[0] = %+v, want rule=zombie_process pid=%d", res.Alerts[0], zombiePID)
	}

	// Focusing on an unrelated PID must drop the zombie alert, same as it
	// drops another process's Diagnosis.
	focused, err := analyzeWindow(context.Background(), collect, 40*time.Millisecond, 5*time.Millisecond, zombiePID+1)
	if err != nil {
		t.Fatalf("analyzeWindow (focused): %v", err)
	}
	if len(focused.Alerts) != 0 {
		t.Errorf("Alerts = %+v with pid filter set to a different PID, want none", focused.Alerts)
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

// TestListIssuesForMCPForwardsWindowFiltersToStore verifies the
// mcp.Service.IssuesList wiring (E2.6): listIssuesForMCP just opens the
// resolved store read-only and forwards opts to Store.List -- the filters
// it receives here are exactly what internal/mcp/server.go's handleIssues
// built from the typed MCP input.
func TestListIssuesForMCPForwardsWindowFiltersToStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	t.Setenv(issues.StorePathEnv, path)
	store, err := issues.OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if _, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		Project: "monitor", Kind: issues.KindException, Message: "boom", RunID: "run-a", Release: "rel-a",
	}); err != nil {
		t.Fatalf("seed exception issue: %v", err)
	}
	if _, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		Project: "monitor", Kind: "monitor.alert.cpu_spike", Message: "cpu spike",
	}); err != nil {
		t.Fatalf("seed alert issue: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, err := listIssuesForMCP(context.Background(), issues.ListOptions{Kind: "exception"})
	if err != nil {
		t.Fatalf("listIssuesForMCP: %v", err)
	}
	if len(got) != 1 || got[0].Kind != issues.KindException {
		t.Fatalf("Kind=exception results = %+v", got)
	}

	got, err = listIssuesForMCP(context.Background(), issues.ListOptions{RunID: "run-a"})
	if err != nil {
		t.Fatalf("listIssuesForMCP: %v", err)
	}
	if len(got) != 1 || got[0].Kind != issues.KindException {
		t.Fatalf("RunID=run-a results = %+v", got)
	}

	all, err := listIssuesForMCP(context.Background(), issues.ListOptions{})
	if err != nil {
		t.Fatalf("listIssuesForMCP: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered results = %+v, want 2", all)
	}
}
