// Package analyzer detects anomalies in collector.Event streams. Rules are
// pluggable; the Engine applies them on every Observe.
package analyzer

import (
	"fmt"
	"math"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// DiskFillRule fires when any mounted partition's usage meets/exceeds a
// percentage threshold (default 90%).
type DiskFillRule struct {
	MinUsagePercent float64
}

// Name returns the rule name.
func (r *DiskFillRule) Name() string { return "disk_fill" }

// Evaluate emits a disk_fill alert per partition over the threshold.
func (r *DiskFillRule) Evaluate(ev collector.Event, _ *History) []collector.Alert {
	threshold := r.MinUsagePercent
	if threshold <= 0 {
		threshold = 90
	}
	var out []collector.Alert
	for _, p := range ev.Disk.Partitions {
		if p.UsagePercent >= threshold {
			out = append(out, collector.Alert{
				Severity: "warning",
				Rule:     "disk_fill",
				Detail:   fmt.Sprintf("%s at %.0f%% (>= %.0f%%)", p.MountPoint, p.UsagePercent, threshold),
			})
		}
	}
	return out
}

// SwapPressureRule fires when swap usage meets/exceeds a fraction of swap total
// (default 50%), a sign of real memory pressure.
type SwapPressureRule struct {
	MinSwapPercent float64
}

// Name returns the rule name.
func (r *SwapPressureRule) Name() string { return "swap_pressure" }

// Evaluate emits a swap_pressure alert when swap usage crosses the threshold.
func (r *SwapPressureRule) Evaluate(ev collector.Event, _ *History) []collector.Alert {
	threshold := r.MinSwapPercent
	if threshold <= 0 {
		threshold = 50
	}
	if ev.Memory.SwapTotal == 0 {
		return nil
	}
	pct := float64(ev.Memory.SwapUsed) / float64(ev.Memory.SwapTotal) * 100
	if pct >= threshold {
		return []collector.Alert{{
			Severity: "warning",
			Rule:     "swap_pressure",
			Detail:   fmt.Sprintf("swap %.0f%% used (>= %.0f%%)", pct, threshold),
		}}
	}
	return nil
}

// ThresholdRule fires when overall CPU or memory usage meets/exceeds a
// configured percentage. A threshold of 0 disables that check. This is the
// rule that gives the config.json cpu/memory alert thresholds teeth.
type ThresholdRule struct {
	CPUPercent float64
	MemPercent float64
}

// ZombieRule reports processes that have exited but are still waiting for
// their parent to reap them. Status collection is availability-aware, so an
// unsupported platform simply produces no zombie findings.
type ZombieRule struct{}

// Name returns the rule name.
func (*ZombieRule) Name() string { return "zombie_process" }

// Evaluate emits one warning per zombie process.
func (*ZombieRule) Evaluate(ev collector.Event, _ *History) []collector.Alert {
	var out []collector.Alert
	for _, p := range ev.Processes {
		zombie := false
		for _, state := range strings.Split(p.Status, ",") {
			state = strings.TrimSpace(state)
			if strings.EqualFold(state, "z") || strings.EqualFold(state, "zombie") {
				zombie = true
				break
			}
		}
		if zombie {
			out = append(out, collector.Alert{
				Severity: "warning",
				Rule:     "zombie_process",
				PID:      p.PID,
				Process:  p.Name,
				Detail:   fmt.Sprintf("%s (pid %d) is a zombie awaiting parent %d", p.Name, p.PID, p.Parent),
			})
		}
	}
	return out
}

// Name returns the rule name.
func (r *ThresholdRule) Name() string { return "threshold" }

// Evaluate emits a cpu_threshold / mem_threshold alert when usage crosses the
// configured percentage.
func (r *ThresholdRule) Evaluate(ev collector.Event, _ *History) []collector.Alert {
	var out []collector.Alert
	if r.CPUPercent > 0 && ev.CPU.UsagePercent >= r.CPUPercent {
		out = append(out, collector.Alert{
			Severity: "warning",
			Rule:     "cpu_threshold",
			Detail:   fmt.Sprintf("CPU %.0f%% >= threshold %.0f%%", ev.CPU.UsagePercent, r.CPUPercent),
		})
	}
	if r.MemPercent > 0 && ev.Memory.UsagePercent >= r.MemPercent {
		out = append(out, collector.Alert{
			Severity: "warning",
			Rule:     "mem_threshold",
			Detail:   fmt.Sprintf("memory %.0f%% >= threshold %.0f%%", ev.Memory.UsagePercent, r.MemPercent),
		})
	}
	return out
}

// Rule is the interface every check implements.
type Rule interface {
	Name() string
	Evaluate(ev collector.Event, hist *History) []collector.Alert
}

// Engine holds active rules and history.
type Engine struct {
	mu    sync.RWMutex
	rules []Rule
	hist  *History

	// onAlert, if set, is invoked once per alert Observe emits. The
	// hook fires synchronously inside Observe; callers that need async
	// capture (e.g. fcheap stash) should spawn a goroutine themselves.
	// nil disables the hook. The hook is the integration point with
	// internal/incidents: the CLI installs a hook that turns the alert
	// into a Capture call.
	onAlert func(ev collector.Event, a collector.Alert)
}

// SetOnAlert installs (or clears, when nil) the alert hook.
func (e *Engine) SetOnAlert(fn func(ev collector.Event, a collector.Alert)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.onAlert = fn
}

// NewEngine returns an engine with no rules configured.
func NewEngine() *Engine {
	return &Engine{hist: NewHistory(120)}
}

// AddRule registers r. Safe to call after construction.
func (e *Engine) AddRule(r Rule) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.rules = append(e.rules, r)
}

