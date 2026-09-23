package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
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

// InvestigateOptions configures the diagnostic pipeline. Zero value keeps
// historical defaults (ttl empty→caller sets, no-save false, auto codebase).
type InvestigateOptions struct {
	TTL           string
	NoSave        bool
	Codebase      string // explicit project root for codemap/vecgrep; empty = auto
	Environment   string
	DeploymentID  string
	RunID         string
	StepID        string
	Suite         string
	Attempt       string
	Release       string
	Service       string
	GitSHA        string
	SkipSemantic  bool // tests / offline
	SkipCorrelate bool
}

// investigateReport is the pipeline result shared verbatim by the CLI
// `monitor investigate` command and (via toMap) MCP monitor_investigate.
type investigateReport struct {
	PID           int32                    `json:"pid"`
	StartedAt     string                   `json:"started_at"`
	Steps         []investigateStep        `json:"steps"`
	Verdict       string                   `json:"verdict"` // "complete" | "partial"
	ProfileMethod string                   `json:"profile_method,omitempty"`
	Profile       *profiler.Profile        `json:"profile,omitempty"`
	Process       *procbind.Binding        `json:"process,omitempty"`
	Context       contextids.IDs           `json:"context,omitempty"`
	Correlations  []map[string]any         `json:"correlations,omitempty"`
	SemanticHits  []map[string]any         `json:"semantic_hits,omitempty"`
	Stash         *incidents.CaptureResult `json:"stash,omitempty"`
	StashError    string                   `json:"stash_error,omitempty"`
	Issue         *issues.Issue            `json:"issue,omitempty"`
	Occurrence    *issues.Occurrence       `json:"occurrence,omitempty"`
	IssueError    string                   `json:"issue_error,omitempty"`
	Note          string                   `json:"note,omitempty"`
	// CodebaseOverride is opts.Codebase verbatim, kept alongside the
	// resolved Process.CodebaseRoot so recordInvestigateOccurrence can tell
	// an explicit `--codebase` override apart from an auto-detected root:
	// an override must win over the process's own cwd for project identity
	// even when that cwd is readable (e.g. a daemon with cwd "/"). Not
	// serialized: Process.CodebaseRoot already carries the resolved value.
	CodebaseOverride string `json:"-"`
}

// toMap JSON-round-trips the report so the MCP surface gets snake_case keys.
func (r investigateReport) toMap() map[string]any {
	b, _ := json.Marshal(r)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m
}

// redactRaw applies the E1.7 payload diet: investigate --json and MCP's
// monitor_investigate omit the captured profile's raw text (a CDP CPU
// profile's full JSON, or a pprof capture's text dump — tens of KB for a
// real Node target) unless includeRaw is true (CLI --include-raw, MCP
// include_raw:true). `monitor profile --json` is a SEPARATE code path
// (newProfileCmd) and is deliberately unaffected: glyphrun procmon persists
// its `text` field verbatim (procmon.go:45-48) and must keep seeing it.
//
// This only changes what gets serialized for a caller: report itself (used
// internally for the stash and the durable issue occurrence) always keeps
// the full profile, because it returns a shallow copy rather than mutating
// r's own Profile.
func (r investigateReport) redactRaw(includeRaw bool) investigateReport {
	if includeRaw || r.Profile == nil || r.Profile.Text == "" {
		return r
	}
	out := r
	redacted := *r.Profile
	redacted.Text = ""
	out.Profile = &redacted
	return out
}

