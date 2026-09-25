package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

// restoreStubs saves the package-level stub points and returns a func that
// restores them, so every test that swaps them can `defer restoreStubs(t)()`.
func restoreStubs() func() {
	origOwnership, origCapture, origIncidents, origValidate, origRecord := verifyOwnership, captureProfile, incidentsCapture, validateProfile, recordIssueOccurrence
	recordIssueOccurrence = func(*investigateReport) (issues.Issue, issues.Occurrence, error) {
		return issues.Issue{ID: "ISS-TEST"}, issues.Occurrence{ID: "OCC-TEST", IssueID: "ISS-TEST"}, nil
	}
	return func() {
		verifyOwnership, captureProfile, incidentsCapture, validateProfile = origOwnership, origCapture, origIncidents, origValidate
		recordIssueOccurrence = origRecord
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
		{name: "unavailable does not flip to partial", steps: []investigateStep{{Status: stepOK}, {Status: stepUnavailable}}, want: "complete"},
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

	_, method, step := captureInvestigateProfile(context.Background(), 42, nil)
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

	_, method, step := captureInvestigateProfile(context.Background(), 42, nil)
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

	_, method, step := captureInvestigateProfile(context.Background(), 42, nil)
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

	_, method, step := captureInvestigateProfile(context.Background(), 42, nil)
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

func TestCaptureInvestigateProfileBlocksUnsupportedFallback(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipNotOwned, "no owned endpoint"
	}
	validateProfile = func(profiler.ProfileType) error {
		return errors.New("capability profile_sample is unsupported")
	}
	captureProfile = func(context.Context, int32, profiler.ProfileType, string) (profiler.Profile, error) {
		t.Fatal("unsupported sample capture must be blocked before collection")
		return profiler.Profile{}, nil
	}
	_, _, step := captureInvestigateProfile(context.Background(), 42, nil)
	if step.Status != stepFailed || !strings.Contains(step.Limitation, "unsupported") {
		t.Fatalf("step = %+v, want failed unsupported limitation", step)
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

	report := investigatePipeline(context.Background(), 999999, InvestigateOptions{TTL: "7d", NoSave: false})

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

	report := investigatePipeline(context.Background(), 42, InvestigateOptions{TTL: "7d", NoSave: true})

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

			report := investigatePipeline(context.Background(), 42, InvestigateOptions{TTL: "7d", NoSave: false})

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

// TestInvestigateReportJSONShapeStableForDownstreamConsumers is a byte-exact
// GOLDEN test of investigate --json's top-level shape (not a key-presence
// check): Chalupa's runtime/ci-engine (chalupa-ci.py) reads
// stash.artifact_ref, and cairntrace reads it too. E1.7's payload diet only
// ever touches profile.text; comparing the full marshaled JSON against a
// literal golden string catches ANY drift — a renamed/moved/added/removed
// field anywhere in the tree, not just the 7 top-level keys a presence
// check would have missed a move within — while redactRaw's own field
// addition/removal stays proven to be limited to profile.text exactly as
// documented.
func TestInvestigateReportJSONShapeStableForDownstreamConsumers(t *testing.T) {
	report := investigateReport{
		PID: 7, StartedAt: "2026-01-01T00:00:00Z", Verdict: "complete",
		ProfileMethod: "inspector_cpu",
		Steps:         []investigateStep{{Step: "profile", Status: stepOK}},
		Profile:       &profiler.Profile{PID: 7, Type: profiler.ProfileCPU, Text: "raw CDP JSON"},
		Stash: &incidents.CaptureResult{
			TreeHash:    strings.Repeat("a", 64),
			ArtifactRef: map[string]any{"uri": "fcheap://stash/stash-1"},
		},
	}

	// toMap() JSON-round-trips through map[string]any (snake_case for the
	// MCP surface), which — unlike marshaling the investigateReport struct
	// directly — sorts keys alphabetically; this golden matches that exact
	// shape, since toMap() is what monitor_investigate actually returns.
	wantFull := `{
  "context": {},
  "pid": 7,
  "profile": {
    "context": {},
    "pid": 7,
    "taken": "0001-01-01T00:00:00Z",
    "text": "raw CDP JSON",
    "type": "cpu"
  },
  "profile_method": "inspector_cpu",
  "started_at": "2026-01-01T00:00:00Z",
  "stash": {
    "artifact_ref": {
      "uri": "fcheap://stash/stash-1"
    },
    "created_at": "0001-01-01T00:00:00Z",
    "path": "",
    "size_bytes": 0,
    "stash_id": "",
    "tags": null,
    "tree_hash": "` + strings.Repeat("a", 64) + `"
  },
  "steps": [
    {
      "status": "ok",
      "step": "profile"
    }
  ],
  "verdict": "complete"
}`
	// redactRaw(false) (the default: --include-raw / include_raw not set)
	// differs from the full golden ONLY by profile.text's absence —
	// everything else, including the stash.artifact_ref path chalupa-ci.py
	// and cairntrace read, is byte-identical.
	wantRedacted := strings.Replace(wantFull, "\n    \"text\": \"raw CDP JSON\",", "", 1)

	assertJSONGolden(t, "report.toMap()", report.toMap(), wantFull)
	assertJSONGolden(t, "report.redactRaw(false).toMap()", report.redactRaw(false).toMap(), wantRedacted)
	// redactRaw(true) (--include-raw / include_raw:true) is byte-identical
	// to the unredacted full report.
	assertJSONGolden(t, "report.redactRaw(true).toMap()", report.redactRaw(true).toMap(), wantFull)
}

// assertJSONGolden re-marshals got (already a map[string]any from toMap's
// own JSON round-trip) with the same indentation the CLI's investigate --json
// and MCP's monitor_investigate use, and fails with a full diff on any
// byte-level mismatch against want.
func assertJSONGolden(t *testing.T, label string, got map[string]any, want string) {
	t.Helper()
	b, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("%s: MarshalIndent: %v", label, err)
	}
	if string(b) != want {
		t.Fatalf("%s shape drifted from the golden.\n--- got ---\n%s\n--- want ---\n%s", label, b, want)
	}
}

func TestRecordInvestigateOccurrenceGroupsRunsAndKeepsEvidence(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	t.Setenv(issues.StorePathEnv, storePath)
	// A real directory with a .git marker: project.Resolve walks the
	// filesystem (unlike the old ad hoc filepath.Base(CodebaseRoot)), so
	// CodebaseRoot must actually exist and look like a repo root for the
	// project derivation to still land on "chalupa".
	codebaseRoot := filepath.Join(t.TempDir(), "chalupa")
	if err := os.MkdirAll(filepath.Join(codebaseRoot, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	report := investigateReport{
		PID: 42, StartedAt: "2026-07-27T12:00:00Z", ProfileMethod: "pprof_heap",
		Process: &procbind.Binding{Name: "api", Runtime: procbind.RuntimeGo, CodebaseRoot: codebaseRoot},
		Context: contextids.IDs{Environment: "preview", RunID: "run-1", StepID: "test", Suite: "pr", Attempt: "1", Release: "v1"},
		Profile: &profiler.Profile{Symbols: []profiler.Symbol{{Func: "main.serve"}, {Func: "main.serve"}}},
		Stash:   &incidents.CaptureResult{TreeHash: strings.Repeat("a", 64), ArtifactRef: map[string]any{"uri": "fcheap://stash/stash-1"}},
	}
	first, occurrence, err := recordInvestigateOccurrence(&report)
	if err != nil {
		t.Fatal(err)
	}
	if first.Project != "chalupa" || occurrence.RunID != "run-1" || len(occurrence.EvidenceRefs) != 1 || occurrence.EvidenceRefs[0] != "fcheap://stash/stash-1" {
		t.Fatalf("first occurrence mapping = issue %+v occurrence %+v", first, occurrence)
	}
	if occurrence.Metadata["step_id"] != "test" || occurrence.Metadata["suite"] != "pr" || occurrence.Metadata["attempt"] != "1" {
		t.Fatalf("Chalupa metadata missing: %v", occurrence.Metadata)
	}
	report.PID = 99
	report.Context.RunID = "run-2"
	report.Context.Release = "v2"
	second, _, err := recordInvestigateOccurrence(&report)
	if err != nil {
		t.Fatal(err)
	}
	if second.ID != first.ID || second.OccurrenceCount != 2 {
		t.Fatalf("dynamic run data split the issue: first=%+v second=%+v", first, second)
	}
}

func TestInvestigateIssuePersistenceFailureMakesVerdictPartial(t *testing.T) {
	defer restoreStubs()()
	recordIssueOccurrence = func(*investigateReport) (issues.Issue, issues.Occurrence, error) {
		return issues.Issue{}, issues.Occurrence{}, errors.New("permission denied")
	}
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipOwned, ""
	}
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		return profiler.Profile{PID: pid, Type: ptype, Text: "heap profile: 1"}, nil
	}
	report := investigatePipeline(context.Background(), 42, InvestigateOptions{NoSave: true, SkipSemantic: true, SkipCorrelate: true})
	if report.Verdict != "partial" || report.IssueError != "permission denied" {
		t.Fatalf("report = %+v", report)
	}
	if got := report.Steps[len(report.Steps)-1]; got.Step != "issue" || got.Status != stepFailed {
		t.Fatalf("issue step = %+v", got)
	}
}

// gitCodebase creates a fresh temp directory with a .git marker so
// project.Resolve (and stacktrace.InApp, which needs a real gitRoot) treat
// it as a real project root, and returns it alongside a file path nested
// under it.
func gitCodebase(t *testing.T) (root, hotFile string) {
	t.Helper()
	root = filepath.Join(t.TempDir(), "app")
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	return root, filepath.Join(root, "src", "hot.js")
}

// TestInvestigateFingerprintStableAcrossSampling verifies bug 11: sampling
// noise reshuffling the low-weight tail of a CPU profile between
// consecutive investigate runs over the SAME process must not open a new
// issue each time, as long as the dominant in-app function stays the same.
func TestInvestigateFingerprintStableAcrossSampling(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	t.Setenv(issues.StorePathEnv, storePath)
	codebaseRoot, hotFile := gitCodebase(t)

	// Three sampled profiles of the same live process: the dominant
	// function (processRow, well over the 15% threshold once its two hot
	// lines are aggregated) is stable, but the low-weight tail — which
	// functions appear at all, their order, even a near-zero symbol
	// appearing in one run and not another — is exactly the kind of
	// sampling jitter that used to open a new issue on almost every run
	// when the old fingerprint hashed the whole top-10.
	runs := [][]profiler.Symbol{
		{
			{Func: "processRow", File: hotFile, Line: 12, Weight: 42.1},
			{Func: "processRow", File: hotFile, Line: 14, Weight: 5.0},
			{Func: "parseHeader", File: hotFile, Line: 3, Weight: 4.0},
			{Func: "(garbage collector)", File: "", Line: 0, Weight: 3.0},
		},
		{
			{Func: "processRow", File: hotFile, Line: 12, Weight: 39.7},
			{Func: "processRow", File: hotFile, Line: 14, Weight: 6.3},
			{Func: "otherHelper", File: hotFile, Line: 50, Weight: 2.1},
		},
		{
			{Func: "processRow", File: hotFile, Line: 12, Weight: 45.0},
			{Func: "parseHeader", File: hotFile, Line: 3, Weight: 9.9},
			{Func: "processRow", File: hotFile, Line: 14, Weight: 4.4},
			{Func: "rareThing", File: hotFile, Line: 99, Weight: 0.4},
		},
	}

	var firstID string
	var lastIssue issues.Issue
	for i, syms := range runs {
		report := &investigateReport{
			PID: int32(100 + i), StartedAt: "2026-01-01T00:00:00Z",
			Process: &procbind.Binding{Name: "api", Runtime: procbind.RuntimeNode, CodebaseRoot: codebaseRoot, Cwd: codebaseRoot},
			Profile: &profiler.Profile{Symbols: syms},
		}
		issue, occurrence, err := recordInvestigateOccurrence(report)
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		if len(issue.Symbols) != 1 || issue.Symbols[0] != "processRow" {
			t.Fatalf("run %d: issue.Symbols = %v, want [processRow] (dominant symbol only)", i, issue.Symbols)
		}
		if len(occurrence.Symbols) != 1 || occurrence.Symbols[0] != "processRow" {
			t.Fatalf("run %d: occurrence.Symbols = %v, want [processRow]", i, occurrence.Symbols)
		}
		if firstID == "" {
			firstID = issue.ID
		} else if issue.ID != firstID {
			t.Fatalf("run %d opened a new issue: got %s, want %s", i, issue.ID, firstID)
		}
		lastIssue = issue
	}
	if lastIssue.OccurrenceCount != int64(len(runs)) {
		t.Fatalf("OccurrenceCount = %d, want %d (all %d runs grouped into one issue)", lastIssue.OccurrenceCount, len(runs), len(runs))
	}
}

// TestInvestigateFingerprintIgnoresPseudoFrames verifies that even a
// pseudo/synthetic frame name that (hypothetically) carries the highest
// weight in a profile can never become the fingerprinted "dominant"
// symbol — defense in depth alongside the profiler's own pseudo-frame
// filtering (E1.1's flattenCDPProfile already excludes these).
func TestInvestigateFingerprintIgnoresPseudoFrames(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	t.Setenv(issues.StorePathEnv, storePath)
	codebaseRoot, hotFile := gitCodebase(t)

	report := &investigateReport{
		PID: 42, StartedAt: "2026-01-01T00:00:00Z",
		Process: &procbind.Binding{Name: "api", Runtime: procbind.RuntimeNode, CodebaseRoot: codebaseRoot, Cwd: codebaseRoot},
		Profile: &profiler.Profile{Symbols: []profiler.Symbol{
			{Func: "(garbage collector)", File: hotFile, Line: 1, Weight: 80},
			{Func: "parseHeader", File: hotFile, Line: 3, Weight: 20},
		}},
	}
	issue, occurrence, err := recordInvestigateOccurrence(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrence.Symbols) != 1 || occurrence.Symbols[0] != "parseHeader" {
		t.Fatalf("Symbols = %v, want [parseHeader] (the pseudo frame must never dominate despite its higher weight)", occurrence.Symbols)
	}
	if issue.Message != "manual process investigation" {
		t.Fatalf("Message = %q, want the non-diffuse default", issue.Message)
	}
}

// TestInvestigateFingerprintDiffuseProfile verifies that when no single
// in-app function clears the 15% dominance threshold, Symbols is left
// empty and Message becomes "diffuse cpu profile" instead of an arbitrary
// pick — and that the full ranked list still lands in Metadata for a human
// or agent reading the issue, just never in the hash.
func TestInvestigateFingerprintDiffuseProfile(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	t.Setenv(issues.StorePathEnv, storePath)
	codebaseRoot, hotFile := gitCodebase(t)

	report := &investigateReport{
		PID: 42, StartedAt: "2026-01-01T00:00:00Z",
		Process: &procbind.Binding{Name: "api", Runtime: procbind.RuntimeNode, CodebaseRoot: codebaseRoot, Cwd: codebaseRoot},
		Profile: &profiler.Profile{Symbols: []profiler.Symbol{
			{Func: "fnA", File: hotFile, Line: 1, Weight: 8},
			{Func: "fnB", File: hotFile, Line: 2, Weight: 7},
			{Func: "fnC", File: hotFile, Line: 3, Weight: 6},
			{Func: "fnD", File: hotFile, Line: 4, Weight: 5},
		}},
	}
	issue, occurrence, err := recordInvestigateOccurrence(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrence.Symbols) != 0 {
		t.Fatalf("Symbols = %v, want empty for a diffuse profile", occurrence.Symbols)
	}
	if issue.Message != "diffuse cpu profile" {
		t.Fatalf("Message = %q, want %q", issue.Message, "diffuse cpu profile")
	}
	if !strings.Contains(occurrence.Metadata["profile_symbols"], "fnA@") {
		t.Fatalf("Metadata[profile_symbols] = %q, want it to include the full ranked list", occurrence.Metadata["profile_symbols"])
	}
}

// TestDominantInAppSymbolSkipsFramesWithNoFile verifies macOS `sample`
// frames (which carry no file:line at all — see profiler's own doc
// comments) can never be picked as "dominant": InApp requires a file.
func TestDominantInAppSymbolSkipsFramesWithNoFile(t *testing.T) {
	_, gitRoot := gitCodebase(t)
	dominant, all := dominantInAppSymbol([]profiler.Symbol{
		{Func: "mystery", File: "", Line: 0, Weight: 99},
	}, gitRoot)
	if dominant.Func != "" {
		t.Fatalf("dominant = %+v, want the zero value (no file, can't be in-app)", dominant)
	}
	if len(all) != 0 {
		t.Fatalf("all = %v, want empty (a symbol with no File is skipped entirely, not just excluded from dominance)", all)
	}
}

// TestCaptureRuntimeAwareProfileBunIsHonestNotA404 verifies bug 5: a Bun
// process requesting a cpu/heap profile never attempts CDP discovery
// against Bun's WebKit/JSC inspector (which would 404 on GET /json/list —
// Bun doesn't speak V8's CDP) and instead fails fast, status "unavailable"
// (a known, non-retryable reason — never "failed", which implies a retry
// or a different flag might help), with an honest limitation and recovery.
func TestCaptureRuntimeAwareProfileBunIsHonestNotA404(t *testing.T) {
	binding := &procbind.Binding{Runtime: procbind.RuntimeBun, InspectAddr: "localhost:6499"}
	for _, ptype := range []profiler.ProfileType{profiler.ProfileCPU, profiler.ProfileHeap} {
		_, method, step := captureRuntimeAwareProfile(context.Background(), 42, binding, ptype, "", "", false, 0, true)
		if step.Status != stepUnavailable {
			t.Fatalf("type %s: step.Status = %q, want unavailable", ptype, step.Status)
		}
		if method != "" {
			t.Errorf("type %s: method = %q, want empty", ptype, method)
		}
		if step.Limitation != bunInspectorLimitation {
			t.Errorf("type %s: Limitation = %q, want %q (never a raw 404)", ptype, step.Limitation, bunInspectorLimitation)
		}
		if step.Recovery != bunInspectorRecovery {
			t.Errorf("type %s: Recovery = %q, want %q", ptype, step.Recovery, bunInspectorRecovery)
		}
	}
}

// TestCaptureRuntimeAwareProfileBunUnavailableEvenWithoutInspectAddr
// verifies the Bun check runs BEFORE the missing-inspector-address check: a
// Bun process with no --inspect must still get the honest
// bunInspectorLimitation/bunInspectorRecovery pair, not the generic
// "start it with --inspect" message that would only lead a Bun user to the
// same JSC dead end (Bun never speaks CDP regardless of --inspect).
func TestCaptureRuntimeAwareProfileBunUnavailableEvenWithoutInspectAddr(t *testing.T) {
	binding := &procbind.Binding{Runtime: procbind.RuntimeBun}
	_, method, step := captureRuntimeAwareProfile(context.Background(), 42, binding, profiler.ProfileCPU, "", "", false, 0, true)
	if step.Status != stepUnavailable {
		t.Fatalf("step.Status = %q, want unavailable", step.Status)
	}
	if method != "" {
		t.Errorf("method = %q, want empty", method)
	}
	if step.Limitation != bunInspectorLimitation {
		t.Errorf("Limitation = %q, want %q (not the generic missing-inspector-address message)", step.Limitation, bunInspectorLimitation)
	}
}

// TestCaptureRuntimeAwareProfileHeapSkipsInspectorWhenNotAllowed verifies
// allowInspectorHeap:false (investigate's own fallback ladder, step 2)
// skips the whole jsRuntime-heap branch — including its Bun check — for a
// JS runtime and falls straight through to the generic pprof switch, which
// fails the normal ownership-not-proven way instead of attempting a CDP
// heap snapshot nobody asked for.
func TestCaptureRuntimeAwareProfileHeapSkipsInspectorWhenNotAllowed(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipNotOwned, "nothing is listening"
	}
	binding := &procbind.Binding{Runtime: procbind.RuntimeNode, InspectAddr: "localhost:9229"}
	_, method, step := captureRuntimeAwareProfile(context.Background(), 42, binding, profiler.ProfileHeap, "", "", false, 0, false)
	if step.Status != stepFailed {
		t.Fatalf("step.Status = %q, want failed (pprof ownership path, not CDP)", step.Status)
	}
	if method != "" {
		t.Errorf("method = %q, want empty", method)
	}
	if strings.Contains(step.Limitation, "CDP") || strings.Contains(step.Limitation, "inspector") {
		t.Errorf("Limitation = %q, must not mention the CDP/inspector path when allowInspectorHeap is false", step.Limitation)
	}
	if !strings.Contains(step.Limitation, "not proven to belong") {
		t.Errorf("Limitation = %q, want the generic pprof-ownership message", step.Limitation)
	}
}

// TestCaptureRuntimeAwareProfilePprofAddrExplicitRefusesNonLoopback
// verifies an explicit pprof_addr/--pprof-addr (which skips the ownership
// proof on the caller's behalf) is still refused when it names a
// non-loopback host — an agent must never be able to make monitor scrape
// an arbitrary remote host's /debug/pprof/*.
func TestCaptureRuntimeAwareProfilePprofAddrExplicitRefusesNonLoopback(t *testing.T) {
	_, method, step := captureRuntimeAwareProfile(context.Background(), 42, nil, profiler.ProfileHeap, "192.0.2.1:6060", "", true, 0, true)
	if step.Status != stepFailed {
		t.Fatalf("step.Status = %q, want failed", step.Status)
	}
	if method != "" {
		t.Errorf("method = %q, want empty", method)
	}
	if !strings.Contains(step.Limitation, "192.0.2.1:6060") {
		t.Errorf("Limitation = %q, want it to name the refused address", step.Limitation)
	}
}

// TestCaptureRuntimeAwareProfileNodeRequiresInspectAddr verifies a
// Node/Deno process with no known inspector address fails with a specific,
// actionable message rather than silently falling through to an unrelated
// pprof/sample attempt.
func TestCaptureRuntimeAwareProfileNodeRequiresInspectAddr(t *testing.T) {
	binding := &procbind.Binding{Runtime: procbind.RuntimeNode}
	_, method, step := captureRuntimeAwareProfile(context.Background(), 42, binding, profiler.ProfileCPU, "", "", false, 0, true)
	if step.Status != stepFailed || method != "" {
		t.Fatalf("step = %+v, method = %q, want a failed step with no method", step, method)
	}
	if !strings.Contains(step.Limitation, "no inspector address") {
		t.Errorf("Limitation = %q, want it to mention the missing inspector address", step.Limitation)
	}
}

// TestCaptureRuntimeAwareProfileTenCapturesLeaveNoTempFiles is the Go-level
// regression for E1.7's "10 captures leave no temp files" AC: it exercises
// the exact cleanup contract both the CLI (profile_logs.go's
// discardTempProfilePath) and MCP (buildProfileService's
// prof.DiscardRawArtifact call, in cli/mcp.go) rely on — the captured
// Profile.Path is the only thing standing between a repeated capture and a
// leaked /tmp/monitor-<type>-* file.
func TestCaptureRuntimeAwareProfileTenCapturesLeaveNoTempFiles(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipOwned, ""
	}
	tmpDir := t.TempDir()
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		f, err := os.CreateTemp(tmpDir, fmt.Sprintf("monitor-%s-%d-*.pb.gz", ptype, pid))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("fake raw profile bytes"); err != nil {
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		return profiler.Profile{PID: pid, Type: ptype, Path: f.Name(), Text: "heap profile: 1"}, nil
	}

	for i := 0; i < 10; i++ {
		prof, _, step := captureRuntimeAwareProfile(context.Background(), int32(9000+i), nil, profiler.ProfileHeap, "", "", false, 0, true)
		if step.Status != stepOK {
			t.Fatalf("capture %d: step = %+v", i, step)
		}
		if prof.Path == "" {
			t.Fatalf("capture %d: expected a temp file Path before cleanup", i)
		}
		if err := prof.DiscardRawArtifact(); err != nil {
			t.Fatalf("capture %d: DiscardRawArtifact: %v", i, err)
		}
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		names := make([]string, len(entries))
		for i, e := range entries {
			names[i] = e.Name()
		}
		t.Fatalf("temp dir has %d leftover file(s) after 10 captures: %v", len(entries), names)
	}
}

