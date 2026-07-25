package telemetry

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

type metricAccumulator struct {
	values []float64
}

type availabilityAccumulator struct {
	observed    int
	unsupported int
	unavailable int
}

type alertKey struct {
	rule     string
	severity string
}

// Builder accumulates a single bounded telemetry window.
type Builder struct {
	samples      int
	metrics      map[MetricID]*metricAccumulator
	availability map[MetricID]*availabilityAccumulator
	alerts       map[alertKey]int
}

// NewBuilder allocates a fixed-size set of metric accumulators.
func NewBuilder() *Builder {
	builder := &Builder{
		metrics:      make(map[MetricID]*metricAccumulator, len(metricDefinitions)),
		availability: make(map[MetricID]*availabilityAccumulator, len(metricDefinitions)),
		alerts:       make(map[alertKey]int),
	}
	for _, definition := range metricDefinitions {
		builder.metrics[definition.ID] = &metricAccumulator{}
		builder.availability[definition.ID] = &availabilityAccumulator{}
	}
	return builder
}

// SampleCount reports how many collector snapshots are in the current window.
func (b *Builder) SampleCount() int {
	if b == nil {
		return 0
	}
	return b.samples
}

// Add projects one collector snapshot into the fixed V1 metric set. Raw
// availability reasons and alert details are deliberately discarded.
func (b *Builder) Add(info collector.SystemInfo, alerts []collector.Alert) error {
	if b == nil {
		return fmt.Errorf("telemetry builder is nil")
	}
	if b.samples >= MaxSamplesPerWindow {
		return fmt.Errorf("telemetry window exceeds %d samples", MaxSamplesPerWindow)
	}
	b.samples++
	for _, definition := range metricDefinitions {
		value, state := reading(info, definition.ID)
		if state == collector.MetricObserved && !validMetricValue(definition.Unit, value) {
			state = collector.MetricUnavailable
		}
		availability := b.availability[definition.ID]
		switch state {
		case collector.MetricObserved, "":
			b.metrics[definition.ID].values = append(b.metrics[definition.ID].values, value)
			availability.observed++
		case collector.MetricUnsupported:
			availability.unsupported++
		default:
			availability.unavailable++
		}
	}

	// Count at most one occurrence of a rule/severity per sample. Disk-fill can
	// produce one alert per mount, but the exported count means affected sample
	// count and therefore stays bounded by Window.SampleCount.
	seen := make(map[alertKey]bool)
	for _, alert := range alerts {
		key := alertKey{
			rule:     strings.ToLower(strings.TrimSpace(alert.Rule)),
			severity: strings.ToLower(strings.TrimSpace(alert.Severity)),
		}
		if !safeAlertRules[key.rule] || !safeAlertSeverities[key.severity] || seen[key] {
			continue
		}
		seen[key] = true
		b.alerts[key]++
	}
	return nil
}

// Build creates and validates one envelope without mutating the builder.
func (b *Builder) Build(sessionID string, sequence uint64, producerVersion string, from, to, emittedAt time.Time, interval time.Duration, partial bool) (WindowEnvelope, error) {
	if b == nil || b.samples == 0 {
		return WindowEnvelope{}, fmt.Errorf("cannot build an empty telemetry window")
	}
	envelope := WindowEnvelope{
		SchemaVersion: SchemaVersion,
		Kind:          Kind,
		SessionID:     sessionID,
		Sequence:      sequence,
		EmittedAt:     emittedAt.UTC(),
		Producer:      Producer{Name: "monitor", Version: producerVersion},
		Window: Window{
			From:             from.UTC(),
			To:               to.UTC(),
			SampleIntervalMS: interval.Milliseconds(),
			SampleCount:      b.samples,
			Partial:          partial,
		},
		Metrics:      make(map[MetricID]MetricSummary, len(metricDefinitions)),
		Availability: make(map[MetricID]Availability, len(metricDefinitions)),
	}
	for _, definition := range metricDefinitions {
		values := b.metrics[definition.ID].values
		availability := b.availability[definition.ID]
		envelope.Availability[definition.ID] = summarizeAvailability(availability, b.samples)
		if len(values) > 0 {
			envelope.Metrics[definition.ID] = summarizeMetric(definition.Unit, values)
		}
	}
	for key, count := range b.alerts {
		envelope.Alerts = append(envelope.Alerts, AlertSummary{
			Rule: key.rule, Severity: key.severity, Count: count,
		})
	}
	sort.Slice(envelope.Alerts, func(i, j int) bool {
		if envelope.Alerts[i].Rule == envelope.Alerts[j].Rule {
			return envelope.Alerts[i].Severity < envelope.Alerts[j].Severity
		}
		return envelope.Alerts[i].Rule < envelope.Alerts[j].Rule
	})
	if err := envelope.Validate(); err != nil {
		return WindowEnvelope{}, err
	}
	return envelope, nil
}

