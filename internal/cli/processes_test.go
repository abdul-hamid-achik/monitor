package cli

import (
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func testProcesses() []collector.ProcessInfo {
	return []collector.ProcessInfo{
		{PID: 40, Name: "worker", CPUPercent: 2, Memory: 800, User: "alice", Status: "sleep"},
		{PID: 10, Name: "API", CPUPercent: 50, Memory: 500, User: "bob", Status: "running"},
		{PID: 30, Name: "agent", CPUPercent: 50, Memory: 900, User: "alice", Status: "running"},
		{PID: 1, Name: "launchd", CPUPercent: 1, Memory: 100, User: "root", IsSystem: true},
	}
}

func TestBuildProcessListFiltersSortsAndBounds(t *testing.T) {
	list, err := buildProcessList(testProcesses(), collector.MetricStatus{}, processListOptions{
		Limit: 2, Sort: "cpu", IncludeSystem: false,
	})
	if err != nil {
		t.Fatalf("buildProcessList: %v", err)
	}
	if list.Total != 4 || list.Matched != 3 || list.Returned != 2 || !list.Truncated || list.IncludesSystem {
		t.Fatalf("metadata = %+v", list)
	}
	// Equal CPU is deterministically broken by PID.
	if list.Processes[0].PID != 10 || list.Processes[1].PID != 30 {
		t.Fatalf("CPU order = %v, want PIDs 10,30", []int32{list.Processes[0].PID, list.Processes[1].PID})
	}

	filtered, err := buildProcessList(testProcesses(), collector.MetricStatus{}, processListOptions{
		Limit: 10, Sort: "memory", Filter: "alice", IncludeSystem: true,
	})
	if err != nil {
		t.Fatalf("filtered buildProcessList: %v", err)
	}
	if filtered.Matched != 2 || filtered.Processes[0].PID != 30 || filtered.Processes[1].PID != 40 {
		t.Fatalf("memory/filter result = %+v", filtered)
	}

	byPID, err := buildProcessList(testProcesses(), collector.MetricStatus{}, processListOptions{
		Limit: 10, Sort: "pid", Filter: "root", IncludeSystem: true,
	})
	if err != nil || len(byPID.Processes) != 1 || byPID.Processes[0].PID != 1 {
		t.Fatalf("system/user filter = %+v, err %v", byPID, err)
	}
}

func TestBuildProcessListRejectsInvalidOptions(t *testing.T) {
	for _, opts := range []processListOptions{
		{Limit: 0, Sort: "cpu"},
		{Limit: 1001, Sort: "cpu"},
		{Limit: 10, Sort: "age"},
	} {
		if _, err := buildProcessList(testProcesses(), collector.MetricStatus{}, opts); err == nil {
			t.Errorf("options %+v should fail", opts)
		}
	}
}

func TestBuildProcessListEmptyIsJSONArray(t *testing.T) {
	list, err := buildProcessList(nil, collector.MetricStatus{}, processListOptions{Limit: 10, Sort: "name"})
	if err != nil {
		t.Fatalf("buildProcessList: %v", err)
	}
	if list.Processes == nil || len(list.Processes) != 0 {
		t.Fatalf("Processes = %#v, want non-nil empty slice", list.Processes)
	}
}

func TestProcessesCommandMetadata(t *testing.T) {
	cmd := newProcessesCmd()
	if cmd.Flags().Lookup("json") == nil || cmd.Flags().Lookup("filter") == nil ||
		cmd.Flags().Lookup("sort") == nil || cmd.Flags().Lookup("limit") == nil ||
		cmd.Flags().Lookup("system") == nil {
		t.Fatal("processes command is missing an agent-facing flag")
	}
	foundPS := false
	for _, alias := range cmd.Aliases {
		foundPS = foundPS || alias == "ps"
	}
	if !foundPS {
		t.Fatalf("processes aliases = %v, want ps", cmd.Aliases)
	}
}
