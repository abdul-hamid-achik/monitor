package devrun

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

func testProjectIdentity() project.Identity {
	return project.Identity{Slug: "acme", Service: "svc", GitRoot: "/repo", Root: "/repo"}
}

func panicLines() []string {
	return strings.Split(strings.TrimRight(goCrashPanicText, "\n"), "\n")
}

// runDetectorOverLines drives det.run over a fresh, self-closed channel of
// sl, waiting for run to return (its own final Flush + flushAllPending)
// before returning -- so callers never race det's fields against its own
// goroutine.
func runDetectorOverLines(t *testing.T, det *detector, sl []streamLine) {
	t.Helper()
	lines := make(chan streamLine, len(sl)+1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		det.run(context.Background(), lines)
	}()
	for _, l := range sl {
		lines <- l
	}
	close(lines)
	select {
	case <-done:
	case <-time.After(flushShutdownBudget + 3*time.Second):
		t.Fatal("detector.run did not return within the shutdown flush budget + margin")
	}
}

func stderrLines(text string) []streamLine {
	var out []streamLine
	for _, l := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		out = append(out, streamLine{stream: streamStderr, line: l})
	}
	return out
}

// TestShortIssueIDUsesFirstFourHexCharsAfterPrefix: docs/contracts/
// issue-context-v1.md defines short_id as the uppercase FIRST 4 hex
// characters of the id's hex portion, since that is what E2.5's prefix
// resolution (`monitor issue <short_id>`) will accept.
func TestShortIssueIDUsesFirstFourHexCharsAfterPrefix(t *testing.T) {
	got := shortIssueID("ISS-A07E1234567890AB")
	if got != "A07E" {
		t.Errorf("shortIssueID = %q, want A07E (the first 4 hex chars)", got)
	}
}

func TestShortIssueIDShortInputPassesThroughUnchanged(t *testing.T) {
	if got := shortIssueID("ISS-AB"); got != "AB" {
		t.Errorf("shortIssueID(short) = %q, want AB unchanged", got)
	}
}

// TestLiveDedupeKeyNeverFoldsSequentialWindowsFromTheSameDetector proves
// dedupeBucketSeconds' invariant: a detector can only ever open a second
// coalescing window for the same fingerprint after the first one's timer
// has fired (coalesceWindow later, at minimum), so consecutive windows
// simulated at exactly that minimum spacing must never collide -- the bug
// a flat 3s bucket (wider than the 2s coalesce window) had.
func TestLiveDedupeKeyNeverFoldsSequentialWindowsFromTheSameDetector(t *testing.T) {
	block := stacktrace.Block{Lines: []string{"panic: boom"}}
	base := time.Unix(1_700_000_000, 999_000_000) // worst-case: just under a second boundary
	seen := map[string]int{}
	for i := 0; i < 20; i++ {
		ts := base.Add(time.Duration(i) * coalesceWindow)
		key := liveDedupeKey("/repo", block, ts)
		if prev, ok := seen[key]; ok {
			t.Fatalf("window %d collided with window %d's key %q -- sequential windows from one detector must never fold together", i, prev, key)
		}
		seen[key] = i
	}
}

// TestLiveDedupeKeyFoldsNearSimultaneousWindowsAcrossDetectors is the other
// half: a nested `monitor run --` reading the same underlying text observes
// it within milliseconds of the outer launch, and must still dedupe.
func TestLiveDedupeKeyFoldsNearSimultaneousWindowsAcrossDetectors(t *testing.T) {
	block := stacktrace.Block{Lines: []string{"panic: boom"}}
	t0 := time.Unix(1_700_000_000, 500_000_000)
	t1 := t0.Add(50 * time.Millisecond)
	if liveDedupeKey("/repo", block, t0) != liveDedupeKey("/repo", block, t1) {
		t.Error("two near-simultaneous windows across nested launches must produce the SAME live DedupeKey")
	}
}

// TestDetectorKeepsSeparateJoinerPerStream is the --scan both regression
// test (the naming ADR's "Joiner por stream"): a
// zap-shaped stdout log line -- a hard block boundary (isBoundary) -- is
// interleaved between every line of a stderr panic. With one Joiner per
// stream, the stdout lines never reach the stderr Joiner at all, and the
// panic is recorded whole.
func TestDetectorKeepsSeparateJoinerPerStream(t *testing.T) {
	store := filepath.Join(t.TempDir(), "issues.veclite")
	det := newDetector(detectorOptions{storePath: store, launch: LaunchIDs{Root: "/repo"}, id: testProjectIdentity()})

	var mixed []streamLine
	zapLine := "2026-09-22T10:02:11-06:00\tinfo\tingest listening on :9101"
	for _, l := range panicLines() {
		mixed = append(mixed, streamLine{stream: streamStderr, line: l})
		mixed = append(mixed, streamLine{stream: streamStdout, line: zapLine})
	}
	runDetectorOverLines(t, det, mixed)

	db := openStoreForTest(t, store)
	list, err := db.List(issues.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("issues = %+v, want exactly 1 (the stderr panic, undisturbed by interleaved stdout lines)", list)
	}
	if list[0].ExceptionType != "panic" {
		t.Errorf("exception_type = %q, want panic", list[0].ExceptionType)
	}
}

