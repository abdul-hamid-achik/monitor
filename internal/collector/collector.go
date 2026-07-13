// Package collector gathers system metrics on a tick and publishes them to
// subscribers. It is a pure-Go refactor of the old internal/system collector.
package collector

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/capability"
	"github.com/abdul-hamid-achik/monitor/internal/cgroup"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/net"
	"github.com/shirou/gopsutil/v4/process"
)

// Options configures a Collector.
type Options struct {
	Interval    time.Duration // tick interval (default 1s)
	HistorySize int           // samples retained for sparklines (default 60)
	// Capabilities and LoadAverage are injectable seams for cross-platform
	// tests. Nil selects detection and gopsutil collection for the current host.
	Capabilities *capability.Set
	LoadAverage  func(context.Context) (*load.AvgStat, error)
}

// Subscriber is a non-blocking callback invoked on every tick.
type Subscriber func(Event)

// Event is what subscribers receive.
type Event struct {
	Timestamp time.Time     `json:"timestamp"`
	Hostname  string        `json:"hostname"`
	CPU       CPUInfo       `json:"cpu"`
	Memory    MemoryInfo    `json:"memory"`
	Network   NetworkInfo   `json:"network"`
	Disk      DiskInfo      `json:"disk"`
	Processes []ProcessInfo `json:"processes"`
	Alert     *Alert        `json:"alert,omitempty"`
}

// Alert is an optional analyzer finding attached to an Event.
//
// Diagnosis is additive: nil when the analyzer window is too short or no
// cross-signal pattern matched. JSON consumers that ignore unknown keys are
// unaffected.
type Alert struct {
	Severity  string     `json:"severity"`
	Rule      string     `json:"rule"`
	PID       int32      `json:"pid,omitempty"`
	Process   string     `json:"process,omitempty"`
	Detail    string     `json:"detail"`
	Diagnosis *Diagnosis `json:"diagnosis,omitempty"`
}

// Diagnosis is the analyzer's interpretation of correlated per-process
// signals: what the pattern is, the numeric evidence behind it (slope, R²,
// sample counts — never discarded), how confident the classification is,
// and which monitor MCP tools to run next. It lives next to Alert (rather
// than in internal/analyzer, which builds it) so every Alert consumer —
// watch NDJSON, webhooks, incidents, MCP — can carry it without importing
// the analyzer and creating an import cycle.
type Diagnosis struct {
	Summary     string   `json:"summary"`
	Evidence    []string `json:"evidence"`
	Confidence  string   `json:"confidence"`   // "low" | "medium" | "high"
	NextActions []string `json:"next_actions"` // at most 2, each a concrete monitor MCP tool invocation
}

// Collector periodically samples the system and emits Events.
type Collector struct {
	opts Options

	// info is the in-progress sample, owned exclusively by the Collect
	// goroutine and mutated WITHOUT mu (so slow IO doesn't block readers).
	// published is the last consistent snapshot, swapped in under mu at the
	// end of each Collect; Snapshot() reads only published.
	mu           sync.RWMutex
	info         SystemInfo
	published    SystemInfo
	lastNet      net.IOCountersStat
	lastDisk     disk.IOCountersStat
	capabilities capability.Set
	loadAverage  func(context.Context) (*load.AvgStat, error)
	intervalCh   chan time.Duration

	cpuHist     *RingBuffer[float64]
	memHist     *RingBuffer[float64]
	netDownHist *RingBuffer[float64]
	netUpHist   *RingBuffer[float64]
	diskRHist   *RingBuffer[float64]
	diskWHist   *RingBuffer[float64]

	subsMu   sync.RWMutex
	subs     map[int]Subscriber
	subsNext int

	// temperatureHook, if set, overrides the default CPU-load estimation
	// for temperature readings. It returns the package-internal Reading
	// shape; the collector translates the values into TemperatureInfo and
	// stamps Source so consumers can badge the UI. Set via
	// WithTemperatureHook (typically to a temperature.Source backed by
	// sudo powermetrics).
	temperatureHook func() (tempCPUPackage, tempCPUCores, tempGPU, tempANE, tempBattery, tempAmbient float64, tempFanRPM int, tempFanMode, tempSource string, tempAvailable bool)
}

