package mcp

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

func TestHandleSnapshotWithStubService(t *testing.T) {
	s := newTestServer(t, &Service{
		Snapshots: func() collector.SystemInfo { return collector.SystemInfo{Hostname: "test-host"} },
	})
	_, payload, err := s.handleSnapshot(context.Background(), nil, &snapshotInput{})
	if err != nil {
		t.Fatalf("handleSnapshot: %v", err)
	}
	// result() JSON-roundtrips into a generic map.
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", payload)
	}
	if m["hostname"] != "test-host" {
		t.Errorf("hostname = %v, want test-host", m["hostname"])
	}
	if sum, _ := m["summary"].(string); sum == "" {
		t.Errorf("snapshot payload should include a non-empty summary; got %v", m["summary"])
	}
}

func TestHandleSnapshotCompactIsBoundedAndFiltered(t *testing.T) {
	procs := make([]collector.ProcessInfo, 40)
	for i := range procs {
		name := "other"
		if i%2 == 0 {
			name = "ollama-runner"
		}
		procs[i] = collector.ProcessInfo{
			PID: int32(i + 1), Name: name, CPUPercent: float64(i), Memory: uint64(40 - i),
		}
	}
	s := newTestServer(t, &Service{Snapshots: func() collector.SystemInfo {
		return collector.SystemInfo{
			Hostname: "test-host", Processes: procs,
			Disk: collector.DiskInfo{Partitions: []collector.DiskPartitionInfo{
				{MountPoint: "/", Filesystem: "apfs"},
				{MountPoint: "/tmp", Filesystem: "tmpfs"},
			}},
		}
	}})

	_, payload, err := s.handleSnapshot(context.Background(), nil, &snapshotInput{
		Compact: true, ProcessLimit: 3, ProcessFilter: "OLLAMA",
		FilesystemLimit: 1, FilesystemFilter: "tmpfs",
	})
	if err != nil {
		t.Fatalf("handleSnapshot: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", payload)
	}
	if got, _ := m["schema_version"].(float64); int(got) != collector.CompactSnapshotSchemaVersion {
		t.Fatalf("schema_version = %v", m["schema_version"])
	}
	if _, exists := m["disk"]; exists {
		t.Fatalf("compact payload unexpectedly includes lossless disk object")
	}
	processes, ok := m["processes"].(map[string]any)
	if !ok {
		t.Fatalf("processes type = %T", m["processes"])
	}
	if got, _ := processes["limit"].(float64); int(got) != 3 {
		t.Fatalf("process limit = %v", processes["limit"])
	}
	if top, _ := processes["top_cpu"].([]any); len(top) != 3 {
		t.Fatalf("top_cpu length = %d, want 3", len(top))
	}
	filesystems, ok := m["filesystems"].([]any)
	if !ok || len(filesystems) != 1 {
		t.Fatalf("filesystems = %T %v", m["filesystems"], m["filesystems"])
	}
	fs := filesystems[0].(map[string]any)
	if fs["mount_point"] != "/tmp" {
		t.Fatalf("filesystem filter returned %v", fs)
	}
}

// TestReadHandlersRefuseNilSnapshots is a regression for the read handlers
// dereferencing a nil Snapshots service: they must return a structured error
// instead of panicking.
func TestReadHandlersRefuseNilSnapshots(t *testing.T) {
	s := newTestServer(t, &Service{}) // Snapshots is nil
	calls := map[string]func() (any, error){
		"snapshot": func() (any, error) {
			_, p, err := s.handleSnapshot(context.Background(), nil, &snapshotInput{})
			return p, err
		},
		"processes": func() (any, error) {
			_, p, err := s.handleProcesses(context.Background(), nil, &processesInput{})
			return p, err
		},
		"analyze": func() (any, error) {
			_, p, err := s.handleAnalyze(context.Background(), nil, &analyzeInput{})
			return p, err
		},
	}
	for name, call := range calls {
		p, err := call()
		if err != nil {
			t.Errorf("%s: unexpected hard error %v", name, err)
		}
		m, ok := p.(map[string]any)
		if !ok || m["error"] == nil {
			t.Errorf("%s: want a structured error payload, got %T (%v)", name, p, p)
		}
	}
}

func newTestServer(t *testing.T, svc *Service) *Server {
	t.Helper()
	if svc == nil {
		svc = &Service{}
	}
	return NewServer(svc, "test")
}

