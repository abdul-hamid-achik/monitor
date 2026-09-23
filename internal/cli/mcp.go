package cli

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/explain"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
	"github.com/abdul-hamid-achik/monitor/internal/mcp"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp <subcommand>",
		Short: "Run an MCP stdio server exposing monitor data",
	}
	cmd.AddCommand(newMCPServeCmd())
	return cmd
}

func newMCPServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP stdio server (newline-delimited JSON-RPC)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := NewCollector(0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			// Collect must never run concurrently on one Collector (see
			// collector.Collect's doc). The MCP SDK may dispatch tool calls
			// concurrently, and monitor_analyze samples in a loop, so every
			// Collect goes through this mutex. Held per-sample, not for the
			// whole analyze window, so monitor_snapshot stalls at most one
			// Collect (~ms), never the full window.
			var collectMu sync.Mutex
			warmed := false
			collect := func(ctx context.Context) collector.SystemInfo {
				collectMu.Lock()
				defer collectMu.Unlock()
				first := c.Collect(ctx)
				if warmed {
					return first
				}
				timer := time.NewTimer(collector.MinFullCollectionInterval)
				defer timer.Stop()
				select {
				case <-ctx.Done():
					return first
				case <-timer.C:
					warmed = true
					return c.Collect(ctx)
				}
			}

			svc := &mcp.Service{
				// Read tool: latest snapshot.
				Snapshots: func() collector.SystemInfo {
					return collect(ctx)
				},
				// monitor_analyze: sample once per second over the window and
				// hand the accumulated history to the analyzer's diagnosis
				// engine (same engine `monitor watch` builds its alerts on top
				// of; see internal/analyzer/diagnosis.go).
				Analyze: func(ctx context.Context, windowSeconds int, pid int32) (mcp.AnalyzeResult, error) {
					return analyzeWindow(ctx, collect, time.Duration(windowSeconds)*time.Second, time.Second, pid)
				},
				IssuesList:   listIssuesForMCP,
				IssueContext: issueContextForMCP,
				// Mutating tools: thin wrappers over the CLI's existing logic.
				Kill: kill.KillVerified,
				// Profile is runtime-aware (E1.7): Node/Deno with --inspect
				// get a real CDP capture (file:line frames), Bun gets an
				// honest "unavailable" instead of the 404 a real /json/list
				// probe against its JSC inspector would produce, and
				// everything else falls through to the same ownership-gated
				// pprof/sample path monitor_profile_capture always used.
				// Built by buildProfileService so tests can exercise the
				// EXACT same production dispatch over an in-memory MCP
				// transport instead of a hand-rolled stub (see mcp_test.go).
				Profile: buildProfileService(),
				// Investigate runs the same real pipeline the CLI does
				// (snapshot + profile + correlate + stash).
				Investigate: func(ctx context.Context, pid int32, opts mcp.InvestigateOptions) map[string]any {
					return investigatePipeline(ctx, pid, InvestigateOptions{
						TTL:          firstNonEmpty(opts.TTL, "7d"),
						NoSave:       opts.NoSave,
						Codebase:     opts.Codebase,
						Environment:  opts.Environment,
						DeploymentID: opts.DeploymentID,
						RunID:        opts.RunID,
						StepID:       opts.StepID,
						Suite:        opts.Suite,
						Attempt:      opts.Attempt,
						Release:      opts.Release,
						Service:      opts.Service,
						GitSHA:       opts.GitSHA,
						IncludeRaw:   opts.IncludeRaw,
					}).redactRaw(opts.IncludeRaw).toMap()
				},
				// Record captures a short screen recording via the platform
				// recorder (screencapture / ffmpeg). Returns an error — turned
				// into a structured refusal by the handler — when no recorder
				// or display is available (headless agents).
				Record: func(ctx context.Context, pid int32, durationSeconds int) (string, error) {
					return ecosystem.RecordScreen(ctx, durationSeconds)
				},
			}
			s := mcp.NewServer(svc, Version)
			if err := s.Run(ctx); err != nil {
				return fmt.Errorf("mcp serve: %w", err)
			}
			return nil
		},
	}
	return cmd
}

