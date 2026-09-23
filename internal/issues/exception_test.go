package issues

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// syntheticInApp is a minimal in-app frame builder for the pure-function
// fingerprint/culprit tests below; real chained fixtures are exercised
// separately (TestChainedPythonDirectCauseCulpritFallsInInnermostCause,
// TestChainedNodeCauseCulpritFallsInInnermostCause).
func syntheticInApp(fn, file string, line int) stacktrace.Frame {
	return stacktrace.Frame{Function: fn, Filename: file, Lineno: line, InApp: true}
}

func TestFingerprintV2ExceptionStableAcrossShiftedLineNumbers(t *testing.T) {
	base := stacktrace.Exception{
		Type:  "TypeError",
		Value: "Cannot read properties of undefined (reading 'id')",
		Frames: []stacktrace.Frame{
			syntheticInApp("loadUser", "src/users.ts", 10),
			syntheticInApp("handle", "src/users.ts", 42),
		},
	}
	shifted := base
	shifted.Frames = []stacktrace.Frame{
		syntheticInApp("loadUser", "src/users.ts", 55),
		syntheticInApp("handle", "src/users.ts", 99),
	}
	first := FingerprintV2Exception(base, "acme")
	second := FingerprintV2Exception(shifted, "acme")
	if first != second {
		t.Fatalf("shifted line numbers changed the fingerprint:\n%s\n%s", first, second)
	}
}

func TestFingerprintV2ExceptionDifferentInAppCallerDiffers(t *testing.T) {
	base := stacktrace.Exception{
		Type:  "TypeError",
		Value: "boom",
		Frames: []stacktrace.Frame{
			syntheticInApp("loadUser", "src/users.ts", 10),
			syntheticInApp("handle", "src/users.ts", 42),
		},
	}
	otherCaller := base
	otherCaller.Frames = []stacktrace.Frame{
		syntheticInApp("loadAdmin", "src/admin.ts", 10),
		syntheticInApp("handle", "src/users.ts", 42),
	}
	first := FingerprintV2Exception(base, "acme")
	second := FingerprintV2Exception(otherCaller, "acme")
	if first == second {
		t.Fatal("a different in-app caller frame produced the same fingerprint")
	}
}

func TestFingerprintV2ExceptionDifferentProjectDiffers(t *testing.T) {
	ex := stacktrace.Exception{Type: "TypeError", Value: "boom", Frames: []stacktrace.Frame{syntheticInApp("f", "a.ts", 1)}}
	if FingerprintV2Exception(ex, "acme") == FingerprintV2Exception(ex, "other") {
		t.Fatal("different project produced the same fingerprint")
	}
}

func TestFingerprintV2ExceptionMessageOnlyFallsBackToNormalizedMessage(t *testing.T) {
	a := stacktrace.Exception{Type: "ConnectionError", Value: "connection refused: 10.0.4.12:5432"}
	b := stacktrace.Exception{Type: "ConnectionError", Value: "connection refused: 10.0.4.19:5432"}
	// No in_app frames at all (a message-only event): the dynamic IP is
	// normalized away, so both still fingerprint together.
	if FingerprintV2Exception(a, "acme") != FingerprintV2Exception(b, "acme") {
		t.Fatal("message-only events with only a dynamic value difference did not group")
	}
	c := stacktrace.Exception{Type: "ConnectionError", Value: "timeout"}
	if FingerprintV2Exception(a, "acme") == FingerprintV2Exception(c, "acme") {
		t.Fatal("unrelated messages produced the same fingerprint")
	}
}

func TestFingerprintV2ExceptionOuterFramesIgnoreOutOfAppFrames(t *testing.T) {
	// A non-in_app frame sitting anywhere in Frames must never enter the
	// hash: only the in_app subset counts toward the top-5.
	withDependencyFrame := stacktrace.Exception{
		Type: "TypeError", Value: "boom",
		Frames: []stacktrace.Frame{
			{Function: "internalHelper", Filename: "node_modules/lib/index.js", Lineno: 5, InApp: false},
			syntheticInApp("loadUser", "src/users.ts", 42),
		},
	}
	withoutDependencyFrame := stacktrace.Exception{
		Type: "TypeError", Value: "boom",
		Frames: []stacktrace.Frame{
			syntheticInApp("loadUser", "src/users.ts", 42),
		},
	}
	if FingerprintV2Exception(withDependencyFrame, "acme") != FingerprintV2Exception(withoutDependencyFrame, "acme") {
		t.Fatal("a non-in_app frame affected the fingerprint")
	}
}

