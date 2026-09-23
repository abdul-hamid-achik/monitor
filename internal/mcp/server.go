// Package mcp exposes the monitor's data through a Model Context Protocol
// stdio server, following the codemap MCP server pattern (one Server struct,
// one Service, typed inputs, NL-JSON-RPC framing).
//
// Read-only tools:
//
//	monitor_snapshot        full SystemInfo
//	monitor_processes       top processes
//	monitor_doctor          ecosystem health
//	monitor_analyze         sample a short window, run diagnosis rules, return findings
//	monitor_issues          list recurring local issues (crashes/exceptions/alerts)
//	monitor_issue           "why did it fail?" in one call: id, an unambiguous
//	                        short_id/prefix, or "latest" (filtered by project/
//	                        service/kind) -> monitor.issue_context.v1
//
// Mutating tools (require explicit `confirm: true` in the typed input):
//
//	monitor_kill            safely terminate a process (uses internal/kill)
//	monitor_profile_capture capture a heap/cpu/goroutine/sample profile
//	monitor_investigate     run the diagnostic pipeline for a process
//	monitor_record          capture a whole-screen recording via the platform
//	                        recorder (screencapture/ffmpeg) for vidtrace to analyze
//
// All mutating tools refuse to run without the confirm flag set. This is an
// MCP-side safety gate so an agent must explicitly assert intent before
// anything changes on the host. (On the CLI only `monitor kill` has a `--yes`
// gate; `monitor profile`/`investigate` run ungated — the MCP surface is
// deliberately stricter.)
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/explain"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

// nowRFC3339 returns the current time formatted as RFC3339Nano. Pulled out
// so the stub fallback in handleInvestigate can match the CLI's output
// exactly without sprinkling time.Now() across handlers.
func nowRFC3339() string {
	return time.Now().Format(time.RFC3339Nano)
}

// AnalyzeResult is what the Analyze service returns: how many samples the
// window produced, the diagnoses derived from them, and the raw rule alerts
// the window's analyzer engine raised (bug 17: before Alerts existed, the
// engine's rules — the same CPUSpike/RSSGrowth/DiskFill/SwapPressure/
// Zombie/Threshold set `monitor watch` runs, via analyzer.NewDefaultEngine —
// ran on every sample but their findings were discarded; only the separate
// cross-signal Diagnoses table was ever returned). Alerts is additive and
// omitted when empty, so existing JSON consumers are unaffected.
type AnalyzeResult struct {
	Samples   int                   `json:"samples"`
	Diagnoses []collector.Diagnosis `json:"diagnoses"`
	Alerts    []collector.Alert     `json:"alerts,omitempty"`
}

// Service is the dependency the MCP server wraps. Each field is a thin
// function so the CLI can wire in real implementations without coupling
// the mcp package to the concrete ones.
type Service struct {
	// Snapshots returns the latest SystemInfo. Required for read tools.
	Snapshots func() collector.SystemInfo

	// Analyze samples the system for windowSeconds seconds, runs the
	// analyzer's diagnosis engine over the window, and returns the findings.
	// pid == 0 means system-wide; a non-zero pid focuses on that PID only.
	// Used by monitor_analyze. Optional; if nil the tool reports unavailable.
	Analyze func(ctx context.Context, windowSeconds int, pid int32) (AnalyzeResult, error)

	// IssuesList and IssueContext expose the durable local issue index. Both
	// are read-only and should open a fresh shared-read snapshot per call.
	//
	// IssuesList takes the monitor_issues filters exactly as the MCP caller
	// supplied them (Since/Until unparsed strings) rather than a pre-built
	// issues.ListOptions: interpreting them -- issues.ParseWindowBound, the
	// same shared parser the CLI's --since/--until flags use -- is the
	// Service implementation's job (internal/cli/mcp.go's listIssuesForMCP),
	// not handleIssues'. This keeps the MCP handler a pure field copy with
	// zero business logic, matching every other tool's handler/Service split.
	IssuesList func(ctx context.Context, filter IssuesListFilter) ([]issues.Issue, error)

	// IssueContext is monitor_issue's implementation (E2.7): id resolves an
	// exact issue ID, an unambiguous short_id/prefix (issues.Store.
	// ResolveID), or the sentinel "latest" -- narrowed by filter.Project/
	// Service/Kind (kind defaults to "exception", so a watch alert or an
	// investigate run never silently wins "latest" over a real crash; see
	// internal/explain.LatestFilter's doc comment). occurrenceLimit bounds
	// the legacy Occurrences slice exactly like the pre-E2.7 IssueGet did.
	//
	// A (nil, nil) result means id/filter matched nothing -- an ordinary
	// outcome ("no exception open right now" for "latest", the same
	// scenario `monitor issue latest` also treats as a real but well-
	// explained CLI error), not a failure: handleIssue turns it into a
	// recovery hint instead of an error envelope. A non-nil error means id
	// was a concrete id/prefix that itself failed to resolve (not found, or
	// *issues.AmbiguousIDError).
	IssueContext func(ctx context.Context, id string, filter IssueContextFilter, occurrenceLimit int) (*IssueContextResult, error)

	// Kill terminates the given PID and returns the verified Result (outcome
	// terminated|still_running|unknown). force=true sends SIGKILL, otherwise
	// SIGTERM. Used by monitor_kill. Required for mutating kill tools.
	Kill func(pid int32, force bool) (kill.Result, error)

	// Profile captures a profile of the given PID, preferring the
	// runtime-appropriate mechanism (CDP for Node/Deno with --inspect,
	// honestly "unavailable" for Bun rather than a raw 404 — E1.7) over a
	// blind pprof scrape. pprofAddr, when non-empty, is the target's
	// net/http/pprof host:port AND asserts on the caller's behalf that it
	// belongs to pid, skipping the ownership proof — same escape hatch as
	// the CLI's --pprof-addr. keep mirrors the typed input's keep:true: the
	// implementation verifies the capture and, when keep is false, discards
	// its raw text/on-disk temp file BEFORE returning — that keep/discard
	// policy lives here (in the Service), not in handleProfileCapture,
	// matching how Investigate's own include_raw redaction lives in the
	// Service rather than the handler. The returned Receipt already
	// reflects the pre-discard artifact, so it stays a true report of what
	// was actually captured even after cleanup. Used by
	// monitor_profile_capture. Optional; if nil the tool reports
	// unavailable.
	//
	// An error satisfying errors.As(err, *UnavailableError) marks a capture
	// that will never succeed for a known reason (Bun's JSC inspector, not
	// V8 CDP) rather than an unexpected failure; handleProfileCapture
	// surfaces those as status:"unavailable" instead of a bare "error".
	Profile func(ctx context.Context, pid int32, ptype profiler.ProfileType, pprofAddr string, keep bool) (profiler.Profile, profiler.Receipt, error)

	// Investigate runs the diagnostic pipeline for the given PID. Used by
	// monitor_investigate. Optional; if nil the tool reports a stub result
	// matching the CLI's stub output so the surface stays stable.
	// opts carry codebase binding and Chalupa correlation IDs.
	Investigate func(ctx context.Context, pid int32, opts InvestigateOptions) map[string]any

	// Record starts a vidtrace recording for the given PID. Used by
	// monitor_record. Optional; if nil the tool reports vidtrace missing.
	Record func(ctx context.Context, pid int32, durationSeconds int) (string, error)
}

