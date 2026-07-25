package collector

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/net"

	"github.com/abdul-hamid-achik/monitor/internal/capability"
)

func TestNewDefaults(t *testing.T) {
	c := New(Options{})
	if c == nil {
		t.Fatal("New returned nil")
	}
	if c.opts.Interval != 1_000_000_000 { // 1s in ns
		t.Errorf("default interval = %v, want 1s", c.opts.Interval)
	}
	if c.opts.HistorySize != 60 {
		t.Errorf("default history size = %d, want 60", c.opts.HistorySize)
	}
}

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		in   uint64
		want string
	}{
		{0, "0 B"},
		{1, "1 B"},
		{1023, "1023 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
	}
	for _, tt := range tests {
		got := FormatBytes(tt.in)
		if got != tt.want {
			t.Errorf("FormatBytes(%d) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestCollectRuns(t *testing.T) {
	c := New(Options{Interval: 100_000_000})
	ctx := context.Background()
	info := c.Collect(ctx)
	if info.LastUpdate.IsZero() {
		t.Error("LastUpdate should be set after Collect")
	}
	if info.CPU.CoreCount == 0 {
		t.Log("no CPU cores reported (acceptable on exotic systems)")
	}
}

func TestCollectProcessTelemetryDeclaresStatusAndIO(t *testing.T) {
	c := New(Options{})
	info := c.Collect(context.Background())
	var self *ProcessInfo
	for i := range info.Processes {
		if info.Processes[i].PID == int32(os.Getpid()) {
			self = &info.Processes[i]
			break
		}
	}
	if self == nil {
		t.Fatal("collector did not return the test process")
	}
	for _, key := range []string{metricProcessStatus, metricProcessIO} {
		state, ok := self.MetricStates[key]
		if !ok {
			t.Errorf("process metric %q has no explicit availability state", key)
			continue
		}
		if state.State != MetricObserved && state.State != MetricUnavailable && state.State != MetricUnsupported {
			t.Errorf("process metric %q state = %q", key, state.State)
		}
	}
	if self.MetricStates[metricProcessStatus].State == MetricObserved && self.Status == "" {
		t.Error("observed process status is empty")
	}
}
func TestLoadAverageObservedZeroVersusUnavailable(t *testing.T) {
	linux := capability.Detect(capability.Detector{
		GOOS:     "linux",
		LookPath: func(string) (string, error) { return "", errors.New("missing") },
	})
	tests := []struct {
		name        string
		collect     func(context.Context) (*load.AvgStat, error)
		wantState   MetricState
		wantNumeric bool
	}{
		{
			name: "observed zero",
			collect: func(context.Context) (*load.AvgStat, error) {
				return &load.AvgStat{}, nil
			},
			wantState: MetricObserved, wantNumeric: true,
		},
		{
			name: "collector unavailable",
			collect: func(context.Context) (*load.AvgStat, error) {
				return nil, errors.New("no load source")
			},
			wantState: MetricUnavailable, wantNumeric: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := New(Options{Capabilities: &linux, LoadAverage: tt.collect})
			info, err := c.Capture(context.Background())
			if err != nil {
				t.Fatalf("Capture: %v", err)
			}
			if got := info.CPU.MetricStates[metricCPULoad].State; got != tt.wantState {
				t.Fatalf("load state = %q, want %q", got, tt.wantState)
			}
			data, err := json.Marshal(info.CPU)
			if err != nil {
				t.Fatalf("marshal CPU: %v", err)
			}
			var payload map[string]any
			if err := json.Unmarshal(data, &payload); err != nil {
				t.Fatalf("unmarshal CPU: %v", err)
			}
			_, numeric := payload["load_avg_1"].(float64)
			if numeric != tt.wantNumeric {
				t.Fatalf("load_avg_1 numeric=%v, want %v; payload=%s", numeric, tt.wantNumeric, data)
			}
		})
	}
}

func TestCaptureBlocksUnsupportedPlatformBeforeCollectors(t *testing.T) {
	unsupported := capability.Detect(capability.Detector{
		GOOS:     "plan9",
		LookPath: func(string) (string, error) { return "", errors.New("missing") },
	})
	called := false
	c := New(Options{
		Capabilities: &unsupported,
		LoadAverage: func(context.Context) (*load.AvgStat, error) {
			called = true
			return &load.AvgStat{}, nil
		},
	})
	info, err := c.Capture(context.Background())
	if err == nil {
		t.Fatal("Capture should reject unsupported platform")
	}
	if called {
		t.Fatal("collector seam was invoked before capability rejection")
	}
	if info.Capture.State != MetricUnsupported {
		t.Fatalf("capture state = %q, want unsupported", info.Capture.State)
	}
}

func TestCaptureCanSkipUnsupportedProcessCollection(t *testing.T) {
	caps := capability.Detect(capability.Detector{
		GOOS:     "linux",
		LookPath: func(string) (string, error) { return "", errors.New("missing") },
	})
	caps.Items[capability.ProcessMetrics] = capability.Support{
		State:  capability.Unsupported,
		Reason: "fixture disables process inspection",
	}

	disabled := New(Options{Capabilities: &caps, DisableProcesses: true})
	info, err := disabled.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture with process collection disabled: %v", err)
	}
	if info.Processes != nil {
		t.Fatalf("Processes = %+v, want nil", info.Processes)
	}
	if info.ProcessesState.State != MetricUnavailable {
		t.Fatalf("ProcessesState = %+v, want unavailable", info.ProcessesState)
	}
	if !strings.Contains(info.ProcessesState.Reason, "disabled") {
		t.Fatalf("ProcessesState reason = %q, want disabled explanation", info.ProcessesState.Reason)
	}

	enabled := New(Options{Capabilities: &caps})
	if _, err := enabled.Capture(context.Background()); err == nil {
		t.Fatal("default collector should still require process metrics")
	}
}

func TestCaptureCanPreserveHostMemoryWhenCgroupOverrideIsDisabled(t *testing.T) {
	c := New(Options{DisableProcesses: true, DisableCgroupOverride: true})
	info, err := c.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if info.Cgroup.Limited {
		t.Fatalf("Cgroup = %+v, want no self-cgroup interpretation", info.Cgroup)
	}
	if info.Cgroup.State.State != MetricUnavailable ||
		!strings.Contains(info.Cgroup.State.Reason, "disabled") {
		t.Fatalf("Cgroup state = %+v", info.Cgroup.State)
	}
}

func TestTelemetryProfileRunsOnlyClosedScalarCollectionPlan(t *testing.T) {
	caps := capability.Detect(capability.Detector{GOOS: "linux"})
	caps.Items[capability.ProcessMetrics] = capability.Support{
		State:  capability.Unsupported,
		Reason: "process inspection must not be required",
	}
	c := New(Options{Profile: ProfileTelemetry, Capabilities: &caps})

	base := time.Unix(1_700_000_000, 0)
	networkCall := 0
	diskCall := 0
	c.telemetrySources = telemetrySourceSet{
		now:      func() time.Time { return base },
		cpuUsage: func(context.Context) (float64, error) { return 42, nil },
		loadOne:  func(context.Context) (float64, error) { return 1.5, nil },
		virtualMemory: func(context.Context) (telemetryVirtualMemory, error) {
			return telemetryVirtualMemory{
				usedBytes: 2048, availableBytes: 4096,
				usagePercent: 33, pressure: 25,
			}, nil
		},
		swap: func(context.Context) (telemetrySwap, error) {
			return telemetrySwap{usedBytes: 600, totalBytes: 1000}, nil
		},
		network: func(context.Context) (telemetryCounterSample, error) {
			values := []telemetryCounterSample{
				{counters: telemetryByteCounters{readBytes: 1000, writeBytes: 2000}, at: base},
				{counters: telemetryByteCounters{readBytes: 5000, writeBytes: 8000}, at: base.Add(2 * time.Second)},
			}
			value := values[networkCall]
			networkCall++
			return value, nil
		},
		disk: func(context.Context) (telemetryCounterSample, error) {
			values := []telemetryCounterSample{
				{counters: telemetryByteCounters{readBytes: 100, writeBytes: 200}, at: base.Add(500 * time.Millisecond)},
				{counters: telemetryByteCounters{readBytes: 2100, writeBytes: 4200}, at: base.Add(2500 * time.Millisecond)},
			}
			value := values[diskCall]
			diskCall++
			return value, nil
		},
	}

	allowed := map[collectionStage]bool{
		collectionCPUUsage: true, collectionCPULoad: true,
		collectionMemoryVirtual: true, collectionMemorySwap: true,
		collectionNetworkAggregate: true, collectionDiskAggregate: true,
	}
	var stages []collectionStage
	c.collectionObserver = func(stage collectionStage) {
		if !allowed[stage] {
			t.Fatalf("telemetry profile invoked forbidden collector %q", stage)
		}
		stages = append(stages, stage)
	}
	c.WithTemperatureHook(func() (float64, float64, float64, float64, float64, float64, int, string, string, bool) {
		panic("telemetry profile invoked the temperature hook")
	})

	first, err := c.Capture(context.Background())
	if err != nil {
		t.Fatalf("first Capture: %v", err)
	}
	if first.Network.MetricStates[metricNetworkRate].State != MetricUnavailable ||
		first.Disk.MetricStates[metricDiskRate].State != MetricUnavailable {
		t.Fatalf("first sample should prewarm rates: network=%+v disk=%+v",
			first.Network.MetricStates, first.Disk.MetricStates)
	}
	info, err := c.Capture(context.Background())
	if err != nil {
		t.Fatalf("second Capture: %v", err)
	}

	wantOneCapture := []collectionStage{
		collectionCPUUsage, collectionCPULoad,
		collectionMemoryVirtual, collectionMemorySwap,
		collectionNetworkAggregate, collectionDiskAggregate,
	}
	wantStages := append(append([]collectionStage(nil), wantOneCapture...), wantOneCapture...)
	if !reflect.DeepEqual(stages, wantStages) {
		t.Fatalf("collection stages = %v, want %v", stages, wantStages)
	}
	if info.CPU.UsagePercent != 42 || info.CPU.LoadAvg1 != 1.5 {
		t.Fatalf("CPU telemetry = %+v", info.CPU)
	}
	if info.CPU.LoadAvg5 != 0 || info.CPU.LoadAvg15 != 0 ||
		info.CPU.PerCoreUsage != nil || info.CPU.CoreCount != 0 ||
		info.CPU.ThreadCount != 0 || info.CPU.FrequencyMHz != 0 {
		t.Fatalf("telemetry collected CPU detail/topology: %+v", info.CPU)
	}
	if info.Memory.UsedBytes != 2048 || info.Memory.AvailableBytes != 4096 ||
		info.Memory.UsagePercent != 33 || info.Memory.MemoryPressure != 25 ||
		info.Memory.SwapUsed != 600 || info.Memory.SwapTotal != 1000 {
		t.Fatalf("memory telemetry = %+v", info.Memory)
	}
	if info.Network.BytesRecvPerSec != 2000 || info.Network.BytesSentPerSec != 3000 {
		t.Fatalf("network rates = %+v", info.Network)
	}
	if info.Network.BytesRecv != 0 || info.Network.BytesSent != 0 ||
		info.Network.PacketsRecv != 0 || info.Network.PacketsSent != 0 {
		t.Fatalf("telemetry retained network counters: %+v", info.Network)
	}
	if info.Disk.ReadPerSec != 1000 || info.Disk.WritePerSec != 2000 {
		t.Fatalf("disk telemetry = %+v", info.Disk)
	}
	if info.Disk.Partitions != nil || info.Disk.ReadBytes != 0 || info.Disk.WriteBytes != 0 {
		t.Fatalf("telemetry retained disk identities/counters: %+v", info.Disk)
	}
	if info.Hostname != "" || info.OS != "" || info.Platform != "" ||
		info.Kernel != "" || info.Uptime != 0 || info.BootTime != 0 {
		t.Fatalf("telemetry collected host identity: %+v", info)
	}
	if info.Processes != nil || !info.ProcessesLastUpdate.IsZero() {
		t.Fatalf("telemetry collected process data: %+v", info.Processes)
	}
	if !reflect.DeepEqual(info.Temperature, TemperatureInfo{}) || info.Cgroup != (CgroupInfo{}) {
		t.Fatalf("telemetry collected temperature/cgroup: temperature=%+v cgroup=%+v",
			info.Temperature, info.Cgroup)
	}
	if c.cpuHist != nil || c.memHist != nil || c.netDownHist != nil ||
		c.netUpHist != nil || c.diskRHist != nil || c.diskWHist != nil {
		t.Fatal("telemetry profile allocated history buffers")
	}
}

func TestTelemetryProfileDoesNotRetainRawSourceErrors(t *testing.T) {
	caps := capability.Detect(capability.Detector{GOOS: "linux"})
	c := New(Options{Profile: ProfileTelemetry, Capabilities: &caps})
	c.telemetrySources.network = func(context.Context) (telemetryCounterSample, error) {
		return telemetryCounterSample{}, errors.New("SECRET_INTERFACE customer0")
	}
	c.telemetrySources.disk = func(context.Context) (telemetryCounterSample, error) {
		return telemetryCounterSample{}, errors.New("SECRET_DEVICE /dev/private0")
	}

	info, err := c.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, forbidden := range []string{
		"SECRET_INTERFACE", "customer0", "SECRET_DEVICE", "/dev/private0",
	} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("restricted snapshot retained %q: %s", forbidden, data)
		}
	}
}

