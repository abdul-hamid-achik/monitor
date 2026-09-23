package profiler

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	gpprof "github.com/google/pprof/profile"
)

func findHeatFunc(t *testing.T, hm *Heatmap, name string) HeatFunction {
	t.Helper()
	for _, f := range hm.Functions {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("function %q not found in %d functions", name, len(hm.Functions))
	return HeatFunction{}
}

// hottestLine returns the HeatLine with the highest Self within f.
func hottestLine(t *testing.T, f HeatFunction) HeatLine {
	t.Helper()
	if len(f.Lines) == 0 {
		t.Fatalf("function %q has no lines", f.Name)
	}
	best := f.Lines[0]
	for _, l := range f.Lines[1:] {
		if l.Self > best.Self {
			best = l
		}
	}
	return best
}

// TestBuildHeatmapFromV8FixtureNamesHotLine is AC-1's headline case:
// v8-hot.cpuprofile's first symbol must be line 5 of hot.js (the
// JSON.stringify call), not the line-1 declaration — flattenCDPProfile
// already proved this at the flat-symbol level (inspector_test.go);
// BuildHeatmap must reproduce it in the grouped, per-function model.
func TestBuildHeatmapFromV8FixtureNamesHotLine(t *testing.T) {
	src, err := LoadFile("testdata/v8-hot.cpuprofile")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	// NoReadCode: the committed fixture's callFrame.url is the honest
	// "/repo/..." placeholder convention (see testdata/README.md), which
	// intentionally doesn't exist on disk — readCode's own behavior against
	// a real file is covered separately by
	// TestBuildHeatmapReadsCodeFromDisk.
	hm, err := BuildHeatmap(context.Background(), src, HeatOptions{NoCodemap: true, NoSourcemaps: true, NoReadCode: true})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	if hm.Method != MethodV8PositionTicks {
		t.Errorf("Method = %q, want %q", hm.Method, MethodV8PositionTicks)
	}
	if hm.Unit != "samples" {
		t.Errorf("Unit = %q, want samples", hm.Unit)
	}

	f := findHeatFunc(t, hm, "heavyStringify")
	hot := hottestLine(t, f)
	if hot.Line != 5 {
		t.Errorf("hot line = %d, want 5 (s += JSON.stringify(o))", hot.Line)
	}
	// README: line 5 carries ~66% of heavyStringify's OWN self ticks
	// (615 of 930: line 3 has 10, line 4 has 304, line 6 has 1).
	if hot.PctOfFunction < 60 || hot.PctOfFunction > 70 {
		t.Errorf("PctOfFunction = %.1f, want ~66%%", hot.PctOfFunction)
	}
	// Cum at the line level must equal Self for a CDP source — V8
	// positionTicks are self-time only; see the Limitations note.
	if hot.Cum != hot.Self {
		t.Errorf("line Cum = %d, Self = %d; a CDP source must report them equal", hot.Cum, hot.Self)
	}
}

// TestBuildHeatmapFromBunFixtureNamesHotLine mirrors the V8 case for Bun's
// own .cpuprofile output (same wire shape, different producer).
func TestBuildHeatmapFromBunFixtureNamesHotLine(t *testing.T) {
	src, err := LoadFile("testdata/bun-cpu-prof.cpuprofile")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	hm, err := BuildHeatmap(context.Background(), src, HeatOptions{NoCodemap: true, NoSourcemaps: true, NoReadCode: true})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	f := findHeatFunc(t, hm, "heavyStringify")
	hot := hottestLine(t, f)
	if hot.Line != 5 {
		t.Errorf("hot line = %d, want 5", hot.Line)
	}
}

// TestBuildHeatmapIdleWarning constructs a synthetic CDP profile that is
// almost entirely (idle) and asserts the AC-5 honesty warning appears
// instead of pointing at whatever line happened to be running when the
// sampler fired.
func TestBuildHeatmapIdleWarning(t *testing.T) {
	raw := `{
		"nodes": [
			{"id":1,"callFrame":{"functionName":"(root)","url":"","lineNumber":-1},"hitCount":0,"children":[2,3]},
			{"id":2,"callFrame":{"functionName":"(idle)","url":"","lineNumber":-1},"hitCount":950,"children":[]},
			{"id":3,"callFrame":{"functionName":"doWork","url":"file:///repo/app.js","lineNumber":4},"hitCount":50,"children":[],"positionTicks":[{"line":6,"ticks":50}]}
		],
		"samples": [2],
		"startTime": 0, "endTime": 1000
	}`
	var cp cdpProfile
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		t.Fatal(err)
	}
	hm, err := BuildHeatmap(context.Background(), &Source{Kind: SourceCDP, CDP: &cp}, HeatOptions{NoCodemap: true, NoSourcemaps: true, NoReadCode: true})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	if hm.IdlePct <= 50 {
		t.Fatalf("IdlePct = %.1f, want >50 for a 950/1000 idle profile", hm.IdlePct)
	}
	found := false
	for _, w := range hm.Warnings {
		if strings.Contains(w, "mostly idle") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want one containing %q", hm.Warnings, "mostly idle")
	}
}

