package collector

import "time"

// CPUInfo holds CPU usage statistics.
type CPUInfo struct {
	UsagePercent   float64    `json:"usage_percent"`
	PerCoreUsage   []float64  `json:"per_core_usage"`
	FrequencyMHz   float64    `json:"frequency_mhz"`
	CoreCount      int        `json:"core_count"`
	ThreadCount    int        `json:"thread_count"`
	LoadAvg1       float64    `json:"load_avg_1"`
	LoadAvg5       float64    `json:"load_avg_5"`
	LoadAvg15      float64    `json:"load_avg_15"`
	History        []float64  `json:"history"`
	PerCoreHistory [][]float64 `json:"per_core_history,omitempty"`
	LastUpdate     time.Time  `json:"last_update"`
}

// MemoryInfo holds RAM and swap statistics.
type MemoryInfo struct {
	TotalBytes       uint64    `json:"total_bytes"`
	UsedBytes        uint64    `json:"used_bytes"`
	FreeBytes        uint64    `json:"free_bytes"`
	AvailableBytes   uint64    `json:"available_bytes"`
	UsagePercent     float64   `json:"usage_percent"`
	SwapTotal        uint64    `json:"swap_total"`
	SwapUsed         uint64    `json:"swap_used"`
	SwapFree         uint64    `json:"swap_free"`
	MemoryPressure   float64   `json:"memory_pressure"`
	AppMemory        uint64    `json:"app_memory"`
	WiredMemory      uint64    `json:"wired_memory"`
	CompressedMemory uint64    `json:"compressed_memory"`
	CacheMemory      uint64    `json:"cache_memory"`
	PurgeableMemory  uint64    `json:"purgeable_memory"`
	History          []float64 `json:"history"`
	LastUpdate       time.Time `json:"last_update"`
}

// TemperatureInfo holds sensor readings (estimated on non-SMC builds).
type TemperatureInfo struct {
	CPUPackage float64    `json:"cpu_package"`
	CPUCores   float64    `json:"cpu_cores"`
	GPU        float64    `json:"gpu"`
	ANE        float64    `json:"ane"`
	Battery    float64    `json:"battery"`
	Ambient    float64    `json:"ambient"`
	FanRPM     int        `json:"fan_rpm"`
	FanMode    string     `json:"fan_mode"`
	History    []float64  `json:"history"`
	LastUpdate time.Time  `json:"last_update"`
	Available  bool       `json:"available"`
	// Source names the data origin: "estimated" (CPU-load heuristic) or
	// "powermetrics" (real SMC readings via sudo powermetrics). Lets
	// callers badge the UI and lets JSON consumers distinguish synthetic
	// from real values without out-of-band knowledge.
	Source string `json:"source"`
}

// NetworkInfo holds aggregate network statistics.
type NetworkInfo struct {
	BytesSent       uint64    `json:"bytes_sent"`
	BytesRecv       uint64    `json:"bytes_recv"`
	PacketsSent     uint64    `json:"packets_sent"`
	PacketsRecv     uint64    `json:"packets_recv"`
	BytesSentPerSec uint64    `json:"bytes_sent_per_sec"`
	BytesRecvPerSec uint64    `json:"bytes_recv_per_sec"`
	DownloadHistory []float64 `json:"download_history"`
	UploadHistory   []float64 `json:"upload_history"`
	LastUpdate      time.Time `json:"last_update"`
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
	Partitions   []DiskPartitionInfo `json:"partitions"`
	ReadBytes    uint64              `json:"read_bytes"`
	WriteBytes   uint64              `json:"write_bytes"`
	ReadPerSec   uint64              `json:"read_per_sec"`
	WritePerSec  uint64              `json:"write_per_sec"`
	ReadHistory  []float64           `json:"read_history"`
	WriteHistory []float64           `json:"write_history"`
	LastUpdate   time.Time           `json:"last_update"`
}

// ProcessInfo describes one OS process.
type ProcessInfo struct {
	PID           int32  `json:"pid"`
	Name          string `json:"name"`
	CPUPercent    float64 `json:"cpu_percent"`
	Memory        uint64  `json:"memory"`
	MemoryPercent float64 `json:"memory_percent"`
	Threads       int32   `json:"threads"`
	User          string  `json:"user"`
	Status        string  `json:"status,omitempty"`
	Parent        int32   `json:"parent,omitempty"`
	IsSystem      bool    `json:"is_system"`
	IsProtected   bool    `json:"is_protected"`
	IOReadBytes   uint64  `json:"io_read_bytes,omitempty"`
	IOWriteBytes  uint64  `json:"io_write_bytes,omitempty"`
}

// SystemInfo aggregates all metric families.
type SystemInfo struct {
	CPU                 CPUInfo        `json:"cpu"`
	Memory              MemoryInfo     `json:"memory"`
	Temperature         TemperatureInfo `json:"temperature"`
	Network             NetworkInfo    `json:"network"`
	Disk                DiskInfo       `json:"disk"`
	Processes           []ProcessInfo  `json:"processes"`
	ProcessesLastUpdate time.Time      `json:"processes_last_update"`
	Hostname            string         `json:"hostname"`
	OS                  string         `json:"os"`
	Platform            string         `json:"platform"`
	Kernel              string         `json:"kernel"`
	Uptime              uint64         `json:"uptime"`
	BootTime            uint64         `json:"boot_time"`
	LastUpdate          time.Time      `json:"last_update"`
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