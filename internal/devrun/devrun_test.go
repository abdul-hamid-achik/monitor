package devrun

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/issues"
)

// goCrashPanicText is internal/stacktrace's own golden dogfood fixture for
// a Go panic (internal/stacktrace/testdata/dogfood/go-crash.stderr.txt),
// reused here via a plain `sh -c printf` child rather than building the
// examples/polyglot/go-crash binary -- Parse only cares about the printed
// text shape, not that a real panic produced it.
const goCrashPanicText = "panic: go-crash workload: intentional uncaught failure\n" +
	"\n" +
	"goroutine 1 [running]:\n" +
	"main.main()\n" +
	"\t/repo/examples/polyglot/go-crash/main.go:11 +0x60\n"

func isolatedStore(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "issues.veclite")
}

func boolPtr(v bool) *bool { return &v }

func baseOptions(t *testing.T, argv []string) Options {
	t.Helper()
	return Options{
		Argv:            argv,
		StorePath:       isolatedStore(t),
		Stdin:           strings.NewReader(""),
		Quiet:           true,
		environOverride: []string{"PATH=/usr/bin:/bin"},
		ttyOverride:     boolPtr(false),
	}
}

func openStoreForTest(t *testing.T, path string) *issues.Store {
	t.Helper()
	store, err := issues.OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly(%s): %v", path, err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func TestRunPropagatesChildExitCodeAndRecordsIssue(t *testing.T) {
	opts := baseOptions(t, []string{"sh", "-c", "printf %s '" + goCrashPanicText + "' >&2; exit 2"})
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2", result.ExitCode)
	}
	if len(result.NewIssueIDs) != 1 {
		t.Fatalf("NewIssueIDs = %v, want exactly 1", result.NewIssueIDs)
	}
	if result.Occurrences != 1 {
		t.Errorf("Occurrences = %d, want 1", result.Occurrences)
	}
	if !strings.Contains(stderr.String(), "panic: go-crash workload") {
		t.Errorf("child stderr passthrough = %q, want the panic text untouched", stderr.String())
	}

	store := openStoreForTest(t, opts.StorePath)
	list, err := store.List(issues.ListOptions{})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(list), list)
	}
	if list[0].OccurrenceCount != 1 {
		t.Errorf("occurrence_count = %d, want 1", list[0].OccurrenceCount)
	}
	if list[0].ExceptionType != "panic" {
		t.Errorf("exception_type = %q, want panic", list[0].ExceptionType)
	}

	// R-major-next-hint: Result plumbs the stored issue's own FULL id
	// through (not just its display-only short id) for callers that need
	// it, but the exit summary's "next:" hint (opts.Quiet suppresses it
	// above, see baseOptions; the rendering itself is covered directly by
	// TestExitSummaryWithNewIssuesSuggestsNext) is FIX 2's `monitor issue
	// <short>` -- see FirstNewIssueFullID's doc comment.
	if result.FirstNewIssueFullID != list[0].ID {
		t.Errorf("FirstNewIssueFullID = %q, want the stored issue's own id %q", result.FirstNewIssueFullID, list[0].ID)
	}
	wantHint := "next: monitor issue " + strings.ToLower(result.NewIssueIDs[0])
	if got := ExitSummary(ExitSummaryInfo{NewIssueIDs: result.NewIssueIDs, FirstNewIssueFullID: result.FirstNewIssueFullID}); !strings.Contains(got, wantHint) {
		t.Errorf("ExitSummary(result's own fields) = %q, want it to contain %q", got, wantHint)
	}
}

// TestRunInheritsStdinForInteractiveRead is the roadmap's explicit
// done-when scenario: `sh -c 'read x; echo $x'` must work, proving stdin is
// inherited/forwarded to the child exactly like a plain, unwrapped launch
// would -- regardless of --scan, which only affects stdout/stderr.
func TestRunInheritsStdinForInteractiveRead(t *testing.T) {
	opts := baseOptions(t, []string{"sh", "-c", "read x; echo GOT:$x"})
	opts.Stdin = strings.NewReader("hello-tty\n")
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if !strings.Contains(stdout.String(), "GOT:hello-tty") {
		t.Errorf("stdout = %q, want the child's `read` to see the piped stdin", stdout.String())
	}
}

