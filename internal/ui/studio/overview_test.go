package studio

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
)

func operationalOverviewFixture(t *testing.T, width, height int) Model {
	t.Helper()
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	t.Cleanup(m.cancel)
	m.ready = true
	m.width, m.height = width, height
	m.settings = config.Default()
	m.settings.CPUAlertThreshold = 80
	m.settings.MemoryAlertThreshold = 90
	m.last.LastUpdate = time.Now()
	m.last.Capture = collector.MetricStatus{State: collector.MetricObserved}
	m.last.CPU = collector.CPUInfo{
		UsagePercent: 55.5,
		FrequencyMHz: 3200,
		CoreCount:    10,
		History:      operationalHistory(42, 60),
	}
	m.last.Memory = collector.MemoryInfo{
		TotalBytes:   16 << 30,
		UsedBytes:    8 << 30,
		UsagePercent: 50,
		History:      operationalHistory(36, 60),
	}
	m.last.Temperature = collector.TemperatureInfo{
		CPUPackage: 52,
		Source:     "powermetrics",
		Available:  true,
		State:      collector.MetricStatus{State: collector.MetricObserved},
	}
	m.last.Disk = collector.DiskInfo{Partitions: []collector.DiskPartitionInfo{{
		MountPoint:   "/",
		TotalBytes:   500 << 30,
		UsedBytes:    365 << 30,
		UsagePercent: 73,
	}}}
	m.last.Network.MetricStates = map[string]collector.MetricStatus{
		"rate": {State: collector.MetricObserved},
	}
	m.last.ProcessesState = collector.MetricStatus{State: collector.MetricObserved}
	m.last.Processes = []collector.ProcessInfo{
		{PID: 8421, Name: "node", CPUPercent: 82.3, Memory: 2 << 30},
		{PID: 9102, Name: "chrome", CPUPercent: 12.7, Memory: 1 << 30},
		{PID: 7788, Name: "go", CPUPercent: 8.3, Memory: 890 << 20},
	}
	return m
}

func operationalHistory(start float64, count int) []float64 {
	out := make([]float64, count)
	for i := range out {
		out[i] = start + float64(i%9)
	}
	return out
}

func TestOperationalOverviewResponsiveCockpit(t *testing.T) {
	for _, size := range []struct {
		width  int
		height int
	}{{120, 40}, {80, 24}, {60, 20}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			m := operationalOverviewFixture(t, size.width, size.height)
			got := m.renderOperationalOverview()
			plain := ansi.Strip(got)
			for _, want := range []string{
				"CPU", "MEMORY", "THERMAL", "DISK", "ACTIVITY", "TOP CPU PROCESSES", "node", "real",
			} {
				if !strings.Contains(plain, want) {
					t.Errorf("overview missing %q:\n%s", want, plain)
				}
			}
			if gotWidth := lipgloss.Width(got); gotWidth > size.width {
				t.Errorf("overview width=%d exceeds terminal width=%d", gotWidth, size.width)
			}
			if gotHeight := lipgloss.Height(got); gotHeight > size.height-3 {
				t.Errorf("overview height=%d exceeds content budget=%d:\n%s", gotHeight, size.height-3, plain)
			}
			for i, line := range strings.Split(got, "\n") {
				if lineWidth := lipgloss.Width(line); lineWidth > size.width {
					t.Errorf("line %d width=%d exceeds terminal width=%d", i, lineWidth, size.width)
				}
			}
		})
	}
}

