package baseline

import (
	"fmt"
	"math"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// Metric names carried on a Verdict so JSON consumers can dispatch without
// parsing the summary string.
const (
	MetricTotalRSS  = "total_rss"
	MetricMemory    = "memory"
	MetricSwap      = "swap"
	MetricCPU       = "cpu"
	MetricLoad1     = "load1"
	MetricDisk      = "disk"
	MetricProcCount = "proc_count"
	MetricProcRSS   = "proc_rss"
)

// Verdict is a Diagnosis tied to the metric whose delta triggered it.
//
// NOTE: the plan for this sprint item assumed a new analyzer.Diagnosis type
// would need to be created. The tree already has that interpretation shape —
// it lives in internal/collector as collector.Diagnosis (Summary/Evidence/
// Confidence/NextActions), because internal/analyzer builds Diagnosis values
// but attaches them to collector.Alert, and putting the type in collector
// avoids every Alert consumer (notify, incidents, MCP) needing to import
// analyzer. baseline sits downstream of both collector and analyzer with no
// cycle risk, so this embeds collector.Diagnosis directly instead of adding
// a second, parallel Diagnosis type. Confidence is the plain string type
// collector.Diagnosis already uses ("low"/"medium"/"high"); the analyzer
// package's ConfidenceLow/Medium/High constants are reused below rather than
// redeclared.
type Verdict struct {
	Metric string `json:"metric"`
	PID    int32  `json:"pid,omitempty"` // set only for proc_rss verdicts
	collector.Diagnosis
}

// Thresholds defines what counts as a "significant" delta. Every metric
// gates on an absolute floor AND (where a relative base exists) a relative
// fraction, so tiny bases don't scream: a 10MB process doubling is noise,
// a 1GB process doubling is a verdict.
type Thresholds struct {
	CPUPoints       float64 // min |Δ| CPU usage, percentage points
	MemPoints       float64 // min |Δ| memory usage, percentage points
	Load1Abs        float64 // min |Δ| load1
	Load1Frac       float64 // AND min |Δ| as fraction of old load1 (skipped when old < 0.1)
	SwapBytes       uint64  // min |Δ| swap-used bytes
	SwapFrac        float64 // AND min |Δ| as fraction of old swap used (skipped when old == 0)
	DiskBytes       uint64  // min |Δ| root-partition used bytes
	DiskFrac        float64 // AND min |Δ| as fraction of current disk total
	TotalRSSBytes   uint64  // min |Δ| summed process RSS bytes
	TotalRSSFrac    float64 // AND min |Δ| as fraction of old total RSS
	ProcRSSBytes    uint64  // per-process min |Δ| RSS bytes
	ProcRSSFrac     float64 // AND min |Δ| as fraction of that process's old RSS
	ProcCountAbs    int     // min |Δ| process count
	ProcCountFrac   float64 // AND min |Δ| as fraction of old count
	MaxProcVerdicts int     // cap on per-process verdicts (biggest movers first)
}

// DefaultThresholds returns the tuned defaults used by `monitor diff`.
func DefaultThresholds() Thresholds {
	return Thresholds{
		CPUPoints:       20,
		MemPoints:       10,
		Load1Abs:        2.0,
		Load1Frac:       0.5,
		SwapBytes:       256 << 20, // 256 MiB
		SwapFrac:        0.5,
		DiskBytes:       2 << 30, // 2 GiB
		DiskFrac:        0.05,
		TotalRSSBytes:   512 << 20, // 512 MiB
		TotalRSSFrac:    0.10,
		ProcRSSBytes:    256 << 20, // 256 MiB
		ProcRSSFrac:     0.50,
		ProcCountAbs:    25,
		ProcCountFrac:   0.20,
		MaxProcVerdicts: 3,
	}
}

// sigDelta reports whether delta is significant given an absolute floor and
// an optional relative gate (relBase <= 0 disables the relative gate), and
// returns |delta| / effectiveThreshold where effectiveThreshold =
// max(absFloor, relFrac*relBase). ratio is 0 when not significant.
func sigDelta(delta, absFloor, relBase, relFrac float64) (sig bool, ratio float64) {
	abs := math.Abs(delta)
	relThresh := relFrac * relBase
	sig = abs >= absFloor && (relBase <= 0 || abs >= relThresh)
	if !sig {
		return false, 0
	}
	eff := absFloor
	if relThresh > eff {
		eff = relThresh
	}
	if eff <= 0 {
		return true, 0
	}
	return true, abs / eff
}

// confidenceFor maps a significance ratio to a Confidence. capMedium is set
// for point-in-time noisy metrics (cpu, load1) that never earn "high" from a
// two-sample comparison. ConfidenceLow is never returned here — below
// threshold means sigDelta already said "no verdict at all".
func confidenceFor(ratio float64, capMedium bool) string {
	if ratio >= 2 && !capMedium {
		return analyzer.ConfidenceHigh
	}
	return analyzer.ConfidenceMedium
}

// verdictDirection reports the summary suffix and whether delta is a
// degradation. delta > 0 is worse for every metric here (even proc_count,
// where a mass drop still reads "improved" — the evidence numbers let a
// human judge).
func verdictDirection(delta float64) (suffix string, degrade bool) {
	if delta > 0 {
		return " — investigate", true
	}
	return " — improved", false
}

// signedBytesDelta renders a signed byte delta ("+12.0 MB" / "-4.0 MB").
// Duplicate of cli.signedBytes; kept local so baseline has no cli dependency.
func signedBytesDelta(delta int64) string {
	if delta >= 0 {
		return "+" + collector.FormatBytes(uint64(delta))
	}
	return "-" + collector.FormatBytes(uint64(-delta))
}

// signedPct renders (newV-oldV)/oldV as a signed percentage. Caller
// guarantees oldV > 0.
func signedPct(oldV, newV float64) string {
	return fmt.Sprintf("%+.0f%%", (newV-oldV)/oldV*100)
}

// totalRSS sums the memory of every process recorded in a baseline.
func totalRSS(b *Baseline) uint64 {
	var total uint64
	for _, p := range b.Processes {
		total += p.Memory
	}
	return total
}

// ComputeVerdicts interprets the significant deltas between from and to.
// Deterministic output order: total_rss, memory, swap, cpu, load1, disk,
// proc_count, then proc_rss (already sorted biggest-mover-first in
// d.ChangedProcs, capped at th.MaxProcVerdicts).
func ComputeVerdicts(from, to *Baseline, d Diff, th Thresholds) []Verdict {
	var out []Verdict

	// 1. total_rss
	if oldT, newT := totalRSS(from), totalRSS(to); oldT > 0 && newT > 0 {
		delta := float64(newT) - float64(oldT)
		if sig, ratio := sigDelta(delta, float64(th.TotalRSSBytes), float64(oldT), th.TotalRSSFrac); sig {
			suffix, degrade := verdictDirection(delta)
			v := Verdict{
				Metric: MetricTotalRSS,
				Diagnosis: collector.Diagnosis{
					Summary: fmt.Sprintf("total RSS %s vs baseline (was %s, now %s)%s",
						signedPct(float64(oldT), float64(newT)), collector.FormatBytes(oldT), collector.FormatBytes(newT), suffix),
					Evidence: []string{
						fmt.Sprintf("was %s, now %s (Δ %s)", collector.FormatBytes(oldT), collector.FormatBytes(newT), signedBytesDelta(int64(newT)-int64(oldT))),
						fmt.Sprintf("significant: |Δ| >= %s and >= %.0f%% of baseline", collector.FormatBytes(th.TotalRSSBytes), th.TotalRSSFrac*100),
					},
					Confidence: confidenceFor(ratio, false),
				},
			}
			if degrade {
				v.NextActions = []string{
					"inspect changed_procs in this diff for the grower",
					"monitor investigate <pid>",
				}
			}
			out = append(out, v)
		}
	}

	// 2. memory
	if sig, ratio := sigDelta(d.MemDelta, th.MemPoints, 0, 0); sig {
		suffix, degrade := verdictDirection(d.MemDelta)
		v := Verdict{
			Metric: MetricMemory,
			Diagnosis: collector.Diagnosis{
				Summary: fmt.Sprintf("memory %.1f%% vs baseline %.1f%% (%+.1f pts)%s", to.MemUsage, from.MemUsage, d.MemDelta, suffix),
				Evidence: []string{
					fmt.Sprintf("was %.1f%%, now %.1f%% (Δ %+.1f pts)", from.MemUsage, to.MemUsage, d.MemDelta),
					fmt.Sprintf("significant: |Δ| >= %.1f pts", th.MemPoints),
				},
				Confidence: confidenceFor(ratio, false),
			},
		}
		if degrade {
			v.NextActions = []string{"monitor snapshot (check top consumers)", "monitor history query memory"}
		}
		out = append(out, v)
	}

	// 3. swap — schema sentinel: pre-verdict baselines unmarshal DiskTotal as
	// 0, and a real capture never has a zero disk total, so old baselines
	// silently skip swap/disk rather than reporting a false "+∞".
	if from.DiskTotal > 0 && to.DiskTotal > 0 {
		delta := float64(to.SwapUsed) - float64(from.SwapUsed)
		if sig, ratio := sigDelta(delta, float64(th.SwapBytes), float64(from.SwapUsed), th.SwapFrac); sig {
			suffix, degrade := verdictDirection(delta)
			v := Verdict{
				Metric: MetricSwap,
				Diagnosis: collector.Diagnosis{
					Summary: fmt.Sprintf("swap %s vs baseline (was %s, now %s)%s",
						signedBytesDelta(int64(to.SwapUsed)-int64(from.SwapUsed)), collector.FormatBytes(from.SwapUsed), collector.FormatBytes(to.SwapUsed), suffix),
					Evidence: []string{
						fmt.Sprintf("was %s, now %s (Δ %s)", collector.FormatBytes(from.SwapUsed), collector.FormatBytes(to.SwapUsed), signedBytesDelta(int64(to.SwapUsed)-int64(from.SwapUsed))),
						fmt.Sprintf("significant: |Δ| >= %s and >= %.0f%% of baseline swap", collector.FormatBytes(th.SwapBytes), th.SwapFrac*100),
					},
					Confidence: confidenceFor(ratio, false),
				},
			}
			if degrade {
				v.NextActions = []string{"monitor snapshot (check memory pressure)", "monitor profile <pid> --type heap on the top RSS grower"}
			}
			out = append(out, v)
		}
	}

	// 4. cpu — single-instant sample, so confidence caps at medium.
	if sig, ratio := sigDelta(d.CPUDelta, th.CPUPoints, 0, 0); sig {
		suffix, degrade := verdictDirection(d.CPUDelta)
		v := Verdict{
			Metric: MetricCPU,
			Diagnosis: collector.Diagnosis{
				Summary: fmt.Sprintf("CPU %.1f%% vs baseline %.1f%% (%+.1f pts)%s", to.CPUUsage, from.CPUUsage, d.CPUDelta, suffix),
				Evidence: []string{
					fmt.Sprintf("was %.1f%%, now %.1f%% (Δ %+.1f pts)", from.CPUUsage, to.CPUUsage, d.CPUDelta),
					"point-in-time sample; confirm with monitor watch before acting",
					fmt.Sprintf("significant: |Δ| >= %.1f pts", th.CPUPoints),
				},
				Confidence: confidenceFor(ratio, true),
			},
		}
		if degrade {
			v.NextActions = []string{"monitor watch (confirm the spike is sustained)", "monitor tree (find the busy process group)"}
		}
		out = append(out, v)
	}

	// 5. load1 — also point-in-time, confidence caps at medium.
	relBase := from.Load1
	if relBase < 0.1 {
		relBase = 0
	}
	if sig, ratio := sigDelta(d.Load1Delta, th.Load1Abs, relBase, th.Load1Frac); sig {
		suffix, degrade := verdictDirection(d.Load1Delta)
		v := Verdict{
			Metric: MetricLoad1,
			Diagnosis: collector.Diagnosis{
				Summary: fmt.Sprintf("load1 %.2f vs baseline %.2f (%+.2f)%s", to.Load1, from.Load1, d.Load1Delta, suffix),
				Evidence: []string{
					fmt.Sprintf("was %.2f, now %.2f (Δ %+.2f)", from.Load1, to.Load1, d.Load1Delta),
					"point-in-time sample; confirm with monitor watch before acting",
					fmt.Sprintf("significant: |Δ| >= %.1f and >= %.0f%% of baseline load1", th.Load1Abs, th.Load1Frac*100),
				},
				Confidence: confidenceFor(ratio, true),
			},
		}
		if degrade {
			v.NextActions = []string{"monitor watch (confirm the spike is sustained)", "monitor tree (find the busy process group)"}
		}
		out = append(out, v)
	}

	// 6. disk — same schema sentinel as swap.
	if from.DiskTotal > 0 && to.DiskTotal > 0 {
		delta := float64(to.DiskUsed) - float64(from.DiskUsed)
		if sig, ratio := sigDelta(delta, float64(th.DiskBytes), float64(to.DiskTotal), th.DiskFrac); sig {
			suffix, degrade := verdictDirection(delta)
			v := Verdict{
				Metric: MetricDisk,
				Diagnosis: collector.Diagnosis{
					Summary: fmt.Sprintf("disk used %s vs baseline (was %s, now %s of %s)%s",
						signedBytesDelta(int64(to.DiskUsed)-int64(from.DiskUsed)), collector.FormatBytes(from.DiskUsed), collector.FormatBytes(to.DiskUsed), collector.FormatBytes(to.DiskTotal), suffix),
					Evidence: []string{
						fmt.Sprintf("was %s, now %s of %s (Δ %s)", collector.FormatBytes(from.DiskUsed), collector.FormatBytes(to.DiskUsed), collector.FormatBytes(to.DiskTotal), signedBytesDelta(int64(to.DiskUsed)-int64(from.DiskUsed))),
						fmt.Sprintf("significant: |Δ| >= %s and >= %.0f%% of disk total", collector.FormatBytes(th.DiskBytes), th.DiskFrac*100),
					},
					Confidence: confidenceFor(ratio, false),
				},
			}
			if degrade {
				v.NextActions = []string{"monitor watch (disk_fill rule will flag partitions >= 90%)", "du -sh over recently written paths"}
			}
			out = append(out, v)
		}
	}

	// 7. proc_count
	if oldN, newN := len(from.Processes), len(to.Processes); oldN > 0 && newN > 0 {
		delta := float64(newN - oldN)
		if sig, ratio := sigDelta(delta, float64(th.ProcCountAbs), float64(oldN), th.ProcCountFrac); sig {
			suffix, degrade := verdictDirection(delta)
			v := Verdict{
				Metric: MetricProcCount,
				Diagnosis: collector.Diagnosis{
					Summary: fmt.Sprintf("process count %d vs baseline %d (%+d)%s", newN, oldN, newN-oldN, suffix),
					Evidence: []string{
						fmt.Sprintf("was %d, now %d (Δ %+d)", oldN, newN, newN-oldN),
						fmt.Sprintf("significant: |Δ| >= %d and >= %.0f%% of baseline count", th.ProcCountAbs, th.ProcCountFrac*100),
					},
					Confidence: confidenceFor(ratio, false),
				},
			}
			if degrade {
				v.NextActions = []string{"monitor tree (look for fork storms / runaway spawners)"}
			}
			out = append(out, v)
		}
	}

	// 8. proc_rss — d.ChangedProcs is already sorted biggest-mover-first.
	emitted := 0
	for _, p := range d.ChangedProcs {
		if emitted >= th.MaxProcVerdicts {
			break
		}
		if p.OldMem == 0 {
			continue
		}
		delta := float64(p.MemDelta)
		sig, ratio := sigDelta(delta, float64(th.ProcRSSBytes), float64(p.OldMem), th.ProcRSSFrac)
		if !sig {
			continue
		}
		suffix, degrade := verdictDirection(delta)
		v := Verdict{
			Metric: MetricProcRSS,
			PID:    p.PID,
			Diagnosis: collector.Diagnosis{
				Summary: fmt.Sprintf("%s (pid %d) RSS %s vs baseline (was %s, now %s)%s",
					p.Name, p.PID, signedPct(float64(p.OldMem), float64(p.NewMem)), collector.FormatBytes(p.OldMem), collector.FormatBytes(p.NewMem), suffix),
				Evidence: []string{
					fmt.Sprintf("was %s, now %s (Δ %s)", collector.FormatBytes(p.OldMem), collector.FormatBytes(p.NewMem), signedBytesDelta(p.MemDelta)),
					fmt.Sprintf("significant: |Δ| >= %s and >= %.0f%% of this process's baseline RSS", collector.FormatBytes(th.ProcRSSBytes), th.ProcRSSFrac*100),
				},
				Confidence: confidenceFor(ratio, false),
			},
		}
		if degrade {
			v.NextActions = []string{
				fmt.Sprintf("monitor profile %d --type heap", p.PID),
				fmt.Sprintf("monitor investigate %d", p.PID),
			}
		}
		out = append(out, v)
		emitted++
	}

	return out
}
