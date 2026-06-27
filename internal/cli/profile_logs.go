package cli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/capture"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

func newProfileCmd() *cobra.Command {
	var ptype string
	cmd := &cobra.Command{
		Use:   "profile <pid>",
		Short: "Capture a process profile (heap, cpu, goroutine)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := parsePID(args[0])
			if err != nil {
				return err
			}
			ctx, cancel := Context()
			defer cancel()
			prof, err := profiler.Capture(ctx, pid, profiler.ProfileType(ptype))
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				return WriteJSON(prof)
			}
			fmt.Printf("Profile of pid %d (%s):\n", prof.PID, prof.Type)
			fmt.Printf("  Taken: %s\n", prof.Taken.Format(time.RFC3339))
			if len(prof.Symbols) > 0 {
				fmt.Printf("  Top symbols:\n")
				for _, s := range prof.Symbols {
					fmt.Printf("    %s  %s:%d\n", s.Func, s.File, s.Line)
				}
			}
			if prof.Path != "" {
				fmt.Printf("  Saved to: %s\n", prof.Path)
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&ptype, "type", "t", "heap", "profile type: heap, cpu, goroutine, sample")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <subcommand>",
		Short: "Manage captured process logs",
	}
	cmd.AddCommand(newLogsSearchCmd())
	cmd.AddCommand(newLogsCaptureCmd())
	return cmd
}

// newLogsCaptureCmd returns the `monitor logs capture` subcommand which
// ingests a command's stdout/stderr into the log store, OR tails an
// already-running process's open log files (via lsof on macOS).
func newLogsCaptureCmd() *cobra.Command {
	var (
		pid         int32
		maxLines    int
		maxBytes    int64
		name        string
		processName string
		level       string
	)
	cmd := &cobra.Command{
		Use:   "capture [--pid N] [-- command args...]",
		Short: "Capture stdout/stderr from a process or command into the log store",
		Long: `capture ingests log lines into the local veclite store (the same
database that 'monitor logs search' reads from).

Two modes:

  1. Wrap a new command:
       monitor logs capture -- sh -c 'echo INFO: hello; echo WARN: bad >&2'
     The runner spawns the command and captures stdout+stderr until the
     process exits (or until --max-lines / --max-bytes / SIGINT).

  2. Tail an existing process:
       monitor logs capture --pid 1234
     The runner shells out to 'lsof -p <pid> -F n' to discover open
     log files (.log / .out / /var/log/... / contains /log/) and tails
     each from EOF until SIGINT.

Lines are auto-tagged: INFO: / [INFO] / WARN: / [WARN] / WARNING: /
[WARNING] / ERROR: / [ERROR] / FATAL: / DEBUG: / TRACE: prefixes are
detected and stored as the entry's level; everything else defaults
to 'info' (or 'error' for stderr lines without a level).`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := Context()
			defer cancel()

			dbPath := filepath.Join(os.TempDir(), "monitor-logs.veclite")
			store, err := openLogStore(dbPath)
			if err != nil {
				return fmt.Errorf("open log store: %w", err)
			}
			defer store.Close()

			src := capture.Source{Level: level}
			switch {
			case pid > 0:
				if name != "" {
					src.Name = name
				} else {
					src.Name = processName
				}
			case len(args) > 0:
				// Use sh -c with the joined arg list. This sidesteps
				// cobra's flag parsing (which would eat -c) AND lets
				// callers use any shell syntax (pipes, redirects, ...).
				script := strings.Join(args, " ")
				src.Command = "sh"
				src.Args = []string{"sh", "-c", script}
				if name != "" {
					src.Name = name
				} else {
					src.Name = "sh"
				}
			default:
				return fmt.Errorf("either --pid N or a command is required (e.g. 'echo INFO: hello')")
			}
			if src.Name == "" {
				src.Name = processName
			}
			if src.Level == "" {
				src.Level = "info"
			}

			runner := capture.NewRunner(store)
			runner.MaxLines = maxLines
			runner.MaxBytes = maxBytes

			fmt.Fprintf(os.Stderr, "monitor: capturing %s into %s (Ctrl-C to stop)\n", src.Name, dbPath)
			res := runner.Run(ctx, src)
			fmt.Fprintf(os.Stderr, "monitor: %d lines / %d bytes in %s\n",
				res.Lines, res.Bytes, res.Duration)
			if res.Err != nil {
				fmt.Fprintf(os.Stderr, "monitor: capture ended with error: %v\n", res.Err)
				if res.Lines == 0 {
					return res.Err
				}
			}
			if JSONOutput(cmd) {
				return WriteJSON(map[string]any{
					"name":     res.Source.Name,
					"lines":    res.Lines,
					"bytes":    res.Bytes,
					"duration": res.Duration.String(),
					"error":    errString(res.Err),
				})
			}
			return nil
		},
	}
	cmd.Flags().Int32Var(&pid, "pid", 0, "tail open log files for the running process (uses lsof)")
	cmd.Flags().IntVar(&maxLines, "max-lines", 0, "stop after capturing N lines (0 = unlimited)")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", 0, "stop after capturing N bytes (0 = unlimited)")
	cmd.Flags().StringVar(&name, "name", "", "override the captured process name in the log store")
	cmd.Flags().StringVar(&processName, "process-name", "", "alias for --name (kept for clarity)")
	cmd.Flags().StringVar(&level, "level", "", "default level for lines without a token; empty = info (or error for stderr)")
	cmd.Flags().Bool("json", false, "emit JSON output (final result)")
	return cmd
}

