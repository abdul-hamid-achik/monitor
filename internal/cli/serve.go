package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/analyzer"
	"github.com/abdul-hamid-achik/monitor/internal/appserver"
	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/config"
	"github.com/abdul-hamid-achik/monitor/internal/devrun"
	"github.com/abdul-hamid-achik/monitor/internal/explain"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
	"github.com/abdul-hamid-achik/monitor/internal/logger"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
)

// Bounds for the app server's list-shaped answers. A desktop client pages
// with filters; none of these is a reason to return the whole store.
const (
	appIssuesDefaultLimit      = 200
	appIssuesMaxLimit          = 1000
	appOccurrencesDefaultLimit = 50
	appOccurrencesMaxLimit     = 500
	appHistogramDefaultBuckets = 24
	appHistogramMaxSince       = 30 * 24 * time.Hour
	appLogsDefaultLimit        = 200
	appLogsMaxLimit            = 1000
	appHostDefaultProcesses    = 10
	appProfileDefaultDuration  = 5 * time.Second
	appProfileMaxDuration      = 2 * time.Minute
)

func newServeCmd() *cobra.Command {
	var stdio, readOnly bool
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Serve monitor.app.v1 to a desktop client over stdio",
		Long: `serve speaks monitor.app.v1 — JSON-RPC 2.0, one message per line — on
stdin/stdout, for Monitor Desktop. The same command serves a local client
and a remote one: the app spawns it directly, or as
` + "`ssh <host> monitor serve --stdio`" + `, so SSH carries authentication and
encryption and nothing listens on a port.

The server lives as long as its client: EOF on stdin ends it. It is never
the only write path — every write goes through the same short-lived store
writers the CLI uses — and --read-only rejects every mutating method.
Destructive methods (process.kill, profile.capture) also require
"confirm": true. See docs/contracts/app-protocol-v1.md.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if !stdio {
				return fmt.Errorf("monitor serve speaks only --stdio for now: run `monitor serve --stdio` (locally, or as `ssh <host> monitor serve --stdio`)")
			}
			// stdout carries protocol messages and nothing else. Anything a
			// helper prints to os.Stdout would corrupt the stream, so the
			// protocol writer keeps the real stdout and every other writer
			// is pointed at stderr for the life of the session.
			protocolOut := cmd.OutOrStdout()
			if protocolOut == os.Stdout {
				os.Stdout = os.Stderr
				defer func() { os.Stdout = protocolOut.(*os.File) }()
			}
			ctx, cancel := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer cancel()
			// A writable session also records what monitor's own SDKs left
			// in the inbox, so the client's issue.event stream shows them.
			// The drainer is waited out before returning: exiting in the
			// middle of a store write is what must never happen.
			if !readOnly {
				drainCtx, stopDrain := context.WithCancel(ctx)
				done := make(chan struct{})
				go func() {
					defer close(done)
					drainInboxLoop(drainCtx)
				}()
				defer func() {
					stopDrain()
					<-done
				}()
			}
			srv := appserver.New(newAppService(), appserver.Options{Version: Version, ReadOnly: readOnly})
			return srv.Run(ctx, cmd.InOrStdin(), protocolOut)
		},
	}
	cmd.Flags().BoolVar(&stdio, "stdio", false, "speak monitor.app.v1 on stdin/stdout (required; the only transport)")
	cmd.Flags().BoolVar(&readOnly, "read-only", false, "reject every mutating method (issues.set_status, process.kill, profile.capture)")
	return cmd
}

// serveInboxPoll is how often a writable serve session drains the SDK
// inbox.
const serveInboxPoll = 2 * time.Second

// drainInboxLoop drains the SDK inbox into the default store until ctx
// ends (see drainInboxIntoDefaultStore for why only the default store).
func drainInboxLoop(ctx context.Context) {
	ticker := time.NewTicker(serveInboxPoll)
	defer ticker.Stop()
	for {
		drainInboxIntoDefaultStore(ctx, "")
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// newAppService wires appserver.Service to the same logic the CLI and MCP
// use. Every function opens its store for the one call and closes it.
func newAppService() *appserver.Service {
	host := newAppHost()
	return &appserver.Service{
		IssuesList:       appIssuesList,
		IssueGet:         appIssueGet,
		IssueOccurrences: appIssueOccurrences,
		IssuesHistogram:  appIssuesHistogram,
		Projects:         appProjects,
		IssuesSetStatus:  appIssuesSetStatus,
		IssueDigests:     appIssueDigests,
		IssuesStorePath:  func() (string, error) { return issues.ResolvePath("") },

		HostSnapshot: host.snapshot,
		HostStream:   host.stream,
		Processes:    host.processes,
		Kill:         appKill,

		ProfileCapture: appProfileCapture,
		HeatmapFile:    appHeatmapFile,

		Launches:  appLaunches,
		Logs:      appLogs,
		Incidents: appIncidents,
		Doctor:    appDoctor,
	}
}

// invalidParams wraps a validation failure in the protocol's own code.
func invalidParams(format string, args ...any) error {
	return appserver.NewError(appserver.CodeInvalidParams, fmt.Sprintf(format, args...), nil)
}

// openAppIssues opens the issue store read-only. ok is false (with a nil
// error) when no store exists yet: an empty answer, not a failure.
func openAppIssues() (store *issues.Store, ok bool, err error) {
	path, err := issues.ResolvePath("")
	if err != nil {
		return nil, false, err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		return nil, false, nil
	}
	store, err = issues.OpenReadOnly(path)
	if err != nil {
		return nil, false, err
	}
	return store, true, nil
}

// newAppScrubber builds the defense-in-depth scrubber every app answer
// carrying process text goes through (the same construction MCP uses, with
// this process's secret env values; SEC-2).
func newAppScrubber() *scrub.Scrubber {
	return scrub.New(scrub.WithValues(scrub.SecretEnvValues(os.Environ(), nil)))
}

func scrubIssue(s *scrub.Scrubber, issue issues.Issue) issues.Issue {
	issue.Title = s.String(issue.Title)
	issue.Message = s.String(issue.Message)
	if issue.LatestException != nil {
		ex := *issue.LatestException
		ex.Value = s.String(ex.Value)
		issue.LatestException = &ex
	}
	return issue
}

// resolveAppIssueID resolves an id, short id or prefix, mapping the
// outcomes a client handles differently onto their own codes.
func resolveAppIssueID(store *issues.Store, raw string) (string, error) {
	id, err := store.ResolveID(raw)
	if err == nil {
		return id, nil
	}
	var ambiguous *issues.AmbiguousIDError
	switch {
	case errors.As(err, &ambiguous):
		return "", appserver.NewError(appserver.CodeAmbiguous, err.Error(), map[string]any{"candidates": ambiguous.Matches})
	case errors.Is(err, issues.ErrIssueNotFound):
		return "", appserver.NewError(appserver.CodeNotFound, err.Error(), nil)
	}
	return "", err
}

func appIssuesList(_ context.Context, p appserver.IssuesListParams) (appserver.IssuesListResult, error) {
	result := appserver.IssuesListResult{Items: []issues.Issue{}}
	now := time.Now()
	since, err := issues.ParseWindowBound(p.Since, now)
	if err != nil {
		return result, invalidParams("since: %v", err)
	}
	until, err := issues.ParseWindowBound(p.Until, now)
	if err != nil {
		return result, invalidParams("until: %v", err)
	}
	statuses, err := parseIssueStatuses(p.Statuses)
	if err != nil {
		return result, invalidParams("%v", err)
	}
	limit := p.Limit
	switch {
	case limit <= 0:
		limit = appIssuesDefaultLimit
	case limit > appIssuesMaxLimit:
		limit = appIssuesMaxLimit
	}
	store, ok, err := openAppIssues()
	if err != nil || !ok {
		return result, err
	}
	items, err := store.List(issues.ListOptions{
		Statuses: statuses, Project: p.Project, Service: p.Service, Kind: p.Kind,
		Release: p.Release, RunID: p.RunID, Since: since, Until: until,
	})
	if closeErr := store.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		if strings.Contains(err.Error(), "invalid") {
			return result, invalidParams("%v", err)
		}
		return result, err
	}
	// Scrub before matching Query, so a search can never find text the
	// answer itself would have redacted.
	s := newAppScrubber()
	query := strings.ToLower(strings.TrimSpace(p.Query))
	matched := make([]issues.Issue, 0, len(items))
	for _, issue := range items {
		issue = scrubIssue(s, issue)
		if query != "" && !issueMatchesQuery(issue, query) {
			continue
		}
		matched = append(matched, issue)
	}
	result.Total = len(matched)
	if len(matched) > limit {
		matched = matched[:limit]
		result.Truncated = true
	}
	result.Items = matched
	result.Privacy.Scrubbed = s.Count()
	return result, nil
}

// issueMatchesQuery is the inbox search: a case-insensitive substring of the
// id, title, message, exception type, or culprit function/file.
func issueMatchesQuery(issue issues.Issue, query string) bool {
	fields := []string{issue.ID, issue.Title, issue.Message, issue.ExceptionType, issue.Project, issue.Service}
	if issue.Culprit != nil {
		fields = append(fields, issue.Culprit.Function, issue.Culprit.File)
	}
	for _, f := range fields {
		if strings.Contains(strings.ToLower(f), query) {
			return true
		}
	}
	return false
}

func appIssueGet(ctx context.Context, p appserver.IssueGetParams) (result appserver.IssueGetResult, err error) {
	budget := explain.BudgetFull
	switch explain.Budget(p.Budget) {
	case "":
	case explain.BudgetBrief, explain.BudgetStandard, explain.BudgetFull:
		budget = explain.Budget(p.Budget)
	default:
		return result, invalidParams("budget must be brief, standard or full, got %q", p.Budget)
	}
	store, ok, err := openAppIssues()
	if err != nil {
		return result, err
	}
	if !ok {
		return appserver.IssueGetResult{NotFound: true, Recovery: "no issue store yet: run a command under `monitor run -- <cmd>` to record one"}, nil
	}
	defer func() {
		if closeErr := store.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	opts := explain.Options{Budget: budget, Redact: true}
	var id string
	if strings.EqualFold(strings.TrimSpace(p.ID), "latest") {
		latest, resolved, found, latestErr := explain.ResolveLatest(store, explain.LatestFilter{
			Project: p.Project, Service: p.Service, Kind: p.Kind,
		})
		if latestErr != nil {
			return result, latestErr
		}
		if !found {
			return appserver.IssueGetResult{NotFound: true, Recovery: recoveryHintForLatestNoMatch(resolved)}, nil
		}
		id = latest.ID
		opts.ResolvedFrom = &resolved
	} else {
		id, err = resolveAppIssueID(store, p.ID)
		if err != nil {
			return result, err
		}
	}
	built, err := explain.Build(ctx, store, id, opts)
	if err != nil {
		return result, err
	}
	result.Context = built
	if issue, getErr := store.Get(id); getErr == nil {
		result.Root = explain.RecordedRoot(issue)
	}
	if p.Markdown {
		result.Markdown = built.RenderMarkdown()
	}
	return result, nil
}

func appIssueOccurrences(_ context.Context, p appserver.OccurrencesParams) (result appserver.OccurrencesResult, err error) {
	result.Items = []issues.Occurrence{}
	limit := p.Limit
	switch {
	case limit <= 0:
		limit = appOccurrencesDefaultLimit
	case limit > appOccurrencesMaxLimit:
		limit = appOccurrencesMaxLimit
	}
	store, ok, err := openAppIssues()
	if err != nil {
		return result, err
	}
	if !ok {
		return result, appserver.NewError(appserver.CodeNotFound, "no issue store yet", nil)
	}
	defer func() {
		if closeErr := store.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	id, err := resolveAppIssueID(store, p.ID)
	if err != nil {
		return result, err
	}
	issue, err := store.Get(id)
	if err != nil {
		return result, err
	}
	all, err := store.Occurrences(id, 0)
	if err != nil {
		return result, err
	}
	result.Total = int64(len(all))
	if len(all) > limit {
		all = all[:limit]
		result.Truncated = true
	}
	_, scrubbed, count := scrubIssueForResponse(issue, all)
	result.Items = scrubbed
	result.Privacy.Scrubbed = count
	return result, nil
}

func appIssuesHistogram(_ context.Context, p appserver.HistogramParams) (result appserver.HistogramResult, err error) {
	window := 24 * time.Hour
	if strings.TrimSpace(p.Since) != "" {
		window, err = time.ParseDuration(strings.TrimSpace(p.Since))
		if err != nil {
			return result, invalidParams("since must be a duration like 24h: %v", err)
		}
	}
	if window <= 0 || window > appHistogramMaxSince {
		return result, invalidParams("since must be between 0 and %s", appHistogramMaxSince)
	}
	n := p.Buckets
	switch {
	case n <= 0:
		n = appHistogramDefaultBuckets
	case n > issues.MaxHistogramBuckets:
		n = issues.MaxHistogramBuckets
	}
	bucket := window / time.Duration(n)
	if bucket < time.Second {
		return result, invalidParams("since/buckets gives buckets under one second")
	}
	end := time.Now().Truncate(bucket).Add(bucket)
	start := end.Add(-time.Duration(n) * bucket)
	result = appserver.HistogramResult{
		Start: start.UTC(), BucketSeconds: int64(bucket / time.Second), Buckets: n, Series: map[string][]int64{},
		Note: "counts cover retained occurrences only: the store keeps a bounded number of occurrence rows, so this is recent activity, not a lifetime total",
	}
	store, ok, err := openAppIssues()
	if err != nil || !ok {
		return result, err
	}
	points, err := store.OccurrencePoints(start, p.IDs)
	if closeErr := store.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return result, err
	}
	result.Series = issues.Histogram(points, start, bucket, n)
	for _, id := range p.IDs {
		if _, has := result.Series[id]; !has {
			result.Series[id] = make([]int64, n)
		}
	}
	return result, nil
}

func appProjects(_ context.Context) (appserver.ProjectsResult, error) {
	result := appserver.ProjectsResult{Projects: []appserver.ProjectSummary{}}
	store, ok, err := openAppIssues()
	if err != nil || !ok {
		return result, err
	}
	items, err := store.List(issues.ListOptions{})
	if closeErr := store.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return result, err
	}
	byProject := map[string]*appserver.ProjectSummary{}
	services := map[string]map[string]bool{}
	for _, issue := range items {
		name := issue.Project
		sum, ok := byProject[name]
		if !ok {
			sum = &appserver.ProjectSummary{Project: name}
			byProject[name] = sum
			services[name] = map[string]bool{}
		}
		sum.Total++
		if issue.Status == issues.StatusOpen {
			sum.Open++
		}
		if issue.LastSeen.After(sum.LastSeen) || sum.Root == "" {
			if issue.LastSeen.After(sum.LastSeen) {
				sum.LastSeen = issue.LastSeen
			}
			if root := explain.RecordedRoot(issue); root != "" {
				sum.Root = root
			}
		}
		if issue.Service != "" && !services[name][issue.Service] {
			services[name][issue.Service] = true
			sum.Services = append(sum.Services, issue.Service)
		}
	}
	for _, sum := range byProject {
		sort.Strings(sum.Services)
		if sum.Services == nil {
			sum.Services = []string{}
		}
		result.Projects = append(result.Projects, *sum)
	}
	sort.Slice(result.Projects, func(i, j int) bool {
		a, b := result.Projects[i], result.Projects[j]
		if !a.LastSeen.Equal(b.LastSeen) {
			return a.LastSeen.After(b.LastSeen)
		}
		return a.Project < b.Project
	})
	return result, nil
}

func appIssuesSetStatus(ctx context.Context, p appserver.SetStatusParams) (appserver.SetStatusResult, error) {
	result := appserver.SetStatusResult{Updated: []issues.Issue{}}
	path, err := issues.ResolvePath("")
	if err != nil {
		return result, err
	}
	s := newAppScrubber()
	err = issues.WithWriter(ctx, path, issues.DefaultWriterWait, func(store *issues.Store) error {
		for _, raw := range p.IDs {
			id, resolveErr := store.ResolveID(raw)
			if resolveErr != nil {
				result.Failed = append(result.Failed, appserver.SetStatusFailed{ID: raw, Error: resolveErr.Error()})
				continue
			}
			var (
				updated issues.Issue
				setErr  error
			)
			switch issues.Status(p.Status) {
			case issues.StatusResolved:
				updated, setErr = store.Resolve(id)
			case issues.StatusIgnored:
				updated, setErr = store.Ignore(id)
			default:
				updated, setErr = store.Reopen(id)
			}
			if setErr != nil {
				result.Failed = append(result.Failed, appserver.SetStatusFailed{ID: raw, Error: setErr.Error()})
				continue
			}
			result.Updated = append(result.Updated, scrubIssue(s, updated))
		}
		return nil
	})
	return result, err
}

func appIssueDigests(_ context.Context) ([]appserver.IssueDigest, error) {
	store, ok, err := openAppIssues()
	if err != nil {
		return nil, err
	}
	if !ok {
		return []appserver.IssueDigest{}, nil
	}
	items, err := store.List(issues.ListOptions{})
	if closeErr := store.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}
	s := newAppScrubber()
	out := make([]appserver.IssueDigest, 0, len(items))
	for _, issue := range items {
		out = append(out, appserver.IssueDigest{
			ID: issue.ID, ShortID: issueShortID(issue.ID), Title: s.String(issue.Title),
			Project: issue.Project, Service: issue.Service, Kind: issue.Kind, Status: string(issue.Status),
			ExceptionType: issue.ExceptionType, Culprit: issue.Culprit,
			OccurrenceCount: issue.OccurrenceCount, ReopenedCount: issue.ReopenedCount,
			LastSeen: issue.LastSeen, Level: issue.Level, Handled: issue.Handled,
		})
	}
	return out, nil
}

// appHost owns the session's one collector. Collect must never run
// concurrently on a Collector (see collector.Collect), and the host stream
// and an on-demand host.snapshot can overlap, so every Collect goes through
// mu — the same arrangement `monitor mcp serve` uses.
type appHost struct {
	c      *collector.Collector
	mu     sync.Mutex
	warmed bool
}

func newAppHost() *appHost {
	return &appHost{c: NewCollector(0)}
}

// collect returns a full sample. The first one waits a collection interval
// for a second observation, since process CPU needs two samples.
func (h *appHost) collect(ctx context.Context) collector.SystemInfo {
	h.mu.Lock()
	defer h.mu.Unlock()
	first := h.c.Collect(ctx)
	if h.warmed {
		return first
	}
	timer := time.NewTimer(collector.MinFullCollectionInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return first
	case <-timer.C:
		h.warmed = true
		return h.c.Collect(ctx)
	}
}

func hostTick(info collector.SystemInfo, p appserver.HostParams) appserver.HostTick {
	limit := p.ProcessLimit
	if limit <= 0 {
		limit = appHostDefaultProcesses
	}
	perCore := append([]float64(nil), info.CPU.PerCoreUsage...)
	if perCore == nil {
		perCore = []float64{}
	}
	return appserver.HostTick{
		Snapshot: collector.BuildCompactSnapshot(info, collector.CompactOptions{
			ProcessLimit: limit, ProcessFilter: p.ProcessFilter,
		}),
		PerCoreUsage: perCore,
		LoadAvg:      [3]float64{info.CPU.LoadAvg1, info.CPU.LoadAvg5, info.CPU.LoadAvg15},
	}
}

func (h *appHost) snapshot(ctx context.Context, p appserver.HostParams) (appserver.HostTick, error) {
	return hostTick(h.collect(ctx), p), nil
}

// stream samples every interval and runs the same rule engine `monitor
// watch` runs (analyzer.NewDefaultEngine), gating repeats through watch's
// own cooldown so a sustained condition alerts once a minute, not once a
// second.
func (h *appHost) stream(ctx context.Context, p appserver.HostParams, interval time.Duration, emit func(appserver.HostTick)) error {
	settings, err := config.Load()
	if err != nil || settings == nil {
		settings = config.Default()
	}
	engine := analyzer.NewDefaultEngine(*settings)
	gate := newAlertCooldownGate(defaultAlertCooldown)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		info := h.collect(ctx)
		if ctx.Err() != nil {
			return nil
		}
		tick := hostTick(info, p)
		for _, a := range engine.Observe(collector.Event{
			Timestamp: info.LastUpdate, Hostname: info.Hostname, CPU: info.CPU, Memory: info.Memory,
			Network: info.Network, Disk: info.Disk, Processes: info.Processes,
		}) {
			if gate.allow(a, info.LastUpdate) {
				tick.Alerts = append(tick.Alerts, a)
			}
		}
		emit(tick)
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func (h *appHost) processes(ctx context.Context, p appserver.ProcessesParams) (any, error) {
	info := h.collect(ctx)
	opts := processListOptions{Limit: p.Limit, Sort: p.Sort, Filter: p.Filter, IncludeSystem: p.IncludeSystem}
	if opts.Limit <= 0 {
		opts.Limit = 200
	}
	if opts.Sort == "" {
		opts.Sort = "cpu"
	}
	list, err := buildProcessList(info.Processes, info.ProcessesState, opts)
	if err != nil {
		return nil, invalidParams("%v", err)
	}
	return list, nil
}

// appKill refuses protected and system processes outright — confirm never
// overrides that, same as monitor_kill — then signals and verifies.
func appKill(_ context.Context, p appserver.KillParams) (any, error) {
	conf := kill.CheckSafety([]int32{p.PID})
	if conf.HasProtected || conf.HasSystem {
		return nil, appserver.NewError(appserver.CodeRefused,
			"refused: the target is a protected or system-owned process; monitor never terminates it", conf)
	}
	res, err := kill.KillVerified(p.PID, p.Force)
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"killed":    res.Outcome == kill.OutcomeTerminated,
		"outcome":   string(res.Outcome),
		"signal":    res.Signal,
		"waited_ms": res.WaitedMs,
		"pid":       p.PID,
		"force":     p.Force,
	}
	if res.NextAction != "" {
		payload["next_action"] = res.NextAction
	}
	return payload, nil
}

// appProfileCapture is `monitor hot <pid>` for the app: the same
// captureLiveHeat core, answered as a full monitor.line_heatmap.v1 with
// source lines (the code is read on the host being profiled, so a remote
// session shows the remote checkout's code).
func appProfileCapture(ctx context.Context, p appserver.ProfileParams) (appserver.ProfileResult, error) {
	var result appserver.ProfileResult
	heatType, err := parseHotType(p.Type)
	if err != nil {
		return result, invalidParams("%v", err)
	}
	duration := appProfileDefaultDuration
	if p.DurationSecs > 0 {
		duration = time.Duration(p.DurationSecs) * time.Second
	}
	if duration > appProfileMaxDuration {
		return result, invalidParams("duration_seconds must be at most %d", int(appProfileMaxDuration/time.Second))
	}
	pprofAddr := strings.TrimSpace(p.PprofAddr)
	live, err := captureLiveHeat(ctx, p.PID, liveHeatOptions{
		Func: p.Func, PprofAddr: firstNonEmpty(pprofAddr, "localhost:6060"), AddrExplicit: pprofAddr != "",
		HeatType: heatType, TypeLabel: firstNonEmpty(p.Type, "cpu"), Duration: duration,
	})
	if err != nil {
		var ambiguous *procbind.AmbiguousLeafError
		var capErr *liveHeatError
		switch {
		case errors.As(err, &ambiguous):
			return result, appserver.NewError(appserver.CodeAmbiguous, err.Error(), map[string]any{"candidates": live.Candidates})
		case errors.As(err, &capErr):
			return result, appserver.NewError(appserver.CodeUnavailable, capErr.Limitation, map[string]string{
				"status": capErr.Status, "limitation": capErr.Limitation, "recovery": capErr.Recovery,
			})
		}
		return result, err
	}
	defer live.Close()
	return appserver.ProfileResult{
		Heatmap: live.Heatmap,
		Method:  live.Method,
		Target: appserver.ProfileTarget{
			PID: live.Binding.PID, Name: live.Binding.Name, Runtime: string(live.Binding.Runtime),
			CodebaseRoot: live.Binding.CodebaseRoot, MainScript: live.Binding.MainScript,
		},
	}, nil
}

// appHeatmapFile is `monitor hot --file` for the app. The path must be
// absolute: the client got it from the user (a file dialog or a drop).
func appHeatmapFile(ctx context.Context, p appserver.HeatmapFileParams) (*profiler.Heatmap, error) {
	path := strings.TrimSpace(p.Path)
	if !filepath.IsAbs(path) {
		return nil, invalidParams("path must be absolute, got %q", p.Path)
	}
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, appserver.NewError(appserver.CodeNotFound, "no such file: "+path, nil)
		}
		return nil, err
	}
	heatType, err := parseHotType(p.Type)
	if err != nil {
		return nil, invalidParams("%v", err)
	}
	src, err := profiler.LoadFile(path)
	if err != nil {
		return nil, invalidParams("%v", err)
	}
	if src.Kind == profiler.SourceCDP && heatType != profiler.HeatCPU {
		return nil, invalidParams("type %s only applies to a pprof profile; %s is a V8/Bun .cpuprofile (always cpu)", p.Type, path)
	}
	hm, err := profiler.BuildHeatmap(ctx, src, profiler.HeatOptions{Func: p.Func, Top: p.Top, ProfileType: heatType})
	if err != nil {
		return nil, err
	}
	if warn := applyIssueOverlay(hm, resolveHeatProjectSlug(filepath.Dir(path), "")); warn != "" {
		hm.Warnings = append(hm.Warnings, warn)
	}
	return hm, nil
}

