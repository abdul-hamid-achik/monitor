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
	PID           int32
	Name          string
	CPUPercent    float64
	Memory        uint64
	MemoryPercent float64
	Threads       int32
	User          string
	Status        string
	CreateTime    int64
	Parent        int32
	IsSystem      bool
	IsProtected   bool   // Cannot be killed safely
	IOReadBytes   uint64 // Process I/O bytes read
	IOWriteBytes  uint64 // Process I/O bytes written
	Connections   int32  // Active network connections
}

// SystemInfo aggregates all system metrics
type SystemInfo struct {
	CPU                 CPUInfo
	Memory              MemoryInfo
	Temperature         TemperatureInfo
	Network             NetworkInfo
	Disk                DiskInfo
	Processes           []ProcessInfo
	ProcessesLastUpdate time.Time
	Hostname            string
	OS                  string
	Platform            string
	Kernel              string
	Uptime              uint64
	BootTime            uint64
	LastUpdate          time.Time
}

// KillConfirmation contains information for safe process termination
type KillConfirmation struct {
	Processes      []ProcessInfo
	HasProtected   bool
	HasSystem      bool
	RequiresSudo   bool
	SafetyWarnings []string
}

// DiskPartitionInfo contains information about a disk partition/mount point
type DiskPartitionInfo struct {
	Device       string // e.g., "/dev/disk1s1"
	MountPoint   string // e.g., "/"
	TotalBytes   uint64
	UsedBytes    uint64
	FreeBytes    uint64
	UsagePercent float64
	Filesystem   string // e.g., "apfs", "hfs"
}

// DiskInfo contains disk metrics
type DiskInfo struct {
	Partitions   []DiskPartitionInfo
	ReadBytes    uint64
	WriteBytes   uint64
	ReadPerSec   uint64
	WritePerSec  uint64
	ReadHistory  []float64
	WriteHistory []float64
	LastUpdate   time.Time
}
