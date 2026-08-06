package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/capture"
	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/logger"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

func newProfileCmd() *cobra.Command {
	var ptype, pprofAddr, inspectAddr, output string
	var duration time.Duration
	cmd := &cobra.Command{
		Use:   "profile <pid>",
		Short: "Capture a runtime-aware process profile",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := parsePID(args[0])
			if err != nil {
				return err
			}
			ctx, cancel := Context()
			defer cancel()
			pt := profiler.ProfileType(ptype)
			if duration <= 0 || duration > 2*time.Minute {
				return fmt.Errorf("--duration must be greater than zero and at most 2m")
			}
			binding, inspectErr := procbind.Inspect(ctx, pid, "")
			jsRuntime := inspectErr == nil && (binding.Runtime == procbind.RuntimeNode ||
				binding.Runtime == procbind.RuntimeBun || binding.Runtime == procbind.RuntimeDeno)
			var prof profiler.Profile
			if jsRuntime && (pt == profiler.ProfileCPU || pt == profiler.ProfileHeap) {
				addr := inspectAddr
				if addr == "" {
					addr = binding.InspectAddr
				}
				if addr == "" {
					return fmt.Errorf("%s process %d has no inspector address; start it with --inspect=127.0.0.1:<port> or pass --inspect-addr", binding.Runtime, pid)
				}
				if own, detail := profiler.VerifyInspectorOwnership(ctx, pid, addr); own != profiler.OwnershipOwned {
					return fmt.Errorf("refusing inspector %s for pid %d: %s", addr, pid, detail)
				}
				var captureErr error
				switch pt {
				case profiler.ProfileCPU:
					prof, captureErr = profiler.ProfileInspector(ctx, pid, addr, duration)
				case profiler.ProfileHeap:
					prof, captureErr = profiler.ProfileInspectorHeap(ctx, pid, addr)
				}
				if captureErr != nil {
					return captureErr
				}
			} else {
				if err := profiler.ValidateCapture(pt); err != nil {
					return err
				}
				if pt != profiler.ProfileSample && !cmd.Flags().Changed("pprof-addr") {
					if own, detail := profiler.VerifyListenerOwnership(ctx, pid, pprofAddr); own != profiler.OwnershipOwned {
						return fmt.Errorf("refusing to scrape %s for pid %d: %s (pass --pprof-addr explicitly to assert the endpoint is correct, or use -t sample)", pprofAddr, pid, detail)
					}
				}
				var captureErr error
				prof, captureErr = profiler.Capture(ctx, pid, pt, pprofAddr)
				if captureErr != nil {
					return captureErr
				}
			}
			if output != "" {
				if err := persistProfileOutput(&prof, output); err != nil {
					return err
				}
			}
			rec := prof.VerifyArtifact()
			prof.Receipt = &rec
			prof.Context = contextids.FromEnv(contextids.IDs{})
			if !rec.Verified {
				return fmt.Errorf("profile captured no usable artifact: %s", rec.Limitation)
			}
			if JSONOutput(cmd) {
				return WriteJSON(prof)
			}
			fmt.Printf("Profile of pid %d (%s via %s):\n", prof.PID, prof.Type, prof.Method)
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
	cmd.Flags().StringVar(&pprofAddr, "pprof-addr", "localhost:6060",
		"host:port of the target's net/http/pprof server (Go heap/cpu/goroutine only); "+
			"passing this flag explicitly asserts the endpoint belongs to the target pid and skips the ownership check")
	cmd.Flags().StringVar(&inspectAddr, "inspect-addr", "", "Node/Bun/Deno inspector host:port (defaults to the process --inspect flag)")
	cmd.Flags().DurationVar(&duration, "duration", 5*time.Second, "CPU sampling duration (maximum 2m)")
	cmd.Flags().StringVar(&output, "output", "", "persist the raw profile at this path (mode 0600)")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

const maxPersistedProfileBytes int64 = 128 << 20

func persistProfileOutput(profile *profiler.Profile, output string) error {
	abs, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("resolve profile output: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return fmt.Errorf("create profile output directory: %w", err)
	}
	temp, err := os.CreateTemp(filepath.Dir(abs), ".monitor-profile-*")
	if err != nil {
		return fmt.Errorf("create profile output: %w", err)
	}
	tempPath := temp.Name()
	committed := false
	defer func() {
		_ = temp.Close()
		if !committed {
			_ = os.Remove(tempPath)
		}
	}()
	if err := temp.Chmod(0o600); err != nil {
		return fmt.Errorf("protect profile output: %w", err)
	}
	if profile.Path != "" {
		source, err := os.Open(profile.Path)
		if err != nil {
			return fmt.Errorf("open captured profile: %w", err)
		}
		written, copyErr := io.Copy(temp, io.LimitReader(source, maxPersistedProfileBytes+1))
		closeErr := source.Close()
		if copyErr != nil {
			return fmt.Errorf("copy captured profile: %w", copyErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close captured profile: %w", closeErr)
		}
		if written > maxPersistedProfileBytes {
			return fmt.Errorf("captured profile exceeds %d bytes", maxPersistedProfileBytes)
		}
	} else {
		if int64(len(profile.Text)) > maxPersistedProfileBytes {
			return fmt.Errorf("captured profile exceeds %d bytes", maxPersistedProfileBytes)
		}
		if _, err := io.WriteString(temp, profile.Text); err != nil {
			return fmt.Errorf("write captured profile: %w", err)
		}
	}
	if err := temp.Sync(); err != nil {
		return fmt.Errorf("sync profile output: %w", err)
	}
	if err := temp.Close(); err != nil {
		return fmt.Errorf("close profile output: %w", err)
	}
	if err := os.Rename(tempPath, abs); err != nil {
		return fmt.Errorf("commit profile output: %w", err)
	}
	committed = true
	if profile.Path != "" && profile.Path != abs {
		_ = os.Remove(profile.Path)
	}
	profile.Path = abs
	profile.Text = ""
	return nil
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
		storePath   string
	)
	cmd := &cobra.Command{
		Use:   "capture [--pid N] [-- command args...]",
		Short: "Capture stdout/stderr from a process or command into the log store",
		Long: `capture ingests log lines into the local veclite store (the same
database that 'monitor logs search' reads from).

Two modes:

  1. Wrap a new command:
       monitor logs capture -- sh -c 'echo INFO: hello; echo WARN: bad >&2'
     Everything after -- is passed directly to the program as its exact argv;
     Monitor does not join, quote, or reinterpret it through a shell. The
     runner captures stdout+stderr until the process exits (or until
     --max-lines / --max-bytes / SIGINT).

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

			src, err := buildCaptureSource(args, cmd.ArgsLenAtDash(), pid, name, processName, level)
			if err != nil {
				return err
			}
			dbPath, err := logger.ResolvePath(storePath)
			if err != nil {
				return err
			}
			store, err := openLogStore(dbPath)
			if err != nil {
				return fmt.Errorf("open log store: %w", err)
			}
			closed := false
			defer func() {
				if !closed {
					_ = store.Close()
				}
			}()

			runner := capture.NewRunner(store)
			runner.MaxLines = maxLines
			runner.MaxBytes = maxBytes

			fmt.Fprintf(os.Stderr, "monitor: capturing %s into %s (Ctrl-C to stop)\n", src.Name, dbPath)
			res := runner.Run(ctx, src)
			if err := store.Close(); err != nil {
				res.Err = errors.Join(res.Err, fmt.Errorf("close log store: %w", err))
			}
			closed = true
			fmt.Fprintf(os.Stderr, "monitor: %d lines / %d bytes in %s\n",
				res.Lines, res.Bytes, res.Duration)
			if res.Err != nil {
				fmt.Fprintf(os.Stderr, "monitor: capture ended with error: %v\n", res.Err)
			}
			if JSONOutput(cmd) {
				if err := WriteJSON(map[string]any{
					"name":     res.Source.Name,
					"lines":    res.Lines,
					"bytes":    res.Bytes,
					"duration": res.Duration.String(),
					"store":    dbPath,
					"error":    errString(res.Err),
				}); err != nil {
					return err
				}
			}
			return res.Err
		},
	}
	cmd.Flags().Int32Var(&pid, "pid", 0, "tail open log files for the running process (uses lsof)")
	cmd.Flags().IntVar(&maxLines, "max-lines", 0, "stop after capturing N lines (0 = unlimited)")
	cmd.Flags().Int64Var(&maxBytes, "max-bytes", 0, "stop after capturing N bytes (0 = unlimited)")
	cmd.Flags().StringVar(&name, "name", "", "override the captured process name in the log store")
	cmd.Flags().StringVar(&processName, "process-name", "", "alias for --name (kept for clarity)")
	cmd.Flags().StringVar(&level, "level", "", "default level for lines without a token; empty = info (or error for stderr)")
	cmd.Flags().StringVar(&storePath, "store", "", "log store path (default ~/.local/share/monitor/logs.veclite; env MONITOR_LOG_STORE)")
	cmd.Flags().Bool("json", false, "emit JSON output (final result)")
	return cmd
}

func buildCaptureSource(args []string, argsAtDash int, pid int32, name, processName, level string) (capture.Source, error) {
	if pid < 0 {
		return capture.Source{}, fmt.Errorf("--pid must be greater than zero")
	}
	if pid > 0 && len(args) > 0 {
		return capture.Source{}, fmt.Errorf("--pid and a command cannot be used together")
	}
	if pid <= 0 && len(args) == 0 {
		return capture.Source{}, fmt.Errorf("either --pid N or a command after -- is required")
	}
	if name == "" {
		name = processName
	}
	if level == "" {
		level = "info"
	}
	level = strings.ToLower(strings.TrimSpace(level))
	if !validLogLevels[level] {
		return capture.Source{}, fmt.Errorf("invalid --level %q (use trace, debug, info, warn, error, or fatal)", level)
	}
	if pid > 0 {
		if name == "" {
			name = fmt.Sprintf("pid:%d", pid)
		}
		return capture.Source{PID: pid, Name: name, Level: level}, nil
	}

	if argsAtDash < 0 {
		// Keep the original single-string form working for existing scripts,
		// but never reconstruct a script by joining multiple argv elements.
		if len(args) != 1 {
			return capture.Source{}, fmt.Errorf("put the command and its arguments after -- to preserve exact argv")
		}
		if name == "" {
			name = "sh"
		}
		return capture.Source{Command: "sh", Args: []string{"sh", "-c", args[0]}, Name: name, Level: level}, nil
	}
	argv := append([]string(nil), args...)
	if name == "" {
		name = filepath.Base(argv[0])
	}
	return capture.Source{Command: argv[0], Args: argv, Name: name, Level: level}, nil
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
	var (
		limit     int
		storePath string
		levels    []string
		process   string
		pid       int32
		since     time.Duration
		format    string
		output    string
	)
	cmd := &cobra.Command{
		Use:   "search [query]",
		Short: "Search and export captured logs",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if limit <= 0 {
				return fmt.Errorf("--limit must be greater than zero")
			}
			if since < 0 {
				return fmt.Errorf("--since cannot be negative")
			}
			if pid < 0 {
				return fmt.Errorf("--pid must be greater than zero")
			}
			for _, level := range levels {
				level = strings.ToLower(strings.TrimSpace(level))
				if !validLogLevels[level] {
					return fmt.Errorf("invalid --level %q", level)
				}
			}
			dbPath, err := logger.ResolvePath(storePath)
			if err != nil {
				return err
			}
			store, err := logger.OpenReadOnly(dbPath)
			if err != nil {
				return fmt.Errorf("open log store: %w", err)
			}
			query := ""
			if len(args) == 1 {
				query = args[0]
			}
			opts := logger.SearchOptions{
				Query: query, Limit: limit, Levels: levels, Process: process, PID: pid,
			}
			if since > 0 {
				opts.Since = time.Now().Add(-since)
			}
			results, err := store.SearchWithOptions(opts)
			if combined := errors.Join(err, store.Close()); combined != nil {
				return combined
			}
			if JSONOutput(cmd) {
				if cmd.Flags().Changed("format") && format != "json" {
					return fmt.Errorf("--json conflicts with --format %s", format)
				}
				format = "json"
			}
			out := cmd.OutOrStdout()
			var file *os.File
			if output != "" && output != "-" {
				file, err = os.Create(output)
				if err != nil {
					return fmt.Errorf("create export: %w", err)
				}
				defer func() {
					if file != nil {
						_ = file.Close()
					}
				}()
				out = file
			}
			if err := writeLogEntries(out, results, format); err != nil {
				return err
			}
			if file != nil {
				if err := file.Close(); err != nil {
					return fmt.Errorf("close export: %w", err)
				}
				file = nil
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "max results")
	cmd.Flags().StringVar(&storePath, "store", "", "log store path (default ~/.local/share/monitor/logs.veclite; env MONITOR_LOG_STORE)")
	cmd.Flags().StringSliceVar(&levels, "level", nil, "filter levels (repeat or comma-separate)")
	cmd.Flags().StringVar(&process, "process", "", "filter by process name substring")
	cmd.Flags().Int32Var(&pid, "pid", 0, "filter by process ID")
	cmd.Flags().DurationVar(&since, "since", 0, "only include entries this recent (for example 15m or 2h)")
	cmd.Flags().StringVar(&format, "format", "text", "output format: text, json, ndjson, raw")
	cmd.Flags().StringVarP(&output, "output", "o", "", "write the export to a file ('-' for stdout)")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

var validLogLevels = map[string]bool{
	"trace": true, "debug": true, "info": true, "warn": true,
	"warning": true, "error": true, "fatal": true,
}

func writeLogEntries(w io.Writer, entries []logger.Entry, format string) error {
	switch strings.ToLower(strings.TrimSpace(format)) {
	case "text":
		for _, entry := range entries {
			if _, err := fmt.Fprintf(w, "[%s] %-7s %d %s %s\n",
				entry.Timestamp.Format(time.RFC3339), entry.Level, entry.PID, entry.Process, entry.Message); err != nil {
				return err
			}
		}
		return nil
	case "raw":
		for _, entry := range entries {
			raw := entry.Raw
			if raw == "" {
				raw = entry.Message
			}
			if _, err := fmt.Fprintln(w, raw); err != nil {
				return err
			}
		}
		return nil
	case "json":
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(entries)
	case "ndjson":
		enc := json.NewEncoder(w)
		for _, entry := range entries {
			if err := enc.Encode(entry); err != nil {
				return err
			}
		}
		return nil
	default:
		return fmt.Errorf("unknown log output format %q (use text, json, ndjson, or raw)", format)
	}
}

func newInvestigateCmd() *cobra.Command {
	var (
		ttl          string
		noSave       bool
		codebase     string
		environment  string
		deploymentID string
		runID        string
		stepID       string
		suite        string
		attempt      string
		release      string
		service      string
		gitSHA       string
	)
	cmd := &cobra.Command{
		Use:   "investigate <pid>",
		Short: "Run the diagnostic pipeline for a process (profile + code correlate + stash)",
		Long: `investigate binds a process to its codebase (Node/Bun/Go/…), captures a
profile, correlates hot frames via codemap, searches related code via
vecgrep, groups the occurrence into a durable local issue, and optionally
stashes an integrity-verified incident bundle to fcheap.

For Node processes, monitor reads cmdline/cwd, detects --inspect, finds the
nearest package.json root (or uses --codebase), and prefers host sampling
over Go pprof. Pass --codebase when auto-detection fails or the process
cwd is not the indexed project.

Correlation IDs (Chalupa CI) are read from the environment or flags and
attached as fcheap tags + manifest context — never mixed into telemetry.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			pid, err := parsePID(args[0])
			if err != nil {
				return err
			}
			ctx, cancel := Context()
			defer cancel()

			report := investigatePipeline(ctx, pid, InvestigateOptions{
				TTL:          ttl,
				NoSave:       noSave,
				Codebase:     codebase,
				Environment:  environment,
				DeploymentID: deploymentID,
				RunID:        runID,
				StepID:       stepID,
				Suite:        suite,
				Attempt:      attempt,
				Release:      release,
				Service:      service,
				GitSHA:       gitSHA,
			})
			if noSave {
				return WriteJSON(report)
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
	cmd.Flags().StringVar(&codebase, "codebase", "", "project root for codemap/vecgrep (default: auto-detect from process cwd)")
	cmd.Flags().StringVar(&environment, "environment", "", "correlation env (or MONITOR_ENVIRONMENT / CHALUPA_CI_ENVIRONMENT)")
	cmd.Flags().StringVar(&deploymentID, "deployment-id", "", "correlation deployment id (or MONITOR_DEPLOYMENT_ID / CHALUPA_DEPLOYMENT_ID)")
	cmd.Flags().StringVar(&runID, "run-id", "", "correlation run id (or MONITOR_RUN_ID / CHALUPA_CI_RUN_ID)")
	cmd.Flags().StringVar(&stepID, "step-id", "", "correlation step id (or MONITOR_STEP_ID / CHALUPA_CI_STEP_ID)")
	cmd.Flags().StringVar(&suite, "suite", "", "correlation suite (or MONITOR_SUITE / CHALUPA_CI_SUITE)")
	cmd.Flags().StringVar(&attempt, "attempt", "", "correlation attempt (or MONITOR_ATTEMPT / CHALUPA_CI_ATTEMPT)")
	cmd.Flags().StringVar(&release, "release", "", "correlation release (or MONITOR_RELEASE)")
	cmd.Flags().StringVar(&service, "service", "", "correlation service name")
	cmd.Flags().StringVar(&gitSHA, "git-sha", "", "correlation git sha")
	return cmd
}

// correlateProfile resolves each profile frame's file:line to its enclosing
// codemap symbol (FQN/kind), enriching the diagnose flow. Best-effort: it
// returns nil when codemap isn't on PATH or there are no frames, and silently
// skips frames codemap can't resolve. codebase, when non-empty, is passed as
// `codemap -C` so the correct index is used.
func correlateProfile(ctx context.Context, syms []profiler.Symbol, codebase string) []map[string]any {
	if !ecosystem.CodemapAvailable() || len(syms) == 0 {
		return nil
	}
	opts := ecosystem.CodemapOpts{Path: codebase}
	var out []map[string]any
	// One total budget and a fixed frame cap keep a profile with thousands of
	// symbols from multiplying subprocess timeouts.
	correlateCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	resolvedFrames := 0
	for _, s := range syms {
		if s.File == "" || s.Line <= 0 {
			continue
		}
		if resolvedFrames >= 12 || correlateCtx.Err() != nil {
			break
		}
		resolvedFrames++
		entry := map[string]any{"func": s.Func, "file": s.File, "line": s.Line}
		if s.Weight > 0 {
			entry["weight_pct"] = s.Weight
		}
		// Bound each codemap subprocess so one slow/hung invocation can't stall
		// the whole pipeline (and hang the stdio MCP server, whose ctx has no
		// deadline). Mirrors ecosystem.probe()'s per-call timeout.
		sym, err := ecosystem.CodemapSymbolAtPath(correlateCtx, s.File, s.Line, opts)
		if err == nil {
			entry["resolution"] = sym.Resolution
			entry["indexed"] = sym.Indexed
			if !sym.Indexed {
				entry["limitation"] = "codemap project is not indexed"
			}
			if sym.FQN != "" {
				entry["fqn"] = sym.FQN
				entry["kind"] = sym.Kind
				// Enrich resolved frames with blast radius + test coverage,
				// turning the frame list into a "fix this first" ranking.
				imp, ierr := ecosystem.CodemapImpactAtPath(correlateCtx, s.File, s.Line, 0, opts)
				if ierr == nil && imp.Found {
					entry["call_graph"] = imp.CallGraph
					if imp.Resolution != "" {
						entry["impact_resolution"] = imp.Resolution
					}
					if imp.Note != "" {
						entry["impact_note"] = imp.Note
					}
					if imp.CallGraph == "resolved" || imp.CallGraph == "name" {
						blast := len(imp.BlastRadius)
						entry["callers"] = len(imp.DirectCallers)
						entry["blast"] = blast
						entry["tests"] = len(imp.Tests)
						entry["untested"] = imp.Untested
						if s.Weight > 0 {
							entry["score"] = s.Weight * float64(blast)
						} else {
							entry["score"] = float64(blast)
						}
					}
				}
			}
		}
		out = append(out, entry)
	}
	// Highest score (hot x blast) first. Stable with a file:line tiebreaker so
	// equal-score frames (common for heap/goroutine profiles, which carry no
	// per-frame weight) rank deterministically across runs.
	sort.SliceStable(out, func(i, j int) bool {
		si, sj := correlationScore(out[i]), correlationScore(out[j])
		if si != sj {
			return si > sj
		}
		fi, _ := out[i]["file"].(string)
		fj, _ := out[j]["file"].(string)
		if fi != fj {
			return fi < fj
		}
		li, _ := out[i]["line"].(int)
		lj, _ := out[j]["line"].(int)
		return li < lj
	})
	return out
}

func correlationScore(m map[string]any) float64 {
	if v, ok := m["score"].(float64); ok {
		return v
	}
	return 0
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
		Long: `stash bundles the current system snapshot, computes a stable integrity
hash, saves it to fcheap, and returns the provider's opaque stash ID. Useful for capturing "before"
states before risky operations, or manual incident triage.

The trigger tag defaults to "manual"; pass --note to record context
that downstream search can pick up via fcheap analyze.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := Context()
			defer cancel()

			snapshot, err := collectFullSnapshot(ctx, NewCollector(0))
			if err != nil {
				return err
			}
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
				if res.RegistryID != "" {
					report["registry_id"] = res.RegistryID
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