// Observe runs all rules, returns their alerts, and invokes the
// onAlert hook (if set) once per alert.
func (e *Engine) Observe(ev collector.Event) []collector.Alert {
	e.hist.Push(ev)
	e.mu.RLock()
	hook := e.onAlert
	rules := append([]Rule(nil), e.rules...)
	e.mu.RUnlock()

	var out []collector.Alert
	for _, r := range rules {
		out = append(out, r.Evaluate(ev, e.hist)...)
	}

	// Interpretation layer (sprint 4.1): attach a cross-signal Diagnosis to
	// per-process alerts so downstream consumers (watch NDJSON, webhooks,
	// incidents) carry the "why", not just the "what". Runs before the
	// onAlert hook so stash/webhook deliveries see the enriched alert.
	// Additive: Diagnosis stays nil when the window is too short or no
	// pattern matches; system-wide alerts (PID 0) are never enriched.
	for i := range out {
		if out[i].PID == 0 {
			continue
		}
		if d, ok := e.DiagnosePID(out[i].PID); ok {
			out[i].Diagnosis = &d
		}
	}

	if hook != nil {
		for _, a := range out {
			hook(ev, a)
		}
	}
	return out
}

// History is a per-process sliding window of memory and CPU samples.
type History struct {
	mu      sync.RWMutex
	maxLen  int
	samples []sample
}

type sample struct {
	ts        time.Time
	memByPID  map[int32]uint64
	cpuByPID  map[int32]float64
	nameByPID map[int32]string
}

// NewHistory creates a history with the given capacity.
func NewHistory(max int) *History {
	if max <= 0 {
		max = 60
	}
	return &History{maxLen: max}
}

