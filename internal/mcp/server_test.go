package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
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
		Kill: func(int32, bool) error {
			t.Fatalf("Kill must not be called without confirm")
			return nil
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
		Kill: func(pid int32, force bool) error {
			called = true
			if pid != 4321 {
				t.Errorf("Kill received pid=%d, want 4321", pid)
			}
			if !force {
				t.Errorf("Kill received force=false, want true")
			}
			return nil
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
}

// TestHandleKillErrorPropagation verifies that errors from the kill
// service are surfaced via the structured result (not as hard Go errors),
// matching the convention used for confirm refusals.
func TestHandleKillErrorPropagation(t *testing.T) {
	s := newTestServer(t, &Service{
		Kill: func(int32, bool) error { return errors.New("boom") },
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
		Kill: func(int32, bool) error {
			t.Fatalf("Kill must not be called for protected process; safety check should short-circuit")
			return nil
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
			return profiler.Profile{PID: pid, Type: ptype, Taken: time.Now()}, nil
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