// IssuesListFilter carries monitor_issues' filters exactly as the MCP
// caller supplied them: Since/Until stay unparsed strings so handleIssues
// can be a pure field copy, and the Service implementation
// (internal/cli/mcp.go's listIssuesForMCP) is the single place -- alongside
// the CLI's --since/--until flags -- that calls issues.ParseWindowBound.
type IssuesListFilter struct {
	Statuses []issues.Status
	Project  string
	Service  string
	Since    string
	Until    string
	RunID    string
	Release  string
	Kind     string
}

// IssueContextFilter narrows Service.IssueContext's "latest" resolution
// (E2.7). It is ignored when id is a concrete id/prefix rather than
// "latest".
type IssueContextFilter struct {
	Project string
	Service string
	// Kind is exception (default) | alert | investigation | any -- see
	// internal/explain.LatestFilter.Kind's doc comment.
	Kind string
}

// IssueContextResult is Service.IssueContext's return: the bounded
// monitor.issue_context.v1 (brief budget), plus OPT-IN access to the legacy
// {issue, occurrences, occurrences_truncated} shape (the pre-E2.7 IssueGet)
// when the caller explicitly asked for occurrences. handleIssue is a pure
// field copy of this struct into the wire response -- every decision about
// WHAT to include lives here (the Service), never in the handler.
type IssueContextResult struct {
	// NotFound, when true, means id/filter (almost always "latest" under a
	// filter) matched nothing -- an ordinary outcome (E2.7: a healthy
	// project between crashes), not a failure. Recovery is the plain-
	// English hint handleIssue surfaces alongside it. Every other field is
	// zero when this is true.
	NotFound bool
	Recovery string

	// Context is the bounded monitor.issue_context.v1 at budget "brief"
	// (docs/contracts/issue-context-v1.md: MCP defaults to brief so a
	// "what's the latest crash" question stays a small, cheap call). Nil
	// only when explain.Build itself failed in some way Build's own
	// honest-degradation Degraded[] couldn't already absorb (e.g. the issue
	// vanished between resolving id and building).
	Context *explain.Context

	// IncludeLegacy, when true, means Issue/Occurrences/OccurrencesTruncated
	// below should be merged into the wire response (overwriting Context's
	// own small "issue" summary with the full issues.Issue) -- set only
	// when the caller explicitly requested occurrences (occurrence_limit >
	// 0), since neither the full Issue nor N raw Occurrences carry any size
	// bound of their own. false is the default, bounded response.
	IncludeLegacy        bool
	Issue                issues.Issue
	Occurrences          []issues.Occurrence
	OccurrencesTruncated bool
}

// InvestigateOptions is the optional input for Service.Investigate beyond pid.
type InvestigateOptions struct {
	TTL          string
	NoSave       bool
	Codebase     string
	Environment  string
	DeploymentID string
	RunID        string
	StepID       string
	Suite        string
	Attempt      string
	Release      string
	Service      string
	GitSHA       string
	// IncludeRaw keeps the captured profile's raw text (a CDP CPU
	// profile's full JSON, or a pprof capture's text dump) in the
	// returned report. Default false: the E1.7 payload diet omits it to
	// keep monitor_investigate's response small (an investigate report
	// against a real Node target ran ~36KB before this, mostly raw CDP
	// JSON).
	IncludeRaw bool
}

// Server wraps the MCP stdio transport.
type Server struct {
	svc *Service
	srv *mcp.Server
}

// NewServer creates an MCP server exposing monitor's read-only and mutating
// surface. Mutating tools are registered unconditionally; they fail at call
// time with a clear "confirm required" error if the agent omits the confirm
// flag. version is reported in the MCP handshake and should be the build
// version injected by the CLI (goreleaser's ldflags), not a hardcoded string.
func NewServer(svc *Service, version string) *Server {
	s := &Server{svc: svc}
	impl := &mcp.Implementation{Name: "monitor", Version: version}
	opts := &mcp.ServerOptions{
		Instructions: "monitor is an agent-harnessable local observability tool. " +
			"Call monitor_snapshot first to orient, then drill down with monitor_processes " +
			"or monitor_doctor. When the user says crash, exception, traceback, panic, error, " +
			"\"which line\", \"why did it fail\", or asks about a stack trace, call monitor_issue " +
			"{id:\"latest\"} (optionally with project/service/kind) FIRST -- it answers with the " +
			"culprit file:line, causes, impact, and last-touched commit in one call; use " +
			"monitor_issues only to list/triage several issues or find a specific id first. When the " +
			"user reports slowness, a suspected leak, or a runaway process, call monitor_analyze " +
			"(read-only, no confirm; it blocks for window_seconds while sampling). All tools return " +
			"JSON. Mutating tools (monitor_kill, monitor_profile_capture, monitor_investigate, " +
			"monitor_record) require the typed 'confirm: true' field in their input before they will " +
			"run. confirm:true is necessary but not sufficient for monitor_kill: it still refuses " +
			"protected or system-owned processes and returns {killed:false, refused:true, reason}.",
	}
	s.srv = mcp.NewServer(impl, opts)
	s.register()
	return s
}

// Run starts the server on stdio.
func (s *Server) Run(ctx context.Context) error {
	return s.srv.Run(ctx, &mcp.StdioTransport{})
}

