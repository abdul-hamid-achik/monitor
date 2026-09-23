package issues

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestFingerprintV1StableAndExcludesOccurrenceContext(t *testing.T) {
	first := FingerprintV1(FingerprintInput{
		Project: " Checkout ", Service: "API", Kind: "Exception",
		ExceptionType: "TimeoutError", Message: "request 123 failed for 550e8400-e29b-41d4-a716-446655440000",
		Symbols: []string{" pkg.Handler ", "pkg.Fetch"},
	})
	second := FingerprintV1(FingerprintInput{
		Project: "checkout", Service: "api", Kind: "exception",
		ExceptionType: "timeouterror", Message: "request 999 failed for a3bb189e-8bf9-3888-9912-ace4e6543002",
		Symbols: []string{"pkg.Fetch", "pkg.Handler", "pkg.Handler"},
	})
	if first != second {
		t.Fatalf("dynamic values or symbol order changed fingerprint:\n%s\n%s", first, second)
	}
	if first == FingerprintV1(FingerprintInput{Project: "other", Message: "request 999 failed for a3bb189e-8bf9-3888-9912-ace4e6543002"}) {
		t.Fatal("different stable identity produced same fingerprint")
	}

	// Occurrence-only fields cannot leak into FingerprintInput. Verify two full
	// inputs with different run/release/PID/artifact context still group.
	a := OccurrenceInput{Project: "p", Service: "svc", Message: "failed item 10", RunID: "run-a", Release: "a", PID: 1, TreeHash: "tree-a"}
	b := OccurrenceInput{Project: "p", Service: "svc", Message: "failed item 20", RunID: "run-b", Release: "b", PID: 999, TreeHash: "tree-b"}
	if fingerprintForTest(a) != fingerprintForTest(b) {
		t.Fatal("run, release, PID, or tree hash affected fingerprint")
	}
}

func TestUpsertGroupsOccurrencesAndPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	store := openTestStore(t, path)
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.FixedZone("test", -6*60*60))

	first, firstOccurrence, err := store.UpsertOccurrence(OccurrenceInput{
		ObservedAt: base, Project: "monitor", Service: "cli", Kind: "error",
		Title: "request failed", Message: "request 101 failed", ExceptionType: "IOError",
		Symbols: []string{"main.run"}, Severity: "ERROR", RunID: "run-1", PID: 12,
		EvidenceRefs: []string{"artifact:first"},
	})
	if err != nil {
		t.Fatalf("first UpsertOccurrence: %v", err)
	}
	second, secondOccurrence, err := store.UpsertOccurrence(OccurrenceInput{
		ObservedAt: base.Add(time.Minute), Project: "monitor", Service: "cli", Kind: "error",
		Title: "request failed again", Message: "request 202 failed", ExceptionType: "IOError",
		Symbols: []string{"main.run"}, Severity: "error", RunID: "run-2", PID: 99,
	})
	if err != nil {
		t.Fatalf("second UpsertOccurrence: %v", err)
	}
	if first.ID != second.ID || firstOccurrence.IssueID != secondOccurrence.IssueID {
		t.Fatalf("occurrences did not group: first=%s second=%s", first.ID, second.ID)
	}
	if second.OccurrenceCount != 2 || !second.FirstSeen.Equal(base.UTC()) || !second.LastSeen.Equal(base.Add(time.Minute).UTC()) {
		t.Fatalf("group aggregate = %+v", second)
	}
	if secondOccurrence.RunID != "run-2" || secondOccurrence.PID != 99 {
		t.Fatalf("occurrence context was not retained: %+v", secondOccurrence)
	}

	if err := store.Close(); err != nil {
		t.Fatalf("Close writer: %v", err)
	}
	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	got, err := reader.Get(first.ID)
	if err != nil {
		t.Fatalf("Get persisted issue: %v", err)
	}
	if got.OccurrenceCount != 2 || got.FingerprintVersion != FingerprintVersionV1 {
		t.Fatalf("persisted issue = %+v", got)
	}
	occurrences, err := reader.Occurrences(first.ID, 0)
	if err != nil {
		t.Fatalf("Occurrences: %v", err)
	}
	if len(occurrences) != 2 || occurrences[0].RunID != "run-2" {
		t.Fatalf("persisted occurrences = %+v", occurrences)
	}
	if _, _, err := reader.UpsertOccurrence(OccurrenceInput{Project: "p", Message: "failure"}); !errors.Is(err, ErrReadOnly) {
		t.Fatalf("read-only mutation error = %v, want ErrReadOnly", err)
	}
}

