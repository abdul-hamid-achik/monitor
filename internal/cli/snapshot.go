package cli

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func newSnapshotCmd() *cobra.Command {
	var interval time.Duration
	var compact bool
	var processLimit int
	var processFilter string
	var filesystemLimit int
	var filesystemFilter string
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Print a one-shot system snapshot",
		Long: `Print a single system snapshot. Default human output, --json for the
lossless machine payload, or --compact for a bounded, versioned agent payload.

Examples:
  monitor snapshot
  monitor snapshot --json | jq '.cpu.usage_percent'
  monitor snapshot --compact --process-limit 5 | jq '.processes.top_cpu'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interval < 0 {
				return fmt.Errorf("--interval must be zero or greater")
			}
			c := NewCollector(interval)
			ctx, cancel := Context()
			defer cancel()
			info, err := collectSnapshot(ctx, c, interval)
			if err != nil {
				return err
			}
			if compact {
				return WriteJSON(collector.BuildCompactSnapshot(info, collector.CompactOptions{
					ProcessLimit: processLimit, ProcessFilter: processFilter,
					FilesystemLimit: filesystemLimit, FilesystemFilter: filesystemFilter,
				}))
			}
			if JSONOutput(cmd) {
				return WriteJSON(info)
			}
			printHumanSnapshot(info)
			return nil
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", time.Second,
		"warm-up interval between counter samples (0 = instant snapshot without rates)")
	cmd.Flags().Bool("json", false, "emit JSON to stdout")
	cmd.Flags().BoolVar(&compact, "compact", false, "emit bounded schema-versioned JSON for agents")
	cmd.Flags().IntVar(&processLimit, "process-limit", collector.DefaultCompactProcessLimit,
		"top CPU and memory processes in compact output (max 25)")
	cmd.Flags().StringVar(&processFilter, "process-filter", "",
		"case-insensitive process name substring in compact output")
	cmd.Flags().IntVar(&filesystemLimit, "filesystem-limit", collector.DefaultCompactFilesystemLimit,
		"filesystems in compact output (max 50)")
	cmd.Flags().StringVar(&filesystemFilter, "filesystem-filter", "",
		"case-insensitive device, mount, or filesystem substring in compact output")
	return cmd
}

// collectSnapshot takes a warm-up sample and, when interval is non-zero, a
// second sample after the requested delay. Counter-derived network/disk rates
// are therefore real instead of first-sample zeroes. A zero interval retains
// the fast orientation path for callers that do not need rates.
func collectSnapshot(ctx context.Context, c *collector.Collector, interval time.Duration) (collector.SystemInfo, error) {
	info := c.Collect(ctx)
	if interval == 0 {
		return info, nil
	}
	timer := time.NewTimer(interval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return collector.SystemInfo{}, ctx.Err()
	case <-timer.C:
		return c.Collect(ctx), nil
	}
}

func printHumanSnapshot(info collector.SystemInfo) {
	cpu := info.CPU
	mem := info.Memory
	net := info.Network
	fmt.Printf("Monitor Snapshot @ %s\n", info.LastUpdate.Format("15:04:05"))
	fmt.Printf("  Host:      %s (%s / %s)\n", info.Hostname, info.Platform, info.Kernel)
	fmt.Printf("  CPU:       %.1f%% (%d cores / %d threads, %.0f MHz)\n",
		cpu.UsagePercent, cpu.CoreCount, cpu.ThreadCount, cpu.FrequencyMHz)
	fmt.Printf("  Memory:    %s / %s (%.1f%%)\n",
		collector.FormatBytes(mem.UsedBytes), collector.FormatBytes(mem.TotalBytes), mem.UsagePercent)
	fmt.Printf("  Network:   ↓ %s/s  ↑ %s/s\n",
		collector.FormatBytes(net.BytesRecvPerSec), collector.FormatBytes(net.BytesSentPerSec))
	fmt.Printf("  Processes: %d\n", len(info.Processes))
}