// appLaunches lists `monitor run --name` launches with inspector PORTS
// only: the registry file's ws:// URL never leaves this process (naming
// ADR §8).
func appLaunches(_ context.Context) (appserver.LaunchesResult, error) {
	result := appserver.LaunchesResult{Launches: []appserver.LaunchInfo{}}
	for _, l := range devrun.ListRegistryEntries() {
		info := appserver.LaunchInfo{
			LaunchID: l.Entry.LaunchID, Name: l.Entry.Name, Project: l.Entry.Project, PID: l.Entry.PID,
			StartedAt: l.Entry.StartedAt, Scan: l.Entry.Scan, Alive: l.Alive,
		}
		for _, in := range l.Entry.Inspectors {
			if in.Port > 0 {
				info.InspectorPorts = append(info.InspectorPorts, in.Port)
			}
		}
		result.Launches = append(result.Launches, info)
	}
	return result, nil
}

func appLogs(_ context.Context, p appserver.LogsParams) (appserver.LogsResult, error) {
	result := appserver.LogsResult{Entries: []logger.Entry{}}
	limit := p.Limit
	switch {
	case limit <= 0:
		limit = appLogsDefaultLimit
	case limit > appLogsMaxLimit:
		limit = appLogsMaxLimit
	}
	for _, level := range p.Levels {
		if !validLogLevels[strings.ToLower(strings.TrimSpace(level))] {
			return result, invalidParams("invalid level %q", level)
		}
	}
	path, err := logger.ResolvePath("")
	if err != nil {
		return result, err
	}
	if _, statErr := os.Stat(path); errors.Is(statErr, os.ErrNotExist) {
		return result, nil
	}
	store, err := logger.OpenReadOnly(path)
	if err != nil {
		return result, fmt.Errorf("open log store: %w", err)
	}
	opts := logger.SearchOptions{Query: p.Query, Limit: limit, Levels: p.Levels, Process: p.Process, PID: p.PID}
	if p.SinceSeconds > 0 {
		opts.Since = time.Now().Add(-time.Duration(p.SinceSeconds) * time.Second)
	}
	entries, err := store.SearchWithOptions(opts)
	if combined := errors.Join(err, store.Close()); combined != nil {
		return result, combined
	}
	s := newAppScrubber()
	for i := range entries {
		entries[i].Message = s.String(entries[i].Message)
		entries[i].Raw = s.String(entries[i].Raw)
	}
	if entries != nil {
		result.Entries = entries
	}
	result.Privacy.Scrubbed = s.Count()
	return result, nil
}