func summarizeMetric(unit Unit, values []float64) MetricSummary {
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	sum := 0.0
	minimum, maximum := values[0], values[0]
	for _, value := range values {
		sum += value
		if value < minimum {
			minimum = value
		}
		if value > maximum {
			maximum = value
		}
	}
	// Nearest-rank percentile: rank=ceil(P*N), using a one-based rank.
	rank := int(math.Ceil(0.95 * float64(len(sorted))))
	if rank < 1 {
		rank = 1
	}
	return MetricSummary{
		Unit: unit, Count: len(values), Min: minimum,
		Avg: sum / float64(len(values)), P95: sorted[rank-1],
		Max: maximum, Last: values[len(values)-1],
	}
}

func summarizeAvailability(a *availabilityAccumulator, samples int) Availability {
	availability := Availability{
		ObservedSamples: a.observed,
		MissingSamples:  samples - a.observed,
	}
	switch {
	case a.observed == samples:
		availability.State = AvailabilityObserved
	case a.observed > 0:
		availability.State = AvailabilityPartial
	case a.unsupported == samples:
		availability.State = AvailabilityUnsupported
	default:
		availability.State = AvailabilityUnavailable
	}
	return availability
}

func reading(info collector.SystemInfo, id MetricID) (float64, collector.MetricState) {
	if info.Capture.State != "" && info.Capture.State != collector.MetricObserved {
		return 0, info.Capture.State
	}
	switch id {
	case MetricCPUUsage:
		return info.CPU.UsagePercent, stateOf(info.CPU.MetricStates, "usage")
	case MetricMemoryUsed:
		return float64(info.Memory.UsedBytes), stateOf(info.Memory.MetricStates, "virtual")
	case MetricMemoryAvailable:
		return float64(info.Memory.AvailableBytes), stateOf(info.Memory.MetricStates, "virtual")
	case MetricMemoryUsage:
		return info.Memory.UsagePercent, stateOf(info.Memory.MetricStates, "virtual")
	case MetricMemoryPressure:
		return info.Memory.MemoryPressure, stateOf(info.Memory.MetricStates, "virtual")
	case MetricSwapUsed:
		return float64(info.Memory.SwapUsed), stateOf(info.Memory.MetricStates, "swap")
	case MetricNetworkReceive:
		return float64(info.Network.BytesRecvPerSec), stateOf(info.Network.MetricStates, "rate")
	case MetricNetworkTransmit:
		return float64(info.Network.BytesSentPerSec), stateOf(info.Network.MetricStates, "rate")
	case MetricDiskRead:
		return float64(info.Disk.ReadPerSec), stateOf(info.Disk.MetricStates, "rate")
	case MetricDiskWrite:
		return float64(info.Disk.WritePerSec), stateOf(info.Disk.MetricStates, "rate")
	case MetricLoadOneMinute:
		return info.CPU.LoadAvg1, stateOf(info.CPU.MetricStates, "load_average")
	default:
		return 0, collector.MetricUnsupported
	}
}

func stateOf(states map[string]collector.MetricStatus, key string) collector.MetricState {
	status, exists := states[key]
	if !exists || status.State == "" {
		return collector.MetricObserved
	}
	return status.State
}

func validMetricValue(unit Unit, value float64) bool {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return false
	}
	if unit == UnitPercent && value > 100 {
		return false
	}
	return true
}
