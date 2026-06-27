package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

func newSnapshotCmd() *cobra.Command {
	var interval time.Duration
	cmd := &cobra.Command{
		Use:   "snapshot",
		Short: "Print a one-shot system snapshot",
		Long: `Print a single system snapshot. Default human output, --json for machine.

Examples:
  monitor snapshot
  monitor snapshot --json | jq '.cpu.usage_percent'`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := NewCollector(interval)
			ctx, cancel := Context()
			defer cancel()
			info := c.Collect(ctx)
			if JSONOutput(cmd) {
				return WriteJSON(info)
			}
			printHumanSnapshot(info)
			return nil
		},
	}
	cmd.Flags().DurationVar(&interval, "interval", time.Second, "sampling interval")
	cmd.Flags().Bool("json", false, "emit JSON to stdout")
	return cmd
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