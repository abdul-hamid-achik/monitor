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