func TestRunSuccessfulChildRecordsNoIssues(t *testing.T) {
	opts := baseOptions(t, []string{"sh", "-c", "echo all good; exit 0"})
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 0 {
		t.Errorf("ExitCode = %d, want 0", result.ExitCode)
	}
	if len(result.NewIssueIDs) != 0 {
		t.Errorf("NewIssueIDs = %v, want none", result.NewIssueIDs)
	}
}

func TestRunNoIssuesFlagSkipsDetectionEntirely(t *testing.T) {
	opts := baseOptions(t, []string{"sh", "-c", "printf %s '" + goCrashPanicText + "' >&2; exit 2"})
	opts.NoIssues = true
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2 (still propagated with --no-issues)", result.ExitCode)
	}
	if len(result.NewIssueIDs) != 0 {
		t.Errorf("NewIssueIDs = %v, want none with --no-issues", result.NewIssueIDs)
	}
	if !strings.Contains(stderr.String(), "panic: go-crash workload") {
		t.Errorf("child stderr passthrough = %q, --no-issues must still pass output through", stderr.String())
	}
}

func TestRunScanBothCatchesStdoutWhileStdoutPassesThrough(t *testing.T) {
	opts := baseOptions(t, []string{"sh", "-c",
		"echo readiness-text; printf %s '" + goCrashPanicText + "'; exit 0"})
	opts.Scan = ScanBoth
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !strings.Contains(stdout.String(), "readiness-text") {
		t.Errorf("stdout = %q, readiness text must still appear on stdout", stdout.String())
	}
	if len(result.NewIssueIDs) != 1 {
		t.Fatalf("NewIssueIDs = %v, want exactly 1 (--scan both must catch the stdout panic text)", result.NewIssueIDs)
	}
}

func TestRunScanStderrDefaultIgnoresStdoutExceptions(t *testing.T) {
	opts := baseOptions(t, []string{"sh", "-c", "printf %s '" + goCrashPanicText + "'; exit 0"})
	// opts.Scan left at "" -> defaults to stderr.
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.NewIssueIDs) != 0 {
		t.Errorf("NewIssueIDs = %v, want none: default --scan stderr must not detect a stdout-only panic", result.NewIssueIDs)
	}
	if !strings.Contains(stdout.String(), "panic: go-crash workload") {
		t.Errorf("stdout = %q, the text must still pass through even though it is not scanned", stdout.String())
	}
}

func TestRunInvalidScanIsRejected(t *testing.T) {
	opts := baseOptions(t, []string{"true"})
	opts.Scan = "bogus"
	if _, err := Run(context.Background(), opts); err == nil {
		t.Fatal("Run with an invalid --scan value should return an error")
	}
}

// TestRunNestedLaunchInheritsOnlyRootAndHonorsAnInnerName is the roadmap's
// own worked nesting example (`monitor run -- task dev` launching `monitor
// run --name web-api -- node ...`): the CHILD process sees the SAME
// MONITOR_LAUNCH_ROOT this process was launched with (so the live DedupeKey
// still folds a nested pair together), but an inner --name still becomes
// its OWN MONITOR_LAUNCH_SERVICE rather than being silently replaced by the
// outer launch's, and it gets its own fresh MONITOR_LAUNCH_ID.
func TestRunNestedLaunchInheritsOnlyRootAndHonorsAnInnerName(t *testing.T) {
	opts := baseOptions(t, []string{"sh", "-c", "env | grep ^MONITOR_LAUNCH_ | sort"})
	opts.environOverride = []string{
		"PATH=/usr/bin:/bin",
		"MONITOR_LAUNCH_ID=outer-id",
		"MONITOR_LAUNCH_SERVICE=outer-svc",
		"MONITOR_LAUNCH_ROOT=/outer/repo",
	}
	opts.Name = "web-api"
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := stdout.String()
	if !strings.Contains(got, "MONITOR_LAUNCH_ROOT=/outer/repo") {
		t.Errorf("child env = %q, missing the inherited MONITOR_LAUNCH_ROOT", got)
	}
	if !strings.Contains(got, "MONITOR_LAUNCH_SERVICE=web-api") {
		t.Errorf("child env = %q, want MONITOR_LAUNCH_SERVICE=web-api (the inner --name), not the outer launch's", got)
	}
	if strings.Contains(got, "MONITOR_LAUNCH_ID=outer-id") {
		t.Errorf("child env = %q, want a fresh MONITOR_LAUNCH_ID, not the outer launch's", got)
	}
}