// Push records one sample.
func (h *History) Push(ev collector.Event) {
	s := sample{ts: ev.Timestamp, memByPID: map[int32]uint64{}, cpuByPID: map[int32]float64{}, nameByPID: map[int32]string{}}
	for _, p := range ev.Processes {
		s.memByPID[p.PID] = p.Memory
		s.cpuByPID[p.PID] = p.CPUPercent
		s.nameByPID[p.PID] = p.Name
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	h.samples = append(h.samples, s)
	if len(h.samples) > h.maxLen {
		h.samples = h.samples[len(h.samples)-h.maxLen:]
	}
}

// RSSForPID returns the recent RSS samples for a PID.
func (h *History) RSSForPID(pid int32) []uint64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]uint64, 0, len(h.samples))
	for _, s := range h.samples {
		if v, ok := s.memByPID[pid]; ok {
			out = append(out, v)
		}
	}
	return out
}

// CPUForPID returns the recent CPU% samples for a PID. It is index-aligned
// with RSSForPID: Push records both maps from the same ProcessInfo entry, so
// a PID present in one sample's memByPID is present in its cpuByPID too.
func (h *History) CPUForPID(pid int32) []float64 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	out := make([]float64, 0, len(h.samples))
	for _, s := range h.samples {
		if v, ok := s.cpuByPID[pid]; ok {
			out = append(out, v)
		}
	}
	return out
}

// SeriesForPID returns the PID's timestamps, RSS and CPU samples in one
// aligned pass: index i of each slice comes from the same collector tick.
// Ticks where the PID was absent are skipped in all three slices.
func (h *History) SeriesForPID(pid int32) (ts []time.Time, rss []uint64, cpu []float64) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for _, s := range h.samples {
		m, ok := s.memByPID[pid]
		if !ok {
			continue
		}
		ts = append(ts, s.ts)
		rss = append(rss, m)
		cpu = append(cpu, s.cpuByPID[pid])
	}
	return ts, rss, cpu
}

// PIDs returns the PIDs present in the MOST RECENT sample, sorted ascending
// for deterministic Diagnose output. Exited processes are not diagnosed.
func (h *History) PIDs() []int32 {
	h.mu.RLock()
	defer h.mu.RUnlock()
	if len(h.samples) == 0 {
		return nil
	}
	last := h.samples[len(h.samples)-1]
	out := make([]int32, 0, len(last.memByPID))
	for pid := range last.memByPID {
		out = append(out, pid)
	}
	slices.Sort(out)
	return out
}

// Len returns the number of samples currently held.
func (h *History) Len() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.samples)
}

// nameForPID returns the most recent process name recorded for pid, or ""
// when the PID has never been seen.
func (h *History) nameForPID(pid int32) string {
	h.mu.RLock()
	defer h.mu.RUnlock()
	for i := len(h.samples) - 1; i >= 0; i-- {
		if n, ok := h.samples[i].nameByPID[pid]; ok && n != "" {
			return n
		}
	}
	return ""
}

// CPUSpikeRule flags processes whose CPU% is N times their baseline.
type CPUSpikeRule struct {
	Factor             float64
	MinCPUPercent      float64
	MinBaselineSamples int
}

// Name returns the rule name.
func (r *CPUSpikeRule) Name() string { return "cpu_spike" }

// Evaluate compares the current CPU sample with a median of prior samples for
// the same PID. A minimum absolute CPU floor prevents idle jitter from being
// labeled as a spike.
func (r *CPUSpikeRule) Evaluate(ev collector.Event, h *History) []collector.Alert {
	factor := r.Factor // local default — don't mutate shared rule state under concurrent Observe
	if factor <= 0 {
		factor = 3.0
	}
	minCPU := r.MinCPUPercent
	if minCPU <= 0 {
		minCPU = 50
	}
	minSamples := r.MinBaselineSamples
	if minSamples <= 0 {
		minSamples = 3
	}
	if h == nil {
		return nil
	}
	var out []collector.Alert
	for _, p := range ev.Processes {
		samples := h.CPUForPID(p.PID)
		if len(samples) < minSamples+1 {
			continue
		}
		baselineSamples := append([]float64(nil), samples[:len(samples)-1]...)
		slices.Sort(baselineSamples)
		baseline := medianFloat64(baselineSamples)
		threshold := math.Max(minCPU, baseline*factor)
		if p.CPUPercent >= threshold {
			out = append(out, collector.Alert{
				Severity: "warning",
				Rule:     r.Name(),
				PID:      p.PID,
				Process:  p.Name,
				Detail: fmt.Sprintf("cpu spike: %.1f%% vs %.1f%% rolling baseline (threshold %.1f%%, %.1fx)",
					p.CPUPercent, baseline, threshold, factor),
			})
		}
	}
	return out
}

