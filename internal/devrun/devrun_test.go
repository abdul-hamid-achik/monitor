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

func TestRunNestedLaunchInheritsLaunchRoot(t *testing.T) {
	// Simulate `monitor run -- task dev` where `task dev` itself invokes
	// `monitor run --name inner -- <cmd>`: the CHILD process (env |
	// grep) should see the SAME MONITOR_LAUNCH_ROOT this process was
	// launched with, not a freshly computed one.
	opts := baseOptions(t, []string{"sh", "-c", "env | grep ^MONITOR_LAUNCH_ | sort"})
	opts.environOverride = []string{
		"PATH=/usr/bin:/bin",
		"MONITOR_LAUNCH_ID=outer-id",
		"MONITOR_LAUNCH_SERVICE=outer-svc",
		"MONITOR_LAUNCH_ROOT=/outer/repo",
	}
	opts.Name = "inner-should-be-ignored"
	var stdout, stderr, banner bytes.Buffer
	opts.Stdout, opts.Stderr, opts.Banner = &stdout, &stderr, &banner

	if _, err := Run(context.Background(), opts); err != nil {
		t.Fatalf("Run: %v", err)
	}
	got := stdout.String()
	for _, want := range []string{
		"MONITOR_LAUNCH_ID=outer-id",
		"MONITOR_LAUNCH_SERVICE=outer-svc",
		"MONITOR_LAUNCH_ROOT=/outer/repo",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("child env = %q, missing %q (nested launch must inherit unchanged)", got, want)
		}
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
