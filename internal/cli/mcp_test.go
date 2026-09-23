package cli

import (
	"context"
	"encoding/json"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gpprof "github.com/google/pprof/profile"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	monitormcp "github.com/abdul-hamid-achik/monitor/internal/mcp"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
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

	res1, err := svc(context.Background(), 1, profiler.ProfileHeap, "", false, false)
	if err != nil {
		t.Fatalf("keep:false: %v", err)
	}
	if !res1.Receipt.Verified {
		t.Fatalf("keep:false: receipt = %+v, want Verified", res1.Receipt)
	}
	if res1.Profile.Path != "" || res1.Profile.Text != "" {
		t.Errorf("keep:false: profile = %+v, want Path and Text both discarded", res1.Profile)
	}

	res2, err := svc(context.Background(), 2, profiler.ProfileHeap, "", true, false)
	if err != nil {
		t.Fatalf("keep:true: %v", err)
	}
	if !res2.Receipt.Verified {
		t.Fatalf("keep:true: receipt = %+v, want Verified", res2.Receipt)
	}
	if res2.Profile.Path == "" || res2.Profile.Text == "" {
		t.Errorf("keep:true: profile = %+v, want Path and Text both retained", res2.Profile)
	}
	if _, statErr := os.Stat(res2.Profile.Path); statErr != nil {
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

// TestProfileServiceLiveNodeInspectorLinesTrueNamesHotLine is E3.6's own
// "wire test": monitor_profile_capture's lines:true, over a real in-memory
// MCP CallTool, against a REAL node --inspect process (the same rig
// TestProfileServiceLiveNodeInspectorViaRealDispatch uses) — the response
// must name examples/polyglot/js/workload.js's planted hot line (17, `s +=
// JSON.stringify(...)` inside heavyStringify — see workload.js's own "HOT
// LINE" comment), stay under a small size budget, and never mention a
// ws:// inspector URL anywhere in the payload.
func TestProfileServiceLiveNodeInspectorLinesTrueNamesHotLine(t *testing.T) {
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
		Arguments: map[string]any{"pid": pid, "type": "cpu", "lines": true, "confirm": true},
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
	lh, ok := m["line_heatmap"].(map[string]any)
	if !ok {
		t.Fatalf("line_heatmap missing or wrong type (%T); payload=%v", m["line_heatmap"], m)
	}
	if lh["schema"] != "monitor.line_heatmap.v1" {
		t.Fatalf("line_heatmap.schema = %v, want monitor.line_heatmap.v1 (payload may have degraded to a skip): %v", lh["schema"], lh)
	}
	functions, ok := lh["functions"].([]any)
	if !ok || len(functions) == 0 {
		t.Fatalf("line_heatmap.functions = %v, want at least one function", lh["functions"])
	}
	var sawLine17 bool
	var hottestFunc string
	var hottestLine float64
	var hottestSelf float64 = -1
	for _, fv := range functions {
		f, ok := fv.(map[string]any)
		if !ok {
			continue
		}
		lines, _ := f["lines"].([]any)
		for _, lv := range lines {
			l, ok := lv.(map[string]any)
			if !ok {
				continue
			}
			if line, ok := l["line"].(float64); ok && int(line) == 17 {
				sawLine17 = true
			}
			self, _ := l["self"].(float64)
			if self > hottestSelf {
				hottestSelf = self
				hottestFunc, _ = f["name"].(string)
				hottestLine, _ = l["line"].(float64)
			}
		}
	}
	if !sawLine17 {
		t.Errorf("line_heatmap never names the planted hot line 17: %+v", lh)
	}
	// Not just "17 appears somewhere" -- it must be the actual HOTTEST
	// line by self weight, in the function workload.js's own hot loop
	// lives in (heavyStringify), matching mcp_profile_lines.yml's own
	// stronger assertion.
	if hottestFunc != "heavyStringify" || int(hottestLine) != 17 {
		t.Errorf("hottest line by self = %s:%v, want heavyStringify:17: %+v", hottestFunc, hottestLine, lh)
	}

	raw, err := json.Marshal(m)
	if err != nil {
		t.Fatalf("marshal payload for size/content checks: %v", err)
	}
	if strings.Contains(string(raw), "ws://") {
		t.Errorf("payload must never carry a ws:// inspector URL: %s", raw)
	}
	lhRaw, err := json.Marshal(lh)
	if err != nil {
		t.Fatalf("marshal line_heatmap for size check: %v", err)
	}
	if len(lhRaw) > 6144 {
		t.Errorf("line_heatmap is %d bytes, want <= 6144 (E3.6's payload budget)", len(lhRaw))
	}
}

// writeFakePprofCPU builds a minimal, valid CPU pprof proto (>3 functions,
// one with >8 distinct lines) and saves it gzip-compressed to a temp file —
// the same on-disk shape profiler.LoadFile / BuildHeatmap consume — so
// E3.6's payload-bounding (top 3 functions, top 8 lines each) can be
// exercised deterministically, without a real subprocess capture.
func writeFakePprofCPU(t *testing.T) string {
	t.Helper()
	fn := func(id uint64, name, file string) *gpprof.Function {
		return &gpprof.Function{ID: id, Name: name, Filename: file}
	}
	loc := func(id uint64, f *gpprof.Function, line int64) *gpprof.Location {
		return &gpprof.Location{ID: id, Line: []gpprof.Line{{Function: f, Line: line}}}
	}

	prof := &gpprof.Profile{
		SampleType:    []*gpprof.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		PeriodType:    &gpprof.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:        1000000,
		DurationNanos: 5 * int64(time.Second),
	}

	var locID uint64
	addFuncSamples := func(name string, weights []int64) {
		f := fn(uint64(len(prof.Function)+1), name, "go-pprof/main.go")
		prof.Function = append(prof.Function, f)
		for i, w := range weights {
			locID++
			l := loc(locID, f, int64(10+i))
			prof.Location = append(prof.Location, l)
			prof.Sample = append(prof.Sample, &gpprof.Sample{Location: []*gpprof.Location{l}, Value: []int64{w}})
		}
	}

	// 4 functions (only the top 3 by weight should survive --top 3): 10
	// distinct lines on the hottest one (only 8 should survive per-function
	// trimming).
	addFuncSamples("main.heavyStringify", []int64{100, 90, 80, 70, 60, 50, 40, 30, 20, 500})
	addFuncSamples("main.processBatch", []int64{15})
	addFuncSamples("main.flakyParse", []int64{10})
	addFuncSamples("main.coldFunc", []int64{1})

	path := filepath.Join(t.TempDir(), "fake-cpu.pb.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := prof.Write(f); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBuildProfileServiceLinesTrueBoundsHeatmap is the E3.6 Service-level
// regression: lines:true builds a real monitor.line_heatmap.v1 from the
// SAME capture, bounded to the top 3 functions and top 8 lines each —
// verified against a synthetic proto that deliberately carries more than
// both caps so the trimming itself is exercised, not just its absence.
func TestBuildProfileServiceLinesTrueBoundsHeatmap(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipOwned, ""
	}
	path := writeFakePprofCPU(t)
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		return profiler.Profile{PID: pid, Type: ptype, Method: "pprof_cpu", Path: path}, nil
	}

	svc := buildProfileService()
	res, err := svc(context.Background(), 1, profiler.ProfileCPU, "", false, true)
	if err != nil {
		t.Fatalf("lines:true: %v", err)
	}
	hm, ok := res.LineHeatmapPayload.(*profiler.Heatmap)
	if !ok || hm == nil {
		t.Fatalf("expected LineHeatmapPayload to hold a *profiler.Heatmap, got %#v", res.LineHeatmapPayload)
	}
	if got := len(hm.Functions); got > mcpHeatmapMaxFunctions {
		t.Errorf("len(Functions) = %d, want <= %d", got, mcpHeatmapMaxFunctions)
	}
	for _, f := range hm.Functions {
		if got := len(f.Lines); got > mcpHeatmapMaxLines {
			t.Errorf("function %s: len(Lines) = %d, want <= %d", f.Name, got, mcpHeatmapMaxLines)
		}
		// boundHeatmapLines must leave lines in ascending order, the same
		// reading order every other Heatmap consumer expects.
		for i := 1; i < len(f.Lines); i++ {
			if f.Lines[i].Line < f.Lines[i-1].Line {
				t.Errorf("function %s: lines out of order: %+v", f.Name, f.Lines)
			}
		}
	}
	// The synthetic proto's hottest, highest-weight line (500 at line 19,
	// the last weight in the heavyStringify series above) must have
	// survived the top-8 trim, not an arbitrary line-number-ordered prefix.
	var sawHotLine bool
	for _, f := range hm.Functions {
		if f.Name != "main.heavyStringify" {
			continue
		}
		for _, l := range f.Lines {
			if l.Line == 19 {
				sawHotLine = true
			}
		}
	}
	if !sawHotLine {
		t.Errorf("expected the hottest line (19) to survive the top-%d-by-weight trim: %+v", mcpHeatmapMaxLines, hm.Functions)
	}
}

// TestBuildProfileServiceLinesTrueKeepsDefaultTargetBeyondTopN is the E3.6
// review regression: a 4-deep call stack (ancestor -> ancestor -> ancestor
// -> the real hot leaf) must still surface the leaf's own line in
// LineHeatmapPayload — mcpSelectFunctions must not let a plain top-3-by-cum
// cap drop the one function that actually burns CPU just because three
// near-zero-self wrapper ancestors outrank it by cumulative weight.
func TestBuildProfileServiceLinesTrueKeepsDefaultTargetBeyondTopN(t *testing.T) {
	defer restoreStubs()()
	verifyOwnership = func(context.Context, int32, string) (profiler.PortOwnership, string) {
		return profiler.OwnershipOwned, ""
	}
	path := writeFakeDeepPprofCPU(t)
	captureProfile = func(_ context.Context, pid int32, ptype profiler.ProfileType, _ string) (profiler.Profile, error) {
		return profiler.Profile{PID: pid, Type: ptype, Method: "pprof_cpu", Path: path}, nil
	}

	svc := buildProfileService()
	res, err := svc(context.Background(), 1, profiler.ProfileCPU, "", false, true)
	if err != nil {
		t.Fatalf("lines:true: %v", err)
	}
	hm, ok := res.LineHeatmapPayload.(*profiler.Heatmap)
	if !ok || hm == nil {
		t.Fatalf("expected LineHeatmapPayload to hold a *profiler.Heatmap, got %#v", res.LineHeatmapPayload)
	}
	if got := len(hm.Functions); got > mcpHeatmapMaxFunctions {
		t.Errorf("len(Functions) = %d, want <= %d", got, mcpHeatmapMaxFunctions)
	}
	var sawHotFunc bool
	for _, f := range hm.Functions {
		if f.Name != "main.hot" {
			continue
		}
		sawHotFunc = true
		for _, l := range f.Lines {
			if l.Line == 3 {
				return
			}
		}
	}
	if !sawHotFunc {
		t.Fatalf("expected main.hot (the real leaf, highest self) to survive the top-%d cap: %+v", mcpHeatmapMaxFunctions, hm.Functions)
	}
	t.Fatalf("main.hot survived the cap but its hot line (3) did not: %+v", hm.Functions)
}

// writeFakeDeepPprofCPU builds a synthetic 4-deep pprof CPU proto
// (handler -> service -> repo -> hot, mirroring the E3.6 review's own
// "deep.js" reproduction): every ancestor frame is a near-zero-self wrapper
// with a HIGHER cumulative weight than the real leaf (hot), which is where
// all the actual CPU time (and its line-level detail) lives.
func writeFakeDeepPprofCPU(t *testing.T) string {
	t.Helper()
	fn := func(id uint64, name, file string) *gpprof.Function {
		return &gpprof.Function{ID: id, Name: name, Filename: file}
	}
	loc := func(id uint64, f *gpprof.Function, line int64) *gpprof.Location {
		return &gpprof.Location{ID: id, Line: []gpprof.Line{{Function: f, Line: line}}}
	}

	prof := &gpprof.Profile{
		SampleType:    []*gpprof.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		PeriodType:    &gpprof.ValueType{Type: "cpu", Unit: "nanoseconds"},
		Period:        1000000,
		DurationNanos: 5 * int64(time.Second),
	}

	addFunc := func(id uint64, name string) *gpprof.Function {
		f := fn(id, name, "deep.go")
		prof.Function = append(prof.Function, f)
		return f
	}
	addLoc := func(id uint64, f *gpprof.Function, line int64) *gpprof.Location {
		l := loc(id, f, line)
		prof.Location = append(prof.Location, l)
		return l
	}

	handlerFn := addFunc(1, "main.handler")
	serviceFn := addFunc(2, "main.service")
	repoFn := addFunc(3, "main.repo")
	hotFn := addFunc(4, "main.hot")
	handlerLoc := addLoc(1, handlerFn, 2)
	serviceLoc := addLoc(2, serviceFn, 3)
	repoLoc := addLoc(3, repoFn, 4)
	hotLoc := addLoc(4, hotFn, 3)

	addSample := func(stack []*gpprof.Location, weight int64) {
		prof.Sample = append(prof.Sample, &gpprof.Sample{Location: stack, Value: []int64{weight}})
	}
	// main.hot itself: the real leaf, one big self-time weight, so it's
	// unambiguously DefaultTarget (highest SELF of anything in this proto).
	addSample([]*gpprof.Location{hotLoc, repoLoc, serviceLoc, handlerLoc}, 1000)
	// Three OTHER same-ancestor leaves (siblings main.hot's own stack never
	// shares) that between them push handler/service/repo's own CUMULATIVE
	// total well past main.hot's — the exact shape ("handler -> service ->
	// repo -> hot" plus other work under the same three wrappers) that
	// makes a plain top-3-by-CUM cap drop main.hot even though it is, by
	// far, the hottest thing by SELF.
	for i, name := range []string{"main.other1", "main.other2", "main.other3"} {
		otherFn := addFunc(uint64(5+i), name)
		otherLoc := addLoc(uint64(5+i), otherFn, int64(10+i))
		addSample([]*gpprof.Location{otherLoc, repoLoc, serviceLoc, handlerLoc}, 400)
	}

	path := filepath.Join(t.TempDir(), "fake-deep-cpu.pb.gz")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := prof.Write(f); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBuildMCPLineHeatmapSkipsForSampleCapture is lines:true's honest
// degradation regression: a macOS `sample` capture has no file:line detail
// to build a heatmap from at all, and must degrade to a HeatmapSkip rather
// than an empty or fabricated one. prof.Text is set here — a REAL `sample`
// capture always carries its raw call-graph dump there (see
// internal/profiler/sample_darwin.go's captureSample, `p.Text =
// string(out)`), a shape the OLD code path used to mis-stage as a
// .cpuprofile and fail with a confusing "neither a V8/Bun .cpuprofile nor a
// pprof profile" parse error that leaked an internal temp path. The fix
// (buildMCPLineHeatmap checking prof.Method BEFORE ever staging anything)
// is what this test guards: a clean, method-aware skip, never that leak.
func TestBuildMCPLineHeatmapSkipsForSampleCapture(t *testing.T) {
	prof := profiler.Profile{PID: 1, Type: profiler.ProfileSample, Method: "sample", Text: "Call graph:\n    100 Thread_1\n"}
	hm, skip := buildMCPLineHeatmap(context.Background(), prof, nil)
	if hm != nil {
		t.Errorf("expected a nil Heatmap for a sample capture, got %+v", hm)
	}
	if skip == nil || skip.Detail == "" {
		t.Fatalf("expected a non-empty HeatmapSkip, got %+v", skip)
	}
	if strings.Contains(skip.Detail, "/tmp") || strings.Contains(skip.Detail, "TestBuild") {
		t.Errorf("skip.Detail must never leak an internal temp path, got %q", skip.Detail)
	}
	if strings.Contains(skip.Detail, "neither a V8/Bun") {
		t.Errorf("skip.Detail must not misdescribe a sample dump as a malformed .cpuprofile, got %q", skip.Detail)
	}
	if skip.Recovery == "" {
		t.Errorf("expected a non-empty Recovery hint, got %+v", skip)
	}
}

// TestBuildMCPLineHeatmapSkipRecoveryNeverSuggestsTheTypeThatJustFailed
// covers the JS-runtime skip's own recovery text: type:heap against a
// Node/Deno target has no line-level detail (see internal/cli/hot.go's own
// JS-runtime heap/goroutine guard), and telling the caller to "retry with
// type:heap" — the exact request that just failed — would be dishonest.
func TestBuildMCPLineHeatmapSkipRecoveryNeverSuggestsTheTypeThatJustFailed(t *testing.T) {
	prof := profiler.Profile{PID: 1, Type: profiler.ProfileHeap, Method: "inspector_heap"}
	binding := procbind.Binding{Runtime: procbind.RuntimeNode}
	hm, skip := buildMCPLineHeatmap(context.Background(), prof, &binding)
	if hm != nil {
		t.Errorf("expected a nil Heatmap for an inspector_heap capture, got %+v", hm)
	}
	if skip == nil {
		t.Fatal("expected a non-nil HeatmapSkip")
	}
	if strings.Contains(skip.Recovery, "type:heap") {
		t.Errorf("Recovery must not suggest retrying type:heap, the type that just failed: %q", skip.Recovery)
	}
	if !strings.Contains(skip.Recovery, "type:cpu") {
		t.Errorf("Recovery should point at type:cpu (the one that can actually work for Node/Deno): %q", skip.Recovery)
	}
}