// TestBuildHeatmapDiffuseWarning asserts the second AC-5 honesty warning:
// when even the top function barely dominates active samples, the profile
// is too spread out to point at one line.
func TestBuildHeatmapDiffuseWarning(t *testing.T) {
	raw := `{
		"nodes": [
			{"id":1,"callFrame":{"functionName":"(root)","url":"","lineNumber":-1},"hitCount":0,"children":[2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21,22]},
			{"id":2,"callFrame":{"functionName":"f1","url":"file:///repo/app.js","lineNumber":0},"hitCount":5,"children":[],"positionTicks":[{"line":1,"ticks":5}]},
			{"id":3,"callFrame":{"functionName":"f2","url":"file:///repo/app.js","lineNumber":1},"hitCount":5,"children":[],"positionTicks":[{"line":2,"ticks":5}]},
			{"id":4,"callFrame":{"functionName":"f3","url":"file:///repo/app.js","lineNumber":2},"hitCount":5,"children":[],"positionTicks":[{"line":3,"ticks":5}]},
			{"id":5,"callFrame":{"functionName":"f4","url":"file:///repo/app.js","lineNumber":3},"hitCount":5,"children":[],"positionTicks":[{"line":4,"ticks":5}]},
			{"id":6,"callFrame":{"functionName":"f5","url":"file:///repo/app.js","lineNumber":4},"hitCount":5,"children":[],"positionTicks":[{"line":5,"ticks":5}]},
			{"id":7,"callFrame":{"functionName":"f6","url":"file:///repo/app.js","lineNumber":5},"hitCount":5,"children":[],"positionTicks":[{"line":6,"ticks":5}]},
			{"id":8,"callFrame":{"functionName":"f7","url":"file:///repo/app.js","lineNumber":6},"hitCount":5,"children":[],"positionTicks":[{"line":7,"ticks":5}]},
			{"id":9,"callFrame":{"functionName":"f8","url":"file:///repo/app.js","lineNumber":7},"hitCount":5,"children":[],"positionTicks":[{"line":8,"ticks":5}]},
			{"id":10,"callFrame":{"functionName":"f9","url":"file:///repo/app.js","lineNumber":8},"hitCount":5,"children":[],"positionTicks":[{"line":9,"ticks":5}]},
			{"id":11,"callFrame":{"functionName":"f10","url":"file:///repo/app.js","lineNumber":9},"hitCount":5,"children":[],"positionTicks":[{"line":10,"ticks":5}]},
			{"id":12,"callFrame":{"functionName":"f11","url":"file:///repo/app.js","lineNumber":10},"hitCount":5,"children":[],"positionTicks":[{"line":11,"ticks":5}]},
			{"id":13,"callFrame":{"functionName":"f12","url":"file:///repo/app.js","lineNumber":11},"hitCount":5,"children":[],"positionTicks":[{"line":12,"ticks":5}]},
			{"id":14,"callFrame":{"functionName":"f13","url":"file:///repo/app.js","lineNumber":12},"hitCount":5,"children":[],"positionTicks":[{"line":13,"ticks":5}]},
			{"id":15,"callFrame":{"functionName":"f14","url":"file:///repo/app.js","lineNumber":13},"hitCount":5,"children":[],"positionTicks":[{"line":14,"ticks":5}]},
			{"id":16,"callFrame":{"functionName":"f15","url":"file:///repo/app.js","lineNumber":14},"hitCount":5,"children":[],"positionTicks":[{"line":15,"ticks":5}]},
			{"id":17,"callFrame":{"functionName":"f16","url":"file:///repo/app.js","lineNumber":15},"hitCount":5,"children":[],"positionTicks":[{"line":16,"ticks":5}]},
			{"id":18,"callFrame":{"functionName":"f17","url":"file:///repo/app.js","lineNumber":16},"hitCount":5,"children":[],"positionTicks":[{"line":17,"ticks":5}]},
			{"id":19,"callFrame":{"functionName":"f18","url":"file:///repo/app.js","lineNumber":17},"hitCount":5,"children":[],"positionTicks":[{"line":18,"ticks":5}]},
			{"id":20,"callFrame":{"functionName":"f19","url":"file:///repo/app.js","lineNumber":18},"hitCount":5,"children":[],"positionTicks":[{"line":19,"ticks":5}]},
			{"id":21,"callFrame":{"functionName":"f20","url":"file:///repo/app.js","lineNumber":19},"hitCount":5,"children":[],"positionTicks":[{"line":20,"ticks":5}]},
			{"id":22,"callFrame":{"functionName":"f21","url":"file:///repo/app.js","lineNumber":20},"hitCount":5,"children":[],"positionTicks":[{"line":21,"ticks":5}]}
		],
		"samples": [2],
		"startTime": 0, "endTime": 1000
	}`
	var cp cdpProfile
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		t.Fatal(err)
	}
	hm, err := BuildHeatmap(context.Background(), &Source{Kind: SourceCDP, CDP: &cp}, HeatOptions{NoCodemap: true, NoSourcemaps: true, NoReadCode: true, Top: 100})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	if hm.IdlePct != 0 {
		t.Fatalf("IdlePct = %.1f, want 0 (no idle/gc frames in this fixture)", hm.IdlePct)
	}
	if len(hm.Functions) == 0 || hm.Functions[0].SelfPct >= diffuseWarningThresholdPct {
		t.Fatalf("top function self_pct = %.2f, want <%.1f for this 21-way-even split", hm.Functions[0].SelfPct, diffuseWarningThresholdPct)
	}
	found := false
	for _, w := range hm.Warnings {
		if strings.Contains(w, "diffuse") {
			found = true
		}
	}
	if !found {
		t.Errorf("Warnings = %v, want one containing %q", hm.Warnings, "diffuse")
	}
}

