// Diagnosis engine: turns the per-PID History window into interpreted
// collector.Diagnosis values via a deterministic cross-signal rule table.
// The table (diagnosisRules) is data: adding a pattern is adding a row.
package analyzer

import (
	"fmt"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// Confidence labels shared by every diagnosis producer (and, in later sprint
// items, snapshot summaries and baseline verdicts).
const (
	ConfidenceLow    = "low"
	ConfidenceMedium = "medium"
	ConfidenceHigh   = "high"
)

// Classification tunables. All windows are sample-indexed; per-minute rates
// are derived from real sample timestamps at render time.
const (
	diagMinSamples = 4 // fewer samples for a PID -> no diagnosis (quiet)

	rssSlopeMinBytes = 50_000 // bytes/sample; matches RSSGrowthRule's default
	rssFitMinR2      = 0.5    // minimum R² to call an RSS trend real

	cpuSlopeMinPP  = 0.5 // CPU percentage points per sample
	cpuFitMinR2    = 0.5
	cpuHighPercent = 60.0 // mean CPU% considered "high"

	sawtoothMinReversals      = 4          // direction changes to call a sawtooth
	sawtoothMaxR2             = 0.5        // a strong monotone fit disqualifies sawtooth
	sawtoothMinAmplitudeFrac  = 0.03       // a reversal leg must move >= 3% of mean RSS...
	sawtoothMinAmplitudeBytes = 512 * 1024 // ...and at least 512 KiB (noise floor)
)

// rssClass / cpuClass are the discrete trend classes the rule table matches on.
type rssClass int

const (
	rssFlat rssClass = iota
	rssRising
	rssFalling
	rssSawtooth
)

type cpuClass int

const (
	cpuFlat cpuClass = iota
	cpuRising
	cpuFalling
	cpuHigh // mean over the window >= cpuHighPercent without a rising trend
)

// signalState is everything the rule table needs to know about one PID's
// window. Built once per PID per DiagnosePID call.
type signalState struct {
	pid       int32
	name      string
	n         int           // aligned sample count
	elapsed   time.Duration // wall-clock span of the window
	rssFirst  uint64
	rssLast   uint64
	rssSlope  float64 // bytes per sample step
	rssR2     float64
	growthPct float64 // (last-first)/first*100; 0 when first == 0
	cpuMean   float64
	cpuSlope  float64 // percentage points per sample step
	cpuR2     float64
	reversals int
	rss       rssClass
	cpu       cpuClass
}

// buildState computes regressions, means and the reversal count for one
// PID's aligned series. len(rss) == len(cpu) == len(ts) >= diagMinSamples
// is the caller's responsibility.
func buildState(pid int32, name string, ts []time.Time, rss []uint64, cpu []float64) signalState {
	s := signalState{pid: pid, name: name, n: len(rss)}
	s.rssFirst, s.rssLast = rss[0], rss[len(rss)-1]
	s.rssSlope, s.rssR2 = linearRegression(rss)
	s.cpuSlope, s.cpuR2 = linearRegressionF(cpu)
	var cpuSum float64
	for _, v := range cpu {
		cpuSum += v
	}
	s.cpuMean = cpuSum / float64(len(cpu))
	if s.rssFirst > 0 {
		s.growthPct = (float64(s.rssLast) - float64(s.rssFirst)) / float64(s.rssFirst) * 100
	}
	s.elapsed = ts[len(ts)-1].Sub(ts[0])
	if s.elapsed <= 0 {
		// Degenerate timestamps (all identical, or clock skew): assume the
		// collector's default 1s tick so per-minute rates stay finite.
		s.elapsed = time.Duration(s.n-1) * time.Second
	}
	var rssMean float64
	for _, v := range rss {
		rssMean += float64(v)
	}
	rssMean /= float64(len(rss))
	minAmp := rssMean * sawtoothMinAmplitudeFrac
	if minAmp < sawtoothMinAmplitudeBytes {
		minAmp = sawtoothMinAmplitudeBytes
	}
	s.reversals = countReversals(rss, minAmp)
	s.rss = classifyRSS(s)
	s.cpu = classifyCPU(s)
	return s
}

// countReversals counts direction changes in y where each leg moved at least
// minAmp away from the previous extremum. Jitter below minAmp never flips
// the direction, so a noisy monotone ramp counts 0 reversals.
func countReversals(y []uint64, minAmp float64) int {
	if len(y) < 3 {
		return 0
	}
	dir := 0 // 0 unknown, +1 rising, -1 falling
	ext := float64(y[0])
	reversals := 0
	for _, v := range y[1:] {
		f := float64(v)
		switch {
		case f >= ext+minAmp:
			if dir == -1 {
				reversals++
			}
			dir = 1
			ext = f
		case f <= ext-minAmp:
			if dir == 1 {
				reversals++
			}
			dir = -1
			ext = f
		default:
			// Within the noise band: keep tracking the running extremum in
			// the current direction so a slow leg still accumulates.
			if dir == 1 && f > ext {
				ext = f
			} else if dir == -1 && f < ext {
				ext = f
			}
		}
	}
	return reversals
}

// classifyRSS orders sawtooth above trend: sawtooth requires a WEAK linear
// fit (r2 < sawtoothMaxR2) while rising/falling require a strong one
// (r2 >= rssFitMinR2), so with the default thresholds the two are mutually
// exclusive and a noisy leak is never misread as GC churn.
func classifyRSS(s signalState) rssClass {
	if s.reversals >= sawtoothMinReversals && s.rssR2 < sawtoothMaxR2 {
		return rssSawtooth
	}
	if s.rssSlope > rssSlopeMinBytes && s.rssR2 >= rssFitMinR2 {
		return rssRising
	}
	if s.rssSlope < -rssSlopeMinBytes && s.rssR2 >= rssFitMinR2 {
		return rssFalling
	}
	return rssFlat
}

func classifyCPU(s signalState) cpuClass {
	if s.cpuSlope > cpuSlopeMinPP && s.cpuR2 >= cpuFitMinR2 {
		return cpuRising
	}
	if s.cpuMean >= cpuHighPercent {
		return cpuHigh
	}
	if s.cpuSlope < -cpuSlopeMinPP && s.cpuR2 >= cpuFitMinR2 {
		return cpuFalling
	}
	return cpuFlat
}

// rssSlopePerMin converts the per-sample RSS slope to bytes/minute using the
// window's real elapsed time (slope is per index step; there are n-1 steps).
func (s signalState) rssSlopePerMin() float64 {
	if s.n < 2 || s.elapsed <= 0 {
		return 0
	}
	return s.rssSlope * float64(s.n-1) / s.elapsed.Minutes()
}

// cpuSlopePerMin converts the per-sample CPU slope to percentage points/minute.
func (s signalState) cpuSlopePerMin() float64 {
	if s.n < 2 || s.elapsed <= 0 {
		return 0
	}
	return s.cpuSlope * float64(s.n-1) / s.elapsed.Minutes()
}

// subject renders "name (pid N)", or "pid N" when the name is unknown.
func (s signalState) subject() string {
	if s.name == "" {
		return fmt.Sprintf("pid %d", s.pid)
	}
	return fmt.Sprintf("%s (pid %d)", s.name, s.pid)
}

// evidence renders the numeric signals behind a diagnosis. Slope and R² are
// ALWAYS preserved here so downstream agents see the math, not just the verdict.
func (s signalState) evidence() []string {
	out := []string{
		fmt.Sprintf("RSS %s -> %s (%+.1f%%) over %s (slope %s/min, R²=%.2f, %d samples)",
			collector.FormatBytes(s.rssFirst), collector.FormatBytes(s.rssLast),
			s.growthPct, s.elapsed.Truncate(time.Second),
			formatSignedBytes(s.rssSlopePerMin()), s.rssR2, s.n),
		fmt.Sprintf("CPU mean %.1f%% (slope %+.2fpp/min, R²=%.2f)",
			s.cpuMean, s.cpuSlopePerMin(), s.cpuR2),
	}
	if s.reversals > 0 {
		out = append(out, fmt.Sprintf("RSS direction reversals: %d (sawtooth threshold %d)",
			s.reversals, sawtoothMinReversals))
	}
	return out
}

// formatSignedBytes renders a possibly negative byte rate with adaptive units.
func formatSignedBytes(v float64) string {
	if v < 0 {
		return "-" + collector.FormatBytes(uint64(-v))
	}
	return collector.FormatBytes(uint64(v))
}

// confidenceFromFit maps regression quality plus evidence volume to a label:
// a strong fit over a short window is still only "low".
func confidenceFromFit(r2 float64, n int) string {
	switch {
	case n >= 10 && r2 >= 0.9:
		return ConfidenceHigh
	case n >= 5 && r2 >= 0.7:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

// confidenceFromLevel is for flat-but-high CPU, where R² of a constant
// series is 0 by construction and fit quality says nothing.
func confidenceFromLevel(mean float64, n int) string {
	switch {
	case n >= 10 && mean >= 90:
		return ConfidenceHigh
	case n >= 5 && mean >= cpuHighPercent:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

// confidenceFromReversals is for sawtooth: more reversals over more samples
// is stronger evidence of a periodic pattern.
func confidenceFromReversals(reversals, n int) string {
	switch {
	case n >= 16 && reversals >= 8:
		return ConfidenceHigh
	case n >= 8 && reversals >= sawtoothMinReversals:
		return ConfidenceMedium
	default:
		return ConfidenceLow
	}
}

// diagRule is one row of the cross-signal correlation table. RSS/CPU are
// any-of sets; an empty set means "don't care". Rows are evaluated in order
// and the FIRST match wins, so put the most specific pattern first.
type diagRule struct {
	ID    string
	RSS   []rssClass
	CPU   []cpuClass
	Build func(s signalState) collector.Diagnosis
}

// diagnosisRules is the sprint-4.1 rule table:
//
//	RSS sawtooth               -> gc_pressure  (checked first: most specific)
//	RSS rising + CPU rising    -> load         (both climbing: demand, not leak)
//	RSS rising + CPU not rising-> memory_leak
//	RSS flat/falling + CPU high or rising -> cpu_spin
//
// Everything else is healthy/quiet: no diagnosis.
var diagnosisRules = []diagRule{
	{ID: "gc_pressure", RSS: []rssClass{rssSawtooth}, CPU: nil, Build: buildGCPressure},
	{ID: "load", RSS: []rssClass{rssRising}, CPU: []cpuClass{cpuRising}, Build: buildLoad},
	{ID: "memory_leak", RSS: []rssClass{rssRising}, CPU: []cpuClass{cpuFlat, cpuFalling, cpuHigh}, Build: buildMemoryLeak},
	{ID: "cpu_spin", RSS: []rssClass{rssFlat, rssFalling}, CPU: []cpuClass{cpuHigh, cpuRising}, Build: buildCPUSpin},
}

func matchRSS(set []rssClass, v rssClass) bool {
	if len(set) == 0 {
		return true
	}
	for _, c := range set {
		if c == v {
			return true
		}
	}
	return false
}

func matchCPU(set []cpuClass, v cpuClass) bool {
	if len(set) == 0 {
		return true
	}
	for _, c := range set {
		if c == v {
			return true
		}
	}
	return false
}

func buildMemoryLeak(s signalState) collector.Diagnosis {
	return collector.Diagnosis{
		Summary: fmt.Sprintf(
			"%s: RSS grew %+.0f%% over %s while CPU stayed flat — consistent with a memory leak (slope %s/min, R²=%.2f)",
			s.subject(), s.growthPct, s.elapsed.Truncate(time.Second),
			formatSignedBytes(s.rssSlopePerMin()), s.rssR2),
		Evidence:   s.evidence(),
		Confidence: confidenceFromFit(s.rssR2, s.n),
		NextActions: []string{
			fmt.Sprintf("monitor_profile_capture pid:%d type:heap confirm:true", s.pid),
			fmt.Sprintf("monitor_investigate pid:%d confirm:true", s.pid),
		},
	}
}

func buildCPUSpin(s signalState) collector.Diagnosis {
	verb := "held high"
	conf := confidenceFromLevel(s.cpuMean, s.n)
	if s.cpu == cpuRising {
		verb = "kept rising"
		conf = confidenceFromFit(s.cpuR2, s.n)
	}
	return collector.Diagnosis{
		Summary: fmt.Sprintf(
			"%s: CPU %s (mean %.0f%%) while RSS stayed flat — consistent with a spin/hot loop (slope %+.2fpp/min, R²=%.2f)",
			s.subject(), verb, s.cpuMean, s.cpuSlopePerMin(), s.cpuR2),
		Evidence:   s.evidence(),
		Confidence: conf,
		NextActions: []string{
			fmt.Sprintf("monitor_profile_capture pid:%d type:cpu confirm:true", s.pid),
			fmt.Sprintf("monitor_investigate pid:%d confirm:true", s.pid),
		},
	}
}

func buildLoad(s signalState) collector.Diagnosis {
	return collector.Diagnosis{
		Summary: fmt.Sprintf(
			"%s: RSS (%+.0f%%) and CPU (%+.2fpp/min) rising together — consistent with increased load rather than a leak (RSS R²=%.2f, CPU R²=%.2f)",
			s.subject(), s.growthPct, s.cpuSlopePerMin(), s.rssR2, s.cpuR2),
		Evidence:   s.evidence(),
		Confidence: confidenceFromFit(min(s.rssR2, s.cpuR2), s.n),
		NextActions: []string{
			"monitor_processes",
			"monitor_snapshot",
		},
	}
}

func buildGCPressure(s signalState) collector.Diagnosis {
	return collector.Diagnosis{
		Summary: fmt.Sprintf(
			"%s: RSS sawtooth (%d reversals over %s) with no sustained trend — consistent with GC pressure / allocation churn (R²=%.2f)",
			s.subject(), s.reversals, s.elapsed.Truncate(time.Second), s.rssR2),
		Evidence:   s.evidence(),
		Confidence: confidenceFromReversals(s.reversals, s.n),
		NextActions: []string{
			fmt.Sprintf("monitor_profile_capture pid:%d type:heap confirm:true", s.pid),
			fmt.Sprintf("monitor_investigate pid:%d confirm:true", s.pid),
		},
	}
}

// Diagnose runs the cross-signal rule table over every PID present in the
// most recent history sample and returns one Diagnosis per matched PID,
// sorted by PID. Read-only: safe to call concurrently with Observe. This is
// the entry point the (sprint 4.2) monitor_analyze MCP tool calls after
// Observing a window of samples:
//
//	engine := analyzer.NewEngine()
//	for i := 0; i < n; i++ { engine.Observe(nextEvent()) }
//	diags := engine.Diagnose()
func (e *Engine) Diagnose() []collector.Diagnosis {
	var out []collector.Diagnosis
	for _, pid := range e.hist.PIDs() {
		if d, ok := e.DiagnosePID(pid); ok {
			out = append(out, d)
		}
	}
	return out
}

// DiagnosePID runs the rule table for a single PID. ok is false when the
// PID has fewer than diagMinSamples aligned samples or no row matches
// (a healthy/quiet process).
func (e *Engine) DiagnosePID(pid int32) (collector.Diagnosis, bool) {
	ts, rss, cpu := e.hist.SeriesForPID(pid)
	if len(rss) < diagMinSamples {
		return collector.Diagnosis{}, false
	}
	s := buildState(pid, e.hist.nameForPID(pid), ts, rss, cpu)
	for _, r := range diagnosisRules {
		if matchRSS(r.RSS, s.rss) && matchCPU(r.CPU, s.cpu) {
			return r.Build(s), true
		}
	}
	return collector.Diagnosis{}, false
}
