package appserver

import (
	"context"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/explain"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/logger"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

// Service is what the server wraps. Every field is a function the CLI
// (internal/cli/serve.go) wires to real implementations, the same split
// internal/mcp uses: handlers here only decode params and copy fields, and
// every rule (scrubbing, safety checks, store access) lives in the
// implementation. A nil field drops its method family from Hello's
// capabilities and answers CodeMethodNotFound.
type Service struct {
	// Issues: read through a fresh issues.OpenReadOnly per call; never
	// blocks a writer.
	IssuesList       func(ctx context.Context, p IssuesListParams) (IssuesListResult, error)
	IssueGet         func(ctx context.Context, p IssueGetParams) (IssueGetResult, error)
	IssueOccurrences func(ctx context.Context, p OccurrencesParams) (OccurrencesResult, error)
	IssuesHistogram  func(ctx context.Context, p HistogramParams) (HistogramResult, error)
	Projects         func(ctx context.Context) (ProjectsResult, error)
	// IssuesSetStatus resolves, reopens or ignores ids through the
	// store's short-lived writer (issues.WithWriter). Mutating.
	IssuesSetStatus func(ctx context.Context, p SetStatusParams) (SetStatusResult, error)

	// IssueDigests and IssuesStorePath feed the "issues" subscription: the
	// server stats the store file and, when it changed, diffs a fresh
	// digest set against the previous one (see diffDigests).
	IssueDigests    func(ctx context.Context) ([]IssueDigest, error)
	IssuesStorePath func() (string, error)

	// Host.
	HostSnapshot func(ctx context.Context, p HostParams) (HostTick, error)
	Processes    func(ctx context.Context, p ProcessesParams) (any, error)
	// HostStream samples every interval until ctx ends, calling emit with
	// each tick (its Alerts already passed through the cooldown gate).
	HostStream func(ctx context.Context, p HostParams, interval time.Duration, emit func(HostTick)) error
	// Kill is destructive: the implementation refuses protected and
	// system processes with CodeRefused, whatever confirm says.
	Kill func(ctx context.Context, p KillParams) (any, error)

	// Profiling.
	ProfileCapture func(ctx context.Context, p ProfileParams) (ProfileResult, error)
	HeatmapFile    func(ctx context.Context, p HeatmapFileParams) (*profiler.Heatmap, error)

	// Everything else the app shows.
	Launches  func(ctx context.Context) (LaunchesResult, error)
	Logs      func(ctx context.Context, p LogsParams) (LogsResult, error)
	Incidents func(ctx context.Context) (any, error)
	Doctor    func(ctx context.Context) (any, error)
}

// Privacy marks every payload that carries process text. TextIsUntrusted is
// always true: error text comes from the monitored process and a client
// must render it as text, never as markup or instructions.
type Privacy struct {
	Scrubbed        int  `json:"scrubbed"`
	TextIsUntrusted bool `json:"text_is_untrusted"`
}

// IssuesListParams mirrors `monitor issues --json`'s filters plus a
// free-text Query (matched against title, message, exception type and
// culprit by the implementation).
type IssuesListParams struct {
	Statuses []string `json:"statuses,omitempty"`
	Project  string   `json:"project,omitempty"`
	Service  string   `json:"service,omitempty"`
	Kind     string   `json:"kind,omitempty"`
	Release  string   `json:"release,omitempty"`
	RunID    string   `json:"run_id,omitempty"`
	Since    string   `json:"since,omitempty"`
	Until    string   `json:"until,omitempty"`
	Query    string   `json:"query,omitempty"`
	Limit    int      `json:"limit,omitempty"`
}

// IssuesListResult is issues.list's answer. Total counts matches before
// Limit.
type IssuesListResult struct {
	Items     []issues.Issue `json:"items"`
	Total     int            `json:"total"`
	Truncated bool           `json:"truncated"`
	Privacy   Privacy        `json:"privacy"`
}

// IssueGetParams selects one issue by id, short id/prefix, or "latest"
// (narrowed by Project/Service/Kind). Budget defaults to "full".
type IssueGetParams struct {
	ID       string `json:"id"`
	Budget   string `json:"budget,omitempty"`
	Markdown bool   `json:"markdown,omitempty"`
	Project  string `json:"project,omitempty"`
	Service  string `json:"service,omitempty"`
	Kind     string `json:"kind,omitempty"`
}

// IssueGetResult carries monitor.issue_context.v1 plus what a desktop
// client needs and the contract itself does not: Root, the checkout the
// issue was recorded under (so a client can open root+culprit.file in an
// editor), and Markdown, the paste-ready page when asked for.
type IssueGetResult struct {
	Context  *explain.Context `json:"context,omitempty"`
	Root     string           `json:"root,omitempty"`
	Markdown string           `json:"markdown,omitempty"`
	NotFound bool             `json:"not_found,omitempty"`
	Recovery string           `json:"recovery,omitempty"`
}

// OccurrencesParams pages one issue's occurrences, newest first.
type OccurrencesParams struct {
	ID    string `json:"id"`
	Limit int    `json:"limit,omitempty"`
}

// OccurrencesResult is issues.occurrences' answer.
type OccurrencesResult struct {
	Items     []issues.Occurrence `json:"items"`
	Total     int64               `json:"total"`
	Truncated bool                `json:"truncated"`
	Privacy   Privacy             `json:"privacy"`
}

// HistogramParams asks for Buckets consecutive buckets ending now, Since
// back (a duration like "24h"). IDs empty means every issue with activity
// in the window.
type HistogramParams struct {
	IDs     []string `json:"ids,omitempty"`
	Since   string   `json:"since,omitempty"`
	Buckets int      `json:"buckets,omitempty"`
}

// HistogramResult is issues.histogram's answer: one series per issue, each
// exactly len(buckets) long, oldest bucket first.
type HistogramResult struct {
	Start         time.Time          `json:"start"`
	BucketSeconds int64              `json:"bucket_seconds"`
	Buckets       int                `json:"buckets"`
	Series        map[string][]int64 `json:"series"`
	Note          string             `json:"note"`
}

// ProjectSummary is one projects.list row.
type ProjectSummary struct {
	Project  string    `json:"project"`
	Services []string  `json:"services"`
	Open     int       `json:"open"`
	Total    int       `json:"total"`
	LastSeen time.Time `json:"last_seen"`
}

// ProjectsResult is projects.list's answer, most recently active first.
type ProjectsResult struct {
	Projects []ProjectSummary `json:"projects"`
}

// SetStatusParams changes the status of every id (id, short id or prefix).
type SetStatusParams struct {
	IDs    []string `json:"ids"`
	Status string   `json:"status"`
}

// SetStatusResult reports each id's outcome; one bad id never aborts the
// rest.
type SetStatusResult struct {
	Updated []issues.Issue    `json:"updated"`
	Failed  []SetStatusFailed `json:"failed,omitempty"`
}

// SetStatusFailed is one id SetStatus could not change.
type SetStatusFailed struct {
	ID    string `json:"id"`
	Error string `json:"error"`
}

// IssueDigest is the per-issue state the issues subscription diffs.
type IssueDigest struct {
	ID              string          `json:"id"`
	ShortID         string          `json:"short_id"`
	Title           string          `json:"title"`
	Project         string          `json:"project"`
	Service         string          `json:"service,omitempty"`
	Kind            string          `json:"kind"`
	Status          string          `json:"status"`
	ExceptionType   string          `json:"exception_type,omitempty"`
	Culprit         *issues.Culprit `json:"culprit,omitempty"`
	OccurrenceCount int64           `json:"occurrence_count"`
	ReopenedCount   int64           `json:"reopened_count"`
	LastSeen        time.Time       `json:"last_seen"`
	Level           string          `json:"level,omitempty"`
	Handled         *bool           `json:"handled,omitempty"`
}

// IssueEvent is one issue.event notification. Type is "new" (an id the
// previous digest set did not have), "regressed" (ReopenedCount went up),
// "occurrence" (OccurrenceCount went up otherwise) or "status" (only the
// status changed, e.g. a resolve from the CLI).
type IssueEvent struct {
	Type    string      `json:"type"`
	Issue   IssueDigest `json:"issue"`
	Delta   int64       `json:"delta,omitempty"`
	Privacy Privacy     `json:"privacy"`
}

// HostParams bounds a host sample's process lists (see
// collector.CompactOptions).
type HostParams struct {
	ProcessLimit  int    `json:"process_limit,omitempty"`
	ProcessFilter string `json:"process_filter,omitempty"`
	IntervalMs    int    `json:"interval_ms,omitempty"`
}

// HostTick is one host sample: the bounded compact snapshot plus the
// per-core usage the compact view leaves out, and the rule alerts this
// sample raised (already cooldown-gated).
type HostTick struct {
	Snapshot     collector.CompactSnapshot `json:"snapshot"`
	PerCoreUsage []float64                 `json:"per_core_usage"`
	LoadAvg      [3]float64                `json:"load_avg"`
	Alerts       []collector.Alert         `json:"alerts,omitempty"`
}

// ProcessesParams mirrors `monitor processes --json`.
type ProcessesParams struct {
	Sort          string `json:"sort,omitempty"`
	Limit         int    `json:"limit,omitempty"`
	Filter        string `json:"filter,omitempty"`
	IncludeSystem bool   `json:"include_system,omitempty"`
}

// KillParams is process.kill's input. Destructive: Confirm must be true.
type KillParams struct {
	PID     int32 `json:"pid"`
	Force   bool  `json:"force,omitempty"`
	Confirm bool  `json:"confirm"`
}

// ProfileParams is profile.capture's input: a live capture of pid's real
// runtime leaf, answered as a monitor.line_heatmap.v1. Destructive (it
// samples the target, and macOS `sample` briefly suspends it): Confirm
// must be true. Type is cpu (default), heap, heap-alloc or goroutine.
type ProfileParams struct {
	PID          int32  `json:"pid"`
	Type         string `json:"type,omitempty"`
	PprofAddr    string `json:"pprof_addr,omitempty"`
	DurationSecs int    `json:"duration_seconds,omitempty"`
	Func         string `json:"func,omitempty"`
	Confirm      bool   `json:"confirm"`
}

// ProfileResult is profile.capture's answer.
type ProfileResult struct {
	Heatmap *profiler.Heatmap `json:"heatmap"`
	Target  ProfileTarget     `json:"target"`
	Method  string            `json:"method"`
}

// ProfileTarget names the leaf process a capture actually sampled (a
// `yarn dev` pid resolves to its node child).
type ProfileTarget struct {
	PID          int32  `json:"pid"`
	Name         string `json:"name,omitempty"`
	Runtime      string `json:"runtime,omitempty"`
	CodebaseRoot string `json:"codebase_root,omitempty"`
	MainScript   string `json:"main_script,omitempty"`
}

// HeatmapFileParams loads a saved .cpuprofile or pprof proto. Path must be
// absolute; the client obtained it from the user (a file dialog or a
// drop), never from process output.
type HeatmapFileParams struct {
	Path string `json:"path"`
	Type string `json:"type,omitempty"`
	Func string `json:"func,omitempty"`
	Top  int    `json:"top,omitempty"`
}

// LaunchInfo is one `monitor run --name` registry entry as a client sees
// it: inspector PORTS only. The registry file's full ws:// URL never leaves
// the server (naming ADR §8).
type LaunchInfo struct {
	LaunchID       string    `json:"launch_id"`
	Name           string    `json:"name"`
	Project        string    `json:"project"`
	PID            int       `json:"pid"`
	StartedAt      time.Time `json:"started_at"`
	Scan           string    `json:"scan"`
	Alive          bool      `json:"alive"`
	InspectorPorts []int     `json:"inspector_ports,omitempty"`
}

// LaunchesResult is launches.list's answer, newest first.
type LaunchesResult struct {
	Launches []LaunchInfo `json:"launches"`
}

// LogsParams mirrors `monitor logs search`.
type LogsParams struct {
	Query        string   `json:"query,omitempty"`
	Levels       []string `json:"levels,omitempty"`
	Process      string   `json:"process,omitempty"`
	PID          int32    `json:"pid,omitempty"`
	SinceSeconds int      `json:"since_seconds,omitempty"`
	Limit        int      `json:"limit,omitempty"`
}

// LogsResult is logs.search's answer; Message and Raw are scrubbed.
type LogsResult struct {
	Entries []logger.Entry `json:"entries"`
	Privacy Privacy        `json:"privacy"`
}