// Connect wires the server onto an arbitrary MCP transport instead of the
// stdio one Run uses — an in-memory transport (mcp.NewInMemoryTransports),
// notably, so a caller that builds a REAL *Service the same way `monitor mcp
// serve` does (cli/mcp.go) can drive it with a real CallTool round trip in
// tests, without spawning the binary or duplicating that wiring as a stub.
// Mirrors the SDK's own Server.Connect signature; Run(ctx) (stdio) remains
// the production entry point cmd/monitor's `mcp serve` uses.
func (s *Server) Connect(ctx context.Context, t mcp.Transport) (*mcp.ServerSession, error) {
	return s.srv.Connect(ctx, t, nil)
}

func (s *Server) register() {
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_snapshot",
		Annotations: readOnlyAnnotations("System snapshot"),
		Description: "Return the latest SystemInfo with an interpreted 'summary' string " +
			"(memory/CPU/disk state, top consumer) and, when a threshold is near, 'next' " +
			"suggestions. Set compact:true for a bounded, history-free, schema-versioned payload " +
			"recommended for agent context; process_limit (default 5, max 25), process_filter, " +
			"filesystem_limit (default 10, max 50), and filesystem_filter narrow that view. " +
			"Without compact, the lossless raw metrics follow at the top level for compatibility.",
	}, s.handleSnapshot)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_processes",
		Annotations: readOnlyAnnotations("Top processes"),
		Description: "Return the top processes. Input: limit (default 15, max 200), " +
			"sort_by: 'cpu' (default) or 'rss', filter: case-insensitive substring on the process name. " +
			"Output: {processes, total, truncated, reason} — total counts matches before truncation, " +
			"truncated says the list was cut at limit, reason is top_cpu | top_rss | filtered.",
	}, s.handleProcesses)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_doctor",
		Description: "Ecosystem tool availability.",
		Annotations: readOnlyAnnotations("Ecosystem health"),
	},
		s.handleDoctor)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_analyze",
		Annotations: readOnlyAnnotations("Analyze system health"),
		Description: "Diagnose why the system is slow or unhealthy. Call this when the user says " +
			"\"something is slow\", the machine feels sluggish, a process seems stuck, or memory/CPU " +
			"looks wrong. Read-only and safe: there is NO confirm field. Samples metrics once per second " +
			"for window_seconds (default 10, min 4, max 60) and returns " +
			"diagnoses: [{summary, evidence, confidence, next_actions}] (cross-signal patterns like a " +
			"memory leak or CPU spin) and alerts: [{severity, rule, pid, process, detail}] (the plain " +
			"per-sample findings \u2014 cpu_spike, rss_growth, disk_fill, swap_pressure, zombie_process, and " +
			"threshold when configured \u2014 that `monitor watch` would also raise for the same window). Pass " +
			"pid to focus on one process. healthy:true with empty diagnoses AND alerts means nothing " +
			"anomalous was observed in the window; retry with a larger window_seconds before concluding " +
			"the system is fine.",
	}, s.handleAnalyze)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_issues",
		Annotations: readOnlyAnnotations("List local issues"),
		Description: "List recurring local issues (crashes, exceptions, tracebacks, panics, watch alerts), " +
			"newest first. Read-only; no confirm field. Filter by statuses (open|resolved|ignored), project, " +
			"service, since/until (RFC3339 or a duration like 10m/24h meaning that long ago), run_id, release, " +
			"or kind (exception|alert|investigation|any); limit defaults to 50 and is capped at 200. Each row's " +
			"culprit (when known) names the file:line that actually broke. For \"why did it fail\" on ONE " +
			"specific or the most recent failure, prefer monitor_issue over listing and picking a row by hand.",
	}, s.handleIssues)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_issue",
		Annotations: readOnlyAnnotations("Why did it fail?"),
		Description: "Answer \"why did it fail / crash / throw / panic\", \"what line broke\", or \"which " +
			"commit touched that\" in ONE call. id is an issue ID, an unambiguous short_id/prefix (as shown by " +
			"monitor_issues), or the literal \"latest\" for the most recently active issue -- optionally narrowed " +
			"by project/service/kind (kind defaults to \"exception\", so a watch alert like a cpu_spike, or an " +
			"investigate run, never silently wins \"latest\" over a real crash; pass kind:\"any\" to widen it). " +
			"When id/filter matches nothing, the response is not_found:true with a recovery hint (a normal, " +
			"expected outcome for a healthy project between crashes), never an error. Read-only; no confirm " +
			"field. Returns monitor.issue_context.v1 at the small, bounded \"brief\" budget (its own issue " +
			"summary, culprit with file:line/function/a snippet, causes, frames, impact, last_touched -- \"last " +
			"touched\", never a verdict of blame -- degraded, and next) as a single cheap call, usually under " +
			"4KB. Pass occurrence_limit > 0 (max 200) to ALSO get the full legacy {issue, occurrences, " +
			"occurrences_truncated} shape (overwriting the small issue summary with the richer one) when you " +
			"specifically need the raw occurrence history.",
	}, s.handleIssue)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_kill",
		Description: "Safely terminate a process. Requires `confirm: true` in the input. Use force=true for SIGKILL.",
		Annotations: mutatingAnnotations("Terminate process", true, false),
	}, s.handleKill)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "monitor_profile_capture",
		Description: "Capture a profile for a process. Requires `confirm: true`. type: heap|cpu|goroutine|sample. " +
			"Runtime-aware: Node/Deno with --inspect get a real CDP capture (file:line frames); Bun reports " +
			"unavailable with an honest reason instead of failing oddly (Bun speaks JSC, not CDP). " +
			"pprof_addr targets a non-default net/http/pprof port and asserts ownership. keep:true retains the " +
			"raw profile text and on-disk temp file; the default discards both after the call.",
		Annotations: mutatingAnnotations("Capture process profile", false, false),
	}, s.handleProfileCapture)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_investigate",
		Annotations: mutatingAnnotations("Investigate process", false, false),
		Description: "Run the diagnostic pipeline for a process: identify runtime/codebase " +
			"(Node cmdline/cwd/package.json), ownership-gated profile, codemap correlate, " +
			"vecgrep semantic hits, fcheap stash with ArtifactRefV1. Pass codebase when " +
			"auto-detect fails. Optional environment/deployment_id/run_id/step_id/suite/attempt/release tag the " +
			"incident for Chalupa CI. The response omits the captured profile's raw text by default (payload " +
			"diet); pass include_raw:true to keep it. Requires `confirm: true`.",
	}, s.handleInvestigate)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_record",
		Description: "Record the screen for N seconds (default 30) via the platform recorder (screencapture/ffmpeg); the result can be analyzed with vidtrace. Requires `confirm: true`.",
		Annotations: mutatingAnnotations("Record screen evidence", false, false),
	}, s.handleRecord)
}