// TestBuildHeatmapCDPWrapperGetsCalleesNotLines reproduces the "ancestor
// with no self ticks" case the roadmap calls out explicitly: a wrapper
// function (processBatch) that only ever calls a hot callee
// (heavyStringify) must report near-zero self, a Cum inherited entirely
// from its callee, an empty (or absent) Lines list — its call-site line
// isn't in V8's positionTicks data — and that callee listed under Callees.
func TestBuildHeatmapCDPWrapperGetsCalleesNotLines(t *testing.T) {
	raw := `{
		"nodes": [
			{"id":1,"callFrame":{"functionName":"(root)","url":"","lineNumber":-1},"hitCount":0,"children":[2]},
			{"id":2,"callFrame":{"functionName":"processBatch","url":"file:///repo/app.js","lineNumber":23},"hitCount":0,"children":[3]},
			{"id":3,"callFrame":{"functionName":"heavyStringify","url":"file:///repo/app.js","lineNumber":10},"hitCount":100,"children":[],"positionTicks":[{"line":17,"ticks":100}]}
		],
		"samples": [3],
		"startTime": 0, "endTime": 1000
	}`
	var cp cdpProfile
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		t.Fatal(err)
	}
	hm, err := BuildHeatmap(context.Background(), &Source{Kind: SourceCDP, CDP: &cp}, HeatOptions{NoCodemap: true, NoSourcemaps: true, NoReadCode: true})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	wrapper := findHeatFunc(t, hm, "processBatch")
	if wrapper.SelfPct != 0 {
		t.Errorf("processBatch SelfPct = %.1f, want 0 (never the leaf)", wrapper.SelfPct)
	}
	if wrapper.CumPct != 100 {
		t.Errorf("processBatch CumPct = %.1f, want 100 (every sample passes through it)", wrapper.CumPct)
	}
	if len(wrapper.Lines) != 0 {
		t.Errorf("processBatch Lines = %+v, want none (no self ticks landed on any of its own lines)", wrapper.Lines)
	}
	if len(wrapper.Callees) != 1 || wrapper.Callees[0].Func != "heavyStringify" || wrapper.Callees[0].Cum != 100 {
		t.Errorf("processBatch Callees = %+v, want [{heavyStringify 100}]", wrapper.Callees)
	}

	callee := findHeatFunc(t, hm, "heavyStringify")
	if callee.SelfPct != 100 || callee.CumPct != 100 {
		t.Errorf("heavyStringify = %+v, want self=cum=100 (sole leaf)", callee)
	}
}