func TestCulpritFallsBackToOuterWhenInnermostHasNoInAppFrame(t *testing.T) {
	ex := stacktrace.Exception{
		Type: "RuntimeError", Value: "work failed",
		Frames: []stacktrace.Frame{syntheticInApp("wrapIt", "src/work.ts", 9)},
		Chained: []stacktrace.Exception{{
			Type: "IOError", Value: "disk full",
			Frames: []stacktrace.Frame{{Function: "write", Filename: "node_modules/fs-extra/index.js", Lineno: 3, InApp: false}},
		}},
	}
	got := culpritFor(ex)
	if got == nil || got.Function != "wrapIt" || got.File != "src/work.ts" || got.Line != 9 || got.Source != "stack" {
		t.Fatalf("culpritFor = %+v, want the outer exception's own in-app crash frame", got)
	}
}

func TestCulpritNilWhenNothingIsInApp(t *testing.T) {
	ex := stacktrace.Exception{
		Type: "RuntimeError", Value: "work failed",
		Frames: []stacktrace.Frame{{Function: "wrapIt", Filename: "node_modules/lib/index.js", Lineno: 9, InApp: false}},
	}
	if got := culpritFor(ex); got != nil {
		t.Fatalf("culpritFor = %+v, want nil (nothing in the chain is in_app)", got)
	}
}

func TestBuildExceptionInfoCapsFramesAndCauses(t *testing.T) {
	frames := make([]stacktrace.Frame, 0, 20)
	for i := 0; i < 20; i++ {
		frames = append(frames, syntheticInApp("fn", "a.ts", i+1))
	}
	chained := make([]stacktrace.Exception, 0, 5)
	for i := 0; i < 5; i++ {
		chained = append(chained, stacktrace.Exception{Type: "Cause", Frames: []stacktrace.Frame{syntheticInApp("cause", "a.ts", i+1)}})
	}
	ex := stacktrace.Exception{Type: "Error", Value: "boom", Frames: frames, Chained: chained}
	info := buildExceptionInfo(ex)
	if len(info.Frames) > maxExceptionFrames {
		t.Fatalf("Frames = %d, want at most %d", len(info.Frames), maxExceptionFrames)
	}
	if len(info.Causes) > maxExceptionCauses {
		t.Fatalf("Causes = %d, want at most %d", len(info.Causes), maxExceptionCauses)
	}
	// The frames kept are the ones closest to the crash (highest line
	// numbers here), not the oldest.
	if info.Frames[len(info.Frames)-1].Lineno != 20 {
		t.Fatalf("last kept frame line = %d, want 20 (the crash frame)", info.Frames[len(info.Frames)-1].Lineno)
	}
}

// TestChainedPythonDirectCauseCulpritFallsInInnermostCause exercises the
// real internal/stacktrace/testdata/chained/py-direct-cause fixture (a
// Python "raise ... from err" direct-cause chain) through the actual parser,
// not a hand-built Exception, matching the E2.3 done-when: "python-3.14
// chained ... fixture -> culprit in the innermost cause".
func TestChainedPythonDirectCauseCulpritFallsInInnermostCause(t *testing.T) {
	ex := parseChainedFixture(t, "py-direct-cause.stderr.txt")
	if ex.Type != "RuntimeError" || len(ex.Chained) != 1 || ex.Chained[0].Type != "ValueError" {
		t.Fatalf("unexpected parse shape: outer=%s chained=%+v", ex.Type, ex.Chained)
	}
	got := culpritFor(ex)
	want := "internal/stacktrace/testdata/chained/py-direct-cause.py"
	if got == nil || got.Function != "root_cause" || got.File != want || got.Line != 2 || got.Source != "stack" {
		t.Fatalf("culpritFor = %+v, want {root_cause, %s, 2, stack} (the innermost ValueError's crash frame)", got, want)
	}
	if innermostCauseType(ex) != "ValueError" {
		t.Fatalf("innermostCauseType = %q, want ValueError", innermostCauseType(ex))
	}
}

// TestChainedNodeCauseCulpritFallsInInnermostCause is the Node `[cause]`
// counterpart, from internal/stacktrace/testdata/chained/node-cause.
func TestChainedNodeCauseCulpritFallsInInnermostCause(t *testing.T) {
	ex := parseChainedFixture(t, "node-cause.stderr.txt")
	if ex.Type != "Error" || len(ex.Chained) != 1 {
		t.Fatalf("unexpected parse shape: outer=%s chained=%+v", ex.Type, ex.Chained)
	}
	got := culpritFor(ex)
	want := "internal/stacktrace/testdata/chained/node-cause.js"
	if got == nil || got.Function != "rootCause" || got.File != want || got.Line != 3 || got.Source != "stack" {
		t.Fatalf("culpritFor = %+v, want {rootCause, %s, 3, stack} (the innermost cause's crash frame)", got, want)
	}
}

