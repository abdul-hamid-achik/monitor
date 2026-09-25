package issues

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
	veclite "github.com/abdul-hamid-achik/veclite"
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

// TestCulpritFallsToInAppCallerWhenCrashFrameIsNotInApp is the fix for the
// blocker: the naming ADR's Culprit rule only falls through to nil "when
// there is no in_app frame anywhere in the chain" -- not merely when the
// literal crash frame (Frames' last element) isn't in-app. An exception
// raised inside stdlib (Python's json.loads) that in-app code called still
// has a real, actionable in-app CALLER a little further up the same
// exception's own Frames, and culpritFor must blame that caller, not nil.
func TestCulpritFallsToInAppCallerWhenCrashFrameIsNotInApp(t *testing.T) {
	ex := stacktrace.Exception{
		Type: "JSONDecodeError", Value: "Expecting value: line 1 column 1 (char 0)",
		Frames: []stacktrace.Frame{
			syntheticInApp("main", "main.py", 8),
			syntheticInApp("load_config", "main.py", 12),
			{Function: "loads", Filename: "json/__init__.py", Lineno: 346, InApp: false},
		},
	}
	got := culpritFor(ex)
	if got == nil || got.Function != "load_config" || got.File != "main.py" || got.Line != 12 || got.Source != "stack" {
		t.Fatalf("culpritFor = %+v, want the last in-app frame (load_config, main.py:12), not nil", got)
	}
}

// TestCulpritNodeENOENTFallsToInAppCallerNotDependencyCrashFrame exercises
// the real internal/stacktrace/testdata/real/node/enoent-uncaught.txt
// fixture through the actual parser: Object.readFileSync (node:fs, not
// in-app) is the literal crash frame, but readConfig (app/src/
// scenarios.mjs:24, in-app) called it directly. The old crash-frame-only
// rule returned nil here; the fixed rule must blame readConfig.
func TestCulpritNodeENOENTFallsToInAppCallerNotDependencyCrashFrame(t *testing.T) {
	ex := parseRealFixture(t, "node", "enoent-uncaught.txt")
	if ex.Type != "Error" || len(ex.Frames) == 0 {
		t.Fatalf("unexpected parse shape: %+v", ex)
	}
	if crash := ex.Frames[len(ex.Frames)-1]; crash.InApp {
		t.Fatalf("test fixture assumption broke: crash frame is now in-app: %+v", crash)
	}
	got := culpritFor(ex)
	want := "app/src/scenarios.mjs"
	if got == nil || got.Function != "readConfig" || got.File != want || got.Line != 24 || got.Source != "stack" {
		t.Fatalf("culpritFor = %+v, want {readConfig, %s, 24, stack} (the in-app caller of the ENOENT crash frame)", got, want)
	}
}

// TestBuildExceptionInfoCauseCulpritFallsToInAppCallerBelowLibraryCrashFrame
// covers buildExceptionInfo's per-cause culprit (the same bug the review
// found at exception.go:86, alongside culpritFor): a chained cause whose
// literal crash frame lives in a dependency, with an in-app caller beneath
// it, must still get a culprit in CauseInfo.
func TestBuildExceptionInfoCauseCulpritFallsToInAppCallerBelowLibraryCrashFrame(t *testing.T) {
	ex := stacktrace.Exception{
		Type: "RuntimeError", Value: "top",
		Frames: []stacktrace.Frame{syntheticInApp("run", "src/run.go", 5)},
		Chained: []stacktrace.Exception{{
			Type: "IOError", Value: "disk full",
			Frames: []stacktrace.Frame{
				syntheticInApp("writeAll", "src/writer.go", 20),
				{Function: "Write", Filename: "vendor/lib/io.go", Lineno: 42, InApp: false},
			},
		}},
	}
	info := buildExceptionInfo(ex)
	if len(info.Causes) != 1 || info.Causes[0].Culprit == nil ||
		info.Causes[0].Culprit.Function != "writeAll" || info.Causes[0].Culprit.Line != 20 {
		t.Fatalf("Causes = %+v, want the innermost cause's in-app caller (writeAll, src/writer.go:20)", info.Causes)
	}
}

