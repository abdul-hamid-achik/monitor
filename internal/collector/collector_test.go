package collector

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