// TestDominantInAppSymbolExcludesBinaryImageNames verifies macOS `sample`
// frames whose File names the containing shared library/image (Bun's
// "libsystem_pthread.dylib" during a stuck pthread wait, Python's
// "libpython3.14.dylib" for the interpreter's own eval loop) are never
// picked as "dominant in-app" just because ".dylib" happens to look like a
// file extension to stacktrace.InApp's relative-path check.
func TestDominantInAppSymbolExcludesBinaryImageNames(t *testing.T) {
	root, hotFile := gitCodebase(t)
	for _, ext := range []string{".dylib", ".so", ".dll"} {
		dominant, all := dominantInAppSymbol([]profiler.Symbol{
			{Func: "_pthread_start", File: "libsystem_pthread" + ext, Weight: 100},
			{Func: "parseHeader", File: hotFile, Line: 3, Weight: 20},
		}, root)
		if dominant.Func != "parseHeader" {
			t.Fatalf("ext %s: dominant = %+v, want parseHeader (the %s image name must never win)", ext, dominant, ext)
		}
		if len(all) != 2 {
			t.Fatalf("ext %s: all = %v, want both symbols kept for metadata/evidence", ext, all)
		}
	}
}

// TestDominantInAppSymbolAnonymousFunctionCanDominate verifies V8's real
// "(anonymous)" function name (set for every anonymous closure —
// inspector.go) is never treated as a pseudo frame the way (idle)/
// (program)/(garbage collector) are: a profile whose hot path runs through
// one anonymous closure must be able to fingerprint on it, not fall back to
// "diffuse" just because its name happens to be wrapped in parens.
func TestDominantInAppSymbolAnonymousFunctionCanDominate(t *testing.T) {
	root, hotFile := gitCodebase(t)
	dominant, _ := dominantInAppSymbol([]profiler.Symbol{
		{Func: "(anonymous)", File: hotFile, Line: 3, Weight: 99},
		{Func: "parseHeader", File: hotFile, Line: 10, Weight: 1},
	}, root)
	if dominant.Func != "(anonymous)" {
		t.Fatalf("dominant = %+v, want (anonymous) (a real V8 function name, not a pseudo frame)", dominant)
	}
}

