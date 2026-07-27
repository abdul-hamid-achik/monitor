package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/incidents"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
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

// captureInvestigateProfile captures a profile for pid. For Node/Bun/Deno
// with --inspect, it prefers the CDP inspector CPU profile (carries
// file:line → codemap correlation works). Otherwise: heap-over-pprof ONLY
// when the LISTEN socket at DefaultPprofAddr provably belongs to pid;
// else macOS `sample`.
func captureInvestigateProfile(ctx context.Context, pid int32, binding *procbind.Binding) (profiler.Profile, string, investigateStep) {
	step := investigateStep{Step: "profile"}
	var reasons []string

	jsRuntime := binding != nil && (binding.Runtime == procbind.RuntimeNode ||
		binding.Runtime == procbind.RuntimeBun ||
		binding.Runtime == procbind.RuntimeDeno)

	// 1) Node/Bun/Deno with --inspect: CDP CPU profile (file:line frames).
	if jsRuntime && binding.InspectAddr != "" {
		if own, detail := profiler.VerifyInspectorOwnership(ctx, pid, binding.InspectAddr); own == profiler.OwnershipOwned {
			prof, err := captureInspectorProfile(ctx, pid, binding.InspectAddr)
			if err == nil {
				rec := prof.VerifyArtifact()
				if rec.Verified {
					step.Status = stepOK
					step.Limitation = "CDP inspector CPU profile (file:line frames; codemap correlation enabled)"
					return prof, "inspector_cpu", step
				}
				reasons = append(reasons, "inspector: "+rec.Limitation)
			} else {
				reasons = append(reasons, "inspector: "+err.Error())
			}
		} else {
			reasons = append(reasons, fmt.Sprintf("inspector %s not proven to belong to pid %d (%s: %s)", binding.InspectAddr, pid, own, detail))
		}
	}

	// 2) Go pprof (skip for JS runtimes unless proven owned).
	tryPprof := !jsRuntime
	if jsRuntime {
		if own, _ := verifyOwnership(ctx, pid, ""); own == profiler.OwnershipOwned {
			tryPprof = true
		} else {
			reasons = append(reasons, fmt.Sprintf("runtime %s: skipping Go pprof (not applicable)", binding.Runtime))
		}
	}

	if tryPprof {
		own, detail := verifyOwnership(ctx, pid, "")
		if own == profiler.OwnershipOwned {
			prof, err := captureProfile(ctx, pid, profiler.ProfileHeap, "")
			if err == nil {
				rec := prof.VerifyArtifact()
				if rec.Verified {
					step.Status = stepOK
					return prof, "pprof_heap", step
				}
				reasons = append(reasons, "pprof heap: "+rec.Limitation)
			} else {
				reasons = append(reasons, "pprof heap: "+err.Error())
			}
		} else {
			reasons = append(reasons, fmt.Sprintf("pprof endpoint %s not proven to belong to pid %d (%s: %s)", profiler.DefaultPprofAddr, pid, own, detail))
		}
	}

	// 3) macOS sample fallback.
	if err := validateProfile(profiler.ProfileSample); err == nil {
		prof, captureErr := captureProfile(ctx, pid, profiler.ProfileSample, "")
		if captureErr == nil {
			rec := prof.VerifyArtifact()
			if rec.Verified {
				step.Status = stepOK
				extra := "used macOS sample: frames carry no file:line, so codemap correlation needs codebase+entry or vecgrep semantic fallback"
				if jsRuntime {
					extra = "used macOS sample (no --inspect or inspector unreachable): frames carry no file:line; start node with --inspect for CDP CPU profiles"
				}
				step.Limitation = strings.Join(append(reasons, extra), "; ")
				return prof, "sample", step
			}
			reasons = append(reasons, "sample: "+rec.Limitation)
		} else {
			reasons = append(reasons, "sample: "+captureErr.Error())
		}
	} else {
		reasons = append(reasons, err.Error())
	}

	step.Status = stepFailed
	step.Limitation = strings.Join(reasons, "; ")
	if jsRuntime {
		step.Recovery = "start node with --inspect (or --inspect=9230) and ensure codemap/vecgrep index the project; on macOS 'sample' is a weaker fallback"
	} else {
		step.Recovery = "start the target with net/http/pprof and re-run (or pass --pprof-addr to 'monitor profile'); on macOS ensure 'sample' can attach (same user or root) and the pid is alive"
	}
	return profiler.Profile{}, "", step
}

