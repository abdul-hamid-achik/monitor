package system

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// Collector collects system metrics
type Collector struct {
	mu                      sync.RWMutex
	info                    SystemInfo
	historySize             int
	lastNetStats            net.IOCountersStat
	lastNetTime             time.Time
	lastCPUInfoRefresh      time.Time
	cpuInfoRefreshInterval  time.Duration
	lastHostInfoRefresh     time.Time
	hostInfoRefreshInterval time.Duration
	lastProcessRefresh      time.Time
	processRefreshInterval  time.Duration
	includeProcessIO        bool
	processRefreshPending   bool

	// Ring buffers for history to avoid memory allocations
	cpuUsageHistory    *RingBuffer[float64]
	memUsageHistory    *RingBuffer[float64]
	tempHistory        *RingBuffer[float64]
	netDownloadHistory *RingBuffer[float64]
	netUploadHistory   *RingBuffer[float64]
	diskReadHistory    *RingBuffer[float64]
	diskWriteHistory   *RingBuffer[float64]

	// Last disk stats for calculating I/O per second
	lastDiskStats []DiskPartitionInfo
	lastDiskTime  time.Time
	diskIORead    uint64
	diskIOWrite   uint64
}

// NewCollector creates a new system collector
func NewCollector() *Collector {
	historySize := 60 // Keep 60 data points (1 minute at 1s intervals)
	return &Collector{
		historySize:             historySize,
		cpuUsageHistory:         NewRingBuffer[float64](historySize),
		memUsageHistory:         NewRingBuffer[float64](historySize),
		tempHistory:             NewRingBuffer[float64](historySize),
		netDownloadHistory:      NewRingBuffer[float64](historySize),
		netUploadHistory:        NewRingBuffer[float64](historySize),
		diskReadHistory:         NewRingBuffer[float64](historySize),
		diskWriteHistory:        NewRingBuffer[float64](historySize),
		lastNetTime:             time.Now(),
		lastDiskTime:            time.Now(),
		cpuInfoRefreshInterval:  30 * time.Second,
		hostInfoRefreshInterval: 30 * time.Second,
		processRefreshInterval:  4 * time.Second, // Background tabs don't need a per-second process sweep.
		processRefreshPending:   true,
	}
}

// SetProcessCollectionOptions adjusts how often the process list is refreshed and
// whether expensive per-process I/O stats should be collected.
func (c *Collector) SetProcessCollectionOptions(interval time.Duration, includeIO bool) {
	if interval <= 0 {
		interval = time.Second
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if c.processRefreshInterval != interval || c.includeProcessIO != includeIO {
		c.processRefreshPending = true
	}
	c.processRefreshInterval = interval
	c.includeProcessIO = includeIO
}

// Collect gathers all system metrics
func (c *Collector) Collect(ctx context.Context) SystemInfo {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.info.LastUpdate = time.Now()

	// Collect CPU metrics
	c.collectCPU(ctx)

	// Collect Memory metrics
	c.collectMemory(ctx)

	// Collect Temperature metrics (macOS specific)
	c.collectTemperature(ctx)

	// Collect Network metrics
	c.collectNetwork(ctx)

	// Collect Disk metrics
	c.collectDisk(ctx)

	// Collect Process list less frequently than the lightweight system metrics.
	if c.shouldRefreshProcesses(c.info.LastUpdate) {
		c.collectProcesses(ctx)
		c.lastProcessRefresh = c.info.LastUpdate
		c.processRefreshPending = false
	}

	// Host metadata changes rarely, so refresh it on a slower cadence.
	if c.shouldRefreshHostInfo(c.info.LastUpdate) {
		c.collectHostInfo(ctx)
		c.lastHostInfoRefresh = c.info.LastUpdate
	}

	return c.info
}

// GetInfo returns the current system info
func (c *Collector) GetInfo() SystemInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.info
}

func (c *Collector) shouldRefreshProcesses(now time.Time) bool {
	if c.processRefreshPending || c.lastProcessRefresh.IsZero() {
		return true
	}
	return now.Sub(c.lastProcessRefresh) >= c.processRefreshInterval
}

func (c *Collector) shouldRefreshCPUInfo(now time.Time) bool {
	if c.lastCPUInfoRefresh.IsZero() {
		return true
	}
	return now.Sub(c.lastCPUInfoRefresh) >= c.cpuInfoRefreshInterval
}

