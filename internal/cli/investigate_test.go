package cli

import (
	"context"
	"errors"
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

// TestInvestigateReportJSONShapeStableForDownstreamConsumers is a snapshot
// test of investigate --json's top-level shape: Chalupa's runtime/ci-engine
// (chalupa-ci.py) reads stash.artifact_ref, and cairntrace reads it too.
// E1.7's payload diet only ever touches profile.text; this locks down that
// none of the other top-level fields (including the nested stash shape)
// moved, and that redactRaw's own field addition/removal is limited to
// profile.text exactly as documented.
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

	// Downstream consumers read the FULL report (not the redacted one) via
	// toMap() today; the top-level shape they depend on must not move.
	m := report.toMap()
	for _, key := range []string{"pid", "started_at", "verdict", "steps", "profile_method", "profile", "stash"} {
		if _, ok := m[key]; !ok {
			t.Errorf("toMap() missing top-level key %q: %v", key, m)
		}
	}
	stash, ok := m["stash"].(map[string]any)
	if !ok {
		t.Fatalf("stash type = %T, want map[string]any", m["stash"])
	}
	artifactRef, ok := stash["artifact_ref"].(map[string]any)
	if !ok || artifactRef["uri"] != "fcheap://stash/stash-1" {
		t.Fatalf("stash.artifact_ref = %v, want the ArtifactRef map (Chalupa's chalupa-ci.py and cairntrace both read this path)", stash["artifact_ref"])
	}

	// redactRaw(false) (the default: --include-raw / include_raw not set)
	// removes ONLY profile.text; everything else, including stash, is
	// untouched.
	redacted := report.redactRaw(false).toMap()
	for _, key := range []string{"pid", "started_at", "verdict", "steps", "profile_method", "profile", "stash"} {
		if _, ok := redacted[key]; !ok {
			t.Errorf("redactRaw(false).toMap() missing top-level key %q: %v", key, redacted)
		}
	}
	redactedStash, ok := redacted["stash"].(map[string]any)
	if !ok || redactedStash["artifact_ref"] == nil {
		t.Fatalf("redactRaw(false) disturbed stash: %v", redacted["stash"])
	}
	redactedProfile, ok := redacted["profile"].(map[string]any)
	if !ok {
		t.Fatalf("redacted profile type = %T", redacted["profile"])
	}
	if _, hasText := redactedProfile["text"]; hasText {
		t.Errorf("redactRaw(false) should omit profile.text; got %v", redactedProfile)
	}

	// redactRaw(true) (--include-raw / include_raw:true) restores text and
	// changes nothing else.
	raw := report.redactRaw(true).toMap()
	rawProfile, ok := raw["profile"].(map[string]any)
	if !ok || rawProfile["text"] != "raw CDP JSON" {
		t.Fatalf("redactRaw(true) should keep profile.text; got %v", rawProfile)
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
// Bun doesn't speak V8's CDP) and instead fails fast with an honest
// limitation and recovery.
func TestCaptureRuntimeAwareProfileBunIsHonestNotA404(t *testing.T) {
	binding := &procbind.Binding{Runtime: procbind.RuntimeBun, InspectAddr: "localhost:6499"}
	for _, ptype := range []profiler.ProfileType{profiler.ProfileCPU, profiler.ProfileHeap} {
		_, method, step := captureRuntimeAwareProfile(context.Background(), 42, binding, ptype, "", "", false, 0)
		if step.Status != stepFailed {
			t.Fatalf("type %s: step.Status = %q, want failed", ptype, step.Status)
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

// TestCaptureRuntimeAwareProfileNodeRequiresInspectAddr verifies a
// Node/Deno process with no known inspector address fails with a specific,
// actionable message rather than silently falling through to an unrelated
// pprof/sample attempt.
func TestCaptureRuntimeAwareProfileNodeRequiresInspectAddr(t *testing.T) {
	binding := &procbind.Binding{Runtime: procbind.RuntimeNode}
	_, method, step := captureRuntimeAwareProfile(context.Background(), 42, binding, profiler.ProfileCPU, "", "", false, 0)
	if step.Status != stepFailed || method != "" {
		t.Fatalf("step = %+v, method = %q, want a failed step with no method", step, method)
	}
	if !strings.Contains(step.Limitation, "no inspector address") {
		t.Errorf("Limitation = %q, want it to mention the missing inspector address", step.Limitation)
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