func TestTelemetryProfileSanitizesCapabilityReasons(t *testing.T) {
	caps := capability.Detect(capability.Detector{GOOS: "linux"})
	caps.Items[capability.SystemMetrics] = capability.Support{
		State: capability.Unavailable, Reason: "SECRET_PATH /private/customer",
	}
	c := New(Options{Profile: ProfileTelemetry, Capabilities: &caps})
	info, err := c.Capture(context.Background())
	if err == nil {
		t.Fatal("Capture succeeded with unavailable system metrics")
	}
	data, marshalErr := json.Marshal(info)
	if marshalErr != nil {
		t.Fatalf("Marshal: %v", marshalErr)
	}
	for _, forbidden := range []string{"SECRET_PATH", "/private/customer"} {
		if strings.Contains(string(data), forbidden) {
			t.Fatalf("restricted snapshot retained %q: %s", forbidden, data)
		}
	}
}

func TestParseAnonymousDiskCounters(t *testing.T) {
	counters, err := parseAnonymousDiskCounters(strings.NewReader(
		"nr_free_pages 10\npgpgin 12\npgpgout 34\npswpin 99\n",
	))
	if err != nil {
		t.Fatalf("parseAnonymousDiskCounters: %v", err)
	}
	if counters.readBytes != 12*1024 || counters.writeBytes != 34*1024 {
		t.Fatalf("counters = %+v", counters)
	}

	for _, input := range []string{
		"pgpgin 12\n",
		"pgpgin invalid\npgpgout 34\n",
		"pgpgin 18014398509481984\npgpgout 34\n",
	} {
		if _, err := parseAnonymousDiskCounters(strings.NewReader(input)); err == nil {
			t.Fatalf("parseAnonymousDiskCounters(%q) succeeded, want error", input)
		}
	}
}