// TestDetectorDiscardsBlockSpanningAGap is pump.go's gap contract
// (the naming ADR, extended): a drop must lose an
// event, never fabricate a wrong one. The block's final line (the crash
// frame) arrives flagged gap:true; the whole in-progress block must be
// discarded, not recorded as a phantom, wrongly-shaped issue. A second,
// CLEAN pass of the same trace right after proves the stream recovers
// (the gap does not permanently wedge that stream's Joiner).
func TestDetectorDiscardsBlockSpanningAGap(t *testing.T) {
	store := filepath.Join(t.TempDir(), "issues.veclite")
	det := newDetector(detectorOptions{storePath: store, launch: LaunchIDs{Root: "/repo"}, id: testProjectIdentity()})

	pl := panicLines()
	var sl []streamLine
	for _, l := range pl[:len(pl)-1] {
		sl = append(sl, streamLine{stream: streamStderr, line: l})
	}
	// The crash frame itself arrives right after a drop.
	sl = append(sl, streamLine{stream: streamStderr, line: pl[len(pl)-1], gap: true})
	runDetectorOverLines(t, det, sl)

	db := openStoreForTest(t, store)
	list, err := db.List(issues.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("issues = %+v, want none: a block spanning a gap must be discarded, not recorded", list)
	}
}

// TestDetectorNewVsRepeatAcrossRunsUsesStoreOccurrenceCount is the NEW-vs-
// "again" fix: NEW means "this write created the issue's first-ever
// occurrence", not merely "this session has not seen the fingerprint
// before". A second, entirely fresh detector (a new `monitor run --`
// session) recording the SAME crash against the SAME store must report it
// as a repeat, never as NEW.
func TestDetectorNewVsRepeatAcrossRunsUsesStoreOccurrenceCount(t *testing.T) {
	store := filepath.Join(t.TempDir(), "issues.veclite")
	runOnce := func() (newCount, repeatCount int) {
		det := newDetector(detectorOptions{
			storePath: store, launch: LaunchIDs{Root: "/repo"}, id: testProjectIdentity(),
			onNewIssue: func(newIssueEvent) { newCount++ },
			onRepeat:   func(string, int64) { repeatCount++ },
		})
		runDetectorOverLines(t, det, stderrLines(goCrashPanicText))
		return newCount, repeatCount
	}

	n1, r1 := runOnce()
	if n1 != 1 || r1 != 0 {
		t.Fatalf("first run: new=%d repeat=%d, want 1/0", n1, r1)
	}
	// Clear dedupeBucketSeconds' window: this test is about NEW-vs-repeat
	// occurrence-counting semantics, not about live dedupe timing (a
	// SEPARATE property, covered by TestLiveDedupeKey*), so the two runs
	// must not accidentally collide on the live DedupeKey the way two
	// nested launches observing the same instant intentionally would.
	time.Sleep(1100 * time.Millisecond)
	n2, r2 := runOnce()
	if n2 != 0 || r2 != 1 {
		t.Fatalf("second run (fresh session, same store, same crash): new=%d repeat=%d, want 0/1 -- a pre-existing issue must never be announced as NEW", n2, r2)
	}
}

// TestDetectorCountsFailedWritesWhenStoreLockedAtShutdown is the bounded-
// shutdown-flush fix: with the store's writer lock held by another process
// for the whole run, flushAllPending must still return within its budget
// (not hang behind DefaultWriterWait per pending fingerprint) and the
// failed write must be counted, not silently lost.
func TestDetectorCountsFailedWritesWhenStoreLockedAtShutdown(t *testing.T) {
	store := filepath.Join(t.TempDir(), "issues.veclite")
	lockHolder, err := issues.OpenStore(store)
	if err != nil {
		t.Fatalf("hold the writer lock: %v", err)
	}
	t.Cleanup(func() { _ = lockHolder.Close() })

	det := newDetector(detectorOptions{storePath: store, launch: LaunchIDs{Root: "/repo"}, id: testProjectIdentity()})
	runDetectorOverLines(t, det, stderrLines(goCrashPanicText))

	if det.failedWrites != 1 {
		t.Errorf("failedWrites = %d, want 1 (the store lock is held for the whole run)", det.failedWrites)
	}
	if len(det.newIssueIDs) != 0 {
		t.Errorf("newIssueIDs = %v, want none: the write never succeeded", det.newIssueIDs)
	}
}