// TestNewServerReportsInjectedVersion runs a real in-memory MCP handshake
// and asserts the server advertises the version passed to NewServer (the CLI
// injects the goreleaser build version) rather than a hardcoded literal.
func TestNewServerReportsInjectedVersion(t *testing.T) {
	ctx := context.Background()
	s := NewServer(&Service{}, "9.9.9")
	clientTr, serverTr := mcp.NewInMemoryTransports()
	ss, err := s.srv.Connect(ctx, serverTr, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := client.Connect(ctx, clientTr, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()
	if got := cs.InitializeResult().ServerInfo.Version; got != "9.9.9" {
		t.Errorf("handshake version = %q, want %q", got, "9.9.9")
	}
}

func TestToolsAdvertiseSafetyAnnotations(t *testing.T) {
	ctx := context.Background()
	s := NewServer(&Service{}, "test")
	clientTr, serverTr := mcp.NewInMemoryTransports()
	ss, err := s.srv.Connect(ctx, serverTr, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer ss.Close()
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := client.Connect(ctx, clientTr, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer cs.Close()

	listed, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatalf("list tools: %v", err)
	}
	tools := make(map[string]*mcp.Tool, len(listed.Tools))
	for _, tool := range listed.Tools {
		tools[tool.Name] = tool
	}
	readOnly := []string{
		"monitor_snapshot", "monitor_processes", "monitor_doctor",
		"monitor_analyze", "monitor_issues", "monitor_issue",
	}
	for _, name := range readOnly {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("%s missing from tools/list", name)
			continue
		}
		annotations := tool.Annotations
		if annotations == nil || !annotations.ReadOnlyHint {
			t.Errorf("%s readOnlyHint = false or missing", name)
			continue
		}
		if annotations.DestructiveHint == nil || *annotations.DestructiveHint {
			t.Errorf("%s destructiveHint = %v, want false", name, annotations.DestructiveHint)
		}
		if annotations.OpenWorldHint == nil || *annotations.OpenWorldHint {
			t.Errorf("%s openWorldHint = %v, want false", name, annotations.OpenWorldHint)
		}
	}

	for _, name := range []string{"monitor_kill", "monitor_profile_capture", "monitor_investigate", "monitor_record"} {
		tool, ok := tools[name]
		if !ok {
			t.Errorf("%s missing from tools/list", name)
			continue
		}
		annotations := tool.Annotations
		if annotations == nil {
			t.Errorf("%s annotations missing", name)
			continue
		}
		if annotations.ReadOnlyHint {
			t.Errorf("%s readOnlyHint = true, want false", name)
		}
		if annotations.OpenWorldHint == nil || *annotations.OpenWorldHint {
			t.Errorf("%s openWorldHint = %v, want false", name, annotations.OpenWorldHint)
		}
	}
	killTool, ok := tools["monitor_kill"]
	if !ok || killTool.Annotations == nil {
		t.Fatal("monitor_kill annotations missing")
	}
	killAnnotations := killTool.Annotations
	if killAnnotations.DestructiveHint == nil || !*killAnnotations.DestructiveHint || killAnnotations.IdempotentHint {
		t.Errorf("monitor_kill annotations = %+v, want destructive and non-idempotent", killAnnotations)
	}
	for _, name := range []string{"monitor_profile_capture", "monitor_investigate", "monitor_record"} {
		tool, ok := tools[name]
		if !ok || tool.Annotations == nil {
			continue // already reported above
		}
		annotations := tool.Annotations
		if annotations.DestructiveHint == nil || *annotations.DestructiveHint || annotations.IdempotentHint {
			t.Errorf("%s annotations = %+v, want additive and non-idempotent", name, annotations)
		}
	}
}

// TestRequireConfirm asserts the confirm gate: nil error when confirmed,
// an error otherwise (handlers build their own refusal payload from it).
func TestRequireConfirm(t *testing.T) {
	if err := requireConfirm(true); err != nil {
		t.Fatalf("confirm=true should not error; got %v", err)
	}
	if err := requireConfirm(false); err == nil {
		t.Fatalf("confirm=false should error")
	}
}

// TestHandleInvestigateForwardsToService verifies the wired investigator is
// used (not the nil-service stub) when confirm=true.
func TestHandleInvestigateForwardsToService(t *testing.T) {
	s := newTestServer(t, &Service{
		Investigate: func(_ context.Context, pid int32, _ InvestigateOptions) map[string]any {
			return map[string]any{"pid": pid, "wired": true}
		},
	})
	_, payload, err := s.handleInvestigate(context.Background(), nil, &investigateInput{PID: 99, Confirm: true})
	if err != nil {
		t.Fatalf("handleInvestigate: %v", err)
	}
	m, _ := payload.(map[string]any)
	if m["wired"] != true {
		t.Errorf("expected the wired investigator result; got %v", m)
	}
}

// TestHandleRecordForwardsToService verifies the wired recorder is used and
// its returned id surfaces in the payload.
func TestHandleRecordForwardsToService(t *testing.T) {
	s := newTestServer(t, &Service{
		Record: func(_ context.Context, _ int32, _ int) (string, error) {
			return "rec-123", nil
		},
	})
	_, payload, err := s.handleRecord(context.Background(), nil, &recordInput{PID: 7, DurationSeconds: 5, Confirm: true})
	if err != nil {
		t.Fatalf("handleRecord: %v", err)
	}
	m, _ := payload.(map[string]any)
	if m["recording"] != true || m["bundle_id"] != "rec-123" {
		t.Errorf("expected the wired record result; got %v", m)
	}
}

// TestHandleKillRefusesWithoutConfirm verifies monitor_kill requires
// confirm=true in the typed input.
func TestHandleKillRefusesWithoutConfirm(t *testing.T) {
	s := newTestServer(t, &Service{
		Kill: func(int32, bool) (kill.Result, error) {
			t.Fatalf("Kill must not be called without confirm")
			return kill.Result{}, nil
		},
	})
	_, payload, err := s.handleKill(context.Background(), nil, &killInput{PID: 1234})
	if err != nil {
		t.Fatalf("handleKill should not return a hard error for refusals; got %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleKill refusal should produce a structured payload; got %T", payload)
	}
	if refused, _ := m["refused"].(bool); !refused {
		t.Fatalf("refusal payload should set refused=true; got %v", m)
	}
}

// TestHandleKillSucceedsWithConfirm verifies the happy path: confirm=true
// triggers the kill function and returns killed=true.
func TestHandleKillSucceedsWithConfirm(t *testing.T) {
	called := false
	s := newTestServer(t, &Service{
		Kill: func(pid int32, force bool) (kill.Result, error) {
			called = true
			if pid != 4321 {
				t.Errorf("Kill received pid=%d, want 4321", pid)
			}
			if !force {
				t.Errorf("Kill received force=false, want true")
			}
			return kill.Result{PID: pid, Signal: "SIGKILL", Outcome: kill.OutcomeTerminated, WaitedMs: 12}, nil
		},
	})
	_, payload, err := s.handleKill(context.Background(), nil, &killInput{PID: 4321, Force: true, Confirm: true})
	if err != nil {
		t.Fatalf("handleKill returned hard error: %v", err)
	}
	if !called {
		t.Fatalf("Kill service func should have been called")
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleKill success should produce a structured payload; got %T", payload)
	}
	if killed, _ := m["killed"].(bool); !killed {
		t.Fatalf("success payload should set killed=true; got %v", m)
	}
	if outcome, _ := m["outcome"].(string); outcome != "terminated" {
		t.Errorf("outcome = %v, want terminated", m["outcome"])
	}
}

// TestHandleKillErrorPropagation verifies that errors from the kill
// service are surfaced via the structured result (not as hard Go errors),
// matching the convention used for confirm refusals.
func TestHandleKillErrorPropagation(t *testing.T) {
	s := newTestServer(t, &Service{
		Kill: func(int32, bool) (kill.Result, error) {
			return kill.Result{Outcome: kill.OutcomeUnknown}, errors.New("boom")
		},
	})
	// Use a PID that's neither protected nor owned by root (a high PIDs
	// like 999999 is almost certainly not running, but the safety check
	// only flags known names + root-owned; PID 999999 falls through to
	// the kill service, which then errors).
	_, payload, err := s.handleKill(context.Background(), nil, &killInput{PID: 999999, Confirm: true})
	if err != nil {
		t.Fatalf("handleKill should surface errors via the result payload; got hard error %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleKill should produce a structured payload on error; got %T", payload)
	}
	if errStr, _ := m["error"].(string); errStr == "" {
		t.Fatalf("error payload should set the error field; got %v", m)
	}
}

// TestHandleKillProtectedProcessIsRefused verifies monitor_kill refuses
// to act on protected processes regardless of confirm (the safety check
// short-circuits before the kill service is invoked).
func TestHandleKillProtectedProcessIsRefused(t *testing.T) {
	s := newTestServer(t, &Service{
		Kill: func(int32, bool) (kill.Result, error) {
			t.Fatalf("Kill must not be called for protected process; safety check should short-circuit")
			return kill.Result{}, nil
		},
	})
	// PID 1 is launchd / init in macOS, treated as protected.
	_, payload, err := s.handleKill(context.Background(), nil, &killInput{PID: 1, Confirm: true})
	if err != nil {
		t.Fatalf("handleKill should not return a hard error for refused-protected; got %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleKill refusal should produce a structured payload; got %T", payload)
	}
	if refused, _ := m["refused"].(bool); !refused {
		t.Fatalf("protected refusal should set refused=true; got %v", m)
	}
}

// TestHandleKillMissingServiceIsRefused verifies monitor_kill returns a
// structured refusal when the service hasn't wired Kill (so an embedder
// that only exposes read tools still gets a clear refusal rather than a
// nil pointer panic).
func TestHandleKillMissingServiceIsRefused(t *testing.T) {
	s := newTestServer(t, &Service{}) // no Kill wired
	_, payload, err := s.handleKill(context.Background(), nil, &killInput{PID: 9999, Confirm: true})
	if err != nil {
		t.Fatalf("handleKill should not return a hard error for missing service; got %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleKill should produce a structured refusal payload; got %T", payload)
	}
	if refused, _ := m["refused"].(bool); !refused {
		t.Fatalf("missing-service refusal should set refused=true; got %v", m)
	}
}

// TestHandleKillStillRunningSurfacesNextAction verifies a still_running
// outcome reports killed=false plus a next_action, and is NOT a "refused"
// payload (the signal was sent — it just wasn't verified to have landed).
func TestHandleKillStillRunningSurfacesNextAction(t *testing.T) {
	s := newTestServer(t, &Service{
		Kill: func(int32, bool) (kill.Result, error) {
			return kill.Result{Outcome: kill.OutcomeStillRunning, Signal: "SIGTERM", WaitedMs: 2000, NextAction: "retry with force"}, nil
		},
	})
	_, payload, err := s.handleKill(context.Background(), nil, &killInput{PID: 999999, Confirm: true})
	if err != nil {
		t.Fatalf("handleKill returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleKill should produce a structured payload; got %T", payload)
	}
	if killed, _ := m["killed"].(bool); killed {
		t.Errorf("killed should be false for still_running; got %v", m)
	}
	if outcome, _ := m["outcome"].(string); outcome != "still_running" {
		t.Errorf("outcome = %v, want still_running", m["outcome"])
	}
	if next, _ := m["next_action"].(string); next == "" {
		t.Errorf("next_action should be non-empty; got %v", m)
	}
	if _, ok := m["refused"]; ok {
		t.Errorf("still_running is not a refusal; refused should be absent, got %v", m["refused"])
	}
}

// TestHandleProfileCaptureRefusesWithoutConfirm mirrors TestHandleKill.
func TestHandleProfileCaptureRefusesWithoutConfirm(t *testing.T) {
	s := newTestServer(t, &Service{
		Profile: func(context.Context, int32, profiler.ProfileType, string, bool) (profiler.Profile, profiler.Receipt, error) {
			t.Fatalf("Profile must not be called without confirm")
			return profiler.Profile{}, profiler.Receipt{}, nil
		},
	})
	_, payload, err := s.handleProfileCapture(context.Background(), nil, &profileInput{PID: 1234, Type: "heap"})
	if err != nil {
		t.Fatalf("handleProfileCapture should not return a hard error for refusals; got %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleProfileCapture refusal should produce a structured payload; got %T", payload)
	}
	if refused, _ := m["refused"].(bool); !refused {
		t.Fatalf("refusal payload should set refused=true; got %v", m)
	}
}

// TestHandleProfileCaptureDefaultsType verifies that omitting the type
// field defaults to "heap" (matching the CLI default).
func TestHandleProfileCaptureDefaultsType(t *testing.T) {
	got := profiler.ProfileType("")
	s := newTestServer(t, &Service{
		Profile: func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string, keep bool) (profiler.Profile, profiler.Receipt, error) {
			got = ptype
			prof := profiler.Profile{PID: pid, Type: ptype, Taken: time.Now(), Text: "heap profile: 1"}
			receipt := prof.VerifyArtifact()
			if !keep {
				_ = prof.DiscardRawArtifact()
			}
			return prof, receipt, nil
		},
	})
	_, payload, err := s.handleProfileCapture(context.Background(), nil, &profileInput{PID: 7, Confirm: true})
	if err != nil {
		t.Fatalf("handleProfileCapture returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleProfileCapture should produce a structured payload; got %T", payload)
	}
	if captured, _ := m["captured"].(bool); !captured {
		t.Fatalf("success payload should set captured=true; got %v", m)
	}
	if got != "heap" {
		t.Fatalf("default profile type should be 'heap'; got %q", got)
	}
}

// TestHandleProfileCaptureRefusesEmptyArtifact verifies that a profile with
// no evidence (no text, symbols, or file) is reported as captured=false with
// a limitation, never as a blind success.
func TestHandleProfileCaptureRefusesEmptyArtifact(t *testing.T) {
	s := newTestServer(t, &Service{
		Profile: func(context.Context, int32, profiler.ProfileType, string, bool) (profiler.Profile, profiler.Receipt, error) {
			prof := profiler.Profile{PID: 7, Type: "heap"}
			return prof, prof.VerifyArtifact(), nil
		},
	})
	_, payload, err := s.handleProfileCapture(context.Background(), nil, &profileInput{PID: 7, Confirm: true})
	if err != nil {
		t.Fatalf("handleProfileCapture returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleProfileCapture should produce a structured payload; got %T", payload)
	}
	if captured, _ := m["captured"].(bool); captured {
		t.Fatalf("captured should be false for an empty artifact; got %v", m)
	}
	if lim, _ := m["limitation"].(string); lim == "" {
		t.Errorf("expected a non-empty limitation; got %v", m)
	}
	if _, ok := m["next_actions"]; !ok {
		t.Errorf("expected next_actions to be present; got %v", m)
	}
}

// TestHandleProfileCaptureVerifiedArtifact verifies a profile carrying text
// is reported as captured=true with a verified artifact receipt.
func TestHandleProfileCaptureVerifiedArtifact(t *testing.T) {
	s := newTestServer(t, &Service{
		Profile: func(context.Context, int32, profiler.ProfileType, string, bool) (profiler.Profile, profiler.Receipt, error) {
			prof := profiler.Profile{PID: 7, Type: "heap", Text: "heap profile: 1"}
			return prof, prof.VerifyArtifact(), nil
		},
	})
	_, payload, err := s.handleProfileCapture(context.Background(), nil, &profileInput{PID: 7, Confirm: true})
	if err != nil {
		t.Fatalf("handleProfileCapture returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleProfileCapture should produce a structured payload; got %T", payload)
	}
	if captured, _ := m["captured"].(bool); !captured {
		t.Fatalf("captured should be true; got %v", m)
	}
	artifact, ok := m["artifact"].(map[string]any)
	if !ok {
		t.Fatalf("artifact should be a map; got %T", m["artifact"])
	}
	if verified, _ := artifact["verified"].(bool); !verified {
		t.Errorf("artifact.verified should be true; got %v", artifact)
	}
}

// TestHandleProfileCaptureUnavailableStatus verifies a Service.Profile
// error satisfying errors.As(err, *UnavailableError) (Bun's "speaks JSC,
// not CDP" case being the only producer today) surfaces as a distinguishable
// status:"unavailable" payload with its own limitation/recovery fields,
// never the bare "error" string an ordinary failure gets — an agent must be
// able to tell "this will never work as asked" apart from "an attempt
// failed, retry might help".
func TestHandleProfileCaptureUnavailableStatus(t *testing.T) {
	s := newTestServer(t, &Service{
		Profile: func(context.Context, int32, profiler.ProfileType, string, bool) (profiler.Profile, profiler.Receipt, error) {
			return profiler.Profile{}, profiler.Receipt{}, &UnavailableError{
				Limitation: "Bun speaks the WebKit/JSC inspector protocol, not V8 CDP",
				Recovery:   "run the app with `bun --cpu-prof`",
			}
		},
	})
	_, payload, err := s.handleProfileCapture(context.Background(), nil, &profileInput{PID: 7, Type: "cpu", Confirm: true})
	if err != nil {
		t.Fatalf("handleProfileCapture returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", payload)
	}
	if captured, _ := m["captured"].(bool); captured {
		t.Fatalf("captured should be false for unavailable; got %v", m)
	}
	if status, _ := m["status"].(string); status != "unavailable" {
		t.Fatalf("status = %q, want \"unavailable\"; payload=%v", m["status"], m)
	}
	if lim, _ := m["limitation"].(string); !strings.Contains(lim, "JSC") {
		t.Errorf("limitation = %q, want it to carry the Bun-specific message; payload=%v", lim, m)
	}
	if rec, _ := m["recovery"].(string); rec == "" {
		t.Errorf("recovery should be non-empty; payload=%v", m)
	}
	if _, hasError := m["error"]; hasError {
		t.Errorf("an unavailable capture should not also set the bare \"error\" field; payload=%v", m)
	}
}

// TestHandleInvestigateRefusesWithoutConfirm verifies the confirm gate
// for monitor_investigate.
func TestHandleInvestigateRefusesWithoutConfirm(t *testing.T) {
	s := newTestServer(t, &Service{
		Investigate: func(context.Context, int32, InvestigateOptions) map[string]any {
			t.Fatalf("Investigate must not be called without confirm")
			return nil
		},
	})
	_, payload, err := s.handleInvestigate(context.Background(), nil, &investigateInput{PID: 1234})
	if err != nil {
		t.Fatalf("handleInvestigate should not return a hard error for refusals; got %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleInvestigate refusal should produce a structured payload; got %T", payload)
	}
	if refused, _ := m["refused"].(bool); !refused {
		t.Fatalf("refusal payload should set refused=true; got %v", m)
	}
}

// TestHandleInvestigateStubShape verifies that when no real investigator is
// wired, the stub still emits pid / started_at / steps / note — the same
// fields the CLI emits today — so agent harnesses see a stable surface.
func TestHandleInvestigateStubShape(t *testing.T) {
	s := newTestServer(t, &Service{}) // no Investigate wired
	_, payload, err := s.handleInvestigate(context.Background(), nil, &investigateInput{PID: 4242, Confirm: true})
	if err != nil {
		t.Fatalf("handleInvestigate returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("stub payload should be a map; got %T", payload)
	}
	for _, k := range []string{"pid", "started_at", "steps", "note"} {
		if _, ok := m[k]; !ok {
			t.Errorf("stub payload missing key %q", k)
		}
	}
	if got, _ := m["pid"].(float64); int32(got) != 4242 {
		t.Errorf("stub payload pid=%v, want 4242", m["pid"])
	}
	if investigated, _ := m["investigated"].(bool); investigated {
		t.Errorf("stub payload should not claim investigated=true; got %v", m["investigated"])
	}
	if verdict, _ := m["verdict"].(string); verdict != "partial" {
		t.Errorf("stub payload verdict = %v, want partial", m["verdict"])
	}
}

// TestHandleInvestigateReflectsVerdict verifies "investigated" is only ever
// derived from the pipeline's own verdict=="complete", and that a missing
// verdict is treated as partial with an injected limitation.
func TestHandleInvestigateReflectsVerdict(t *testing.T) {
	tests := []struct {
		name             string
		out              map[string]any
		wantInvestigated bool
		wantVerdict      string
		wantLimitation   bool
	}{
		{name: "complete verdict", out: map[string]any{"verdict": "complete"}, wantInvestigated: true, wantVerdict: "complete"},
		{name: "partial verdict", out: map[string]any{"verdict": "partial"}, wantInvestigated: false, wantVerdict: "partial"},
		{name: "no verdict", out: map[string]any{}, wantInvestigated: false, wantVerdict: "partial", wantLimitation: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t, &Service{
				Investigate: func(context.Context, int32, InvestigateOptions) map[string]any {
					// Return a fresh copy each call; handleInvestigate mutates the map.
					out := map[string]any{}
					for k, v := range tt.out {
						out[k] = v
					}
					return out
				},
			})
			_, payload, err := s.handleInvestigate(context.Background(), nil, &investigateInput{PID: 1, Confirm: true})
			if err != nil {
				t.Fatalf("handleInvestigate returned hard error: %v", err)
			}
			m, ok := payload.(map[string]any)
			if !ok {
				t.Fatalf("payload type = %T, want map[string]any", payload)
			}
			if investigated, _ := m["investigated"].(bool); investigated != tt.wantInvestigated {
				t.Errorf("investigated = %v, want %v", m["investigated"], tt.wantInvestigated)
			}
			if verdict, _ := m["verdict"].(string); verdict != tt.wantVerdict {
				t.Errorf("verdict = %v, want %v", m["verdict"], tt.wantVerdict)
			}
			if tt.wantLimitation {
				if lim, _ := m["limitation"].(string); lim == "" {
					t.Errorf("expected a non-empty limitation; got %v", m)
				}
			}
		})
	}
}

