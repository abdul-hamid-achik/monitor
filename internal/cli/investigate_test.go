package cli

import (
	"context"
	"errors"
	"runtime"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

// restoreStubs saves the package-level stub points and returns a func that
// restores them, so every test that swaps them can `defer restoreStubs(t)()`.
func restoreStubs() func() {
	origOwnership, origCapture, origIncidents := verifyOwnership, captureProfile, incidentsCapture
	return func() {
		verifyOwnership, captureProfile, incidentsCapture = origOwnership, origCapture, origIncidents
	}
}

func TestComputeVerdict(t *testing.T) {
	tests := []struct {
		name  string
		steps []investigateStep
		want  string
	}{
		{name: "no steps", steps: nil, want: "complete"},
		{name: "all ok", steps: []investigateStep{{Status: stepOK}, {Status: stepOK}}, want: "complete"},
		{name: "ok and skipped", steps: []investigateStep{{Status: stepOK}, {Status: stepSkipped}}, want: "complete"},
		{name: "one failed among ok", steps: []investigateStep{{Status: stepOK}, {Status: stepFailed}, {Status: stepOK}}, want: "partial"},
		{name: "all failed", steps: []investigateStep{{Status: stepFailed}, {Status: stepFailed}}, want: "partial"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := computeVerdict(tt.steps); got != tt.want {
				t.Errorf("computeVerdict() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCaptureInvestigateProfileUsesPprofWhenOwned(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipOwned, ""
	}
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		if ptype != profiler.ProfileHeap {
			t.Fatalf("captureProfile called with type %q, want heap", ptype)
		}
		return profiler.Profile{PID: pid, Type: profiler.ProfileHeap, Text: "heap profile: 1"}, nil
	}

	_, method, step := captureInvestigateProfile(context.Background(), 42)
	if step.Status != stepOK {
		t.Fatalf("step.Status = %q, want ok (limitation=%q)", step.Status, step.Limitation)
	}
	if method != "pprof_heap" {
		t.Errorf("method = %q, want pprof_heap", method)
	}
}

func TestCaptureInvestigateProfilePrefersSampleWhenNotOwned(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sample only available on macOS")
	}
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipNotOwned, "port 6060 is owned by pid 7, not pid 42"
	}
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		if ptype == profiler.ProfileHeap {
			t.Fatalf("captureProfile must not scrape heap when ownership is not proven")
		}
		return profiler.Profile{PID: pid, Type: profiler.ProfileSample, Text: "Sampling process"}, nil
	}

	_, method, step := captureInvestigateProfile(context.Background(), 42)
	if step.Status != stepOK {
		t.Fatalf("step.Status = %q, want ok (limitation=%q)", step.Status, step.Limitation)
	}
	if method != "sample" {
		t.Errorf("method = %q, want sample", method)
	}
	if !strings.Contains(step.Limitation, "not proven") {
		t.Errorf("Limitation = %q, want it to mention 'not proven'", step.Limitation)
	}
}

func TestCaptureInvestigateProfileFallsBackWhenHeapEmpty(t *testing.T) {
	if runtime.GOOS != "darwin" {
		t.Skip("sample only available on macOS")
	}
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipOwned, ""
	}
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		if ptype == profiler.ProfileHeap {
			return profiler.Profile{}, nil // no error, but empty -> receipt fails
		}
		return profiler.Profile{PID: pid, Type: profiler.ProfileSample, Text: "Sampling process"}, nil
	}

	_, method, step := captureInvestigateProfile(context.Background(), 42)
	if step.Status != stepOK {
		t.Fatalf("step.Status = %q, want ok (limitation=%q)", step.Status, step.Limitation)
	}
	if method != "sample" {
		t.Errorf("method = %q, want sample", method)
	}
}

func TestCaptureInvestigateProfileBothFail(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipUnknown, "insufficient permissions"
	}
	captureProfile = func(context.Context, int32, profiler.ProfileType, string) (profiler.Profile, error) {
		return profiler.Profile{}, errors.New("boom")
	}

	_, method, step := captureInvestigateProfile(context.Background(), 42)
	if step.Status != stepFailed {
		t.Fatalf("step.Status = %q, want failed", step.Status)
	}
	if step.Limitation == "" {
		t.Error("Limitation should be non-empty on failure")
	}
	if step.Recovery == "" {
		t.Error("Recovery should be non-empty on failure")
	}
	if method != "" {
		t.Errorf("method = %q, want empty on failure", method)
	}
}