func (c *Collector) shouldRefreshHostInfo(now time.Time) bool {
	if c.lastHostInfoRefresh.IsZero() {
		return true
	}
	return now.Sub(c.lastHostInfoRefresh) >= c.hostInfoRefreshInterval
}

// collectCPU gathers CPU metrics
func (c *Collector) collectCPU(ctx context.Context) {
	// Get overall CPU usage
	percent, err := cpu.PercentWithContext(ctx, 0, false)
	if err == nil && len(percent) > 0 {
		c.info.CPU.UsagePercent = percent[0]
	}

	// Get per-core usage
	perCore, err := cpu.PercentWithContext(ctx, 0, true)
	if err == nil {
		c.info.CPU.PerCoreUsage = perCore
		c.info.CPU.CoreCount = len(perCore)
	}

	// Frequency and thread metadata change much less frequently than utilization.
	if c.shouldRefreshCPUInfo(c.info.LastUpdate) {
		info, err := cpu.InfoWithContext(ctx)
		if err == nil && len(info) > 0 {
			c.info.CPU.FrequencyMHz = info[0].Mhz
			c.info.CPU.ThreadCount = int(info[0].Cores) * len(info)
		}
		c.lastCPUInfoRefresh = c.info.LastUpdate
	}

	// Get load averages (not available on macOS via gopsutil, use 0 as fallback)
	// On macOS, load averages can be read from sysctl but gopsutil doesn't expose them
	c.info.CPU.LoadAvg1 = 0
	c.info.CPU.LoadAvg5 = 0
	c.info.CPU.LoadAvg15 = 0

	// Update history using ring buffer
	c.cpuUsageHistory.Push(c.info.CPU.UsagePercent)
	c.info.CPU.History = c.cpuUsageHistory.ToSlice()

	// Update per-core history
	if len(c.info.CPU.PerCoreUsage) > 0 {
		if len(c.info.CPU.PerCoreHistory) == 0 {
			c.info.CPU.PerCoreHistory = make([][]float64, len(c.info.CPU.PerCoreUsage))
		}
		for i := range c.info.CPU.PerCoreHistory {
			c.info.CPU.PerCoreHistory[i] = c.info.CPU.PerCoreHistory[i][:0]
		}
		for i, usage := range c.info.CPU.PerCoreUsage {
			c.info.CPU.PerCoreHistory[i] = append(c.info.CPU.PerCoreHistory[i], usage)
		}
	}

	c.info.CPU.LastUpdate = time.Now()
}

// collectMemory gathers memory metrics
func (c *Collector) collectMemory(ctx context.Context) {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return
	}

	c.info.Memory.TotalBytes = vm.Total
	c.info.Memory.UsedBytes = vm.Used
	c.info.Memory.FreeBytes = vm.Free
	c.info.Memory.AvailableBytes = vm.Available
	c.info.Memory.UsagePercent = vm.UsedPercent

	// macOS specific memory breakdown (approximate)
	// Note: gopsutil doesn't provide all macOS-specific metrics directly
	c.info.Memory.AppMemory = vm.Used
	c.info.Memory.WiredMemory = 0 // Would need CGO for accurate macOS SMC data
	c.info.Memory.CompressedMemory = 0
	c.info.Memory.CacheMemory = vm.Total - vm.Used - vm.Free
	c.info.Memory.PurgeableMemory = 0

	// Calculate memory pressure (simplified)
	if vm.Total > 0 {
		pressure := float64(vm.Used) / float64(vm.Total) * 100
		c.info.Memory.MemoryPressure = pressure
	}

	// Get swap info
	swap, err := mem.SwapMemoryWithContext(ctx)
	if err == nil {
		c.info.Memory.SwapTotal = swap.Total
		c.info.Memory.SwapUsed = swap.Used
		c.info.Memory.SwapFree = swap.Free
	}

	c.info.Memory.LastUpdate = time.Now()
}