// TestHandleInvestigateCustomService ensures a wired Investigate service
// receives the call and its result flows back unchanged.
func TestHandleInvestigateCustomService(t *testing.T) {
	called := false
	s := newTestServer(t, &Service{
		Investigate: func(_ context.Context, pid int32, _ InvestigateOptions) map[string]any {
			called = true
			return map[string]any{"pid": pid, "custom": true}
		},
	})
	_, payload, err := s.handleInvestigate(context.Background(), nil, &investigateInput{PID: 99, Confirm: true})
	if err != nil {
		t.Fatalf("handleInvestigate returned hard error: %v", err)
	}
	if !called {
		t.Fatalf("Investigate service func should have been called")
	}
	m, _ := payload.(map[string]any)
	if custom, _ := m["custom"].(bool); !custom {
		t.Fatalf("custom investigate result did not flow through; got %v", m)
	}
}

// TestHandleRecordRefusesWithoutConfirm mirrors TestHandleKill.
func TestHandleRecordRefusesWithoutConfirm(t *testing.T) {
	s := newTestServer(t, &Service{
		Record: func(context.Context, int32, int) (string, error) {
			t.Fatalf("Record must not be called without confirm")
			return "", nil
		},
	})
	_, payload, err := s.handleRecord(context.Background(), nil, &recordInput{PID: 1234})
	if err != nil {
		t.Fatalf("handleRecord should not return a hard error for refusals; got %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleRecord refusal should produce a structured payload; got %T", payload)
	}
	if refused, _ := m["refused"].(bool); !refused {
		t.Fatalf("refusal payload should set refused=true; got %v", m)
	}
}

