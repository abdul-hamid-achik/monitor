package collector

import (
	"encoding/json"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/capability"
)

// MetricState makes absence explicit instead of overloading numeric zero.
type MetricState string

const (
	MetricObserved    MetricState = "observed"
	MetricUnsupported MetricState = "unsupported"
	MetricUnavailable MetricState = "unavailable"
)

// MetricStatus describes whether a metric was collected and, when it was not,
// why. Observed metrics may legitimately contain numeric zero.
type MetricStatus struct {
	State  MetricState `json:"state"`
	Reason string      `json:"reason,omitempty"`
}

const (
	metricCPUUsage        = "usage"
	metricCPUPerCore      = "per_core"
	metricCPUInfo         = "info"
	metricCPULoad         = "load_average"
	metricMemoryVirtual   = "virtual"
	metricMemorySwap      = "swap"
	metricMemoryBreakdown = "breakdown"
	metricNetworkIO       = "io"
	metricNetworkRate     = "rate"
	metricDiskParts       = "partitions"
	metricDiskIO          = "io"
	metricDiskRate        = "rate"
	metricProcessCPU      = "cpu"
	metricProcessMemory   = "memory"
	metricProcessMemPct   = "memory_percent"
	metricProcessName     = "name"
	metricProcessThread   = "threads"
	metricProcessUser     = "user"
	metricProcessParent   = "parent"
	metricProcessStatus   = "status"
	metricProcessIO       = "io"
)

func observed(statuses map[string]MetricStatus, key string) bool {
	status, exists := statuses[key]
	return !exists || status.State == MetricObserved
}

func metricStatus(state MetricState, reason string) MetricStatus {
	return MetricStatus{State: state, Reason: reason}
}

func statusFromCapability(s capability.Support) MetricStatus {
	switch s.State {
	case capability.Unsupported:
		return metricStatus(MetricUnsupported, s.Reason)
	case capability.Unavailable:
		return metricStatus(MetricUnavailable, s.Reason)
	default:
		return metricStatus(MetricObserved, "")
	}
}

// CPUInfo holds CPU usage statistics.
type CPUInfo struct {
	UsagePercent   float64                 `json:"usage_percent"`
	PerCoreUsage   []float64               `json:"per_core_usage"`
	FrequencyMHz   float64                 `json:"frequency_mhz"`
	CoreCount      int                     `json:"core_count"`
	ThreadCount    int                     `json:"thread_count"`
	LoadAvg1       float64                 `json:"load_avg_1"`
	LoadAvg5       float64                 `json:"load_avg_5"`
	LoadAvg15      float64                 `json:"load_avg_15"`
	History        []float64               `json:"history"`
	PerCoreHistory [][]float64             `json:"per_core_history,omitempty"`
	LastUpdate     time.Time               `json:"last_update"`
	MetricStates   map[string]MetricStatus `json:"metric_states"`
}

// MemoryInfo holds RAM and swap statistics.
type MemoryInfo struct {
	TotalBytes       uint64                  `json:"total_bytes"`
	UsedBytes        uint64                  `json:"used_bytes"`
	FreeBytes        uint64                  `json:"free_bytes"`
	AvailableBytes   uint64                  `json:"available_bytes"`
	UsagePercent     float64                 `json:"usage_percent"`
	SwapTotal        uint64                  `json:"swap_total"`
	SwapUsed         uint64                  `json:"swap_used"`
	SwapFree         uint64                  `json:"swap_free"`
	MemoryPressure   float64                 `json:"memory_pressure"`
	AppMemory        uint64                  `json:"app_memory"`
	WiredMemory      uint64                  `json:"wired_memory"`
	CompressedMemory uint64                  `json:"compressed_memory"`
	CacheMemory      uint64                  `json:"cache_memory"`
	PurgeableMemory  uint64                  `json:"purgeable_memory"`
	History          []float64               `json:"history"`
	LastUpdate       time.Time               `json:"last_update"`
	MetricStates     map[string]MetricStatus `json:"metric_states"`
}