// buildProfileService returns the mcp.Service.Profile implementation wired
// into `monitor mcp serve` (newMCPServeCmd, above). Pulled out into its own
// function — rather than an inline closure — so a test can build and call
// the EXACT same production runtime-aware dispatch (procbind.Inspect +
// captureRuntimeAwareProfile with allowInspectorHeap:true, since an
// explicit type:heap request is exactly the caller-asked-for-it case that
// flag exists for) over a real in-memory MCP transport, instead of
// re-implementing a parallel stub that could silently drift from what
// production actually calls (see mcp_test.go's
// TestProfileServiceLiveNodeInspectorViaRealDispatch).
//
// keep/discard and the receipt live here, not in server.go's
// handleProfileCapture: the same principle that keeps monitor_investigate's
// include_raw redaction in the Service instead of the handler.
func buildProfileService() func(ctx context.Context, pid int32, ptype profiler.ProfileType, pprofAddr string, keep bool) (profiler.Profile, profiler.Receipt, error) {
	return func(ctx context.Context, pid int32, ptype profiler.ProfileType, pprofAddr string, keep bool) (profiler.Profile, profiler.Receipt, error) {
		binding, inspectErr := procbind.Inspect(ctx, pid, "")
		var bindingPtr *procbind.Binding
		if inspectErr == nil {
			bindingPtr = &binding
		}
		prof, _, step := captureRuntimeAwareProfile(ctx, pid, bindingPtr, ptype, pprofAddr, "", pprofAddr != "", 0, true)
		if step.Status == stepUnavailable {
			return profiler.Profile{}, profiler.Receipt{}, &mcp.UnavailableError{Limitation: step.Limitation, Recovery: step.Recovery}
		}
		if step.Status != stepOK {
			msg := step.Limitation
			if step.Recovery != "" {
				msg = fmt.Sprintf("%s (%s)", msg, step.Recovery)
			}
			return profiler.Profile{}, profiler.Receipt{}, fmt.Errorf("%s", msg)
		}
		receipt := prof.VerifyArtifact()
		if receipt.Verified && !keep {
			if err := prof.DiscardRawArtifact(); err != nil {
				receipt.Limitation = joinLimitation(receipt.Limitation, "cleanup: "+err.Error())
			}
		}
		return prof, receipt, nil
	}
}

// listIssuesForMCP is mcp.Service.IssuesList's implementation: the one place
// (alongside newIssuesListCmd's CLI flags) that turns monitor_issues' raw
// since/until strings into time.Time via the shared issues.ParseWindowBound,
// so internal/mcp/server.go's handleIssues stays a pure field copy with no
// business logic of its own (see mcp.IssuesListFilter's doc comment).
func listIssuesForMCP(_ context.Context, filter mcp.IssuesListFilter) (items []issues.Issue, err error) {
	now := time.Now()
	since, err := issues.ParseWindowBound(filter.Since, now)
	if err != nil {
		return nil, err
	}
	until, err := issues.ParseWindowBound(filter.Until, now)
	if err != nil {
		return nil, err
	}
	path, err := issues.ResolvePath("")
	if err != nil {
		return nil, err
	}
	store, err := issues.OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := store.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	return store.List(issues.ListOptions{
		Statuses: filter.Statuses, Project: filter.Project, Service: filter.Service,
		Since: since, Until: until, RunID: filter.RunID, Release: filter.Release, Kind: filter.Kind,
	})
}

// issueContextForMCP is mcp.Service.IssueContext's implementation (E2.7):
// the single place, alongside internal/cli/issues.go's `monitor issue`
// command, that resolves "latest"+filter (via explain.ResolveLatest) or a
// concrete id/prefix (via issues.Store.ResolveID) and builds
// monitor.issue_context.v1 (explain.Build, budget "brief" -- see
// docs/contracts/issue-context-v1.md: MCP defaults to brief so a "what's
// the latest crash" question stays a small, cheap call).
func issueContextForMCP(ctx context.Context, id string, filter mcp.IssueContextFilter, occurrenceLimit int) (result *mcp.IssueContextResult, err error) {
	path, err := issues.ResolvePath("")
	if err != nil {
		return nil, err
	}
	store, err := issues.OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := store.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	var resolvedID string
	var resolvedFrom *explain.ResolvedFrom
	if strings.EqualFold(strings.TrimSpace(id), "latest") {
		latest, resolved, ok, latestErr := explain.ResolveLatest(store, explain.LatestFilter{
			Project: filter.Project, Service: filter.Service, Kind: filter.Kind,
		})
		if latestErr != nil {
			return nil, latestErr
		}
		if !ok {
			// No match under this filter -- an ordinary outcome (E2.7),
			// not a failure; handleIssue turns a nil result into a
			// recovery hint instead of an error envelope.
			return nil, nil
		}
		resolvedID = latest.ID
		resolvedFrom = &resolved
	} else {
		resolvedID, err = store.ResolveID(id)
		if err != nil {
			return nil, err
		}
	}

	issue, err := store.Get(resolvedID)
	if err != nil {
		return nil, err
	}
	occurrences, err := store.Occurrences(resolvedID, occurrenceLimit)
	if err != nil {
		return nil, err
	}

	built, buildErr := explain.Build(ctx, store, resolvedID, explain.Options{
		Budget: explain.BudgetBrief, Redact: true, ResolvedFrom: resolvedFrom,
	})
	if buildErr != nil {
		// explain.Build failing (e.g. a store.Get race after the lookups
		// above) degrades to the legacy fields alone rather than failing
		// monitor_issue outright -- Build's OWN dependency failures
		// (codemap/git/vecgrep) are never why this branch runs; those are
		// already absorbed into Context.Degraded by Build itself.
		built = nil
	}

	return &mcp.IssueContextResult{
		Issue: issue, Occurrences: occurrences,
		OccurrencesTruncated: issue.OccurrenceCount > int64(len(occurrences)),
		Context:              built,
	}, nil
}