// TestUpsertOccurrenceCountCoalescesBursts is the E1.2 done-when for
// OccurrenceInput.Count: a coalesced burst writes ONE occurrence row whose
// Count subsumes every raw event, the issue's cumulative OccurrenceCount
// reflects the sum of Counts across every occurrence (not the number of
// UpsertOccurrence calls), and Count<=0 (unset, or an accidental negative)
// defaults to 1 rather than leaving the cumulative count unchanged.
func TestUpsertOccurrenceCountCoalescesBursts(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	store := openTestStore(t, path)

	issue, occurrence, err := store.UpsertOccurrence(OccurrenceInput{
		Project: "p", Message: "burst", Count: 5,
	})
	if err != nil {
		t.Fatalf("UpsertOccurrence: %v", err)
	}
	if occurrence.Count != 5 {
		t.Fatalf("occurrence.Count = %d, want 5", occurrence.Count)
	}
	if issue.OccurrenceCount != 5 {
		t.Fatalf("issue.OccurrenceCount = %d, want 5", issue.OccurrenceCount)
	}

	issue, occurrence, err = store.UpsertOccurrence(OccurrenceInput{
		Project: "p", Message: "burst", Count: -3,
	})
	if err != nil {
		t.Fatalf("second UpsertOccurrence: %v", err)
	}
	if occurrence.Count != 1 {
		t.Fatalf("occurrence.Count = %d, want 1 (Count<=0 defaults to 1)", occurrence.Count)
	}
	if issue.OccurrenceCount != 6 {
		t.Fatalf("issue.OccurrenceCount = %d, want 6 (5 + 1)", issue.OccurrenceCount)
	}

	stored, err := store.Occurrences(issue.ID, 10)
	if err != nil {
		t.Fatalf("Occurrences: %v", err)
	}
	if len(stored) != 2 {
		t.Fatalf("stored occurrences = %d, want 2", len(stored))
	}
	var total int64
	for _, occ := range stored {
		total += occ.Count
	}
	if total != issue.OccurrenceCount {
		t.Fatalf("sum of retained occurrence Counts = %d, want issue.OccurrenceCount %d", total, issue.OccurrenceCount)
	}
}

// TestNormalizeOccurrenceSlicesDefaultsLegacyCountToOne verifies a record
// persisted before Count existed (JSON zero value on decode) normalizes to
// Count=1, not 0: every occurrence represents at least one raw event, and a
// caller dividing or comparing by Count must never see zero.
func TestNormalizeOccurrenceSlicesDefaultsLegacyCountToOne(t *testing.T) {
	occurrence := Occurrence{ID: "OCC-LEGACY"}
	normalizeOccurrenceSlices(&occurrence)
	if occurrence.Count != 1 {
		t.Fatalf("Count = %d, want 1 for a legacy record with no Count field", occurrence.Count)
	}
}

func TestRunEventIssueEvidenceRoundTripAndDoNotAffectGrouping(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	store := openTestStore(t, path)
	base := time.Date(2026, 7, 27, 18, 0, 0, 0, time.UTC)
	firstRun := &RunContext{
		ID: "run-1", Environment: "preview", DeploymentID: "dep-1",
		StepID: "test", Suite: "pull-request", Attempt: "1",
		Release: "v1.15.0", GitSHA: "abc123",
	}
	firstEvidence := []EvidenceRef{{
		Kind: "monitor.incident", URI: "fcheap://stash/stash-1",
		TreeHash: strings.Repeat("a", 64),
	}}
	issue, event, err := store.UpsertOccurrence(OccurrenceInput{
		ObservedAt: base, Project: "chalupa", Service: "api", Kind: "exception",
		Message: "request 101 failed", RunID: firstRun.ID, Release: firstRun.Release,
		PID: 101, Run: firstRun, Evidence: firstEvidence,
	})
	if err != nil {
		t.Fatalf("first event: %v", err)
	}
	if event.Run == nil || event.Run.StepID != "test" || len(event.Evidence) != 1 ||
		event.Evidence[0].URI != "fcheap://stash/stash-1" {
		t.Fatalf("event context = %+v", event)
	}

	// Mutating caller-owned input after the write must not mutate the event
	// returned by the store or the durable copy.
	firstRun.StepID = "mutated"
	firstEvidence[0].URI = "fcheap://stash/mutated"
	if event.Run.StepID != "test" || event.Evidence[0].URI != "fcheap://stash/stash-1" {
		t.Fatalf("event retained caller aliases: %+v", event)
	}

	second, _, err := store.UpsertOccurrence(OccurrenceInput{
		ObservedAt: base.Add(time.Minute), Project: "chalupa", Service: "api", Kind: "exception",
		Message: "request 202 failed", RunID: "run-2", Release: "v1.15.1", PID: 202,
		Run:      &RunContext{ID: "run-2", StepID: "deploy", Attempt: "2"},
		Evidence: []EvidenceRef{{Kind: "monitor.incident.pending", URI: "monitor://incidents/deadbeef0000"}},
	})
	if err != nil {
		t.Fatalf("second event: %v", err)
	}
	if second.ID != issue.ID || second.OccurrenceCount != 2 {
		t.Fatalf("run/evidence split grouping: first=%+v second=%+v", issue, second)
	}

	occurrences, err := store.Occurrences(issue.ID, 10)
	if err != nil {
		t.Fatalf("Occurrences: %v", err)
	}
	if len(occurrences) != 2 || occurrences[0].Run == nil || occurrences[0].Run.ID != "run-2" ||
		len(occurrences[1].Evidence) != 1 || occurrences[1].Evidence[0].URI != "fcheap://stash/stash-1" {
		t.Fatalf("durable events = %+v", occurrences)
	}
	data, err := json.Marshal(occurrences)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`"run"`, `"evidence"`, `"deployment_id"`, `"tree_hash"`} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("event JSON %s missing %s", data, want)
		}
	}
}

