package collector

import (
	"encoding/json"
	"sort"
	"strings"
	"time"
)

const (
	// CompactSnapshotSchemaVersion identifies the stable agent-facing payload.
	// Increment it only for a breaking change to CompactSnapshot's JSON shape.
	CompactSnapshotSchemaVersion = 1

	DefaultCompactProcessLimit    = 5
	MaxCompactProcessLimit        = 25
	DefaultCompactFilesystemLimit = 10
	MaxCompactFilesystemLimit     = 50
	MaxCompactFilterRunes         = 128
)

// CompactOptions bounds and filters the agent-facing snapshot projection.
// Limits are clamped so callers cannot accidentally produce an unbounded
// response. A non-positive limit selects the corresponding default.
type CompactOptions struct {
	ProcessLimit     int
	ProcessFilter    string
	FilesystemLimit  int
	FilesystemFilter string
}

// CompactSnapshot is a bounded, history-free system view intended for small
// model context windows. SystemInfo remains the lossless API; this projection
// deliberately keeps only the values useful for orientation and drill-down.
type CompactSnapshot struct {
	SchemaVersion         int                 `json:"schema_version"`
	Kind                  string              `json:"kind"`
	CapturedAt            time.Time           `json:"captured_at"`
	Host                  CompactHost         `json:"host"`
	CPU                   CompactCPU          `json:"cpu"`
	Memory                CompactMemory       `json:"memory"`
	Cgroup                CgroupInfo          `json:"cgroup"`
	Temperature           CompactTemperature  `json:"temperature"`
	Network               CompactNetwork      `json:"network"`
	DiskIO                CompactDiskIO       `json:"disk_io"`
	Filesystems           []CompactFilesystem `json:"filesystems"`
	FilesystemSystemTotal int                 `json:"filesystem_system_total"`
	FilesystemTotal       int                 `json:"filesystem_total"`
	FilesystemLimit       int                 `json:"filesystem_limit"`
	FilesystemFilter      string              `json:"filesystem_filter,omitempty"`
	FilesystemsTruncated  bool                `json:"filesystems_truncated"`
	Processes             CompactProcesses    `json:"processes"`
}

type CompactHost struct {
	Hostname      string `json:"hostname"`
	OS            string `json:"os"`
	Platform      string `json:"platform"`
	Kernel        string `json:"kernel"`
	UptimeSeconds uint64 `json:"uptime_seconds"`
}

type CompactCPU struct {
	UsagePercent float64                       `json:"usage_percent"`
	CoreCount    int                           `json:"core_count"`
	ThreadCount  int                           `json:"thread_count"`
	FrequencyMHz float64                       `json:"frequency_mhz"`
	LoadAvg1     float64                       `json:"load_avg_1"`
	MetricStates map[string]MetricStatus       `json:"metric_states"`
}

type CompactMemory struct {
	TotalBytes     uint64                       `json:"total_bytes"`
	UsedBytes      uint64                       `json:"used_bytes"`
	AvailableBytes uint64                       `json:"available_bytes"`
	UsagePercent   float64                      `json:"usage_percent"`
	SwapTotal      uint64                       `json:"swap_total"`
	SwapUsed       uint64                       `json:"swap_used"`
	Pressure       float64                      `json:"pressure"`
	MetricStates   map[string]MetricStatus      `json:"metric_states"`
}

type CompactTemperature struct {
	Available  bool         `json:"available"`
	Source     string       `json:"source"`
	CPUPackage float64      `json:"cpu_package"`
	GPU        float64      `json:"gpu"`
	FanRPM     int          `json:"fan_rpm"`
	State      MetricStatus `json:"state"`
}

type CompactNetwork struct {
	BytesRecvPerSec uint64                       `json:"bytes_recv_per_sec"`
	BytesSentPerSec uint64                       `json:"bytes_sent_per_sec"`
	MetricStates    map[string]MetricStatus      `json:"metric_states"`
}

type CompactDiskIO struct {
	ReadPerSec  uint64                       `json:"read_per_sec"`
	WritePerSec uint64                       `json:"write_per_sec"`
	MetricStates map[string]MetricStatus     `json:"metric_states"`
}

type CompactFilesystem struct {
	Device       string  `json:"device"`
	MountPoint   string  `json:"mount_point"`
	Filesystem   string  `json:"filesystem"`
	TotalBytes   uint64  `json:"total_bytes"`
	UsedBytes    uint64  `json:"used_bytes"`
	FreeBytes    uint64  `json:"free_bytes"`
	UsagePercent float64 `json:"usage_percent"`
}

type CompactProcesses struct {
	SystemTotal int              `json:"system_total"`
	Matched     int              `json:"matched"`
	Limit       int              `json:"limit"`
	Filter      string           `json:"filter,omitempty"`
	Truncated   bool             `json:"truncated"`
	TopCPU      []CompactProcess `json:"top_cpu"`
	TopMemory   []CompactProcess `json:"top_memory"`
}

