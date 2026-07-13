package cli

import (
	"context"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func TestCollectSnapshotHonorsWarmupInterval(t *testing.T) {
	c := collector.New(collector.Options{})
	start := time.Now()
	info, err := collectSnapshot(context.Background(), c, 20*time.Millisecond)
	if err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("snapshot returned before warm-up interval: %v", elapsed)
	}
	if info.LastUpdate.IsZero() {
		t.Fatal("second snapshot has no timestamp")
	}
}

func TestCollectSnapshotInstantMode(t *testing.T) {
	c := collector.New(collector.Options{})
	info, err := collectSnapshot(context.Background(), c, 0)
	if err != nil {
		t.Fatal(err)
	}
	if info.LastUpdate.IsZero() {
		t.Fatal("instant snapshot has no timestamp")
	}
}
