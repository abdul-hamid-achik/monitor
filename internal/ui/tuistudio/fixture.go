package tuistudio

// fixture.go builds the deterministic, offline data source used by
// `monitor studio --tuimark --fixture`, by Go tests, and by the glyphrun
// specs in specs/tuistudio_*.yml. It never touches the real host: no
// syscalls beyond what math/rand needs, no ~/.config/monitor/config.json
// read or write, no history.veclite store, and no real process is ever
// signaled. It reuses the same collector.SystemInfo / config.Settings /
// kill.Confirmation shapes the real path uses, and the same
// collector.IsProtectedProcess / IsSystemProcess policy, so the safety
// rules a spec exercises here are the real ones, not a re-implementation.

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/history"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
	"github.com/abdul-hamid-achik/monitor/internal/temperature"
)

const fixtureHistorySize = 60

// fixtureProc is one fixture process: its identity plus the base CPU load
// (scaled by a per-sample jitter factor, like examples/monitor's fake
// collector) and whether it is the short-lived row used to exercise the
// process-detail "no longer present" path.
type fixtureProc struct {
	pid        int32
	name, user string
	cpu0       float64
	memMiB     uint64
	threads    int32
	ioRead     uint64
	ioWrite    uint64
	ephemeral  bool // dropped once the fixture tick reaches fixtureEphemeralTick
}

const fixtureEphemeralTick = 3

func fixtureProcesses() []fixtureProc {
	return []fixtureProc{
		{pid: 1, name: "launchd", user: "root", cpu0: 3.0, memMiB: 44, threads: 5},
		{pid: 4, name: "kernel_task", user: "root", cpu0: 18.0, memMiB: 2150, threads: 512},
		{pid: 411, name: "WindowServer", user: "_windowserver", cpu0: 42.0, memMiB: 1430, threads: 24},
		{pid: 2201, name: "monitor-agent", user: "alice", cpu0: 27.5, memMiB: 512, threads: 18},
		{pid: 3305, name: "node", user: "alice", cpu0: 14.2, memMiB: 388, threads: 12},
		{pid: 4410, name: "postgres", user: "_postgres", cpu0: 6.1, memMiB: 96, threads: 7},
		{pid: 5521, name: "redis-server", user: "alice", cpu0: 2.3, memMiB: 9, threads: 5},
		{pid: 6633, name: "Slack Helper (GPU)", user: "alice", cpu0: 9.8, memMiB: 310, threads: 17},
		{pid: 7744, name: "Code Helper (Renderer)", user: "alice", cpu0: 21.0, memMiB: 904, threads: 31},
		{pid: 8855, name: "syslogd", user: "root", cpu0: 0.4, memMiB: 33, threads: 4},
		{pid: 9966, name: "sshd", user: "root", cpu0: 0.2, memMiB: 12, threads: 2},
		{pid: 11077, name: "demo-worker", user: "alice", cpu0: 1.1, memMiB: 21, threads: 3, ephemeral: true},
		{pid: 12188, name: "zsh", user: "alice", cpu0: 0.1, memMiB: 4, threads: 1},
	}
}

// fixtureSource is a metricSource that generates seeded, reproducible
// samples instead of gathering real host telemetry. Its shape mirrors
// collector.Collector's external contract (Run/Snapshot/SetInterval/
// Subscribe) closely enough that host.go never needs to know which one it
// has.
type fixtureSource struct {
	mu   sync.Mutex
	subs map[int]collector.Subscriber
	next int

	rng        *rand.Rand
	tick       int
	interval   time.Duration
	intervalCh chan time.Duration

	procs []fixtureProc
	base  []float64 // per-core base load, 0-100

	cpuHist, memHist, netDownHist, netUpHist, diskRHist, diskWHist []float64

	netSent, netRecv, netPktSent, netPktRecv uint64
	diskRead, diskWrite                      uint64

	published collector.SystemInfo
}

func newFixtureSource() *fixtureSource {
	f := &fixtureSource{
		subs:       make(map[int]collector.Subscriber),
		rng:        rand.New(rand.NewSource(7)),
		interval:   time.Second,
		intervalCh: make(chan time.Duration, 1),
		procs:      fixtureProcesses(),
		base:       []float64{18, 62, 9, 71, 33, 26, 54, 4},
	}
	f.published = f.snapshotLocked()
	return f
}

