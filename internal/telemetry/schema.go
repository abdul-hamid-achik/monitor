// Package telemetry builds bounded, privacy-safe metric windows for external
// control planes. Its schema deliberately excludes host and process identity;
// transport, authentication, retries, and durable storage belong to the
// caller consuming the NDJSON stream.
package telemetry

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"
)

const (
	// SchemaVersion is the major compatibility version of WindowEnvelope.
	SchemaVersion = 1
	// Kind is the stable discriminator for each NDJSON line.
	Kind = "monitor.telemetry_window"

	MaxSamplesPerWindow = 3600
	MaxNDJSONLineBytes  = 32 * 1024
)

// MetricID names one fixed, non-identifying scalar series.
type MetricID string

const (
	MetricCPUUsage        MetricID = "system.cpu.usage"
	MetricMemoryUsed      MetricID = "system.memory.used"
	MetricMemoryAvailable MetricID = "system.memory.available"
	MetricMemoryUsage     MetricID = "system.memory.usage"
	MetricMemoryPressure  MetricID = "system.memory.pressure"
	MetricSwapUsed        MetricID = "system.swap.used"
	MetricNetworkReceive  MetricID = "system.network.receive_rate"
	MetricNetworkTransmit MetricID = "system.network.transmit_rate"
	MetricDiskRead        MetricID = "system.disk.read_rate"
	MetricDiskWrite       MetricID = "system.disk.write_rate"
	MetricLoadOneMinute   MetricID = "system.load.one_minute"
)

// Unit fixes the meaning of a MetricID across compatible schema revisions.
type Unit string

const (
	UnitPercent        Unit = "percent"
	UnitBytes          Unit = "bytes"
	UnitBytesPerSecond Unit = "bytes_per_second"
	UnitLoad           Unit = "load"
)

// AvailabilityState distinguishes an observed zero from missing telemetry.
type AvailabilityState string

const (
	AvailabilityObserved    AvailabilityState = "observed"
	AvailabilityPartial     AvailabilityState = "partial"
	AvailabilityUnsupported AvailabilityState = "unsupported"
	AvailabilityUnavailable AvailabilityState = "unavailable"
)

// Producer identifies the software contract producer, never the host.
type Producer struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Window describes the bounded sampling period represented by the line.
type Window struct {
	From             time.Time `json:"from"`
	To               time.Time `json:"to"`
	SampleIntervalMS int64     `json:"sample_interval_ms"`
	SampleCount      int       `json:"sample_count"`
	Partial          bool      `json:"partial"`
}

// MetricSummary is the fixed statistical projection for one metric.
type MetricSummary struct {
	Unit  Unit    `json:"unit"`
	Count int     `json:"count"`
	Min   float64 `json:"min"`
	Avg   float64 `json:"avg"`
	P95   float64 `json:"p95"`
	Max   float64 `json:"max"`
	Last  float64 `json:"last"`
}

// Availability summarizes collection success without forwarding raw errors.
type Availability struct {
	State           AvailabilityState `json:"state"`
	ObservedSamples int               `json:"observed_samples"`
	MissingSamples  int               `json:"missing_samples"`
}

// AlertSummary retains only an allowlisted rule, severity, and count. It never
// carries the collector's detail, PID, process name, diagnosis, or source path.
type AlertSummary struct {
	Rule     string `json:"rule"`
	Severity string `json:"severity"`
	Count    int    `json:"count"`
}

// WindowEnvelope is one independently ingestible NDJSON event.
type WindowEnvelope struct {
	SchemaVersion int                        `json:"schema_version"`
	Kind          string                     `json:"kind"`
	SessionID     string                     `json:"session_id"`
	Sequence      uint64                     `json:"sequence"`
	EmittedAt     time.Time                  `json:"emitted_at"`
	Producer      Producer                   `json:"producer"`
	Window        Window                     `json:"window"`
	Metrics       map[MetricID]MetricSummary `json:"metrics"`
	Availability  map[MetricID]Availability  `json:"availability"`
	Alerts        []AlertSummary             `json:"alerts,omitempty"`
}

type metricDefinition struct {
	ID   MetricID
	Unit Unit
}

