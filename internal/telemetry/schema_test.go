package telemetry

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestV1GoldenNDJSON(t *testing.T) {
	builder := NewBuilder()
	if err := builder.Add(observedInfo(), nil); err != nil {
		t.Fatal(err)
	}
	envelope := buildTestEnvelope(t, builder)
	data, err := MarshalNDJSON(envelope)
	if err != nil {
		t.Fatal(err)
	}
	goldenPath := filepath.Join("testdata", "telemetry_v1.ndjson")
	golden, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, golden) {
		t.Fatalf("V1 NDJSON changed\ngenerated: %s\ngolden:    %s", data, golden)
	}

	var decoded WindowEnvelope
	if err := json.Unmarshal(golden, &decoded); err != nil {
		t.Fatalf("decode golden corpus: %v", err)
	}
	if err := decoded.Validate(); err != nil {
		t.Fatalf("validate golden corpus: %v", err)
	}

	hashFile, err := os.ReadFile(filepath.Join("testdata", "telemetry_v1.sha256"))
	if err != nil {
		t.Fatal(err)
	}
	got := fmt.Sprintf("%x", sha256.Sum256(data))
	want := strings.TrimSpace(string(hashFile))
	if got != want {
		t.Fatalf("V1 NDJSON hash changed (got %s, want %s)", got, want)
	}
}

func TestMarshalNDJSONIsBoundedAndNewlineTerminated(t *testing.T) {
	builder := NewBuilder()
	if err := builder.Add(observedInfo(), nil); err != nil {
		t.Fatal(err)
	}
	data, err := MarshalNDJSON(buildTestEnvelope(t, builder))
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > MaxNDJSONLineBytes {
		t.Fatalf("line size = %d, maximum = %d", len(data), MaxNDJSONLineBytes)
	}
	if len(data) == 0 || data[len(data)-1] != '\n' || bytes.Count(data, []byte{'\n'}) != 1 {
		t.Fatalf("output is not exactly one newline-terminated line: %q", data)
	}
}

func TestEnvelopeValidationRejectsContractViolations(t *testing.T) {
	newEnvelope := func(t *testing.T) WindowEnvelope {
		t.Helper()
		builder := NewBuilder()
		if err := builder.Add(observedInfo(), nil); err != nil {
			t.Fatal(err)
		}
		return buildTestEnvelope(t, builder)
	}
	tests := []struct {
		name   string
		mutate func(*WindowEnvelope)
		want   string
	}{
		{"schema", func(e *WindowEnvelope) { e.SchemaVersion = 2 }, "schema_version"},
		{"kind", func(e *WindowEnvelope) { e.Kind = "other" }, "kind"},
		{"session", func(e *WindowEnvelope) { e.SessionID = "SECRET_HOST" }, "session_id"},
		{"sequence", func(e *WindowEnvelope) { e.Sequence = 0 }, "sequence"},
		{"emitted_at_not_utc", func(e *WindowEnvelope) {
			e.EmittedAt = e.EmittedAt.In(time.FixedZone("private", 3600))
		}, "emitted_at must be UTC"},
		{"emitted_before_window", func(e *WindowEnvelope) {
			e.EmittedAt = e.Window.To.Add(-time.Nanosecond)
		}, "must not precede window.to"},
		{"producer", func(e *WindowEnvelope) { e.Producer.Version = "not safe!" }, "producer.version"},
		{"window_not_utc", func(e *WindowEnvelope) {
			e.Window.From = e.Window.From.In(time.FixedZone("private", 3600))
		}, "window.from and window.to must be UTC"},
		{"empty_window", func(e *WindowEnvelope) { e.Window.SampleCount = 0 }, "sample_count"},
		{"zero_duration_window", func(e *WindowEnvelope) {
			e.Window.To = e.Window.From
			e.EmittedAt = e.Window.To
		}, "window.to must be after"},
		{"unknown_metric", func(e *WindowEnvelope) {
			e.Metrics[MetricID("secret.metric")] = MetricSummary{Unit: UnitBytes, Count: 1}
		}, "unknown metric"},
		{"wrong_unit", func(e *WindowEnvelope) {
			m := e.Metrics[MetricCPUUsage]
			m.Unit = UnitBytes
			e.Metrics[MetricCPUUsage] = m
		}, "unit"},
		{"last_outside_range", func(e *WindowEnvelope) {
			m := e.Metrics[MetricCPUUsage]
			m.Last = m.Max + 1
			e.Metrics[MetricCPUUsage] = m
		}, "last"},
		{"percent_above_100", func(e *WindowEnvelope) {
			m := e.Metrics[MetricCPUUsage]
			m.Min, m.Avg, m.P95, m.Max, m.Last = 101, 101, 101, 101, 101
			e.Metrics[MetricCPUUsage] = m
		}, "must not exceed 100"},
		{"missing_availability", func(e *WindowEnvelope) {
			delete(e.Availability, MetricCPUUsage)
		}, "availability must describe"},
		{"unknown_availability", func(e *WindowEnvelope) {
			delete(e.Availability, MetricCPUUsage)
			e.Availability[MetricID("private.metric")] = Availability{
				State: AvailabilityObserved, ObservedSamples: 1,
			}
		}, "unknown metric"},
		{"unsafe_alert", func(e *WindowEnvelope) {
			e.Alerts = []AlertSummary{{Rule: "rss_growth", Severity: "warning", Count: 1}}
		}, "not safe"},
		{"duplicate_alert", func(e *WindowEnvelope) {
			e.Alerts = []AlertSummary{
				{Rule: "disk_fill", Severity: "warning", Count: 1},
				{Rule: "disk_fill", Severity: "warning", Count: 1},
			}
		}, "duplicated"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			envelope := newEnvelope(t)
			tt.mutate(&envelope)
			err := envelope.Validate()
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("Validate error = %v, want %q", err, tt.want)
			}
		})
	}
}

func TestSessionIDAndVersionValidation(t *testing.T) {
	for _, value := range []string{
		"0123456789abcdef0123456789abcdef",
		"00000000000000000000000000000000",
	} {
		if !validSessionID(value) {
			t.Errorf("validSessionID(%q) = false", value)
		}
	}
	for _, value := range []string{
		"", "ABCDEF0123456789ABCDEF0123456789", "0123", strings.Repeat("0", 34),
	} {
		if validSessionID(value) {
			t.Errorf("validSessionID(%q) = true", value)
		}
	}
	for _, value := range []string{"dev", "1.14.0", "v1.14.0-1-gabcdef-dirty"} {
		if !validVersion(value) {
			t.Errorf("validVersion(%q) = false", value)
		}
	}
	for _, value := range []string{"", "version with spaces", "v1/secret", strings.Repeat("a", 33)} {
		if validVersion(value) {
			t.Errorf("validVersion(%q) = true", value)
		}
	}
}

func FuzzBuilderNeverSerializesInvalidNumbers(f *testing.F) {
	f.Add(10.0, 20.0)
	f.Add(-1.0, 101.0)
	f.Fuzz(func(t *testing.T, cpu, memory float64) {
		info := observedInfo()
		info.CPU.UsagePercent = cpu
		info.Memory.UsagePercent = memory
		builder := NewBuilder()
		if err := builder.Add(info, nil); err != nil {
			t.Fatal(err)
		}
		data, err := MarshalNDJSON(buildTestEnvelope(t, builder))
		if err != nil {
			t.Fatal(err)
		}
		for _, invalid := range []string{"NaN", "Infinity", "-Infinity"} {
			if bytes.Contains(data, []byte(invalid)) {
				t.Fatalf("serialized invalid number %q: %s", invalid, data)
			}
		}
	})
}
