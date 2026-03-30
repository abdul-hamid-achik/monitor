package system

import (
	"context"
	"testing"
	"time"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		bytes    uint64
		expected string
	}{
		{0, "00 B"},
		{100, "100 B"},
		{1024, "1.0 KB"},
		{1536, "1.5 KB"},
		{1024 * 1024, "1.0 MB"},
		{1024 * 1024 * 1024, "1.0 GB"},
	}

	for _, tt := range tests {
		result := FormatBytes(tt.bytes)
		if result != tt.expected {
			t.Errorf("FormatBytes(%d) = %s, expected %s", tt.bytes, result, tt.expected)
		}
	}
}

func TestFormatUint64(t *testing.T) {
	tests := []struct {
		n        uint64
		expected string
	}{
		{0, "00"},
		{5, "05"},
		{42, "42"},
		{1000, "1000"},
		{999999, "999999"},
	}

	for _, tt := range tests {
		result := formatUint64(tt.n)
		if result != tt.expected {
			t.Errorf("formatUint64(%d) = %s, expected %s", tt.n, result, tt.expected)
		}
	}
}

func TestDiskInfoStructure(t *testing.T) {
	// Test that DiskInfo can be properly populated
	diskInfo := DiskInfo{
		LastUpdate: time.Now(),
	}

	partition := DiskPartitionInfo{
		Device:       "/dev/disk1s1",
		MountPoint:   "/",
		TotalBytes:   1000000000,
		UsedBytes:    500000000,
		FreeBytes:    500000000,
		UsagePercent: 50.0,
		Filesystem:   "apfs",
	}

	diskInfo.Partitions = append(diskInfo.Partitions, partition)

	if len(diskInfo.Partitions) != 1 {
		t.Errorf("Expected 1 partition, got %d", len(diskInfo.Partitions))
	}

	p := diskInfo.Partitions[0]
	if p.MountPoint != "/" {
		t.Errorf("Expected mount point '/', got %s", p.MountPoint)
	}
	if p.UsagePercent != 50.0 {
		t.Errorf("Expected 50%% usage, got %.2f%%", p.UsagePercent)
	}
}

func TestCollectorCollectDisk(t *testing.T) {
	collector := NewCollector()

	// Test that collectDisk doesn't panic
	ctx := context.Background()
	collector.collectDisk(ctx)

	// Get info after collection
	info := collector.GetInfo()

	// We should have at least some partition info
	if info.Disk.LastUpdate.IsZero() {
		t.Error("Expected Disk.LastUpdate to be set after collection")
	}

	// On most systems, we should have at least one partition
	if len(info.Disk.Partitions) == 0 {
		t.Log("No disk partitions found (this is acceptable on some systems)")
	}
}