func TestInvestigatePipelineNeverStashesEmptyProfile(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipNotOwned, "nothing is listening"
	}
	captureProfile = func(context.Context, int32, profiler.ProfileType, string) (profiler.Profile, error) {
		return profiler.Profile{}, errors.New("boom")
	}
	var gotReq incidents.CaptureRequest
	incidentsCapture = func(_ context.Context, req incidents.CaptureRequest) (incidents.CaptureResult, error) {
		gotReq = req
		return incidents.CaptureResult{StashID: "s1"}, nil
	}

	report := investigatePipeline(context.Background(), 999999, "7d", false)

	if gotReq.Profile.PID != 0 {
		t.Errorf("stashed request carried a non-empty profile: %+v", gotReq.Profile)
	}
	if report.Verdict != "partial" {
		t.Errorf("Verdict = %q, want partial", report.Verdict)
	}
	var profileStep, stashStep *investigateStep
	for i := range report.Steps {
		switch report.Steps[i].Step {
		case "profile":
			profileStep = &report.Steps[i]
		case "stash":
			stashStep = &report.Steps[i]
		}
	}
	if profileStep == nil || profileStep.Status != stepFailed {
		t.Errorf("profile step = %+v, want status failed", profileStep)
	}
	if stashStep == nil || stashStep.Status != stepOK {
		t.Errorf("stash step = %+v, want status ok", stashStep)
	}
}

func TestInvestigatePipelineNoSaveSkipsStash(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipOwned, ""
	}
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		return profiler.Profile{PID: pid, Type: ptype, Text: "heap profile: 1"}, nil
	}
	incidentsCapture = func(context.Context, incidents.CaptureRequest) (incidents.CaptureResult, error) {
		t.Fatalf("incidentsCapture must not be called when noSave is true")
		return incidents.CaptureResult{}, nil
	}

	report := investigatePipeline(context.Background(), 42, "7d", true)

	var stashStep *investigateStep
	for i := range report.Steps {
		if report.Steps[i].Step == "stash" {
			stashStep = &report.Steps[i]
		}
	}
	if stashStep == nil || stashStep.Status != stepSkipped {
		t.Errorf("stash step = %+v, want status skipped", stashStep)
	}
	if report.Verdict != "complete" {
		t.Errorf("Verdict = %q, want complete", report.Verdict)
	}
	if report.Profile == nil {
		t.Error("report.Profile should be populated when the profile step succeeds")
	}
}

// TestInvestigatePipelineStashFailureRecoveryHint verifies the stash step's
// Recovery message points at `monitor incidents resume-stash <id>` when the
// failed capture registered a bundle (RegistryID set), and falls back to the
// pre-registry wording otherwise.
func TestInvestigatePipelineStashFailureRecoveryHint(t *testing.T) {
	tests := []struct {
		name       string
		result     incidents.CaptureResult
		wantSubstr string
		wantAbsent string
	}{
		{
			name:       "registered bundle points at resume-stash",
			result:     incidents.CaptureResult{Path: "/state/monitor/incidents/abc123/bundle", RegistryID: "abc123def456"},
			wantSubstr: "monitor incidents resume-stash abc123def456",
		},
		{
			name:       "unregistered bundle keeps the manual-save wording",
			result:     incidents.CaptureResult{Path: "/tmp/monitor-incident-xyz"},
			wantSubstr: "save it manually with 'fcheap save'",
			wantAbsent: "resume-stash",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			defer restoreStubs()()
			verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
				return profiler.OwnershipOwned, ""
			}
			captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
				return profiler.Profile{PID: pid, Type: ptype, Text: "heap profile: 1"}, nil
			}
			incidentsCapture = func(context.Context, incidents.CaptureRequest) (incidents.CaptureResult, error) {
				return tt.result, errors.New("stash failed")
			}

			report := investigatePipeline(context.Background(), 42, "7d", false)

			var stashStep *investigateStep
			for i := range report.Steps {
				if report.Steps[i].Step == "stash" {
					stashStep = &report.Steps[i]
				}
			}
			if stashStep == nil || stashStep.Status != stepFailed {
				t.Fatalf("stash step = %+v, want status failed", stashStep)
			}
			if !strings.Contains(stashStep.Recovery, tt.wantSubstr) {
				t.Errorf("Recovery = %q, want it to contain %q", stashStep.Recovery, tt.wantSubstr)
			}
			if tt.wantAbsent != "" && strings.Contains(stashStep.Recovery, tt.wantAbsent) {
				t.Errorf("Recovery = %q, should not contain %q", stashStep.Recovery, tt.wantAbsent)
			}
		})
	}
}

func TestInvestigateReportToMapSnakeCase(t *testing.T) {
	report := investigateReport{
		PID:       7,
		StartedAt: "2026-01-01T00:00:00Z",
		Verdict:   "complete",
		Steps:     []investigateStep{{Step: "snapshot", Status: stepOK}},
	}
	m := report.toMap()
	for _, key := range []string{"pid", "started_at", "verdict", "steps"} {
		if _, ok := m[key]; !ok {
			t.Errorf("toMap() missing key %q: %v", key, m)
		}
	}
	steps, ok := m["steps"].([]any)
	if !ok || len(steps) != 1 {
		t.Fatalf("steps = %v, want a 1-element slice", m["steps"])
	}
	step0, ok := steps[0].(map[string]any)
	if !ok {
		t.Fatalf("steps[0] type = %T, want map[string]any", steps[0])
	}
	for _, key := range []string{"step", "status"} {
		if _, ok := step0[key]; !ok {
			t.Errorf("steps[0] missing key %q: %v", key, step0)
		}
	}
}