// TemperatureInfo holds sensor readings (estimated on non-SMC builds).
type TemperatureInfo struct {
	CPUPackage float64      `json:"cpu_package"`
	CPUCores   float64      `json:"cpu_cores"`
	GPU        float64      `json:"gpu"`
	ANE        float64      `json:"ane"`
	Battery    float64      `json:"battery"`
	Ambient    float64      `json:"ambient"`
	FanRPM     int          `json:"fan_rpm"`
	FanMode    string       `json:"fan_mode"`
	History    []float64    `json:"history"`
	LastUpdate time.Time    `json:"last_update"`
	State      MetricStatus `json:"state"`
	Available  bool         `json:"available"`
	// Source names the data origin: "estimated" (CPU-load heuristic) or
	// "powermetrics" (real SMC readings via sudo powermetrics). Lets
	// callers badge the UI and lets JSON consumers distinguish synthetic
	// from real values without out-of-band knowledge.
	Source string `json:"source"`
}

// NetworkInfo holds aggregate network statistics.
type NetworkInfo struct {
	BytesSent       uint64                  `json:"bytes_sent"`
	BytesRecv       uint64                  `json:"bytes_recv"`
	PacketsSent     uint64                  `json:"packets_sent"`
	PacketsRecv     uint64                  `json:"packets_recv"`
	BytesSentPerSec uint64                  `json:"bytes_sent_per_sec"`
	BytesRecvPerSec uint64                  `json:"bytes_recv_per_sec"`
	DownloadHistory []float64               `json:"download_history"`
	UploadHistory   []float64               `json:"upload_history"`
	LastUpdate      time.Time               `json:"last_update"`
	MetricStates    map[string]MetricStatus `json:"metric_states"`
}

// DiskPartitionInfo describes a mounted partition.
type DiskPartitionInfo struct {
	Device       string  `json:"device"`
	MountPoint   string  `json:"mount_point"`
	TotalBytes   uint64  `json:"total_bytes"`
	UsedBytes    uint64  `json:"used_bytes"`
	FreeBytes    uint64  `json:"free_bytes"`
	UsagePercent float64 `json:"usage_percent"`
	Filesystem   string  `json:"filesystem"`
}

// DiskInfo holds disk statistics.
type DiskInfo struct {
	Partitions   []DiskPartitionInfo     `json:"partitions"`
	ReadBytes    uint64                  `json:"read_bytes"`
	WriteBytes   uint64                  `json:"write_bytes"`
	ReadPerSec   uint64                  `json:"read_per_sec"`
	WritePerSec  uint64                  `json:"write_per_sec"`
	ReadHistory  []float64               `json:"read_history"`
	WriteHistory []float64               `json:"write_history"`
	LastUpdate   time.Time               `json:"last_update"`
	MetricStates map[string]MetricStatus `json:"metric_states"`
}

// ProcessInfo describes one OS process.
type ProcessInfo struct {
	PID           int32                   `json:"pid"`
	Name          string                  `json:"name"`
	CPUPercent    float64                 `json:"cpu_percent"`
	Memory        uint64                  `json:"memory"`
	MemoryPercent float64                 `json:"memory_percent"`
	Threads       int32                   `json:"threads"`
	User          string                  `json:"user"`
	Status        string                  `json:"status,omitempty"`
	Parent        int32                   `json:"parent,omitempty"`
	IsSystem      bool                    `json:"is_system"`
	IsProtected   bool                    `json:"is_protected"`
	IOReadBytes   uint64                  `json:"io_read_bytes,omitempty"`
	IOWriteBytes  uint64                  `json:"io_write_bytes,omitempty"`
	MetricStates  map[string]MetricStatus `json:"metric_states"`
}

// SystemInfo aggregates all metric families.
// CgroupInfo reports cgroup v2 limits when running inside a limited cgroup
// (a Linux container). Limited is false on the host and on macOS.
type CgroupInfo struct {
	Limited       bool         `json:"limited"`
	MemLimitBytes uint64       `json:"mem_limit_bytes,omitempty"`
	MemUsageBytes uint64       `json:"mem_usage_bytes,omitempty"`
	CPUQuotaCores float64      `json:"cpu_quota_cores,omitempty"`
	State         MetricStatus `json:"state"`
}