func (f *fixtureSource) Subscribe(fn collector.Subscriber) func() {
	f.mu.Lock()
	defer f.mu.Unlock()
	id := f.next
	f.next++
	f.subs[id] = fn
	return func() {
		f.mu.Lock()
		defer f.mu.Unlock()
		delete(f.subs, id)
	}
}

func (f *fixtureSource) SetInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	f.mu.Lock()
	f.interval = d
	f.mu.Unlock()
	select {
	case f.intervalCh <- d:
	default:
	}
}

func (f *fixtureSource) Snapshot() collector.SystemInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.published
}

// Capture advances one sample immediately, without waiting for the
// ticker, matching collector.Collector.Capture's contract closely enough
// for the studio host to use either one interchangeably.
func (f *fixtureSource) Capture(ctx context.Context) (collector.SystemInfo, error) {
	return f.sample(), nil
}

// Run advances one deterministic sample per tick (see sample), publishing
// it to every subscriber, exactly like collector.Collector.Run does with
// real telemetry.
func (f *fixtureSource) Run(ctx context.Context) error {
	f.mu.Lock()
	interval := f.interval
	f.mu.Unlock()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case d := <-f.intervalCh:
			t.Reset(d)
		case <-t.C:
			info := f.sample()
			f.mu.Lock()
			subs := make([]collector.Subscriber, 0, len(f.subs))
			for _, fn := range f.subs {
				subs = append(subs, fn)
			}
			f.mu.Unlock()
			for _, fn := range subs {
				fn(collector.Event{
					Timestamp: info.LastUpdate, Hostname: info.Hostname,
					CPU: info.CPU, Memory: info.Memory, Network: info.Network,
					Disk: info.Disk, Processes: info.Processes,
				})
			}
		}
	}
}

func ringAppend(hist []float64, v float64) []float64 {
	hist = append(hist, v)
	if len(hist) > fixtureHistorySize {
		hist = hist[len(hist)-fixtureHistorySize:]
	}
	return hist
}

func clamp(v, lo, hi float64) float64 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// sample advances the fixture by one tick and returns the new published
// snapshot. It is deterministic given the fixed rng seed and the sequence
// of calls, matching examples/monitor's fake collector.
func (f *fixtureSource) sample() collector.SystemInfo {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tick++
	info := f.snapshotLocked()
	f.published = info
	return info
}

