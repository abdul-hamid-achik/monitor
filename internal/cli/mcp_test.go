package cli

import (
	"context"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	monitormcp "github.com/abdul-hamid-achik/monitor/internal/mcp"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// leakyCollector returns a collect func for analyzeWindow tests: pid leak
// gains 2MB of RSS per call with flat CPU (a textbook memory_leak pattern
// per internal/analyzer/diagnosis.go), while pid spin holds flat RSS with
// CPU pinned high (a textbook cpu_spin pattern). Each call bumps an internal
// counter, so RSS is a clean linear ramp regardless of how many samples the
// window ends up taking.
func leakyCollector() func(context.Context) collector.SystemInfo {
	const leak, spin = int32(100), int32(200)
	n := 0
	return func(context.Context) collector.SystemInfo {
		n++
		return collector.SystemInfo{
			LastUpdate: time.Now(),
			Processes: []collector.ProcessInfo{
				{PID: leak, Name: "leaky", Memory: uint64(100_000_000 + n*2_000_000), CPUPercent: 10},
				{PID: spin, Name: "spinner", Memory: 100_000_000, CPUPercent: 95},
			},
		}
	}
}

// TestAnalyzeWindowSystemWide verifies pid==0 diagnoses every PID present in
// the window (memory_leak for the leaker, cpu_spin for the spinner).
func TestAnalyzeWindowSystemWide(t *testing.T) {
	res, err := analyzeWindow(context.Background(), leakyCollector(), 40*time.Millisecond, 5*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("analyzeWindow: %v", err)
	}
	if res.Samples < 4 {
		t.Fatalf("Samples = %d, want >= 4 (diagMinSamples)", res.Samples)
	}
	if len(res.Diagnoses) != 2 {
		t.Fatalf("expected 2 diagnoses (leak + spin), got %d: %+v", len(res.Diagnoses), res.Diagnoses)
	}
	var sawLeak, sawSpin bool
	for _, d := range res.Diagnoses {
		if strings.Contains(d.Summary, "memory leak") {
			sawLeak = true
		}
		if strings.Contains(d.Summary, "spin/hot loop") {
			sawSpin = true
		}
	}
	if !sawLeak || !sawSpin {
		t.Errorf("expected both a memory_leak and a cpu_spin diagnosis; got %+v", res.Diagnoses)
	}
}

// TestAnalyzeWindowFocusesOnPID verifies a non-zero pid drops every other
// process's findings (the boundary contract: "pid == 0: system-wide; else
// drop process-scoped findings for other PIDs").
func TestAnalyzeWindowFocusesOnPID(t *testing.T) {
	res, err := analyzeWindow(context.Background(), leakyCollector(), 40*time.Millisecond, 5*time.Millisecond, 100)
	if err != nil {
		t.Fatalf("analyzeWindow: %v", err)
	}
	if len(res.Diagnoses) != 1 {
		t.Fatalf("expected exactly 1 diagnosis scoped to pid 100, got %d: %+v", len(res.Diagnoses), res.Diagnoses)
	}
	if !strings.Contains(res.Diagnoses[0].Summary, "pid 100") {
		t.Errorf("diagnosis should name pid 100; got %q", res.Diagnoses[0].Summary)
	}
	if !strings.Contains(res.Diagnoses[0].Summary, "memory leak") {
		t.Errorf("pid 100 (leaky) should be diagnosed as a memory leak; got %q", res.Diagnoses[0].Summary)
	}
}

// TestAnalyzeWindowNoAnomalies verifies a flat, quiet system produces no
// diagnoses (empty, not nil-vs-empty is the handler's job — this only checks
// the engine layer).
func TestAnalyzeWindowNoAnomalies(t *testing.T) {
	collect := func(context.Context) collector.SystemInfo {
		return collector.SystemInfo{
			LastUpdate: time.Now(),
			Processes:  []collector.ProcessInfo{{PID: 1, Name: "quiet", Memory: 100_000_000, CPUPercent: 2}},
		}
	}
	res, err := analyzeWindow(context.Background(), collect, 40*time.Millisecond, 5*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("analyzeWindow: %v", err)
	}
	if len(res.Diagnoses) != 0 {
		t.Errorf("expected no diagnoses for a flat system, got %+v", res.Diagnoses)
	}
}

// TestAnalyzeWindowSurfacesAlerts is the wire regression for bug 17's second
// half: analyzeWindow's engine (NewDefaultEngine) runs the same rules
// `monitor watch` does, but analyzeWindow used to discard every Alert
// Observe returned and surface only the separate cross-signal Diagnose
// table. A zombie process is flat CPU/RSS (never a Diagnose finding) but
// must always raise a zombie_process Alert, so seeing it in res.Alerts
// proves the plain rule findings now reach the caller. It also covers the
// dedup-by-rule+PID requirement (a zombie present for the whole window would
// otherwise repeat once per sample) and the pid filter already covered for
// Diagnoses above.
func TestAnalyzeWindowSurfacesAlerts(t *testing.T) {
	const zombiePID = int32(300)
	collect := func(context.Context) collector.SystemInfo {
		return collector.SystemInfo{
			LastUpdate: time.Now(),
			Processes: []collector.ProcessInfo{
				{PID: zombiePID, Name: "reaper-orphan", Status: "Z", Memory: 0, CPUPercent: 0},
			},
		}
	}
	res, err := analyzeWindow(context.Background(), collect, 40*time.Millisecond, 5*time.Millisecond, 0)
	if err != nil {
		t.Fatalf("analyzeWindow: %v", err)
	}
	if res.Samples < 4 {
		t.Fatalf("Samples = %d, want >= 4 samples to prove dedup across repeats", res.Samples)
	}
	if len(res.Alerts) != 1 {
		t.Fatalf("Alerts = %+v, want exactly 1 deduplicated zombie_process alert across %d samples", res.Alerts, res.Samples)
	}
	if res.Alerts[0].Rule != "zombie_process" || res.Alerts[0].PID != zombiePID {
		t.Errorf("Alerts[0] = %+v, want rule=zombie_process pid=%d", res.Alerts[0], zombiePID)
	}

	// Focusing on an unrelated PID must drop the zombie alert, same as it
	// drops another process's Diagnosis.
	focused, err := analyzeWindow(context.Background(), collect, 40*time.Millisecond, 5*time.Millisecond, zombiePID+1)
	if err != nil {
		t.Fatalf("analyzeWindow (focused): %v", err)
	}
	if len(focused.Alerts) != 0 {
		t.Errorf("Alerts = %+v with pid filter set to a different PID, want none", focused.Alerts)
	}
}

// TestAnalyzeWindowContextCancelled verifies a cancelled context aborts the
// sampling loop with ctx.Err() instead of running to the deadline.
func TestAnalyzeWindowContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	calls := 0
	collect := func(context.Context) collector.SystemInfo {
		calls++
		if calls == 2 {
			cancel()
		}
		return collector.SystemInfo{LastUpdate: time.Now()}
	}
	_, err := analyzeWindow(ctx, collect, time.Hour, time.Millisecond, 0)
	if err == nil {
		t.Fatal("expected an error from a cancelled context")
	}
	if err != context.Canceled {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// TestListIssuesForMCPForwardsWindowFiltersToStore verifies the
// mcp.Service.IssuesList wiring (E2.6): listIssuesForMCP just opens the
// resolved store read-only and forwards opts to Store.List -- the filters
// it receives here are exactly what internal/mcp/server.go's handleIssues
// built from the typed MCP input.
func TestListIssuesForMCPForwardsWindowFiltersToStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	t.Setenv(issues.StorePathEnv, path)
	store, err := issues.OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if _, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		Project: "monitor", Kind: issues.KindException, Message: "boom", RunID: "run-a", Release: "rel-a",
	}); err != nil {
		t.Fatalf("seed exception issue: %v", err)
	}
	if _, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		Project: "monitor", Kind: "monitor.alert.cpu_spike", Message: "cpu spike",
	}); err != nil {
		t.Fatalf("seed alert issue: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	got, err := listIssuesForMCP(context.Background(), monitormcp.IssuesListFilter{Kind: "exception"})
	if err != nil {
		t.Fatalf("listIssuesForMCP: %v", err)
	}
	if len(got) != 1 || got[0].Kind != issues.KindException {
		t.Fatalf("Kind=exception results = %+v", got)
	}

	got, err = listIssuesForMCP(context.Background(), monitormcp.IssuesListFilter{RunID: "run-a"})
	if err != nil {
		t.Fatalf("listIssuesForMCP: %v", err)
	}
	if len(got) != 1 || got[0].Kind != issues.KindException {
		t.Fatalf("RunID=run-a results = %+v", got)
	}

	all, err := listIssuesForMCP(context.Background(), monitormcp.IssuesListFilter{})
	if err != nil {
		t.Fatalf("listIssuesForMCP: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("unfiltered results = %+v, want 2", all)
	}

	// Since/Until (E2.6): listIssuesForMCP is the one place that turns the
	// MCP tool's raw since/until strings into time.Time via the shared
	// issues.ParseWindowBound, matching handleIssues' pure-field-copy
	// contract (see monitormcp.IssuesListFilter's doc comment). A relative
	// duration reaches the seeded issue; an unparseable value is a
	// structured error, not a panic or a silently-ignored filter.
	sinceMatch, err := listIssuesForMCP(context.Background(), monitormcp.IssuesListFilter{Since: "24h"})
	if err != nil {
		t.Fatalf("listIssuesForMCP Since=24h: %v", err)
	}
	if len(sinceMatch) != 2 {
		t.Fatalf("Since=24h results = %+v, want both issues", sinceMatch)
	}
	if _, err := listIssuesForMCP(context.Background(), monitormcp.IssuesListFilter{Since: "not-a-time"}); err == nil {
		t.Fatal("listIssuesForMCP Since=not-a-time did not return an error")
	}
	if _, err := listIssuesForMCP(context.Background(), monitormcp.IssuesListFilter{Until: "not-a-time"}); err == nil {
		t.Fatal("listIssuesForMCP Until=not-a-time did not return an error")
	}
}

// TestBuildProfileServiceKeepPassthrough verifies the keep parameter
// buildProfileService receives from monitor_profile_capture's typed
// keep:true/false input reaches the verify-then-discard decision the
// Service now owns (E1.7's keep/discard policy — see server.go's
// Service.Profile doc comment): keep:false discards Path/Text after
// verifying, keep:true leaves both in place.
func TestBuildProfileServiceKeepPassthrough(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipOwned, ""
	}
	tmpDir := t.TempDir()
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		f, err := os.CreateTemp(tmpDir, "monitor-heap-*.pb.gz")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("raw bytes"); err != nil {
			t.Fatal(err)
		}
		_ = f.Close()
		return profiler.Profile{PID: pid, Type: ptype, Path: f.Name(), Text: "heap profile: 1"}, nil
	}
	svc := buildProfileService()

	discarded, receipt, err := svc(context.Background(), 1, profiler.ProfileHeap, "", false)
	if err != nil {
		t.Fatalf("keep:false: %v", err)
	}
	if !receipt.Verified {
		t.Fatalf("keep:false: receipt = %+v, want Verified", receipt)
	}
	if discarded.Path != "" || discarded.Text != "" {
		t.Errorf("keep:false: profile = %+v, want Path and Text both discarded", discarded)
	}

	kept, receipt2, err := svc(context.Background(), 2, profiler.ProfileHeap, "", true)
	if err != nil {
		t.Fatalf("keep:true: %v", err)
	}
	if !receipt2.Verified {
		t.Fatalf("keep:true: receipt = %+v, want Verified", receipt2)
	}
	if kept.Path == "" || kept.Text == "" {
		t.Errorf("keep:true: profile = %+v, want Path and Text both retained", kept)
	}
	if _, statErr := os.Stat(kept.Path); statErr != nil {
		t.Errorf("keep:true: on-disk file gone: %v", statErr)
	}
}

