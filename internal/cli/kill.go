package cli

import (
	"encoding/json"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
)

func newKillCmd() *cobra.Command {
	var force bool
	var acknowledged bool
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
			// Safety classification is an invariant shared by the CLI, TUI, and
			// MCP surfaces. --yes acknowledges the request for compatibility,
			// but it must never turn into a protected-process bypass.
			if conf.HasProtected || conf.HasSystem {
				if JSONOutput(cmd) {
					// Field names mirror MCP monitor_kill: refused/reason for the
					// refusal, "safety" for the kill.Confirmation object.
					return WriteJSON(map[string]any{
						"killed":          false,
						"refused":         true,
						"reason":          "protected or system processes cannot be terminated by monitor",
						"safety":          conf,
						"protected":       conf.HasProtected,
						"safety_warnings": conf.SafetyWarnings,
					})
				}
				fmt.Println("Refusing to kill protected/system processes:")
				for _, w := range conf.SafetyWarnings {
					fmt.Println("  -", w)
				}
				return fmt.Errorf("refused")
			}
			results := make([]map[string]any, 0, len(pids))
			allKilled := true
			for _, pid := range pids {
				res, err := kill.KillVerified(pid, force)
				killed := err == nil && res.Outcome == kill.OutcomeTerminated
				if !killed {
					allKilled = false
				}
				r := map[string]any{
					"pid":       pid,
					"killed":    killed,
					"outcome":   string(res.Outcome),
					"signal":    res.Signal,
					"waited_ms": res.WaitedMs,
				}
				if err != nil {
					r["error"] = err.Error()
				}
				if res.NextAction != "" {
					r["next_action"] = res.NextAction
				}
				results = append(results, r)
			}
			if JSONOutput(cmd) {
				// killed reflects VERIFIED termination of every target, not
				// merely that signals were dispatched.
				return WriteJSON(map[string]any{
					"killed":  allKilled,
					"results": results,
				})
			}
			for _, r := range results {
				pid := r["pid"]
				if errStr, _ := r["error"].(string); errStr != "" {
					fmt.Printf("pid %v: error: %s\n", pid, errStr)
					continue
				}
				switch r["outcome"] {
				case string(kill.OutcomeTerminated):
					fmt.Printf("pid %v: terminated (%s after %vms)\n", pid, r["signal"], r["waited_ms"])
				case string(kill.OutcomeStillRunning):
					fmt.Printf("pid %v: %s sent but process is still running after %vms\n  next: %s\n", pid, r["signal"], r["waited_ms"], r["next_action"])
				default:
					fmt.Printf("pid %v: %s sent; outcome unknown\n  next: %s\n", pid, r["signal"], r["next_action"])
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&force, "force", false, "send SIGKILL instead of SIGTERM")
	cmd.Flags().BoolVar(&acknowledged, "yes", false,
		"acknowledge the request (never overrides protected/system safety)")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newProcessCmd() *cobra.Command {
	var codebase string
	cmd := &cobra.Command{
		Use:   "process <pid>",
		Short: "Print detailed process information",
		Long: `process shows collector metrics plus a redacted runtime binding (cwd, exe,
runtime class, main script, codebase root, Node --inspect addr). Use this to
confirm a Node PID can be correlated with codemap/vecgrep before investigate.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := parsePID(args[0])
			if err != nil {
				return err
			}
			c := NewCollector(0)
			ctx, cancel := Context()
			defer cancel()
			info, err := collectFullSnapshot(ctx, c)
			if err != nil {
				return err
			}
			var found *collector.ProcessInfo
			for i := range info.Processes {
				if info.Processes[i].PID == pid {
					found = &info.Processes[i]
					break
				}
			}
			if found == nil {
				direct, directErr := c.Process(ctx, pid)
				if directErr != nil {
					return fmt.Errorf("pid %d not found", pid)
				}
				found = &direct
			}
			binding, bindErr := inspectProcess(ctx, pid, codebase)
			processJSON, _ := json.Marshal(*found)
			out := map[string]any{}
			_ = json.Unmarshal(processJSON, &out)
			if bindErr != nil {
				out["binding_error"] = bindErr.Error()
			} else {
				out["binding"] = binding
			}
			if JSONOutput(cmd) {
				return WriteJSON(out)
			}
			p := *found
			fmt.Printf("PID %d  %s\n", p.PID, p.Name)
			fmt.Printf("  CPU:    %.1f%%\n", p.CPUPercent)
			fmt.Printf("  Memory: %s\n", collector.FormatBytes(p.Memory))
			fmt.Printf("  Share:   %.1f%%\n", p.MemoryPercent)
			fmt.Printf("  Threads: %d\n", p.Threads)
			fmt.Printf("  User:    %s\n", p.User)
			if p.Status != "" {
				fmt.Printf("  Status:  %s\n", p.Status)
			} else if status, ok := p.MetricStates["status"]; ok {
				fmt.Printf("  Status:  %s (%s)\n", status.State, status.Reason)
			}
			fmt.Printf("  Parent:  %d\n", p.Parent)
			if status, ok := p.MetricStates["io"]; ok && status.State != collector.MetricObserved {
				fmt.Printf("  I/O:     %s (%s)\n", status.State, status.Reason)
			} else {
				fmt.Printf("  I/O:     read %s, write %s\n", collector.FormatBytes(p.IOReadBytes), collector.FormatBytes(p.IOWriteBytes))
			}
			fmt.Printf("  Safety:  system=%t protected=%t\n", p.IsSystem, p.IsProtected)
			if bindErr != nil {
				fmt.Printf("  Binding: error: %s\n", bindErr)
				return nil
			}
			fmt.Printf("  Runtime: %s\n", binding.Runtime)
			if binding.Exe != "" {
				fmt.Printf("  Exe:     %s\n", binding.Exe)
			}
			if binding.Cwd != "" {
				fmt.Printf("  Cwd:     %s\n", binding.Cwd)
			}
			if binding.ArgvRedacted {
				fmt.Println("  Cmdline: [redacted; inspected in memory for runtime binding]")
			}
			if binding.MainScript != "" {
				fmt.Printf("  Main:    %s\n", binding.MainScript)
			}
			if binding.CodebaseRoot != "" {
				fmt.Printf("  Codebase:%s\n", binding.CodebaseRoot)
			}
			if binding.InspectAddr != "" {
				fmt.Printf("  Inspect: %s\n", binding.InspectAddr)
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	cmd.Flags().StringVar(&codebase, "codebase", "", "override codebase root for binding")
	return cmd
}