// analyzeWindow drives collect once per sampleInterval for the duration of
// window, feeding a fresh analyzer engine, then returns the engine's
// diagnosis: DiagnosePID(pid) when pid != 0 (dropping every other process's
// findings), otherwise Diagnose() (every PID seen in the latest sample). A
// fresh engine per call keeps the history window scoped to exactly this
// analysis, matching `monitor watch`'s per-run engine.
//
// It also collects every rule Alert the engine's Observe raises across the
// window (bug 17's second half: NewDefaultEngine made this window register
// the same CPUSpike/RSSGrowth/DiskFill/SwapPressure/Zombie/Threshold rules
// `monitor watch` runs, but analyzeWindow used to throw away Observe's return
// value entirely and only ever surfaced the separate cross-signal Diagnose
// table — an agent calling monitor_analyze never saw a plain threshold, spike,
// or zombie finding that watch would have raised for the same window). Alerts
// are deduplicated by rule+PID (a sustained condition would otherwise repeat
// once per sample) and, like Diagnoses, dropped to those matching pid when
// pid != 0.
//
// Extracted from the mcp.Service.Analyze closure so the sampling/diagnosis
// wiring is unit-testable without a live collector or multi-second
// wall-clock waits (tests pass a tiny sampleInterval).
func analyzeWindow(ctx context.Context, collect func(context.Context) collector.SystemInfo, window, sampleInterval time.Duration, pid int32) (mcp.AnalyzeResult, error) {
	if sampleInterval <= 0 {
		sampleInterval = time.Second
	}
	// NewDefaultEngine is the single rule-set source shared with `monitor
	// watch` and Studio (bug 17: this window used to register zero rules, so
	// an agent calling monitor_analyze never saw a threshold/spike/growth
	// finding that `monitor watch` would have raised for the same window).
	settings, err := config.Load()
	if err != nil || settings == nil {
		settings = config.Default()
	}
	engine := analyzer.NewDefaultEngine(*settings)
	samples := 0
	var alerts []collector.Alert
	seenAlerts := map[string]bool{}
	deadline := time.After(window)
	ticker := time.NewTicker(sampleInterval)
	defer ticker.Stop()
	for {
		info := collect(ctx)
		for _, a := range engine.Observe(collector.Event{
			Timestamp: info.LastUpdate,
			Hostname:  info.Hostname,
			CPU:       info.CPU,
			Memory:    info.Memory,
			Network:   info.Network,
			Disk:      info.Disk,
			Processes: info.Processes,
		}) {
			if pid != 0 && a.PID != pid {
				continue
			}
			key := fmt.Sprintf("%s|%d", a.Rule, a.PID)
			if seenAlerts[key] {
				continue
			}
			seenAlerts[key] = true
			alerts = append(alerts, a)
		}
		samples++
		select {
		case <-ctx.Done():
			return mcp.AnalyzeResult{}, ctx.Err()
		case <-deadline:
			var diags []collector.Diagnosis
			if pid != 0 {
				if d, ok := engine.DiagnosePID(pid); ok {
					diags = []collector.Diagnosis{d}
				}
			} else {
				diags = engine.Diagnose()
			}
			return mcp.AnalyzeResult{Samples: samples, Diagnoses: diags, Alerts: alerts}, nil
		case <-ticker.C:
		}
	}
}