// TestHandleRecordDefaultsDuration verifies that omitting duration
// defaults to 30s (matching the CLI plan in the vision doc).
func TestHandleRecordDefaultsDuration(t *testing.T) {
	got := 0
	s := newTestServer(t, &Service{
		Record: func(_ context.Context, pid int32, dur int) (string, error) {
			got = dur
			return "bundle-abc", nil
		},
	})
	_, payload, err := s.handleRecord(context.Background(), nil, &recordInput{PID: 7, Confirm: true})
	if err != nil {
		t.Fatalf("handleRecord returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleRecord should produce a structured payload; got %T", payload)
	}
	if recording, _ := m["recording"].(bool); !recording {
		t.Fatalf("success payload should set recording=true; got %v", m)
	}
	if got != 30 {
		t.Fatalf("default record duration should be 30; got %d", got)
	}
}

// TestHandleRecordMissingServiceIsRefused mirrors TestHandleKillMissingService.
func TestHandleRecordMissingServiceIsRefused(t *testing.T) {
	s := newTestServer(t, &Service{}) // no Record wired
	_, payload, err := s.handleRecord(context.Background(), nil, &recordInput{PID: 7, Confirm: true})
	if err != nil {
		t.Fatalf("handleRecord should not return a hard error for missing service; got %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleRecord should produce a structured refusal payload; got %T", payload)
	}
	if refused, _ := m["refused"].(bool); !refused {
		t.Fatalf("missing-service refusal should set refused=true; got %v", m)
	}
}

// TestHandleRecordVerifiesArtifactFile verifies that when the recorder
// returns an absolute path, the handler stats it and reports the verified
// artifact size rather than trusting the id blindly.
func TestHandleRecordVerifiesArtifactFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rec.mov")
	if err := os.WriteFile(path, []byte("fake video bytes"), 0o644); err != nil {
		t.Fatalf("write temp file: %v", err)
	}
	s := newTestServer(t, &Service{
		Record: func(context.Context, int32, int) (string, error) { return path, nil },
	})
	_, payload, err := s.handleRecord(context.Background(), nil, &recordInput{PID: 7, Confirm: true})
	if err != nil {
		t.Fatalf("handleRecord returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleRecord should produce a structured payload; got %T", payload)
	}
	if recording, _ := m["recording"].(bool); !recording {
		t.Fatalf("recording should be true; got %v", m)
	}
	if verified, _ := m["artifact_verified"].(bool); !verified {
		t.Errorf("artifact_verified should be true; got %v", m)
	}
	if bytes, _ := m["artifact_bytes"].(float64); bytes <= 0 {
		t.Errorf("artifact_bytes should be > 0; got %v", m["artifact_bytes"])
	}
}