// TestRecordInvestigateOccurrenceDiffuseMessageMatchesProfileType verifies
// a diffuse profile's issue Message names the ACTUAL profile type
// (report.ProfileMethod), never a hardcoded "diffuse cpu profile" for a
// heap/goroutine/sample capture that was never CPU at all.
func TestRecordInvestigateOccurrenceDiffuseMessageMatchesProfileType(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	t.Setenv(issues.StorePathEnv, storePath)
	codebaseRoot, hotFile := gitCodebase(t)

	diffuseSymbols := []profiler.Symbol{
		{Func: "fnA", File: hotFile, Line: 1, Weight: 8},
		{Func: "fnB", File: hotFile, Line: 2, Weight: 7},
	}
	for _, tt := range []struct {
		method string
		want   string
	}{
		{method: "pprof_heap", want: "diffuse heap profile"},
		{method: "pprof_goroutine", want: "diffuse goroutine profile"},
		{method: "sample", want: "diffuse sample profile"},
		{method: "inspector_cpu", want: "diffuse cpu profile"},
	} {
		t.Run(tt.method, func(t *testing.T) {
			report := &investigateReport{
				PID: 42, StartedAt: "2026-01-01T00:00:00Z", ProfileMethod: tt.method,
				Process: &procbind.Binding{Name: "api", Runtime: procbind.RuntimeGo, CodebaseRoot: codebaseRoot, Cwd: codebaseRoot},
				Profile: &profiler.Profile{Symbols: diffuseSymbols},
			}
			issue, _, err := recordInvestigateOccurrence(report)
			if err != nil {
				t.Fatal(err)
			}
			if issue.Message != tt.want {
				t.Fatalf("Message = %q, want %q", issue.Message, tt.want)
			}
		})
	}
}

