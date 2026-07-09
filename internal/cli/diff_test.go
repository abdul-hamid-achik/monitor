package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/baseline"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func TestPrintDiffRendersVerdicts(t *testing.T) {
	d := baseline.Diff{
		From: "pre", To: "live", CPUDelta: 12.4, MemDelta: 3.1, Load1Delta: 0.87,
		Verdicts: []baseline.Verdict{{
			Metric: baseline.MetricTotalRSS,
			Diagnosis: collector.Diagnosis{
				Summary:     "total RSS +36% vs baseline (was 1.5 GB, now 2.0 GB) — investigate",
				Evidence:    []string{"was 1.5 GB, now 2.0 GB (Δ +570.4 MB)"},
				Confidence:  analyzer.ConfidenceMedium,
				NextActions: []string{"monitor investigate <pid>"},
			},
		}},
	}
	var buf bytes.Buffer
	printDiff(&buf, d)
	out := buf.String()
	for _, want := range []string{"verdicts:", "total RSS +36%", "[medium confidence]", "evidence:", "next:"} {
		if !strings.Contains(out, want) {
			t.Errorf("printDiff output missing %q:\n%s", want, out)
		}
	}
}

func TestPrintDiffNoVerdictsOmitsSection(t *testing.T) {
	d := baseline.Diff{From: "pre", To: "live"}
	var buf bytes.Buffer
	printDiff(&buf, d)
	if strings.Contains(buf.String(), "verdicts:") {
		t.Errorf("printDiff with no verdicts should not print a verdicts section:\n%s", buf.String())
	}
}

func TestCaptureBaselineRecordsSwapDisk(t *testing.T) {
	info := collector.SystemInfo{
		Memory: collector.MemoryInfo{SwapUsed: 1 << 30, SwapTotal: 2 << 30},
		Disk: collector.DiskInfo{Partitions: []collector.DiskPartitionInfo{
			{MountPoint: "/", UsedBytes: 100, TotalBytes: 500},
		}},
	}
	b := captureBaseline(context.Background(), "t", info)
	if b.SwapUsed != 1<<30 || b.SwapTotal != 2<<30 {
		t.Errorf("swap not recorded: SwapUsed=%d SwapTotal=%d", b.SwapUsed, b.SwapTotal)
	}
	if b.DiskUsed != 100 || b.DiskTotal != 500 {
		t.Errorf("disk not recorded: DiskUsed=%d DiskTotal=%d", b.DiskUsed, b.DiskTotal)
	}
}

func TestRootPartition(t *testing.T) {
	tests := []struct {
		name      string
		parts     []collector.DiskPartitionInfo
		wantUsed  uint64
		wantTotal uint64
	}{
		{
			name: "prefers root",
			parts: []collector.DiskPartitionInfo{
				{MountPoint: "/data", UsedBytes: 900, TotalBytes: 9000},
				{MountPoint: "/", UsedBytes: 100, TotalBytes: 500},
			},
			wantUsed:  100,
			wantTotal: 500,
		},
		{
			name: "falls back to largest partition",
			parts: []collector.DiskPartitionInfo{
				{MountPoint: "/data", UsedBytes: 900, TotalBytes: 9000},
				{MountPoint: "/mnt/small", UsedBytes: 10, TotalBytes: 100},
			},
			wantUsed:  900,
			wantTotal: 9000,
		},
		{
			name:      "empty slice returns zeros",
			parts:     nil,
			wantUsed:  0,
			wantTotal: 0,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			used, total := rootPartition(tc.parts)
			if used != tc.wantUsed || total != tc.wantTotal {
				t.Errorf("rootPartition = (%d, %d), want (%d, %d)", used, total, tc.wantUsed, tc.wantTotal)
			}
		})
	}
}
