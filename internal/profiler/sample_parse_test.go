package profiler

// No build tag: parseSampleTree is exercised on every OS CI runs, not just
// darwin (only the `sample` exec itself is darwin-only; see sample_darwin.go
// / sample_other.go).

import (
	"os"
	"strings"
	"testing"
)

// callGraphOnlyFixture reads darwin-sample-go.txt and cuts it right before
// the trailing "Total number in stack" section, so tests that assert the
// parser's core correctness run against ONLY the genuine call-graph tree —
// never relying on parseSampleTree's own trailing-section boundary to
// accidentally produce the right answer for the wrong reason. See
// TestParseSampleTreeIgnoresTrailingSections for the complementary
// regression proving the two texts parse identically anyway.
func callGraphOnlyFixture(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("testdata/darwin-sample-go.txt")
	if err != nil {
		t.Fatal(err)
	}
	idx := strings.Index(string(raw), "\nTotal number in stack")
	if idx <= 0 {
		t.Fatal("fixture missing the trailing 'Total number in stack' section this test guards against")
	}
	return string(raw)[:idx]
}

// TestParseSampleTreeRealOutputFindsGoFrames replaces the old regex parser's
// test (which fed it output the real tool never produces — a digit at the
// very start of the line). darwin-sample-go.txt is a genuine `sample <pid> 1`
// capture of a Go binary with a planted json.Marshal-heavy hot path
// (main.heavyStringify) whose own cost is almost entirely inside its
// encoding/json.Marshal callee — self time alone (self = 89/777 active
// samples, ~11.5%) ranks it outside a self-only top-50, so it must surface
// via Cum instead, honestly, the same way a pprof wrapper with near-zero
// flat but high cum does. This runs against the call-graph section only
// (see callGraphOnlyFixture); TestParseSampleTreeIgnoresTrailingSections
// separately proves the trailing sections don't change the outcome.
func TestParseSampleTreeRealOutputFindsGoFrames(t *testing.T) {
	text := callGraphOnlyFixture(t)
	syms, stats := parseSampleTree(text)
	if len(syms) == 0 {
		t.Fatal("parseSampleTree found 0 symbols in a real sample capture")
	}
	var hot *Symbol
	for i := range syms {
		if syms[i].Func == "main.heavyStringify" {
			hot = &syms[i]
			break
		}
	}
	if hot == nil {
		t.Fatalf("main.heavyStringify missing from top symbols: %+v", syms)
	}
	if hot.File != "gowork" {
		t.Errorf("main.heavyStringify image = %q, want gowork (the containing binary)", hot.File)
	}
	if hot.Cum <= 0 {
		t.Errorf("main.heavyStringify cum = %v, want > 0 (its cost is almost entirely inside json.Marshal, so self alone would never rank it)", hot.Cum)
	}
	// No `sample` thread-descriptor row ("Thread_NNNN ...") must ever
	// appear as a Symbol: it is sample's own call-tree bookkeeping, not
	// code, and (unlike a real wrapper) its cum would otherwise dominate
	// the ranking as roughly "this thread's share of the whole profile".
	for _, s := range syms {
		if strings.HasPrefix(s.Func, "Thread_") {
			t.Errorf("sample's synthetic thread-root row leaked into output: %+v", s)
		}
	}
	// This capture is dominated by idle goroutine-park time (the workload
	// sleeps between bursts); ActiveSamples must still be a real, non-idle
	// slice of Samples, not accidentally the whole thing or nothing.
	if stats.Samples <= 0 || stats.ActiveSamples <= 0 || stats.ActiveSamples >= stats.Samples {
		t.Errorf("stats = %+v, want 0 < ActiveSamples < Samples", stats)
	}
	if stats.IdlePct <= 0 {
		t.Errorf("IdlePct = %v, want > 0 (this capture parks on channels/timers most of the time)", stats.IdlePct)
	}
}

// TestParseSampleTreeIgnoresTrailingSections is the regression for the
// misparse where `sample`'s trailing "Total number in stack (recursive
// counted multiple, when >=5):" and "Sort by top of stack" sections — which
// reuse the exact "<prefix><count> name" shape a real call-graph row has —
// got read as children of whatever frame was still open on the stack,
// inventing self time and inflating Stats.Samples. Parsing the full fixture
// (trailer included) must produce byte-for-byte the same result as parsing
// only its call-graph section.
func TestParseSampleTreeIgnoresTrailingSections(t *testing.T) {
	raw, err := os.ReadFile("testdata/darwin-sample-go.txt")
	if err != nil {
		t.Fatal(err)
	}
	full := string(raw)
	if !strings.Contains(full, "Total number in stack") {
		t.Fatal("fixture missing the trailing section this test guards against")
	}
	trimmed := callGraphOnlyFixture(t)

	symsFull, statsFull := parseSampleTree(full)
	symsTrimmed, statsTrimmed := parseSampleTree(trimmed)

	if statsFull != statsTrimmed {
		t.Fatalf("trailing sections changed Stats: with=%+v without=%+v", statsFull, statsTrimmed)
	}
	if len(symsFull) != len(symsTrimmed) {
		t.Fatalf("trailing sections changed symbol count: with=%d without=%d", len(symsFull), len(symsTrimmed))
	}
	for i := range symsFull {
		if symsFull[i] != symsTrimmed[i] {
			t.Fatalf("trailing sections changed symbol[%d]: with=%+v without=%+v", i, symsFull[i], symsTrimmed[i])
		}
	}
}

