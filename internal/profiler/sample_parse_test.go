package profiler

// No build tag: parseSampleTree is exercised on every OS CI runs, not just
// darwin (only the `sample` exec itself is darwin-only; see sample_darwin.go
// / sample_other.go).

import (
	"os"
	"testing"
)

// TestParseSampleTreeRealOutputFindsGoFrames replaces the old regex parser's
// test (which fed it output the real tool never produces — a digit at the
// very start of the line). darwin-sample-go.txt is a genuine `sample <pid> 1`
// capture of a Go binary with a planted json.Marshal-heavy hot path
// (main.heavyStringify). The old parser matched zero lines against it.
func TestParseSampleTreeRealOutputFindsGoFrames(t *testing.T) {
	raw, err := os.ReadFile("testdata/darwin-sample-go.txt")
	if err != nil {
		t.Fatal(err)
	}
	syms, stats := parseSampleTree(string(raw))
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
	if hot.Weight <= 0 {
		t.Errorf("main.heavyStringify weight = %v, want > 0", hot.Weight)
	}
	if hot.File != "gowork" {
		t.Errorf("main.heavyStringify image = %q, want gowork (the containing binary)", hot.File)
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

// TestParseSampleTreeAggregatesAllThreads: two threads each spend all of
// their samples inside the SAME leaf function (reached via different
// top-level callers). The old parser read frames without any per-thread
// tree structure and had no notion of "all threads"; parseSampleTree must
// walk both threads and sum their self time into one entry.
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
		if s.Func == "main.workerA" || s.Func == "main.workerB" {
			t.Errorf("pass-through caller %q leaked into output (should have self=0): %+v", s.Func, s)
		}
	}
	if hotCount != 1 {
		t.Fatalf("main.hotFunc appeared %d times, want 1 merged entry across both threads; got %+v", hotCount, syms)
	}
	if hot.Weight != 100 {
		t.Errorf("main.hotFunc weight = %v, want 100 (sole active symbol, both threads combined)", hot.Weight)
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