// TestHandleRecordMissingArtifactFile verifies a recorder-returned path that
// was never created is reported as recording=false with a limitation.
func TestHandleRecordMissingArtifactFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.mov")
	s := newTestServer(t, &Service{
		Record: func(context.Context, int32, int) (string, error) { return path, nil },
	})
	_, payload, err := s.handleRecord(context.Background(), nil, &recordInput{PID: 7, Confirm: true})
	if err != nil {
		t.Fatalf("handleRecord returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleRecord should produce a structured payload; got %T", payload)
	}
	if recording, _ := m["recording"].(bool); recording {
		t.Fatalf("recording should be false for a missing artifact; got %v", m)
	}
	if lim, _ := m["limitation"].(string); !strings.Contains(lim, "missing") {
		t.Errorf("limitation = %q, want it to contain 'missing'", lim)
	}
}

// TestHandleRecordEmptyArtifactFile verifies a zero-byte recorder artifact
// is reported as recording=false with a limitation (never a silent success).
func TestHandleRecordEmptyArtifactFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.mov")
	if err := os.WriteFile(path, nil, 0o644); err != nil {
		t.Fatalf("write empty temp file: %v", err)
	}
	s := newTestServer(t, &Service{
		Record: func(context.Context, int32, int) (string, error) { return path, nil },
	})
	_, payload, err := s.handleRecord(context.Background(), nil, &recordInput{PID: 7, Confirm: true})
	if err != nil {
		t.Fatalf("handleRecord returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleRecord should produce a structured payload; got %T", payload)
	}
	if recording, _ := m["recording"].(bool); recording {
		t.Fatalf("recording should be false for an empty artifact; got %v", m)
	}
	if lim, _ := m["limitation"].(string); !strings.Contains(lim, "empty") {
		t.Errorf("limitation = %q, want it to contain 'empty'", lim)
	}
}

// TestHandleRecordOpaqueIDNotVerifiable verifies a non-path id (e.g. a
// vidtrace bundle id) is reported as recording=true but artifact_verified
// false, since existence can't be checked.
func TestHandleRecordOpaqueIDNotVerifiable(t *testing.T) {
	s := newTestServer(t, &Service{
		Record: func(context.Context, int32, int) (string, error) { return "rec-123", nil },
	})
	_, payload, err := s.handleRecord(context.Background(), nil, &recordInput{PID: 7, Confirm: true})
	if err != nil {
		t.Fatalf("handleRecord returned hard error: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("handleRecord should produce a structured payload; got %T", payload)
	}
	if recording, _ := m["recording"].(bool); !recording {
		t.Fatalf("recording should be true for an opaque id; got %v", m)
	}
	if verified, _ := m["artifact_verified"].(bool); verified {
		t.Errorf("artifact_verified should be false for a non-path id; got %v", m)
	}
	if lim, _ := m["limitation"].(string); lim == "" {
		t.Errorf("expected a non-empty limitation; got %v", m)
	}
}

// procsSnapshot returns a Service whose Snapshots yields n processes with
// descending CPU (pid i has CPU n-i) and ascending RSS (pid i has Memory i MB).
func procsSnapshot(n int) *Service {
	procs := make([]collector.ProcessInfo, n)
	for i := range procs {
		procs[i] = collector.ProcessInfo{
			PID:        int32(i + 1),
			Name:       fmt.Sprintf("proc-%d", i+1),
			CPUPercent: float64(n - i),
			Memory:     uint64(i+1) << 20,
		}
	}
	return &Service{Snapshots: func() collector.SystemInfo { return collector.SystemInfo{Processes: procs} }}
}

func TestHandleProcesses(t *testing.T) {
	tests := []struct {
		name          string
		n             int
		in            processesInput
		wantLen       int
		wantTotal     int
		wantTruncated bool
		wantReason    string
		wantFirstPID  float64 // JSON numbers round-trip as float64
		wantErr       bool
	}{
		{name: "defaults to top 15 by cpu", n: 20, in: processesInput{},
			wantLen: 15, wantTotal: 20, wantTruncated: true, wantReason: "top_cpu", wantFirstPID: 1},
		{name: "sort_by rss returns biggest rss first", n: 20, in: processesInput{SortBy: "rss"},
			wantLen: 15, wantTotal: 20, wantTruncated: true, wantReason: "top_rss", wantFirstPID: 20},
		{name: "limit larger than total is not truncated", n: 5, in: processesInput{Limit: 50},
			wantLen: 5, wantTotal: 5, wantTruncated: false, wantReason: "top_cpu", wantFirstPID: 1},
		{name: "filter matches case-insensitively", n: 20, in: processesInput{Filter: "PROC-2"},
			// matches proc-2 and proc-20
			wantLen: 2, wantTotal: 2, wantTruncated: false, wantReason: "filtered", wantFirstPID: 2},
		{name: "filter with no matches", n: 5, in: processesInput{Filter: "zzz"},
			wantLen: 0, wantTotal: 0, wantTruncated: false, wantReason: "filtered"},
		{name: "invalid sort_by is a structured error", n: 5, in: processesInput{SortBy: "bogus"}, wantErr: true},
		{name: "limit clamped to max 200", n: 250, in: processesInput{Limit: 10_000},
			wantLen: 200, wantTotal: 250, wantTruncated: true, wantReason: "top_cpu", wantFirstPID: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestServer(t, procsSnapshot(tt.n))
			_, payload, err := s.handleProcesses(context.Background(), nil, &tt.in)
			if err != nil {
				t.Fatalf("handleProcesses: %v", err)
			}
			m, ok := payload.(map[string]any)
			if !ok {
				t.Fatalf("payload type = %T, want map[string]any", payload)
			}
			if tt.wantErr {
				if m["error"] == nil {
					t.Fatalf("want structured error payload; got %v", m)
				}
				return
			}
			list, ok := m["processes"].([]any)
			if !ok {
				t.Fatalf("processes should be a JSON array (never null); got %T", m["processes"])
			}
			if len(list) != tt.wantLen {
				t.Errorf("len(processes) = %d, want %d", len(list), tt.wantLen)
			}
			if got, _ := m["total"].(float64); int(got) != tt.wantTotal {
				t.Errorf("total = %v, want %d", m["total"], tt.wantTotal)
			}
			if got, _ := m["truncated"].(bool); got != tt.wantTruncated {
				t.Errorf("truncated = %v, want %v", m["truncated"], tt.wantTruncated)
			}
			if got, _ := m["reason"].(string); got != tt.wantReason {
				t.Errorf("reason = %q, want %q", got, tt.wantReason)
			}
			if tt.wantLen > 0 {
				first, _ := list[0].(map[string]any)
				if got, _ := first["pid"].(float64); got != tt.wantFirstPID {
					t.Errorf("first pid = %v, want %v", first["pid"], tt.wantFirstPID)
				}
			}
		})
	}
}

// TestHandleProcessesDoesNotMutateSnapshot is a regression: the handler must
// sort a copy, because the snapshot's slice shares its backing array with the
// collector's published state.
func TestHandleProcessesDoesNotMutateSnapshot(t *testing.T) {
	shared := []collector.ProcessInfo{
		{PID: 1, Name: "a", CPUPercent: 9, Memory: 1},
		{PID: 2, Name: "b", CPUPercent: 5, Memory: 100},
		{PID: 3, Name: "c", CPUPercent: 1, Memory: 50},
	}
	s := newTestServer(t, &Service{Snapshots: func() collector.SystemInfo {
		return collector.SystemInfo{Processes: shared}
	}})
	if _, _, err := s.handleProcesses(context.Background(), nil, &processesInput{SortBy: "rss"}); err != nil {
		t.Fatalf("handleProcesses: %v", err)
	}
	if shared[0].PID != 1 || shared[1].PID != 2 || shared[2].PID != 3 {
		t.Fatalf("handler mutated the shared snapshot slice: %+v", shared)
	}
}

