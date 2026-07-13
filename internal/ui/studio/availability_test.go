package studio

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"charm.land/lipgloss/v2"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func availabilityModel(t *testing.T, width, height int) Model {
	t.Helper()
	m := NewModelWithOptions(Options{DisableTemperatureSource: true})
	t.Cleanup(m.cancel)
	m.width, m.height = width, height
	return m
}

func TestAvailabilityViewsWaitBeforeFirstSample(t *testing.T) {
	m := availabilityModel(t, 80, 24)
	cases := []struct {
		name   string
		render func() string
	}{
		{"memory", m.renderMemory},
		{"network", m.renderNetwork},
		{"disk", m.renderDisk},
		{"temperature", m.renderTemperature},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plain := normalizedStudioText(tc.render())
			if !strings.Contains(plain, "Waiting for first") {
				t.Fatalf("waiting view missing explicit state:\n%s", plain)
			}
		})
	}
}

func TestAvailabilityViewsDoNotInventUnavailableZeroes(t *testing.T) {
	m := availabilityModel(t, 80, 24)
	m.last.LastUpdate = time.Now()

	m.last.Memory.MetricStates = map[string]collector.MetricStatus{
		"virtual": {State: collector.MetricUnavailable, Reason: "memory permission denied"},
		"swap":    {State: collector.MetricUnavailable, Reason: "swap permission denied"},
	}
	memory := normalizedStudioText(m.renderMemory())
	assertAvailabilityState(t, memory, "memory permission denied", "0.0%")

	m.last.Network.MetricStates = map[string]collector.MetricStatus{
		"io":   {State: collector.MetricUnavailable, Reason: "netstat denied"},
		"rate": {State: collector.MetricUnavailable, Reason: "netstat denied"},
	}
	network := normalizedStudioText(m.renderNetwork())
	assertAvailabilityState(t, network, "netstat denied", "0 B/s")

	m.last.Disk.MetricStates = map[string]collector.MetricStatus{
		"partitions": {State: collector.MetricUnavailable, Reason: "mount scan denied"},
		"io":         {State: collector.MetricUnavailable, Reason: "disk counters denied"},
	}
	disk := normalizedStudioText(m.renderDisk())
	assertAvailabilityState(t, disk, "mount scan denied", "0 B/s")

	m.last.Temperature.State = collector.MetricStatus{State: collector.MetricUnavailable, Reason: "sensor source denied"}
	temperature := normalizedStudioText(m.renderTemperature())
	assertAvailabilityState(t, temperature, "sensor source denied", "0.0°C")
}

func assertAvailabilityState(t *testing.T, body, reason, forbidden string) {
	t.Helper()
	for _, want := range []string{"Unavailable", reason, "monitor doctor"} {
		if !strings.Contains(body, want) {
			t.Errorf("availability state missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, forbidden) {
		t.Errorf("unavailable state presented invented value %q:\n%s", forbidden, body)
	}
}

func TestNetworkFirstRateSampleKeepsObservedTotals(t *testing.T) {
	m := availabilityModel(t, 40, 18)
	m.last.LastUpdate = time.Now()
	m.last.Network.BytesRecv = 8 << 20
	m.last.Network.BytesSent = 3 << 20
	m.last.Network.PacketsRecv = 1200
	m.last.Network.PacketsSent = 900
	m.last.Network.MetricStates = map[string]collector.MetricStatus{
		"io":   {State: collector.MetricObserved},
		"rate": {State: collector.MetricUnavailable, Reason: "first sample has no prior counter"},
	}
	plain := normalizedStudioText(m.renderNetwork())
	for _, want := range []string{"Rates waiting", "No prior counter", "Total", "8.0 MB", "3.0 MB"} {
		if !strings.Contains(plain, want) {
			t.Errorf("first network sample missing %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "0 B/s") {
		t.Fatalf("first rate sample must not invent a zero rate:\n%s", plain)
	}
}

func TestAvailabilityViewsFitRepresentativeTerminals(t *testing.T) {
	for _, size := range []struct{ width, height int }{{80, 24}, {40, 18}} {
		t.Run(fmt.Sprintf("%dx%d", size.width, size.height), func(t *testing.T) {
			m := availabilityModel(t, size.width, size.height)
			m.last.LastUpdate = time.Now()
			m.last.Memory = collector.MemoryInfo{
				TotalBytes: 16 << 30, UsedBytes: 8 << 30, AvailableBytes: 8 << 30, UsagePercent: 50,
				SwapTotal: 4 << 30, SwapUsed: 1 << 30, SwapFree: 3 << 30,
				MetricStates: map[string]collector.MetricStatus{"virtual": {State: collector.MetricObserved}, "swap": {State: collector.MetricObserved}},
			}
			m.last.Network = collector.NetworkInfo{
				BytesRecv: 9 << 30, BytesSent: 4 << 30, BytesRecvPerSec: 4 << 20, BytesSentPerSec: 2 << 20,
				PacketsRecv: 12000, PacketsSent: 8000,
				DownloadHistory: []float64{1, 4, 2}, UploadHistory: []float64{1, 2, 1},
				MetricStates: map[string]collector.MetricStatus{"io": {State: collector.MetricObserved}, "rate": {State: collector.MetricObserved}},
			}
			m.last.Disk.MetricStates = map[string]collector.MetricStatus{
				"partitions": {State: collector.MetricObserved}, "rate": {State: collector.MetricObserved},
			}
			for i := 0; i < 20; i++ {
				m.last.Disk.Partitions = append(m.last.Disk.Partitions, collector.DiskPartitionInfo{
					MountPoint: fmt.Sprintf("/Library/Developer/Very-Long-Volume-%02d", i),
					TotalBytes: 500 << 30, UsedBytes: 350 << 30, UsagePercent: 70,
				})
			}
			m.last.Disk.ReadPerSec, m.last.Disk.WritePerSec = 4<<20, 2<<20
			m.last.Disk.ReadHistory = []float64{1, 4, 2}
			m.last.Disk.WriteHistory = []float64{1, 2, 1}
			m.last.Temperature = collector.TemperatureInfo{
				CPUPackage: 55, CPUCores: 52, GPU: 48, ANE: 44, Battery: 35,
				Source: "estimated", State: collector.MetricStatus{State: collector.MetricObserved},
			}

			// Studio reserves two header rows and one footer row.
			contentHeight := size.height - 3
			for name, content := range map[string]string{
				"memory": m.renderMemory(), "network": m.renderNetwork(),
				"disk": m.renderDisk(), "temperature": m.renderTemperature(),
			} {
				if got := lipgloss.Width(content); got > size.width {
					t.Errorf("%s width=%d exceeds terminal width=%d:\n%s", name, got, size.width, content)
				}
				if got := lipgloss.Height(content); got > contentHeight {
					t.Errorf("%s height=%d exceeds available height=%d:\n%s", name, got, contentHeight, content)
				}
			}
		})
	}
}