func TestParseAnonymousNetworkCounters(t *testing.T) {
	ipv4, err := parseAnonymousIPv4Counters(strings.NewReader(
		"TcpExt: Other\nTcpExt: 1\n" +
			"IpExt: InNoRoutes InOctets OutOctets InMcastOctets\n" +
			"IpExt: 0 1200 3400 5\n",
	))
	if err != nil {
		t.Fatalf("parseAnonymousIPv4Counters: %v", err)
	}
	ipv6, err := parseAnonymousIPv6Counters(strings.NewReader(
		"Ip6InReceives 10\nIp6InOctets 56\nIp6OutOctets 78\n",
	))
	if err != nil {
		t.Fatalf("parseAnonymousIPv6Counters: %v", err)
	}
	total, err := addAnonymousByteCounters(ipv4, ipv6)
	if err != nil {
		t.Fatalf("addAnonymousByteCounters: %v", err)
	}
	if total.readBytes != 1256 || total.writeBytes != 3478 {
		t.Fatalf("network counters = %+v", total)
	}

	for name, test := range map[string]func() error{
		"IPv4 missing": func() error {
			_, err := parseAnonymousIPv4Counters(strings.NewReader("IpExt: InOctets\nIpExt: 1\n"))
			return err
		},
		"IPv4 malformed": func() error {
			_, err := parseAnonymousIPv4Counters(strings.NewReader(
				"IpExt: InOctets OutOctets\nIpExt: invalid 2\n",
			))
			return err
		},
		"IPv6 missing": func() error {
			_, err := parseAnonymousIPv6Counters(strings.NewReader("Ip6InOctets 1\n"))
			return err
		},
		"IPv6 malformed": func() error {
			_, err := parseAnonymousIPv6Counters(strings.NewReader(
				"Ip6InOctets invalid\nIp6OutOctets 2\n",
			))
			return err
		},
		"sum overflow": func() error {
			_, err := addAnonymousByteCounters(
				telemetryByteCounters{readBytes: math.MaxUint64},
				telemetryByteCounters{readBytes: 1},
			)
			return err
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := test(); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestUnsupportedLoadAverageIsNotCollectedOrSerialized(t *testing.T) {
	darwin := capability.Detect(capability.Detector{
		GOOS:     "darwin",
		LookPath: func(string) (string, error) { return "/usr/bin/sample", nil },
	})
	called := false
	c := New(Options{
		Capabilities: &darwin,
		LoadAverage: func(context.Context) (*load.AvgStat, error) {
			called = true
			return &load.AvgStat{}, nil
		},
	})
	info, err := c.Capture(context.Background())
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if called {
		t.Fatal("unsupported load collector must not run")
	}
	if info.CPU.MetricStates[metricCPULoad].State != MetricUnsupported {
		t.Fatalf("load state = %+v", info.CPU.MetricStates[metricCPULoad])
	}
	data, err := json.Marshal(info.CPU)
	if err != nil {
		t.Fatalf("marshal CPU: %v", err)
	}
	var payload map[string]any
	if err := json.Unmarshal(data, &payload); err != nil {
		t.Fatalf("unmarshal CPU: %v", err)
	}
	if _, exists := payload["load_avg_1"]; exists {
		t.Fatalf("unsupported load average serialized as a number: %s", data)
	}
}

func TestSubscribeCalledOnEachTick(t *testing.T) {
	c := New(Options{Interval: 1_000_000})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var got int
	c.Subscribe(func(Event) { got++ })
	for i := 0; i < 3; i++ {
		c.Collect(ctx)
	}
	if got != 3 {
		t.Errorf("subscriber called %d times, want 3", got)
	}
}

// TestPerSecond covers the rate math extracted from collectNetwork/Disk —
// the path where the "rates always 0" bug lived, plus the elapsed==0 and
// counter-wrap edge cases.
func TestPerSecond(t *testing.T) {
	cases := []struct {
		prev, cur uint64
		elapsed   float64
		want      uint64
	}{
		{0, 1000, 1.0, 1000},    // 1000 bytes in 1s
		{1000, 3000, 2.0, 1000}, // 2000 bytes in 2s
		{1000, 1000, 1.0, 0},    // no change
		{0, 1000, 0, 0},         // elapsed 0 → no divide-by-zero
		{0, 1000, -1, 0},        // negative elapsed → 0
		{5000, 1000, 1.0, 0},    // counter reset/wrap → 0, no underflow
	}
	for _, c := range cases {
		if got := perSecond(c.prev, c.cur, c.elapsed); got != c.want {
			t.Errorf("perSecond(%d, %d, %v) = %d, want %d", c.prev, c.cur, c.elapsed, got, c.want)
		}
	}
}

func TestCollectNetworkContainsCollectorPanic(t *testing.T) {
	c := New(Options{
		NetworkCounters: func(context.Context) ([]net.IOCountersStat, error) {
			panic("malformed netstat output")
		},
	})
	c.info.Network.MetricStates = make(map[string]MetricStatus)

	c.collectNetwork(context.Background())

	for _, metric := range []string{metricNetworkIO, metricNetworkRate} {
		status := c.info.Network.MetricStates[metric]
		if status.State != MetricUnavailable {
			t.Fatalf("%s state = %q, want unavailable", metric, status.State)
		}
		if status.Reason != "network collector panic: malformed netstat output" {
			t.Fatalf("%s reason = %q", metric, status.Reason)
		}
	}
	if !c.info.Network.LastUpdate.IsZero() {
		t.Fatal("failed network sample must not advance LastUpdate")
	}
}

// TestSnapshotConcurrentWithCollect spins Snapshot() while Collect() samples,
// exercising the published/in-progress double buffer. Must be race-free under
// -race (the whole point of taking sampling out from under the lock).
func TestSnapshotConcurrentWithCollect(t *testing.T) {
	c := New(Options{Interval: time.Millisecond})
	ctx := context.Background()
	done := make(chan struct{})
	go func() {
		for i := 0; i < 5; i++ {
			c.Collect(ctx)
		}
		close(done)
	}()
	for {
		select {
		case <-done:
			return
		default:
			_ = c.Snapshot()
		}
	}
}

func TestSetInterval(t *testing.T) {
	c := New(Options{})
	c.SetInterval(0) // no-op for invalid
	c.SetInterval(250_000_000)
	if c.opts.Interval != 250_000_000 {
		t.Errorf("interval = %v, want 250ms", c.opts.Interval)
	}
}

func TestSetIntervalResetsRunningCollector(t *testing.T) {
	c := New(Options{Interval: time.Hour})
	seen := make(chan struct{}, 1)
	c.Subscribe(func(Event) {
		select {
		case seen <- struct{}{}:
		default:
		}
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	c.SetInterval(5 * time.Millisecond)
	select {
	case <-seen:
	case <-time.After(5 * time.Second):
		t.Fatal("running collector did not apply the new interval")
	}
}

// TestSubscribeCancelStopsCallbacks is a regression for the bug where the
// cancel func returned by Subscribe was a no-op, so unsubscribed callbacks
// kept firing on every tick.
func TestSubscribeCancelStopsCallbacks(t *testing.T) {
	c := New(Options{Interval: 1_000_000})
	ctx := context.Background()
	var got int
	cancel := c.Subscribe(func(Event) { got++ })
	c.Collect(ctx)
	if got != 1 {
		t.Fatalf("after subscribe + 1 tick: got %d, want 1", got)
	}
	cancel()
	c.Collect(ctx)
	c.Collect(ctx)
	if got != 1 {
		t.Errorf("after cancel + 2 ticks: got %d, want 1 (callback must not fire)", got)
	}
	cancel() // double cancel must be harmless
}