// Tool annotations let MCP clients present risk and approval affordances
// without parsing prose. They remain hints: the typed confirm gate and the
// handler-level safety checks are the enforcement boundary.
func readOnlyAnnotations(title string) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           title,
		ReadOnlyHint:    true,
		DestructiveHint: boolRef(false),
		OpenWorldHint:   boolRef(false),
	}
}

func mutatingAnnotations(title string, destructive, idempotent bool) *mcp.ToolAnnotations {
	return &mcp.ToolAnnotations{
		Title:           title,
		DestructiveHint: boolRef(destructive),
		IdempotentHint:  idempotent,
		OpenWorldHint:   boolRef(false),
	}
}

func boolRef(value bool) *bool { return &value }

// -- Read-only input/handler types ----------------------------------------

type snapshotInput struct {
	Compact          bool   `json:"compact,omitempty"           jsonschema:"return the bounded schema-versioned agent view instead of full SystemInfo"`
	ProcessLimit     int    `json:"process_limit,omitempty"     jsonschema:"top CPU and memory processes in compact view (default 5, max 25)"`
	ProcessFilter    string `json:"process_filter,omitempty"    jsonschema:"case-insensitive process name substring in compact view"`
	FilesystemLimit  int    `json:"filesystem_limit,omitempty"  jsonschema:"filesystems in compact view (default 10, max 50)"`
	FilesystemFilter string `json:"filesystem_filter,omitempty" jsonschema:"case-insensitive device, mount, or filesystem substring in compact view"`
}

// processesInput is the typed input for monitor_processes.
type processesInput struct {
	Limit  int    `json:"limit,omitempty"   jsonschema:"maximum processes to return (default 15, max 200)"`
	SortBy string `json:"sort_by,omitempty" jsonschema:"sort order: cpu (default) or rss"`
	Filter string `json:"filter,omitempty"  jsonschema:"case-insensitive substring match on the process name"`
}

// doctorInput is the (empty) typed input for monitor_doctor. It exists so
// monitor_processes can grow fields without leaking them into the doctor
// tool's input schema.
type doctorInput struct{}

// analyzeInput is the typed input for monitor_analyze. Read-only: there is
// deliberately NO confirm field.
type analyzeInput struct {
	WindowSeconds int   `json:"window_seconds,omitempty" jsonschema:"sampling window in seconds (default 10, min 4, max 60)"`
	PID           int32 `json:"pid,omitempty"            jsonschema:"optional: focus the diagnosis on this PID only"`
}

type issuesInput struct {
	Statuses []string `json:"statuses,omitempty" jsonschema:"optional statuses: open, resolved, ignored"`
	Project  string   `json:"project,omitempty"  jsonschema:"case-insensitive project filter"`
	Service  string   `json:"service,omitempty"  jsonschema:"case-insensitive service filter"`
	// Since/Until/RunID/Release/Kind (E2.6) are passed through unparsed:
	// issues.ParseWindowBound and issues.Store.List (the domain/service
	// layer, not this handler) own interpreting them, so the CLI's
	// --since/--until/--run-id/--release/--kind flags and this tool always
	// mean exactly the same filter.
	Since   string `json:"since,omitempty"   jsonschema:"only issues active at/after this time: RFC3339, or a duration like 10m/24h meaning that long ago"`
	Until   string `json:"until,omitempty"   jsonschema:"only issues active at/before this time: RFC3339, or a duration like 10m/24h meaning that long ago"`
	RunID   string `json:"run_id,omitempty"  jsonschema:"filter by a run id the issue has seen"`
	Release string `json:"release,omitempty" jsonschema:"filter by a release the issue has seen"`
	Kind    string `json:"kind,omitempty"    jsonschema:"filter by kind: exception, alert, investigation, or any (default: any)"`
	Limit   int    `json:"limit,omitempty"    jsonschema:"maximum issues to return (default 50, max 200)"`
}

type issueInput struct {
	ID string `json:"id" jsonschema:"issue ID, an unambiguous short_id/prefix, or \"latest\" for the most recently active issue"`
	// Project/Service/Kind (E2.7) narrow "latest"; they are ignored when ID
	// is a concrete id/prefix.
	Project         string `json:"project,omitempty"         jsonschema:"restrict \"latest\" to this project (case-insensitive)"`
	Service         string `json:"service,omitempty"         jsonschema:"restrict \"latest\" to this service (case-insensitive)"`
	Kind            string `json:"kind,omitempty"            jsonschema:"restrict \"latest\" to this kind: exception (default), alert, investigation, or any"`
	OccurrenceLimit int    `json:"occurrence_limit,omitempty" jsonschema:"opt in to the legacy issue+occurrences shape by setting this > 0 (max 200); 0 (the default) omits it, keeping the response the small bounded brief"`
}

// processesOutput is the structured payload of monitor_processes.
type processesOutput struct {
	Processes []collector.ProcessInfo `json:"processes"`
	Total     int                     `json:"total"`     // matches before truncation
	Truncated bool                    `json:"truncated"` // len(Processes) < Total
	Reason    string                  `json:"reason"`    // top_cpu | top_rss | filtered
}

// snapshotPayload prepends the interpreted summary to the raw SystemInfo.
// Embedding keeps every SystemInfo field at the top level of the JSON, so
// existing consumers of monitor_snapshot (.hostname, .cpu, ...) are unbroken.
type snapshotPayload struct {
	Summary string   `json:"summary"`
	Next    []string `json:"next,omitempty"`
	collector.SystemInfo
}

// compactSnapshotPayload keeps the interpretation beside the bounded metrics,
// matching the full MCP response without reintroducing SystemInfo's histories.
type compactSnapshotPayload struct {
	Summary string   `json:"summary"`
	Next    []string `json:"next,omitempty"`
	collector.CompactSnapshot
}

const (
	defaultProcessLimit = 15
	maxProcessLimit     = 200

	defaultAnalyzeWindowSeconds = 10
	minAnalyzeWindowSeconds     = 4 // analyzer's diagMinSamples: fewer aligned samples -> no diagnosis
	maxAnalyzeWindowSeconds     = 60
	defaultIssuesLimit          = 50
	maxIssuesLimit              = 200
)