// parseChainedFixture parses internal/stacktrace/testdata/chained/<name>
// with stacktrace.Detect and applies the "/repo" git root the fixtures'
// absolute paths ("/repo/internal/stacktrace/testdata/chained/...") are
// written against, mirroring what RecordException does before hashing.
func parseChainedFixture(t *testing.T, name string) stacktrace.Exception {
	t.Helper()
	path := filepath.Join("..", "stacktrace", "testdata", "chained", name)
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	exs := stacktrace.Detect(string(data))
	if len(exs) != 1 {
		t.Fatalf("Detect(%s) = %d exceptions, want 1", path, len(exs))
	}
	stacktrace.ApplyGitRoot(exs[0], "/repo")
	return *exs[0]
}

func TestRecordExceptionBuildsFingerprintCulpritAndExceptionInfo(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	ex := parseChainedFixture(t, "py-direct-cause.stderr.txt")
	// Detect/ApplyGitRoot were already run by parseChainedFixture, exactly
	// as a caller who ran ApplyGitRoot itself would hand RecordException an
	// Exception; RecordException's own ApplyGitRoot call must be a no-op
	// (idempotent) in that case.
	id := project.Identity{Slug: "acme", Service: "web-api", GitRoot: "/repo"}
	run := contextids.IDs{RunID: "run-1", Release: "v1.2.3", GitSHA: "deadbeef"}
	observed := time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC)

	result, err := RecordException(context.Background(), path, DefaultWriterWait, ex, id, run,
		RecordExceptionOptions{ObservedAt: observed, PID: 4242})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}
	if result.Deduped {
		t.Fatal("first-ever occurrence reported Deduped")
	}
	issue := result.Issue
	if issue.FingerprintVersion != FingerprintVersionV2 {
		t.Fatalf("FingerprintVersion = %q, want %q", issue.FingerprintVersion, FingerprintVersionV2)
	}
	if issue.Project != "acme" || issue.Service != "web-api" {
		t.Fatalf("project/service = %s/%s, want acme/web-api", issue.Project, issue.Service)
	}
	if issue.Kind != KindException {
		t.Fatalf("Kind = %q, want %q", issue.Kind, KindException)
	}
	if issue.Title != "RuntimeError: work failed" {
		t.Fatalf("Title = %q, want %q", issue.Title, "RuntimeError: work failed")
	}
	if issue.Culprit == nil || issue.Culprit.Function != "root_cause" || issue.Culprit.Line != 2 {
		t.Fatalf("Issue.Culprit = %+v", issue.Culprit)
	}
	if issue.LatestException == nil || issue.LatestException.Type != "RuntimeError" || len(issue.LatestException.Causes) != 1 {
		t.Fatalf("Issue.LatestException = %+v", issue.LatestException)
	}
	if issue.LatestException.Causes[0].Type != "ValueError" || issue.LatestException.Causes[0].Culprit == nil ||
		issue.LatestException.Causes[0].Culprit.Function != "root_cause" {
		t.Fatalf("Issue.LatestException.Causes = %+v", issue.LatestException.Causes)
	}
	if issue.FirstGitSHA != "deadbeef" {
		t.Fatalf("FirstGitSHA = %q, want deadbeef", issue.FirstGitSHA)
	}
	if issue.Level != "fatal" {
		t.Fatalf("Level = %q, want fatal", issue.Level)
	}
	if issue.Handled == nil || *issue.Handled {
		t.Fatalf("Handled = %v, want false (an uncaught traceback)", issue.Handled)
	}
	if !containsFold(issue.Runs, "run-1") || !containsFold(issue.Releases, "v1.2.3") {
		t.Fatalf("Runs/Releases = %v/%v, want run-1/v1.2.3 present", issue.Runs, issue.Releases)
	}
	if result.Occurrence.Exception == nil {
		t.Fatal("first occurrence did not retain Exception detail")
	}
	if result.Occurrence.PID != 4242 || result.Occurrence.RunID != "run-1" || result.Occurrence.Release != "v1.2.3" {
		t.Fatalf("occurrence context = %+v", result.Occurrence)
	}
}

func TestRecordExceptionSameCrashShiftedLinesSameIssue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	id := project.Identity{Slug: "acme", GitRoot: "/repo"}
	run := contextids.IDs{}

	// AbsPath under gitRoot ("/repo") is what makes ApplyGitRoot mark a
	// frame in_app; a bare relative Filename with no gitRoot match would
	// not.
	first := stacktrace.Exception{
		Type: "TypeError", Value: "boom",
		Frames: []stacktrace.Frame{{Function: "handle", AbsPath: "/repo/src/app.go", Lineno: 10}},
	}
	second := first
	second.Frames = []stacktrace.Frame{{Function: "handle", AbsPath: "/repo/src/app.go", Lineno: 55}}

	r1, err := RecordException(context.Background(), path, DefaultWriterWait, first, id, run, RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("first RecordException: %v", err)
	}
	r2, err := RecordException(context.Background(), path, DefaultWriterWait, second, id, run, RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("second RecordException: %v", err)
	}
	if r1.Issue.ID != r2.Issue.ID {
		t.Fatalf("same crash with a shifted line number opened a new issue: %s vs %s", r1.Issue.ID, r2.Issue.ID)
	}
	if r2.Issue.OccurrenceCount != 2 {
		t.Fatalf("OccurrenceCount = %d, want 2", r2.Issue.OccurrenceCount)
	}
}