// TestBuildHeatmapCDPRecursionDoesNotDoubleCountCum builds a genuinely
// recursive call tree (f calls f calls f) and asserts the function's total
// Cum still equals its outermost occurrence's own subtree weight — not that
// weight counted again for every nested recursive occurrence, which would
// silently inflate Cum past 100% of active samples.
func TestBuildHeatmapCDPRecursionDoesNotDoubleCountCum(t *testing.T) {
	raw := `{
		"nodes": [
			{"id":1,"callFrame":{"functionName":"(root)","url":"","lineNumber":-1},"hitCount":0,"children":[2]},
			{"id":2,"callFrame":{"functionName":"recurse","url":"file:///repo/app.js","lineNumber":0},"hitCount":10,"children":[3],"positionTicks":[{"line":2,"ticks":10}]},
			{"id":3,"callFrame":{"functionName":"recurse","url":"file:///repo/app.js","lineNumber":0},"hitCount":20,"children":[4],"positionTicks":[{"line":2,"ticks":20}]},
			{"id":4,"callFrame":{"functionName":"recurse","url":"file:///repo/app.js","lineNumber":0},"hitCount":30,"children":[],"positionTicks":[{"line":2,"ticks":30}]}
		],
		"samples": [4],
		"startTime": 0, "endTime": 1000
	}`
	var cp cdpProfile
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		t.Fatal(err)
	}
	hm, err := BuildHeatmap(context.Background(), &Source{Kind: SourceCDP, CDP: &cp}, HeatOptions{NoCodemap: true, NoSourcemaps: true, NoReadCode: true})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	f := findHeatFunc(t, hm, "recurse")
	// Self: 10+20+30 = 60, all attributed to line 2, active=60.
	if f.SelfPct != 100 {
		t.Errorf("SelfPct = %.1f, want 100 (only function in the profile)", f.SelfPct)
	}
	// Cum: the outermost node's own subtree sum is 10+20+30=60, i.e. 100%
	// of active samples — NOT 60+50+30=140% from summing every recursive
	// occurrence's own subtree independently.
	if f.CumPct != 100 {
		t.Errorf("CumPct = %.1f, want 100 (recursion must not double-count)", f.CumPct)
	}
}

// TestBuildHeatmapCDPCycleIsRejected guards the subtree-sum DFS's cycle
// detector: a malformed profile whose children graph cycles back on itself
// must fail loudly instead of hanging or silently under-reporting.
func TestBuildHeatmapCDPCycleIsRejected(t *testing.T) {
	raw := `{
		"nodes": [
			{"id":1,"callFrame":{"functionName":"a","url":"file:///repo/app.js","lineNumber":0},"hitCount":1,"children":[2],"positionTicks":[{"line":1,"ticks":1}]},
			{"id":2,"callFrame":{"functionName":"b","url":"file:///repo/app.js","lineNumber":1},"hitCount":1,"children":[1],"positionTicks":[{"line":2,"ticks":1}]}
		],
		"samples": [1],
		"startTime": 0, "endTime": 1000
	}`
	var cp cdpProfile
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		t.Fatal(err)
	}
	if _, err := BuildHeatmap(context.Background(), &Source{Kind: SourceCDP, CDP: &cp}, HeatOptions{NoCodemap: true, NoSourcemaps: true}); err == nil {
		t.Fatal("expected an error for a cyclic call tree")
	}
}