// killInput is the typed input for monitor_kill. The agent must set
// Confirm=true for the tool to act.
type killInput struct {
	PID     int32 `json:"pid"               jsonschema:"the PID to terminate"`
	Force   bool  `json:"force,omitempty"  jsonschema:"send SIGKILL instead of SIGTERM"`
	Confirm bool  `json:"confirm"           jsonschema:"must be true; confirms intent to terminate the process"`
}

// profileInput is the typed input for monitor_profile_capture.
type profileInput struct {
	PID       int32  `json:"pid"                  jsonschema:"the PID to profile"`
	Type      string `json:"type,omitempty"       jsonschema:"profile type: heap, cpu, goroutine, sample (default heap)"`
	PprofAddr string `json:"pprof_addr,omitempty" jsonschema:"loopback host:port (localhost/127.0.0.1 only) of the target's net/http/pprof server (Go heap/cpu/goroutine only); setting this asserts the endpoint belongs to pid and skips the ownership check, like the CLI's --pprof-addr — a non-loopback host is refused"`
	Keep      bool   `json:"keep,omitempty"       jsonschema:"keep the captured profile's raw text and on-disk temp file; default false discards both after the call so repeated captures don't leak /tmp/monitor-<type>-* files or bloat the response"`
	Confirm   bool   `json:"confirm"              jsonschema:"must be true; confirms intent to capture a profile"`
}

// investigateInput is the typed input for monitor_investigate.
type investigateInput struct {
	PID          int32  `json:"pid"                     jsonschema:"the PID to investigate"`
	Confirm      bool   `json:"confirm"                 jsonschema:"must be true; confirms intent to run the diagnostic pipeline"`
	Codebase     string `json:"codebase,omitempty"      jsonschema:"project root for codemap/vecgrep (auto-detected from process cwd when empty)"`
	Environment  string `json:"environment,omitempty"   jsonschema:"correlation environment id (Chalupa env / MONITOR_ENVIRONMENT)"`
	DeploymentID string `json:"deployment_id,omitempty" jsonschema:"correlation deployment id (CHALUPA_DEPLOYMENT_ID)"`
	RunID        string `json:"run_id,omitempty"        jsonschema:"correlation CI/run id"`
	StepID       string `json:"step_id,omitempty"       jsonschema:"correlation CI step id"`
	Suite        string `json:"suite,omitempty"         jsonschema:"correlation CI suite"`
	Attempt      string `json:"attempt,omitempty"       jsonschema:"correlation CI attempt"`
	Release      string `json:"release,omitempty"       jsonschema:"correlation release label"`
	Service      string `json:"service,omitempty"       jsonschema:"correlation service name"`
	GitSHA       string `json:"git_sha,omitempty"       jsonschema:"correlation git sha"`
	TTL          string `json:"ttl,omitempty"           jsonschema:"fcheap stash TTL (default 7d)"`
	NoSave       bool   `json:"no_save,omitempty"      jsonschema:"skip fcheap stash; return the profile's symbols/stats inline (pass include_raw:true for the raw capture text too)"`
	IncludeRaw   bool   `json:"include_raw,omitempty"  jsonschema:"keep the captured profile's raw text (CDP JSON / pprof dump) in the response; default false keeps the payload small"`
}

// recordInput is the typed input for monitor_record.
type recordInput struct {
	PID             int32 `json:"pid"               jsonschema:"PID for context/labeling only — the recorder captures the WHOLE screen, not just this process"`
	DurationSeconds int   `json:"duration,omitempty" jsonschema:"recording duration in seconds (default 30)"`
	Confirm         bool  `json:"confirm"           jsonschema:"must be true; confirms intent to start a screen recording"`
}

// -- Handlers ------------------------------------------------------------

func (s *Server) handleSnapshot(_ context.Context, _ *mcp.CallToolRequest, in *snapshotInput) (*mcp.CallToolResult, any, error) {
	if s.svc.Snapshots == nil {
		return result(map[string]any{"error": "snapshot service not configured"})
	}
	info := s.svc.Snapshots()
	summary, next := buildSnapshotSummary(info)
	if in != nil && in.Compact {
		compact := collector.BuildCompactSnapshot(info, collector.CompactOptions{
			ProcessLimit: in.ProcessLimit, ProcessFilter: in.ProcessFilter,
			FilesystemLimit: in.FilesystemLimit, FilesystemFilter: in.FilesystemFilter,
		})
		return result(compactSnapshotPayload{Summary: summary, Next: next, CompactSnapshot: compact})
	}
	return result(snapshotPayload{Summary: summary, Next: next, SystemInfo: info})
}

func (s *Server) handleProcesses(_ context.Context, _ *mcp.CallToolRequest, in *processesInput) (*mcp.CallToolResult, any, error) {
	if s.svc.Snapshots == nil {
		return result(map[string]any{"error": "snapshot service not configured"})
	}
	sortBy := in.SortBy
	switch sortBy {
	case "", "cpu":
		sortBy = "cpu"
	case "rss":
	default:
		return result(map[string]any{"error": fmt.Sprintf("invalid sort_by %q: must be \"cpu\" or \"rss\"", in.SortBy)})
	}
	info := s.svc.Snapshots()
	// Copy before filtering/sorting: info.Processes shares its backing array
	// with the collector's published snapshot; an in-place sort would corrupt
	// the collector's order and race with the next Collect.
	procs := make([]collector.ProcessInfo, len(info.Processes))
	copy(procs, info.Processes)
	if in.Filter != "" {
		needle := strings.ToLower(in.Filter)
		kept := procs[:0]
		for _, p := range procs {
			if strings.Contains(strings.ToLower(p.Name), needle) {
				kept = append(kept, p)
			}
		}
		procs = kept
	}
	switch sortBy {
	case "rss":
		sort.SliceStable(procs, func(i, j int) bool { return procs[i].Memory > procs[j].Memory })
	default: // cpu — collector pre-sorts by CPU, but don't depend on it
		sort.SliceStable(procs, func(i, j int) bool { return procs[i].CPUPercent > procs[j].CPUPercent })
	}
	total := len(procs)
	limit := in.Limit
	if limit <= 0 {
		limit = defaultProcessLimit
	}
	if limit > maxProcessLimit {
		limit = maxProcessLimit
	}
	truncated := total > limit
	if truncated {
		procs = procs[:limit]
	}
	reason := "top_cpu"
	if sortBy == "rss" {
		reason = "top_rss"
	}
	if in.Filter != "" {
		reason = "filtered"
	}
	return result(processesOutput{Processes: procs, Total: total, Truncated: truncated, Reason: reason})
}

