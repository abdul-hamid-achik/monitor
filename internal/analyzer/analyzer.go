// Package analyzer detects anomalies in collector.Event streams. Rules are
// pluggable; the Engine applies them on every Observe.
package analyzer

import (
	"math"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

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
	if hook != nil {
		for _, a := range out {
			hook(ev, a)
		}
	}
	return out
}

// History is a per-process sliding window of memory and CPU samples.
type History struct {
	mu     sync.RWMutex
	maxLen int
	samples []sample
}

type sample struct {
	ts       time.Time
	memByPID map[int32]uint64
	cpuByPID map[int32]float64
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
	s := sample{ts: ev.Timestamp, memByPID: map[int32]uint64{}, cpuByPID: map[int32]float64{}}
	for _, p := range ev.Processes {
		s.memByPID[p.PID] = p.Memory
		s.cpuByPID[p.PID] = p.CPUPercent
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

// CPUSpikeRule flags processes whose CPU% is N times their baseline.
type CPUSpikeRule struct {
	Factor float64
}

// Name returns the rule name.
func (r *CPUSpikeRule) Name() string { return "cpu_spike" }

// Evaluate returns an alert per process whose CPU% exceeds factor*50 (placeholder baseline).
func (r *CPUSpikeRule) Evaluate(ev collector.Event, _ *History) []collector.Alert {
	if r.Factor <= 0 {
		r.Factor = 3.0
	}
	var out []collector.Alert
	for _, p := range ev.Processes {
		if p.CPUPercent > r.Factor*50 {
			out = append(out, collector.Alert{
				Severity: "warning",
				Rule:     r.Name(),
				PID:      p.PID,
				Process:  p.Name,
				Detail:   "cpu spike",
			})
		}
	}
	return out
}

// RSSGrowthRule detects monotonic RSS growth across the history window.
type RSSGrowthRule struct {
	MinBytesPerSec uint64
}

// Name returns the rule name.
func (r *RSSGrowthRule) Name() string { return "rss_growth" }

// Evaluate returns alerts for processes with sustained RSS growth.
func (r *RSSGrowthRule) Evaluate(ev collector.Event, h *History) []collector.Alert {
	if r.MinBytesPerSec == 0 {
		r.MinBytesPerSec = 50_000 // ~50KB/s
	}
	var out []collector.Alert
	for _, p := range ev.Processes {
		samples := h.RSSForPID(p.PID)
		if len(samples) < 3 {
			continue
		}
		slope, r2 := linearRegression(samples)
		if slope > float64(r.MinBytesPerSec) && r2 > 0.7 {
			out = append(out, collector.Alert{
				Severity: "warning",
				Rule:     r.Name(),
				PID:      p.PID,
				Process:  p.Name,
				Detail:   "suspected memory leak",
			})
		}
	}
	return out
}

// linearRegression returns slope and R² for y over x=0..n-1.
func linearRegression(y []uint64) (slope, r2 float64) {
	n := float64(len(y))
	if n < 2 {
		return 0, 0
	}
	var sx, sy, sxx, sxy, syy float64
	for i, v := range y {
		xi := float64(i)
		yi := float64(v)
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
	ssRes := syy - slope*sxy - (sy - slope*sx)*mean
	if ssTot == 0 {
		return slope, 0
	}
	r2 = 1 - ssRes/ssTot
	if math.IsNaN(r2) {
		r2 = 0
	}
	return slope, r2
}