var metricDefinitions = []metricDefinition{
	{ID: MetricCPUUsage, Unit: UnitPercent},
	{ID: MetricMemoryUsed, Unit: UnitBytes},
	{ID: MetricMemoryAvailable, Unit: UnitBytes},
	{ID: MetricMemoryUsage, Unit: UnitPercent},
	{ID: MetricMemoryPressure, Unit: UnitPercent},
	{ID: MetricSwapUsed, Unit: UnitBytes},
	{ID: MetricNetworkReceive, Unit: UnitBytesPerSecond},
	{ID: MetricNetworkTransmit, Unit: UnitBytesPerSecond},
	{ID: MetricDiskRead, Unit: UnitBytesPerSecond},
	{ID: MetricDiskWrite, Unit: UnitBytesPerSecond},
	{ID: MetricLoadOneMinute, Unit: UnitLoad},
}

var metricDefinitionByID = func() map[MetricID]metricDefinition {
	out := make(map[MetricID]metricDefinition, len(metricDefinitions))
	for _, definition := range metricDefinitions {
		out[definition.ID] = definition
	}
	return out
}()

var safeAlertRules = map[string]bool{
	"cpu_threshold": true,
	"disk_fill":     true,
	"mem_threshold": true,
	"swap_pressure": true,
}

var safeAlertSeverities = map[string]bool{
	"info":     true,
	"warning":  true,
	"critical": true,
}

// MetricIDs returns the complete V1 metric set in contract order.
func MetricIDs() []MetricID {
	out := make([]MetricID, len(metricDefinitions))
	for i, definition := range metricDefinitions {
		out[i] = definition.ID
	}
	return out
}

// Validate rejects malformed or unbounded envelopes before serialization.
func (e WindowEnvelope) Validate() error {
	if e.SchemaVersion != SchemaVersion {
		return fmt.Errorf("schema_version must be %d", SchemaVersion)
	}
	if e.Kind != Kind {
		return fmt.Errorf("kind must be %q", Kind)
	}
	if !validSessionID(e.SessionID) {
		return fmt.Errorf("session_id must be 32 lowercase hexadecimal characters")
	}
	if e.Sequence == 0 {
		return fmt.Errorf("sequence must be greater than zero")
	}
	if e.EmittedAt.IsZero() {
		return fmt.Errorf("emitted_at is required")
	}
	if !isUTC(e.EmittedAt) {
		return fmt.Errorf("emitted_at must be UTC")
	}
	if e.Producer.Name != "monitor" {
		return fmt.Errorf("producer.name must be %q", "monitor")
	}
	if !validVersion(e.Producer.Version) {
		return fmt.Errorf("producer.version must contain 1-32 safe characters")
	}
	if err := e.Window.validate(); err != nil {
		return err
	}
	if e.EmittedAt.Before(e.Window.To) {
		return fmt.Errorf("emitted_at must not precede window.to")
	}
	if len(e.Availability) != len(metricDefinitions) {
		return fmt.Errorf("availability must describe all %d metrics", len(metricDefinitions))
	}
	for id, availability := range e.Availability {
		if _, ok := metricDefinitionByID[id]; !ok {
			return fmt.Errorf("availability contains unknown metric %q", id)
		}
		if err := availability.validate(e.Window.SampleCount); err != nil {
			return fmt.Errorf("availability %s: %w", id, err)
		}
	}
	for id, metric := range e.Metrics {
		definition, ok := metricDefinitionByID[id]
		if !ok {
			return fmt.Errorf("metrics contains unknown metric %q", id)
		}
		if metric.Unit != definition.Unit {
			return fmt.Errorf("metric %s unit must be %q", id, definition.Unit)
		}
		if err := metric.validate(e.Window.SampleCount); err != nil {
			return fmt.Errorf("metric %s: %w", id, err)
		}
		if availability := e.Availability[id]; availability.ObservedSamples != metric.Count {
			return fmt.Errorf("metric %s count does not match availability", id)
		}
	}
	for id, availability := range e.Availability {
		_, observed := e.Metrics[id]
		if availability.ObservedSamples > 0 && !observed {
			return fmt.Errorf("metric %s is observed but has no summary", id)
		}
		if availability.ObservedSamples == 0 && observed {
			return fmt.Errorf("metric %s has a summary without observed samples", id)
		}
	}
	alertKeys := make(map[string]bool, len(e.Alerts))
	for _, alert := range e.Alerts {
		if !safeAlertRules[alert.Rule] {
			return fmt.Errorf("alert rule %q is not safe", alert.Rule)
		}
		if !safeAlertSeverities[alert.Severity] {
			return fmt.Errorf("alert severity %q is not safe", alert.Severity)
		}
		if alert.Count <= 0 || alert.Count > e.Window.SampleCount {
			return fmt.Errorf("alert %s count must be between 1 and sample_count", alert.Rule)
		}
		key := alert.Rule + "\x00" + alert.Severity
		if alertKeys[key] {
			return fmt.Errorf("alert %s/%s is duplicated", alert.Rule, alert.Severity)
		}
		alertKeys[key] = true
	}
	return nil
}