// New creates a Collector with the given options.
func New(opts Options) *Collector {
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.HistorySize <= 0 {
		opts.HistorySize = 60
	}
	caps := capability.Current()
	if opts.Capabilities != nil {
		caps = *opts.Capabilities
	}
	loadAverage := opts.LoadAverage
	if loadAverage == nil {
		loadAverage = load.AvgWithContext
	}
	return &Collector{
		opts:         opts,
		subs:         make(map[int]Subscriber),
		cpuHist:      NewRingBuffer[float64](opts.HistorySize),
		memHist:      NewRingBuffer[float64](opts.HistorySize),
		netDownHist:  NewRingBuffer[float64](opts.HistorySize),
		netUpHist:    NewRingBuffer[float64](opts.HistorySize),
		diskRHist:    NewRingBuffer[float64](opts.HistorySize),
		diskWHist:    NewRingBuffer[float64](opts.HistorySize),
		capabilities: caps,
		loadAverage:  loadAverage,
		intervalCh:   make(chan time.Duration, 1),
	}
}

// Subscribe registers fn to receive every tick. The returned func
// unsubscribes it; funcs aren't comparable in Go, so subscribers are keyed
// by an opaque token and the cancel closure deletes that key. Calling the
// cancel more than once is harmless.
func (c *Collector) Subscribe(fn Subscriber) func() {
	c.subsMu.Lock()
	defer c.subsMu.Unlock()
	if c.subs == nil {
		c.subs = make(map[int]Subscriber)
	}
	id := c.subsNext
	c.subsNext++
	c.subs[id] = fn
	return func() {
		c.subsMu.Lock()
		defer c.subsMu.Unlock()
		delete(c.subs, id)
	}
}

// SetInterval changes the tick interval used by Run. An active Run loop resets
// its ticker immediately; callers do not need to restart the collector.
// Values <= 0 are ignored.
func (c *Collector) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.opts.Interval = d
	c.mu.Unlock()
	// Keep only the newest requested interval. The channel is buffered so a
	// settings edit never blocks while Run is collecting a slow sample.
	select {
	case c.intervalCh <- d:
	default:
		select {
		case <-c.intervalCh:
		default:
		}
		select {
		case c.intervalCh <- d:
		default:
		}
	}
}

// Snapshot returns the latest published SystemInfo. It only contends with
// the brief publish step in Collect, never with the slow lock-free sampling,
// so the TUI render loop is never blocked by process enumeration.
func (c *Collector) Snapshot() SystemInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.published
}

// Capture is the strict one-shot API. It validates required platform
// capabilities before invoking any collector and returns a structured
// unsupported/unavailable snapshot together with the capability error.
func (c *Collector) Capture(ctx context.Context) (SystemInfo, error) {
	if err := c.capabilities.Require(capability.SystemMetrics, capability.ProcessMetrics); err != nil {
		support := c.capabilities.SupportFor(capability.SystemMetrics)
		if c.capabilities.SupportFor(capability.ProcessMetrics).State != capability.Supported {
			support = c.capabilities.SupportFor(capability.ProcessMetrics)
		}
		c.info = SystemInfo{
			Capture:      statusFromCapability(support),
			Capabilities: c.capabilities.Items,
		}
		c.mu.Lock()
		c.published = c.info
		info := c.published
		c.mu.Unlock()
		return info, err
	}

	c.info.LastUpdate = time.Now()
	c.info.Capture = metricStatus(MetricObserved, "")
	c.info.Capabilities = c.capabilities.Items
	c.info.CPU.MetricStates = make(map[string]MetricStatus)
	c.info.Memory.MetricStates = make(map[string]MetricStatus)
	c.info.Network.MetricStates = make(map[string]MetricStatus)
	c.info.Disk.MetricStates = make(map[string]MetricStatus)
	c.collectCPU(ctx)
	c.collectMemory(ctx)
	c.collectCgroup()
	// Push the memory sparkline AFTER any cgroup override so the plotted
	// history matches the reported UsagePercent.
	c.memHist.Push(c.info.Memory.UsagePercent)
	c.info.Memory.History = c.memHist.ToSlice()
	c.collectTemperature()
	c.collectNetwork(ctx)
	c.collectDisk(ctx)
	c.collectProcesses(ctx)
	c.collectHost()

	c.mu.Lock()
	c.published = c.info
	info := c.published
	c.mu.Unlock()

	c.subsMu.RLock()
	defer c.subsMu.RUnlock()
	for _, fn := range c.subs {
		fn(Event{
			Timestamp: info.LastUpdate,
			Hostname:  info.Hostname,
			CPU:       info.CPU,
			Memory:    info.Memory,
			Network:   info.Network,
			Disk:      info.Disk,
			Processes: info.Processes,
		})
	}
	return info, nil
}

