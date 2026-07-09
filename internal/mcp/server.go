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
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
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
// window produced and the diagnoses derived from them.
type AnalyzeResult struct {
	Samples   int                   `json:"samples"`
	Diagnoses []collector.Diagnosis `json:"diagnoses"`
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

	// Kill terminates the given PID and returns the verified Result (outcome
	// terminated|still_running|unknown). force=true sends SIGKILL, otherwise
	// SIGTERM. Used by monitor_kill. Required for mutating kill tools.
	Kill func(pid int32, force bool) (kill.Result, error)

	// Profile captures a profile of the given PID. Used by
	// monitor_profile_capture. Optional; if nil the tool reports unavailable.
	Profile func(ctx context.Context, pid int32, ptype profiler.ProfileType) (profiler.Profile, error)

	// Investigate runs the diagnostic pipeline for the given PID. Used by
	// monitor_investigate. Optional; if nil the tool reports a stub result
	// matching the CLI's stub output so the surface stays stable.
	Investigate func(ctx context.Context, pid int32) map[string]any

	// Record starts a vidtrace recording for the given PID. Used by
	// monitor_record. Optional; if nil the tool reports vidtrace missing.
	Record func(ctx context.Context, pid int32, durationSeconds int) (string, error)
}

// Server wraps the MCP stdio transport.
type Server struct {
	svc *Service
	srv *mcp.Server
}

// NewServer creates an MCP server exposing monitor's read-only and mutating
// surface. Mutating tools are registered unconditionally; they fail at call
// time with a clear "confirm required" error if the agent omits the confirm
// flag.
func NewServer(svc *Service) *Server {
	s := &Server{svc: svc}
	impl := &mcp.Implementation{Name: "monitor", Version: "0.3.0"}
	opts := &mcp.ServerOptions{
		Instructions: "monitor is an agent-harnessable local observability tool. " +
			"Call monitor_snapshot first to orient, then drill down with monitor_processes " +
			"or monitor_doctor. When the user reports slowness, a suspected leak, or a " +
			"runaway process, call monitor_analyze (read-only, no confirm; it blocks for " +
			"window_seconds while sampling). All tools return JSON. Mutating tools (monitor_kill, " +
			"monitor_profile_capture, monitor_investigate, monitor_record) require the " +
			"typed 'confirm: true' field in their input before they will run. " +
			"confirm:true is necessary but not sufficient for monitor_kill: it still " +
			"refuses protected or system-owned processes and returns " +
			"{killed:false, refused:true, reason}.",
	}
	s.srv = mcp.NewServer(impl, opts)
	s.register()
	return s
}

// Run starts the server on stdio.
func (s *Server) Run(ctx context.Context) error {
	return s.srv.Run(ctx, &mcp.StdioTransport{})
}

func (s *Server) register() {
	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "monitor_snapshot",
		Description: "Return the latest SystemInfo with an interpreted 'summary' string " +
			"(memory/CPU/disk state, top consumer) and, when a threshold is near, 'next' " +
			"suggestions. The raw metrics (cpu, memory, disk, processes, ...) follow at the top level.",
	}, s.handleSnapshot)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "monitor_processes",
		Description: "Return the top processes. Input: limit (default 15, max 200), " +
			"sort_by: 'cpu' (default) or 'rss', filter: case-insensitive substring on the process name. " +
			"Output: {processes, total, truncated, reason} — total counts matches before truncation, " +
			"truncated says the list was cut at limit, reason is top_cpu | top_rss | filtered.",
	}, s.handleProcesses)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "monitor_doctor", Description: "Ecosystem tool availability."},
		s.handleDoctor)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name: "monitor_analyze",
		Description: "Diagnose why the system is slow or unhealthy. Call this when the user says " +
			"\"something is slow\", the machine feels sluggish, a process seems stuck, or memory/CPU " +
			"looks wrong. Read-only and safe: there is NO confirm field. Samples metrics once per second " +
			"for window_seconds (default 10, min 4, max 60) and returns " +
			"diagnoses: [{summary, evidence, confidence, next_actions}]. Pass pid to focus on one process. " +
			"healthy:true with an empty diagnoses list means nothing anomalous was observed in the window; " +
			"retry with a larger window_seconds before concluding the system is fine.",
	}, s.handleAnalyze)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_kill",
		Description: "Safely terminate a process. Requires `confirm: true` in the input. Use force=true for SIGKILL.",
	}, s.handleKill)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_profile_capture",
		Description: "Capture a profile for a process. Requires `confirm: true`. type: heap|cpu|goroutine|sample.",
	}, s.handleProfileCapture)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_investigate",
		Description: "Run the diagnostic pipeline (snapshot + profile + correlate + stash) for a process. Requires `confirm: true`.",
	}, s.handleInvestigate)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_record",
		Description: "Record the screen for N seconds (default 30) via the platform recorder (screencapture/ffmpeg); the result can be analyzed with vidtrace. Requires `confirm: true`.",
	}, s.handleRecord)
}