// TestParseSampleTreeAggregatesAllThreads: two threads each spend all of
// their samples inside the SAME leaf function (reached via different
// top-level callers). The old parser read frames without any per-thread
// tree structure and had no notion of "all threads"; parseSampleTree must
// walk both threads and sum their self time into one entry. The two
// distinct pass-through callers (workerA/workerB) are real call frames —
// unlike the synthetic "Thread_N" root row sample itself prints — so, like
// a pprof wrapper with flat=0/cum>0, they legitimately appear with Weight=0
// (never the leaf) and Cum=50 (their own thread's 100 active samples out of
// the profile-wide 200 total), rather than being excluded outright.
func TestParseSampleTreeAggregatesAllThreads(t *testing.T) {
	text := `Call graph:
    100 Thread_1
    + 100 main.workerA  (in app) + 10  [0x1]
    + !   100 main.hotFunc  (in app) + 20  [0x2]
    100 Thread_2
    + 100 main.workerB  (in app) + 10  [0x3]
    + !   100 main.hotFunc  (in app) + 20  [0x4]
`
	syms, stats := parseSampleTree(text)
	var hotCount int
	var hot Symbol
	for _, s := range syms {
		if s.Func == "main.hotFunc" {
			hotCount++
			hot = s
		}
		if s.Func == "Thread_1" || s.Func == "Thread_2" {
			t.Errorf("sample's synthetic thread-root row %q leaked into output: %+v", s.Func, s)
		}
		if (s.Func == "main.workerA" || s.Func == "main.workerB") && (s.Weight != 0 || s.Cum != 50) {
			t.Errorf("pass-through caller %q = %+v, want Weight=0 Cum=50 (never the leaf, but its own thread's 100 active samples out of 200 total flow through it)", s.Func, s)
		}
	}
	if hotCount != 1 {
		t.Fatalf("main.hotFunc appeared %d times, want 1 merged entry across both threads; got %+v", hotCount, syms)
	}
	if hot.Weight != 100 || hot.Cum != 100 {
		t.Errorf("main.hotFunc = %+v, want Weight=100 Cum=100 (sole active symbol, both threads combined)", hot)
	}
	if stats.Samples != 200 || stats.ActiveSamples != 200 {
		t.Fatalf("stats = %+v, want Samples=200 ActiveSamples=200 (100 from each of 2 threads)", stats)
	}
}

// TestParseSampleTreeIdleBucket: a thread that's 90% parked in a known idle
// syscall leaf and 10% in real work must report the real function at 100%
// weight (denominator excludes idle) and the idle share in Stats, instead of
// idle syscalls dominating the Symbol list the way (idle) used to for CDP
// profiles before flattenCDPProfile excluded it.
func TestParseSampleTreeIdleBucket(t *testing.T) {
	text := `Call graph:
    100 Thread_1
    + 90 runtime.usleep_trampoline.abi0  (in app) + 1  [0x1]
    + !   90 __semwait_signal  (in libsystem_kernel.dylib) + 8  [0x2]
    + 10 main.hotFunc  (in app) + 20  [0x3]
`
	syms, stats := parseSampleTree(text)
	for _, s := range syms {
		if s.Func == "__semwait_signal" {
			t.Fatalf("idle leaf __semwait_signal leaked into the symbol list: %+v", syms)
		}
	}
	var hot *Symbol
	for i := range syms {
		if syms[i].Func == "main.hotFunc" {
			hot = &syms[i]
		}
	}
	if hot == nil {
		t.Fatalf("main.hotFunc missing: %+v", syms)
	}
	if hot.Weight != 100 {
		t.Errorf("main.hotFunc weight = %v, want 100 (idle excluded from the denominator)", hot.Weight)
	}
	if stats.Samples != 100 || stats.ActiveSamples != 10 {
		t.Fatalf("stats = %+v, want Samples=100 ActiveSamples=10", stats)
	}
	if stats.IdlePct < 89.9 || stats.IdlePct > 90.1 {
		t.Errorf("IdlePct = %v, want ~90", stats.IdlePct)
	}
}

func TestParseSampleTreeEmpty(t *testing.T) {
	syms, stats := parseSampleTree("no call graph here\njust some other text\n")
	if syms != nil {
		t.Errorf("no call-graph lines should return nil symbols, got %+v", syms)
	}
	if stats != (Stats{}) {
		t.Errorf("no call-graph lines should return zero-value stats, got %+v", stats)
	}
}