func (s *Server) handleDoctor(ctx context.Context, _ *mcp.CallToolRequest, _ *doctorInput) (*mcp.CallToolResult, any, error) {
	return result(ecosystem.Probe(ctx))
}

// handleAnalyze implements monitor_analyze. Read-only: no confirm gate.
// It clamps the window handler-side so the wired service always receives
// a sane value, and guarantees "diagnoses" is [] (never null) so weak
// agents can iterate it unconditionally.
func (s *Server) handleAnalyze(ctx context.Context, _ *mcp.CallToolRequest, in *analyzeInput) (*mcp.CallToolResult, any, error) {
	if s.svc.Analyze == nil {
		return result(map[string]any{"error": "analyze service not configured"})
	}
	w := in.WindowSeconds
	if w <= 0 {
		w = defaultAnalyzeWindowSeconds
	}
	if w < minAnalyzeWindowSeconds {
		w = minAnalyzeWindowSeconds
	}
	if w > maxAnalyzeWindowSeconds {
		w = maxAnalyzeWindowSeconds
	}
	res, err := s.svc.Analyze(ctx, w, in.PID)
	if err != nil {
		return result(map[string]any{"error": err.Error(), "window_seconds": w})
	}
	diags := res.Diagnoses
	if diags == nil {
		diags = []collector.Diagnosis{}
	}
	alerts := res.Alerts
	if alerts == nil {
		alerts = []collector.Alert{}
	}
	out := map[string]any{
		"window_seconds": w,
		"samples":        res.Samples,
		"diagnoses":      diags,
		"alerts":         alerts,
		"healthy":        len(diags) == 0 && len(alerts) == 0,
	}
	if in.PID > 0 {
		out["pid"] = in.PID
	}
	if len(diags) == 0 && len(alerts) == 0 {
		out["note"] = fmt.Sprintf("no anomalies detected over the %ds window", w)
	}
	return result(out)
}

func (s *Server) handleIssues(ctx context.Context, _ *mcp.CallToolRequest, in *issuesInput) (*mcp.CallToolResult, any, error) {
	if s.svc.IssuesList == nil {
		return result(map[string]any{"issues": []issues.Issue{}, "total": 0, "truncated": false, "error": "issue service not configured"})
	}
	if in == nil {
		in = &issuesInput{}
	}
	statuses := make([]issues.Status, 0, len(in.Statuses))
	for _, raw := range in.Statuses {
		status := issues.Status(strings.ToLower(strings.TrimSpace(raw)))
		if status != issues.StatusOpen && status != issues.StatusResolved && status != issues.StatusIgnored {
			return result(map[string]any{"issues": []issues.Issue{}, "total": 0, "truncated": false, "error": fmt.Sprintf("invalid status %q", raw)})
		}
		statuses = append(statuses, status)
	}
	limit := in.Limit
	if limit <= 0 {
		limit = defaultIssuesLimit
	}
	if limit > maxIssuesLimit {
		limit = maxIssuesLimit
	}
	// Since/Until are passed through unparsed: interpreting them
	// (issues.ParseWindowBound) is the Service implementation's job (see
	// IssuesListFilter's doc comment), not this handler's -- the actual
	// filter matching (window overlap, RunID/Release aggregate lookup, Kind
	// prefix rule) all happens in issues.Store.List either way.
	items, err := s.svc.IssuesList(ctx, IssuesListFilter{
		Statuses: statuses, Project: in.Project, Service: in.Service,
		Since: in.Since, Until: in.Until, RunID: in.RunID, Release: in.Release, Kind: in.Kind,
	})
	if err != nil {
		return result(map[string]any{"issues": []issues.Issue{}, "total": 0, "truncated": false, "error": err.Error()})
	}
	if items == nil {
		items = []issues.Issue{}
	}
	// Payload diet (AC-6): a list row carries only a trimmed
	// LatestException summary, never the full frame/cause detail -- see
	// issues.SummarizeForList. monitor_issue (Store.Get, one issue) keeps
	// the untrimmed detail.
	for i := range items {
		items[i] = issues.SummarizeForList(items[i])
	}
	total := len(items)
	if len(items) > limit {
		items = items[:limit]
	}
	return result(map[string]any{"issues": items, "total": total, "truncated": total > len(items)})
}

// handleIssue is a pure field copy of Service.IssueContext's result into
// the wire response -- every decision about what the response CONTAINS
// (bounded brief vs. the legacy issue/occurrences shape, the not_found
// recovery hint's wording) is made by the Service (internal/cli/mcp.go's
// issueContextForMCP), never here.
func (s *Server) handleIssue(ctx context.Context, _ *mcp.CallToolRequest, in *issueInput) (*mcp.CallToolResult, any, error) {
	if in == nil || strings.TrimSpace(in.ID) == "" {
		return result(map[string]any{"id": "", "not_found": false, "occurrences": []issues.Occurrence{}, "error": "issue id is required"})
	}
	if s.svc.IssueContext == nil {
		return result(map[string]any{"id": in.ID, "not_found": false, "occurrences": []issues.Occurrence{}, "error": "issue service not configured"})
	}
	limit := in.OccurrenceLimit
	if limit < 0 {
		limit = 0
	}
	if limit > maxIssuesLimit {
		limit = maxIssuesLimit
	}
	filter := IssueContextFilter{Project: in.Project, Service: in.Service, Kind: in.Kind}
	res, err := s.svc.IssueContext(ctx, in.ID, filter, limit)
	if err != nil {
		return result(map[string]any{
			"id": in.ID, "not_found": errors.Is(err, issues.ErrIssueNotFound),
			"occurrences": []issues.Occurrence{}, "error": err.Error(),
		})
	}
	if res == nil {
		// A Service implementation that still uses the pre-refactor (nil,
		// nil) sentinel instead of IssueContextResult.NotFound -- a
		// generic, non-field-specific fallback (real production code,
		// issueContextForMCP, always returns a populated NotFound/Recovery
		// pair instead; see its own doc comment for why that logic lives
		// there and not here).
		return result(map[string]any{
			"id": in.ID, "not_found": true, "occurrences": []issues.Occurrence{},
			"recovery": fmt.Sprintf("no issue matches %q; call monitor_issues to see what is open", in.ID),
		})
	}
	if res.NotFound {
		// id/filter (most commonly "latest" under a filter) matched
		// nothing -- an ordinary outcome, not a failure (E2.7): a recovery
		// hint, never an error envelope.
		return result(map[string]any{
			"id": in.ID, "not_found": true, "occurrences": []issues.Occurrence{},
			"recovery": res.Recovery,
		})
	}

	out := map[string]any{}
	if res.Context != nil {
		for k, v := range explainContextFields(res.Context) {
			out[k] = v
		}
	}
	if res.IncludeLegacy {
		// Opt-in (occurrence_limit > 0 on the request): the full legacy
		// {issue, occurrences, occurrences_truncated} shape (the pre-E2.7
		// IssueGet), overwriting Context's own small "issue" summary above
		// with the richer issues.Issue -- see IssueContextResult.
		occurrences := res.Occurrences
		if occurrences == nil {
			occurrences = []issues.Occurrence{}
		}
		out["issue"] = res.Issue
		out["occurrences"] = occurrences
		out["occurrences_truncated"] = res.OccurrencesTruncated
	}
	return result(out)
}