// Collect preserves the original convenience API while delegating to Capture,
// so direct callers cannot bypass capability enforcement. Inspect
// SystemInfo.Capture when using this compatibility method.
func (c *Collector) Collect(ctx context.Context) SystemInfo {
	info, _ := c.Capture(ctx)
	return info
}

// Run blocks until ctx is cancelled, calling Collect on every Interval tick.
func (c *Collector) Run(ctx context.Context) error {
	c.mu.RLock()
	interval := c.opts.Interval
	c.mu.RUnlock()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case interval := <-c.intervalCh:
			t.Reset(interval)
		case <-t.C:
			if _, err := c.Capture(ctx); err != nil {
				return err
			}
		}
	}
}

func (c *Collector) collectCPU(ctx context.Context) {
	percent, err := cpu.PercentWithContext(ctx, 0, false)
	if err == nil && len(percent) > 0 {
		c.info.CPU.UsagePercent = percent[0]
		c.info.CPU.MetricStates[metricCPUUsage] = metricStatus(MetricObserved, "")
	} else {
		c.info.CPU.UsagePercent = 0
		reason := "CPU usage collector returned no samples"
		if err != nil {
			reason = err.Error()
		}
		c.info.CPU.MetricStates[metricCPUUsage] = metricStatus(MetricUnavailable, reason)
	}

	perCore, err := cpu.PercentWithContext(ctx, 0, true)
	if err == nil && len(perCore) > 0 {
		c.info.CPU.PerCoreUsage = perCore
		c.info.CPU.CoreCount = len(perCore)
		c.info.CPU.MetricStates[metricCPUPerCore] = metricStatus(MetricObserved, "")
	} else {
		c.info.CPU.PerCoreUsage = nil
		c.info.CPU.CoreCount = 0
		reason := "per-core CPU collector returned no samples"
		if err != nil {
			reason = err.Error()
		}
		c.info.CPU.MetricStates[metricCPUPerCore] = metricStatus(MetricUnavailable, reason)
	}

	info, err := cpu.InfoWithContext(ctx)
	if err == nil && len(info) > 0 {
		c.info.CPU.FrequencyMHz = info[0].Mhz
		c.info.CPU.ThreadCount = int(info[0].Cores) * len(info)
		c.info.CPU.MetricStates[metricCPUInfo] = metricStatus(MetricObserved, "")
	} else {
		c.info.CPU.FrequencyMHz = 0
		c.info.CPU.ThreadCount = 0
		reason := "CPU info collector returned no samples"
		if err != nil {
			reason = err.Error()
		}
		c.info.CPU.MetricStates[metricCPUInfo] = metricStatus(MetricUnavailable, reason)
	}

	c.info.CPU.LoadAvg1 = 0
	c.info.CPU.LoadAvg5 = 0
	c.info.CPU.LoadAvg15 = 0
	loadSupport := c.capabilities.SupportFor(capability.CPULoadAverage)
	if loadSupport.State != capability.Supported {
		c.info.CPU.MetricStates[metricCPULoad] = statusFromCapability(loadSupport)
	} else if avg, loadErr := c.loadAverage(ctx); loadErr != nil {
		c.info.CPU.MetricStates[metricCPULoad] = metricStatus(MetricUnavailable, loadErr.Error())
	} else if avg == nil {
		c.info.CPU.MetricStates[metricCPULoad] = metricStatus(MetricUnavailable, "load average collector returned no sample")
	} else {
		c.info.CPU.LoadAvg1 = avg.Load1
		c.info.CPU.LoadAvg5 = avg.Load5
		c.info.CPU.LoadAvg15 = avg.Load15
		c.info.CPU.MetricStates[metricCPULoad] = metricStatus(MetricObserved, "")
	}

	c.cpuHist.Push(c.info.CPU.UsagePercent)
	c.info.CPU.History = c.cpuHist.ToSlice()
	c.info.CPU.LastUpdate = time.Now()
}

