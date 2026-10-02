package issues

import (
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestHistogramBucketsByIssue(t *testing.T) {
	start := time.Date(2026, 10, 1, 10, 0, 0, 0, time.UTC)
	points := []OccurrencePoint{
		{IssueID: "A", ObservedAt: start, Count: 1},
		{IssueID: "A", ObservedAt: start.Add(59 * time.Minute), Count: 2},
		{IssueID: "A", ObservedAt: start.Add(2 * time.Hour), Count: 3},
		{IssueID: "B", ObservedAt: start.Add(time.Hour), Count: 4},
		{IssueID: "B", ObservedAt: start.Add(-time.Second), Count: 9},  // before the window
		{IssueID: "B", ObservedAt: start.Add(3 * time.Hour), Count: 9}, // at the exclusive end
	}
	got := Histogram(points, start, time.Hour, 3)
	want := map[string][]int64{
		"A": {3, 0, 3},
		"B": {0, 4, 0},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Histogram = %v, want %v", got, want)
	}
}

func TestHistogramRejectsDegenerateShape(t *testing.T) {
	points := []OccurrencePoint{{IssueID: "A", ObservedAt: time.Now(), Count: 1}}
	if got := Histogram(points, time.Now(), 0, 4); len(got) != 0 {
		t.Fatalf("zero bucket: got %v, want empty", got)
	}
	if got := Histogram(points, time.Now(), time.Minute, 0); len(got) != 0 {
		t.Fatalf("zero buckets: got %v, want empty", got)
	}
}

func TestOccurrencePointsFiltersBySinceAndIDs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })

	base := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	record := func(title string, at time.Time) Issue {
		t.Helper()
		issue, _, err := store.UpsertOccurrence(OccurrenceInput{
			ObservedAt: at, Project: "demo", Kind: "exception", Title: title,
			Symbols: []string{title + "Fn"}, Severity: "error",
		})
		if err != nil {
			t.Fatal(err)
		}
		return issue
	}
	a := record("alpha", base.Add(-2*time.Hour))
	record("alpha", base)
	b := record("beta", base.Add(time.Minute))

	all, err := store.OccurrencePoints(time.Time{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(all) != 3 {
		t.Fatalf("all points = %d, want 3", len(all))
	}

	recent, err := store.OccurrencePoints(base.Add(-time.Minute), nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 2 {
		t.Fatalf("recent points = %d, want 2 (the -2h alpha falls before since)", len(recent))
	}

	onlyB, err := store.OccurrencePoints(time.Time{}, []string{b.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(onlyB) != 1 || onlyB[0].IssueID != b.ID || onlyB[0].Count != 1 {
		t.Fatalf("points for %s = %+v, want one point with count 1", b.ID, onlyB)
	}
	if a.ID == b.ID {
		t.Fatalf("alpha and beta grouped into one issue %s", a.ID)
	}
}
