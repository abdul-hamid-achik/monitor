package collector

import (
	"context"
	"testing"
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

func TestSetProcessInterval(t *testing.T) {
	c := New(Options{})
	c.SetProcessInterval(0) // no-op for invalid
	c.SetProcessInterval(250_000_000)
	if c.opts.Interval != 250_000_000 {
		t.Errorf("interval = %v, want 250ms", c.opts.Interval)
	}
}