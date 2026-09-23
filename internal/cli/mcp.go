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
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
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
//
// The legacy {issue, occurrences, occurrences_truncated} fields (the
// pre-E2.7 shape) are only fetched and returned when the caller explicitly
// asks for occurrences (occurrenceLimit > 0): AC-6's size budget applies to
// the DEFAULT response, and the full issues.Issue plus N raw occurrences
// have no size bound of their own (a long exception message alone, or a
// large occurrence count, blew the response well past 4KB/16KB even after
// explain.Build's own budget was fixed). This also builds the not_found/
// recovery shape server.go's handleIssue merely copies, per the "zero logic
// in MCP handlers" rule -- handleIssue used to re-derive the "latest"
// default kind itself to word the recovery hint.
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
			// not a failure: a structured not_found result, never an
			// error envelope.
			return &mcp.IssueContextResult{NotFound: true, Recovery: recoveryHintForLatestNoMatch(resolved)}, nil
		}
		resolvedID = latest.ID
		resolvedFrom = &resolved
	} else {
		resolvedID, err = store.ResolveID(id)
		if err != nil {
			return nil, err
		}
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

	result = &mcp.IssueContextResult{Context: built}
	if occurrenceLimit <= 0 {
		// The bounded default: the brief Context above already answers
		// "why did it fail", including its own small issue summary
		// (short_id, status, ...) -- see explainContextFields. No store
		// re-read, no unbounded raw text.
		return result, nil
	}

	issue, err := store.Get(resolvedID)
	if err != nil {
		return nil, err
	}
	occurrences, err := store.Occurrences(resolvedID, occurrenceLimit)
	if err != nil {
		return nil, err
	}
	// Defense-in-depth scrub, same principle as explain.Options.Redact
	// (E2.2 already scrubbed this text before it was ever persisted; this
	// catches anything that could not have been -- and is the ONLY pass
	// over the legacy issue/occurrences fields, which explain.Build's own
	// Redact never touches since they are not part of its Context).
	scrubbedIssue, scrubbedOccurrences, scrubbedCount := scrubIssueForResponse(issue, occurrences)
	if built != nil {
		built.Privacy.Scrubbed += scrubbedCount
	}
	result.Issue = scrubbedIssue
	result.Occurrences = scrubbedOccurrences
	result.OccurrencesTruncated = issue.OccurrenceCount > int64(len(occurrences))
	result.IncludeLegacy = true
	return result, nil
}

// recoveryHintForLatestNoMatch explains, in plain English, why "latest"
// (optionally filtered) matched nothing and what to try next -- built here,
// not in server.go's handler (the "zero logic in MCP handlers" rule), using
// resolved's ALREADY-effective-defaulted Kind (explain.ResolveLatest fills
// it in, so this never has to re-derive the "exception" default itself).
func recoveryHintForLatestNoMatch(resolved explain.ResolvedFrom) string {
	return fmt.Sprintf("no issue matches \"latest\" (project=%q service=%q kind=%q); call monitor_issues to see what is open, or widen kind to \"any\"",
		resolved.Project, resolved.Service, resolved.Kind)
}

// scrubIssueForResponse runs a defense-in-depth scrub.Scrubber pass over
// every free-text field the legacy issue/occurrences shape would otherwise
// return verbatim (Title/Message/the exception Value, on both the issue and
// each occurrence) -- mirrors internal/explain's own redactContext, which
// only ever covers Context's fields, never these legacy ones. Returns
// copies; the store's own records are never mutated.
func scrubIssueForResponse(issue issues.Issue, occurrences []issues.Occurrence) (issues.Issue, []issues.Occurrence, int) {
	s := scrub.New()
	issue.Title = s.String(issue.Title)
	issue.Message = s.String(issue.Message)
	if issue.LatestException != nil {
		ex := *issue.LatestException
		ex.Value = s.String(ex.Value)
		issue.LatestException = &ex
	}
	scrubbedOccurrences := make([]issues.Occurrence, len(occurrences))
	for i, occ := range occurrences {
		occ.Title = s.String(occ.Title)
		occ.Message = s.String(occ.Message)
		if occ.Exception != nil {
			ex := *occ.Exception
			ex.Value = s.String(ex.Value)
			occ.Exception = &ex
		}
		scrubbedOccurrences[i] = occ
	}
	return issue, scrubbedOccurrences, s.Count()
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
