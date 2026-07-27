package cli

import (
	"fmt"
	"io"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

type analyzeReport struct {
	Window    string                `json:"window"`
	Interval  string                `json:"interval"`
	PID       int32                 `json:"pid,omitempty"`
	Samples   int                   `json:"samples"`
	Healthy   bool                  `json:"healthy"`
	Diagnoses []collector.Diagnosis `json:"diagnoses"`
}

func newAnalyzeCmd() *cobra.Command {
	var window time.Duration
	var interval time.Duration
	var pid int32
	cmd := &cobra.Command{
		Use:   "analyze",
		Short: "Sample a window and diagnose process behavior",
		Long: `Sample the system over a bounded window and run the same cross-signal
diagnosis engine exposed by the monitor_analyze MCP tool. A PID focuses the
result on one process; without it, every process in the final sample is
considered.

Examples:
  monitor analyze --window 10s
  monitor analyze --pid 1234 --window 15s --json`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateAnalyzeOptions(window, interval, pid); err != nil {
				return err
			}
			ctx, cancel := Context()
			defer cancel()
			c := NewCollector(interval)
			result, err := analyzeWindow(ctx, c.Collect, window, interval, pid)
			if err != nil {
				return err
			}
			diagnoses := result.Diagnoses
			if diagnoses == nil {
				diagnoses = []collector.Diagnosis{}
			}
			report := analyzeReport{
				Window: window.String(), Interval: interval.String(), PID: pid,
				Samples: result.Samples, Healthy: len(diagnoses) == 0, Diagnoses: diagnoses,
			}
			if JSONOutput(cmd) {
				return WriteJSON(report)
			}
			return printAnalyzeReport(cmd.OutOrStdout(), report)
		},
	}
	cmd.Flags().DurationVar(&window, "window", 10*time.Second, "total sampling window (max 60s)")
	cmd.Flags().DurationVarP(&interval, "interval", "i", time.Second, "time between samples")
	cmd.Flags().Int32Var(&pid, "pid", 0, "focus diagnosis on one PID (0 = all processes)")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func validateAnalyzeOptions(window, interval time.Duration, pid int32) error {
	if window <= 0 || window > 60*time.Second {
		return fmt.Errorf("--window must be greater than zero and at most 60s")
	}
	if interval <= 0 || interval > window {
		return fmt.Errorf("--interval must be greater than zero and no longer than --window")
	}
	if interval < collector.MinFullCollectionInterval {
		return fmt.Errorf("--interval must be at least %s for full process collection", collector.MinFullCollectionInterval)
	}
	if pid < 0 {
		return fmt.Errorf("--pid must be zero or a positive process ID")
	}
	return nil
}

func printAnalyzeReport(w io.Writer, report analyzeReport) error {
	focus := "all processes"
	if report.PID > 0 {
		focus = fmt.Sprintf("pid %d", report.PID)
	}
	if _, err := fmt.Fprintf(w, "Analyzed %d samples over %s (%s)\n", report.Samples, report.Window, focus); err != nil {
		return err
	}
	if report.Healthy {
		_, err := fmt.Fprintln(w, "No cross-signal process anomalies found.")
		return err
	}
	for _, diagnosis := range report.Diagnoses {
		if _, err := fmt.Fprintf(w, "\n[%s] %s\n", diagnosis.Confidence, diagnosis.Summary); err != nil {
			return err
		}
		for _, evidence := range diagnosis.Evidence {
			if _, err := fmt.Fprintf(w, "  evidence: %s\n", evidence); err != nil {
				return err
			}
		}
		for _, action := range diagnosis.NextActions {
			if _, err := fmt.Fprintf(w, "  next: %s\n", action); err != nil {
				return err
			}
		}
	}
	return nil
}
