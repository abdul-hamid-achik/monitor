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
//	monitor_record          start a vidtrace screen recording for a process
//
// All mutating tools refuse to run without the confirm flag set, mirroring
// the CLI's --yes convention. This keeps the MCP surface safe for agents
// to call: the agent must explicitly assert intent before anything changes
// on the host.
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
			"Call monitor_snapshot first to orient, then drill down with monitor_process " +
			"or monitor_alerts. All tools return JSON. Mutating tools (monitor_kill, " +
			"monitor_profile_capture, monitor_investigate, monitor_record) require the " +
			"typed 'confirm: true' field in their input before they will run.",
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
		Description: "Run the diagnostic pipeline (snapshot + profile + search + correlate) for a process. Requires `confirm: true`.",
	}, s.handleInvestigate)
	mcp.AddTool(s.srv, &mcp.Tool{
		Name:        "monitor_record",
		Description: "Start a vidtrace screen recording for a process. Requires `confirm: true`.",
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
	PID             int32 `json:"pid"               jsonschema:"the PID to record"`
	DurationSeconds int   `json:"duration,omitempty" jsonschema:"recording duration in seconds (default 30)"`
	Confirm         bool  `json:"confirm"           jsonschema:"must be true; confirms intent to start a screen recording"`
}

// -- Handlers ------------------------------------------------------------

func (s *Server) handleSnapshot(_ context.Context, _ *mcp.CallToolRequest, _ *snapshotInput) (*mcp.CallToolResult, any, error) {
	info := s.svc.Snapshots()
	return result(info)
}

func (s *Server) handleProcesses(_ context.Context, _ *mcp.CallToolRequest, _ *processesInput) (*mcp.CallToolResult, any, error) {
	info := s.svc.Snapshots()
	return result(info.Processes)
}

func (s *Server) handleDoctor(ctx context.Context, _ *mcp.CallToolRequest, _ *processesInput) (*mcp.CallToolResult, any, error) {
	return result(ecosystem.Probe(ctx))
}

// requireConfirm returns a structured refusal when the agent forgot to
// confirm. Mirrors the CLI's "refused" error so agent harnesses can
// detect the failure mode uniformly across the surface.
func requireConfirm(confirm bool) (map[string]any, error) {
	if confirm {
		return nil, nil
	}
	return map[string]any{
		"killed":   false,
		"refused":  true,
		"requires": "set confirm=true in the typed input to acknowledge the destructive action",
	}, fmt.Errorf("refused: confirm=true required")
}

// handleKill implements monitor_kill. It runs a safety check first and
// returns a structured "refused" payload when the target is protected (or
// when the agent forgot to confirm), so the harness can inspect the reason
// before retrying with confirm=true.
func (s *Server) handleKill(ctx context.Context, _ *mcp.CallToolRequest, in *killInput) (*mcp.CallToolResult, any, error) {
	if _, err := requireConfirm(in.Confirm); err != nil {
		return result(map[string]any{"killed": false, "refused": true, "reason": err.Error(), "pid": in.PID})
	}
	if s.svc.Kill == nil {
		return result(map[string]any{"killed": false, "refused": true, "reason": "kill service not configured", "pid": in.PID})
	}
	conf := kill.CheckSafety([]int32{in.PID})
	if conf.HasProtected {
		return result(map[string]any{
			"killed":     false,
			"refused":    true,
			"reason":     "protected process; refuse without explicit override (call with confirm=true after re-checking)",
			"pid":        in.PID,
			"safety":     conf,
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
	if _, err := requireConfirm(in.Confirm); err != nil {
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
		return result(map[string]any{"captured": false, "error": err.Error(), "pid": in.PID, "type": in.Type})
	}
	return result(map[string]any{
		"captured": true,
		"profile":  prof,
	})
}

// handleInvestigate implements monitor_investigate. If the service has
// wired a real investigator it forwards the call; otherwise it returns the
// stable stub shape the CLI uses today so consumers always see the same
// fields (pid, started_at, steps, note).
func (s *Server) handleInvestigate(ctx context.Context, _ *mcp.CallToolRequest, in *investigateInput) (*mcp.CallToolResult, any, error) {
	if _, err := requireConfirm(in.Confirm); err != nil {
		return result(map[string]any{"refused": true, "reason": err.Error(), "pid": in.PID})
	}
	if s.svc.Investigate != nil {
		return result(s.svc.Investigate(ctx, in.PID))
	}
	// Fall back to the same stub shape the CLI emits — keeps the surface
	// stable for agents while the real pipeline lands.
	return result(map[string]any{
		"pid":        in.PID,
		"started_at": nowRFC3339(),
		"steps":      []string{"snapshot", "profile", "search"},
		"note":       "investigation pipeline stub; full pipeline lands in iteration 2",
	})
}

// handleRecord implements monitor_record. Defaults to 30s. Returns a
// refusal if vidtrace is unavailable or the record service isn't wired.
func (s *Server) handleRecord(ctx context.Context, _ *mcp.CallToolRequest, in *recordInput) (*mcp.CallToolResult, any, error) {
	if _, err := requireConfirm(in.Confirm); err != nil {
		return result(map[string]any{"recording": false, "refused": true, "reason": err.Error(), "pid": in.PID})
	}
	if s.svc.Record == nil {
		return result(map[string]any{
			"recording": false,
			"refused":   true,
			"reason":    "vidtrace not installed or record service not configured",
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
