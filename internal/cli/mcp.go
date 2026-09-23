package cli

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
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
				IssuesList: listIssuesForMCP,
				IssueGet:   getIssueForMCP,
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
// include_raw redaction in the Service instead of the handler. lines:true's
// bounded line_heatmap (E3.6) is built here too, BEFORE the keep/discard
// step below runs — a CDP CPU capture's only raw evidence, Profile.Text, is
// gone once discarded, so building the heatmap has to happen first.
func buildProfileService() func(ctx context.Context, pid int32, ptype profiler.ProfileType, pprofAddr string, keep, lines bool) (mcp.ProfileCaptureResult, error) {
	return func(ctx context.Context, pid int32, ptype profiler.ProfileType, pprofAddr string, keep, lines bool) (mcp.ProfileCaptureResult, error) {
		binding, inspectErr := procbind.Inspect(ctx, pid, "")
		var bindingPtr *procbind.Binding
		if inspectErr == nil {
			bindingPtr = &binding
		}
		prof, _, step := captureRuntimeAwareProfile(ctx, pid, bindingPtr, ptype, pprofAddr, "", pprofAddr != "", 0, true)
		if step.Status == stepUnavailable {
			return mcp.ProfileCaptureResult{}, &mcp.UnavailableError{Limitation: step.Limitation, Recovery: step.Recovery}
		}
		if step.Status != stepOK {
			msg := step.Limitation
			if step.Recovery != "" {
				msg = fmt.Sprintf("%s (%s)", msg, step.Recovery)
			}
			return mcp.ProfileCaptureResult{}, fmt.Errorf("%s", msg)
		}
		receipt := prof.VerifyArtifact()

		var heatmap *profiler.Heatmap
		var heatmapSkip *mcp.HeatmapSkip
		if lines && receipt.Verified {
			heatmap, heatmapSkip = buildMCPLineHeatmap(ctx, prof, bindingPtr)
		}

		if receipt.Verified && !keep {
			if err := prof.DiscardRawArtifact(); err != nil {
				receipt.Limitation = joinLimitation(receipt.Limitation, "cleanup: "+err.Error())
			}
		}
		return mcp.ProfileCaptureResult{Profile: prof, Receipt: receipt, Heatmap: heatmap, HeatmapSkip: heatmapSkip}, nil
	}
}

// mcpHeatmapMaxFunctions/mcpHeatmapMaxLines bound monitor_profile_capture's
// lines:true response to E3.6's payload budget (roughly 6 KB): top 3
// functions, top 8 lines each — by weight, never the WHOLE per-line
// breakdown a saved .cpuprofile/.pb.gz can carry.
const (
	mcpHeatmapMaxFunctions = 3
	mcpHeatmapMaxLines     = 8
)

// buildMCPLineHeatmap builds a bounded monitor.line_heatmap.v1 from a
// JUST-captured profile — the same profiler.BuildHeatmap `monitor hot`
// uses, via profilerSourceFromCapture (hot.go) — before buildProfileService
// discards the capture's raw text/temp file. A capture with no file:line
// detail at all (macOS `sample`; a CDP HEAP snapshot, a completely
// different wire shape than the CPU .cpuprofile LoadFile parses) degrades
// honestly to a HeatmapSkip instead of an empty or fabricated heatmap.
// Never touches or returns anything ws://-shaped: profiler.Profile carries
// no inspector websocket URL at all, only the already-captured .cpuprofile/
// pprof bytes.
func buildMCPLineHeatmap(ctx context.Context, prof profiler.Profile, binding *procbind.Binding) (*profiler.Heatmap, *mcp.HeatmapSkip) {
	src, err := profilerSourceFromCapture(prof)
	if err != nil {
		return nil, &mcp.HeatmapSkip{
			Detail:   err.Error(),
			Recovery: "retry with type:cpu, type:heap, or type:goroutine on a Node/Deno --inspect or Go pprof target — type:sample carries no file:line detail to build a line heatmap from",
		}
	}
	runtime := ""
	if binding != nil {
		runtime = string(binding.Runtime)
	}
	heatType := profiler.HeatCPU
	switch prof.Type {
	case profiler.ProfileHeap:
		heatType = profiler.HeatHeapInuse
	case profiler.ProfileGoroutine:
		heatType = profiler.HeatGoroutine
	}
	hm, buildErr := profiler.BuildHeatmap(ctx, src, profiler.HeatOptions{
		Top:         mcpHeatmapMaxFunctions,
		ProfileType: heatType,
		Runtime:     runtime,
		NoReadCode:  true, // payload diet: line numbers/percentages answer "which line", not the source text itself
	})
	if buildErr != nil {
		return nil, &mcp.HeatmapSkip{Detail: buildErr.Error()}
	}
	boundHeatmapLines(hm, mcpHeatmapMaxLines)
	return hm, nil
}

// boundHeatmapLines trims each function's Lines to its top maxLines by
// weight (Self desc, then Cum desc), re-sorted back to ascending Line order
// afterward — the same reading order every other Heatmap consumer
// (CodeFrame, --json) expects. A function with maxLines or fewer lines is
// left untouched.
func boundHeatmapLines(hm *profiler.Heatmap, maxLines int) {
	if hm == nil {
		return
	}
	for i := range hm.Functions {
		f := &hm.Functions[i]
		if len(f.Lines) <= maxLines {
			continue
		}
		trimmed := append([]profiler.HeatLine(nil), f.Lines...)
		sort.SliceStable(trimmed, func(a, b int) bool {
			if trimmed[a].Self != trimmed[b].Self {
				return trimmed[a].Self > trimmed[b].Self
			}
			return trimmed[a].Cum > trimmed[b].Cum
		})
		trimmed = trimmed[:maxLines]
		sort.Slice(trimmed, func(a, b int) bool { return trimmed[a].Line < trimmed[b].Line })
		f.Lines = trimmed
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

func getIssueForMCP(_ context.Context, id string, occurrenceLimit int) (issue issues.Issue, occurrences []issues.Occurrence, err error) {
	path, err := issues.ResolvePath("")
	if err != nil {
		return issue, nil, err
	}
	store, err := issues.OpenReadOnly(path)
	if err != nil {
		return issue, nil, err
	}
	defer func() {
		if closeErr := store.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	issue, err = store.Get(id)
	if err != nil {
		return issue, nil, err
	}
	occurrences, err = store.Occurrences(id, occurrenceLimit)
	return issue, occurrences, err
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