func TestResolvePathPrecedenceIsolatedFromHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	envPath := filepath.Join(t.TempDir(), "env-issues.veclite")
	t.Setenv(StorePathEnv, envPath)

	if got, err := ResolvePath(""); err != nil || got != envPath {
		t.Fatalf("env path = (%q, %v), want %q", got, err, envPath)
	}
	explicit := filepath.Join(t.TempDir(), "explicit-issues.veclite")
	if got, err := ResolvePath(explicit); err != nil || got != explicit {
		t.Fatalf("explicit path = (%q, %v), want %q", got, err, explicit)
	}
	t.Setenv(StorePathEnv, "")
	got, err := ResolvePath("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".local", "share", "monitor", "issues.veclite")
	if got != want {
		t.Fatalf("default path = %q, want %q", got, want)
	}
}

func TestResolvedIssueReopensOnNewRegression(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	issue, _, err := store.UpsertOccurrence(OccurrenceInput{Project: "p", Message: "boom"})
	if err != nil {
		t.Fatalf("UpsertOccurrence: %v", err)
	}
	resolved, err := store.Resolve(issue.ID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Status != StatusResolved || resolved.ResolvedAt == nil {
		t.Fatalf("resolved issue = %+v", resolved)
	}

	reopened, _, err := store.UpsertOccurrence(OccurrenceInput{Project: "p", Message: "boom"})
	if err != nil {
		t.Fatalf("regression UpsertOccurrence: %v", err)
	}
	if reopened.Status != StatusOpen || reopened.ResolvedAt != nil || reopened.ReopenedCount != 1 || reopened.OccurrenceCount != 2 {
		t.Fatalf("reopened issue = %+v", reopened)
	}

	reopenedAgain, err := store.Reopen(issue.ID)
	if err != nil {
		t.Fatalf("idempotent Reopen: %v", err)
	}
	if reopenedAgain.ReopenedCount != 1 {
		t.Fatalf("explicit idempotent reopen changed regression count: %+v", reopenedAgain)
	}
}

func TestOutOfOrderOccurrenceDoesNotReopenResolvedIssue(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	old := time.Now().UTC().Add(-time.Hour)
	issue, _, err := store.UpsertOccurrence(OccurrenceInput{ObservedAt: old, Project: "p", Message: "boom"})
	if err != nil {
		t.Fatalf("UpsertOccurrence: %v", err)
	}
	if _, err := store.Resolve(issue.ID); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	got, _, err := store.UpsertOccurrence(OccurrenceInput{ObservedAt: old.Add(time.Minute), Project: "p", Message: "boom"})
	if err != nil {
		t.Fatalf("out-of-order UpsertOccurrence: %v", err)
	}
	if got.Status != StatusResolved || got.ReopenedCount != 0 {
		t.Fatalf("old occurrence reopened issue: %+v", got)
	}
}

func TestIgnoredIssueStaysIgnoredUntilExplicitReopen(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	issue, _, err := store.UpsertOccurrence(OccurrenceInput{Project: "p", Message: "boom"})
	if err != nil {
		t.Fatalf("UpsertOccurrence: %v", err)
	}
	ignored, err := store.Ignore(issue.ID)
	if err != nil {
		t.Fatalf("Ignore: %v", err)
	}
	if ignored.Status != StatusIgnored {
		t.Fatalf("status = %q, want ignored", ignored.Status)
	}
	ignored, _, err = store.UpsertOccurrence(OccurrenceInput{Project: "p", Message: "boom"})
	if err != nil {
		t.Fatalf("UpsertOccurrence ignored: %v", err)
	}
	if ignored.Status != StatusIgnored || ignored.OccurrenceCount != 2 {
		t.Fatalf("ignored issue after occurrence = %+v", ignored)
	}
	opened, err := store.Reopen(issue.ID)
	if err != nil || opened.Status != StatusOpen {
		t.Fatalf("Reopen = (%+v, %v)", opened, err)
	}
}

func TestConcurrentUpsertIsAtomicWithinStore(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	const count = 64
	var wg sync.WaitGroup
	errs := make(chan error, count)
	ids := make(chan string, count)
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			issue, _, err := store.UpsertOccurrence(OccurrenceInput{
				Project: "p", Service: "api", Message: fmt.Sprintf("request %d failed", i),
				RunID: fmt.Sprintf("run-%d", i), PID: int32(i + 1),
			})
			if err != nil {
				errs <- err
				return
			}
			ids <- issue.ID
		}(i)
	}
	wg.Wait()
	close(errs)
	close(ids)
	for err := range errs {
		t.Errorf("concurrent UpsertOccurrence: %v", err)
	}
	var issueID string
	for id := range ids {
		if issueID == "" {
			issueID = id
		}
		if id != issueID {
			t.Errorf("concurrent upsert created issue %s, want %s", id, issueID)
		}
	}
	issue, err := store.Get(issueID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if issue.OccurrenceCount != count {
		t.Fatalf("occurrence_count = %d, want %d", issue.OccurrenceCount, count)
	}
	occurrences, err := store.Occurrences(issueID, 0)
	if err != nil {
		t.Fatalf("Occurrences: %v", err)
	}
	if len(occurrences) != count {
		t.Fatalf("occurrences = %d, want %d", len(occurrences), count)
	}
}