// TestBuildHeatmapCDPMethodDegradesWithoutPositionTicks asserts the honest
// method label: a .cpuprofile with hitCount but no positionTicks anywhere
// is reported as MethodCPUProfileFile, not silently upgraded to
// v8_position_ticks.
func TestBuildHeatmapCDPMethodDegradesWithoutPositionTicks(t *testing.T) {
	raw := `{
		"nodes": [
			{"id":1,"callFrame":{"functionName":"(root)","url":"","lineNumber":-1},"hitCount":0,"children":[2]},
			{"id":2,"callFrame":{"functionName":"legacyFn","url":"file:///repo/app.js","lineNumber":9},"hitCount":42,"children":[]}
		],
		"samples": [2],
		"startTime": 0, "endTime": 1000
	}`
	var cp cdpProfile
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		t.Fatal(err)
	}
	hm, err := BuildHeatmap(context.Background(), &Source{Kind: SourceCDP, CDP: &cp}, HeatOptions{NoCodemap: true, NoSourcemaps: true, NoReadCode: true})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	if hm.Method != MethodCPUProfileFile {
		t.Errorf("Method = %q, want %q", hm.Method, MethodCPUProfileFile)
	}
	f := findHeatFunc(t, hm, "legacyFn")
	if len(f.Lines) != 1 || f.Lines[0].Line != 10 { // lineNumber 9 (0-based) + 1
		t.Errorf("Lines = %+v, want a single fallback entry at line 10", f.Lines)
	}
}

// TestBuildHeatmapPprofNamesJSONMarshalLine is AC-1's Go case, built from a
// synthetic profile.Profile the way pprof_test.go's own tests do (no `go`
// toolchain, no real process): a wrapper (processBatch) that calls a hot
// function (heavyStringify) whose own hot line is a json.Marshal call.
func TestBuildHeatmapPprofNamesJSONMarshalLine(t *testing.T) {
	wrapperFn := &gpprof.Function{ID: 1, Name: "main.processBatch", Filename: "main.go"}
	heavyFn := &gpprof.Function{ID: 2, Name: "main.heavyStringify", Filename: "main.go"}

	wrapperLoc := &gpprof.Location{ID: 1, Line: []gpprof.Line{{Function: wrapperFn, Line: 27}}}
	loopLoc := &gpprof.Location{ID: 2, Line: []gpprof.Line{{Function: heavyFn, Line: 18}}}        // for i := ...
	marshalCallLoc := &gpprof.Location{ID: 3, Line: []gpprof.Line{{Function: heavyFn, Line: 19}}} // json.Marshal(...)

	prof := &gpprof.Profile{
		SampleType: []*gpprof.ValueType{{Type: "cpu", Unit: "nanoseconds"}},
		Location:   []*gpprof.Location{wrapperLoc, loopLoc, marshalCallLoc},
		Function:   []*gpprof.Function{wrapperFn, heavyFn},
		Sample: []*gpprof.Sample{
			// Leaf-first (pprof convention): marshalCallLoc is what's
			// actually executing; loopLoc and wrapperLoc are still on the
			// stack. wrapperLoc is never itself Location[0] in any sample —
			// processBatch is a pure wrapper, never the leaf.
			{Value: []int64{74}, Location: []*gpprof.Location{marshalCallLoc, loopLoc, wrapperLoc}},
			{Value: []int64{1}, Location: []*gpprof.Location{loopLoc, wrapperLoc}},
		},
	}

	src := &Source{Kind: SourcePprof, Pprof: prof}
	hm, err := BuildHeatmap(context.Background(), src, HeatOptions{NoCodemap: true, NoReadCode: true, ProfileType: HeatCPU})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	if hm.Method != MethodPprofProto {
		t.Errorf("Method = %q, want %q", hm.Method, MethodPprofProto)
	}
	if hm.Unit != "nanoseconds" {
		t.Errorf("Unit = %q, want nanoseconds (from SampleType)", hm.Unit)
	}

	heavy := findHeatFunc(t, hm, "main.heavyStringify")
	hot := hottestLine(t, heavy)
	if hot.Line != 19 {
		t.Errorf("hot line = %d, want 19 (json.Marshal call)", hot.Line)
	}
	if hot.Cum != 74 { // only sample 1's stack ever touches line 19
		t.Errorf("line 19 Cum = %d, want 74", hot.Cum)
	}
	if hot.Self != 74 {
		t.Errorf("line 19 Self = %d, want 74 (only ever the leaf in sample 1)", hot.Self)
	}

	wrapper := findHeatFunc(t, hm, "main.processBatch")
	// processBatch's own line 27 (the call site) never appears as a leaf
	// anywhere: its flat/self is 0, but cum is 100% (every sample's stack
	// passes through it).
	if len(wrapper.Lines) != 1 || wrapper.Lines[0].Self != 0 {
		t.Errorf("processBatch line 27 = %+v, want Self=0", wrapper.Lines)
	}
	if wrapper.CumPct != 100 {
		t.Errorf("processBatch CumPct = %.1f, want 100", wrapper.CumPct)
	}
}

