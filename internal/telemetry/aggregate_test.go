package telemetry

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func observedInfo() collector.SystemInfo {
	observed := collector.MetricStatus{State: collector.MetricObserved}
	return collector.SystemInfo{
		Capture: collector.MetricStatus{State: collector.MetricObserved},
		CPU: collector.CPUInfo{
			UsagePercent: 10,
			LoadAvg1:     0.5,
			MetricStates: map[string]collector.MetricStatus{
				"usage": observed, "load_average": observed,
			},
		},
		Memory: collector.MemoryInfo{
			UsedBytes:      100,
			AvailableBytes: 900,
			UsagePercent:   10,
			MemoryPressure: 11,
			SwapUsed:       5,
			MetricStates: map[string]collector.MetricStatus{
				"virtual": observed, "swap": observed,
			},
		},
		Network: collector.NetworkInfo{
			BytesRecvPerSec: 20,
			BytesSentPerSec: 30,
			MetricStates:    map[string]collector.MetricStatus{"rate": observed},
		},
		Disk: collector.DiskInfo{
			ReadPerSec:  40,
			WritePerSec: 50,
			MetricStates: map[string]collector.MetricStatus{
				"rate": observed,
			},
		},
	}
}

func buildTestEnvelope(t *testing.T, builder *Builder) WindowEnvelope {
	t.Helper()
	from := time.Date(2026, 7, 24, 12, 0, 0, 0, time.UTC)
	envelope, err := builder.Build(
		"0123456789abcdef0123456789abcdef", 1, "1.14.0",
		from, from.Add(5*time.Second), from.Add(5*time.Second),
		5*time.Second, true,
	)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	return envelope
}