func (f *fixtureSource) snapshotLocked() collector.SystemInfo {
	jitter := 0.85 + 0.3*f.rng.Float64()
	cpuUsage := clamp(23*jitter, 0, 100)
	memUsage := clamp(61+6*(f.rng.Float64()-0.5), 0, 100)

	perCore := make([]float64, len(f.base))
	for i, b := range f.base {
		perCore[i] = clamp(b*(0.7+0.6*f.rng.Float64()), 0, 100)
	}
	f.cpuHist = ringAppend(f.cpuHist, cpuUsage)
	f.memHist = ringAppend(f.memHist, memUsage)

	const totalMem = uint64(17179869184) // 16 GiB
	usedMem := uint64(float64(totalMem) * memUsage / 100)
	swapTotal := uint64(2147483648) // 2 GiB

	f.netSent += 120_000 + uint64(f.rng.Intn(40_000))
	f.netRecv += 480_000 + uint64(f.rng.Intn(120_000))
	f.netPktSent += 400
	f.netPktRecv += 900
	// Histories hold bytes per second, as internal/collector's do.
	downRate := clamp(30+40*f.rng.Float64(), 0, 100)
	upRate := clamp(10+15*f.rng.Float64(), 0, 100)
	f.netDownHist = ringAppend(f.netDownHist, downRate*12_000)
	f.netUpHist = ringAppend(f.netUpHist, upRate*12_000)

	f.diskRead += 200_000 + uint64(f.rng.Intn(80_000))
	f.diskWrite += 90_000 + uint64(f.rng.Intn(40_000))
	readRate := clamp(15+20*f.rng.Float64(), 0, 100)
	writeRate := clamp(8+12*f.rng.Float64(), 0, 100)
	f.diskRHist = ringAppend(f.diskRHist, readRate*40_000)
	f.diskWHist = ringAppend(f.diskWHist, writeRate*40_000)

	observed := collector.MetricStatus{State: collector.MetricObserved}
	states := func(keys ...string) map[string]collector.MetricStatus {
		m := make(map[string]collector.MetricStatus, len(keys))
		for _, k := range keys {
			m[k] = observed
		}
		return m
	}

	procs := make([]collector.ProcessInfo, 0, len(f.procs))
	kept := f.procs[:0:0]
	for _, p := range f.procs {
		if p.ephemeral && f.tick >= fixtureEphemeralTick {
			continue
		}
		kept = append(kept, p)
		cpuPct := clamp(p.cpu0*jitter, 0, 100)
		procs = append(procs, collector.ProcessInfo{
			PID: p.pid, Name: p.name, User: p.user, Status: "R",
			CPUPercent: cpuPct, Memory: p.memMiB * 1024 * 1024,
			MemoryPercent: float64(p.memMiB*1024*1024) / float64(totalMem) * 100,
			Threads:       p.threads,
			IsProtected:   collector.IsProtectedProcess(p.name, p.pid),
			IsSystem:      collector.IsSystemProcess(p.user),
			IOReadBytes:   p.ioRead + uint64(f.tick)*2048,
			IOWriteBytes:  p.ioWrite + uint64(f.tick)*1024,
			Parent:        1,
			MetricStates:  states("cpu", "memory", "memory_percent", "threads", "user", "parent", "status", "io"),
		})
	}
	f.procs = kept

	reading := temperature.Estimate(cpuUsage)

	return collector.SystemInfo{
		CPU: collector.CPUInfo{
			UsagePercent: cpuUsage, PerCoreUsage: perCore, FrequencyMHz: 3200,
			CoreCount: len(perCore), ThreadCount: len(perCore),
			LoadAvg1: clamp(1.2*jitter, 0, 64), LoadAvg5: 0.98, LoadAvg15: 0.75,
			History: append([]float64(nil), f.cpuHist...), LastUpdate: time.Now(),
			MetricStates: states("usage", "per_core", "info", "load_average"),
		},
		Memory: collector.MemoryInfo{
			TotalBytes: totalMem, UsedBytes: usedMem, FreeBytes: totalMem - usedMem,
			AvailableBytes: totalMem - usedMem, UsagePercent: memUsage,
			SwapTotal: swapTotal, SwapUsed: uint64(float64(swapTotal) * 0.04),
			SwapFree:  uint64(float64(swapTotal) * 0.96),
			AppMemory: usedMem / 3, WiredMemory: usedMem / 6, CompressedMemory: usedMem / 8,
			CacheMemory: usedMem / 5, PurgeableMemory: usedMem / 20,
			History: append([]float64(nil), f.memHist...), LastUpdate: time.Now(),
			MetricStates: states("virtual", "swap", "breakdown"),
		},
		Temperature: collector.TemperatureInfo{
			CPUPackage: reading.CPUPackage, CPUCores: reading.CPUCores, GPU: reading.GPU,
			ANE: reading.ANE, Battery: reading.Battery, Ambient: reading.Ambient,
			FanRPM: reading.FanRPM, FanMode: reading.FanMode,
			History: append([]float64(nil), f.cpuHist...), LastUpdate: time.Now(),
			State: observed, Available: reading.Available, Source: string(reading.Source),
		},
		Network: collector.NetworkInfo{
			BytesSent: f.netSent, BytesRecv: f.netRecv,
			PacketsSent: f.netPktSent, PacketsRecv: f.netPktRecv,
			BytesSentPerSec: uint64(upRate * 12_000), BytesRecvPerSec: uint64(downRate * 12_000),
			DownloadHistory: append([]float64(nil), f.netDownHist...),
			UploadHistory:   append([]float64(nil), f.netUpHist...),
			LastUpdate:      time.Now(), MetricStates: states("io", "rate"),
		},
		Disk: collector.DiskInfo{
			Partitions: []collector.DiskPartitionInfo{
				{Device: "/dev/disk3s1", MountPoint: "/", TotalBytes: 500_000_000_000, UsedBytes: 340_000_000_000, FreeBytes: 160_000_000_000, UsagePercent: 68.0, Filesystem: "apfs"},
				{Device: "/dev/disk3s5", MountPoint: "/System/Volumes/Data", TotalBytes: 500_000_000_000, UsedBytes: 210_000_000_000, FreeBytes: 290_000_000_000, UsagePercent: 42.0, Filesystem: "apfs"},
			},
			ReadBytes: f.diskRead, WriteBytes: f.diskWrite,
			ReadPerSec: uint64(readRate * 40_000), WritePerSec: uint64(writeRate * 40_000),
			ReadHistory:  append([]float64(nil), f.diskRHist...),
			WriteHistory: append([]float64(nil), f.diskWHist...),
			LastUpdate:   time.Now(), MetricStates: states("partitions", "io", "rate"),
		},
		Processes: procs, ProcessesState: observed, ProcessesLastUpdate: time.Now(),
		Hostname: "studio-fixture", OS: "darwin", Platform: "darwin", Kernel: "24.0.0",
		Uptime: 86400, BootTime: uint64(time.Now().Add(-24 * time.Hour).Unix()),
		LastUpdate: time.Now(), Capture: observed,
	}
}