func TestHandleAnalyze(t *testing.T) {
	tests := []struct {
		name        string
		in          analyzeInput
		wantWindow  int
		wantPID     int32
		diags       []collector.Diagnosis
		serviceErr  error
		wantHealthy bool
		wantErr     bool
	}{
		{name: "defaults window to 10", in: analyzeInput{}, wantWindow: 10, wantHealthy: true},
		{name: "clamps tiny window up to 4", in: analyzeInput{WindowSeconds: 1}, wantWindow: 4, wantHealthy: true},
		{name: "clamps huge window down to 60", in: analyzeInput{WindowSeconds: 1000}, wantWindow: 60, wantHealthy: true},
		{name: "forwards pid", in: analyzeInput{PID: 42}, wantWindow: 10, wantPID: 42, wantHealthy: true},
		{name: "diagnoses flow through and healthy is false",
			in:         analyzeInput{},
			wantWindow: 10,
			diags: []collector.Diagnosis{{
				Summary: "leaky (pid 42): suspected memory leak", Evidence: []string{"rule=rss_growth severity=warning"},
				Confidence: "medium", NextActions: []string{"monitor_profile_capture pid:42 type:heap confirm:true"},
			}},
			wantHealthy: false},
		{name: "service error becomes structured payload", in: analyzeInput{},
			wantWindow: 10, serviceErr: errors.New("boom"), wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var gotWindow int
			var gotPID int32
			s := newTestServer(t, &Service{
				Analyze: func(_ context.Context, w int, pid int32) (AnalyzeResult, error) {
					gotWindow, gotPID = w, pid
					return AnalyzeResult{Samples: w, Diagnoses: tt.diags}, tt.serviceErr
				},
			})
			_, payload, err := s.handleAnalyze(context.Background(), nil, &tt.in)
			if err != nil {
				t.Fatalf("handleAnalyze: %v", err)
			}
			m, ok := payload.(map[string]any)
			if !ok {
				t.Fatalf("payload type = %T, want map[string]any", payload)
			}
			if gotWindow != tt.wantWindow {
				t.Errorf("service received window %d, want %d", gotWindow, tt.wantWindow)
			}
			if gotPID != tt.wantPID {
				t.Errorf("service received pid %d, want %d", gotPID, tt.wantPID)
			}
			if tt.wantErr {
				if m["error"] == nil {
					t.Fatalf("want structured error payload; got %v", m)
				}
				return
			}
			diags, ok := m["diagnoses"].([]any)
			if !ok {
				t.Fatalf("diagnoses should be a JSON array (never null); got %T", m["diagnoses"])
			}
			if len(diags) != len(tt.diags) {
				t.Errorf("len(diagnoses) = %d, want %d", len(diags), len(tt.diags))
			}
			if healthy, _ := m["healthy"].(bool); healthy != tt.wantHealthy {
				t.Errorf("healthy = %v, want %v", m["healthy"], tt.wantHealthy)
			}
			if tt.wantHealthy && m["note"] == nil {
				t.Errorf("healthy payload should carry a note; got %v", m)
			}
			if len(tt.diags) > 0 {
				d0, _ := diags[0].(map[string]any)
				for _, k := range []string{"summary", "evidence", "confidence", "next_actions"} {
					if _, ok := d0[k]; !ok {
						t.Errorf("diagnosis missing key %q: %v", k, d0)
					}
				}
			}
		})
	}
}

// TestHandleAnalyzeSurfacesAlerts is the wire regression for bug 17's second
// half: AnalyzeResult.Alerts (the plain rule findings analyzeWindow now
// collects from the same NewDefaultEngine rules `monitor watch` runs) must
// reach the monitor_analyze JSON payload as an additive "alerts" field, and
// must count toward "healthy"/"note" the same way Diagnoses does — a caller
// seeing healthy:true while a zombie_process alert fired would be worse off
// than seeing no Diagnoses at all.
func TestHandleAnalyzeSurfacesAlerts(t *testing.T) {
	alerts := []collector.Alert{{
		Severity: "warning", Rule: "zombie_process", PID: 300,
		Detail: "orphan (pid 300) is a zombie awaiting parent 1",
	}}
	s := newTestServer(t, &Service{
		Analyze: func(_ context.Context, w int, _ int32) (AnalyzeResult, error) {
			return AnalyzeResult{Samples: w, Alerts: alerts}, nil
		},
	})
	_, payload, err := s.handleAnalyze(context.Background(), nil, &analyzeInput{})
	if err != nil {
		t.Fatalf("handleAnalyze: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", payload)
	}
	gotAlerts, ok := m["alerts"].([]any)
	if !ok {
		t.Fatalf("alerts should be a JSON array (never null); got %T", m["alerts"])
	}
	if len(gotAlerts) != 1 {
		t.Fatalf("len(alerts) = %d, want 1", len(gotAlerts))
	}
	a0, _ := gotAlerts[0].(map[string]any)
	if a0["rule"] != "zombie_process" {
		t.Errorf("alerts[0].rule = %v, want zombie_process", a0["rule"])
	}
	if healthy, _ := m["healthy"].(bool); healthy {
		t.Error("healthy = true with a live alert present, want false")
	}
	if m["note"] != nil {
		t.Errorf("note should be omitted once an alert is present; got %v", m["note"])
	}
}

// TestHandleAnalyzeAlertsDefaultToEmptyArray covers the nil-vs-empty JSON
// contract for Alerts, matching Diagnoses' existing guarantee — an agent
// parsing monitor_analyze must never see `"alerts": null`.
func TestHandleAnalyzeAlertsDefaultToEmptyArray(t *testing.T) {
	s := newTestServer(t, &Service{
		Analyze: func(_ context.Context, w int, _ int32) (AnalyzeResult, error) {
			return AnalyzeResult{Samples: w}, nil
		},
	})
	_, payload, err := s.handleAnalyze(context.Background(), nil, &analyzeInput{})
	if err != nil {
		t.Fatalf("handleAnalyze: %v", err)
	}
	m, ok := payload.(map[string]any)
	if !ok {
		t.Fatalf("payload type = %T, want map[string]any", payload)
	}
	alerts, ok := m["alerts"].([]any)
	if !ok || len(alerts) != 0 {
		t.Fatalf("alerts = %v (%T), want an empty JSON array", m["alerts"], m["alerts"])
	}
}