func (w Window) validate() error {
	if w.From.IsZero() || w.To.IsZero() {
		return fmt.Errorf("window.from and window.to are required")
	}
	if !isUTC(w.From) || !isUTC(w.To) {
		return fmt.Errorf("window.from and window.to must be UTC")
	}
	if !w.To.After(w.From) {
		return fmt.Errorf("window.to must be after window.from")
	}
	if w.SampleIntervalMS <= 0 {
		return fmt.Errorf("window.sample_interval_ms must be greater than zero")
	}
	if w.SampleCount <= 0 || w.SampleCount > MaxSamplesPerWindow {
		return fmt.Errorf("window.sample_count must be between 1 and %d", MaxSamplesPerWindow)
	}
	return nil
}

func (m MetricSummary) validate(sampleCount int) error {
	if m.Count <= 0 || m.Count > sampleCount {
		return fmt.Errorf("count must be between 1 and sample_count")
	}
	for name, value := range map[string]float64{
		"min": m.Min, "avg": m.Avg, "p95": m.P95, "max": m.Max, "last": m.Last,
	} {
		if math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%s must be finite", name)
		}
		if value < 0 {
			return fmt.Errorf("%s must be non-negative", name)
		}
		if m.Unit == UnitPercent && value > 100 {
			return fmt.Errorf("%s percent must not exceed 100", name)
		}
	}
	if m.Min > m.Avg || m.Avg > m.Max || m.P95 < m.Min || m.P95 > m.Max {
		return fmt.Errorf("summary statistics are inconsistent")
	}
	if m.Last < m.Min || m.Last > m.Max {
		return fmt.Errorf("last must be between min and max")
	}
	return nil
}

func (a Availability) validate(sampleCount int) error {
	if a.ObservedSamples < 0 || a.MissingSamples < 0 ||
		a.ObservedSamples+a.MissingSamples != sampleCount {
		return fmt.Errorf("sample counts must sum to sample_count")
	}
	switch a.State {
	case AvailabilityObserved:
		if a.ObservedSamples != sampleCount {
			return fmt.Errorf("observed state requires every sample")
		}
	case AvailabilityPartial:
		if a.ObservedSamples <= 0 || a.MissingSamples <= 0 {
			return fmt.Errorf("partial state requires observed and missing samples")
		}
	case AvailabilityUnsupported, AvailabilityUnavailable:
		if a.ObservedSamples != 0 {
			return fmt.Errorf("%s state cannot contain observed samples", a.State)
		}
	default:
		return fmt.Errorf("unknown state %q", a.State)
	}
	return nil
}

// MarshalNDJSON validates and serializes exactly one newline-terminated event.
func MarshalNDJSON(e WindowEnvelope) ([]byte, error) {
	if err := e.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > MaxNDJSONLineBytes {
		return nil, fmt.Errorf("telemetry line is %d bytes; maximum is %d", len(data), MaxNDJSONLineBytes)
	}
	return data, nil
}

func validSessionID(value string) bool {
	if len(value) != 32 || strings.ToLower(value) != value {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == 16
}

func validVersion(value string) bool {
	if len(value) == 0 || len(value) > 32 {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') || strings.ContainsRune(".+_-", r) {
			continue
		}
		return false
	}
	return true
}

func isUTC(value time.Time) bool {
	_, offset := value.Zone()
	return offset == 0
}