func TestMetricIDsAreTheFixedV1Set(t *testing.T) {
	want := []MetricID{
		MetricCPUUsage,
		MetricMemoryUsed,
		MetricMemoryAvailable,
		MetricMemoryUsage,
		MetricMemoryPressure,
		MetricSwapUsed,
		MetricNetworkReceive,
		MetricNetworkTransmit,
		MetricDiskRead,
		MetricDiskWrite,
		MetricLoadOneMinute,
	}
	got := MetricIDs()
	if len(got) != 11 {
		t.Fatalf("MetricIDs count = %d, want 11", len(got))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("MetricIDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}

func TestBuilderSummarizesNearestRankP95(t *testing.T) {
	builder := NewBuilder()
	for i := 1; i <= 20; i++ {
		info := observedInfo()
		info.CPU.UsagePercent = float64(i)
		if err := builder.Add(info, nil); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	envelope := buildTestEnvelope(t, builder)
	cpu := envelope.Metrics[MetricCPUUsage]
	if cpu.Count != 20 || cpu.Min != 1 || cpu.Avg != 10.5 ||
		cpu.P95 != 19 || cpu.Max != 20 || cpu.Last != 20 {
		t.Fatalf("CPU summary = %+v", cpu)
	}
	if got := envelope.Availability[MetricCPUUsage]; got != (Availability{
		State: AvailabilityObserved, ObservedSamples: 20, MissingSamples: 0,
	}) {
		t.Fatalf("CPU availability = %+v", got)
	}
}

func TestBuilderAvailabilityIsHonest(t *testing.T) {
	builder := NewBuilder()
	if err := builder.Add(observedInfo(), nil); err != nil {
		t.Fatal(err)
	}
	missing := observedInfo()
	missing.CPU.MetricStates["usage"] = collector.MetricStatus{
		State: collector.MetricUnavailable, Reason: "/private/secret collector failure",
	}
	missing.CPU.MetricStates["load_average"] = collector.MetricStatus{
		State: collector.MetricUnsupported, Reason: "secret unsupported reason",
	}
	if err := builder.Add(missing, nil); err != nil {
		t.Fatal(err)
	}
	envelope := buildTestEnvelope(t, builder)
	if got := envelope.Availability[MetricCPUUsage]; got.State != AvailabilityPartial ||
		got.ObservedSamples != 1 || got.MissingSamples != 1 {
		t.Fatalf("CPU availability = %+v", got)
	}
	if got := envelope.Availability[MetricLoadOneMinute]; got.State != AvailabilityPartial {
		t.Fatalf("load availability = %+v, want partial", got)
	}
	if envelope.Metrics[MetricCPUUsage].Count != 1 {
		t.Fatalf("CPU observed count = %d, want 1", envelope.Metrics[MetricCPUUsage].Count)
	}

	unsupported := NewBuilder()
	info := observedInfo()
	info.CPU.MetricStates["load_average"] = collector.MetricStatus{
		State: collector.MetricUnsupported, Reason: "sensitive reason",
	}
	if err := unsupported.Add(info, nil); err != nil {
		t.Fatal(err)
	}
	unsupportedEnvelope := buildTestEnvelope(t, unsupported)
	if got := unsupportedEnvelope.Availability[MetricLoadOneMinute]; got.State != AvailabilityUnsupported {
		t.Fatalf("unsupported load availability = %+v", got)
	}
	if _, exists := unsupportedEnvelope.Metrics[MetricLoadOneMinute]; exists {
		t.Fatal("unsupported load must not have a numeric summary")
	}
}

func TestBuilderRejectsNonFiniteAndOutOfRangeValues(t *testing.T) {
	info := observedInfo()
	info.CPU.UsagePercent = math.NaN()
	info.Memory.UsagePercent = 101
	info.CPU.LoadAvg1 = math.Inf(1)
	builder := NewBuilder()
	if err := builder.Add(info, nil); err != nil {
		t.Fatal(err)
	}
	envelope := buildTestEnvelope(t, builder)
	for _, id := range []MetricID{MetricCPUUsage, MetricMemoryUsage, MetricLoadOneMinute} {
		if got := envelope.Availability[id]; got.State != AvailabilityUnavailable {
			t.Errorf("%s availability = %+v, want unavailable", id, got)
		}
		if _, exists := envelope.Metrics[id]; exists {
			t.Errorf("%s unexpectedly has a numeric summary", id)
		}
	}
}

func TestAlertAggregationIsAllowlistedSanitizedAndPerSample(t *testing.T) {
	builder := NewBuilder()
	alerts := []collector.Alert{
		{
			Rule: "disk_fill", Severity: "warning", PID: 4242,
			Process: "SECRET_PROCESS", Detail: "/home/SECRET_USER/private at 99%",
			Diagnosis: &collector.Diagnosis{
				Summary:  "SECRET_DIAGNOSIS",
				Evidence: []string{"SECRET_EVIDENCE"},
			},
		},
		{Rule: "disk_fill", Severity: "warning", Detail: "/another-secret-mount"},
		{Rule: "rss_growth", Severity: "warning", Process: "SECRET_RSS_PROCESS"},
		{Rule: "swap_pressure", Severity: "verbose", Detail: "SECRET_SEVERITY"},
	}
	if err := builder.Add(observedInfo(), alerts); err != nil {
		t.Fatal(err)
	}
	envelope := buildTestEnvelope(t, builder)
	if len(envelope.Alerts) != 1 || envelope.Alerts[0] != (AlertSummary{
		Rule: "disk_fill", Severity: "warning", Count: 1,
	}) {
		t.Fatalf("alerts = %+v", envelope.Alerts)
	}
	data, err := MarshalNDJSON(envelope)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"SECRET_PROCESS", "SECRET_USER", "SECRET_DIAGNOSIS",
		"SECRET_EVIDENCE", "SECRET_RSS_PROCESS", "SECRET_SEVERITY",
	} {
		if strings.Contains(string(data), secret) {
			t.Errorf("serialized telemetry leaked %q: %s", secret, data)
		}
	}
}

func TestPrivacyProjectionExcludesIdentityAndRawReasons(t *testing.T) {
	info := observedInfo()
	info.Hostname = "SECRET_HOSTNAME"
	info.OS = "SECRET_OS"
	info.Platform = "SECRET_PLATFORM"
	info.Kernel = "SECRET_KERNEL"
	info.Processes = []collector.ProcessInfo{{
		PID: 99, Parent: 1, Name: "SECRET_PROCESS", User: "SECRET_USER",
	}}
	info.Disk.Partitions = []collector.DiskPartitionInfo{{
		Device: "SECRET_DEVICE", MountPoint: "/SECRET_MOUNT", Filesystem: "SECRET_FS",
	}}
	info.Network.MetricStates["rate"] = collector.MetricStatus{
		State:  collector.MetricUnavailable,
		Reason: "SECRET_RAW_ERROR /home/alice/token",
	}
	builder := NewBuilder()
	if err := builder.Add(info, nil); err != nil {
		t.Fatal(err)
	}
	data, err := MarshalNDJSON(buildTestEnvelope(t, builder))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{
		"SECRET_HOSTNAME", "SECRET_OS", "SECRET_PLATFORM", "SECRET_KERNEL",
		"SECRET_PROCESS", "SECRET_USER", "SECRET_DEVICE", "SECRET_MOUNT",
		"SECRET_FS", "SECRET_RAW_ERROR", "/home/alice/token",
	} {
		if strings.Contains(string(data), secret) {
			t.Errorf("serialized telemetry leaked %q: %s", secret, data)
		}
	}
	for _, forbiddenKey := range []string{
		`"hostname"`, `"pid"`, `"process"`, `"path"`, `"mount_point"`,
		`"device"`, `"reason"`, `"detail"`, `"diagnosis"`,
	} {
		if strings.Contains(string(data), forbiddenKey) {
			t.Errorf("serialized telemetry contains forbidden key %s: %s", forbiddenKey, data)
		}
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
}

func TestBuilderEnforcesMaximumSamples(t *testing.T) {
	builder := NewBuilder()
	info := observedInfo()
	for i := 0; i < MaxSamplesPerWindow; i++ {
		if err := builder.Add(info, nil); err != nil {
			t.Fatalf("Add %d: %v", i, err)
		}
	}
	if err := builder.Add(info, nil); err == nil {
		t.Fatal("expected maximum-sample error")
	}
}

func TestAllMetricsUnavailableWindowIsValid(t *testing.T) {
	info := observedInfo()
	info.Capture = collector.MetricStatus{
		State:  collector.MetricUnavailable,
		Reason: "SECRET_CAPTURE_FAILURE /private/path",
	}
	builder := NewBuilder()
	if err := builder.Add(info, nil); err != nil {
		t.Fatal(err)
	}
	envelope := buildTestEnvelope(t, builder)
	if len(envelope.Metrics) != 0 {
		t.Fatalf("metrics = %+v, want no numeric summaries", envelope.Metrics)
	}
	if len(envelope.Availability) != 11 {
		t.Fatalf("availability count = %d, want 11", len(envelope.Availability))
	}
	for id, availability := range envelope.Availability {
		if availability.State != AvailabilityUnavailable ||
			availability.ObservedSamples != 0 || availability.MissingSamples != 1 {
			t.Errorf("%s availability = %+v", id, availability)
		}
	}
	data, err := MarshalNDJSON(envelope)
	if err != nil {
		t.Fatalf("MarshalNDJSON: %v", err)
	}
	if strings.Contains(string(data), "SECRET_CAPTURE_FAILURE") ||
		strings.Contains(string(data), "/private/path") {
		t.Fatalf("unavailable window leaked raw reason: %s", data)
	}
}
