// Package collector gathers system metrics on a tick and publishes them to
// subscribers. It is a pure-Go refactor of the old internal/system collector.
package collector

import (
	"context"
	"os"
	"sort"
	"sync"
	"time"

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
	mu        sync.RWMutex
	info      SystemInfo
	published SystemInfo
	lastNet   net.IOCountersStat
	lastDisk  disk.IOCountersStat

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
	return &Collector{
		opts:        opts,
		subs:        make(map[int]Subscriber),
		cpuHist:     NewRingBuffer[float64](opts.HistorySize),
		memHist:     NewRingBuffer[float64](opts.HistorySize),
		netDownHist: NewRingBuffer[float64](opts.HistorySize),
		netUpHist:   NewRingBuffer[float64](opts.HistorySize),
		diskRHist:   NewRingBuffer[float64](opts.HistorySize),
		diskWHist:   NewRingBuffer[float64](opts.HistorySize),
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

// SetInterval changes the tick interval used by Run. It only affects a
// ticker started AFTER this call — Run reads the interval once at startup
// and does not reset an already-running ticker. Values <= 0 are ignored.
func (c *Collector) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	c.mu.Lock()
	c.opts.Interval = d
	c.mu.Unlock()
}

// Snapshot returns the latest published SystemInfo. It only contends with
// the brief publish step in Collect, never with the slow lock-free sampling,
// so the TUI render loop is never blocked by process enumeration.
func (c *Collector) Snapshot() SystemInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.published
}

// Collect samples the system once and returns the SystemInfo. Subscribers
// receive an Event built from the same sample.
//
// Sampling runs WITHOUT c.mu: the collect* helpers and the lastNet/lastDisk/
// ring-buffer state they touch are owned by the single Collect goroutine
// (Run's ticker, or a one-off CLI call), so Collect must NOT be invoked
// concurrently on the same Collector. c.mu is taken only to publish the
// assembled snapshot atomically, so a slow process enumeration can't block
// Snapshot() readers.
func (c *Collector) Collect(ctx context.Context) SystemInfo {
	c.info.LastUpdate = time.Now()
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
		case <-t.C:
			c.Collect(ctx)
		}
	}
}

func (c *Collector) collectCPU(ctx context.Context) {
	percent, err := cpu.PercentWithContext(ctx, 0, false)
	if err == nil && len(percent) > 0 {
		c.info.CPU.UsagePercent = percent[0]
	}
	perCore, err := cpu.PercentWithContext(ctx, 0, true)
	if err == nil {
		c.info.CPU.PerCoreUsage = perCore
		c.info.CPU.CoreCount = len(perCore)
	}
	info, err := cpu.InfoWithContext(ctx)
	if err == nil && len(info) > 0 {
		c.info.CPU.FrequencyMHz = info[0].Mhz
		c.info.CPU.ThreadCount = int(info[0].Cores) * len(info)
	}
	c.info.CPU.LoadAvg1 = 0
	c.info.CPU.LoadAvg5 = 0
	c.info.CPU.LoadAvg15 = 0
	// On Linux, gopsutil's load.Avg() reads /proc/loadavg and returns
	// real values. On macOS it returns 0s (known gopsutil limitation).
	// We try it unconditionally; Linux users get load averages, macOS
	// users get 0 (same as before).
	if avg, err := load.AvgWithContext(ctx); err == nil {
		c.info.CPU.LoadAvg1 = avg.Load1
		c.info.CPU.LoadAvg5 = avg.Load5
		c.info.CPU.LoadAvg15 = avg.Load15
	}
	c.cpuHist.Push(c.info.CPU.UsagePercent)
	c.info.CPU.History = c.cpuHist.ToSlice()
	c.info.CPU.LastUpdate = time.Now()
}

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
	c.info.Memory.AppMemory = vm.Used
	// Guard the unsigned subtraction: on some platforms Used+Free can
	// transiently exceed Total, which would wrap to a near-2^64 value.
	if vm.Total > vm.Used+vm.Free {
		c.info.Memory.CacheMemory = vm.Total - vm.Used - vm.Free
	} else {
		c.info.Memory.CacheMemory = 0
	}
	if vm.Total > 0 {
		c.info.Memory.MemoryPressure = float64(vm.Used) / float64(vm.Total) * 100
	}
	if swap, err := mem.SwapMemoryWithContext(ctx); err == nil {
		c.info.Memory.SwapTotal = swap.Total
		c.info.Memory.SwapUsed = swap.Used
		c.info.Memory.SwapFree = swap.Free
	}
	// History is pushed after collectCgroup (in Collect) so the sparkline plots
	// the final UsagePercent — container-relative inside a memory-limited
	// cgroup, host-relative otherwise — matching the headline value.
	c.info.Memory.LastUpdate = time.Now()
}

// collectCgroup reads cgroup v2 limits and, when a memory limit is set (a
// container), re-reports memory against the limit instead of host RAM so usage
// reflects the container rather than the whole machine. No-op on the host /
// macOS (Active=false).
func (c *Collector) collectCgroup() {
	l := cgroup.Read()
	c.info.Cgroup = CgroupInfo{
		Limited:       l.Active,
		MemLimitBytes: l.MemLimit,
		MemUsageBytes: l.MemCurrent,
		CPUQuotaCores: l.CPUQuota,
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
		// Rescale the App/Cache breakdown to the container too; otherwise these
		// keep host-scale values (vm.Used can be many GB) that exceed the
		// overridden TotalBytes (the limit) and render a nonsensical split.
		c.info.Memory.AppMemory = l.MemCurrent
		c.info.Memory.CacheMemory = 0
	}
}