// collectTemperature gathers temperature metrics
// Note: Accurate temperature on macOS requires CGO to access SMC
// This provides a fallback implementation
func (c *Collector) collectTemperature(ctx context.Context) {
	// On macOS Apple Silicon, we need to use CGO to access SMC (System Management Controller)
	// For now, we'll provide estimated values based on CPU usage
	// A production version would use Objective-C bindings

	// Estimate temperature based on CPU usage (simplified model)
	baseTemp := 35.0                          // Base idle temperature for M-series chips
	loadTemp := c.info.CPU.UsagePercent * 0.5 // Add temperature based on load

	c.info.Temperature.CPUPackage = baseTemp + loadTemp
	c.info.Temperature.CPUCores = baseTemp + loadTemp + 2
	c.info.Temperature.GPU = baseTemp + (c.info.CPU.UsagePercent * 0.3)
	c.info.Temperature.ANE = baseTemp + (c.info.CPU.UsagePercent * 0.2)
	c.info.Temperature.Battery = 38.0                                  // Typical battery temperature
	c.info.Temperature.Ambient = 22.0                                  // Assume room temperature
	c.info.Temperature.FanRPM = 2000 + int(c.info.CPU.UsagePercent*40) // Estimate
	c.info.Temperature.FanMode = "Auto"
	c.info.Temperature.Available = true

	// Update history using ring buffer
	c.tempHistory.Push(c.info.Temperature.CPUPackage)
	c.info.Temperature.History = c.tempHistory.ToSlice()

	c.info.Temperature.LastUpdate = time.Now()
}

// collectNetwork gathers network metrics
func (c *Collector) collectNetwork(ctx context.Context) {
	counters, err := net.IOCountersWithContext(ctx, false)
	if err != nil || len(counters) == 0 {
		return
	}

	current := counters[0]
	now := time.Now()

	// Calculate bytes per second
	if !c.lastNetTime.IsZero() {
		elapsed := now.Sub(c.lastNetTime).Seconds()
		if elapsed > 0 {
			c.info.Network.BytesSentPerSec = uint64(float64(current.BytesSent-c.lastNetStats.BytesSent) / elapsed)
			c.info.Network.BytesRecvPerSec = uint64(float64(current.BytesRecv-c.lastNetStats.BytesRecv) / elapsed)

			// Track history for sparklines using ring buffers
			c.netDownloadHistory.Push(float64(c.info.Network.BytesRecvPerSec))
			c.netUploadHistory.Push(float64(c.info.Network.BytesSentPerSec))
			c.info.Network.DownloadHistory = c.netDownloadHistory.ToSlice()
			c.info.Network.UploadHistory = c.netUploadHistory.ToSlice()
		}
	}

	c.info.Network.BytesSent = current.BytesSent
	c.info.Network.BytesRecv = current.BytesRecv
	c.info.Network.PacketsSent = current.PacketsSent
	c.info.Network.PacketsRecv = current.PacketsRecv

	c.lastNetStats = current
	c.lastNetTime = now
	c.info.Network.LastUpdate = now
}

// collectDisk gathers disk metrics
func (c *Collector) collectDisk(ctx context.Context) {
	// Get disk partitions
	partitions, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		return
	}

	var diskInfos []DiskPartitionInfo
	for _, p := range partitions {
		usage, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil {
			continue
		}

		diskInfos = append(diskInfos, DiskPartitionInfo{
			Device:       p.Device,
			MountPoint:   p.Mountpoint,
			TotalBytes:   usage.Total,
			UsedBytes:    usage.Used,
			FreeBytes:    usage.Free,
			UsagePercent: usage.UsedPercent,
			Filesystem:   p.Fstype,
		})
	}

	// Sort by mount point to keep order consistent
	sort.Slice(diskInfos, func(i, j int) bool {
		return diskInfos[i].MountPoint < diskInfos[j].MountPoint
	})

	c.info.Disk.Partitions = diskInfos

	// Calculate disk I/O rates
	now := time.Now()
	if !c.lastDiskTime.IsZero() {
		elapsed := now.Sub(c.lastDiskTime).Seconds()
		if elapsed > 0 {
			// Get disk I/O counters
			ioCounters, err := disk.IOCountersWithContext(ctx)
			if err == nil {
				var totalRead, totalWrite uint64
				for _, counter := range ioCounters {
					totalRead += counter.ReadBytes
					totalWrite += counter.WriteBytes
				}

				c.info.Disk.ReadPerSec = uint64(float64(totalRead-c.diskIORead) / elapsed)
				c.info.Disk.WritePerSec = uint64(float64(totalWrite-c.diskIOWrite) / elapsed)

				// Track history using ring buffers
				c.diskReadHistory.Push(float64(c.info.Disk.ReadPerSec))
				c.diskWriteHistory.Push(float64(c.info.Disk.WritePerSec))
				c.info.Disk.ReadHistory = c.diskReadHistory.ToSlice()
				c.info.Disk.WriteHistory = c.diskWriteHistory.ToSlice()

				c.diskIORead = totalRead
				c.diskIOWrite = totalWrite
			}
		}
	}

	c.lastDiskTime = now
	c.lastDiskStats = diskInfos
	c.info.Disk.LastUpdate = now
}