type SystemInfo struct {
	CPU                 CPUInfo                                `json:"cpu"`
	Memory              MemoryInfo                             `json:"memory"`
	Cgroup              CgroupInfo                             `json:"cgroup"`
	Temperature         TemperatureInfo                        `json:"temperature"`
	Network             NetworkInfo                            `json:"network"`
	Disk                DiskInfo                               `json:"disk"`
	Processes           []ProcessInfo                          `json:"processes"`
	ProcessesState      MetricStatus                           `json:"processes_state"`
	ProcessesLastUpdate time.Time                              `json:"processes_last_update"`
	Hostname            string                                 `json:"hostname"`
	OS                  string                                 `json:"os"`
	Platform            string                                 `json:"platform"`
	Kernel              string                                 `json:"kernel"`
	Uptime              uint64                                 `json:"uptime"`
	BootTime            uint64                                 `json:"boot_time"`
	LastUpdate          time.Time                              `json:"last_update"`
	Capture             MetricStatus                           `json:"capture"`
	Capabilities        map[capability.Name]capability.Support `json:"capabilities"`
}

// MarshalJSON keeps observed values at their established numeric keys, while
// omitting values that were unsupported or unavailable. metric_states remains
// explicit in both cases, so an observed zero cannot be confused with absence.
func (c CPUInfo) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"history": c.History, "last_update": c.LastUpdate, "metric_states": c.MetricStates,
	}
	if len(c.PerCoreHistory) > 0 {
		out["per_core_history"] = c.PerCoreHistory
	}
	if observed(c.MetricStates, metricCPUUsage) {
		out["usage_percent"] = c.UsagePercent
	}
	if observed(c.MetricStates, metricCPUPerCore) {
		out["per_core_usage"] = c.PerCoreUsage
		out["core_count"] = c.CoreCount
	}
	if observed(c.MetricStates, metricCPUInfo) {
		out["frequency_mhz"] = c.FrequencyMHz
		out["thread_count"] = c.ThreadCount
	}
	if observed(c.MetricStates, metricCPULoad) {
		out["load_avg_1"] = c.LoadAvg1
		out["load_avg_5"] = c.LoadAvg5
		out["load_avg_15"] = c.LoadAvg15
	}
	return json.Marshal(out)
}

func (m MemoryInfo) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"history": m.History, "last_update": m.LastUpdate, "metric_states": m.MetricStates,
	}
	if observed(m.MetricStates, metricMemoryVirtual) {
		out["total_bytes"] = m.TotalBytes
		out["used_bytes"] = m.UsedBytes
		out["free_bytes"] = m.FreeBytes
		out["available_bytes"] = m.AvailableBytes
		out["usage_percent"] = m.UsagePercent
		out["memory_pressure"] = m.MemoryPressure
	}
	if observed(m.MetricStates, metricMemorySwap) {
		out["swap_total"] = m.SwapTotal
		out["swap_used"] = m.SwapUsed
		out["swap_free"] = m.SwapFree
	}
	if observed(m.MetricStates, metricMemoryBreakdown) {
		out["app_memory"] = m.AppMemory
		out["wired_memory"] = m.WiredMemory
		out["compressed_memory"] = m.CompressedMemory
		out["cache_memory"] = m.CacheMemory
		out["purgeable_memory"] = m.PurgeableMemory
	}
	return json.Marshal(out)
}

func (n NetworkInfo) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"download_history": n.DownloadHistory, "upload_history": n.UploadHistory,
		"last_update": n.LastUpdate, "metric_states": n.MetricStates,
	}
	if observed(n.MetricStates, metricNetworkIO) {
		out["bytes_sent"] = n.BytesSent
		out["bytes_recv"] = n.BytesRecv
		out["packets_sent"] = n.PacketsSent
		out["packets_recv"] = n.PacketsRecv
	}
	if observed(n.MetricStates, metricNetworkRate) {
		out["bytes_sent_per_sec"] = n.BytesSentPerSec
		out["bytes_recv_per_sec"] = n.BytesRecvPerSec
	}
	return json.Marshal(out)
}