func TestHandleIssuesIsBoundedAndReadOnly(t *testing.T) {
	items := make([]issues.Issue, 250)
	for i := range items {
		items[i] = issues.Issue{ID: fmt.Sprintf("ISS-%03d", i), Status: issues.StatusOpen}
	}
	var got IssuesListFilter
	s := newTestServer(t, &Service{IssuesList: func(_ context.Context, filter IssuesListFilter) ([]issues.Issue, error) {
		got = filter
		return items, nil
	}})
	_, payload, err := s.handleIssues(context.Background(), nil, &issuesInput{
		Statuses: []string{"OPEN"}, Project: "monitor", Service: "api", Limit: 999,
		Since: "24h", RunID: "run-1", Release: "v1.2.3", Kind: "exception",
	})
	if err != nil {
		t.Fatal(err)
	}
	m := payload.(map[string]any)
	if len(m["issues"].([]any)) != 200 || m["total"].(float64) != 250 || !m["truncated"].(bool) {
		t.Fatalf("bounded payload = %v", m)
	}
	if len(got.Statuses) != 1 || got.Statuses[0] != issues.StatusOpen || got.Project != "monitor" || got.Service != "api" {
		t.Fatalf("filters = %+v", got)
	}
	// Since/RunID/Release/Kind (E2.6) must reach Service.IssuesList exactly
	// as the MCP caller supplied them -- handleIssues is a pure field copy;
	// interpreting Since/Until (issues.ParseWindowBound) is the Service
	// implementation's job (internal/cli/mcp.go's listIssuesForMCP), not
	// this handler's -- see IssuesListFilter's doc comment and
	// TestListIssuesForMCPForwardsWindowFiltersToStore in internal/cli.
	if got.Since != "24h" {
		t.Fatalf("Since = %q, want the raw \"24h\" passed straight through", got.Since)
	}
	if got.RunID != "run-1" || got.Release != "v1.2.3" || got.Kind != "exception" {
		t.Fatalf("RunID/Release/Kind = %q/%q/%q", got.RunID, got.Release, got.Kind)
	}
	if _, _, err := s.handleIssues(context.Background(), nil, &issuesInput{Statuses: []string{"bogus"}}); err != nil {
		t.Fatalf("invalid status should be structured, got hard error: %v", err)
	}
}

// TestHandleIssuesSurfacesServiceErrorAsStructuredPayload verifies a Service
// error (e.g. from the Service's own issues.ParseWindowBound call, once
// since/until leave this handler unparsed) becomes the same structured
// {error: ...} payload as any other IssuesList failure, not a hard Go error
// or a silently-ignored filter -- handleIssues itself does no since/until
// parsing (or validation) of its own; see IssuesListFilter's doc comment.
func TestHandleIssuesSurfacesServiceErrorAsStructuredPayload(t *testing.T) {
	s := newTestServer(t, &Service{IssuesList: func(_ context.Context, filter IssuesListFilter) ([]issues.Issue, error) {
		if filter.Since == "not-a-time" {
			return nil, fmt.Errorf("invalid time %q", filter.Since)
		}
		return nil, nil
	}})
	_, payload, err := s.handleIssues(context.Background(), nil, &issuesInput{Since: "not-a-time"})
	if err != nil {
		t.Fatalf("a Service error should be structured, got hard error: %v", err)
	}
	m := payload.(map[string]any)
	if errMsg, _ := m["error"].(string); errMsg == "" {
		t.Fatalf("payload = %v, want a non-empty error", payload)
	}
	if issuesVal, ok := m["issues"].([]any); !ok || len(issuesVal) != 0 {
		t.Fatalf("payload issues = %v, want an empty array", m["issues"])
	}
}

func TestHandleIssueReturnsOccurrencesAndStructuredNotFound(t *testing.T) {
	issue := issues.Issue{ID: "ISS-1", OccurrenceCount: 3}
	s := newTestServer(t, &Service{IssueGet: func(_ context.Context, id string, limit int) (issues.Issue, []issues.Occurrence, error) {
		if id == "missing" {
			return issues.Issue{}, nil, fmt.Errorf("%w: %s", issues.ErrIssueNotFound, id)
		}
		if limit != 20 {
			t.Fatalf("default occurrence limit = %d, want 20", limit)
		}
		return issue, []issues.Occurrence{{ID: "OCC-1"}, {ID: "OCC-2"}}, nil
	}})
	_, payload, err := s.handleIssue(context.Background(), nil, &issueInput{ID: "ISS-1"})
	if err != nil {
		t.Fatal(err)
	}
	m := payload.(map[string]any)
	if len(m["occurrences"].([]any)) != 2 || !m["occurrences_truncated"].(bool) {
		t.Fatalf("detail payload = %v", m)
	}
	_, payload, err = s.handleIssue(context.Background(), nil, &issueInput{ID: "missing"})
	if err != nil {
		t.Fatal(err)
	}
	m = payload.(map[string]any)
	if found, _ := m["not_found"].(bool); !found {
		t.Fatalf("not-found payload = %v", m)
	}
}

func TestHandleIssueClampsOccurrenceLimitAndKeepsTypedEvidence(t *testing.T) {
	var gotLimit int
	wantRun := &issues.RunContext{ID: "run-1", Environment: "preview", StepID: "test"}
	wantEvidence := []issues.EvidenceRef{{Kind: "monitor.incident", URI: "fcheap://stash/stash-1"}}
	s := newTestServer(t, &Service{IssueGet: func(_ context.Context, id string, limit int) (issues.Issue, []issues.Occurrence, error) {
		gotLimit = limit
		return issues.Issue{ID: id, OccurrenceCount: 1}, []issues.Occurrence{{
			ID: "OCC-1", IssueID: id, Run: wantRun, Evidence: wantEvidence,
		}}, nil
	}})
	_, payload, err := s.handleIssue(context.Background(), nil, &issueInput{ID: "ISS-1", OccurrenceLimit: 999})
	if err != nil {
		t.Fatal(err)
	}
	if gotLimit != maxIssuesLimit {
		t.Fatalf("service occurrence limit = %d, want %d", gotLimit, maxIssuesLimit)
	}
	m := payload.(map[string]any)
	occurrences, ok := m["occurrences"].([]any)
	if !ok || len(occurrences) != 1 {
		t.Fatalf("occurrences = %T %v", m["occurrences"], m["occurrences"])
	}
	event := occurrences[0].(map[string]any)
	run := event["run"].(map[string]any)
	evidence := event["evidence"].([]any)
	if run["id"] != "run-1" || len(evidence) != 1 || evidence[0].(map[string]any)["uri"] != "fcheap://stash/stash-1" {
		t.Fatalf("typed event payload = %v", event)
	}
}

func TestHandleIssuesNormalizesNilServices(t *testing.T) {
	s := newTestServer(t, &Service{})
	_, payload, err := s.handleIssues(context.Background(), nil, &issuesInput{})
	if err != nil {
		t.Fatal(err)
	}
	m := payload.(map[string]any)
	if _, ok := m["issues"].([]any); !ok {
		t.Fatalf("issues must be an array: %T", m["issues"])
	}
}

// connectInMemory wires an in-memory client/server pair for a real wire-level
// CallTool round trip (E1.7: "Add in-memory MCP CallTool tests"), instead of
// calling the handler methods directly the way the rest of this file's tests
// do. It returns the connected ClientSession and a cleanup func.
func connectInMemory(t *testing.T, s *Server) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()
	clientTr, serverTr := mcp.NewInMemoryTransports()
	ss, err := s.srv.Connect(ctx, serverTr, nil)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	client := mcp.NewClient(&mcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := client.Connect(ctx, clientTr, nil)
	if err != nil {
		ss.Close()
		t.Fatalf("client connect: %v", err)
	}
	return cs, func() {
		_ = cs.Close()
		_ = ss.Close()
	}
}

// callToolStructured runs one CallTool over the wire and returns its
// StructuredContent as a map, failing the test on a transport error or an
// unexpected content type (the server's own `result` helper always produces
// a JSON object).
func callToolStructured(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) map[string]any {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("CallTool(%s): StructuredContent type = %T, want map[string]any (content=%+v)", name, res.StructuredContent, res.Content)
	}
	return m
}