// explainContextFields flattens c's sections into the map handleIssue
// merges into monitor_issue's response, keyed exactly as
// monitor.issue_context.v1 names them, INCLUDING c.Issue (the small, bounded
// {id, short_id, status, kind, title, ...} summary) under "issue" -- the
// response's default, bounded identity block. handleIssue overwrites this
// key with the full legacy issues.Issue only when IssueContextResult.
// IncludeLegacy is true.
func explainContextFields(c *explain.Context) map[string]any {
	out := map[string]any{
		"schema": c.Schema, "budget": c.Budget, "generated_at": c.GeneratedAt,
		"issue": c.Issue, "timeline": c.Timeline, "causes": c.Causes, "frames": c.Frames,
		"impact": c.Impact, "last_touched": c.LastTouched, "related_notes": c.RelatedNotes,
		"degraded": c.Degraded, "next": c.Next, "truncated": c.Truncated, "privacy": c.Privacy,
	}
	if c.ResolvedFrom != nil {
		out["resolved_from"] = c.ResolvedFrom
	}
	if c.Culprit != nil {
		out["culprit"] = c.Culprit
	}
	return out
}

// requireConfirm returns an error when the agent forgot to confirm. Mirrors
// the CLI's "refused" error so agent harnesses detect the failure mode
// uniformly across the surface; each handler builds its own per-tool
// refusal payload from the error string.
func requireConfirm(confirm bool) error {
	if confirm {
		return nil
	}
	return fmt.Errorf("refused: confirm=true required")
}

// handleKill implements monitor_kill. It runs a safety check first and
// returns a structured "refused" payload when the target is protected (or
// when the agent forgot to confirm), so the harness can inspect the reason
// before retrying with confirm=true.
func (s *Server) handleKill(ctx context.Context, _ *mcp.CallToolRequest, in *killInput) (*mcp.CallToolResult, any, error) {
	if err := requireConfirm(in.Confirm); err != nil {
		return result(map[string]any{"killed": false, "refused": true, "reason": err.Error(), "pid": in.PID})
	}
	if s.svc.Kill == nil {
		return result(map[string]any{"killed": false, "refused": true, "reason": "kill service not configured", "pid": in.PID})
	}
	conf := kill.CheckSafety([]int32{in.PID})
	if conf.HasProtected || conf.HasSystem {
		// The CLI refuses protected/system PIDs unless --yes is passed; this
		// tool is stricter and has NO override path (confirm:true is not enough).
		return result(map[string]any{
			"killed":  false,
			"refused": true,
			"reason":  "refused: target is a protected or system-owned process; this tool cannot terminate it",
			"pid":     in.PID,
			"safety":  conf,
		})
	}
	res, err := s.svc.Kill(in.PID, in.Force)
	if err != nil {
		return result(map[string]any{"killed": false, "error": err.Error(), "pid": in.PID, "outcome": string(res.Outcome)})
	}
	payload := map[string]any{
		"killed":    res.Outcome == kill.OutcomeTerminated, // verified, not "signal sent"
		"outcome":   string(res.Outcome),
		"signal":    res.Signal,
		"waited_ms": res.WaitedMs,
		"pid":       in.PID,
		"force":     in.Force,
		"safety":    conf,
	}
	if res.NextAction != "" {
		payload["next_action"] = res.NextAction // e.g. suggest force — NEVER auto-escalate
	}
	return result(payload)
}

// UnavailableError marks a profile capture that failed for a known,
// non-retryable reason — Bun speaking the WebKit/JSC inspector protocol
// instead of V8 CDP is the only producer today — rather than an unexpected
// failure. handleProfileCapture surfaces these as a distinguishable
// status:"unavailable" payload (limitation + recovery as their own fields)
// instead of the bare "error" string an ordinary failure gets, so an agent
// can tell "this will never work as asked, do something else" apart from
// "an attempt failed, retrying or adjusting might help".
type UnavailableError struct {
	Limitation string
	Recovery   string
}

func (e *UnavailableError) Error() string {
	if e.Recovery == "" {
		return e.Limitation
	}
	return e.Limitation + " (" + e.Recovery + ")"
}