// appDoctor is `monitor doctor --json`, with code_intel probed against the
// project checkout the client names (absolute, existing) rather than the
// server's own working directory.
func appDoctor(ctx context.Context, p appserver.DoctorParams) (any, error) {
	dir := strings.TrimSpace(p.Dir)
	if dir == "" {
		return buildDoctorReport(ctx), nil
	}
	if !filepath.IsAbs(dir) {
		return nil, invalidParams("dir must be absolute, got %q", p.Dir)
	}
	if fi, err := os.Stat(dir); err != nil || !fi.IsDir() {
		return nil, appserver.NewError(appserver.CodeNotFound, "no such directory on this host: "+dir, nil)
	}
	return buildDoctorReportIn(ctx, dir), nil
}

// appIncidents lists archived incident stashes (through fcheap) and the
// local bundles still waiting for archival. A missing or failing fcheap is
// reported beside the pending list instead of failing the whole answer.
func appIncidents(ctx context.Context) (any, error) {
	out := map[string]any{"stashes": []any{}, "pending": []any{}}
	if stashes, err := incidents.Search(ctx, nil); err != nil {
		out["stash_error"] = err.Error()
	} else if stashes != nil {
		out["stashes"] = stashes
	}
	pending, err := incidents.ListRegistry()
	if err != nil {
		out["pending_error"] = err.Error()
	} else if pending != nil {
		out["pending"] = pending
	}
	return out, nil
}