// TestBuildHeatmapPprofHeapUsesInuseSpace asserts HeatHeapInuse threads
// through to the inuse_space column (bytes), not alloc_space.
func TestBuildHeatmapPprofHeapUsesInuseSpace(t *testing.T) {
	fn := &gpprof.Function{ID: 1, Name: "main.buildIndex", Filename: "main.go"}
	loc := &gpprof.Location{ID: 1, Line: []gpprof.Line{{Function: fn, Line: 47}}}
	prof := &gpprof.Profile{
		SampleType: []*gpprof.ValueType{
			{Type: "alloc_objects", Unit: "count"},
			{Type: "alloc_space", Unit: "bytes"},
			{Type: "inuse_objects", Unit: "count"},
			{Type: "inuse_space", Unit: "bytes"},
		},
		Location: []*gpprof.Location{loc},
		Function: []*gpprof.Function{fn},
		Sample: []*gpprof.Sample{
			{Value: []int64{100, 900000000, 5, 61200000}, Location: []*gpprof.Location{loc}},
		},
	}
	hm, err := BuildHeatmap(context.Background(), &Source{Kind: SourcePprof, Pprof: prof}, HeatOptions{NoCodemap: true, NoReadCode: true, ProfileType: HeatHeapInuse})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	if hm.Unit != "bytes" {
		t.Errorf("Unit = %q, want bytes", hm.Unit)
	}
	f := findHeatFunc(t, hm, "main.buildIndex")
	if f.Lines[0].Self != 61200000 {
		t.Errorf("Self = %d, want the inuse_space value (61200000), not alloc_space", f.Lines[0].Self)
	}
}

func TestBuildHeatmapNilSourceErrors(t *testing.T) {
	if _, err := BuildHeatmap(context.Background(), nil, HeatOptions{}); err == nil {
		t.Fatal("expected an error for a nil source")
	}
}

func TestBuildHeatmapEmptyPprofErrors(t *testing.T) {
	prof := &gpprof.Profile{SampleType: []*gpprof.ValueType{{Type: "cpu", Unit: "nanoseconds"}}}
	if _, err := BuildHeatmap(context.Background(), &Source{Kind: SourcePprof, Pprof: prof}, HeatOptions{}); err == nil {
		t.Fatal("expected an error for a pprof profile with zero samples")
	}
}

// TestBuildHeatmapFuncFilterAndTop exercises HeatOptions.Func and .Top.
func TestBuildHeatmapFuncFilterAndTop(t *testing.T) {
	src, err := LoadFile("testdata/v8-hot.cpuprofile")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	hm, err := BuildHeatmap(context.Background(), src, HeatOptions{NoCodemap: true, NoSourcemaps: true, NoReadCode: true, Func: "heavyStringify"})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	if len(hm.Functions) != 1 || hm.Functions[0].Name != "heavyStringify" {
		t.Fatalf("Functions = %+v, want exactly [heavyStringify]", hm.Functions)
	}

	hm2, err := BuildHeatmap(context.Background(), src, HeatOptions{NoCodemap: true, NoSourcemaps: true, NoReadCode: true, Top: 1})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	if len(hm2.Functions) != 1 {
		t.Fatalf("Functions = %d, want capped at Top=1", len(hm2.Functions))
	}
}

// TestBuildHeatmapObservedRangeFallback asserts that with codemap disabled
// (or unavailable), a function's range degrades honestly to its observed
// min/max touched line, marked "observed" rather than "codemap".
func TestBuildHeatmapObservedRangeFallback(t *testing.T) {
	src, err := LoadFile("testdata/v8-hot.cpuprofile")
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	hm, err := BuildHeatmap(context.Background(), src, HeatOptions{NoCodemap: true, NoSourcemaps: true, NoReadCode: true})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	f := findHeatFunc(t, hm, "heavyStringify")
	if f.RangeSource != "observed" {
		t.Errorf("RangeSource = %q, want observed (codemap disabled)", f.RangeSource)
	}
	if f.StartLine != 3 || f.EndLine != 6 {
		t.Errorf("range = %d-%d, want 3-6 (the observed min/max touched line)", f.StartLine, f.EndLine)
	}
}

