package profiler

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime/pprof"
	"strings"
	"testing"
	"time"

	gpprof "github.com/google/pprof/profile"
)

// TestSymbolsFromPprofWrapperHasZeroFlatButFullCum: a wrapper that only ever
// calls into one hot callee should report Weight (flat) ~0 — it's never the
// leaf, so it's never actually executing when a sample lands — but Cum
// (cumulative) at 100%, since every sample's stack passes through it. This
// is exactly the "hot *line* inside a caller" signal `go tool pprof -list`
// gives that a flat-only view (the old goToolPprofTop/parsePprofTop path)
// could never surface for a pure pass-through function.
func TestSymbolsFromPprofWrapperHasZeroFlatButFullCum(t *testing.T) {
	wrapper := &gpprof.Function{ID: 1, Name: "main.wrapper", Filename: "main.go"}
	callee := &gpprof.Function{ID: 2, Name: "main.callee", Filename: "callee.go"}
	locCallee := &gpprof.Location{ID: 1, Line: []gpprof.Line{{Function: callee, Line: 20}}}
	locWrapper := &gpprof.Location{ID: 2, Line: []gpprof.Line{{Function: wrapper, Line: 10}}}
	prof := &gpprof.Profile{
		SampleType: []*gpprof.ValueType{{Type: "samples", Unit: "count"}},
		Sample: []*gpprof.Sample{
			// Leaf-first: index 0 is what was actually executing (callee);
			// index 1 is the caller whose stack frame is still on the stack
			// (wrapper), matching the pprof proto convention documented at
			// "the leaf is at location_id[0]".
			{Value: []int64{10}, Location: []*gpprof.Location{locCallee, locWrapper}},
		},
	}

	syms := symbolsFromPprof(prof, 0)
	wrapperSym := findSymbol(t, syms, "main.wrapper")
	if wrapperSym.Weight != 0 {
		t.Errorf("wrapper flat = %v, want 0 (never the leaf)", wrapperSym.Weight)
	}
	if wrapperSym.Cum != 100 {
		t.Errorf("wrapper cum = %v, want 100 (every sample's stack passes through it)", wrapperSym.Cum)
	}
	calleeSym := findSymbol(t, syms, "main.callee")
	if calleeSym.Weight != 100 || calleeSym.Cum != 100 {
		t.Errorf("callee = %+v, want flat=100 cum=100 (sole leaf)", calleeSym)
	}
}

// TestSymbolsFromPprofKeepsSiblingFunctionsInSameFile: the old text parser
// deduplicated by function name only after a naming bug (any function
// ending in "s" became "unknown") collided distinct functions together.
// Aggregating on the resolved (func,file,line) key must keep two distinct
// functions that happen to live in the same file as two separate entries.
func TestSymbolsFromPprofKeepsSiblingFunctionsInSameFile(t *testing.T) {
	funcA := &gpprof.Function{ID: 1, Name: "main.first", Filename: "shared.go"}
	funcB := &gpprof.Function{ID: 2, Name: "main.second", Filename: "shared.go"}
	locA := &gpprof.Location{ID: 1, Line: []gpprof.Line{{Function: funcA, Line: 5}}}
	locB := &gpprof.Location{ID: 2, Line: []gpprof.Line{{Function: funcB, Line: 15}}}
	prof := &gpprof.Profile{
		SampleType: []*gpprof.ValueType{{Type: "samples", Unit: "count"}},
		Sample: []*gpprof.Sample{
			{Value: []int64{3}, Location: []*gpprof.Location{locA}},
			{Value: []int64{7}, Location: []*gpprof.Location{locB}},
		},
	}

	syms := symbolsFromPprof(prof, 0)
	first := findSymbol(t, syms, "main.first")
	second := findSymbol(t, syms, "main.second")
	if first.File != "shared.go" || first.Line != 5 {
		t.Errorf("main.first = %+v, want shared.go:5", first)
	}
	if second.File != "shared.go" || second.Line != 15 {
		t.Errorf("main.second = %+v, want shared.go:15", second)
	}
	if first.Weight == second.Weight {
		t.Errorf("expected distinct weights (3 vs 7 samples), got both = %v", first.Weight)
	}
}

