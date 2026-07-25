package cli

import (
	"context"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/telemetry"
)

func newTelemetryCmd() *cobra.Command {
	var (
		interval time.Duration
		window   time.Duration
		once     bool
	)
	cmd := &cobra.Command{
		Use:   "telemetry",
		Short: "Stream bounded, privacy-safe telemetry windows as NDJSON",
		Long: `Stream privacy-safe, versioned metric rollups as one NDJSON object per window.

The telemetry contract contains fixed host-level CPU, memory, swap, network,
disk, and load metrics. It deliberately excludes hostname, IP addresses, PIDs,
process data, command arguments, environment variables, mounts, paths, raw
collector errors, and alert details. Monitor writes only to stdout; transport,
authentication, retries, and durable storage belong to the consuming adapter.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := collector.New(collector.Options{
				Interval:              interval,
				Profile:               collector.ProfileTelemetry,
				DisableProcesses:      true,
				DisableCgroupOverride: true,
			})
			swapPressure := analyzer.SwapPressureRule{}

			capture := func(ctx context.Context) (collector.SystemInfo, []collector.Alert, error) {
				info, err := c.Capture(ctx)
				if err != nil {
					return info, nil, err
				}
				event := collector.Event{
					Timestamp: info.LastUpdate,
					CPU:       info.CPU,
					Memory:    info.Memory,
					Network:   info.Network,
					Disk:      info.Disk,
				}
				alerts := swapPressure.Evaluate(event, nil)
				return info, alerts, nil
			}

			ctx, cancel := Context()
			defer cancel()
			return (telemetry.Runner{
				Interval:        interval,
				Window:          window,
				Once:            once,
				ProducerVersion: Version,
				Capture:         capture,
				Writer:          cmd.OutOrStdout(),
			}).Run(ctx)
		},
	}
	cmd.Flags().DurationVarP(&interval, "interval", "i", 5*time.Second, "sampling interval")
	cmd.Flags().DurationVarP(&window, "window", "w", 30*time.Second, "rollup window")
	cmd.Flags().BoolVar(&once, "once", false, "prewarm counters, emit one partial window, and exit")
	return cmd
}