func (c *Collector) collectMemory(ctx context.Context) {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		c.info.Memory.MetricStates[metricMemoryVirtual] = metricStatus(MetricUnavailable, err.Error())
		c.info.Memory.MetricStates[metricMemoryBreakdown] = metricStatus(MetricUnavailable, err.Error())
	} else {
		c.info.Memory.TotalBytes = vm.Total
		c.info.Memory.UsedBytes = vm.Used
		c.info.Memory.FreeBytes = vm.Free
		c.info.Memory.AvailableBytes = vm.Available
		c.info.Memory.UsagePercent = vm.UsedPercent
		if vm.Total > 0 {
			c.info.Memory.MemoryPressure = float64(vm.Used) / float64(vm.Total) * 100
		} else {
			c.info.Memory.MemoryPressure = 0
		}
		c.info.Memory.MetricStates[metricMemoryVirtual] = metricStatus(MetricObserved, "")
		// gopsutil's cross-platform VirtualMemoryStat does not identify the
		// app/wired/compressed/purgeable breakdown consistently.
		c.info.Memory.MetricStates[metricMemoryBreakdown] = metricStatus(MetricUnavailable, "platform memory breakdown is not exposed by this collector")
	}

	if swap, swapErr := mem.SwapMemoryWithContext(ctx); swapErr == nil {
		c.info.Memory.SwapTotal = swap.Total
		c.info.Memory.SwapUsed = swap.Used
		c.info.Memory.SwapFree = swap.Free
		c.info.Memory.MetricStates[metricMemorySwap] = metricStatus(MetricObserved, "")
	} else {
		c.info.Memory.SwapTotal = 0
		c.info.Memory.SwapUsed = 0
		c.info.Memory.SwapFree = 0
		c.info.Memory.MetricStates[metricMemorySwap] = metricStatus(MetricUnavailable, swapErr.Error())
	}
	// History is pushed after collectCgroup (in Capture) so the sparkline
	// reflects a container-relative value when cgroup limits are active.
	c.info.Memory.LastUpdate = time.Now()
}

// collectCgroup reads cgroup v2 limits and, when a memory limit is set (a
// container), re-reports memory against the limit instead of host RAM so usage
// reflects the container rather than the whole machine. No-op on the host /
// macOS (Active=false).
func (c *Collector) collectCgroup() {
	support := c.capabilities.SupportFor(capability.CgroupV2)
	if support.State != capability.Supported {
		c.info.Cgroup = CgroupInfo{State: statusFromCapability(support)}
		return
	}
	l := cgroup.Read()
	c.info.Cgroup = CgroupInfo{
		Limited:       l.Active,
		MemLimitBytes: l.MemLimit,
		MemUsageBytes: l.MemCurrent,
		CPUQuotaCores: l.CPUQuota,
		State:         metricStatus(MetricObserved, ""),
	}
	if l.MemLimit > 0 {
		free := uint64(0)
		if l.MemLimit > l.MemCurrent {
			free = l.MemLimit - l.MemCurrent
		}
		c.info.Memory.TotalBytes = l.MemLimit
		c.info.Memory.UsedBytes = l.MemCurrent
		c.info.Memory.FreeBytes = free
		c.info.Memory.AvailableBytes = free
		c.info.Memory.UsagePercent = float64(l.MemCurrent) / float64(l.MemLimit) * 100
		c.info.Memory.MemoryPressure = c.info.Memory.UsagePercent
	}
}