func TestOperationalOverviewTemperatureTruth(t *testing.T) {
	tests := []struct {
		name      string
		temp      collector.TemperatureInfo
		want      []string
		notWanted string
	}{
		{
			name: "real",
			temp: collector.TemperatureInfo{CPUPackage: 52, Source: "powermetrics", Available: true,
				State: collector.MetricStatus{State: collector.MetricObserved}},
			want: []string{"52.0 C", "real"},
		},
		{
			name: "estimated",
			temp: collector.TemperatureInfo{CPUPackage: 47.5, Source: "estimated", Available: true,
				State: collector.MetricStatus{State: collector.MetricObserved}},
			want: []string{"47.5 C", "estimated"},
		},
		{
			name: "unavailable",
			temp: collector.TemperatureInfo{Available: false,
				State: collector.MetricStatus{State: collector.MetricUnavailable, Reason: "permission denied"}},
			want:      []string{"unavailable", "permission denied"},
			notWanted: "0.0 C",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := operationalOverviewFixture(t, 120, 40)
			m.last.Temperature = tc.temp
			plain := ansi.Strip(m.renderOperationalOverview())
			for _, want := range tc.want {
				if !strings.Contains(plain, want) {
					t.Errorf("temperature overview missing %q:\n%s", want, plain)
				}
			}
			if tc.notWanted != "" && strings.Contains(plain, tc.notWanted) {
				t.Errorf("temperature overview invented unavailable value %q:\n%s", tc.notWanted, plain)
			}
		})
	}
}

func TestOperationalOverviewAttentionRail(t *testing.T) {
	m := operationalOverviewFixture(t, 120, 40)
	m.last.CPU.UsagePercent = 85
	plain := ansi.Strip(m.renderOperationalOverview())
	if !strings.Contains(plain, "ATTENTION | CPU 85.0% >= 80% threshold") {
		t.Fatalf("threshold attention is missing:\n%s", plain)
	}

	m.last.CPU.UsagePercent = 40
	m.settings.CPUAlertThreshold = 0
	m.last.CPU.MetricStates = map[string]collector.MetricStatus{
		"usage": {State: collector.MetricUnavailable, Reason: "sampler failed"},
	}
	plain = ansi.Strip(m.renderOperationalOverview())
	if !strings.Contains(plain, "ATTENTION | CPU unavailable | sampler failed") {
		t.Fatalf("metric-state degradation is missing:\n%s", plain)
	}
}

func TestOperationalOverviewSurfacesAnalyzerAlert(t *testing.T) {
	m := operationalOverviewFixture(t, 120, 40)
	m.alerts = []collector.Alert{{
		Rule:   "cpu_spike",
		Detail: "node (pid 8421) at 82.3% vs 24.1% baseline",
	}}
	plain := ansi.Strip(m.renderOperationalOverview())
	if !strings.Contains(plain, "ATTENTION | CPU SPIKE | node (pid 8421)") {
		t.Fatalf("analyzer finding is missing from the attention rail:\n%s", plain)
	}
}

func TestOperationalOverviewActivityEmptyState(t *testing.T) {
	m := operationalOverviewFixture(t, 80, 24)
	m.last.CPU.History = nil
	m.last.Memory.History = nil
	plain := ansi.Strip(m.renderOperationalOverview())
	for _, want := range []string{"CPU no samples yet", "MEM no samples yet"} {
		if !strings.Contains(plain, want) {
			t.Errorf("activity empty state missing %q:\n%s", want, plain)
		}
	}
}

func TestOperationalOverviewPrefersStartupDiskOverPseudoMount(t *testing.T) {
	m := operationalOverviewFixture(t, 120, 40)
	m.last.Disk.Partitions = []collector.DiskPartitionInfo{
		{MountPoint: "/dev", Filesystem: "devfs", TotalBytes: 240 << 10, UsedBytes: 240 << 10, UsagePercent: 100},
		{MountPoint: "/", Filesystem: "apfs", TotalBytes: 500 << 30, UsedBytes: 480 << 30, UsagePercent: 96},
	}

	value, note := m.operationalDiskSummary()
	if value != "96.0%" || !strings.Contains(note, "/ |") {
		t.Fatalf("disk summary = %q, %q; want startup volume rather than devfs", value, note)
	}
}

func TestOperationalOverviewOmitsImplausibleCPUFrequency(t *testing.T) {
	m := operationalOverviewFixture(t, 120, 40)
	m.last.CPU.FrequencyMHz = 4

	plain := ansi.Strip(m.renderOperationalOverview())
	if strings.Contains(plain, "0.00 GHz") {
		t.Fatalf("overview must not present a platform placeholder as a real frequency:\n%s", plain)
	}
	if !strings.Contains(plain, "10 cores") {
		t.Fatalf("overview lost the useful CPU topology:\n%s", plain)
	}
}
