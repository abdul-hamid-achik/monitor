package explain

import (
	"context"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// seedException records a minimal exception issue for project/service at
// observedAt, returning the resulting issue ID.
func seedException(t *testing.T, storePath, projectSlug, service string, observedAt time.Time) string {
	t.Helper()
	ex := stacktrace.Exception{Runtime: "go", Type: "panic", Value: projectSlug + "-" + service + "-" + observedAt.String(), Parser: "gopanic", Level: "fatal"}
	id := project.Identity{Slug: projectSlug, Service: service}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: observedAt})
	if err != nil {
		t.Fatalf("seedException: %v", err)
	}
	return res.Issue.ID
}

// seedAlert writes a plain watch-style alert issue (kind
// "monitor.alert.<rule>") directly via UpsertOccurrenceResult -- alerts are
// never produced by RecordException.
func seedAlert(t *testing.T, storePath, projectSlug string, observedAt time.Time) string {
	t.Helper()
	var id string
	err := issues.WithWriter(context.Background(), storePath, issues.DefaultWriterWait, func(store *issues.Store) error {
		result, err := store.UpsertOccurrenceResult(issues.OccurrenceInput{
			ObservedAt: observedAt, Project: projectSlug, Kind: "monitor.alert.cpu_spike",
			Title: "cpu_spike", Message: "cpu spike", Severity: "warning",
		})
		if err != nil {
			return err
		}
		id = result.Issue.ID
		return nil
	})
	if err != nil {
		t.Fatalf("seedAlert: %v", err)
	}
	return id
}

func TestResolveLatestDefaultsToExceptionKindIgnoringAlerts(t *testing.T) {
	storePath := newTestStore(t)
	now := time.Now().UTC()
	excID := seedException(t, storePath, "polyglot", "workload", now.Add(-time.Hour))
	seedAlert(t, storePath, "polyglot", now) // more recent, but an alert

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	issue, resolved, ok, err := ResolveLatest(store, LatestFilter{Project: "polyglot"})
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("ok = false, want a match")
	}
	if issue.ID != excID {
		t.Fatalf("resolved issue = %s, want the exception %s (alerts must be excluded by default)", issue.ID, excID)
	}
	if resolved.Kind != "exception" {
		t.Errorf("resolved_from.kind = %q, want exception", resolved.Kind)
	}
}

func TestResolveLatestRespectsProjectFilter(t *testing.T) {
	storePath := newTestStore(t)
	now := time.Now().UTC()
	wantID := seedException(t, storePath, "polyglot", "workload", now.Add(-time.Minute))
	seedException(t, storePath, "other-project", "svc", now) // more recent, different project

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	issue, _, ok, err := ResolveLatest(store, LatestFilter{Project: "polyglot"})
	if err != nil || !ok {
		t.Fatalf("ResolveLatest: ok=%v err=%v", ok, err)
	}
	if issue.ID != wantID {
		t.Fatalf("resolved issue = %s, want %s (project filter must restrict the result)", issue.ID, wantID)
	}
}

func TestResolveLatestAnyKindIncludesAlerts(t *testing.T) {
	storePath := newTestStore(t)
	now := time.Now().UTC()
	seedException(t, storePath, "polyglot", "workload", now.Add(-time.Hour))
	alertID := seedAlert(t, storePath, "polyglot", now)

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	issue, resolved, ok, err := ResolveLatest(store, LatestFilter{Project: "polyglot", Kind: "any"})
	if err != nil || !ok {
		t.Fatalf("ResolveLatest: ok=%v err=%v", ok, err)
	}
	if issue.ID != alertID {
		t.Fatalf("resolved issue = %s, want the more recent alert %s under kind=any", issue.ID, alertID)
	}
	if resolved.Kind != "any" {
		t.Errorf("resolved_from.kind = %q", resolved.Kind)
	}
}

func TestResolveLatestNoMatchIsNotAnError(t *testing.T) {
	storePath := newTestStore(t)
	store, err := issues.OpenStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	_, resolved, ok, err := ResolveLatest(store, LatestFilter{Project: "nothing-here"})
	if err != nil {
		t.Fatalf("err = %v, want nil (no match is not a failure)", err)
	}
	if ok {
		t.Fatal("ok = true, want false")
	}
	if resolved.ID != "latest" || resolved.Project != "nothing-here" {
		t.Errorf("resolved = %+v", resolved)
	}
}