func (c *Collector) collectTemperature() {
	c.mu.RLock()
	hook := c.temperatureHook
	c.mu.RUnlock()
	if hook != nil {
		cpuPkg, cpuCores, gpu, ane, battery, ambient, fanRPM, fanMode, source, available := hook()
		c.info.Temperature = TemperatureInfo{
			CPUPackage: cpuPkg, CPUCores: cpuCores, GPU: gpu, ANE: ane,
			Battery: battery, Ambient: ambient, FanRPM: fanRPM, FanMode: fanMode,
			Source: source, Available: available, LastUpdate: time.Now(),
		}
		if available {
			c.info.Temperature.State = metricStatus(MetricObserved, "")
		} else {
			c.info.Temperature.State = metricStatus(MetricUnavailable, "temperature source returned no reading")
		}
		return
	}
	baseTemp := 35.0
	loadTemp := c.info.CPU.UsagePercent * 0.5
	c.info.Temperature = TemperatureInfo{
		CPUPackage: baseTemp + loadTemp,
		CPUCores:   baseTemp + loadTemp + 2,
		GPU:        baseTemp + c.info.CPU.UsagePercent*0.3,
		ANE:        baseTemp + c.info.CPU.UsagePercent*0.2,
		Battery:    38.0,
		Source:     "estimated",
		Available:  true,
		LastUpdate: time.Now(),
		State:      metricStatus(MetricObserved, ""),
	}
}

// WithTemperatureHook installs a function that supplies temperature
// readings on every tick. The hook returns individual values so the
// collector package doesn't need to import internal/temperature (which
// would create an import cycle once a downstream package wants both).
// Pass nil to clear the hook and revert to the default CPU-load
// estimate.
//
// The typical wiring is:
//
//	ts := temperature.New(ctx, temperature.Options{Logf: ...})
//	c.WithTemperatureHook(func() (float64, float64, float64, float64, float64, float64, int, string, string, bool) {
//	    r := ts.Latest()
//	    return r.CPUPackage, r.CPUCores, r.GPU, r.ANE, r.Battery, r.Ambient, r.FanRPM, r.FanMode, string(r.Source), r.Available
//	})
func (c *Collector) WithTemperatureHook(hook func() (float64, float64, float64, float64, float64, float64, int, string, string, bool)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.temperatureHook = hook
}

// perSecond converts a monotonic byte-counter delta into a per-second rate.
// It returns 0 when elapsed <= 0 (no divide-by-zero) or when the counter
// went backwards (interface reset / wrap), which would otherwise underflow
// the unsigned subtraction into a near-2^64 bogus rate.
func perSecond(prev, cur uint64, elapsed float64) uint64 {
	if elapsed <= 0 || cur < prev {
		return 0
	}
	return uint64(float64(cur-prev) / elapsed)
}