type CompactProcess struct {
	PID          int32                       `json:"pid"`
	Parent       int32                       `json:"parent,omitempty"`
	Name         string                      `json:"name"`
	CPUPercent   float64                     `json:"cpu_percent"`
	MemoryBytes  uint64                      `json:"memory_bytes"`
	Threads      int32                       `json:"threads"`
	Status       string                      `json:"status,omitempty"`
	IsSystem     bool                        `json:"is_system"`
	IsProtected  bool                        `json:"is_protected"`
	MetricStates map[string]MetricStatus      `json:"metric_states"`
}

// BuildCompactSnapshot creates a deterministic, bounded projection without
// mutating any slices owned by the collector's published SystemInfo.
func BuildCompactSnapshot(info SystemInfo, opts CompactOptions) CompactSnapshot {
	processLimit := compactLimit(opts.ProcessLimit, DefaultCompactProcessLimit, MaxCompactProcessLimit)
	filesystemLimit := compactLimit(opts.FilesystemLimit, DefaultCompactFilesystemLimit, MaxCompactFilesystemLimit)
	processFilter := normalizeCompactFilter(opts.ProcessFilter)
	filesystemFilter := normalizeCompactFilter(opts.FilesystemFilter)

	filesystems := compactFilesystems(info.Disk.Partitions, filesystemFilter)
	filesystemTotal := len(filesystems)
	filesystemsTruncated := filesystemTotal > filesystemLimit
	if filesystemsTruncated {
		filesystems = filesystems[:filesystemLimit]
	}

	processes := compactProcessSet(info.Processes, processFilter, processLimit)

	return CompactSnapshot{
		SchemaVersion: CompactSnapshotSchemaVersion,
		Kind:          "monitor.compact_snapshot",
		CapturedAt:    info.LastUpdate,
		Host: CompactHost{
			Hostname: info.Hostname, OS: info.OS, Platform: info.Platform,
			Kernel: info.Kernel, UptimeSeconds: info.Uptime,
		},
		CPU: CompactCPU{
			UsagePercent: info.CPU.UsagePercent, CoreCount: info.CPU.CoreCount,
			ThreadCount: info.CPU.ThreadCount, FrequencyMHz: info.CPU.FrequencyMHz,
			LoadAvg1: info.CPU.LoadAvg1, MetricStates: info.CPU.MetricStates,
		},
		Memory: CompactMemory{
			TotalBytes: info.Memory.TotalBytes, UsedBytes: info.Memory.UsedBytes,
			AvailableBytes: info.Memory.AvailableBytes, UsagePercent: info.Memory.UsagePercent,
			SwapTotal: info.Memory.SwapTotal, SwapUsed: info.Memory.SwapUsed,
			Pressure: info.Memory.MemoryPressure, MetricStates: info.Memory.MetricStates,
		},
		Cgroup: info.Cgroup,
		Temperature: CompactTemperature{
			Available: info.Temperature.Available, Source: info.Temperature.Source,
			CPUPackage: info.Temperature.CPUPackage, GPU: info.Temperature.GPU,
			FanRPM: info.Temperature.FanRPM, State: info.Temperature.State,
		},
		Network: CompactNetwork{
			BytesRecvPerSec: info.Network.BytesRecvPerSec,
			BytesSentPerSec: info.Network.BytesSentPerSec,
			MetricStates: info.Network.MetricStates,
		},
		DiskIO: CompactDiskIO{
			ReadPerSec: info.Disk.ReadPerSec, WritePerSec: info.Disk.WritePerSec,
			MetricStates: info.Disk.MetricStates,
		},
		Filesystems:           filesystems,
		FilesystemSystemTotal: len(info.Disk.Partitions),
		FilesystemTotal:       filesystemTotal,
		FilesystemLimit:       filesystemLimit,
		FilesystemFilter:      filesystemFilter,
		FilesystemsTruncated:  filesystemsTruncated,
		Processes:             processes,
	}
}
func (c CompactCPU) MarshalJSON() ([]byte, error) {
	out := map[string]any{"metric_states": c.MetricStates}
	if observed(c.MetricStates, metricCPUUsage) {
		out["usage_percent"] = c.UsagePercent
	}
	if observed(c.MetricStates, metricCPUPerCore) {
		out["core_count"] = c.CoreCount
	}
	if observed(c.MetricStates, metricCPUInfo) {
		out["thread_count"] = c.ThreadCount
		out["frequency_mhz"] = c.FrequencyMHz
	}
	if observed(c.MetricStates, metricCPULoad) {
		out["load_avg_1"] = c.LoadAvg1
	}
	return json.Marshal(out)
}

func (m CompactMemory) MarshalJSON() ([]byte, error) {
	out := map[string]any{"metric_states": m.MetricStates}
	if observed(m.MetricStates, metricMemoryVirtual) {
		out["total_bytes"] = m.TotalBytes
		out["used_bytes"] = m.UsedBytes
		out["available_bytes"] = m.AvailableBytes
		out["usage_percent"] = m.UsagePercent
		out["pressure"] = m.Pressure
	}
	if observed(m.MetricStates, metricMemorySwap) {
		out["swap_total"] = m.SwapTotal
		out["swap_used"] = m.SwapUsed
	}
	return json.Marshal(out)
}