// TestSymbolsFromPprofHandlesInlinedLocation: one Location with 2 Lines (the
// inlined callee first, its caller second — the pprof convention for
// inlining) must produce two symbols: the innermost line gets both flat and
// cum, the outer/inlined-into line gets cum only (it was never itself the
// executing PC).
func TestSymbolsFromPprofHandlesInlinedLocation(t *testing.T) {
	inlined := &gpprof.Function{ID: 1, Name: "main.inlinedHelper", Filename: "helper.go"}
	outer := &gpprof.Function{ID: 2, Name: "main.outer", Filename: "main.go"}
	loc := &gpprof.Location{ID: 1, Line: []gpprof.Line{
		{Function: inlined, Line: 42}, // innermost: Line[0]
		{Function: outer, Line: 100},  // the function it was inlined into
	}}
	prof := &gpprof.Profile{
		SampleType: []*gpprof.ValueType{{Type: "samples", Unit: "count"}},
		Sample: []*gpprof.Sample{
			{Value: []int64{20}, Location: []*gpprof.Location{loc}},
		},
	}

	syms := symbolsFromPprof(prof, 0)
	if len(syms) != 2 {
		t.Fatalf("got %d symbols from one 2-line inlined location, want 2: %+v", len(syms), syms)
	}
	helper := findSymbol(t, syms, "main.inlinedHelper")
	if helper.Weight != 100 || helper.Cum != 100 || helper.File != "helper.go" || helper.Line != 42 {
		t.Errorf("main.inlinedHelper = %+v, want flat=100 cum=100 helper.go:42", helper)
	}
	outerSym := findSymbol(t, syms, "main.outer")
	if outerSym.Weight != 0 || outerSym.Cum != 100 || outerSym.File != "main.go" || outerSym.Line != 100 {
		t.Errorf("main.outer = %+v, want flat=0 cum=100 main.go:100", outerSym)
	}
}

// TestSymbolsFromPprofNeverMislabelsFunctionsEndingInS is the direct
// regression for the removed profiler.go:298 bug
// (`strings.HasSuffix(fn, "s")` renamed any function whose text-parsed name
// ended in "s" to "unknown", which then collided with and dropped its
// same-file siblings). Reading the name from profile.Function.Name (a
// structured field, not text-scraped) can't hit that heuristic at all.
func TestSymbolsFromPprofNeverMislabelsFunctionsEndingInS(t *testing.T) {
	fn := &gpprof.Function{ID: 1, Name: "main.processRequests", Filename: "main.go"}
	loc := &gpprof.Location{ID: 1, Line: []gpprof.Line{{Function: fn, Line: 7}}}
	prof := &gpprof.Profile{
		SampleType: []*gpprof.ValueType{{Type: "samples", Unit: "count"}},
		Sample:     []*gpprof.Sample{{Value: []int64{3}, Location: []*gpprof.Location{loc}}},
	}

	syms := symbolsFromPprof(prof, 0)
	if len(syms) != 1 || syms[0].Func != "main.processRequests" {
		t.Fatalf("syms = %+v, want exactly main.processRequests", syms)
	}
	for _, s := range syms {
		if s.Func == "unknown" || s.Func == "(unknown)" {
			t.Errorf("function ending in 's' was mislabeled %s: %+v", s.Func, syms)
		}
	}
}

// TestSymbolsFromPprofRecursionGuardCountsOnceePerSample: a recursive
// function that appears more than once in a single sample's stack must have
// its cum credited exactly once for that sample, not once per occurrence —
// otherwise a function recursing N deep would inflate its own cum by a
// factor of N instead of reporting "this sample's stack passed through me",
// the actual definition of cum. The pprof.go doc comment describes this
// guard (seenInSample); this exercises it directly with a 4-deep stack
// (leaf, 3x recursive) whose sole sample has value 100.
func TestSymbolsFromPprofRecursionGuardCountsOncePerSample(t *testing.T) {
	rec := &gpprof.Function{ID: 1, Name: "main.recurse", Filename: "main.go"}
	locLeaf := &gpprof.Location{ID: 1, Line: []gpprof.Line{{Function: rec, Line: 10}}}
	locRecA := &gpprof.Location{ID: 2, Line: []gpprof.Line{{Function: rec, Line: 10}}}
	locRecB := &gpprof.Location{ID: 3, Line: []gpprof.Line{{Function: rec, Line: 10}}}
	locRecC := &gpprof.Location{ID: 4, Line: []gpprof.Line{{Function: rec, Line: 10}}}
	prof := &gpprof.Profile{
		SampleType: []*gpprof.ValueType{{Type: "samples", Unit: "count"}},
		Sample: []*gpprof.Sample{
			// Leaf-first (index 0 is what was executing): the same
			// (func,file,line) appears at all 4 stack depths.
			{Value: []int64{100}, Location: []*gpprof.Location{locLeaf, locRecA, locRecB, locRecC}},
		},
	}

	syms := symbolsFromPprof(prof, 0)
	if len(syms) != 1 {
		t.Fatalf("got %d symbols, want 1 (every location resolves to the same func/file/line): %+v", len(syms), syms)
	}
	rs := syms[0]
	if rs.Weight != 100 {
		t.Errorf("main.recurse flat = %v, want 100 (it IS the leaf)", rs.Weight)
	}
	if rs.Cum != 100 {
		t.Errorf("main.recurse cum = %v, want 100 (recursion guard: counted once per sample, not once per stack depth — a naive sum would give 400)", rs.Cum)
	}
}