func (c *Collector) collectNetwork(ctx context.Context) {
	counters, err := net.IOCountersWithContext(ctx, false)
	if err != nil || len(counters) == 0 {
		reason := "network collector returned no counters"
		if err != nil {
			reason = err.Error()
		}
		c.info.Network.MetricStates[metricNetworkIO] = metricStatus(MetricUnavailable, reason)
		c.info.Network.MetricStates[metricNetworkRate] = metricStatus(MetricUnavailable, reason)
		return
	}
	cur := counters[0]
	now := time.Now()
	c.info.Network.MetricStates[metricNetworkIO] = metricStatus(MetricObserved, "")
	if !c.info.Network.LastUpdate.IsZero() {
		elapsed := now.Sub(c.info.Network.LastUpdate).Seconds()
		if elapsed > 0 {
			c.info.Network.BytesSentPerSec = perSecond(c.lastNet.BytesSent, cur.BytesSent, elapsed)
			c.info.Network.BytesRecvPerSec = perSecond(c.lastNet.BytesRecv, cur.BytesRecv, elapsed)
			c.netDownHist.Push(float64(c.info.Network.BytesRecvPerSec))
			c.netUpHist.Push(float64(c.info.Network.BytesSentPerSec))
			c.info.Network.MetricStates[metricNetworkRate] = metricStatus(MetricObserved, "")
		} else {
			c.info.Network.MetricStates[metricNetworkRate] = metricStatus(MetricUnavailable, "sampling interval was not positive")
		}
	} else {
		c.info.Network.MetricStates[metricNetworkRate] = metricStatus(MetricUnavailable, "first sample has no prior counter")
	}
	c.info.Network.BytesSent = cur.BytesSent
	c.info.Network.BytesRecv = cur.BytesRecv
	c.info.Network.PacketsSent = cur.PacketsSent
	c.info.Network.PacketsRecv = cur.PacketsRecv
	c.info.Network.DownloadHistory = c.netDownHist.ToSlice()
	c.info.Network.UploadHistory = c.netUpHist.ToSlice()
	c.lastNet = cur
	c.info.Network.LastUpdate = now
}

func (c *Collector) collectDisk(ctx context.Context) {
	parts, err := disk.PartitionsWithContext(ctx, false)
	if err != nil {
		c.info.Disk.Partitions = nil
		c.info.Disk.MetricStates[metricDiskParts] = metricStatus(MetricUnavailable, err.Error())
	} else {
		out := make([]DiskPartitionInfo, 0, len(parts))
		skipped := 0
		for _, p := range parts {
			u, usageErr := disk.UsageWithContext(ctx, p.Mountpoint)
			if usageErr != nil {
				skipped++
				continue
			}
			out = append(out, DiskPartitionInfo{
				Device: p.Device, MountPoint: p.Mountpoint, TotalBytes: u.Total,
				UsedBytes: u.Used, FreeBytes: u.Free, UsagePercent: u.UsedPercent,
				Filesystem: p.Fstype,
			})
		}
		sort.Slice(out, func(i, j int) bool { return out[i].MountPoint < out[j].MountPoint })
		c.info.Disk.Partitions = out
		reason := ""
		if skipped > 0 {
			reason = "one or more mounted filesystems were unavailable"
		}
		c.info.Disk.MetricStates[metricDiskParts] = metricStatus(MetricObserved, reason)
	}

	ioCounters, ioErr := disk.IOCountersWithContext(ctx)
	if ioErr != nil {
		c.info.Disk.MetricStates[metricDiskIO] = metricStatus(MetricUnavailable, ioErr.Error())
		c.info.Disk.MetricStates[metricDiskRate] = metricStatus(MetricUnavailable, ioErr.Error())
		c.info.Disk.LastUpdate = time.Now()
		return
	}
	var read, write uint64
	for _, counter := range ioCounters {
		read += counter.ReadBytes
		write += counter.WriteBytes
	}
	c.info.Disk.ReadBytes = read
	c.info.Disk.WriteBytes = write
	c.info.Disk.MetricStates[metricDiskIO] = metricStatus(MetricObserved, "")
	now := time.Now()
	if !c.info.Disk.LastUpdate.IsZero() {
		elapsed := now.Sub(c.info.Disk.LastUpdate).Seconds()
		if elapsed > 0 {
			c.info.Disk.ReadPerSec = perSecond(c.lastDisk.ReadBytes, read, elapsed)
			c.info.Disk.WritePerSec = perSecond(c.lastDisk.WriteBytes, write, elapsed)
			c.diskRHist.Push(float64(c.info.Disk.ReadPerSec))
			c.diskWHist.Push(float64(c.info.Disk.WritePerSec))
			c.info.Disk.ReadHistory = c.diskRHist.ToSlice()
			c.info.Disk.WriteHistory = c.diskWHist.ToSlice()
			c.info.Disk.MetricStates[metricDiskRate] = metricStatus(MetricObserved, "")
		} else {
			c.info.Disk.MetricStates[metricDiskRate] = metricStatus(MetricUnavailable, "sampling interval was not positive")
		}
	} else {
		c.info.Disk.MetricStates[metricDiskRate] = metricStatus(MetricUnavailable, "first sample has no prior counter")
	}
	c.lastDisk.ReadBytes = read
	c.lastDisk.WriteBytes = write
	c.info.Disk.LastUpdate = now
}

