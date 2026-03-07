package system

import (
	"time"
)

// CPUInfo contains CPU metrics
type CPUInfo struct {
	UsagePercent   float64
	PerCoreUsage   []float64
	FrequencyMHz   float64
	CoreCount      int
	ThreadCount    int
	LoadAvg1       float64
	LoadAvg5       float64
	LoadAvg15      float64
	History        []float64
	PerCoreHistory [][]float64
	LastUpdate     time.Time
}

// MemoryInfo contains memory metrics
type MemoryInfo struct {
	TotalBytes       uint64
	UsedBytes        uint64
	FreeBytes        uint64
	AvailableBytes   uint64
	UsagePercent     float64
	SwapTotal        uint64
	SwapUsed         uint64
	SwapFree         uint64
	MemoryPressure   float64 // 0-100
	AppMemory        uint64
	WiredMemory      uint64
	CompressedMemory uint64
	CacheMemory      uint64
	PurgeableMemory  uint64
	LastUpdate       time.Time
}

// TemperatureInfo contains temperature sensor readings
type TemperatureInfo struct {
	CPUPackage float64
	CPUCores   float64
	GPU        float64
	ANE        float64 // Apple Neural Engine
	Battery    float64
	Ambient    float64
	FanRPM     int
	FanMode    string // "Auto" or "Manual"
	History    []float64
	LastUpdate time.Time
	Available  bool // Whether temperature sensors are accessible
}

// NetworkInfo contains network metrics
type NetworkInfo struct {
	BytesSent       uint64
	BytesRecv       uint64
	PacketsSent     uint64
	PacketsRecv     uint64
	BytesSentPerSec uint64
	BytesRecvPerSec uint64
	DownloadHistory []float64
	UploadHistory   []float64
	LastUpdate      time.Time
}

// ProcessInfo contains process information
type ProcessInfo struct {
	PID            int32
	Name           string
	CPUPercent     float64
	Memory         uint64
	MemoryPercent  float64
	Threads        int32
	User           string
	Status         string
	CreateTime     int64
	Parent         int32
	IsSystem       bool
	IsProtected    bool // Cannot be killed safely
	BytesSent      uint64 // Network bytes sent
	BytesRecv      uint64 // Network bytes received
	Connections    int32  // Active network connections
}

// SystemInfo aggregates all system metrics
type SystemInfo struct {
	CPU         CPUInfo
	Memory      MemoryInfo
	Temperature TemperatureInfo
	Network     NetworkInfo
	Processes   []ProcessInfo
	Hostname    string
	OS          string
	Platform    string
	Kernel      string
	Uptime      uint64
	BootTime    uint64
	LastUpdate  time.Time
}

// KillConfirmation contains information for safe process termination
type KillConfirmation struct {
	Processes      []ProcessInfo
	HasProtected   bool
	HasSystem      bool
	RequiresSudo   bool
	SafetyWarnings []string
}
