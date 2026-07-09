package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"runtime"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

// Step status values for investigateStep.Status.
const (
	stepOK      = "ok"
	stepFailed  = "failed"
	stepSkipped = "skipped"
)

// investigateStep is one typed pipeline step result. Limitation is honest
// degradation (may be set even on ok, e.g. sample frames lack file:line);
// Recovery tells the agent what to do about it.
type investigateStep struct {
	Step       string `json:"step"`
	Status     string `json:"status"` // ok | failed | skipped
	Limitation string `json:"limitation,omitempty"`
	Recovery   string `json:"recovery,omitempty"`
}

// investigateReport is the pipeline result shared verbatim by the CLI
// `monitor investigate` command and (via toMap) MCP monitor_investigate.
type investigateReport struct {
	PID           int32                    `json:"pid"`
	StartedAt     string                   `json:"started_at"`
	Steps         []investigateStep        `json:"steps"`
	Verdict       string                   `json:"verdict"`                  // "complete" | "partial"
	ProfileMethod string                   `json:"profile_method,omitempty"` // "pprof_heap" | "sample"
	Profile       *profiler.Profile        `json:"profile,omitempty"`
	Correlations  []map[string]any         `json:"correlations,omitempty"`
	Stash         *incidents.CaptureResult `json:"stash,omitempty"`
	StashError    string                   `json:"stash_error,omitempty"`
	Note          string                   `json:"note,omitempty"`
}