// TestBuildHeatmapSourceMapResolvesToOriginalTSLine is E3.1's E3.3a wiring
// case: a real bun-built .cpuprofile whose hot generated line resolves,
// through a real source map, to its original .ts line with mapping exact.
// See testdata/tssrc/README.md for how the fixture was produced.
func TestBuildHeatmapSourceMapResolvesToOriginalTSLine(t *testing.T) {
	dir := t.TempDir()
	copyTestdataFile(t, "testdata/tssrc/hot.ts", filepath.Join(dir, "hot.ts"))
	jsPath := filepath.Join(dir, "dist", "hot.js")
	mapPath := filepath.Join(dir, "dist", "hot.js.map")
	copyTestdataFile(t, "testdata/tssrc/dist/hot.js", jsPath)
	copyTestdataFile(t, "testdata/tssrc/dist/hot.js.map", mapPath)

	// Real build tools write the .map a hair before the generated file;
	// pin both to "now" so staleTolerance never flags this fresh copy as
	// stale regardless of checkout write order (see
	// internal/sourcemap/fixture_test.go, which does the same).
	now := time.Now()
	if err := os.Chtimes(mapPath, now, now); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(jsPath, now.Add(time.Millisecond), now.Add(time.Millisecond)); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile("testdata/tssrc/dist/hot.cpuprofile")
	if err != nil {
		t.Fatalf("read fixture cpuprofile: %v", err)
	}
	rewritten := strings.ReplaceAll(string(raw),
		"file:///repo/internal/profiler/testdata/tssrc/dist/hot.js",
		"file://"+jsPath,
	)
	profPath := filepath.Join(dir, "dist", "hot.cpuprofile")
	if err := os.WriteFile(profPath, []byte(rewritten), 0o600); err != nil {
		t.Fatalf("write rewritten cpuprofile: %v", err)
	}

	src, err := LoadFile(profPath)
	if err != nil {
		t.Fatalf("LoadFile: %v", err)
	}
	hm, err := BuildHeatmap(context.Background(), src, HeatOptions{NoCodemap: true, NoReadCode: true})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	f := findHeatFunc(t, hm, "heavyWork")
	if !strings.HasSuffix(f.File, "hot.ts") {
		t.Fatalf("File = %q, want it resolved to hot.ts", f.File)
	}
	hot := hottestLine(t, f)
	if hot.Line != 4 {
		t.Errorf("resolved hot line = %d, want 4 (the // HOT LINE comment in hot.ts)", hot.Line)
	}
	if hot.Mapping != "exact" {
		t.Errorf("Mapping = %q, want exact", hot.Mapping)
	}
	if hot.Stale {
		t.Error("Stale = true for a freshly-copied, same-timestamp pair")
	}
}

// TestBuildHeatmapReadsCodeFromDisk asserts HeatLine.Code is filled from a
// real, resolvable file — separate from the fixture-based hot-line tests,
// whose committed .cpuprofile carries the honest, deliberately-unresolvable
// "/repo/..." placeholder path convention (testdata/README.md).
func TestBuildHeatmapReadsCodeFromDisk(t *testing.T) {
	dir := t.TempDir()
	jsPath := filepath.Join(dir, "app.js")
	src := "line one\nline two\nthe hot line\nline four\n"
	if err := os.WriteFile(jsPath, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	raw := `{
		"nodes": [
			{"id":1,"callFrame":{"functionName":"(root)","url":"","lineNumber":-1},"hitCount":0,"children":[2]},
			{"id":2,"callFrame":{"functionName":"work","url":"file://` + jsPath + `","lineNumber":0},"hitCount":10,"children":[],"positionTicks":[{"line":3,"ticks":10}]}
		],
		"samples": [2],
		"startTime": 0, "endTime": 1000
	}`
	var cp cdpProfile
	if err := json.Unmarshal([]byte(raw), &cp); err != nil {
		t.Fatal(err)
	}
	hm, err := BuildHeatmap(context.Background(), &Source{Kind: SourceCDP, CDP: &cp}, HeatOptions{NoCodemap: true, NoSourcemaps: true})
	if err != nil {
		t.Fatalf("BuildHeatmap: %v", err)
	}
	f := findHeatFunc(t, hm, "work")
	hot := hottestLine(t, f)
	if hot.Code != "the hot line" {
		t.Errorf("Code = %q, want %q", hot.Code, "the hot line")
	}
}

func copyTestdataFile(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		t.Fatalf("mkdir for %s: %v", dst, err)
	}
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		t.Fatalf("write %s: %v", dst, err)
	}
}
