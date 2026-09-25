package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func TestHistoryRecordRejectsUnsafeIntervalBeforeOpeningStore(t *testing.T) {
	cmd := newHistoryRecordCmd()
	cmd.SetArgs([]string{"--interval", "0", "--db", t.TempDir() + "/history.veclite"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "at least 100ms") {
		t.Fatalf("error = %v, want full collection interval validation", err)
	}
}

// TestNewHistoryCollectorDisablesProcessEnumeration is the regression for bug
// 18's quick win: `monitor history record` ticks indefinitely but
// sampleSystem never reads collector.SystemInfo.Processes, so the collector
// it uses must not pay for a full host process enumeration on every tick.
func TestNewHistoryCollectorDisablesProcessEnumeration(t *testing.T) {
	c := newHistoryCollector()
	info, err := c.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if info.Processes != nil {
		t.Errorf("Processes = %+v, want nil (enumeration disabled)", info.Processes)
	}
	if info.ProcessesState.State != collector.MetricUnavailable {
		t.Fatalf("ProcessesState = %+v, want unavailable", info.ProcessesState)
	}
	if !strings.Contains(info.ProcessesState.Reason, "disabled") {
		t.Fatalf("ProcessesState reason = %q, want a disabled explanation", info.ProcessesState.Reason)
	}
	// sampleSystem must still get everything it actually reads.
	samples := sampleSystem(info.LastUpdate, info)
	if len(samples) != 8 {
		t.Fatalf("sampleSystem returned %d samples, want 8 (unaffected by DisableProcesses)", len(samples))
	}
}