func TestRunChildEnvironmentOnlyExportsLaunchVars(t *testing.T) {
	// The golden rule (docs/contracts/local-sentry-naming.md §2): run --
	// exports ONLY MONITOR_LAUNCH_*, never MONITOR, MONITOR_RUN_DIR,
	// MONITOR_SERVICE, or MONITOR_RUN_ID.
	opts := baseOptions(t, []string{"sh", "-c", "env | grep ^MONITOR | sort"})
	opts.environOverride = []string{
		"PATH=/usr/bin:/bin",
		"MONITOR=1",
		"MONITOR_RUN_DIR=/tmp/somewhere",
		"MONITOR_RUN_ID=outside-id",
		"MONITOR_SERVICE=outside-svc",
	}
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := stdout.String()
	for _, forbidden := range []string{"MONITOR=1", "MONITOR_RUN_DIR="} {
		if strings.Contains(got, forbidden) {
			t.Errorf("child env = %q, must never see %q", got, forbidden)
		}
	}
	for _, want := range []string{"MONITOR_LAUNCH_ID=", "MONITOR_LAUNCH_SERVICE=", "MONITOR_LAUNCH_ROOT="} {
		if !strings.Contains(got, want) {
			t.Errorf("child env = %q, missing %q", got, want)
		}
	}
}

// TestRunStartBannerScrubsSecretShapedArgv is the argv-in-the-banner fix: a
// secret-shaped value passed as a command-line argument (not just one
// detected in the child's own output) must not land in the start banner
// unredacted.
func TestRunStartBannerScrubsSecretShapedArgv(t *testing.T) {
	opts := baseOptions(t, []string{"true", "--token=sekrit-value-1234"})
	opts.environOverride = []string{"PATH=/usr/bin:/bin", "FAKE_API_TOKEN=sekrit-value-1234"}
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if strings.Contains(banner.String(), "sekrit-value-1234") {
		t.Errorf("banner = %q, must not contain the unredacted argv value", banner.String())
	}
}

func TestRunQuietSuppressesBannersButNotExitSummaryDrops(t *testing.T) {
	opts := baseOptions(t, []string{"true"})
	opts.Quiet = true
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if banner.Len() != 0 {
		t.Errorf("banner output = %q, want nothing printed for a clean quiet run", banner.String())
	}
}

// TestRunDoesNotHangBehindAnOrphanedGrandchildHoldingThePipeOpen is the
// process-reap-ordering fix: a grandchild that inherits the scanned pipe's
// write end and outlives the direct child (a background job the shell
// leaves running, or `go run .`'s compiled binary staying alive after `go
// run` itself exits) must not pin Run() waiting for that pipe to EOF --
// which requires EVERY holder of the write end to close it -- once the
// direct child is already gone. Bounded by childIOGrace instead.
func TestRunDoesNotHangBehindAnOrphanedGrandchildHoldingThePipeOpen(t *testing.T) {
	opts := baseOptions(t, []string{"sh", "-c", "sleep 6 & echo started; exit 3"})
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	start := time.Now()
	result, err := Run(context.Background(), opts)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 3 {
		t.Errorf("ExitCode = %d, want 3 (the direct child's own exit code)", result.ExitCode)
	}
	// childIOGrace (2s) plus generous scheduling slack -- nowhere near the
	// orphan's full 6s sleep, which is what this package used to wait out.
	if elapsed > 4*time.Second {
		t.Errorf("Run took %v, want well under the orphan's 6s sleep (bounded by childIOGrace)", elapsed)
	}
	if !strings.Contains(stdout.String(), "started") {
		t.Errorf("stdout = %q, want the child's own output before it exited", stdout.String())
	}
}