// TestProfileServiceLiveNodeInspectorViaRealDispatch is the in-memory MCP
// CallTool test the review flagged as missing: it drives monitor_profile_capture
// through buildProfileService() — the EXACT closure newMCPServeCmd wires
// into mcp.Service.Profile for the real `monitor mcp serve` binary — over a
// real SDK in-memory transport, against a REAL node --inspect process. This
// is what would actually fail if the production runtime-aware dispatch
// (procbind.Inspect + captureRuntimeAwareProfile) were reverted or broken;
// server_test.go's own live-node test only exercises a hand-rolled CDP
// stub, not this wiring, and stays scoped to the MCP wire/schema round trip.
func TestProfileServiceLiveNodeInspectorViaRealDispatch(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	workload, err := filepath.Abs(filepath.Join("..", "..", "examples", "polyglot", "js", "workload.js"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(workload); statErr != nil {
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
		t.Fatalf("node inspector never came up on %s", addr)
	}
	// Let the workload's setInterval hot loop accumulate real samples
	// before the profiler window starts.
	time.Sleep(300 * time.Millisecond)

	svc := &monitormcp.Service{Profile: buildProfileService()}
	s := monitormcp.NewServer(svc, "test")

	ctx := context.Background()
	clientTr, serverTr := sdkmcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, serverTr)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Close() }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := client.Connect(ctx, clientTr, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "monitor_profile_capture",
		Arguments: map[string]any{"pid": pid, "type": "cpu", "confirm": true},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent type = %T, want map[string]any (content=%+v)", res.StructuredContent, res.Content)
	}
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
	line, ok := first["line"].(float64)
	if !ok || line <= 0 {
		t.Errorf("symbols[0].line = %v, want a positive line number (file:line, not just a function name): %+v", first["line"], first)
	}
	if _, ok := first["func"]; !ok {
		t.Errorf("symbols[0] missing a 'func' field: %+v", first)
	}
}