// -- Read-only input/handler types ----------------------------------------

type snapshotInput struct{}

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

const (
	defaultProcessLimit = 15
	maxProcessLimit     = 200

	defaultAnalyzeWindowSeconds = 10
	minAnalyzeWindowSeconds     = 4 // analyzer's diagMinSamples: fewer aligned samples -> no diagnosis
	maxAnalyzeWindowSeconds     = 60
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
	PID     int32  `json:"pid"                jsonschema:"the PID to profile"`
	Type    string `json:"type,omitempty"     jsonschema:"profile type: heap, cpu, goroutine, sample (default heap)"`
	Confirm bool   `json:"confirm"            jsonschema:"must be true; confirms intent to capture a profile"`
}

// investigateInput is the typed input for monitor_investigate.
type investigateInput struct {
	PID     int32 `json:"pid"     jsonschema:"the PID to investigate"`
	Confirm bool  `json:"confirm" jsonschema:"must be true; confirms intent to run the diagnostic pipeline"`
}

// recordInput is the typed input for monitor_record.
type recordInput struct {
	PID             int32 `json:"pid"               jsonschema:"PID for context/labeling only — the recorder captures the WHOLE screen, not just this process"`
	DurationSeconds int   `json:"duration,omitempty" jsonschema:"recording duration in seconds (default 30)"`
	Confirm         bool  `json:"confirm"           jsonschema:"must be true; confirms intent to start a screen recording"`
}

// -- Handlers ------------------------------------------------------------

func (s *Server) handleSnapshot(_ context.Context, _ *mcp.CallToolRequest, _ *snapshotInput) (*mcp.CallToolResult, any, error) {
	if s.svc.Snapshots == nil {
		return result(map[string]any{"error": "snapshot service not configured"})
	}
	info := s.svc.Snapshots()
	summary, next := buildSnapshotSummary(info)
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
	out := map[string]any{
		"window_seconds": w,
		"samples":        res.Samples,
		"diagnoses":      diags,
		"healthy":        len(diags) == 0,
	}
	if in.PID > 0 {
		out["pid"] = in.PID
	}
	if len(diags) == 0 {
		out["note"] = fmt.Sprintf("no anomalies detected over the %ds window", w)
	}
	return result(out)
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

// handleProfileCapture implements monitor_profile_capture. Defaults to
// "heap" if the agent omits the type. Returns a structured refusal when
// the profile service is not wired.
func (s *Server) handleProfileCapture(ctx context.Context, _ *mcp.CallToolRequest, in *profileInput) (*mcp.CallToolResult, any, error) {
	if err := requireConfirm(in.Confirm); err != nil {
		return result(map[string]any{"captured": false, "refused": true, "reason": err.Error(), "pid": in.PID})
	}
	if s.svc.Profile == nil {
		return result(map[string]any{"captured": false, "refused": true, "reason": "profile service not configured", "pid": in.PID})
	}
	if in.Type == "" {
		in.Type = "heap"
	}
	prof, err := s.svc.Profile(ctx, in.PID, profiler.ProfileType(in.Type))
	if err != nil {
		return result(map[string]any{"captured": false, "error": err.Error(), "pid": in.PID})
	}
	receipt := prof.VerifyArtifact()
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
		out := s.svc.Investigate(ctx, in.PID)
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
		{"step": "snapshot", "status": "skipped", "limitation": "no investigator configured"},
		{"step": "profile", "status": "skipped", "limitation": "no investigator configured"},
		{"step": "correlate", "status": "skipped", "limitation": "no investigator configured"},
		{"step": "stash", "status": "skipped", "limitation": "no investigator configured"},
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