func (c *Collector) collectProcesses(ctx context.Context) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		c.info.Processes = nil
		c.info.ProcessesState = metricStatus(MetricUnavailable, err.Error())
		c.info.ProcessesLastUpdate = time.Time{}
		return
	}
	statusByPID, statusErr := bulkProcessStatuses(ctx)
	out := make([]ProcessInfo, 0, len(procs))
	for _, p := range procs {
		pi, processErr := c.processInfo(ctx, p, statusByPID, statusErr)
		if processErr != nil {
			continue
		}
		out = append(out, pi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPUPercent > out[j].CPUPercent })
	c.info.Processes = out
	c.info.ProcessesState = metricStatus(MetricObserved, "")
	c.info.ProcessesLastUpdate = c.info.LastUpdate
}

func (c *Collector) processInfo(ctx context.Context, p *process.Process, statusByPID map[int32]string, bulkStatusErr error) (ProcessInfo, error) {
	pi := ProcessInfo{PID: p.Pid, MetricStates: make(map[string]MetricStatus)}
	if name, err := p.NameWithContext(ctx); err == nil {
		pi.Name = name
		pi.MetricStates[metricProcessName] = metricStatus(MetricObserved, "")
	} else {
		pi.Name = "unknown"
		pi.MetricStates[metricProcessName] = metricStatus(MetricUnavailable, err.Error())
	}
	if value, err := p.CPUPercentWithContext(ctx); err == nil {
		pi.CPUPercent = value
		pi.MetricStates[metricProcessCPU] = metricStatus(MetricObserved, "")
	} else {
		pi.MetricStates[metricProcessCPU] = metricStatus(MetricUnavailable, err.Error())
	}
	if value, err := p.MemoryInfoWithContext(ctx); err == nil {
		pi.Memory = value.RSS
		pi.MetricStates[metricProcessMemory] = metricStatus(MetricObserved, "")
	} else {
		pi.MetricStates[metricProcessMemory] = metricStatus(MetricUnavailable, err.Error())
	}
	if value, err := p.MemoryPercentWithContext(ctx); err == nil {
		pi.MemoryPercent = float64(value)
		pi.MetricStates[metricProcessMemPct] = metricStatus(MetricObserved, "")
	} else {
		pi.MetricStates[metricProcessMemPct] = metricStatus(MetricUnavailable, err.Error())
	}
	if value, err := p.NumThreadsWithContext(ctx); err == nil {
		pi.Threads = value
		pi.MetricStates[metricProcessThread] = metricStatus(MetricObserved, "")
	} else {
		pi.MetricStates[metricProcessThread] = metricStatus(MetricUnavailable, err.Error())
	}
	if value, err := p.UsernameWithContext(ctx); err == nil {
		pi.User = value
		pi.IsSystem = IsSystemProcess(value)
		pi.MetricStates[metricProcessUser] = metricStatus(MetricObserved, "")
	} else {
		pi.MetricStates[metricProcessUser] = metricStatus(MetricUnavailable, err.Error())
	}
	if value, err := p.PpidWithContext(ctx); err == nil {
		pi.Parent = value
		pi.MetricStates[metricProcessParent] = metricStatus(MetricObserved, "")
	} else {
		pi.MetricStates[metricProcessParent] = metricStatus(MetricUnavailable, err.Error())
	}
	if runtime.GOOS == "darwin" {
		if value, ok := statusByPID[p.Pid]; ok && value != "" {
			pi.Status = value
			pi.MetricStates[metricProcessStatus] = metricStatus(MetricObserved, "")
		} else {
			reason := "process status was absent from the bulk ps sample"
			if bulkStatusErr != nil {
				reason = bulkStatusErr.Error()
			}
			pi.MetricStates[metricProcessStatus] = metricStatus(MetricUnavailable, reason)
		}
	} else if value, err := p.StatusWithContext(ctx); err == nil && len(value) > 0 {
		pi.Status = strings.Join(value, ",")
		pi.MetricStates[metricProcessStatus] = metricStatus(MetricObserved, "")
	} else {
		reason := "process status collector returned no state"
		if err != nil {
			reason = err.Error()
		}
		pi.MetricStates[metricProcessStatus] = metricStatus(MetricUnavailable, reason)
	}
	if runtime.GOOS == "darwin" {
		pi.MetricStates[metricProcessIO] = metricStatus(MetricUnsupported, "per-process I/O counters are not exposed by gopsutil on macOS")
	} else if value, err := p.IOCountersWithContext(ctx); err == nil && value != nil {
		pi.IOReadBytes = value.ReadBytes
		pi.IOWriteBytes = value.WriteBytes
		pi.MetricStates[metricProcessIO] = metricStatus(MetricObserved, "")
	} else {
		reason := "process I/O collector returned no counters"
		if err != nil {
			reason = err.Error()
		}
		pi.MetricStates[metricProcessIO] = metricStatus(MetricUnavailable, reason)
	}
	pi.IsProtected = IsProtectedProcess(pi.Name, pi.PID)
	return pi, nil
}