// TestListValidatesFiltersEvenOnAnEmptyStore is a regression test: an
// invalid --status/--kind must be rejected the same way against a store
// whose issues collection does not exist yet (nothing has ever been
// written) as against a populated one, not silently accepted just because
// List's early "nothing to filter" path would otherwise skip validation.
func TestListValidatesFiltersEvenOnAnEmptyStore(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	if _, err := store.List(ListOptions{Kind: "bogus"}); err == nil {
		t.Fatal("invalid Kind on an empty store did not return an error")
	}
	if _, err := store.List(ListOptions{Statuses: []Status{"bogus"}}); err == nil {
		t.Fatal("invalid Statuses on an empty store did not return an error")
	}
	got, err := store.List(ListOptions{Kind: "any"})
	if err != nil || len(got) != 0 {
		t.Fatalf("valid filters on an empty store = %+v, %v", got, err)
	}
}

func TestListFiltersSortsLimitsAndUsesNonNilEmptySlices(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	empty, err := store.List(ListOptions{})
	if err != nil {
		t.Fatalf("empty List: %v", err)
	}
	assertJSONArray(t, empty, "[]")
	missing, err := store.Occurrences("ISS-MISSING", 10)
	if err != nil {
		t.Fatalf("empty Occurrences: %v", err)
	}
	assertJSONArray(t, missing, "[]")

	base := time.Now().UTC().Add(-time.Hour)
	old, _, _ := store.UpsertOccurrence(OccurrenceInput{ObservedAt: base, Project: "alpha", Service: "api", Message: "old"})
	newest, _, _ := store.UpsertOccurrence(OccurrenceInput{ObservedAt: base.Add(time.Minute), Project: "alpha", Service: "worker", Message: "newest"})
	other, _, _ := store.UpsertOccurrence(OccurrenceInput{ObservedAt: base.Add(30 * time.Second), Project: "beta", Service: "api", Message: "other"})
	if _, err := store.Resolve(other.ID); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	got, err := store.List(ListOptions{Project: "ALPHA", Statuses: []Status{StatusOpen}, Limit: 1})
	if err != nil {
		t.Fatalf("filtered List: %v", err)
	}
	if len(got) != 1 || got[0].ID != newest.ID || got[0].ID == old.ID {
		t.Fatalf("filtered List = %+v", got)
	}
	if _, err := store.List(ListOptions{Statuses: []Status{"broken"}}); err == nil {
		t.Fatal("invalid status did not return an error")
	}
}

func TestDefaultPathPermissionsAndStoreJSONArrays(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_DATA_HOME", "")
	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("DefaultPath: %v", err)
	}
	want := filepath.Join(home, ".local", "share", "monitor", "issues.veclite")
	if path != want {
		t.Fatalf("DefaultPath = %q, want %q", path, want)
	}
	info, err := os.Stat(filepath.Dir(path))
	if err != nil {
		t.Fatalf("Stat parent: %v", err)
	}
	if info.Mode().Perm() != 0o700 {
		t.Fatalf("parent mode = %o, want 700", info.Mode().Perm())
	}
	store := openTestStore(t, path)
	issue, occurrence, err := store.UpsertOccurrence(OccurrenceInput{Project: "p", Message: "boom"})
	if err != nil {
		t.Fatalf("UpsertOccurrence: %v", err)
	}
	assertJSONContains(t, issue, `"symbols":[]`)
	assertJSONContains(t, occurrence, `"symbols":[]`)
	assertJSONContains(t, occurrence, `"evidence_refs":[]`)
	fileInfo, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat store: %v", err)
	}
	if fileInfo.Mode().Perm() != 0o600 {
		t.Fatalf("store mode = %o, want 600", fileInfo.Mode().Perm())
	}
}

func TestDefaultPathHonorsXDGDataHome(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_DATA_HOME", root)
	path, err := DefaultPath()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, "monitor", "issues.veclite"); path != want {
		t.Fatalf("DefaultPath = %q, want %q", path, want)
	}
	t.Setenv("XDG_DATA_HOME", "relative")
	if _, err := DefaultPath(); err == nil {
		t.Fatal("DefaultPath accepted a relative XDG_DATA_HOME")
	}
}