// fixtureKiller answers kill.CheckSafety-shaped questions from the
// fixture's own process list and "verifies" a kill by removing the process
// from the fixture instead of sending a real signal, so a spec or test can
// exercise the confirm/force/spare flow with zero risk to the host running
// it.
type fixtureKiller struct{ src *fixtureSource }

func (fk fixtureKiller) CheckSafety(pids []int32) kill.Confirmation {
	fk.src.mu.Lock()
	defer fk.src.mu.Unlock()
	var conf kill.Confirmation
	for _, pid := range pids {
		var found *fixtureProc
		for i := range fk.src.procs {
			if fk.src.procs[i].pid == pid {
				found = &fk.src.procs[i]
				break
			}
		}
		if found == nil {
			continue
		}
		pi := collector.ProcessInfo{PID: pid, Name: found.name, User: found.user}
		isProtected := collector.IsProtectedProcess(found.name, found.pid)
		isSystem := collector.IsSystemProcess(found.user)
		if isProtected {
			conf.HasProtected = true
			pi.IsProtected = true
			conf.SafetyWarnings = append(conf.SafetyWarnings, fmt.Sprintf("%s (pid %d) is a protected system process", found.name, pid))
		} else if isSystem {
			conf.HasSystem = true
			pi.IsSystem = true
			conf.SafetyWarnings = append(conf.SafetyWarnings, fmt.Sprintf("%s (pid %d) is owned by %s", found.name, pid, found.user))
		}
		conf.Processes = append(conf.Processes, pi)
	}
	return conf
}

func (fk fixtureKiller) Verify(pid int32, force bool) (kill.Result, error) {
	fk.src.mu.Lock()
	kept := fk.src.procs[:0:0]
	found := false
	for _, p := range fk.src.procs {
		if p.pid == pid {
			found = true
			continue
		}
		kept = append(kept, p)
	}
	fk.src.procs = kept
	fk.src.mu.Unlock()
	res := kill.Result{PID: pid, Signal: "SIGTERM", Outcome: kill.OutcomeTerminated, WaitedMs: 1}
	if force {
		res.Signal = "SIGKILL"
	}
	if !found {
		res.Outcome = kill.OutcomeUnknown
	}
	return res, nil
}

// fixtureSettingsStore never touches ~/.config/monitor/config.json: Load
// always returns the built-in defaults, and Save only keeps the value in
// memory (readable back for tests that want to assert a save happened).
type fixtureSettingsStore struct {
	mu   sync.Mutex
	last *config.Settings
}

func (s *fixtureSettingsStore) Load() (*config.Settings, error) { return config.Default(), nil }

func (s *fixtureSettingsStore) Save(v *config.Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	cp := *v
	s.last = &cp
	return nil
}

func (s *fixtureSettingsStore) Path() (string, error) { return "(fixture: not written to disk)", nil }

// fixtureHistoryReader returns a canned hour of samples so the Trends tab
// has something to plot without a real history.veclite store. noData
// makes it behave like "no store yet" instead, for the empty-state test.
type fixtureHistoryReader struct{ noData bool }

func (h fixtureHistoryReader) recent(metrics []string, since time.Time) (map[string][]history.Point, error, bool) {
	if h.noData {
		return nil, nil, true
	}
	rng := rand.New(rand.NewSource(11))
	out := make(map[string][]history.Point, len(metrics))
	for _, m := range metrics {
		base := 30.0
		if m == "mem.usage" {
			base = 55.0
		}
		pts := make([]history.Point, 0, 60)
		for i := 0; i < 60; i++ {
			v := clamp(base+18*math.Sin(float64(i)/6)+6*(rng.Float64()-0.5), 0, 100)
			pts = append(pts, history.Point{Timestamp: since.Add(time.Duration(i) * time.Minute), Value: v})
		}
		out[m] = pts
	}
	return out, nil, false
}