// TestInvestigatePipelineCorrelateSkipsOnUnhealthyCodemap verifies the E1.3
// health wiring: investigate's correlate step calls ecosystem.ProbeCodemap
// exactly ONCE and, when it reports anything other than ok, emits a single
// skipped step carrying the probe's own limitation/recovery — never the up
// to 12 separate `codemap symbol-at`/`impact` subprocesses correlateProfile
// would otherwise spend, all of which would fail identically against the
// same unhealthy codemap.
func TestInvestigatePipelineCorrelateSkipsOnUnhealthyCodemap(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipOwned, ""
	}
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		return profiler.Profile{PID: pid, Type: ptype, Text: "heap profile: 1"}, nil
	}
	codebaseRoot, _ := gitCodebase(t)
	argsFile := installFakeCodemap(t, `#!/bin/sh
printf '%s\n' "$@" >> "$CODEMAP_ARGS_FILE"
case " $* " in
  *" status "*) printf '%s' '{"registered": false}' ;;
  *) exit 9 ;;
esac
`)

	report := investigatePipeline(context.Background(), 42, InvestigateOptions{
		Codebase: codebaseRoot, NoSave: true, SkipSemantic: true,
	})

	var corrStep *investigateStep
	for i := range report.Steps {
		if report.Steps[i].Step == "correlate" {
			corrStep = &report.Steps[i]
		}
	}
	if corrStep == nil || corrStep.Status != stepSkipped {
		t.Fatalf("correlate step = %+v, want status skipped", corrStep)
	}
	if !strings.Contains(corrStep.Limitation, "not indexed") {
		t.Errorf("correlate Limitation = %q, want it to carry ProbeCodemap's own detail", corrStep.Limitation)
	}
	if corrStep.Recovery != "run: codemap index" {
		t.Errorf("correlate Recovery = %q, want ProbeCodemap's own recovery", corrStep.Recovery)
	}

	raw, err := os.ReadFile(argsFile)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "symbol-at") || strings.Contains(string(raw), "impact") {
		t.Fatalf("codemap was invoked beyond the single status probe: %s", raw)
	}
	if strings.Count(string(raw), "status\n") != 1 {
		t.Fatalf("codemap status was invoked %d time(s), want exactly 1 (cached, not re-probed per frame): %s",
			strings.Count(string(raw), "status\n"), raw)
	}
}