func TestErrorsAreClearAndClosedStoreIsSafe(t *testing.T) {
	if _, err := OpenStore(""); err == nil {
		t.Fatal("OpenStore empty path succeeded")
	}
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	if _, _, err := store.UpsertOccurrence(OccurrenceInput{}); err == nil {
		t.Fatal("empty occurrence succeeded")
	}
	if _, err := store.Get(""); err == nil {
		t.Fatal("Get empty ID succeeded")
	}
	if _, err := store.Get("ISS-MISSING"); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("Get missing error = %v, want ErrIssueNotFound", err)
	}
	if _, err := store.Resolve("ISS-MISSING"); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("Resolve missing error = %v, want ErrIssueNotFound", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := store.List(ListOptions{}); err == nil {
		t.Fatal("List on closed store succeeded")
	}
}

func TestReadOnlySharedReadWhileWriterOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	bootstrap := openTestStore(t, path)
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("bootstrap Close: %v", err)
	}
	writer, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore writer: %v", err)
	}
	t.Cleanup(func() { _ = writer.Close() })
	if _, _, err := writer.UpsertOccurrence(OccurrenceInput{Project: "p", Message: "boom"}); err != nil {
		t.Fatalf("writer UpsertOccurrence: %v", err)
	}
	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly while writer open: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	if _, err := reader.List(ListOptions{}); err != nil {
		t.Fatalf("shared-read List: %v", err)
	}
}

func TestReadOnlyMissingDatabaseBehavesAsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-created.veclite")
	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly missing database: %v", err)
	}
	t.Cleanup(func() { _ = reader.Close() })
	issues, err := reader.List(ListOptions{})
	if err != nil {
		t.Fatalf("List missing database: %v", err)
	}
	assertJSONArray(t, issues, "[]")
	occurrences, err := reader.Occurrences("ISS-MISSING", 10)
	if err != nil {
		t.Fatalf("Occurrences missing database: %v", err)
	}
	assertJSONArray(t, occurrences, "[]")
	if _, err := reader.Get("ISS-MISSING"); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("Get missing database error = %v, want ErrIssueNotFound", err)
	}
}