// parseRealFixture parses internal/stacktrace/testdata/real/<runtime>/<name>
// with stacktrace.Detect and applies the "/repo" git root the fixture's
// absolute paths ("file:///repo/...") are written against, mirroring what
// RecordException does before hashing/culprit-picking.
func parseRealFixture(t *testing.T, runtime, name string) stacktrace.Exception {
	t.Helper()
	path := filepath.Join("..", "stacktrace", "testdata", "real", runtime, name)
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

// TestBuildExceptionInfoCausesAlwaysKeepsInnermost is the fix for the minor
// finding at exception.go:81: a chain longer than maxExceptionCauses must
// never drop its LAST (innermost) entry -- Issue.Culprit and the
// fingerprint's innermost.Type both come from it, and the rendered causes
// would otherwise be unable to show the line the culprit points into.
func TestBuildExceptionInfoCausesAlwaysKeepsInnermost(t *testing.T) {
	chained := make([]stacktrace.Exception, 0, 5)
	for i := 0; i < 5; i++ {
		chained = append(chained, stacktrace.Exception{Type: fmt.Sprintf("Cause%d", i)})
	}
	ex := stacktrace.Exception{Type: "Outer", Value: "boom", Chained: chained}
	info := buildExceptionInfo(ex)
	if len(info.Causes) != maxExceptionCauses {
		t.Fatalf("Causes = %d, want %d", len(info.Causes), maxExceptionCauses)
	}
	if last := info.Causes[len(info.Causes)-1]; last.Type != "Cause4" {
		t.Fatalf("innermost cause was dropped: Causes = %+v, want the last entry to be Cause4 (the innermost)", info.Causes)
	}
	if first := info.Causes[0]; first.Type != "Cause0" {
		t.Fatalf("outermost cause was dropped: Causes = %+v, want the first entry to be Cause0", info.Causes)
	}
}

// TestBuildExceptionInfoCapsSizeAndTruncatesValueBeforeDroppingFrames is the
// fix for the blocker-adjacent major finding at exception.go:100: the ~2 KB
// cap was never actually enforced (Value was never truncated, so a long
// message alone could blow past maxExceptionInfoBytes with zero frames
// kept). Value must be truncated FIRST; frames -- the actually actionable
// part of the budget -- must survive a pathologically long message.
func TestBuildExceptionInfoCapsSizeAndTruncatesValueBeforeDroppingFrames(t *testing.T) {
	ex := stacktrace.Exception{
		Type: "ValueError", Value: strings.Repeat("x", 10_000),
		Frames: []stacktrace.Frame{
			syntheticInApp("a", "src/a.go", 1),
			syntheticInApp("b", "src/b.go", 2),
			syntheticInApp("crash", "src/crash.go", 3),
		},
	}
	info := buildExceptionInfo(ex)
	data, err := json.Marshal(info)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > maxExceptionInfoBytes {
		t.Fatalf("serialized ExceptionInfo = %d bytes, want <= %d", len(data), maxExceptionInfoBytes)
	}
	if len(info.Frames) == 0 {
		t.Fatal("frames were dropped to satisfy the size cap instead of truncating Value first")
	}
	if len(info.Value) > maxExceptionValueBytes {
		t.Fatalf("Value = %d bytes, want <= %d (truncated first)", len(info.Value), maxExceptionValueBytes)
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

// TestRecordExceptionRubyHandledFramesOnlyEventRecords is the fix for the
// major finding at exception.go:205: a Ruby rescue-printed backtrace
// (`warn e.backtrace`) parses with in-app Frames but empty Type/Value (see
// ruby.go's rubyBacktraceGrammar). The golden-table
// internal/stacktrace/testdata/dogfood/ruby.stderr.txt fixture has 10 of
// these plus 1 fatal RuntimeError. Before the fix, every one of the 10
// handled events failed validateOccurrenceInput outright ("message,
// exception type, or symbol is required"), so `monitor run --` and
// `stacktrace parse --record` would never record a handled Ruby error.
func TestRecordExceptionRubyHandledFramesOnlyEventRecords(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	data, err := os.ReadFile(filepath.Join("..", "stacktrace", "testdata", "dogfood", "ruby.stderr.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	exs := stacktrace.Detect(string(data))
	if len(exs) != 11 {
		t.Fatalf("Detect(ruby.stderr.txt) = %d exceptions, want 11 (10 handled + 1 fatal)", len(exs))
	}
	id := project.Identity{Slug: "acme", GitRoot: "/repo"}
	run := contextids.IDs{}

	var handledIssueID string
	handledCount := 0
	for i, ex := range exs {
		result, err := RecordException(context.Background(), path, DefaultWriterWait, *ex, id, run,
			RecordExceptionOptions{ObservedAt: time.Now().UTC()})
		if err != nil {
			t.Fatalf("RecordException[%d] (Type=%q Value=%q): %v", i, ex.Type, ex.Value, err)
		}
		if ex.Type != "" || ex.Value != "" {
			continue // the one fatal RuntimeError -- not this test's concern
		}
		handledCount++
		if len(result.Occurrence.Symbols) == 0 {
			t.Fatalf("RecordException[%d]: Symbols empty for a Type/Value-less event", i)
		}
		if result.Issue.Title == "" {
			t.Fatalf("RecordException[%d]: Title empty for a Type/Value-less event", i)
		}
		if handledIssueID == "" {
			handledIssueID = result.Issue.ID
		} else if result.Issue.ID != handledIssueID {
			t.Fatalf("RecordException[%d]: identical handled events opened a new issue: %s vs %s", i, result.Issue.ID, handledIssueID)
		}
	}
	if handledCount != 10 {
		t.Fatalf("handled (Type/Value-less) events recorded = %d, want 10", handledCount)
	}

	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	got, err := reader.Get(handledIssueID)
	if err != nil {
		t.Fatalf("Get handled issue: %v", err)
	}
	if got.OccurrenceCount != 10 {
		t.Fatalf("handled issue OccurrenceCount = %d, want 10 (all 10 identical events grouped)", got.OccurrenceCount)
	}
}

// TestRecordExceptionDoesNotMutateCallersException is the fix for the minor
// finding at exception.go:186: RecordException took ex by value but
// ApplyGitRoot mutated its Frames/Chained backing arrays in place (a slice
// header copy still aliases the caller's array). A caller that keeps its
// own reference to the Exception it handed RecordException must see it
// unchanged afterward.
func TestRecordExceptionDoesNotMutateCallersException(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	id := project.Identity{Slug: "acme", GitRoot: "/repo"}
	run := contextids.IDs{}
	ex := stacktrace.Exception{
		Type: "TypeError", Value: "boom",
		Frames: []stacktrace.Frame{{Function: "handle", AbsPath: "/repo/src/app.go", Lineno: 10}},
		Chained: []stacktrace.Exception{{
			Type: "IOError", Frames: []stacktrace.Frame{{Function: "read", AbsPath: "/repo/src/io.go", Lineno: 4}},
		}},
	}
	if ex.Frames[0].InApp || ex.Chained[0].Frames[0].InApp {
		t.Fatal("test setup: frames should not start InApp")
	}
	if _, err := RecordException(context.Background(), path, DefaultWriterWait, ex, id, run, RecordExceptionOptions{ObservedAt: time.Now().UTC()}); err != nil {
		t.Fatalf("RecordException: %v", err)
	}
	if ex.Frames[0].InApp || ex.Frames[0].Filename != "" {
		t.Fatalf("RecordException mutated the caller's outer Frames in place: %+v", ex.Frames[0])
	}
	if ex.Chained[0].Frames[0].InApp || ex.Chained[0].Frames[0].Filename != "" {
		t.Fatalf("RecordException mutated the caller's Chained Frames in place: %+v", ex.Chained[0].Frames[0])
	}
}

// TestSummarizeForListTrimsExceptionDetailButKeepsCulprit is the fix for
// the minor payload-diet finding: a list surface (`issues list --json`,
// MCP's monitor_issues) must not pay LatestException's full frame/cause
// budget per row, but a trimmed row should still name a culprit.
func TestSummarizeForListTrimsExceptionDetailButKeepsCulprit(t *testing.T) {
	issue := Issue{
		ID:      "ISS-1",
		Culprit: &Culprit{Function: "loadUser", File: "src/users.ts", Line: 42, Source: "stack"},
		LatestException: &ExceptionInfo{
			Type: "TypeError", Value: "boom", Runtime: "node",
			Frames: []stacktrace.Frame{syntheticInApp("loadUser", "src/users.ts", 42)},
			Causes: []CauseInfo{{Type: "Cause"}},
		},
	}
	got := SummarizeForList(issue)
	if got.Culprit == nil || got.Culprit.Function != "loadUser" {
		t.Fatalf("Culprit = %+v, want it preserved", got.Culprit)
	}
	if got.LatestException == nil || got.LatestException.Type != "TypeError" || got.LatestException.Value != "boom" {
		t.Fatalf("LatestException summary = %+v, want Type/Value preserved", got.LatestException)
	}
	if len(got.LatestException.Frames) != 0 || len(got.LatestException.Causes) != 0 {
		t.Fatalf("LatestException = %+v, want Frames/Causes trimmed", got.LatestException)
	}
	// The original issue's LatestException must not be mutated in place --
	// SummarizeForList returns a copy, not an alias.
	if len(issue.LatestException.Frames) != 1 {
		t.Fatalf("SummarizeForList mutated the caller's Issue in place: %+v", issue.LatestException)
	}
	if got := SummarizeForList(Issue{ID: "ISS-2"}); got.LatestException != nil {
		t.Fatalf("SummarizeForList on a nil LatestException = %+v, want nil", got.LatestException)
	}
}

// TestBuildExceptionInfoCountsDroppedFramesAndCauses (CC-4): the caps'
// cuts must be COUNTED, not just performed -- a reader rendering "12
// frames kept" must be able to say "of 20", and the same for a chain
// longer than maxExceptionCauses.
func TestBuildExceptionInfoCountsDroppedFramesAndCauses(t *testing.T) {
	frames := make([]stacktrace.Frame, 0, 20)
	for i := range 20 {
		frames = append(frames, syntheticInApp(fmt.Sprintf("fn%d", i), "a.ts", i+1))
	}
	chained := make([]stacktrace.Exception, 0, 5)
	for i := range 5 {
		chained = append(chained, stacktrace.Exception{
			Type:   fmt.Sprintf("Cause%d", i),
			Frames: []stacktrace.Frame{syntheticInApp("cause", "a.ts", i+1)},
		})
	}
	ex := stacktrace.Exception{Type: "Error", Value: "boom", Frames: frames, Chained: chained}
	info := buildExceptionInfo(ex)
	if len(info.Frames) != maxExceptionFrames {
		t.Fatalf("Frames = %d, want %d", len(info.Frames), maxExceptionFrames)
	}
	if want := 20 - maxExceptionFrames; info.DroppedFrames != want {
		t.Fatalf("DroppedFrames = %d, want %d (20 in-app frames, %d kept)", info.DroppedFrames, want, maxExceptionFrames)
	}
	if len(info.Causes) != maxExceptionCauses {
		t.Fatalf("Causes = %d, want %d", len(info.Causes), maxExceptionCauses)
	}
	if want := 5 - maxExceptionCauses; info.DroppedCauses != want {
		t.Fatalf("DroppedCauses = %d, want %d (5 chained causes, %d kept)", info.DroppedCauses, want, maxExceptionCauses)
	}
	// Non-in-app frames are filtered by design, never "dropped": they
	// were never candidates for the stored list.
	mixed := stacktrace.Exception{Frames: []stacktrace.Frame{
		syntheticInApp("a", "a.ts", 1),
		{Function: "dep", Filename: "node_modules/x.js", Lineno: 2, InApp: false},
	}}
	if info := buildExceptionInfo(mixed); info.DroppedFrames != 0 {
		t.Fatalf("DroppedFrames = %d, want 0 (a non-in-app frame is filtered, not dropped)", info.DroppedFrames)
	}
}

// TestBuildExceptionInfoCountsSizeBackstopCuts (CC-4): frames and causes
// removed by truncateExceptionInfo's ~2 KB backstop must be counted too,
// so everything cut at ingest is accounted for exactly once.
func TestBuildExceptionInfoCountsSizeBackstopCuts(t *testing.T) {
	frames := make([]stacktrace.Frame, 0, 20)
	for i := range 20 {
		frames = append(frames, stacktrace.Frame{
			Function: fmt.Sprintf("fn%02d-%s", i, strings.Repeat("f", 180)),
			Filename: "a.ts", Lineno: i + 1, InApp: true,
		})
	}
	ex := stacktrace.Exception{Type: "Error", Value: "boom", Frames: frames}
	info := buildExceptionInfo(ex)
	if len(info.Frames) == maxExceptionFrames {
		t.Fatal("precondition failed: the size backstop did not run (frames still at the cap)")
	}
	if want := 20 - len(info.Frames); info.DroppedFrames != want {
		t.Fatalf("DroppedFrames = %d, want %d (cap cut + backstop cut, %d kept)", info.DroppedFrames, want, len(info.Frames))
	}

	// Causes-only variant: no frames, so the backstop drops causes.
	chained := make([]stacktrace.Exception, 0, 3)
	for range 3 {
		chained = append(chained, stacktrace.Exception{Type: strings.Repeat("c", 700)})
	}
	info = buildExceptionInfo(stacktrace.Exception{Type: "Error", Value: "boom", Chained: chained})
	if len(info.Causes) == len(chained) {
		t.Fatal("precondition failed: the size backstop did not run (all causes kept)")
	}
	if want := 3 - len(info.Causes); info.DroppedCauses != want {
		t.Fatalf("DroppedCauses = %d, want %d (%d kept)", info.DroppedCauses, want, len(info.Causes))
	}
}

// TestExceptionInfoDroppedFieldsDecodeAdditively (CC-4): an old record
// without the new fields decodes as 0, and a zero value is omitted from
// the encoding -- the JSON change is additive in both directions.
func TestExceptionInfoDroppedFieldsDecodeAdditively(t *testing.T) {
	var info ExceptionInfo
	if err := json.Unmarshal([]byte(`{"type":"TypeError","value":"boom"}`), &info); err != nil {
		t.Fatal(err)
	}
	if info.DroppedFrames != 0 || info.DroppedCauses != 0 {
		t.Fatalf("old record decoded DroppedFrames/DroppedCauses = %d/%d, want 0/0", info.DroppedFrames, info.DroppedCauses)
	}
	data, err := json.Marshal(ExceptionInfo{Type: "TypeError"})
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("dropped_")) {
		t.Fatalf("zero dropped counts must be omitted: %s", data)
	}
	data, err = json.Marshal(ExceptionInfo{DroppedFrames: 9, DroppedCauses: 2})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"dropped_frames":9`)) || !bytes.Contains(data, []byte(`"dropped_causes":2`)) {
		t.Fatalf("dropped counts must round-trip under their documented key names: %s", data)
	}
}

// stripIssueLikeOldBinary re-saves issue the way a v1.15 binary does:
// decode into the old struct (which drops the additive fields), marshal,
// update the document. This is main's store.go saveIssue path verbatim
// and is how CC-3's data loss actually happens in the wild.
func stripIssueLikeOldBinary(t *testing.T, store *Store, issue Issue) {
	t.Helper()
	record, err := store.db.Collection(issuesCollection).FindOne(veclite.Equal("fingerprint", issue.Fingerprint))
	if err != nil {
		t.Fatalf("find issue record: %v", err)
	}
	stripped := issue
	stripped.Culprit = nil
	stripped.LatestException = nil
	stripped.Handled = nil
	if err := store.saveIssue(record, stripped); err != nil {
		t.Fatalf("re-save stripped issue: %v", err)
	}
}

// recordChainedForRebuild records one chained exception whose innermost
// cause owns the culprit (src/users.ts:3), distinct from the outer
// frame (src/run.ts:5), so a rebuild that picks the wrong frame is
// detectable.
func recordChainedForRebuild(t *testing.T, path string) Issue {
	t.Helper()
	handled := true
	ex := stacktrace.Exception{
		Type: "RuntimeError", Value: "work failed", Runtime: "node", Level: "error", Handled: &handled,
		Frames: []stacktrace.Frame{syntheticInApp("run", "src/run.ts", 5)},
		Chained: []stacktrace.Exception{{
			Type: "TypeError", Value: "inner",
			Frames: []stacktrace.Frame{syntheticInApp("loadUser", "src/users.ts", 3)},
		}},
	}
	id := project.Identity{Slug: "acme", GitRoot: "/repo"}
	res, err := RecordException(context.Background(), path, DefaultWriterWait, ex, id, contextids.IDs{},
		RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}
	return res.Issue
}

// TestRebuildFromFirstOccurrenceRestoresStrippedRecord (CC-3): after an
// older binary rewrites the issue record without the additive fields, the
// rebuild must restore LatestException, re-derive the Culprit with the
// innermost-cause rule, and fill Handled -- from the FIRST occurrence,
// the only row that still carries Exception detail.
func TestRebuildFromFirstOccurrenceRestoresStrippedRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	issue := recordChainedForRebuild(t, path)

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stripIssueLikeOldBinary(t, store, issue)

	stripped, err := store.Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stripped.Culprit != nil || stripped.LatestException != nil || stripped.Handled != nil {
		t.Fatalf("precondition failed: the strip did not remove the additive fields: %+v", stripped)
	}

	rebuilt, ok := RebuildFromFirstOccurrence(store, stripped)
	if !ok {
		t.Fatal("RebuildFromFirstOccurrence reported no rebuild for a stripped exception issue")
	}
	if rebuilt.LatestException == nil || rebuilt.LatestException.Type != "RuntimeError" {
		t.Fatalf("LatestException = %+v, want the first occurrence's detail", rebuilt.LatestException)
	}
	// The innermost-cause rule: src/users.ts:3 (the cause's frame), not
	// the outer src/run.ts:5.
	if rebuilt.Culprit == nil || rebuilt.Culprit.File != "src/users.ts" || rebuilt.Culprit.Line != 3 || rebuilt.Culprit.Source != "stack" {
		t.Fatalf("Culprit = %+v, want src/users.ts:3 via the innermost-cause rule", rebuilt.Culprit)
	}
	if rebuilt.Handled == nil || !*rebuilt.Handled {
		t.Fatalf("Handled = %v, want true from ExceptionInfo.Handled", rebuilt.Handled)
	}
}

// TestRebuildFromFirstOccurrenceGuards (CC-3): a nil store, an intact
// issue, and a non-exception kind must never trigger a rebuild, and data
// that survived the rewrite must never be overwritten.
func TestRebuildFromFirstOccurrenceGuards(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	issue := recordChainedForRebuild(t, path)

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	if _, ok := RebuildFromFirstOccurrence(nil, issue); ok {
		t.Fatal("nil store must not rebuild")
	}
	if _, ok := RebuildFromFirstOccurrence(store, issue); ok {
		t.Fatal("an issue that still carries LatestException must not rebuild")
	}
	alert := Issue{ID: "ISS-ALERT", Kind: "monitor.alert.disk_fill"}
	if _, ok := RebuildFromFirstOccurrence(store, alert); ok {
		t.Fatal("a non-exception kind must not rebuild")
	}

	// A stripped issue whose Culprit survived (a partial rewrite) keeps
	// its own Culprit; only the missing fields are filled.
	stripIssueLikeOldBinary(t, store, issue)
	stripped, err := store.Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}
	stripped.Culprit = &Culprit{Function: "survived", File: "src/kept.go", Line: 9, Source: "stack"}
	rebuilt, ok := RebuildFromFirstOccurrence(store, stripped)
	if !ok {
		t.Fatal("expected a rebuild for the stripped exception issue")
	}
	if rebuilt.Culprit.File != "src/kept.go" {
		t.Fatalf("Culprit = %+v, want the surviving record's own culprit, never overwritten", rebuilt.Culprit)
	}
}

// TestSummarizeForListRebuildsStrippedRow (CC-3): a list surface holding
// the store gets the rebuilt culprit and trimmed exception summary; the
// store-less historical call keeps returning the stripped row untouched.
func TestSummarizeForListRebuildsStrippedRow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	issue := recordChainedForRebuild(t, path)

	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	stripIssueLikeOldBinary(t, store, issue)
	stripped, err := store.Get(issue.ID)
	if err != nil {
		t.Fatal(err)
	}

	got := SummarizeForList(stripped, store)
	if got.Culprit == nil || got.Culprit.File != "src/users.ts" {
		t.Fatalf("Culprit = %+v, want the rebuilt innermost-cause culprit", got.Culprit)
	}
	if got.LatestException == nil || got.LatestException.Type != "RuntimeError" || len(got.LatestException.Frames) != 0 {
		t.Fatalf("LatestException = %+v, want a rebuilt but trimmed summary", got.LatestException)
	}

	plain := SummarizeForList(stripped)
	if plain.Culprit != nil || plain.LatestException != nil {
		t.Fatalf("store-less SummarizeForList = %+v, want the historical untouched row", plain)
	}
}