// TestIssueContextForMCPWireLatestIgnoresAlertsAndRespectsProject is E2.7's
// wire test: monitor_issue {id:"latest", project:...} driven through
// issueContextForMCP -- the EXACT closure newMCPServeCmd wires into
// mcp.Service.IssueContext for the real `monitor mcp serve` binary -- over a
// real SDK in-memory transport, against a real isolated issues store (not a
// hand-rolled stub; server_test.go's own IssueContext tests stay scoped to
// the MCP wire/schema round trip with a fake Service).
func TestIssueContextForMCPWireLatestIgnoresAlertsAndRespectsProject(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	t.Setenv(issues.StorePathEnv, storePath)

	now := time.Now().UTC()
	wantIssue := seedMCPException(t, storePath, "polyglot", "workload", now.Add(-time.Hour))
	// More recently active, but an alert -- "latest" must ignore it
	// (kind defaults to "exception").
	seedMCPAlert(t, storePath, "polyglot", now)
	// More recently active AND an exception, but a DIFFERENT project --
	// "latest" must respect the project filter.
	seedMCPException(t, storePath, "other-project", "svc", now)

	svc := &monitormcp.Service{IssueContext: issueContextForMCP}
	s := monitormcp.NewServer(svc, "test")

	ctx := context.Background()
	clientTr, serverTr := sdkmcp.NewInMemoryTransports()
	ss, err := s.Connect(ctx, serverTr)
	if err != nil {
		t.Fatalf("server connect: %v", err)
	}
	defer func() { _ = ss.Close() }()
	client := sdkmcp.NewClient(&sdkmcp.Implementation{Name: "test-client", Version: "0.0.0"}, nil)
	cs, err := client.Connect(ctx, clientTr, nil)
	if err != nil {
		t.Fatalf("client connect: %v", err)
	}
	defer func() { _ = cs.Close() }()

	res, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "monitor_issue",
		Arguments: map[string]any{"id": "latest", "project": "polyglot"},
	})
	if err != nil {
		t.Fatalf("CallTool: %v", err)
	}
	m, ok := res.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent type = %T, want map[string]any (content=%+v)", res.StructuredContent, res.Content)
	}
	issue, ok := m["issue"].(map[string]any)
	if !ok {
		t.Fatalf("issue field missing or wrong type (%T); payload=%v", m["issue"], m)
	}
	if issue["id"] != wantIssue.ID {
		t.Fatalf("resolved issue = %v, want %s (alerts must be excluded, project must restrict)", issue["id"], wantIssue.ID)
	}
	resolvedFrom, ok := m["resolved_from"].(map[string]any)
	if !ok || resolvedFrom["project"] != "polyglot" || resolvedFrom["kind"] != "exception" {
		t.Fatalf("resolved_from = %v", m["resolved_from"])
	}
	// The DEFAULT response is the bounded monitor.issue_context.v1 brief --
	// no legacy occurrences (AC-6's size budget applies to what a caller
	// gets without asking for more).
	if m["schema"] != "monitor.issue_context.v1" || m["budget"] != "brief" {
		t.Fatalf("schema/budget = %v/%v, want the brief issue_context.v1", m["schema"], m["budget"])
	}
	if _, ok := m["occurrences"]; ok {
		t.Fatalf("legacy occurrences field present by default: %v, want it opt-in via occurrence_limit", m)
	}

	// occurrence_limit > 0 opts into the legacy {issue, occurrences,
	// occurrences_truncated} shape, in the SAME response.
	resLegacy, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "monitor_issue",
		Arguments: map[string]any{"id": "latest", "project": "polyglot", "occurrence_limit": 5},
	})
	if err != nil {
		t.Fatalf("CallTool (occurrence_limit): %v", err)
	}
	mLegacy, ok := resLegacy.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("StructuredContent type = %T", resLegacy.StructuredContent)
	}
	if _, ok := mLegacy["occurrences"]; !ok {
		t.Fatalf("legacy occurrences field missing when occurrence_limit was set: %v", mLegacy)
	}
	legacyIssue, ok := mLegacy["issue"].(map[string]any)
	if !ok || legacyIssue["id"] != wantIssue.ID {
		t.Fatalf("legacy issue = %v, want the full issue for %s", mLegacy["issue"], wantIssue.ID)
	}

	// The same query without a project filter would resolve the alert
	// (kind:any) or the other-project exception under looser filters --
	// confirming this test actually distinguishes "ignores alerts" and
	// "respects project" rather than having only one issue to find.
	resAny, err := cs.CallTool(ctx, &sdkmcp.CallToolParams{
		Name:      "monitor_issue",
		Arguments: map[string]any{"id": "latest", "project": "polyglot", "kind": "any"},
	})
	if err != nil {
		t.Fatalf("CallTool (kind=any): %v", err)
	}
	mAny := resAny.StructuredContent.(map[string]any)
	issueAny := mAny["issue"].(map[string]any)
	if issueAny["id"] == wantIssue.ID {
		t.Fatalf("kind=any resolved %v, want the more recent alert to win once alerts are no longer excluded (precondition check)", issueAny["id"])
	}
}

