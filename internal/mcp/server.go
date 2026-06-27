// Package mcp exposes the monitor's data through a Model Context Protocol
// stdio server, following the codemap MCP server pattern (one Server struct,
// one Service, typed inputs, NL-JSON-RPC framing).
//
// Read-only tools:
//
//	monitor_snapshot        full SystemInfo
//	monitor_processes       top processes
//	monitor_doctor          ecosystem health
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

// Service is the dependency the MCP server wraps. Each field is a thin
// function so the CLI can wire in real implementations without coupling
// the mcp package to the concrete ones.
type Service struct {
	// Snapshots returns the latest SystemInfo. Required for read tools.
	Snapshots func() collector.SystemInfo

	// Kill terminates the given PID. force=true sends SIGKILL, otherwise
	// SIGTERM. Used by monitor_kill. Required for mutating kill tools.
	Kill func(pid int32, force bool) error

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
			"or monitor_doctor. All tools return JSON. Mutating tools (monitor_kill, " +
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
	mcp.AddTool(s.srv, &mcp.Tool{Name: "monitor_snapshot", Description: "Return the latest SystemInfo."},
		s.handleSnapshot)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "monitor_processes", Description: "Return the process list (already sorted by CPU)."},
		s.handleProcesses)
	mcp.AddTool(s.srv, &mcp.Tool{Name: "monitor_doctor", Description: "Ecosystem tool availability."},
		s.handleDoctor)
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
type processesInput struct{}

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
	return result(info)
}

func (s *Server) handleProcesses(_ context.Context, _ *mcp.CallToolRequest, _ *processesInput) (*mcp.CallToolResult, any, error) {
	if s.svc.Snapshots == nil {
		return result(map[string]any{"error": "snapshot service not configured"})
	}
	info := s.svc.Snapshots()
	return result(info.Processes)
}

func (s *Server) handleDoctor(ctx context.Context, _ *mcp.CallToolRequest, _ *processesInput) (*mcp.CallToolResult, any, error) {
	return result(ecosystem.Probe(ctx))
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
	if err := s.svc.Kill(in.PID, in.Force); err != nil {
		return result(map[string]any{"killed": false, "error": err.Error(), "pid": in.PID})
	}
	return result(map[string]any{
		"killed": true,
		"pid":    in.PID,
		"force":  in.Force,
		"safety": conf,
	})
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
	return result(map[string]any{
		"captured": true,
		"pid":      in.PID,
		"profile":  prof,
	})
}

// handleInvestigate implements monitor_investigate. If the service has
// wired a real investigator it forwards the call; otherwise it returns the
// stable stub shape. An "investigated" boolean is added so the tool carries a
// success/refusal discriminator like kill/profile/record (killed/captured/
// recording). The boolean is injected here, MCP-side, so the CLI's shared
// investigatePipeline output is unchanged.
func (s *Server) handleInvestigate(ctx context.Context, _ *mcp.CallToolRequest, in *investigateInput) (*mcp.CallToolResult, any, error) {
	if err := requireConfirm(in.Confirm); err != nil {
		return result(map[string]any{"investigated": false, "refused": true, "reason": err.Error(), "pid": in.PID})
	}
	if s.svc.Investigate != nil {
		out := s.svc.Investigate(ctx, in.PID)
		out["investigated"] = true
		return result(out)
	}
	// Fall back to a stable stub shape when no investigator is wired (e.g.
	// in tests); production wires the real pipeline (snapshot + profile + stash).
	return result(map[string]any{
		"investigated": true,
		"pid":          in.PID,
		"started_at":   nowRFC3339(),
		"steps":        []string{"snapshot", "profile", "stash"},
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
	return result(map[string]any{
		"recording":  true,
		"pid":        in.PID,
		"scope":      "whole_screen", // pid does NOT scope the capture
		"duration_s": in.DurationSeconds,
		"bundle_id":  id,
	})
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
