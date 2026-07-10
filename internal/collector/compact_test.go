package collector

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildCompactSnapshotBoundsAndSorts(t *testing.T) {
	info := SystemInfo{
		Hostname: "test-host",
		Processes: []ProcessInfo{
			{PID: 1, Name: "idle", CPUPercent: 1, Memory: 900},
			{PID: 2, Name: "hot", CPUPercent: 90, Memory: 100},
			{PID: 3, Name: "warm", CPUPercent: 40, Memory: 500},
		},
		Disk: DiskInfo{Partitions: []DiskPartitionInfo{
			{Device: "disk1", MountPoint: "/", Filesystem: "apfs"},
			{Device: "disk2", MountPoint: "/data", Filesystem: "apfs"},
		}},
	}

	got := BuildCompactSnapshot(info, CompactOptions{ProcessLimit: 2, FilesystemLimit: 1})
	if got.SchemaVersion != CompactSnapshotSchemaVersion || got.Kind != "monitor.compact_snapshot" {
		t.Fatalf("missing compact schema identity: %+v", got)
	}
	if len(got.Processes.TopCPU) != 2 || got.Processes.TopCPU[0].PID != 2 {
		t.Fatalf("top_cpu = %+v, want hot process first and two entries", got.Processes.TopCPU)
	}
	if len(got.Processes.TopMemory) != 2 || got.Processes.TopMemory[0].PID != 1 {
		t.Fatalf("top_memory = %+v, want largest process first and two entries", got.Processes.TopMemory)
	}
	if !got.Processes.Truncated || got.Processes.Matched != 3 || got.Processes.SystemTotal != 3 {
		t.Fatalf("process truncation metadata = %+v", got.Processes)
	}
	if len(got.Filesystems) != 1 || !got.FilesystemsTruncated || got.FilesystemTotal != 2 ||
		got.FilesystemSystemTotal != 2 || got.FilesystemLimit != 1 {
		t.Fatalf("filesystem truncation metadata = len %d, truncated %v, total %d, system %d, limit %d",
			len(got.Filesystems), got.FilesystemsTruncated, got.FilesystemTotal,
			got.FilesystemSystemTotal, got.FilesystemLimit)
	}

	data, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal compact snapshot: %v", err)
	}
	if len(data) > 10_000 {
		t.Fatalf("small fixture produced unexpectedly large payload: %d bytes", len(data))
	}
}

func TestBuildCompactSnapshotFiltersAndClamps(t *testing.T) {
	procs := make([]ProcessInfo, MaxCompactProcessLimit+10)
	for i := range procs {
		name := "worker"
		if i%2 == 0 {
			name = "MODEL-runner"
		}
		procs[i] = ProcessInfo{PID: int32(i + 1), Name: name, CPUPercent: float64(i), Memory: uint64(i)}
	}
	info := SystemInfo{
		Processes: procs,
		Disk: DiskInfo{Partitions: []DiskPartitionInfo{
			{Device: "disk1", MountPoint: "/", Filesystem: "apfs"},
			{Device: "tmpfs", MountPoint: "/tmp", Filesystem: "TMPFS"},
		}},
	}

	got := BuildCompactSnapshot(info, CompactOptions{
		ProcessLimit: MaxCompactProcessLimit + 1, ProcessFilter: "model",
		FilesystemLimit: MaxCompactFilesystemLimit + 1, FilesystemFilter: "tmpfs",
	})
	if got.Processes.Limit != MaxCompactProcessLimit {
		t.Fatalf("process limit = %d, want clamp %d", got.Processes.Limit, MaxCompactProcessLimit)
	}
	if got.Processes.Matched != 18 || len(got.Processes.TopCPU) != 18 || len(got.Processes.TopMemory) != 18 {
		t.Fatalf("case-insensitive process filter failed: %+v", got.Processes)
	}
	if got.Processes.Filter != "model" {
		t.Fatalf("filter metadata = %q", got.Processes.Filter)
	}
	if got.FilesystemTotal != 1 || len(got.Filesystems) != 1 || got.Filesystems[0].MountPoint != "/tmp" {
		t.Fatalf("case-insensitive filesystem filter failed: %+v", got.Filesystems)
	}
	if got.FilesystemFilter != "tmpfs" || got.FilesystemLimit != MaxCompactFilesystemLimit {
		t.Fatalf("filesystem filter metadata = %q, limit %d", got.FilesystemFilter, got.FilesystemLimit)
	}
}

func TestBuildCompactSnapshotBoundsEchoedFilter(t *testing.T) {
	got := BuildCompactSnapshot(SystemInfo{}, CompactOptions{ProcessFilter: strings.Repeat("x", 10_000)})
	if len([]rune(got.Processes.Filter)) != MaxCompactFilterRunes {
		t.Fatalf("filter length = %d, want %d", len([]rune(got.Processes.Filter)), MaxCompactFilterRunes)
	}
}

func TestBuildCompactSnapshotEmptyArraysAreNotNull(t *testing.T) {
	data, err := json.Marshal(BuildCompactSnapshot(SystemInfo{}, CompactOptions{}))
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if _, ok := payload["filesystems"].([]any); !ok {
		t.Fatalf("filesystems must be a JSON array, got %T", payload["filesystems"])
	}
	processes := payload["processes"].(map[string]any)
	if _, ok := processes["top_cpu"].([]any); !ok {
		t.Fatalf("top_cpu must be a JSON array, got %T", processes["top_cpu"])
	}
	if _, ok := processes["top_memory"].([]any); !ok {
		t.Fatalf("top_memory must be a JSON array, got %T", processes["top_memory"])
	}
}

func TestCompactSnapshotPreservesMetricAvailability(t *testing.T) {
	info := SystemInfo{
		CPU: CPUInfo{
			LoadAvg1: 0,
			MetricStates: map[string]MetricStatus{
				metricCPUUsage:   metricStatus(MetricObserved, ""),
				metricCPUPerCore: metricStatus(MetricObserved, ""),
				metricCPUInfo:    metricStatus(MetricObserved, ""),
				metricCPULoad:    metricStatus(MetricUnavailable, "no source"),
			},
		},
	}
	data, err := json.Marshal(BuildCompactSnapshot(info, CompactOptions{}))
	if err != nil {
		t.Fatalf("marshal compact snapshot: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal compact snapshot: %v", err)
	}
	cpu := payload["cpu"].(map[string]any)
	if _, exists := cpu["load_avg_1"]; exists {
		t.Fatalf("unavailable compact load average serialized as zero: %s", data)
	}
	states := cpu["metric_states"].(map[string]any)
	loadState := states[metricCPULoad].(map[string]any)
	if loadState["state"] != string(MetricUnavailable) {
		t.Fatalf("load state = %v, want unavailable", loadState)
	}
}
