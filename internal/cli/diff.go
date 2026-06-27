package cli

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	gnet "github.com/shirou/gopsutil/v4/net"

	"github.com/abdul-hamid-achik/monitor/internal/baseline"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
)

// baselineDir returns ~/.local/share/monitor/baselines, creating it.
func baselineDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, ".local", "share", "monitor", "baselines")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	return dir, nil
}

// captureBaseline builds a baseline from a live snapshot plus listening ports.
func captureBaseline(ctx context.Context, name string, info collector.SystemInfo) *baseline.Baseline {
	procs := make(map[int32]baseline.ProcSnap, len(info.Processes))
	for _, p := range info.Processes {
		procs[p.PID] = baseline.ProcSnap{Name: p.Name, Memory: p.Memory, CPUPercent: p.CPUPercent}
	}
	return &baseline.Baseline{
		Name:       name,
		CapturedAt: time.Now(),
		CPUUsage:   info.CPU.UsagePercent,
		MemUsage:   info.Memory.UsagePercent,
		Load1:      info.CPU.LoadAvg1,
		Processes:  procs,
		Listeners:  gatherListeners(ctx, procs),
	}
}

// gatherListeners returns the TCP listening sockets, best-effort (some may be
// invisible without elevated privileges).
func gatherListeners(ctx context.Context, procs map[int32]baseline.ProcSnap) []baseline.Listener {
	conns, err := gnet.ConnectionsWithContext(ctx, "tcp")
	if err != nil {
		return nil
	}
	var out []baseline.Listener
	for _, c := range conns {
		if c.Status != "LISTEN" {
			continue
		}
		name := ""
		if p, ok := procs[c.Pid]; ok {
			name = p.Name
		}
		out = append(out, baseline.Listener{Proto: "tcp", Port: c.Laddr.Port, PID: c.Pid, Process: name})
	}
	return out
}

func newBaselineCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "baseline <subcommand>",
		Short: "Capture and manage labeled system baselines",
	}
	cmd.AddCommand(newBaselineSaveCmd(), newBaselineListCmd(), newBaselineDeleteCmd())
	return cmd
}

func newBaselineSaveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "save <name>",
		Short: "Capture the current system as a named baseline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := baselineDir()
			if err != nil {
				return err
			}
			ctx, cancel := Context()
			defer cancel()
			b := captureBaseline(ctx, args[0], NewCollector(0).Collect(ctx))
			if err := baseline.Save(dir, b); err != nil {
				return fmt.Errorf("save baseline: %w", err)
			}
			if JSONOutput(cmd) {
				return WriteJSON(map[string]any{"saved": args[0], "processes": len(b.Processes), "listeners": len(b.Listeners)})
			}
			fmt.Printf("saved baseline %q: %d processes, %d listeners\n", args[0], len(b.Processes), len(b.Listeners))
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newBaselineListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List saved baselines",
		RunE: func(cmd *cobra.Command, _ []string) error {
			dir, err := baselineDir()
			if err != nil {
				return err
			}
			names, err := baseline.List(dir)
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				return WriteJSON(names)
			}
			for _, n := range names {
				fmt.Println(n)
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newBaselineDeleteCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "delete <name>",
		Short: "Delete a saved baseline",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := baselineDir()
			if err != nil {
				return err
			}
			if err := baseline.Delete(dir, args[0]); err != nil {
				return fmt.Errorf("delete baseline: %w", err)
			}
			fmt.Printf("deleted baseline %q\n", args[0])
			return nil
		},
	}
	return cmd
}

func newDiffCmd() *cobra.Command {
	var memKB int
	cmd := &cobra.Command{
		Use:   "diff <baseline> [<baseline2>]",
		Short: "Diff the live system (or a second baseline) against a saved baseline",
		Long: `diff compares a saved baseline against the live system, reporting new/gone
processes, per-process memory changes, new/gone listening ports, and the
shift in CPU / memory / load.

  monitor diff pre-deploy               # pre-deploy  -> live
  monitor diff pre-deploy post-deploy   # pre-deploy  -> post-deploy`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir, err := baselineDir()
			if err != nil {
				return err
			}
			from, err := baseline.Load(dir, args[0])
			if err != nil {
				return fmt.Errorf("load baseline %q: %w", args[0], err)
			}
			var to *baseline.Baseline
			if len(args) == 2 {
				if to, err = baseline.Load(dir, args[1]); err != nil {
					return fmt.Errorf("load baseline %q: %w", args[1], err)
				}
			} else {
				ctx, cancel := Context()
				defer cancel()
				to = captureBaseline(ctx, "live", NewCollector(0).Collect(ctx))
			}
			d := baseline.Compute(from, to, uint64(memKB)*1024)
			if JSONOutput(cmd) {
				return WriteJSON(d)
			}
			printDiff(d)
			return nil
		},
	}
	cmd.Flags().IntVar(&memKB, "mem-threshold", 1024, "min per-process memory change to report, in KB")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func printDiff(d baseline.Diff) {
	fmt.Printf("%s -> %s  (cpu %+.1f%%  mem %+.1f%%  load1 %+.2f)\n", d.From, d.To, d.CPUDelta, d.MemDelta, d.Load1Delta)
	for _, p := range d.NewProcs {
		fmt.Printf("  + proc  %s (pid %d)  %s\n", p.Name, p.PID, collector.FormatBytes(p.NewMem))
	}
	for _, p := range d.GoneProcs {
		fmt.Printf("  - proc  %s (pid %d)  %s\n", p.Name, p.PID, collector.FormatBytes(p.OldMem))
	}
	for _, p := range d.ChangedProcs {
		fmt.Printf("  ~ proc  %s (pid %d)  %s  (%s -> %s)\n", p.Name, p.PID, signedBytes(p.MemDelta), collector.FormatBytes(p.OldMem), collector.FormatBytes(p.NewMem))
	}
	for _, l := range d.NewListeners {
		fmt.Printf("  + port  %s :%d (pid %d %s)\n", l.Proto, l.Port, l.PID, l.Process)
	}
	for _, l := range d.GoneListeners {
		fmt.Printf("  - port  %s :%d (pid %d %s)\n", l.Proto, l.Port, l.PID, l.Process)
	}
	if len(d.NewProcs)+len(d.GoneProcs)+len(d.ChangedProcs)+len(d.NewListeners)+len(d.GoneListeners) == 0 {
		fmt.Println("  (no process or listener changes)")
	}
}

func signedBytes(delta int64) string {
	if delta >= 0 {
		return "+" + collector.FormatBytes(uint64(delta))
	}
	return "-" + collector.FormatBytes(uint64(-delta))
}