// bulkProcessStatuses avoids gopsutil's macOS StatusWithContext implementation,
// which launches one `ps` process per PID. One bulk query keeps collector ticks
// bounded even on machines with hundreds of processes. Other platforms return
// nil and use gopsutil's native per-process implementation.
func bulkProcessStatuses(ctx context.Context) (map[int32]string, error) {
	if runtime.GOOS != "darwin" {
		return nil, nil
	}
	out, err := exec.CommandContext(ctx, "ps", "-axo", "pid=,state=").Output()
	if err != nil {
		return nil, fmt.Errorf("collect process states: %w", err)
	}
	statuses := make(map[int32]string)
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		pid, err := strconv.ParseInt(fields[0], 10, 32)
		if err != nil || pid <= 0 || fields[1] == "" {
			continue
		}
		statuses[int32(pid)] = fields[1][:1]
	}
	return statuses, nil
}

func (c *Collector) collectHost() {
	if h, err := os.Hostname(); err == nil {
		c.info.Hostname = h
	}
	if plat, fam, _, err := host.PlatformInformation(); err == nil {
		c.info.OS = plat
		c.info.Platform = fam
	}
	if k, err := host.KernelVersion(); err == nil {
		c.info.Kernel = k
	}
	if u, err := host.Uptime(); err == nil {
		c.info.Uptime = u
	}
	if b, err := host.BootTime(); err == nil {
		c.info.BootTime = b
	}
}

// FormatBytes formats bytes with adaptive units (KB/MB/GB/TB/PB).
func FormatBytes(b uint64) string {
	const unit = 1024
	if b < unit {
		return formatUint(b) + " B"
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return sprintf1dp(float64(b)/float64(div)) + " " + "KMGTPE"[exp:exp+1] + "B"
}

func formatUint(n uint64) string {
	if n == 0 {
		return "0"
	}
	out := ""
	for n > 0 {
		out = string('0'+byte(n%10)) + out
		n /= 10
	}
	return out
}

func sprintf1dp(f float64) string {
	if f < 0 {
		f = 0
	}
	ip := int(f)
	dp := int((f - float64(ip)) * 10)
	if dp < 0 {
		dp = 0
	}
	if dp > 9 {
		dp = 9
	}
	return formatUint(uint64(ip)) + "." + string('0'+byte(dp))
}
