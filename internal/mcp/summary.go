package mcp

import (
	"fmt"
	"strings"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// Thresholds for the snapshot summary. Deliberately near (not equal to) the
// analyzer rule thresholds: the summary warns when a rule is about to fire.
const (
	memHighPct  = 75.0
	memCritPct  = 90.0
	cpuHighPct  = 75.0
	cpuCritPct  = 90.0
	diskHighPct = 80.0 // "approaching" — DiskFillRule fires at 90
	diskCritPct = 90.0
	swapWarnPct = 50.0 // matches SwapPressureRule's default
)

// severityLabel classifies pct: "" below high, "high" in [high,crit),
// "critical" at/above crit.
func severityLabel(pct, high, crit float64) string {
	switch {
	case pct >= crit:
		return "critical"
	case pct >= high:
		return "high"
	default:
		return ""
	}
}

// worstPartition returns the partition with the highest usage, ok=false when
// there are none.
func worstPartition(parts []collector.DiskPartitionInfo) (collector.DiskPartitionInfo, bool) {
	if len(parts) == 0 {
		return collector.DiskPartitionInfo{}, false
	}
	worst := parts[0]
	for _, p := range parts[1:] {
		if p.UsagePercent > worst.UsagePercent {
			worst = p
		}
	}
	return worst, true
}

// topMemoryProcess returns the process with the largest RSS, ok=false when
// the process list is empty.
func topMemoryProcess(procs []collector.ProcessInfo) (collector.ProcessInfo, bool) {
	if len(procs) == 0 {
		return collector.ProcessInfo{}, false
	}
	top := procs[0]
	for _, p := range procs[1:] {
		if p.Memory > top.Memory {
			top = p
		}
	}
	return top, true
}

// buildSnapshotSummary turns a raw SystemInfo into a one-paragraph
// interpretation plus zero or more next-step suggestions. Pure function:
// same input, same output — unit-tested table-driven in summary_test.go.
//
// Example: "Memory 78% (high), CPU 12%, disk OK. Top consumer: chrome (4.2 GB)."
func buildSnapshotSummary(info collector.SystemInfo) (string, []string) {
	var next []string

	// Memory clause.
	memPct := info.Memory.UsagePercent
	memClause := fmt.Sprintf("Memory %.0f%%", memPct)
	if l := severityLabel(memPct, memHighPct, memCritPct); l != "" {
		memClause += " (" + l + ")"
	}
	switch severityLabel(memPct, memHighPct, memCritPct) {
	case "critical":
		next = append(next, "Memory is critical — call monitor_processes with sort_by:rss, then monitor_analyze to check for a leak.")
	case "high":
		next = append(next, "Memory is high — call monitor_processes with sort_by:rss to find the top consumers.")
	}

	// CPU clause.
	cpuPct := info.CPU.UsagePercent
	cpuClause := fmt.Sprintf("CPU %.0f%%", cpuPct)
	if l := severityLabel(cpuPct, cpuHighPct, cpuCritPct); l != "" {
		cpuClause += " (" + l + ")"
		next = append(next, "CPU is "+l+" — call monitor_processes with sort_by:cpu, then monitor_analyze (window_seconds:10) to identify the hot process.")
	}

	// Disk clause: report the worst partition.
	diskClause := "disk unknown"
	if worst, ok := worstPartition(info.Disk.Partitions); ok {
		if l := severityLabel(worst.UsagePercent, diskHighPct, diskCritPct); l != "" {
			diskClause = fmt.Sprintf("disk %s %.0f%% (%s)", worst.MountPoint, worst.UsagePercent, l)
			next = append(next, fmt.Sprintf("Disk %s at %.0f%% — free space or investigate large files before it fills.", worst.MountPoint, worst.UsagePercent))
		} else {
			diskClause = "disk OK"
		}
	}

	var b strings.Builder
	b.WriteString(memClause + ", " + cpuClause + ", " + diskClause + ".")

	// Top consumer sentence.
	if top, ok := topMemoryProcess(info.Processes); ok {
		fmt.Fprintf(&b, " Top consumer: %s (%s).", top.Name, collector.FormatBytes(top.Memory))
	}

	// Swap sentence + suggestion.
	if info.Memory.SwapTotal > 0 {
		swapPct := float64(info.Memory.SwapUsed) / float64(info.Memory.SwapTotal) * 100
		if swapPct >= swapWarnPct {
			fmt.Fprintf(&b, " Swap %.0f%% used.", swapPct)
			next = append(next, "Swap pressure detected — memory is genuinely constrained; check monitor_processes with sort_by:rss.")
		}
	}

	return b.String(), next
}