func TestRecordExceptionDedupeSkipsIncrement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	id := project.Identity{Slug: "acme", GitRoot: "/repo"}
	run := contextids.IDs{}
	ex := stacktrace.Exception{
		Type: "TypeError", Value: "boom",
		Frames: []stacktrace.Frame{{Function: "handle", AbsPath: "/repo/src/app.go", Lineno: 10}},
	}

	first, err := RecordException(context.Background(), path, DefaultWriterWait, ex, id, run,
		RecordExceptionOptions{ObservedAt: time.Now().UTC(), DedupeKey: "dedupe-1"})
	if err != nil {
		t.Fatalf("first RecordException: %v", err)
	}
	if first.Deduped {
		t.Fatal("first occurrence reported Deduped")
	}
	second, err := RecordException(context.Background(), path, DefaultWriterWait, ex, id, run,
		RecordExceptionOptions{ObservedAt: time.Now().UTC(), DedupeKey: "dedupe-1"})
	if err != nil {
		t.Fatalf("second RecordException: %v", err)
	}
	if !second.Deduped {
		t.Fatal("repeat with the same DedupeKey was not reported as Deduped")
	}
	if second.Issue.OccurrenceCount != 1 {
		t.Fatalf("OccurrenceCount = %d, want 1 (deduped write must not increment)", second.Issue.OccurrenceCount)
	}
	if second.Occurrence.ID != first.Occurrence.ID {
		t.Fatalf("deduped call returned a different occurrence: %s vs %s", second.Occurrence.ID, first.Occurrence.ID)
	}

	third, err := RecordException(context.Background(), path, DefaultWriterWait, ex, id, run,
		RecordExceptionOptions{ObservedAt: time.Now().UTC(), DedupeKey: "dedupe-2"})
	if err != nil {
		t.Fatalf("third RecordException: %v", err)
	}
	if third.Deduped {
		t.Fatal("a different DedupeKey was reported as Deduped")
	}
	if third.Issue.OccurrenceCount != 2 {
		t.Fatalf("OccurrenceCount = %d, want 2 (a genuinely new DedupeKey must increment)", third.Issue.OccurrenceCount)
	}
}

// TestRecordExceptionReplayDoesNotReopenResolvedIssue is the E2.3 reopen-
// rule done-when, exercised through RecordException specifically: resolve,
// then replay an OLDER observation of the same issue (as `stacktrace parse`
// would after a resolve) -- it must stay resolved. A genuinely NEW
// observation (later than ResolvedAt) must reopen it.
func TestRecordExceptionReplayDoesNotReopenResolvedIssue(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	id := project.Identity{Slug: "acme", GitRoot: "/repo"}
	run := contextids.IDs{}
	ex := stacktrace.Exception{
		Type: "TypeError", Value: "boom",
		Frames: []stacktrace.Frame{{Function: "handle", AbsPath: "/repo/src/app.go", Lineno: 10}},
	}
	old := time.Now().UTC().Add(-time.Hour)

	first, err := RecordException(context.Background(), path, DefaultWriterWait, ex, id, run, RecordExceptionOptions{ObservedAt: old})
	if err != nil {
		t.Fatalf("first RecordException: %v", err)
	}

	if err := WithWriter(context.Background(), path, DefaultWriterWait, func(store *Store) error {
		_, err := store.Resolve(first.Issue.ID)
		return err
	}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	replay, err := RecordException(context.Background(), path, DefaultWriterWait, ex, id, run,
		RecordExceptionOptions{ObservedAt: old.Add(time.Minute)})
	if err != nil {
		t.Fatalf("replay RecordException: %v", err)
	}
	if replay.Issue.Status != StatusResolved || replay.Issue.ReopenedCount != 0 {
		t.Fatalf("replaying an old observation reopened the issue: %+v", replay.Issue)
	}

	fresh, err := RecordException(context.Background(), path, DefaultWriterWait, ex, id, run,
		RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("fresh RecordException: %v", err)
	}
	if fresh.Issue.Status != StatusOpen || fresh.Issue.ReopenedCount != 1 {
		t.Fatalf("a genuinely new observation did not reopen the issue: %+v", fresh.Issue)
	}
}