// errString returns err.Error() or "" for nil; used to render an
// optional error field in JSON output without printing "<nil>".
func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func newLogsSearchCmd() *cobra.Command {
	var limit int
	cmd := &cobra.Command{
		Use:   "search <query>",
		Short: "Search captured logs",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dbPath := filepath.Join(os.TempDir(), "monitor-logs.veclite")
			store, err := openLogStore(dbPath)
			if err != nil {
				return fmt.Errorf("open log store: %w", err)
			}
			defer store.Close()
			results, err := store.Search(args[0], limit)
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				return WriteJSON(results)
			}
			for _, r := range results {
				fmt.Printf("[%s] %d %s %s\n", r.Timestamp.Format(time.RFC3339), r.PID, r.Process, r.Message)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "max results")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newInvestigateCmd() *cobra.Command {
	var (
		ttl    string
		noSave bool
	)
	cmd := &cobra.Command{
		Use:   "investigate <pid>",
		Short: "Run the diagnostic pipeline for a process",
		Long: `investigate captures the current system snapshot + a process
profile into an fcheap stash (via internal/incidents), then prints the
stash ID so the bundle can be searched or restored later.

Pass --no-save to capture the bundle into a temp dir but skip the
fcheap stash step (useful for sandboxed environments).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := parsePID(args[0])
			if err != nil {
				return err
			}
			ctx, cancel := Context()
			defer cancel()

			// Capture the profile first; if the process is gone, the
			// snapshot-only stash is still useful.
			profile, _ := profiler.Capture(ctx, pid, profiler.ProfileHeap)
			processName := ""
			for _, p := range NewCollector(0).Collect(ctx).Processes {
				if p.PID == pid {
					processName = p.Name
					break
				}
			}

			snapshot := NewCollector(0).Collect(ctx)

			req := incidents.CaptureRequest{
				Snapshot: snapshot,
				Profile:  profile,
				Alert: incidents.AlertDetail{
					Rule:    "investigate",
					PID:     pid,
					Process: processName,
					Detail:  fmt.Sprintf("manual investigate of pid %d", pid),
				},
				Trigger: "investigate",
				TTL:     ttl,
			}
			if noSave {
				// Skip fcheap; produce a structured report instead.
				report := map[string]any{
					"pid":        pid,
					"started_at": time.Now().Format(time.RFC3339),
					"steps":      []string{"snapshot", "profile"},
					"profile":    profile,
					"note":       "--no-save: bundle not stashed; profile included in JSON",
				}
				return WriteJSON(report)
			}

			res, err := incidents.Capture(ctx, req)
			report := map[string]any{
				"pid":        pid,
				"started_at": time.Now().Format(time.RFC3339),
				"steps":      []string{"snapshot", "profile", "stash"},
				"stash":      res,
				"note":       "investigation pipeline (capture + stash)",
			}
			if err != nil {
				report["stash_error"] = err.Error()
				report["note"] = "stash failed; profile captured locally at " + res.Path
			}
			if JSONOutput(cmd) {
				return WriteJSON(report)
			}
			b, _ := json.MarshalIndent(report, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	cmd.Flags().StringVar(&ttl, "ttl", "7d", "TTL for the stash (fcheap --ttl)")
	cmd.Flags().BoolVar(&noSave, "no-save", false, "skip the fcheap stash step")
	return cmd
}

// newStashCmd is a manual incident-capture entry point. Useful when an
// operator sees something the analyzer didn't fire on, or before/after
// a known-good deploy.
func newStashCmd() *cobra.Command {
	var (
		note string
		ttl  string
	)
	cmd := &cobra.Command{
		Use:   "stash",
		Short: "Capture the current system snapshot to fcheap",
		Long: `stash bundles the current system snapshot into a content-addressed
fcheap stash and returns the stash ID. Useful for capturing "before"
states before risky operations, or manual incident triage.

The trigger tag defaults to "manual"; pass --note to record context
that downstream search can pick up via fcheap analyze.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := Context()
			defer cancel()

			snapshot := NewCollector(0).Collect(ctx)
			req := incidents.CaptureRequest{
				Snapshot: snapshot,
				Alert: incidents.AlertDetail{
					Rule:   "manual",
					Detail: note,
				},
				Trigger: "manual",
				TTL:     ttl,
			}
			res, err := incidents.Capture(ctx, req)
			report := map[string]any{
				"started_at": time.Now().Format(time.RFC3339),
				"stash":      res,
			}
			if err != nil {
				report["stash_error"] = err.Error()
				if res.Path != "" {
					report["local_path"] = res.Path
				}
			}
			if JSONOutput(cmd) {
				return WriteJSON(report)
			}
			b, _ := json.MarshalIndent(report, "", "  ")
			fmt.Println(string(b))
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	cmd.Flags().StringVar(&note, "note", "", "free-form note for downstream search")
	cmd.Flags().StringVar(&ttl, "ttl", "7d", "TTL for the stash (fcheap --ttl)")
	return cmd
}

// newIncidentsCmd lists recent monitor incident stashes. Wraps
// fcheap list with the monitor-incident tag pre-applied.
func newIncidentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "incidents",
		Short: "List recent monitor incident stashes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := Context()
			defer cancel()
			entries, err := incidents.Search(ctx, nil)
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				return WriteJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Println("No monitor incident stashes found.")
				return nil
			}
			fmt.Printf("Recent incident stashes (%d):\n", len(entries))
			for _, e := range entries {
				fmt.Printf("  %s  %s  %s\n", e.CreatedAt, e.ID, e.Name)
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}
