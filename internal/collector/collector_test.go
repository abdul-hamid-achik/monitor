package collector

import (
	"context"
	"testing"
	"time"
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