// TestInvestigatePipelineCorrelateSkipsOnSchemaSkewedCodemap is the
// schema_skew variant of the health-wiring test above: a codemap whose
// binary is older than the on-disk index's schema must degrade to a single
// skipped correlate step carrying the "upgrade codemap, do NOT reindex"
// recovery (ecosystem's own codemapSchemaSkewRecovery) — never the
// misleading index_corrupt/"run codemap index --reindex" advice, which
// would rewrite the shared global index with an older schema for every
// other consumer.
func TestInvestigatePipelineCorrelateSkipsOnSchemaSkewedCodemap(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipOwned, ""
	}
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		return profiler.Profile{PID: pid, Type: ptype, Text: "heap profile: 1"}, nil
	}
	codebaseRoot, _ := gitCodebase(t)
	installFakeCodemap(t, `#!/bin/sh
printf '%s\n' "$@" >> "$CODEMAP_ARGS_FILE"
case " $* " in
  *" status "*) printf '%s' '{"ok":false,"error":"graph db schema v9 is newer than this codemap (supports v7); upgrade codemap","code":"index_corrupt"}'; exit 1 ;;
  *) exit 9 ;;
esac
`)

	report := investigatePipeline(context.Background(), 42, InvestigateOptions{
		Codebase: codebaseRoot, NoSave: true, SkipSemantic: true,
	})

	var corrStep *investigateStep
	for i := range report.Steps {
		if report.Steps[i].Step == "correlate" {
			corrStep = &report.Steps[i]
		}
	}
	if corrStep == nil || corrStep.Status != stepSkipped {
		t.Fatalf("correlate step = %+v, want status skipped", corrStep)
	}
	if !strings.Contains(corrStep.Limitation, "schema v9 is newer") {
		t.Errorf("correlate Limitation = %q, want the schema-skew detail", corrStep.Limitation)
	}
	if !strings.Contains(corrStep.Recovery, "upgrade the codemap binary") || !strings.Contains(corrStep.Recovery, "do NOT run codemap index --reindex") {
		t.Errorf("correlate Recovery = %q, want the upgrade-not-reindex recovery", corrStep.Recovery)
	}
}

// TestDominantInAppSymbolAggregatesAcrossLines verifies a function's
// activity split across several statements (three lines under the 15%
// threshold individually) is summed before the threshold check, not
// judged line by line.
func TestDominantInAppSymbolAggregatesAcrossLines(t *testing.T) {
	root, hotFile := gitCodebase(t)
	dominant, _ := dominantInAppSymbol([]profiler.Symbol{
		{Func: "spread", File: hotFile, Line: 10, Weight: 6},
		{Func: "spread", File: hotFile, Line: 11, Weight: 5},
		{Func: "spread", File: hotFile, Line: 12, Weight: 5},
	}, root)
	if dominant.Func != "spread" {
		t.Fatalf("dominant.Func = %q, want spread", dominant.Func)
	}
	if dominant.Weight != 16 {
		t.Fatalf("dominant.Weight = %v, want 16 (6+5+5 summed across spread's 3 hot lines)", dominant.Weight)
	}
}