func (t CompactTemperature) MarshalJSON() ([]byte, error) {
	out := map[string]any{"available": t.Available, "source": t.Source, "state": t.State}
	if t.State.State == "" || t.State.State == MetricObserved {
		out["cpu_package"] = t.CPUPackage
		out["gpu"] = t.GPU
		out["fan_rpm"] = t.FanRPM
	}
	return json.Marshal(out)
}

func (n CompactNetwork) MarshalJSON() ([]byte, error) {
	out := map[string]any{"metric_states": n.MetricStates}
	if observed(n.MetricStates, metricNetworkRate) {
		out["bytes_recv_per_sec"] = n.BytesRecvPerSec
		out["bytes_sent_per_sec"] = n.BytesSentPerSec
	}
	return json.Marshal(out)
}

func (d CompactDiskIO) MarshalJSON() ([]byte, error) {
	out := map[string]any{"metric_states": d.MetricStates}
	if observed(d.MetricStates, metricDiskRate) {
		out["read_per_sec"] = d.ReadPerSec
		out["write_per_sec"] = d.WritePerSec
	}
	return json.Marshal(out)
}

func (p CompactProcess) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"pid": p.PID, "name": p.Name, "is_system": p.IsSystem,
		"is_protected": p.IsProtected, "metric_states": p.MetricStates,
	}
	if p.Parent != 0 {
		out["parent"] = p.Parent
	}
	if p.Status != "" {
		out["status"] = p.Status
	}
	if observed(p.MetricStates, metricProcessCPU) {
		out["cpu_percent"] = p.CPUPercent
	}
	if observed(p.MetricStates, metricProcessMemory) {
		out["memory_bytes"] = p.MemoryBytes
	}
	if observed(p.MetricStates, metricProcessThread) {
		out["threads"] = p.Threads
	}
	return json.Marshal(out)
}


func compactLimit(value, fallback, maximum int) int {
	if value <= 0 {
		return fallback
	}
	if value > maximum {
		return maximum
	}
	return value
}

func normalizeCompactFilter(value string) string {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) > MaxCompactFilterRunes {
		return string(runes[:MaxCompactFilterRunes])
	}
	return value
}

func compactFilesystems(parts []DiskPartitionInfo, filter string) []CompactFilesystem {
	needle := strings.ToLower(filter)
	out := make([]CompactFilesystem, 0, len(parts))
	for _, part := range parts {
		if needle != "" && !strings.Contains(strings.ToLower(part.Device), needle) &&
			!strings.Contains(strings.ToLower(part.MountPoint), needle) &&
			!strings.Contains(strings.ToLower(part.Filesystem), needle) {
			continue
		}
		out = append(out, CompactFilesystem{
			Device: part.Device, MountPoint: part.MountPoint, Filesystem: part.Filesystem,
			TotalBytes: part.TotalBytes, UsedBytes: part.UsedBytes, FreeBytes: part.FreeBytes,
			UsagePercent: part.UsagePercent,
		})
	}
	return out
}

func compactProcessSet(all []ProcessInfo, filter string, limit int) CompactProcesses {
	needle := strings.ToLower(filter)
	matched := make([]ProcessInfo, 0, len(all))
	for _, process := range all {
		if needle == "" || strings.Contains(strings.ToLower(process.Name), needle) {
			matched = append(matched, process)
		}
	}

	byCPU := append([]ProcessInfo(nil), matched...)
	sort.SliceStable(byCPU, func(i, j int) bool {
		if byCPU[i].CPUPercent == byCPU[j].CPUPercent {
			return byCPU[i].PID < byCPU[j].PID
		}
		return byCPU[i].CPUPercent > byCPU[j].CPUPercent
	})
	byMemory := append([]ProcessInfo(nil), matched...)
	sort.SliceStable(byMemory, func(i, j int) bool {
		if byMemory[i].Memory == byMemory[j].Memory {
			return byMemory[i].PID < byMemory[j].PID
		}
		return byMemory[i].Memory > byMemory[j].Memory
	})

	if len(byCPU) > limit {
		byCPU = byCPU[:limit]
	}
	if len(byMemory) > limit {
		byMemory = byMemory[:limit]
	}
	return CompactProcesses{
		SystemTotal: len(all), Matched: len(matched), Limit: limit, Filter: filter,
		Truncated: len(matched) > limit,
		TopCPU:    compactProcesses(byCPU), TopMemory: compactProcesses(byMemory),
	}
}

func compactProcesses(processes []ProcessInfo) []CompactProcess {
	out := make([]CompactProcess, 0, len(processes))
	for _, process := range processes {
		out = append(out, CompactProcess{
			PID: process.PID, Parent: process.Parent, Name: process.Name,
			CPUPercent: process.CPUPercent, MemoryBytes: process.Memory,
			Threads: process.Threads, Status: process.Status,
			IsSystem: process.IsSystem, IsProtected: process.IsProtected,
			MetricStates: process.MetricStates,
		})
	}
	return out
}