func (d DiskInfo) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"read_history": d.ReadHistory, "write_history": d.WriteHistory,
		"last_update": d.LastUpdate, "metric_states": d.MetricStates,
	}
	if observed(d.MetricStates, metricDiskParts) {
		out["partitions"] = d.Partitions
	}
	if observed(d.MetricStates, metricDiskIO) {
		out["read_bytes"] = d.ReadBytes
		out["write_bytes"] = d.WriteBytes
	}
	if observed(d.MetricStates, metricDiskRate) {
		out["read_per_sec"] = d.ReadPerSec
		out["write_per_sec"] = d.WritePerSec
	}
	return json.Marshal(out)
}

func (p ProcessInfo) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"pid": p.PID, "name": p.Name, "is_system": p.IsSystem,
		"is_protected": p.IsProtected, "metric_states": p.MetricStates,
	}
	if observed(p.MetricStates, metricProcessCPU) {
		out["cpu_percent"] = p.CPUPercent
	}
	if observed(p.MetricStates, metricProcessMemory) {
		out["memory"] = p.Memory
	}
	if observed(p.MetricStates, metricProcessMemPct) {
		out["memory_percent"] = p.MemoryPercent
	}
	if observed(p.MetricStates, metricProcessThread) {
		out["threads"] = p.Threads
	}
	if observed(p.MetricStates, metricProcessUser) {
		out["user"] = p.User
	}
	if p.Status != "" {
		out["status"] = p.Status
	}
	if observed(p.MetricStates, metricProcessParent) {
		out["parent"] = p.Parent
	}
	// Preserve legacy manually-constructed ProcessInfo values while making an
	// observed zero distinguishable from an unsupported/unavailable metric.
	ioStatus, ioDeclared := p.MetricStates[metricProcessIO]
	if p.IOReadBytes != 0 || (ioDeclared && ioStatus.State == MetricObserved) {
		out["io_read_bytes"] = p.IOReadBytes
	}
	if p.IOWriteBytes != 0 || (ioDeclared && ioStatus.State == MetricObserved) {
		out["io_write_bytes"] = p.IOWriteBytes
	}
	return json.Marshal(out)
}

func (t TemperatureInfo) MarshalJSON() ([]byte, error) {
	out := map[string]any{
		"state": t.State, "available": t.Available, "source": t.Source,
		"history": t.History, "last_update": t.LastUpdate,
	}
	if t.State.State == "" || t.State.State == MetricObserved {
		out["cpu_package"] = t.CPUPackage
		out["cpu_cores"] = t.CPUCores
		out["gpu"] = t.GPU
		out["ane"] = t.ANE
		out["battery"] = t.Battery
		out["ambient"] = t.Ambient
		out["fan_rpm"] = t.FanRPM
		out["fan_mode"] = t.FanMode
	}
	return json.Marshal(out)
}

// ProtectedProcessNames are macOS processes monitor will not kill.
var ProtectedProcessNames = map[string]bool{
	"launchd":        true,
	"kernel_task":    true,
	"init":           true,
	"System":         true,
	"loginwindow":    true,
	"WindowServer":   true,
	"Finder":         true,
	"Dock":           true,
	"SystemUIServer": true,
	"coreaudiod":     true,
	"powerd":         true,
	"thermald":       true,
	"kernelmanagerd": true,
	"syspolicyd":     true,
	"trustd":         true,
	"securityd":      true,
}

// CriticalProcessNames is a smaller set of "never kill" processes.
var CriticalProcessNames = map[string]bool{
	"launchd":     true,
	"kernel_task": true,
	"init":        true,
}

// IsProtectedProcess is the single definition of "protected" shared by the
// collector (which serializes it as is_protected) and the kill safety gate, so
// the value shown on every read surface matches what kill will actually refuse.
// pid 1 and the low PID range are kernel/early-boot processes.
func IsProtectedProcess(name string, pid int32) bool {
	return ProtectedProcessNames[name] || pid == 1 || pid < 100
}

// IsSystemProcess is the single definition of "system-owned", shared the same way.
func IsSystemProcess(user string) bool {
	return user == "root" || user == "_mbsetupuser"
}