func (c *Collector) collectTemperature() {
	// Read the hook under the lock: sampling is otherwise lock-free, but
	// WithTemperatureHook writes this field under c.mu, so snapshot it here
	// to keep the lock discipline consistent even if a hook is ever
	// installed after sampling has started.
	c.mu.RLock()
	hook := c.temperatureHook
	c.mu.RUnlock()
	if hook != nil {
		cpuPkg, cpuCores, gpu, ane, battery, ambient, fanRPM, fanMode, source, available := hook()
		c.info.Temperature.CPUPackage = cpuPkg
		c.info.Temperature.CPUCores = cpuCores
		c.info.Temperature.GPU = gpu
		c.info.Temperature.ANE = ane
		c.info.Temperature.Battery = battery
		c.info.Temperature.Ambient = ambient
		c.info.Temperature.FanRPM = fanRPM
		c.info.Temperature.FanMode = fanMode
		c.info.Temperature.Source = source
		c.info.Temperature.Available = available
		c.info.Temperature.LastUpdate = time.Now()
		return
	}
	baseTemp := 35.0
	loadTemp := c.info.CPU.UsagePercent * 0.5
	c.info.Temperature.CPUPackage = baseTemp + loadTemp
	c.info.Temperature.CPUCores = baseTemp + loadTemp + 2
	c.info.Temperature.GPU = baseTemp + c.info.CPU.UsagePercent*0.3
	c.info.Temperature.ANE = baseTemp + c.info.CPU.UsagePercent*0.2
	c.info.Temperature.Battery = 38.0
	c.info.Temperature.Source = "estimated"
	c.info.Temperature.Available = true
	c.info.Temperature.LastUpdate = time.Now()
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
		return
	}
	cur := counters[0]
	now := time.Now()
	// Compute per-second rates against the PREVIOUS sample before
	// overwriting lastNet/LastUpdate. The first sample (LastUpdate zero)
	// has no previous counters, so it seeds a 0 into the history.
	if !c.info.Network.LastUpdate.IsZero() {
		if elapsed := now.Sub(c.info.Network.LastUpdate).Seconds(); elapsed > 0 {
			c.info.Network.BytesSentPerSec = perSecond(c.lastNet.BytesSent, cur.BytesSent, elapsed)
			c.info.Network.BytesRecvPerSec = perSecond(c.lastNet.BytesRecv, cur.BytesRecv, elapsed)
			c.netDownHist.Push(float64(c.info.Network.BytesRecvPerSec))
			c.netUpHist.Push(float64(c.info.Network.BytesSentPerSec))
		}
	} else {
		c.netDownHist.Push(0)
		c.netUpHist.Push(0)
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
		return
	}
	var out []DiskPartitionInfo
	for _, p := range parts {
		u, err := disk.UsageWithContext(ctx, p.Mountpoint)
		if err != nil {
			continue
		}
		out = append(out, DiskPartitionInfo{
			Device:       p.Device,
			MountPoint:   p.Mountpoint,
			TotalBytes:   u.Total,
			UsedBytes:    u.Used,
			FreeBytes:    u.Free,
			UsagePercent: u.UsedPercent,
			Filesystem:   p.Fstype,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].MountPoint < out[j].MountPoint })
	c.info.Disk.Partitions = out

	io, err := disk.IOCountersWithContext(ctx)
	if err == nil {
		var read, write uint64
		for _, v := range io {
			read += v.ReadBytes
			write += v.WriteBytes
		}
		if !c.info.Disk.LastUpdate.IsZero() {
			elapsed := time.Since(c.info.Disk.LastUpdate).Seconds()
			if elapsed > 0 {
				c.info.Disk.ReadPerSec = perSecond(c.lastDisk.ReadBytes, read, elapsed)
				c.info.Disk.WritePerSec = perSecond(c.lastDisk.WriteBytes, write, elapsed)
				c.diskRHist.Push(float64(c.info.Disk.ReadPerSec))
				c.diskWHist.Push(float64(c.info.Disk.WritePerSec))
				c.info.Disk.ReadHistory = c.diskRHist.ToSlice()
				c.info.Disk.WriteHistory = c.diskWHist.ToSlice()
			}
		}
		c.lastDisk.ReadBytes = read
		c.lastDisk.WriteBytes = write
	}
	c.info.Disk.LastUpdate = time.Now()
}

func (c *Collector) collectProcesses(ctx context.Context) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return
	}
	var out []ProcessInfo
	for _, p := range procs {
		pi, err := c.processInfo(ctx, p)
		if err != nil {
			continue
		}
		out = append(out, pi)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CPUPercent > out[j].CPUPercent })
	c.info.Processes = out
	c.info.ProcessesLastUpdate = c.info.LastUpdate
}

func (c *Collector) processInfo(ctx context.Context, p *process.Process) (ProcessInfo, error) {
	var pi ProcessInfo
	pi.PID = p.Pid
	if name, err := p.NameWithContext(ctx); err == nil {
		pi.Name = name
	} else {
		pi.Name = "unknown"
	}
	if cpu, err := p.CPUPercentWithContext(ctx); err == nil {
		pi.CPUPercent = cpu
	}
	if mem, err := p.MemoryInfoWithContext(ctx); err == nil {
		pi.Memory = mem.RSS
	}
	if t, err := p.NumThreadsWithContext(ctx); err == nil {
		pi.Threads = t
	}
	if u, err := p.UsernameWithContext(ctx); err == nil {
		pi.User = u
		pi.IsSystem = IsSystemProcess(u)
	}
	if ppid, err := p.PpidWithContext(ctx); err == nil {
		pi.Parent = ppid
	}
	pi.IsProtected = IsProtectedProcess(pi.Name, pi.PID)
	return pi, nil
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