func TestStoreRetentionBoundsIssuesAndOccurrences(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))

	first, _, err := store.UpsertOccurrence(OccurrenceInput{
		Project: "alpha", Kind: "error", Message: "first",
	})
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Millisecond)
	for _, message := range []string{"second", "third"} {
		if _, _, err := store.UpsertOccurrence(OccurrenceInput{
			Project: "alpha", Kind: "error", Message: message,
		}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
	if err := store.enforceRecordBounds(2, 10); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Get(first.ID); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("oldest issue get error = %v, want ErrIssueNotFound", err)
	}
	if occurrences, err := store.Occurrences(first.ID, 10); err != nil || len(occurrences) != 0 {
		t.Fatalf("evicted issue occurrences = %d, %v; want 0, nil", len(occurrences), err)
	}
	listed, err := store.List(ListOptions{Limit: 10})
	if err != nil || len(listed) != 2 {
		t.Fatalf("retained issues = %d, %v; want 2, nil", len(listed), err)
	}

	retained := listed[0]
	for range 3 {
		if _, _, err := store.UpsertOccurrence(OccurrenceInput{
			Project: retained.Project, Service: retained.Service, Kind: retained.Kind,
			Message: retained.Message,
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.enforceRecordBounds(2, 2); err != nil {
		t.Fatal(err)
	}
	occurrences, err := store.Occurrences(retained.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(occurrences) > 2 {
		t.Fatalf("retained occurrences = %d, want at most 2 globally", len(occurrences))
	}
	updated, err := store.Get(retained.ID)
	if err != nil {
		t.Fatal(err)
	}
	if updated.OccurrenceCount != 4 {
		t.Fatalf("cumulative occurrence count = %d, want 4", updated.OccurrenceCount)
	}
}

// TestIssueOccurrenceDecodeV1_15Unchanged decodes a hand-written v1.15-shape
// Issue/Occurrence JSON fixture -- every field that existed before E2.3/E2.6
// added Culprit/LatestException/FirstGitSHA/Level/Handled/Runs/Releases
// (Issue) and Exception/DedupeKey (Occurrence) -- and asserts the original
// fields decode exactly as before, with every new field at its zero value.
// This is the "additive; v1.15 JSON must still decode unchanged" done-when.
func TestIssueOccurrenceDecodeV1_15Unchanged(t *testing.T) {
	const issueJSON = `{
		"id": "ISS-0123456789ABCDEF",
		"fingerprint": "abc123",
		"fingerprint_version": "v1",
		"project": "checkout",
		"service": "api",
		"kind": "investigation",
		"title": "Investigation: api",
		"message": "manual process investigation",
		"exception_type": "",
		"symbols": ["pkg.Handler"],
		"severity": "warning",
		"status": "open",
		"first_seen": "2026-07-27T12:00:00Z",
		"last_seen": "2026-07-27T12:05:00Z",
		"occurrence_count": 3,
		"reopened_count": 0
	}`
	var issue Issue
	if err := json.Unmarshal([]byte(issueJSON), &issue); err != nil {
		t.Fatalf("decode v1.15 issue: %v", err)
	}
	if issue.ID != "ISS-0123456789ABCDEF" || issue.FingerprintVersion != "v1" || issue.OccurrenceCount != 3 ||
		len(issue.Symbols) != 1 || issue.Symbols[0] != "pkg.Handler" {
		t.Fatalf("v1.15 fields decoded wrong: %+v", issue)
	}
	if issue.Culprit != nil || issue.LatestException != nil || issue.FirstGitSHA != "" ||
		issue.Level != "" || issue.Handled != nil || len(issue.Runs) != 0 || len(issue.Releases) != 0 {
		t.Fatalf("E2.3/E2.6 fields were not zero-valued on a v1.15 issue: %+v", issue)
	}
	// Round-tripping through the current struct and back must not corrupt
	// the original fields either (the additive fields all carry omitempty).
	reencoded, err := json.Marshal(issue)
	if err != nil {
		t.Fatal(err)
	}
	var roundTrip Issue
	if err := json.Unmarshal(reencoded, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.ID != issue.ID || roundTrip.OccurrenceCount != issue.OccurrenceCount {
		t.Fatalf("round-trip changed v1.15 fields: %+v", roundTrip)
	}

	const occurrenceJSON = `{
		"id": "OCC-0123456789ABCDEF",
		"issue_id": "ISS-0123456789ABCDEF",
		"observed_at": "2026-07-27T12:00:00Z",
		"project": "checkout",
		"service": "api",
		"kind": "investigation",
		"title": "Investigation: api",
		"message": "manual process investigation",
		"exception_type": "",
		"symbols": ["pkg.Handler"],
		"severity": "warning",
		"run_id": "run-1",
		"release": "v1.15.0",
		"pid": 42,
		"tree_hash": "deadbeef",
		"evidence_refs": ["fcheap://stash/1"],
		"evidence": [{"kind": "monitor.incident", "uri": "fcheap://stash/1"}]
	}`
	var occurrence Occurrence
	if err := json.Unmarshal([]byte(occurrenceJSON), &occurrence); err != nil {
		t.Fatalf("decode v1.15 occurrence: %v", err)
	}
	if occurrence.ID != "OCC-0123456789ABCDEF" || occurrence.RunID != "run-1" || occurrence.PID != 42 ||
		len(occurrence.EvidenceRefs) != 1 || len(occurrence.Evidence) != 1 {
		t.Fatalf("v1.15 occurrence fields decoded wrong: %+v", occurrence)
	}
	if occurrence.Exception != nil || occurrence.DedupeKey != "" {
		t.Fatalf("E2.3 occurrence fields were not zero-valued on a v1.15 occurrence: %+v", occurrence)
	}
}

func TestListWindowFiltersSinceUntilRunIDReleaseKind(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	base := time.Now().UTC().Add(-time.Hour)

	exceptionFingerprint := strings.Repeat("ab", 32) // 64 hex chars, like a real sha256 fingerprint
	exIssue, _, err := store.UpsertOccurrence(OccurrenceInput{
		ObservedAt: base, Project: "acme", Kind: KindException, Message: "boom",
		RunID: "run-a", Release: "rel-a", Fingerprint: exceptionFingerprint, FingerprintVersion: FingerprintVersionV2,
	})
	if err != nil {
		t.Fatalf("seed exception issue: %v", err)
	}
	// A second occurrence on the SAME issue widens its Runs/Releases set and
	// its LastSeen without opening a new issue.
	if _, _, err := store.UpsertOccurrence(OccurrenceInput{
		ObservedAt: base.Add(30 * time.Minute), Project: "acme", Kind: KindException, Message: "boom",
		RunID: "run-b", Release: "rel-b", Fingerprint: exceptionFingerprint, FingerprintVersion: FingerprintVersionV2,
	}); err != nil {
		t.Fatalf("second exception occurrence: %v", err)
	}

	alertIssue, _, err := store.UpsertOccurrence(OccurrenceInput{
		ObservedAt: base, Project: "acme", Kind: "monitor.alert.cpu_spike", Message: "cpu spike", RunID: "run-a",
	})
	if err != nil {
		t.Fatalf("seed alert issue: %v", err)
	}

	investigationIssue, _, err := store.UpsertOccurrence(OccurrenceInput{
		ObservedAt: base, Project: "acme", Kind: "investigation", Message: "manual process investigation",
	})
	if err != nil {
		t.Fatalf("seed investigation issue: %v", err)
	}

	// run-a matches the exception issue (first occurrence) and the alert
	// issue, but not the investigation issue.
	byRun, err := store.List(ListOptions{RunID: "run-a"})
	if err != nil {
		t.Fatalf("List RunID: %v", err)
	}
	if !containsIssueID(byRun, exIssue.ID) || !containsIssueID(byRun, alertIssue.ID) || containsIssueID(byRun, investigationIssue.ID) {
		t.Fatalf("RunID=run-a results = %+v", byRun)
	}
	// run-b was only ever seen by the exception issue's second occurrence.
	byRunB, err := store.List(ListOptions{RunID: "run-b"})
	if err != nil {
		t.Fatalf("List RunID: %v", err)
	}
	if len(byRunB) != 1 || byRunB[0].ID != exIssue.ID {
		t.Fatalf("RunID=run-b results = %+v", byRunB)
	}
	if _, err := store.List(ListOptions{RunID: "no-such-run"}); err != nil {
		t.Fatal(err)
	}
	byMissingRun, err := store.List(ListOptions{RunID: "no-such-run"})
	if err != nil || len(byMissingRun) != 0 {
		t.Fatalf("RunID=no-such-run results = %+v, err=%v", byMissingRun, err)
	}

	byRelease, err := store.List(ListOptions{Release: "rel-b"})
	if err != nil || len(byRelease) != 1 || byRelease[0].ID != exIssue.ID {
		t.Fatalf("Release=rel-b results = %+v, err=%v", byRelease, err)
	}

	byKindException, err := store.List(ListOptions{Kind: "exception"})
	if err != nil || len(byKindException) != 1 || byKindException[0].ID != exIssue.ID {
		t.Fatalf("Kind=exception results = %+v, err=%v", byKindException, err)
	}
	byKindAlert, err := store.List(ListOptions{Kind: "alert"})
	if err != nil || len(byKindAlert) != 1 || byKindAlert[0].ID != alertIssue.ID {
		t.Fatalf("Kind=alert results = %+v, err=%v", byKindAlert, err)
	}
	byKindInvestigation, err := store.List(ListOptions{Kind: "investigation"})
	if err != nil || len(byKindInvestigation) != 1 || byKindInvestigation[0].ID != investigationIssue.ID {
		t.Fatalf("Kind=investigation results = %+v, err=%v", byKindInvestigation, err)
	}
	byKindAny, err := store.List(ListOptions{Kind: "any"})
	if err != nil || len(byKindAny) != 3 {
		t.Fatalf("Kind=any results = %+v, err=%v", byKindAny, err)
	}
	if _, err := store.List(ListOptions{Kind: "bogus"}); err == nil {
		t.Fatal("invalid kind did not return an error")
	}

	// Since/Until: base+90m is after every issue's LastSeen -> excluded;
	// base-1m..base+1h includes them (the exception issue's LastSeen is
	// base+30m).
	future := base.Add(90 * time.Minute)
	excluded, err := store.List(ListOptions{Since: future})
	if err != nil || len(excluded) != 0 {
		t.Fatalf("Since in the future results = %+v, err=%v", excluded, err)
	}
	included, err := store.List(ListOptions{Since: base.Add(-time.Minute), Until: base.Add(time.Hour)})
	if err != nil || len(included) != 3 {
		t.Fatalf("Since/Until window results = %+v, err=%v", included, err)
	}
}

func containsIssueID(issues []Issue, id string) bool {
	for _, issue := range issues {
		if issue.ID == id {
			return true
		}
	}
	return false
}

// TestListWindowFilterPerformanceOver10kOccurrences is the E2.6 done-when:
// "a window query over 10k occurrences takes less than 300ms". Runs/
// Releases aggregates (see Issue.Runs's doc comment) are what makes this
// possible: List reads only the issues collection, never the occurrences
// one, so its cost tracks the number of ISSUES, not occurrences.
//
// Seeding goes straight through veclite's collection API (store.db is
// reachable because this test lives in-package) instead of 10,000
// UpsertOccurrence calls: veclite v0.22.1 (pinned in go.mod; the WAL/atomic-
// Save fix is upstream roadmap item E1.8a, not this package's job) fsyncs
// and rewrites the WHOLE file on every UpsertOccurrence's Sync, so seeding
// this way is O(n^2) and takes minutes, not milliseconds -- it would dwarf
// the 300ms budget with setup cost that has nothing to do with what this
// test actually measures (List's own algorithmic cost). One Sync after
// every record is inserted keeps setup fast while still exercising List
// against a store whose occurrences collection genuinely holds 10k rows.
func TestListWindowFilterPerformanceOver10kOccurrences(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	const (
		totalOccurrences  = 10_000
		numIssues         = 1_000
		occurrencesPerIss = totalOccurrences / numIssues
	)
	base := time.Now().UTC().Add(-time.Hour)
	nato := []string{
		"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel",
		"india", "juliett", "kilo", "lima", "mike", "november", "oscar", "papa",
		"quebec", "romeo", "sierra", "tango", "uniform", "victor", "whiskey",
		"xray", "yankee", "zulu",
	}
	issueWord := func(n int) string { return nato[n/len(nato)%len(nato)] + "-" + nato[n%len(nato)] }

	for i := 0; i < numIssues; i++ {
		id := fmt.Sprintf("ISS-PERF%05d", i)
		lastSeen := base.Add(time.Duration(i) * time.Second)
		issue := Issue{
			ID: id, Fingerprint: fmt.Sprintf("perf-fingerprint-%d", i), FingerprintVersion: FingerprintVersionV2,
			Project: "acme", Kind: KindException, Title: issueWord(i), Message: issueWord(i),
			Symbols: []string{}, Status: StatusOpen,
			FirstSeen: base, LastSeen: lastSeen, OccurrenceCount: occurrencesPerIss,
			// Every issue sees its own RunID/Release; issue #7 is the one
			// the assertions below look for.
			Runs: []string{fmt.Sprintf("run-%d", i)}, Releases: []string{"v1"},
		}
		content, err := json.Marshal(issue)
		if err != nil {
			t.Fatalf("marshal seed issue %d: %v", i, err)
		}
		if _, err := store.db.Collection(issuesCollection).InsertTextDocument(string(content), map[string]any{
			"id": issue.ID, "fingerprint": issue.Fingerprint, "status": string(issue.Status),
			"project": issue.Project, "service": issue.Service, "last_seen": issue.LastSeen,
		}); err != nil {
			t.Fatalf("insert seed issue %d: %v", i, err)
		}
		for j := 0; j < occurrencesPerIss; j++ {
			occurrence := Occurrence{
				ID: fmt.Sprintf("OCC-PERF%05d-%02d", i, j), IssueID: id,
				ObservedAt: base.Add(time.Duration(i) * time.Second), Project: "acme", Kind: KindException,
				Symbols: []string{}, EvidenceRefs: []string{}, Evidence: []EvidenceRef{}, Count: 1,
			}
			occContent, err := json.Marshal(occurrence)
			if err != nil {
				t.Fatalf("marshal seed occurrence %d/%d: %v", i, j, err)
			}
			if _, err := store.db.Collection(occurrencesCollection).InsertTextDocument(string(occContent), map[string]any{
				"id": occurrence.ID, "issue_id": occurrence.IssueID, "observed_at": occurrence.ObservedAt, "dedupe_key": "",
			}); err != nil {
				t.Fatalf("insert seed occurrence %d/%d: %v", i, j, err)
			}
		}
	}
	if err := store.db.Sync(); err != nil {
		t.Fatalf("Sync after bulk seed: %v", err)
	}
	if got := store.db.Collection(occurrencesCollection).Count(); got != totalOccurrences {
		t.Fatalf("seeded %d occurrences, want %d", got, totalOccurrences)
	}

	start := time.Now()
	got, err := store.List(ListOptions{RunID: "run-7", Release: "v1", Kind: "exception", Since: base.Add(-time.Minute)})
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 || got[0].ID != "ISS-PERF00007" {
		t.Fatalf("filtered result = %+v, want exactly issue #7", got)
	}
	if elapsed > 300*time.Millisecond {
		t.Fatalf("List over a store with %d occurrences (%d issues) took %s, want < 300ms", totalOccurrences, numIssues, elapsed)
	}
}

func TestAppendBoundedUniqueDedupesAndEvictsOldest(t *testing.T) {
	var values []string
	for i := 0; i < maxIssueRunsReleases+5; i++ {
		values = appendBoundedUnique(values, fmt.Sprintf("run-%d", i), maxIssueRunsReleases)
	}
	if len(values) != maxIssueRunsReleases {
		t.Fatalf("len(values) = %d, want %d", len(values), maxIssueRunsReleases)
	}
	if values[0] != "run-5" {
		t.Fatalf("oldest 5 were not evicted first: values[0] = %q", values[0])
	}
	before := len(values)
	values = appendBoundedUnique(values, "run-9", maxIssueRunsReleases)
	if len(values) != before {
		t.Fatalf("re-adding an existing value changed the length: %d -> %d", before, len(values))
	}
	values = appendBoundedUnique(values, "", maxIssueRunsReleases)
	if len(values) != before {
		t.Fatal("an empty value was appended")
	}
}

func fingerprintForTest(input OccurrenceInput) string {
	return FingerprintV1(FingerprintInput{
		Project: input.Project, Service: input.Service, Kind: input.Kind,
		ExceptionType: input.ExceptionType, Message: input.Message, Symbols: input.Symbols,
	})
}

func openTestStore(t *testing.T, path string) *Store {
	t.Helper()
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func assertJSONArray(t *testing.T, value any, want string) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if string(data) != want {
		t.Fatalf("JSON = %s, want %s", data, want)
	}
}

func assertJSONContains(t *testing.T, value any, want string) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	if !contains(string(data), want) {
		t.Fatalf("JSON = %s, want it to contain %s", data, want)
	}
}

func contains(value, part string) bool {
	for i := 0; i+len(part) <= len(value); i++ {
		if value[i:i+len(part)] == part {
			return true
		}
	}
	return false
}