func findSymbol(t *testing.T, syms []Symbol, fn string) Symbol {
	t.Helper()
	for _, s := range syms {
		if s.Func == fn {
			return s
		}
	}
	t.Fatalf("symbol %q not found in %+v", fn, syms)
	return Symbol{}
}

func TestPprofValueIndexPrefersNamedColumnOverPosition(t *testing.T) {
	prof := &gpprof.Profile{SampleType: []*gpprof.ValueType{
		{Type: "alloc_objects"}, {Type: "alloc_space"}, {Type: "inuse_objects"}, {Type: "inuse_space"},
	}}
	if got := pprofValueIndex(prof, "inuse_space", "alloc_space"); got != 3 {
		t.Errorf("index = %d, want 3 (inuse_space)", got)
	}
	if got := pprofValueIndex(prof, "nonexistent"); got != 3 {
		t.Errorf("fallback index = %d, want 3 (last column)", got)
	}
	if got := pprofValueIndex(&gpprof.Profile{}, "anything"); got != 0 {
		t.Errorf("empty SampleType index = %d, want 0", got)
	}
}

func TestSummarizeSymbolsRendersTopN(t *testing.T) {
	syms := []Symbol{
		{Func: "main.hot", File: "main.go", Line: 19, Weight: 1.2, Cum: 87.5},
		{Func: "encoding/json.Marshal", File: "json.go", Line: 3, Weight: 60, Cum: 80},
	}
	out := summarizeSymbols(syms, 1)
	if !strings.Contains(out, "main.hot") {
		t.Errorf("summary missing top symbol: %q", out)
	}
	if strings.Contains(out, "encoding/json.Marshal") {
		t.Errorf("summary should be capped to 1 row: %q", out)
	}
	if summarizeSymbols(nil, 10) != "" {
		t.Errorf("empty input should render empty summary")
	}
}

// busyWorkForCPUProfile burns real CPU so a runtime/pprof.StartCPUProfile
// capture over it has actual samples to report.
func busyWorkForCPUProfile() int {
	sum := 0
	for i := 0; i < 30_000_000; i++ {
		sum += i % 7
	}
	return sum
}

// TestCaptureCPUParsesRealPprofProtoWithoutGoToolchain captures a genuine
// runtime/pprof CPU profile (not a hand-built synthetic one) and drives it
// through the full Capture() path — httpGet, writeTempProfile,
// profile.Parse, symbolsFromPprof — the same way scraping a real target's
// net/http/pprof endpoint would, without ever invoking `go tool pprof`.
func TestCaptureCPUParsesRealPprofProtoWithoutGoToolchain(t *testing.T) {
	var bb bytes.Buffer
	if err := pprof.StartCPUProfile(&bb); err != nil {
		t.Skipf("cannot start CPU profile in this environment: %v", err)
	}
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		busyWorkForCPUProfile()
	}
	pprof.StopCPUProfile()
	raw := bb.Bytes()
	if len(raw) == 0 {
		t.Skip("runtime/pprof produced no samples in this environment")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(raw)
	}))
	defer srv.Close()
	addr := strings.TrimPrefix(srv.URL, "http://")

	p, err := Capture(context.Background(), 999, ProfileCPU, addr)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	if p.Path == "" {
		t.Fatal("expected the raw proto to be saved to Path")
	}
	defer os.Remove(p.Path)
	if info, statErr := os.Stat(p.Path); statErr != nil || info.Size() == 0 {
		t.Fatalf("saved profile missing/empty: stat=%v err=%v", info, statErr)
	}
	if len(p.Symbols) == 0 {
		t.Fatal("expected symbols parsed from a real CPU profile without the go toolchain")
	}
	found := false
	for _, s := range p.Symbols {
		if strings.Contains(s.Func, "busyWorkForCPUProfile") {
			found = true
		}
		if s.Func == "unknown" || s.Func == "(unknown)" {
			t.Errorf("real profile produced an %s symbol: %+v", s.Func, s)
		}
	}
	if !found {
		t.Errorf("busyWorkForCPUProfile not found among symbols: %+v", p.Symbols)
	}
}