// handleProfileCapture implements monitor_profile_capture. Defaults to
// "heap" if the agent omits the type. Returns a structured refusal when
// the profile service is not wired.
func (s *Server) handleProfileCapture(ctx context.Context, _ *mcp.CallToolRequest, in *profileInput) (*mcp.CallToolResult, any, error) {
	if err := requireConfirm(in.Confirm); err != nil {
		return result(map[string]any{"captured": false, "refused": true, "reason": err.Error(), "pid": in.PID})
	}
	if in.Type == "" {
		in.Type = "heap"
	}
	if err := profiler.ValidateCapture(profiler.ProfileType(in.Type)); err != nil {
		return result(map[string]any{"captured": false, "refused": true, "reason": err.Error(), "pid": in.PID, "type": in.Type})
	}
	if s.svc.Profile == nil {
		return result(map[string]any{"captured": false, "refused": true, "reason": "profile service not configured", "pid": in.PID})
	}
	// E1.7 payload diet + temp-file cleanup: the Service itself verifies
	// the capture and, unless in.Keep, discards the raw text (a CDP CPU
	// profile's full JSON, or a pprof text dump) AND deletes the on-disk
	// temp file a pprof/CDP-heap capture left behind, so repeated
	// monitor_profile_capture calls don't leak /tmp/monitor-<type>-* files
	// or bloat every response with a payload that can run into tens of KB.
	// That policy decision lives in the Service (cli/mcp.go), not here —
	// this handler stays free of capture-specific business logic, matching
	// how monitor_investigate's include_raw redaction also lives in the
	// Service rather than being reimplemented per handler.
	prof, receipt, err := s.svc.Profile(ctx, in.PID, profiler.ProfileType(in.Type), in.PprofAddr, in.Keep)
	if err != nil {
		var unavail *UnavailableError
		if errors.As(err, &unavail) {
			return result(map[string]any{
				"captured":   false,
				"status":     "unavailable",
				"pid":        in.PID,
				"type":       in.Type,
				"limitation": unavail.Limitation,
				"recovery":   unavail.Recovery,
			})
		}
		return result(map[string]any{"captured": false, "error": err.Error(), "pid": in.PID})
	}
	if !receipt.Verified {
		return result(map[string]any{
			"captured":   false,
			"pid":        in.PID,
			"type":       in.Type,
			"limitation": receipt.Limitation,
			"next_actions": []string{
				"try type:sample (works for any process on macOS, no pprof needed)",
				"ensure the target exposes net/http/pprof on localhost:6060, or profile via the CLI with --pprof-addr",
			},
		})
	}
	return result(map[string]any{
		"captured": true,
		"pid":      in.PID,
		"profile":  prof,
		"artifact": receipt, // {"verified":true,"size_bytes":N}
	})
}

// handleInvestigate implements monitor_investigate. If the service has
// wired a real investigator it forwards the call; otherwise it returns the
// stable stub shape. The handler REFLECTS the pipeline verdict — it never
// injects investigated:true blindly: "investigated" is only ever derived
// from the pipeline's own verdict=="complete".
func (s *Server) handleInvestigate(ctx context.Context, _ *mcp.CallToolRequest, in *investigateInput) (*mcp.CallToolResult, any, error) {
	if err := requireConfirm(in.Confirm); err != nil {
		return result(map[string]any{"investigated": false, "refused": true, "reason": err.Error(), "pid": in.PID})
	}
	if s.svc.Investigate != nil {
		out := s.svc.Investigate(ctx, in.PID, InvestigateOptions{
			TTL:          in.TTL,
			NoSave:       in.NoSave,
			Codebase:     in.Codebase,
			Environment:  in.Environment,
			DeploymentID: in.DeploymentID,
			RunID:        in.RunID,
			StepID:       in.StepID,
			Suite:        in.Suite,
			Attempt:      in.Attempt,
			Release:      in.Release,
			Service:      in.Service,
			GitSHA:       in.GitSHA,
			IncludeRaw:   in.IncludeRaw,
		})
		verdict, _ := out["verdict"].(string)
		out["investigated"] = verdict == "complete"
		if verdict == "" {
			out["verdict"] = "partial"
			out["limitation"] = "pipeline returned no verdict; treating the result as partial"
		}
		return result(out)
	}
	// Stub when no investigator is wired (tests / read-only embedders):
	// honestly reports that nothing ran.
	steps := []map[string]any{
		{"step": "identify", "status": "skipped", "limitation": "no investigator configured"},
		{"step": "snapshot", "status": "skipped", "limitation": "no investigator configured"},
		{"step": "profile", "status": "skipped", "limitation": "no investigator configured"},
		{"step": "correlate", "status": "skipped", "limitation": "no investigator configured"},
		{"step": "semantic", "status": "skipped", "limitation": "no investigator configured"},
		{"step": "stash", "status": "skipped", "limitation": "no investigator configured"},
		{"step": "issue", "status": "skipped", "limitation": "no investigator configured"},
	}
	return result(map[string]any{
		"investigated": false,
		"verdict":      "partial",
		"pid":          in.PID,
		"started_at":   nowRFC3339(),
		"steps":        steps,
		"note":         "investigation pipeline stub (no investigator configured)",
	})
}

// handleRecord implements monitor_record. Defaults to 30s. Returns a
// refusal if vidtrace is unavailable or the record service isn't wired.
func (s *Server) handleRecord(ctx context.Context, _ *mcp.CallToolRequest, in *recordInput) (*mcp.CallToolResult, any, error) {
	if err := requireConfirm(in.Confirm); err != nil {
		return result(map[string]any{"recording": false, "refused": true, "reason": err.Error(), "pid": in.PID})
	}
	if s.svc.Record == nil {
		return result(map[string]any{
			"recording": false,
			"refused":   true,
			"reason":    "no screen recorder available (record service not configured)",
			"pid":       in.PID,
		})
	}
	if in.DurationSeconds <= 0 {
		in.DurationSeconds = 30
	}
	id, err := s.svc.Record(ctx, in.PID, in.DurationSeconds)
	if err != nil {
		return result(map[string]any{"recording": false, "error": err.Error(), "pid": in.PID})
	}
	payload := map[string]any{
		"recording":  true,
		"pid":        in.PID,
		"scope":      "whole_screen", // pid does NOT scope the capture
		"duration_s": in.DurationSeconds,
		"bundle_id":  id,
	}
	if filepath.IsAbs(id) {
		fi, statErr := os.Stat(id)
		switch {
		case statErr != nil:
			return result(map[string]any{
				"recording": false, "pid": in.PID, "bundle_id": id,
				"limitation": fmt.Sprintf("recording artifact missing at %s: %v", id, statErr),
			})
		case fi.Size() == 0:
			return result(map[string]any{
				"recording": false, "pid": in.PID, "bundle_id": id,
				"limitation": fmt.Sprintf("recording artifact is empty at %s (no display permission?)", id),
			})
		default:
			payload["artifact_verified"] = true
			payload["artifact_bytes"] = fi.Size()
		}
	} else {
		payload["artifact_verified"] = false
		payload["limitation"] = "artifact existence not verifiable (recorder returned a non-path id)"
	}
	return result(payload)
}

// result marshals v as indented JSON for the MCP payload. Matches the
// codemap pattern: SetEscapeHTML=false is implicit because json.MarshalIndent
// already escapes only the necessary characters; the round-trip through
// json.Unmarshal→Marshal keeps the agent-visible payload canonical.
func result(v any) (*mcp.CallToolResult, any, error) {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return nil, nil, err
	}
	var out any
	_ = json.Unmarshal(b, &out)
	return nil, out, nil
}