// TestIssueContextForMCPScrubsLegacyFreeText covers the review's exact
// repro: a raw secret embedded in the exception message must never reach
// the wire, even in the legacy issue/occurrences fields (which explain.
// Build's own Options.Redact pass never touches, since they are not part of
// its Context) -- see scrubIssueForResponse. The fake token is built by
// concatenation (never a single literal) so it cannot trip GitHub push
// protection on this test file itself.
func TestIssueContextForMCPScrubsLegacyFreeText(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	fakeToken := "ghp_" + strings.Repeat("a", 36)
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait,
		stacktrace.Exception{Runtime: "go", Type: "panic", Value: "auth failed with token " + fakeToken, Level: "fatal"},
		project.Identity{Slug: "polyglot"}, contextids.IDs{},
		issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}

	t.Setenv(issues.StorePathEnv, storePath)
	result, err := issueContextForMCP(context.Background(), res.Issue.ID, monitormcp.IssueContextFilter{}, 5)
	if err != nil {
		t.Fatalf("issueContextForMCP: %v", err)
	}
	if !result.IncludeLegacy {
		t.Fatal("IncludeLegacy = false, want true (occurrence_limit was set)")
	}
	if strings.Contains(result.Issue.Title, fakeToken) || strings.Contains(result.Issue.Message, fakeToken) {
		t.Fatalf("legacy issue leaked the raw token: title=%q message=%q", result.Issue.Title, result.Issue.Message)
	}
	if result.Issue.LatestException != nil && strings.Contains(result.Issue.LatestException.Value, fakeToken) {
		t.Fatalf("legacy issue.latest_exception.value leaked the raw token: %q", result.Issue.LatestException.Value)
	}
	for _, occ := range result.Occurrences {
		if strings.Contains(occ.Title, fakeToken) || strings.Contains(occ.Message, fakeToken) {
			t.Fatalf("legacy occurrence leaked the raw token: title=%q message=%q", occ.Title, occ.Message)
		}
	}
	if !strings.Contains(result.Issue.Title, "[token]") && !strings.Contains(result.Issue.Message, "[token]") {
		t.Errorf("issue = %+v, want the redaction marker somewhere in title/message", result.Issue)
	}
}