// TestCallToolMutatingToolsRefuseWithoutConfirmOnWire is a real, in-memory
// MCP wire round trip (not a direct handler call) verifying all four
// mutating tools refuse a call that doesn't carry `confirm: true` — the
// MCP-side safety gate documented in this package's own doc comment. Two
// distinct paths both need covering (see AGENTS.md's mutating-MCP-tools
// checklist): the SDK's own typed-schema validation rejects a request that
// OMITS `confirm` outright (a required property) before the handler ever
// runs, and the handler's own requireConfirm re-check refuses a
// hand-built/schema-bypassing request that sends `confirm: false`
// explicitly — both must end in a refusal an agent can act on, never a
// silent mutation.
func TestCallToolMutatingToolsRefuseWithoutConfirmOnWire(t *testing.T) {
	s := NewServer(&Service{
		Kill: func(int32, bool) (kill.Result, error) {
			t.Fatal("Kill must not be called without confirm")
			return kill.Result{}, nil
		},
		Profile: func(context.Context, int32, profiler.ProfileType, string, bool) (profiler.Profile, profiler.Receipt, error) {
			t.Fatal("Profile must not be called without confirm")
			return profiler.Profile{}, profiler.Receipt{}, nil
		},
		Investigate: func(context.Context, int32, InvestigateOptions) map[string]any {
			t.Fatal("Investigate must not be called without confirm")
			return nil
		},
		Record: func(context.Context, int32, int) (string, error) {
			t.Fatal("Record must not be called without confirm")
			return "", nil
		},
	}, "test")
	cs, cleanup := connectInMemory(t, s)
	defer cleanup()

	tools := []string{"monitor_kill", "monitor_profile_capture", "monitor_investigate", "monitor_record"}

	for _, tool := range tools {
		t.Run(tool+"/omitted", func(t *testing.T) {
			// The MCP SDK itself rejects this before the handler runs:
			// `confirm` has no `omitempty` in every *Input struct, so it is
			// a required property in the generated JSON schema.
			res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: map[string]any{"pid": 999999}})
			if err != nil {
				t.Fatalf("CallTool(%s): transport error %v", tool, err)
			}
			if !res.IsError {
				t.Fatalf("%s: IsError = false for a request missing confirm; content=%+v", tool, res.Content)
			}
			var text string
			if len(res.Content) > 0 {
				if tc, ok := res.Content[0].(*mcp.TextContent); ok {
					text = tc.Text
				}
			}
			if !strings.Contains(text, "confirm") {
				t.Errorf("%s: rejection text = %q, want it to mention the missing confirm field", tool, text)
			}
		})
		t.Run(tool+"/explicitFalse", func(t *testing.T) {
			// A schema-valid call (confirm present, just false) reaches the
			// handler, which must refuse it with its own structured
			// {refused:true, reason} payload — not a protocol-level error —
			// so a hand-built request that bypasses SDK-side validation
			// still gets a refusal an agent can inspect programmatically.
			m := callToolStructured(t, cs, tool, map[string]any{"pid": 999999, "confirm": false})
			if refused, _ := m["refused"].(bool); !refused {
				t.Fatalf("%s: refused=%v (want true) with confirm:false; payload=%v", tool, m["refused"], m)
			}
			if reason, _ := m["reason"].(string); reason == "" {
				t.Errorf("%s: reason is empty; payload=%v", tool, m)
			}
		})
	}
}

// TestCallToolProfileCaptureLiveNodeInspectorReturnsCPULines is a real,
// in-memory MCP wire round trip against a REAL node --inspect process (not
// a stub): monitor_profile_capture with type:cpu must come back with
// method inspector_cpu and symbols carrying file:line (AC-6 / E1.7 — "CDP
// line-level profiles of Node/Deno processes started with --inspect").
func TestCallToolProfileCaptureLiveNodeInspectorReturnsCPULines(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	workload, err := filepath.Abs(filepath.Join("..", "..", "examples", "polyglot", "js", "workload.js"))
	if err != nil || !fileExists(workload) {
		t.Skipf("workload fixture not found at %s", workload)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve a free port: %v", err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()

	// --jitless (interpreter only, no JIT inlining) matches
	// specs/profile_node_lines.yml's own documented workaround for stable
	// per-line attribution against this exact fixture.
	cmd := exec.Command(nodeBin, "--jitless", "--inspect="+addr, workload)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start node: %v", err)
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
		_ = cmd.Wait()
	}()
	pid := int32(cmd.Process.Pid)

	deadline := time.Now().Add(10 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		if conn, dialErr := net.DialTimeout("tcp", addr, 200*time.Millisecond); dialErr == nil {
			_ = conn.Close()
			ready = true
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		t.Fatalf("node inspector never came up on %s; stderr so far:\n%s", addr, stderr.String())
	}
	// Let the workload's setInterval hot loop accumulate real samples
	// before the profiler window starts.
	time.Sleep(300 * time.Millisecond)

	svc := &Service{
		// NOTE: this stub deliberately mirrors ONLY the CDP capture
		// mechanics (VerifyInspectorOwnership + ProfileInspector), not the
		// production runtime-aware dispatch (procbind.Inspect +
		// captureRuntimeAwareProfile's Bun/pprof/sample fallback logic),
		// which lives in the cli package and would be an import cycle to
		// call from here. cli/mcp_test.go's
		// TestProfileServiceLiveNodeInspectorViaRealDispatch exercises that
		// real dispatch (via the same buildProfileService the production
		// `monitor mcp serve` wires in) over its own in-memory transport;
		// this test's job is narrower — the MCP wire/schema round trip
		// (tool registration, typed input, structuredContent shape) against
		// a REAL node --inspect process, not a synthetic Profile.
		Profile: func(ctx context.Context, pid int32, ptype profiler.ProfileType, pprofAddr string, keep bool) (profiler.Profile, profiler.Receipt, error) {
			if ptype != profiler.ProfileCPU {
				return profiler.Profile{}, profiler.Receipt{}, fmt.Errorf("unexpected profile type %q", ptype)
			}
			if own, detail := profiler.VerifyInspectorOwnership(ctx, pid, addr); own != profiler.OwnershipOwned {
				return profiler.Profile{}, profiler.Receipt{}, fmt.Errorf("inspector %s not proven to belong to pid %d: %s", addr, pid, detail)
			}
			prof, err := profiler.ProfileInspector(ctx, pid, addr, 2*time.Second)
			if err != nil {
				return profiler.Profile{}, profiler.Receipt{}, err
			}
			receipt := prof.VerifyArtifact()
			if !keep {
				_ = prof.DiscardRawArtifact()
			}
			return prof, receipt, nil
		},
	}
	s := NewServer(svc, "test")
	cs, cleanup := connectInMemory(t, s)
	defer cleanup()

	m := callToolStructured(t, cs, "monitor_profile_capture", map[string]any{
		"pid": pid, "type": "cpu", "confirm": true,
	})
	if captured, _ := m["captured"].(bool); !captured {
		t.Fatalf("captured = %v, want true; payload=%v", m["captured"], m)
	}
	profileMap, ok := m["profile"].(map[string]any)
	if !ok {
		t.Fatalf("profile field missing or wrong type (%T); payload=%v", m["profile"], m)
	}
	if profileMap["method"] != "inspector_cpu" {
		t.Fatalf("profile.method = %v, want inspector_cpu; payload=%v", profileMap["method"], m)
	}
	symbols, ok := profileMap["symbols"].([]any)
	if !ok || len(symbols) == 0 {
		t.Fatalf("profile.symbols = %v, want at least one CDP symbol", profileMap["symbols"])
	}
	first, ok := symbols[0].(map[string]any)
	if !ok {
		t.Fatalf("symbols[0] type = %T, want map[string]any", symbols[0])
	}
	if _, ok := first["line"]; !ok {
		t.Errorf("symbols[0] missing a 'line' field (must carry file:line, not just a function name): %+v", first)
	}
	if _, ok := first["func"]; !ok {
		t.Errorf("symbols[0] missing a 'func' field: %+v", first)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
