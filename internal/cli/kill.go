package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
)

func newKillCmd() *cobra.Command {
	var force bool
	var skipConfirm bool
	cmd := &cobra.Command{
		Use:   "kill <pid> [pid...]",
		Short: "Safely terminate one or more processes",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var pids []int32
			for _, a := range args {
				p, err := parsePID(a)
				if err != nil {
					return err
				}
				pids = append(pids, p)
			}
			conf := kill.CheckSafety(pids)
			if !skipConfirm && (conf.HasProtected || conf.HasSystem) {
				if JSONOutput(cmd) {
					return WriteJSON(map[string]any{
						"killed":          false,
						"confirmation":    conf,
						"protected":       conf.HasProtected,
						"safety_warnings": conf.SafetyWarnings,
						"note":            "protected or system process; pass --yes to override",
					})
				}
				fmt.Println("Refusing to kill protected/system processes without --yes:")
				for _, w := range conf.SafetyWarnings {
					fmt.Println("  -", w)
				}
				return fmt.Errorf("refused")
			}
			results := make([]map[string]any, 0, len(pids))
			for _, pid := range pids {
				err := kill.Kill(pid, force)
				r := map[string]any{"pid": pid, "killed": err == nil}
				if err != nil {
					r["error"] = err.Error()
				}
				results = append(results, r)
			}
			if JSONOutput(cmd) {
				return WriteJSON(map[string]any{
					"killed":  true,
					"results": results,
				})
			}
			for _, r := range results {
				pid := r["pid"]
				err, _ := r["error"].(string)
				if err != "" {
					fmt.Printf("pid %v: error: %s\n", pid, err)
				} else {
					fmt.Printf("pid %v: killed\n", pid)
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "send SIGKILL instead of SIGTERM")
	cmd.Flags().BoolVar(&skipConfirm, "yes", false, "skip protection checks")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newProcessCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "process <pid>",
		Short: "Print detailed process information",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := parsePID(args[0])
			if err != nil {
				return err
			}
			c := NewCollector(0)
			ctx, cancel := Context()
			defer cancel()
			info := c.Collect(ctx) // sample once; Snapshot() is empty until Collect runs
			for _, p := range info.Processes {
				if p.PID == pid {
					if JSONOutput(cmd) {
						return WriteJSON(p)
					}
					fmt.Printf("PID %d  %s\n", p.PID, p.Name)
					fmt.Printf("  CPU:    %.1f%%\n", p.CPUPercent)
					fmt.Printf("  Memory: %s\n", collector.FormatBytes(p.Memory))
					fmt.Printf("  Threads: %d\n", p.Threads)
					fmt.Printf("  User:    %s\n", p.User)
					return nil
				}
			}
			return fmt.Errorf("pid %d not found", pid)
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}