// Stub points for tests (pattern: incidents.stashSave/hasFcheap vars).
var (
	verifyOwnership       = profiler.VerifyListenerOwnership
	captureProfile        = profiler.Capture
	incidentsCapture      = incidents.Capture
	validateProfile       = profiler.ValidateCapture
	inspectProcess        = procbind.Inspect
	recordIssueOccurrence = recordInvestigateOccurrence
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

// bunInspectorLimitation and bunInspectorRecovery are the honest answer for
// a Bun process's cpu/heap profile step (bug 5). Bun speaks the
// WebKit/JSC inspector protocol on its --inspect port, not V8's Chrome
// DevTools Protocol; probing it the way Node/Deno's CDP discovery does (GET
// /json/list) 404s in a way that tells the caller nothing. Bun is detected
// from procbind.Binding.Runtime BEFORE any inspector discovery is
// attempted, so this is returned instead of that 404, never after it.
const (
	bunInspectorLimitation = "Bun speaks the WebKit/JSC inspector protocol, not V8 CDP"
	bunInspectorRecovery   = "run the app with `bun --cpu-prof` (or `monitor run --profile` when available) and inspect the .cpuprofile"
)

// captureRuntimeAwareProfile captures exactly one profile of type ptype for
// pid via the runtime-appropriate mechanism, shared by `monitor profile`
// (newProfileCmd), MCP's monitor_profile_capture, and (as one candidate in
// its own multi-type fallback ladder — see captureInvestigateProfile)
// `monitor investigate`:
//
//   - Node/Deno with a resolvable inspector address, ptype cpu|heap: the
//     CDP inspector (Profiler.start/stop for cpu, a heap snapshot for
//     heap) — the only path that carries file:line for codemap
//     correlation (AC-1).
//   - Bun, ptype cpu|heap: never attempts CDP discovery; fails fast with
//     bunInspectorLimitation/bunInspectorRecovery instead of the 404 a
//     real discovery attempt against Bun's JSC inspector would produce.
//   - Everything else: Go's net/http/pprof for ptype heap|cpu|goroutine
//     (gated on verifyOwnership unless addrExplicit asserts pprofAddr is
//     correct — the CLI's --pprof-addr / an explicit MCP pprof_addr), or
//     macOS `sample` for ptype sample.
//
// inspectAddr overrides binding's own InspectAddr when non-empty (the
// CLI's --inspect-addr flag); empty keeps whatever procbind detected.
// cpuDuration sizes a CPU sampling window for both the CDP and pprof
// paths; <=0 uses each path's own default (profiler.Capture's fixed 1s for
// pprof via the captureProfile stub — keeping captureInvestigateProfile's
// existing tests stub-testable — and a fixed 5s for CDP, matching the
// pre-E1.7 captureInspectorProfile helper this replaces).
func captureRuntimeAwareProfile(ctx context.Context, pid int32, binding *procbind.Binding, ptype profiler.ProfileType, pprofAddr, inspectAddr string, addrExplicit bool, cpuDuration time.Duration) (profiler.Profile, string, investigateStep) {
	step := investigateStep{Step: "profile"}
	jsRuntime := binding != nil && (binding.Runtime == procbind.RuntimeNode ||
		binding.Runtime == procbind.RuntimeBun ||
		binding.Runtime == procbind.RuntimeDeno)

	addr := inspectAddr
	if addr == "" && binding != nil {
		addr = binding.InspectAddr
	}

	if jsRuntime && (ptype == profiler.ProfileCPU || ptype == profiler.ProfileHeap) {
		if addr == "" {
			step.Status = stepFailed
			step.Limitation = fmt.Sprintf("%s process %d has no inspector address", binding.Runtime, pid)
			step.Recovery = "start it with --inspect=127.0.0.1:<port>, or pass --inspect-addr"
			return profiler.Profile{}, "", step
		}
		if binding.Runtime == procbind.RuntimeBun {
			step.Status = stepFailed
			step.Limitation = bunInspectorLimitation
			step.Recovery = bunInspectorRecovery
			return profiler.Profile{}, "", step
		}
		own, detail := profiler.VerifyInspectorOwnership(ctx, pid, addr)
		if own != profiler.OwnershipOwned {
			step.Status = stepFailed
			step.Limitation = fmt.Sprintf("inspector %s not proven to belong to pid %d (%s: %s)", addr, pid, own, detail)
			return profiler.Profile{}, "", step
		}
		var prof profiler.Profile
		var err error
		method := "inspector_cpu"
		if ptype == profiler.ProfileHeap {
			method = "inspector_heap"
			prof, err = profiler.ProfileInspectorHeap(ctx, pid, addr)
		} else {
			dur := cpuDuration
			if dur <= 0 {
				dur = 5 * time.Second
			}
			durCtx, cancel := context.WithTimeout(ctx, dur+5*time.Second)
			prof, err = profiler.ProfileInspector(durCtx, pid, addr, dur)
			cancel()
		}
		if err != nil {
			step.Status = stepFailed
			step.Limitation = "inspector: " + err.Error()
			return profiler.Profile{}, "", step
		}
		rec := prof.VerifyArtifact()
		if !rec.Verified {
			step.Status = stepFailed
			step.Limitation = "inspector: " + rec.Limitation
			return profiler.Profile{}, "", step
		}
		step.Status = stepOK
		if method == "inspector_cpu" {
			step.Limitation = "CDP inspector CPU profile (file:line frames; codemap correlation enabled)"
		} else {
			step.Limitation = "CDP inspector heap snapshot"
		}
		return prof, method, step
	}

	switch ptype {
	case profiler.ProfileHeap, profiler.ProfileCPU, profiler.ProfileGoroutine:
		if err := validateProfile(ptype); err != nil {
			step.Status = stepFailed
			step.Limitation = err.Error()
			return profiler.Profile{}, "", step
		}
		if !addrExplicit {
			own, detail := verifyOwnership(ctx, pid, pprofAddr)
			if own != profiler.OwnershipOwned {
				display := pprofAddr
				if display == "" {
					display = profiler.DefaultPprofAddr
				}
				step.Status = stepFailed
				step.Limitation = fmt.Sprintf("pprof endpoint %s not proven to belong to pid %d (%s: %s)", display, pid, own, detail)
				return profiler.Profile{}, "", step
			}
		}
		var prof profiler.Profile
		var err error
		if cpuDuration > 0 {
			prof, err = profiler.CaptureWithDuration(ctx, pid, ptype, pprofAddr, cpuDuration)
		} else {
			prof, err = captureProfile(ctx, pid, ptype, pprofAddr)
		}
		if err != nil {
			step.Status = stepFailed
			step.Limitation = "pprof " + string(ptype) + ": " + err.Error()
			return profiler.Profile{}, "", step
		}
		rec := prof.VerifyArtifact()
		if !rec.Verified {
			step.Status = stepFailed
			step.Limitation = "pprof " + string(ptype) + ": " + rec.Limitation
			return profiler.Profile{}, "", step
		}
		step.Status = stepOK
		return prof, "pprof_" + string(ptype), step
	case profiler.ProfileSample:
		if err := validateProfile(profiler.ProfileSample); err != nil {
			step.Status = stepFailed
			step.Limitation = err.Error()
			return profiler.Profile{}, "", step
		}
		prof, err := captureProfile(ctx, pid, profiler.ProfileSample, "")
		if err != nil {
			step.Status = stepFailed
			step.Limitation = "sample: " + err.Error()
			return profiler.Profile{}, "", step
		}
		rec := prof.VerifyArtifact()
		if !rec.Verified {
			step.Status = stepFailed
			step.Limitation = "sample: " + rec.Limitation
			return profiler.Profile{}, "", step
		}
		step.Status = stepOK
		return prof, "sample", step
	default:
		step.Status = stepFailed
		step.Limitation = fmt.Sprintf("unknown profile type %q", ptype)
		return profiler.Profile{}, "", step
	}
}

// captureInvestigateProfile captures a profile for pid by trying, in order:
// (1) for Node/Bun/Deno with --inspect, the CDP inspector CPU profile
// (carries file:line → codemap correlation works; honestly unavailable for
// Bun rather than a raw 404); (2) heap-over-pprof, ownership-gated; (3)
// macOS `sample`. Each attempt delegates its actual capture mechanics to
// captureRuntimeAwareProfile (shared with `monitor profile` and MCP's
// monitor_profile_capture); this function owns only the fallback order and
// the accumulated-reasons message a caller sees when every attempt fails.
func captureInvestigateProfile(ctx context.Context, pid int32, binding *procbind.Binding) (profiler.Profile, string, investigateStep) {
	var reasons []string
	jsRuntime := binding != nil && (binding.Runtime == procbind.RuntimeNode ||
		binding.Runtime == procbind.RuntimeBun ||
		binding.Runtime == procbind.RuntimeDeno)

	// 1) Node/Bun/Deno with --inspect: CDP CPU profile (file:line frames).
	if jsRuntime && binding.InspectAddr != "" {
		if prof, method, step := captureRuntimeAwareProfile(ctx, pid, binding, profiler.ProfileCPU, "", "", false, 0); step.Status == stepOK {
			return prof, method, step
		} else {
			reasons = append(reasons, step.Limitation)
		}
	}

	// 2) Go pprof heap. captureRuntimeAwareProfile's own ownership check
	// already refuses an endpoint that doesn't provably belong to pid, so
	// a JS runtime that (as almost all do) exposes no net/http/pprof
	// server fails this exactly the same way a non-JS one with nothing
	// listening does — no separate "is this runtime applicable" guard
	// needed before trying.
	if prof, method, step := captureRuntimeAwareProfile(ctx, pid, binding, profiler.ProfileHeap, "", "", false, 0); step.Status == stepOK {
		return prof, method, step
	} else {
		reasons = append(reasons, step.Limitation)
	}

	// 3) macOS sample fallback.
	prof, method, step := captureRuntimeAwareProfile(ctx, pid, binding, profiler.ProfileSample, "", "", false, 0)
	if step.Status == stepOK {
		extra := "used macOS sample: frames carry no file:line, so codemap correlation needs codebase+entry or vecgrep semantic fallback"
		if jsRuntime {
			extra = "used macOS sample (no --inspect or inspector unreachable): frames carry no file:line; start node with --inspect for CDP CPU profiles"
		}
		step.Limitation = strings.Join(append(reasons, extra), "; ")
		return prof, method, step
	}
	reasons = append(reasons, step.Limitation)

	step.Status = stepFailed
	step.Limitation = strings.Join(reasons, "; ")
	if jsRuntime {
		step.Recovery = "start node with --inspect (or --inspect=9230) and ensure codemap/vecgrep index the project; on macOS 'sample' is a weaker fallback"
	} else {
		step.Recovery = "start the target with net/http/pprof and re-run (or pass --pprof-addr to 'monitor profile'); on macOS ensure 'sample' can attach (same user or root) and the pid is alive"
	}
	return profiler.Profile{}, "", step
}

// investigatePipeline runs the diagnostic pipeline for pid with typed
// per-step receipts:
//
//	identify → snapshot → profile → correlate → semantic → stash
//
// It NEVER stashes an unverified/empty profile and returns an overall
// verdict ("complete" | "partial") instead of implying success.
// Shared by `monitor investigate` and MCP monitor_investigate.
func investigatePipeline(ctx context.Context, pid int32, opts InvestigateOptions) investigateReport {
	if opts.TTL == "" {
		opts.TTL = "7d"
	}
	report := investigateReport{
		PID:              pid,
		StartedAt:        time.Now().Format(time.RFC3339),
		CodebaseOverride: opts.Codebase,
		Context: contextids.FromEnv(contextids.IDs{
			Environment:  opts.Environment,
			DeploymentID: opts.DeploymentID,
			RunID:        opts.RunID,
			StepID:       opts.StepID,
			Suite:        opts.Suite,
			Attempt:      opts.Attempt,
			Release:      opts.Release,
			Service:      opts.Service,
			GitSHA:       opts.GitSHA,
		}),
	}

	// identify — process runtime + codebase binding (Node cmdline/cwd/etc.).
	idStep := investigateStep{Step: "identify"}
	binding, bindErr := inspectProcess(ctx, pid, opts.Codebase)
	if bindErr != nil {
		idStep.Status = stepSkipped
		idStep.Limitation = bindErr.Error()
		idStep.Recovery = "process may have exited; correlation will rely on explicit --codebase if provided"
	} else {
		report.Process = &binding
		idStep.Status = stepOK
		if len(binding.Limitations) > 0 {
			idStep.Limitation = strings.Join(binding.Limitations, "; ")
		}
		if binding.CodebaseRoot == "" && opts.Codebase == "" {
			idStep.Limitation = joinLimitation(idStep.Limitation, "no codebase root detected (pass --codebase /path/to/project)")
			idStep.Recovery = "pass --codebase to the project root indexed by codemap/vecgrep"
		}
	}
	report.Steps = append(report.Steps, idStep)

	// snapshot — Collect never errors; a missing pid is a limitation, not a failure.
	snapshot, snapshotErr := collectFullSnapshot(ctx, NewCollector(0))
	snapStep := investigateStep{Step: "snapshot", Status: stepOK}
	if snapshotErr != nil {
		snapStep.Status = stepFailed
		snapStep.Limitation = snapshotErr.Error()
	}
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
	if processName == "" && report.Process != nil {
		processName = report.Process.Name
	}
	if report.Context.Service == "" && processName != "" {
		report.Context.Service = processName
	}
	report.Steps = append(report.Steps, snapStep)

	// profile — ownership-gated / runtime-aware.
	profile, method, profStep := captureInvestigateProfile(ctx, pid, report.Process)
	report.Steps = append(report.Steps, profStep)
	if profStep.Status == stepOK {
		report.Profile = &profile
		report.ProfileMethod = method
	}

	// correlate — codemap frame→symbol, bound to codebase root when known.
	corrStep := investigateStep{Step: "correlate"}
	codebase := opts.Codebase
	if codebase == "" && report.Process != nil {
		codebase = report.Process.CodebaseRoot
	}
	if opts.SkipCorrelate {
		corrStep.Status = stepSkipped
		corrStep.Limitation = "correlate disabled"
	} else if profStep.Status != stepOK && (report.Process == nil || report.Process.MainScript == "") {
		corrStep.Status = stepSkipped
		corrStep.Limitation = "no verified profile or main script to correlate"
	} else if codemapHealth := ecosystem.ProbeCodemap(ctx, codebase); codemapHealth.State != ecosystem.HealthOK {
		// One probe, one skipped step — not up to 12 codemap subprocess
		// calls (correlateProfile's own per-frame budget) that would all
		// fail identically on a schema-skewed/corrupt/unindexed/missing
		// codemap (bug 16 / E1.3 wiring).
		corrStep.Status = stepSkipped
		corrStep.Limitation = firstNonEmpty(codemapHealth.Detail, "codemap "+codemapHealth.State)
		corrStep.Recovery = codemapHealth.Recovery
	} else {
		var syms []profiler.Symbol
		if profStep.Status == stepOK {
			syms = profile.Symbols
		}
		// For Node (and sample profiles without file:line), seed correlation
		// from the main script so codemap still has a starting point.
		if report.Process != nil && report.Process.MainScript != "" {
			syms = append([]profiler.Symbol{{
				Func: filepath.Base(report.Process.MainScript),
				File: report.Process.MainScript,
				Line: 1,
			}}, syms...)
		}
		if corr := correlateProfile(ctx, syms, codebase); len(corr) > 0 {
			report.Correlations = corr
			corrStep.Status = stepOK
			if method == "sample" {
				corrStep.Limitation = "profile frames lack file:line; correlated main script / resolvable paths only"
			}
		} else {
			corrStep.Status = stepSkipped
			corrStep.Limitation = "no frames resolved (codemap missing, wrong --codebase, or no file:line)"
			corrStep.Recovery = "install codemap, index the project, and pass --codebase <root>"
		}
	}
	report.Steps = append(report.Steps, corrStep)

	// semantic — vecgrep similar/search against the bound codebase.
	semStep := investigateStep{Step: "semantic"}
	if opts.SkipSemantic {
		semStep.Status = stepSkipped
		semStep.Limitation = "semantic disabled"
	} else if codebase == "" {
		semStep.Status = stepSkipped
		semStep.Limitation = "no codebase root for vecgrep"
		semStep.Recovery = "pass --codebase <project root indexed by vecgrep>"
	} else if vecgrepHealth := ecosystem.ProbeVecgrep(ctx, codebase); vecgrepHealth.State != ecosystem.HealthOK {
		// Same one-probe-not-N-calls treatment as correlate, above.
		semStep.Status = stepSkipped
		semStep.Limitation = firstNonEmpty(vecgrepHealth.Detail, "vecgrep "+vecgrepHealth.State)
		semStep.Recovery = vecgrepHealth.Recovery
	} else {
		hits, limitation := semanticCorrelate(ctx, codebase, report.Process, report.Correlations, profile.Symbols)
		if len(hits) > 0 {
			report.SemanticHits = hits
			semStep.Status = stepOK
			semStep.Limitation = limitation
		} else {
			semStep.Status = stepSkipped
			semStep.Limitation = firstNonEmpty(limitation, "vecgrep returned no hits from a fresh index")
			semStep.Recovery = "run `vecgrep ensure` or `vecgrep index` in " + codebase
		}
	}
	report.Steps = append(report.Steps, semStep)

	// stash — omit the profile from the bundle unless it verified.
	stashStep := investigateStep{Step: "stash"}
	if opts.NoSave {
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
			Trigger:       "investigate",
			TTL:           opts.TTL,
			Correlations:  report.Correlations,
			SemanticHits:  report.SemanticHits,
			ProfileMethod: report.ProfileMethod,
			Context:       contextMap(report.Context),
			ExtraTags:     report.Context.Tags(),
		}
		if report.Process != nil {
			req.Process = processBinding(report.Process)
			if report.Process.Runtime != "" && report.Process.Runtime != procbind.RuntimeUnknown {
				req.ExtraTags = append(req.ExtraTags, "runtime:"+string(report.Process.Runtime))
			}
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

	// Whether or not the bundle was stashed, the transient on-disk profile
	// copy (only pprof heap ever sets Path — CDP and macOS sample capture
	// straight to Text) is no longer needed by the time we reach here: a
	// successful stash already copied it into fcheap/the resumable local
	// bundle above, and either way Text (or the stash itself) already
	// carries what a reader of this report needs. Removing it here, not
	// just on the stash-succeeded path, keeps investigate from leaving a
	// /tmp/monitor-heap-<pid>-*.pb.gz behind on every run regardless of
	// --no-save (E1.7: 10 captures leave no temp files).
	if report.Profile != nil && report.Profile.Path != "" {
		_ = os.Remove(report.Profile.Path)
		report.Profile.Path = ""
	}

	issueStep := investigateStep{Step: "issue"}
	issue, occurrence, err := recordIssueOccurrence(&report)
	if err != nil {
		issueStep.Status = stepFailed
		issueStep.Limitation = "issue persistence failed: " + err.Error()
		issueStep.Recovery = "check the local issue store path and permissions, then re-run investigate"
		report.IssueError = err.Error()
	} else {
		issueStep.Status = stepOK
		report.Issue = &issue
		report.Occurrence = &occurrence
	}
	report.Steps = append(report.Steps, issueStep)

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

// dominantInAppSymbolThreshold is the minimum share of a profile's active
// samples (a symbol's Cum when the capture method populates it — pprof and
// macOS `sample`; otherwise its Weight, aggregated across every hot line of
// the SAME function — CDP's positionTicks are per-statement, so a
// function's real activity can be split thin across several lines) a
// single in-app function must carry before recordInvestigateOccurrence
// treats it as that profile's identity (E1.6 / bug 11). Below this, the
// profile's hot time is spread thin enough across the call graph that any
// single "dominant" pick is closer to sampling noise than a real signal —
// a different investigate run over the exact SAME live process can shuffle
// which line edges past a much lower bar, opening a brand new issue every
// time.
const dominantInAppSymbolThreshold = 15.0

// dominantSymbol summarizes one in-app function's aggregated hotness
// within a profile: Weight sums symbolWeight across every one of the
// function's sampled lines (so a function whose time is split across
// several statements is not undercounted against a single-line function),
// and Line/File name its single hottest line, kept for display.
type dominantSymbol struct {
	Func   string
	File   string
	Line   int
	Weight float64
}

// symbolWeight returns the share of a profile's active samples sym
// accounts for, preferring Cum (pprof/sample: self time plus everything
// sampled underneath — the more meaningful "how hot is this call" number,
// and what correlateProfile's own scoring already prefers) over Weight
// (CDP's self-time-only positionTicks, or any Cum-less capture) when both
// are present.
func symbolWeight(sym profiler.Symbol) float64 {
	if sym.Cum > 0 {
		return sym.Cum
	}
	return sym.Weight
}

// isPseudoSymbolName defends against a pseudo/synthetic frame name reaching
// this far. flattenCDPProfile already strips V8's (idle)/(program)/
// (garbage collector)/(root) out of Profile.Symbols entirely (E1.1), so in
// practice this never fires for a CDP capture; it exists so a future
// capture method (or a hand-built profiler.Profile in a test) can't
// silently make a pseudo frame "the dominant function" just because it
// slipped past its own producer's filtering.
func isPseudoSymbolName(name string) bool {
	switch name {
	case "", "(idle)", "(program)", "(garbage collector)", "(root)", "(anonymous)", "(unknown)", "native":
		return true
	default:
		return strings.HasPrefix(name, "(") && strings.HasSuffix(name, ")")
	}
}

// dominantInAppSymbol aggregates profile symbols by (Func, File) —
// summing symbolWeight across every line of the same function — and
// returns the highest-scoring group that is in-app under gitRoot (real
// application code: a file under the codebase/git root, not
// node_modules/vendor/site-packages/GOROOT/a runtime pseudo-path; see
// stacktrace.InApp) and not a pseudo frame, plus every group's summary
// (hottest-first) for metadata/evidence. Symbols with no File (macOS
// `sample`'s frames, which carry no file:line at all) can never be in-app
// and are skipped outright. A gitRoot of "" makes every symbol
// out-of-app by stacktrace.InApp's own contract, so this correctly
// reports "nothing dominant" rather than guessing without a codebase.
func dominantInAppSymbol(symbols []profiler.Symbol, gitRoot string) (dominantSymbol, []dominantSymbol) {
	type key struct{ Func, File string }
	agg := make(map[key]*dominantSymbol)
	var order []key
	for _, s := range symbols {
		name := strings.TrimSpace(s.Func)
		if name == "" || s.File == "" || isPseudoSymbolName(name) {
			continue
		}
		k := key{Func: name, File: s.File}
		d, ok := agg[k]
		if !ok {
			// Symbols arrive pre-sorted hottest-first (flattenCDPProfile,
			// symbolsFromPprof), so the first row seen for this key is
			// already its hottest single line.
			d = &dominantSymbol{Func: name, File: s.File, Line: s.Line}
			agg[k] = d
			order = append(order, k)
		}
		d.Weight += symbolWeight(s)
	}
	all := make([]dominantSymbol, 0, len(order))
	for _, k := range order {
		all = append(all, *agg[k])
	}
	sort.SliceStable(all, func(i, j int) bool { return all[i].Weight > all[j].Weight })

	for _, d := range all {
		if stacktrace.InApp(stacktrace.Frame{Filename: d.File, AbsPath: d.File}, gitRoot) {
			return d, all
		}
	}
	return dominantSymbol{}, all
}

// formatTopSymbols renders up to n dominantSymbol groups (already
// hottest-first) as a single Metadata-friendly string: "func@file:line
// (NN.N%)" entries separated by "; ". It is display evidence only — see
// dominantInAppSymbol's doc comment — and never enters the fingerprint.
func formatTopSymbols(all []dominantSymbol, n int) string {
	if len(all) > n {
		all = all[:n]
	}
	parts := make([]string, 0, len(all))
	for _, d := range all {
		parts = append(parts, fmt.Sprintf("%s@%s:%d (%.1f%%)", d.Func, d.File, d.Line, d.Weight))
	}
	return strings.Join(parts, "; ")
}

// recordInvestigateOccurrence persists one investigate run as an occurrence,
// opening the issue store fresh for this single write via issues.WithWriter
// (bug 12: investigate must not fail with ErrFileLocked just because
// `watch --stash` is running). project.Resolve replaces the ad hoc
// CodebaseRoot-basename derivation this used to do inline, so investigate
// agrees with watch on the same project/service identity for the same
// process (bug 15): a process's cwd is preferred over its already-resolved
// CodebaseRoot as the git-root/marker walk's starting point, since in a
// monorepo the codebase root is often the nearest manifest, not the git
// root, and Resolve needs to tell those two apart itself.
//
// An explicit `investigate --codebase <root>` (report.CodebaseOverride)
// wins over the process's own cwd: without this, a daemon with cwd "/" (or
// any cwd outside the intended root) silently ignored the override for
// project identity, even though it was honored for codemap/vecgrep
// correlation. report.CodebaseOverride is checked before Process at all,
// so it applies even when identify failed to bind a process (report.Process
// == nil). Neither this override nor Process.Cwd ever falls back to
// monitor's own os.Getwd() (project.Resolve's Hints.UseWorkingDir is
// intentionally left unset): investigate describes the target process, not
// the monitor invocation itself.
func recordInvestigateOccurrence(report *investigateReport) (issues.Issue, issues.Occurrence, error) {
	path, err := issues.ResolvePath("")
	if err != nil {
		return issues.Issue{}, issues.Occurrence{}, err
	}

	processName := ""
	if report.Process != nil {
		processName = strings.TrimSpace(report.Process.Name)
	}
	dir := strings.TrimSpace(report.CodebaseOverride)
	if dir == "" && report.Process != nil {
		dir = firstNonEmpty(report.Process.Cwd, report.Process.CodebaseRoot)
	}
	identity := project.Resolve(project.Hints{
		ExplicitService: strings.TrimSpace(report.Context.Service),
		Dir:             dir,
		ProcessName:     processName,
		PID:             report.PID,
	})
	projectSlug := identity.Slug
	service := identity.Service

	// E1.6 (bug 11): the fingerprint hashes at most ONE symbol — the
	// dominant in-app function — never the top-10 sampled symbols. A
	// live process's own sampling noise reshuffles which functions land
	// in an arbitrary "top 10" between one investigate run and the next,
	// so hashing that whole set opened a brand new issue almost every
	// time. A single, threshold-gated dominant symbol is far more stable
	// run over run for the SAME hot path. The full ranked list still goes
	// into Metadata/Evidence for a human or agent reading the issue, just
	// never into the hash.
	symbols := []string(nil)
	message := "manual process investigation"
	profileSymbolsMetadata := ""
	if report.Profile != nil && len(report.Profile.Symbols) > 0 {
		dominant, all := dominantInAppSymbol(report.Profile.Symbols, identity.GitRoot)
		if dominant.Weight >= dominantInAppSymbolThreshold {
			symbols = []string{dominant.Func}
		} else {
			message = "diffuse cpu profile"
		}
		profileSymbolsMetadata = formatTopSymbols(all, 10)
	}
	evidence := []string{}
	typedEvidence := []issues.EvidenceRef{}
	treeHash := ""
	if report.Stash != nil {
		treeHash = report.Stash.TreeHash
		if uri, ok := report.Stash.ArtifactRef["uri"].(string); ok && uri != "" {
			evidence = append(evidence, uri)
			typedEvidence = append(typedEvidence, issues.EvidenceRef{Kind: "monitor.incident", URI: uri, TreeHash: treeHash})
		} else if report.Stash.StashID != "" {
			uri := "fcheap://stash/" + report.Stash.StashID
			evidence = append(evidence, uri)
			typedEvidence = append(typedEvidence, issues.EvidenceRef{Kind: "monitor.incident", URI: uri, TreeHash: treeHash})
		} else if report.Stash.RegistryID != "" {
			uri := "monitor://incidents/" + report.Stash.RegistryID
			evidence = append(evidence, uri)
			typedEvidence = append(typedEvidence, issues.EvidenceRef{Kind: "monitor.incident.pending", URI: uri, TreeHash: treeHash})
		}
	}
	metadata := map[string]string{}
	for key, value := range map[string]string{
		"environment": report.Context.Environment, "deployment_id": report.Context.DeploymentID,
		"step_id": report.Context.StepID, "suite": report.Context.Suite, "attempt": report.Context.Attempt,
		"git_sha": report.Context.GitSHA, "profile_method": report.ProfileMethod, "trigger": "investigate",
		// profile_symbols is the FULL ranked in-app-function list (E1.6):
		// evidence for a human/agent reading the issue, never hashed into
		// the fingerprint (see the Symbols/message derivation above).
		"profile_symbols": profileSymbolsMetadata,
	} {
		if value != "" {
			metadata[key] = value
		}
	}
	observedAt, _ := time.Parse(time.RFC3339, report.StartedAt)
	titleSubject := firstNonEmpty(service, projectSlug)
	input := issues.OccurrenceInput{
		ObservedAt: observedAt, Project: projectSlug, Service: service, Kind: "investigation",
		Title: "Investigation: " + titleSubject, Message: message,
		Symbols: symbols, Severity: "warning", RunID: report.Context.RunID,
		Release: report.Context.Release, PID: report.PID, TreeHash: treeHash,
		EvidenceRefs: evidence, Evidence: typedEvidence, Metadata: metadata,
		Run: &issues.RunContext{
			ID: report.Context.RunID, Environment: report.Context.Environment,
			DeploymentID: report.Context.DeploymentID, StepID: report.Context.StepID,
			Suite: report.Context.Suite, Attempt: report.Context.Attempt,
			Release: report.Context.Release, GitSHA: report.Context.GitSHA,
		},
	}
	var issue issues.Issue
	var occurrence issues.Occurrence
	err = issues.WithWriter(context.Background(), path, issues.DefaultWriterWait, func(store *issues.Store) error {
		var writeErr error
		issue, occurrence, writeErr = store.UpsertOccurrence(input)
		return writeErr
	})
	return issue, occurrence, err
}

func contextMap(id contextids.IDs) map[string]string {
	if id.Empty() {
		return nil
	}
	m := map[string]string{}
	put := func(k, v string) {
		if v != "" {
			m[k] = v
		}
	}
	put("environment", id.Environment)
	put("deployment_id", id.DeploymentID)
	put("run_id", id.RunID)
	put("step_id", id.StepID)
	put("suite", id.Suite)
	put("attempt", id.Attempt)
	put("release", id.Release)
	put("service", id.Service)
	put("git_sha", id.GitSHA)
	if len(m) == 0 {
		return nil
	}
	return m
}

func processBinding(b *procbind.Binding) *incidents.ProcessBinding {
	if b == nil {
		return nil
	}
	return &incidents.ProcessBinding{
		PID:          b.PID,
		Name:         b.Name,
		Exe:          b.Exe,
		Cwd:          b.Cwd,
		Cmdline:      b.Cmdline,
		ArgvRedacted: b.ArgvRedacted || len(b.Cmdline) > 0,
		Runtime:      string(b.Runtime),
		MainScript:   b.MainScript,
		CodebaseRoot: b.CodebaseRoot,
		InspectAddr:  b.InspectAddr,
		Markers:      b.Markers,
		Limitations:  b.Limitations,
	}
}

func joinLimitation(a, b string) string {
	switch {
	case a == "":
		return b
	case b == "":
		return a
	default:
		return a + "; " + b
	}
}

// semanticCorrelate asks vecgrep for code similar to hot frames / main script
// / process identity. Best-effort; returns nil on total failure.
func semanticCorrelate(ctx context.Context, codebase string, binding *procbind.Binding, correlations []map[string]any, symbols []profiler.Symbol) ([]map[string]any, string) {
	readiness, err := ecosystem.VecgrepReadiness(ctx, codebase)
	if err != nil {
		return nil, "vecgrep readiness failed: " + err.Error()
	}
	if !readiness.Index.Indexed {
		return nil, "vecgrep project is not indexed"
	}
	if !readiness.Index.Fresh {
		return nil, "vecgrep index is stale"
	}
	limitation := readiness.Warning
	opts := ecosystem.VecgrepSimilarOpts{Dir: codebase, Limit: 5}
	var out []map[string]any
	seen := map[string]bool{}
	addHits := func(source string, hits []ecosystem.VecgrepHit) {
		for _, h := range hits {
			key := fmt.Sprintf("%s:%d", h.RelativePath, h.StartLine)
			if key == ":0" {
				key = fmt.Sprintf("%s:%d", h.FilePath, h.StartLine)
			}
			if seen[key] {
				continue
			}
			seen[key] = true
			entry := map[string]any{
				"source":     source,
				"file":       firstNonEmpty(h.RelativePath, h.FilePath),
				"start_line": h.StartLine,
				"end_line":   h.EndLine,
				"score":      h.Score,
				"symbol":     h.SymbolName,
				"chunk_type": h.ChunkType,
				"language":   h.Language,
				"snippet":    trimSnippet(h.Content, 280),
			}
			out = append(out, entry)
			if len(out) >= 12 {
				return
			}
		}
	}

	// 1) Similar to top correlated file:line (codemap-resolved).
	n := 0
	for _, c := range correlations {
		if n >= 3 {
			break
		}
		file, _ := c["file"].(string)
		line, _ := c["line"].(int)
		if file == "" || line <= 0 {
			continue
		}
		hits, err := ecosystem.VecgrepSimilarAt(ctx, file, line, opts)
		if err == nil {
			addHits("similar:"+file, hits)
			n++
		}
	}

	// 2) Main script as anchor (Node entry).
	if binding != nil && binding.MainScript != "" && len(out) < 12 {
		hits, err := ecosystem.VecgrepSimilarAt(ctx, binding.MainScript, 1, opts)
		if err == nil {
			addHits("main_script", hits)
		}
	}

	// 3) Text search from process name + top symbol funcs (sample frames).
	if len(out) < 8 {
		var parts []string
		if binding != nil {
			if binding.Name != "" {
				parts = append(parts, binding.Name)
			}
			if binding.MainScript != "" {
				parts = append(parts, filepath.Base(binding.MainScript))
			}
			if binding.Runtime != "" && binding.Runtime != procbind.RuntimeUnknown {
				parts = append(parts, string(binding.Runtime))
			}
		}
		for i, s := range symbols {
			if i >= 5 {
				break
			}
			if s.Func != "" {
				parts = append(parts, s.Func)
			}
		}
		q := strings.Join(parts, " ")
		if strings.TrimSpace(q) != "" {
			lang := ""
			if binding != nil {
				switch binding.Runtime {
				case procbind.RuntimeNode, procbind.RuntimeBun, procbind.RuntimeDeno:
					lang = "javascript"
				case procbind.RuntimeGo:
					lang = "go"
				case procbind.RuntimePython:
					lang = "python"
				}
			}
			envelope, err := ecosystem.VecgrepSearchWithReadiness(ctx, q, ecosystem.VecgrepSearchOpts{
				Dir:   codebase,
				Limit: 5,
				Mode:  "hybrid",
				Lang:  lang,
			})
			if err == nil {
				addHits("search", envelope.Hits)
				if envelope.Warning != "" {
					limitation = joinLimitation(limitation, envelope.Warning)
				}
			}
		}
	}
	return out, limitation
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func trimSnippet(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
