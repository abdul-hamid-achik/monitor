package cli

import (
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func TestSignedBytes(t *testing.T) {
	cases := map[int64]string{
		0:        "+0 B",
		1024:     "+1.0 KB",
		-1024:    "-1.0 KB",
		1048576:  "+1.0 MB",
		-2097152: "-2.0 MB",
	}
	for delta, want := range cases {
		if got := signedBytes(delta); got != want {
			t.Errorf("signedBytes(%d) = %q, want %q", delta, got, want)
		}
	}
}

func TestCorrelationScore(t *testing.T) {
	if got := correlationScore(map[string]any{"score": 12.5}); got != 12.5 {
		t.Errorf("score present = %v, want 12.5", got)
	}
	if got := correlationScore(map[string]any{}); got != 0 {
		t.Errorf("missing score = %v, want 0", got)
	}
	// A non-float score (e.g. an int that slipped in) must not panic and reads 0.
	if got := correlationScore(map[string]any{"score": 7}); got != 0 {
		t.Errorf("non-float score = %v, want 0 (type-assert miss)", got)
	}
}

func TestEventToSystemInfo(t *testing.T) {
	ts := time.Now().Truncate(time.Second)
	ev := collector.Event{
		Timestamp: ts,
		Hostname:  "host-x",
		CPU:       collector.CPUInfo{UsagePercent: 42},
		Memory:    collector.MemoryInfo{UsagePercent: 55},
		Network:   collector.NetworkInfo{BytesRecvPerSec: 99},
	}
	info := eventToSystemInfo(ev)
	if info.Hostname != "host-x" || info.CPU.UsagePercent != 42 ||
		info.Memory.UsagePercent != 55 || info.Network.BytesRecvPerSec != 99 {
		t.Errorf("flattened fields mismatch: %+v", info)
	}
	if !info.LastUpdate.Equal(ts) {
		t.Errorf("LastUpdate = %v, want the event Timestamp %v", info.LastUpdate, ts)
	}
}