// toMap JSON-round-trips the report so the MCP surface gets snake_case keys.
func (r investigateReport) toMap() map[string]any {
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// Stub points for tests (pattern: incidents.stashSave/hasFcheap vars).
var (
	verifyOwnership  = profiler.VerifyListenerOwnership
	captureProfile   = profiler.Capture
	incidentsCapture = incidents.Capture
)

// computeVerdict: complete iff no step failed (skips are acceptable).
func computeVerdict(steps []investigateStep) string {
	for _, s := range steps {
		if s.Status == stepFailed {
			return "partial"
		}
	}
	return "complete"
}

// captureInvestigateProfile captures a profile for pid without trusting an
// unproven pprof endpoint: heap-over-pprof ONLY when the LISTEN socket at
// DefaultPprofAddr provably belongs to pid; otherwise macOS `sample`.
// The returned step is never "skipped": profiling is always attempted.
func captureInvestigateProfile(ctx context.Context, pid int32) (profiler.Profile, string, investigateStep) {
	step := investigateStep{Step: "profile"}
	var reasons []string

	own, detail := verifyOwnership(ctx, pid, "")
	if own == profiler.OwnershipOwned {
		prof, err := captureProfile(ctx, pid, profiler.ProfileHeap, "")
		if err == nil {
			if rec := prof.VerifyArtifact(); rec.Verified {
				step.Status = stepOK
				return prof, "pprof_heap", step
			} else {
				reasons = append(reasons, "pprof heap: "+rec.Limitation)
			}
		} else {
			reasons = append(reasons, "pprof heap: "+err.Error())
		}
	} else {
		reasons = append(reasons, fmt.Sprintf("pprof endpoint %s not proven to belong to pid %d (%s: %s)", profiler.DefaultPprofAddr, pid, own, detail))
	}

	if runtime.GOOS == "darwin" {
		prof, err := captureProfile(ctx, pid, profiler.ProfileSample, "")
		if err == nil {
			if rec := prof.VerifyArtifact(); rec.Verified {
				step.Status = stepOK
				step.Limitation = strings.Join(append(reasons, "used macOS sample: frames carry no file:line, so codemap correlation is unavailable"), "; ")
				return prof, "sample", step
			} else {
				reasons = append(reasons, "sample: "+rec.Limitation)
			}
		} else {
			reasons = append(reasons, "sample: "+err.Error())
		}
	} else {
		reasons = append(reasons, "macOS sample unavailable on "+runtime.GOOS)
	}

	step.Status = stepFailed
	step.Limitation = strings.Join(reasons, "; ")
	step.Recovery = "start the target with net/http/pprof and re-run (or pass --pprof-addr to 'monitor profile'); on macOS ensure 'sample' can attach (same user or root) and the pid is alive"
	return profiler.Profile{}, "", step
}

// investigatePipeline runs the diagnostic pipeline for pid with typed
// per-step receipts: snapshot -> profile (ownership-gated) -> correlate ->
// stash. It NEVER stashes an unverified/empty profile and returns an
// overall verdict ("complete" | "partial") instead of implying success.
// Shared by `monitor investigate` and MCP monitor_investigate.
func investigatePipeline(ctx context.Context, pid int32, ttl string, noSave bool) investigateReport {
	report := investigateReport{PID: pid, StartedAt: time.Now().Format(time.RFC3339)}

	// snapshot — Collect never errors; a missing pid is a limitation, not a failure.
	snapshot := NewCollector(0).Collect(ctx)
	snapStep := investigateStep{Step: "snapshot", Status: stepOK}
	processName := ""
	found := false
	for _, p := range snapshot.Processes {
		if p.PID == pid {
			processName, found = p.Name, true
			break
		}
	}
	if !found {
		snapStep.Limitation = fmt.Sprintf("pid %d not found in the process table (it may have exited)", pid)
	}
	report.Steps = append(report.Steps, snapStep)

	// profile — ownership-gated (see captureInvestigateProfile).
	profile, method, profStep := captureInvestigateProfile(ctx, pid)
	report.Steps = append(report.Steps, profStep)
	if profStep.Status == stepOK {
		report.Profile = &profile
		report.ProfileMethod = method
	}

	// correlate — needs a verified profile with file:line frames + codemap.
	corrStep := investigateStep{Step: "correlate"}
	if profStep.Status != stepOK {
		corrStep.Status = stepSkipped
		corrStep.Limitation = "no verified profile to correlate"
	} else if corr := correlateProfile(ctx, profile.Symbols); corr != nil {
		report.Correlations = corr
		corrStep.Status = stepOK
	} else {
		corrStep.Status = stepSkipped
		corrStep.Limitation = "no frames resolved (codemap missing, or the profile's frames carry no file:line)"
		corrStep.Recovery = "install codemap and index the target project to enable frame-to-symbol correlation"
	}
	report.Steps = append(report.Steps, corrStep)

	// stash — omit the profile from the bundle unless it verified.
	stashStep := investigateStep{Step: "stash"}
	if noSave {
		stashStep.Status = stepSkipped
		stashStep.Limitation = "--no-save: bundle not stashed; profile included in JSON"
		report.Note = "--no-save: bundle not stashed; profile included in JSON"
	} else {
		req := incidents.CaptureRequest{
			Snapshot: snapshot,
			Alert: incidents.AlertDetail{
				Rule:    "investigate",
				PID:     pid,
				Process: processName,
				Detail:  fmt.Sprintf("manual investigate of pid %d", pid),
			},
			Trigger: "investigate",
			TTL:     ttl,
		}
		if profStep.Status == stepOK {
			// writeBundle drops profile.json when Profile is zero, so a
			// failed capture can never masquerade as stashed evidence.
			req.Profile = profile
		}
		res, err := incidentsCapture(ctx, req)
		if err != nil {
			stashStep.Status = stepFailed
			stashStep.Limitation = "stash failed: " + err.Error()
			report.StashError = err.Error()
			if res.Path != "" {
				report.Stash = &res
				if res.RegistryID != "" {
					stashStep.Recovery = "bundle retained locally at " + res.Path + "; retry with `monitor incidents resume-stash " + res.RegistryID + "`"
				} else {
					stashStep.Recovery = "bundle retained locally at " + res.Path + "; save it manually with 'fcheap save' once fcheap is available"
				}
			} else {
				stashStep.Recovery = "no local bundle was written; fix the underlying error and re-run 'monitor investigate'"
			}
		} else {
			stashStep.Status = stepOK
			report.Stash = &res
		}
	}
	report.Steps = append(report.Steps, stashStep)

	report.Verdict = computeVerdict(report.Steps)
	if report.Note == "" {
		if report.Verdict == "complete" {
			report.Note = "investigation pipeline complete (profile and stash verified)"
		} else {
			report.Note = "investigation pipeline partial — see steps[].limitation and steps[].recovery"
		}
	}
	return report
}
