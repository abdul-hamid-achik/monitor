package mcp

import (
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func sysInfo(memPct, cpuPct float64) collector.SystemInfo {
	return collector.SystemInfo{
		CPU:    collector.CPUInfo{UsagePercent: cpuPct},
		Memory: collector.MemoryInfo{UsagePercent: memPct},
		Disk: collector.DiskInfo{Partitions: []collector.DiskPartitionInfo{
			{MountPoint: "/", UsagePercent: 35},
		}},
	}
}

func TestBuildSnapshotSummary(t *testing.T) {
	tests := []struct {
		name            string
		info            collector.SystemInfo
		wantContains    []string
		wantNotContains []string
		wantNext        int      // exact number of suggestions
		nextContains    []string // substrings that must appear across next
	}{
		{
			name:            "healthy system produces no suggestions",
			info:            sysInfo(42, 8),
			wantContains:    []string{"Memory 42%", "CPU 8%", "disk OK"},
			wantNotContains: []string{"(high)", "(critical)", "Swap"},
			wantNext:        0,
		},
		{
			name:         "high memory labels and suggests rss",
			info:         sysInfo(78, 12),
			wantContains: []string{"Memory 78% (high)", "CPU 12%"},
			wantNext:     1,
			nextContains: []string{"sort_by:rss"},
		},
		{
			name:         "critical memory suggests analyze",
			info:         sysInfo(92, 12),
			wantContains: []string{"Memory 92% (critical)"},
			wantNext:     1,
			nextContains: []string{"monitor_analyze"},
		},
		{
			name:         "high cpu labels and suggests cpu sort",
			info:         sysInfo(30, 81),
			wantContains: []string{"CPU 81% (high)"},
			wantNext:     1,
			nextContains: []string{"sort_by:cpu"},
		},
		{
			name: "full disk names the mount point",
			info: collector.SystemInfo{
				Memory: collector.MemoryInfo{UsagePercent: 30},
				Disk: collector.DiskInfo{Partitions: []collector.DiskPartitionInfo{
					{MountPoint: "/", UsagePercent: 40},
					{MountPoint: "/data", UsagePercent: 93},
				}},
			},
			wantContains: []string{"disk /data 93% (critical)"},
			wantNext:     1,
			nextContains: []string{"/data"},
		},
		{
			name: "swap pressure adds sentence and suggestion",
			info: collector.SystemInfo{
				Memory: collector.MemoryInfo{UsagePercent: 30, SwapTotal: 100, SwapUsed: 63},
				Disk:   collector.DiskInfo{Partitions: []collector.DiskPartitionInfo{{MountPoint: "/", UsagePercent: 20}}},
			},
			wantContains: []string{"Swap 63% used"},
			wantNext:     1,
			nextContains: []string{"Swap pressure"},
		},
		{
			name: "top consumer is named with formatted bytes",
			info: func() collector.SystemInfo {
				i := sysInfo(40, 5)
				i.Processes = []collector.ProcessInfo{
					{PID: 2, Name: "smallproc", Memory: 10 << 20},
					{PID: 1, Name: "chrome", Memory: 4509715660}, // FormatBytes -> "4.1 GB"
				}
				return i
			}(),
			wantContains: []string{"Top consumer: chrome (4.1 GB)"},
			wantNext:     0,
		},
		{
			name:            "no partitions reports disk unknown",
			info:            collector.SystemInfo{Memory: collector.MemoryInfo{UsagePercent: 10}},
			wantContains:    []string{"disk unknown"},
			wantNotContains: []string{"disk OK"},
			wantNext:        0,
		},
		{
			name:            "no processes omits top consumer",
			info:            sysInfo(10, 5),
			wantNotContains: []string{"Top consumer"},
			wantNext:        0,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			summary, next := buildSnapshotSummary(tt.info)
			for _, want := range tt.wantContains {
				if !strings.Contains(summary, want) {
					t.Errorf("summary %q missing %q", summary, want)
				}
			}
			for _, bad := range tt.wantNotContains {
				if strings.Contains(summary, bad) {
					t.Errorf("summary %q should not contain %q", summary, bad)
				}
			}
			if len(next) != tt.wantNext {
				t.Fatalf("len(next) = %d, want %d (next=%v)", len(next), tt.wantNext, next)
			}
			joined := strings.Join(next, " | ")
			for _, want := range tt.nextContains {
				if !strings.Contains(joined, want) {
					t.Errorf("next %q missing %q", joined, want)
				}
			}
		})
	}
}