func medianFloat64(values []float64) float64 {
	if len(values) == 0 {
		return 0
	}
	mid := len(values) / 2
	if len(values)%2 == 0 {
		return (values[mid-1] + values[mid]) / 2
	}
	return values[mid]
}

// RSSGrowthRule detects sustained RSS growth across the history window.
type RSSGrowthRule struct {
	// MinBytesPerSecond is the preferred wall-clock-normalized threshold.
	MinBytesPerSecond uint64
	// MinBytesPerSample is retained for source compatibility. When non-zero it
	// selects the legacy sample-index threshold instead.
	MinBytesPerSample uint64
}

// Name returns the rule name.
func (r *RSSGrowthRule) Name() string { return "rss_growth" }

// Evaluate returns alerts for processes with sustained RSS growth.
func (r *RSSGrowthRule) Evaluate(ev collector.Event, h *History) []collector.Alert {
	minRate := r.MinBytesPerSecond
	if minRate == 0 {
		minRate = 50_000
	}
	var out []collector.Alert
	for _, p := range ev.Processes {
		timestamps, samples, _ := h.SeriesForPID(p.PID)
		if len(samples) < 3 {
			continue
		}
		slope, r2 := linearRegression(samples)
		growth := slope
		unit := "sample"
		threshold := float64(r.MinBytesPerSample)
		if r.MinBytesPerSample == 0 {
			elapsed := timestamps[len(timestamps)-1].Sub(timestamps[0]).Seconds()
			if elapsed <= 0 {
				continue
			}
			growth = slope * float64(len(samples)-1) / elapsed
			threshold = float64(minRate)
			unit = "second"
		}
		if growth > threshold && r2 > 0.7 {
			out = append(out, collector.Alert{
				Severity: "warning",
				Rule:     r.Name(),
				PID:      p.PID,
				Process:  p.Name,
				Detail: fmt.Sprintf("suspected memory leak: RSS +%s/%s over %d samples (R²=%.2f)",
					collector.FormatBytes(uint64(growth)), unit, len(samples), r2),
			})
		}
	}
	return out
}

// linearRegression returns slope (per sample — dy per unit index) and R²
// for y over x=0..n-1. Thin adapter over linearRegressionF for RSS series.
func linearRegression(y []uint64) (slope, r2 float64) {
	f := make([]float64, len(y))
	for i, v := range y {
		f[i] = float64(v)
	}
	return linearRegressionF(f)
}

// linearRegressionF is the float64 core; CPU% series use it directly.
// A constant series returns r2 == 0 (ssTot == 0 guard), as before.
func linearRegressionF(y []float64) (slope, r2 float64) {
	n := float64(len(y))
	if n < 2 {
		return 0, 0
	}
	var sx, sy, sxx, sxy, syy float64
	for i, v := range y {
		xi := float64(i)
		yi := v
		sx += xi
		sy += yi
		sxx += xi * xi
		sxy += xi * yi
		syy += yi * yi
	}
	denom := n*sxx - sx*sx
	if denom == 0 {
		return 0, 0
	}
	slope = (n*sxy - sx*sy) / denom
	mean := sy / n
	ssTot := syy - n*mean*mean
	ssRes := syy - slope*sxy - (sy-slope*sx)*mean
	if ssTot == 0 {
		return slope, 0
	}
	r2 = 1 - ssRes/ssTot
	if math.IsNaN(r2) {
		r2 = 0
	}
	return slope, r2
}