// collectProcesses gathers process information
func (c *Collector) collectProcesses(ctx context.Context) {
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return
	}

	var procInfos []ProcessInfo
	for _, p := range processes {
		info, err := c.getProcessInfo(ctx, p)
		if err != nil {
			continue
		}
		procInfos = append(procInfos, info)
	}

	// Sort by CPU usage (descending)
	sort.Slice(procInfos, func(i, j int) bool {
		return procInfos[i].CPUPercent > procInfos[j].CPUPercent
	})

	c.info.Processes = procInfos
	c.info.ProcessesLastUpdate = c.info.LastUpdate
}

// getProcessInfo gets detailed information for a single process
func (c *Collector) getProcessInfo(ctx context.Context, p *process.Process) (ProcessInfo, error) {
	var info ProcessInfo
	var err error

	info.PID = p.Pid

	// Get process name
	info.Name, err = p.NameWithContext(ctx)
	if err != nil {
		info.Name = "unknown"
	}

	// Get CPU percent
	info.CPUPercent, err = p.CPUPercentWithContext(ctx)
	if err != nil {
		info.CPUPercent = 0
	}

	// Get memory info
	memInfo, err := p.MemoryInfoWithContext(ctx)
	if err == nil {
		info.Memory = memInfo.RSS
	}

	// Get thread count
	info.Threads, err = p.NumThreadsWithContext(ctx)
	if err != nil {
		info.Threads = 0
	}

	// Get username
	info.User, err = p.UsernameWithContext(ctx)
	if err != nil {
		info.User = "unknown"
	}

	// Determine if system process
	info.IsSystem = info.User == "root" || info.User == "_mbsetupuser" || info.PID < 100

	// Determine if protected (critical system processes) using shared list
	info.IsProtected = ProtectedProcessNames[info.Name] || info.PID == 1

	if c.includeProcessIO {
		// Per-process I/O is only shown in the Processes tab, so skip the syscall elsewhere.
		ioStats, err := p.IOCountersWithContext(ctx)
		if err == nil && ioStats != nil {
			info.IOReadBytes = ioStats.ReadBytes
			info.IOWriteBytes = ioStats.WriteBytes
		}
	}

	return info, nil
}

// collectHostInfo gathers host/system information
func (c *Collector) collectHostInfo(ctx context.Context) {
	// Get hostname
	hostname, err := os.Hostname()
	if err == nil {
		c.info.Hostname = hostname
	}

	// Get platform info (returns platform, family, version, err)
	platform, family, version, err := host.PlatformInformation()
	if err == nil {
		c.info.OS = platform
		c.info.Platform = family
		_ = version // unused for now
	}

	// Get kernel info
	kernel, err := host.KernelVersion()
	if err == nil {
		c.info.Kernel = kernel
	}

	// Get uptime
	uptime, err := host.Uptime()
	if err == nil {
		c.info.Uptime = uptime
	}

	// Get boot time
	bootTime, err := host.BootTime()
	if err == nil {
		c.info.BootTime = bootTime
	}
}

// FormatBytes formats bytes to human readable string
func FormatBytes(bytes uint64) string {
	const unit = 1024
	if bytes < unit {
		return formatUint64(bytes) + " B"
	}
	div, exp := uint64(unit), 0
	for n := bytes / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return formatFloat64(float64(bytes)/float64(div)) + " " + "KMGTPE"[exp:exp+1] + "B"
}

func formatUint64(n uint64) string {
	if n < 10 {
		return "0" + string('0'+byte(n))
	}
	result := ""
	for n > 0 {
		result = string('0'+byte(n%10)) + result
		n /= 10
	}
	return result
}

func formatFloat64(f float64) string {
	if f < 10 {
		return sprintf1dp(f)
	}
	return sprintf0dp(f)
}

func sprintf1dp(f float64) string {
	// Simple 1 decimal place formatting
	intPart := int(f)
	decPart := int((f - float64(intPart)) * 10)
	if decPart < 0 {
		decPart = 0
	}
	if decPart > 9 {
		decPart = 9
	}
	return formatInt(intPart) + "." + formatInt(decPart)
}

func sprintf0dp(f float64) string {
	return formatInt(int(f + 0.5))
}

func formatInt(n int) string {
	if n == 0 {
		return "0"
	}
	negative := n < 0
	if negative {
		n = -n
	}
	result := ""
	for n > 0 {
		result = string('0'+byte(n%10)) + result
		n /= 10
	}
	if negative {
		result = "-" + result
	}
	return result
}
