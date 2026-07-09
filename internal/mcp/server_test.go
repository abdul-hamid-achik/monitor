package mcp

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
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
	return NewServer(svc)
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
		Investigate: func(_ context.Context, pid int32) map[string]any {
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
		Profile: func(context.Context, int32, profiler.ProfileType) (profiler.Profile, error) {
			t.Fatalf("Profile must not be called without confirm")
			return profiler.Profile{}, nil
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
		Profile: func(_ context.Context, pid int32, ptype profiler.ProfileType) (profiler.Profile, error) {
			got = ptype
			return profiler.Profile{PID: pid, Type: ptype, Taken: time.Now(), Text: "heap profile: 1"}, nil
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
		Profile: func(context.Context, int32, profiler.ProfileType) (profiler.Profile, error) {
			return profiler.Profile{PID: 7, Type: "heap"}, nil
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
		Profile: func(context.Context, int32, profiler.ProfileType) (profiler.Profile, error) {
			return profiler.Profile{PID: 7, Type: "heap", Text: "heap profile: 1"}, nil
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

// TestHandleInvestigateRefusesWithoutConfirm verifies the confirm gate
// for monitor_investigate.
func TestHandleInvestigateRefusesWithoutConfirm(t *testing.T) {
	s := newTestServer(t, &Service{
		Investigate: func(context.Context, int32) map[string]any {
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
				Investigate: func(context.Context, int32) map[string]any {
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
		Investigate: func(_ context.Context, pid int32) map[string]any {
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