// seedMCPException records one exception issue via issues.RecordException
// (the same call `monitor run --`/`stacktrace parse --record` will make
// once E2.4/E2.1 land) for the wire test above.
func seedMCPException(t *testing.T, storePath, projectSlug, service string, observedAt time.Time) issues.Issue {
	t.Helper()
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait,
		stacktrace.Exception{Runtime: "go", Type: "panic", Value: projectSlug + "/" + service, Level: "fatal"},
		project.Identity{Slug: projectSlug, Service: service}, contextids.IDs{},
		issues.RecordExceptionOptions{ObservedAt: observedAt})
	if err != nil {
		t.Fatalf("seedMCPException: %v", err)
	}
	return res.Issue
}

// seedMCPAlert writes a plain watch-style alert issue (kind
// "monitor.alert.<rule>") -- alerts are never produced by RecordException.
func seedMCPAlert(t *testing.T, storePath, projectSlug string, observedAt time.Time) issues.Issue {
	t.Helper()
	var result issues.UpsertResult
	err := issues.WithWriter(context.Background(), storePath, issues.DefaultWriterWait, func(store *issues.Store) error {
		var upsertErr error
		result, upsertErr = store.UpsertOccurrenceResult(issues.OccurrenceInput{
			ObservedAt: observedAt, Project: projectSlug, Kind: "monitor.alert.cpu_spike",
			Title: "cpu_spike", Message: "cpu spike", Severity: "warning",
		})
		return upsertErr
	})
	if err != nil {
		t.Fatalf("seedMCPAlert: %v", err)
	}
	return result.Issue
}