// TestRunFlushesFailedWritesQuicklyWhenStoreIsLocked is the other half of
// the roadmap's "burst while the store is locked" done-when: the OTHER
// burst test below uses pure filler text, so the detector never actually
// attempts a store write and the held lock is never contended. This one
// carries a real crash, so flushAllPending's bounded, concurrent shutdown
// flush (not issues.DefaultWriterWait synchronously per pending
// fingerprint) is what is actually being measured.
func TestRunFlushesFailedWritesQuicklyWhenStoreIsLocked(t *testing.T) {
	storePath := isolatedStore(t)
	lockHolder, err := issues.OpenStore(storePath)
	if err != nil {
		t.Fatalf("open store to hold the writer lock: %v", err)
	}
	t.Cleanup(func() { _ = lockHolder.Close() })

	opts := baseOptions(t, []string{"sh", "-c", "printf %s '" + goCrashPanicText + "' >&2; exit 2"})
	opts.StorePath = storePath
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	start := time.Now()
	result, err := Run(context.Background(), opts)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if result.ExitCode != 2 {
		t.Errorf("ExitCode = %d, want 2 (the child's own exit code, unaffected by the store being locked)", result.ExitCode)
	}
	// Well under issues.DefaultWriterWait (5s): bounded by
	// detector.flushShutdownBudget instead.
	if elapsed > 4*time.Second {
		t.Errorf("Run took %v with the store locked, want well under issues.DefaultWriterWait (5s)", elapsed)
	}
	if result.FailedWrites != 1 {
		t.Errorf("FailedWrites = %d, want 1 (the crash could not be recorded: the store lock was held the whole run)", result.FailedWrites)
	}
	if len(result.NewIssueIDs) != 0 {
		t.Errorf("NewIssueIDs = %v, want none: the write never succeeded", result.NewIssueIDs)
	}
	if !strings.Contains(stderr.String(), "panic: go-crash workload") {
		t.Error("stderr passthrough is missing the crash text -- it must still reach the terminal even when the store write fails")
	}
}

// TestRunBurstNeverSlowsTheChildEvenWithStoreLocked is the roadmap's
// explicit done-when scenario: a child writing a large burst to stderr,
// while another process holds the issues store's exclusive writer lock,
// must finish within 2s of how long the same child takes with no monitor
// involved at all, and the exit summary must report the drops honestly.
//
// The burst is ordinary (non-exception-shaped) text -- the realistic case,
// since most raw process output is not a stack trace -- generated by a
// single awk process rather than a shell loop, so the measurement reflects
// pump.go's overhead rather than 200k fork/exec cycles of a naive script.
func TestRunBurstNeverSlowsTheChildEvenWithStoreLocked(t *testing.T) {
	const burstLines = 200_000 // well over the 1024-line channel capacity
	script := `awk 'BEGIN{for(i=0;i<` + strconv.Itoa(burstLines) +
		`;i++) print "burst line " i " of filler text that is not a stack trace"}' >&2`

	rawStart := time.Now()
	raw := exec.Command("sh", "-c", script)
	raw.Stdout = io.Discard
	raw.Stderr = io.Discard
	if err := raw.Run(); err != nil {
		t.Fatalf("baseline (no monitor) run: %v", err)
	}
	rawElapsed := time.Since(rawStart)

	storePath := isolatedStore(t)
	lockHolder, err := issues.OpenStore(storePath)
	if err != nil {
		t.Fatalf("open store to hold the writer lock: %v", err)
	}
	t.Cleanup(func() { _ = lockHolder.Close() })

	opts := baseOptions(t, []string{"sh", "-c", script})
	opts.StorePath = storePath
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	devrunStart := time.Now()
	result, err := Run(context.Background(), opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	devrunElapsed := time.Since(devrunStart)

	if diff := devrunElapsed - rawElapsed; diff > 2*time.Second {
		t.Errorf("devrun took %v vs baseline %v (no monitor) -- %v slower, want within 2s even with the store locked",
			devrunElapsed, rawElapsed, diff)
	}
	if result.Dropped == 0 {
		t.Error("Dropped = 0, want > 0: a burst this size must overflow the bounded channel")
	}
	if stderr.Len() == 0 {
		t.Error("stderr passthrough is empty, want the burst text to still reach the terminal")
	}
}
