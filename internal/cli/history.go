package cli

import (
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/history"
)

func newHistoryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "history <subcommand>",
		Short: "Record and query persistent metric history",
	}
	cmd.AddCommand(newHistoryRecordCmd(), newHistoryQueryCmd(), newHistoryListCmd())
	return cmd
}

func resolveHistoryPath(dbPath string) (string, error) {
	if dbPath != "" {
		return dbPath, nil
	}
	return history.DefaultPath()
}

// sampleSystem extracts the recorded scalar metric series from a snapshot.
func sampleSystem(ts time.Time, info collector.SystemInfo) []history.Sample {
	return []history.Sample{
		{Timestamp: ts, Metric: "cpu.usage", Value: info.CPU.UsagePercent},
		{Timestamp: ts, Metric: "mem.usage", Value: info.Memory.UsagePercent},
		{Timestamp: ts, Metric: "mem.pressure", Value: info.Memory.MemoryPressure},
		{Timestamp: ts, Metric: "net.recv_bps", Value: float64(info.Network.BytesRecvPerSec)},
		{Timestamp: ts, Metric: "net.sent_bps", Value: float64(info.Network.BytesSentPerSec)},
		{Timestamp: ts, Metric: "disk.read_bps", Value: float64(info.Disk.ReadPerSec)},
		{Timestamp: ts, Metric: "disk.write_bps", Value: float64(info.Disk.WritePerSec)},
		{Timestamp: ts, Metric: "load.1", Value: info.CPU.LoadAvg1},
	}
}

func newHistoryRecordCmd() *cobra.Command {
	var (
		interval time.Duration
		dbPath   string
	)
	cmd := &cobra.Command{
		Use:   "record",
		Short: "Sample metrics on an interval and append them to the history store (until Ctrl-C)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			if interval < collector.MinFullCollectionInterval {
				return fmt.Errorf("--interval must be at least %s for full process collection", collector.MinFullCollectionInterval)
			}
			path, err := resolveHistoryPath(dbPath)
			if err != nil {
				return err
			}
			store, err := history.Open(path)
			if err != nil {
				return fmt.Errorf("open history store: %w", err)
			}
			c := NewCollector(0)
			ctx, cancel := Context()
			defer cancel()

			fmt.Fprintf(os.Stderr, "monitor: recording history to %s every %s (Ctrl-C to stop)\n", path, interval)
			t := time.NewTicker(interval)
			defer t.Stop()
			n := 0
			for {
				select {
				case <-ctx.Done():
					fmt.Fprintf(os.Stderr, "monitor: recorded %d ticks\n", n)
					return store.Close()
				case <-t.C:
					info := c.Collect(ctx)
					if err := store.Append(sampleSystem(time.Now(), info)...); err != nil {
						fmt.Fprintf(os.Stderr, "monitor: append failed: %v\n", err)
					}
					n++
				}
			}
		},
	}
	cmd.Flags().DurationVarP(&interval, "interval", "i", time.Second, "sampling interval")
	cmd.Flags().StringVar(&dbPath, "db", "", "history store path (default ~/.local/share/monitor/history.veclite)")
	return cmd
}

func newHistoryQueryCmd() *cobra.Command {
	var (
		since  time.Duration
		dbPath string
	)
	cmd := &cobra.Command{
		Use:   "query <metric>",
		Short: "Query a recorded metric over a time window with summary stats",
		Long: `query reports the samples of <metric> (e.g. cpu.usage, mem.usage,
net.recv_bps) recorded within the look-back window, plus summary stats
(min/avg/p95/max, first/last, and the trend = last - first).

Use 'monitor history list' to see which metrics have been recorded.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path, err := resolveHistoryPath(dbPath)
			if err != nil {
				return err
			}
			store, err := history.OpenReadOnly(path)
			if err != nil {
				return fmt.Errorf("open history store: %w\n"+
					"(no store yet? run 'monitor history record' first. "+
					"a recorder already running? it holds an exclusive lock — "+
					"stop it before querying, or query a copied --db path)", err)
			}
			pts, err := store.Query(args[0], time.Now().Add(-since))
			if combined := errors.Join(err, store.Close()); combined != nil {
				return combined
			}
			summary := history.Summarize(pts)
			if JSONOutput(cmd) {
				return WriteJSON(map[string]any{
					"metric":  args[0],
					"since":   since.String(),
					"summary": summary,
					"points":  pts,
				})
			}
			fmt.Printf("%s over the last %s: %d samples\n", args[0], since, summary.Count)
			if summary.Count > 0 {
				fmt.Printf("  min %.2f  avg %.2f  p95 %.2f  max %.2f\n", summary.Min, summary.Avg, summary.P95, summary.Max)
				fmt.Printf("  first %.2f  last %.2f  trend %+.2f\n", summary.First, summary.Last, summary.Trend)
			}
			return nil
		},
	}
	cmd.Flags().DurationVar(&since, "since", time.Hour, "look back this far")
	cmd.Flags().StringVar(&dbPath, "db", "", "history store path")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newHistoryListCmd() *cobra.Command {
	var dbPath string
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the metrics present in the history store",
		RunE: func(cmd *cobra.Command, _ []string) error {
			path, err := resolveHistoryPath(dbPath)
			if err != nil {
				return err
			}
			store, err := history.OpenReadOnly(path)
			if err != nil {
				return fmt.Errorf("open history store: %w\n"+
					"(no store yet? run 'monitor history record' first. "+
					"a recorder already running? it holds an exclusive lock — "+
					"stop it before querying, or query a copied --db path)", err)
			}
			metrics, err := store.Metrics()
			if combined := errors.Join(err, store.Close()); combined != nil {
				return combined
			}
			if JSONOutput(cmd) {
				return WriteJSON(metrics)
			}
			for _, m := range metrics {
				fmt.Println(m)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&dbPath, "db", "", "history store path")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}