// captureInspectorProfile wraps profiler.ProfileInspector with a bounded
// duration so investigate doesn't block longer than ~10s.
func captureInspectorProfile(ctx context.Context, pid int32, addr string) (profiler.Profile, error) {
	durCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	return profiler.ProfileInspector(durCtx, pid, addr, 5*time.Second)
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
		PID:       pid,
		StartedAt: time.Now().Format(time.RFC3339),
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
	} else if !ecosystem.VecgrepAvailable() {
		semStep.Status = stepSkipped
		semStep.Limitation = "vecgrep not on PATH"
		semStep.Recovery = "install vecgrep and run `vecgrep index` in the project"
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
		if report.Profile != nil && report.Profile.Path != "" && (err == nil || res.Path != "") {
			// The verified bytes now live in fcheap or the resumable local
			// bundle. Remove the transient profiler copy and do not return a
			// machine-local path that is already invalid after the handoff.
			_ = os.Remove(report.Profile.Path)
			report.Profile.Path = ""
		}
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

func recordInvestigateOccurrence(report *investigateReport) (issues.Issue, issues.Occurrence, error) {
	path, err := issues.ResolvePath("")
	if err != nil {
		return issues.Issue{}, issues.Occurrence{}, err
	}
	store, err := issues.OpenStore(path)
	if err != nil {
		return issues.Issue{}, issues.Occurrence{}, err
	}
	defer store.Close()

	project := ""
	service := strings.TrimSpace(report.Context.Service)
	processName := ""
	if report.Process != nil {
		processName = strings.TrimSpace(report.Process.Name)
		if report.Process.CodebaseRoot != "" {
			project = filepath.Base(filepath.Clean(report.Process.CodebaseRoot))
		}
	}
	if project == "" {
		project = service
	}
	if project == "" {
		project = processName
	}
	if project == "" {
		project = "local"
	}
	if service == "" {
		service = processName
	}

	symbols := make([]string, 0, 10)
	seenSymbols := map[string]struct{}{}
	if report.Profile != nil {
		for _, symbol := range report.Profile.Symbols {
			name := strings.TrimSpace(symbol.Func)
			if name == "" {
				continue
			}
			if _, exists := seenSymbols[name]; exists {
				continue
			}
			seenSymbols[name] = struct{}{}
			symbols = append(symbols, name)
			if len(symbols) == 10 {
				break
			}
		}
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
	} {
		if value != "" {
			metadata[key] = value
		}
	}
	observedAt, _ := time.Parse(time.RFC3339, report.StartedAt)
	titleSubject := firstNonEmpty(service, project)
	return store.UpsertOccurrence(issues.OccurrenceInput{
		ObservedAt: observedAt, Project: project, Service: service, Kind: "investigation",
		Title: "Investigation: " + titleSubject, Message: "manual process investigation",
		Symbols: symbols, Severity: "warning", RunID: report.Context.RunID,
		Release: report.Context.Release, PID: report.PID, TreeHash: treeHash,
		EvidenceRefs: evidence, Evidence: typedEvidence, Metadata: metadata,
		Run: &issues.RunContext{
			ID: report.Context.RunID, Environment: report.Context.Environment,
			DeploymentID: report.Context.DeploymentID, StepID: report.Context.StepID,
			Suite: report.Context.Suite, Attempt: report.Context.Attempt,
			Release: report.Context.Release, GitSHA: report.Context.GitSHA,
		},
	})
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
