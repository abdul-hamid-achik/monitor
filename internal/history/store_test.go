package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAppendAndQuery(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.veclite")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeStoreOnCleanup(t, s)

	base := time.Now().Add(-10 * time.Minute)
	for i := 0; i < 6; i++ {
		if err := s.Append(Sample{Timestamp: base.Add(time.Duration(i) * time.Minute), Metric: "cpu.usage", Value: float64(i * 10)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// A different metric that must not leak into a cpu query.
	_ = s.Append(Sample{Timestamp: base, Metric: "mem.usage", Value: 99})

	pts, err := s.Query("cpu.usage", base.Add(2*time.Minute))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	// minutes 2,3,4,5 => 4 points, oldest-first.
	if len(pts) != 4 {
		t.Fatalf("got %d points, want 4", len(pts))
	}
	if pts[0].Value != 20 || pts[len(pts)-1].Value != 50 {
		t.Errorf("boundary values = %v..%v, want 20..50", pts[0].Value, pts[len(pts)-1].Value)
	}
	for i := 1; i < len(pts); i++ {
		if pts[i].Timestamp.Before(pts[i-1].Timestamp) {
			t.Errorf("points not oldest-first at %d", i)
		}
	}
}

// TestPersistsAcrossReopen verifies the recorder/query split: data written by
// one Store survives a Close + reopen (the Unix-nano timestamp round-trips).
func TestPersistsAcrossReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.veclite")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	ts := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := w.Append(Sample{Timestamp: ts, Metric: "cpu.usage", Value: 42}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	closeStoreOnCleanup(t, r)
	pts, err := r.Query("cpu.usage", ts.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(pts) != 1 || pts[0].Value != 42 {
		t.Fatalf("reloaded points = %+v, want one value 42", pts)
	}
	if !pts[0].Timestamp.Equal(ts) {
		t.Errorf("reloaded timestamp = %v, want %v (round-trip)", pts[0].Timestamp, ts)
	}
}

// TestAppendIsDurableBeforeClose is the regression for the durability finding:
// Append must flush each batch to disk immediately, so a SIGKILL/crash before
// Close loses at most the in-flight tick rather than the whole recording
// session. veclite's default syncOnWrite is off, so before Append called
// db.Sync() the on-disk file stayed frozen at the last clean Close.
//
// We assert the invariant by snapshotting the live (still-open) DB file to a
// second path and opening that copy read-only — it must already contain the
// just-appended sample. (veclite holds an exclusive lock on the writer's path,
// so a second handle on the *same* path is intentionally not possible; the copy
// stands in for "what a post-crash reader would recover from disk".)
func TestAppendIsDurableBeforeClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "h.veclite")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open writer: %v", err)
	}
	closeStoreOnCleanup(t, w)

	ts := time.Now().Add(-time.Minute).Truncate(time.Second)
	if err := w.Append(Sample{Timestamp: ts, Metric: "cpu.usage", Value: 73}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	// Copy the on-disk file WITHOUT closing the writer, then open the copy.
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read live db file: %v", err)
	}
	copyPath := filepath.Join(dir, "snapshot.veclite")
	if err := os.WriteFile(copyPath, raw, 0o644); err != nil {
		t.Fatalf("write snapshot copy: %v", err)
	}
	r, err := OpenReadOnly(copyPath)
	if err != nil {
		t.Fatalf("OpenReadOnly(copy): %v", err)
	}
	closeStoreOnCleanup(t, r)
	pts, err := r.Query("cpu.usage", ts.Add(-time.Hour))
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(pts) != 1 || pts[0].Value != 73 {
		t.Fatalf("on-disk snapshot = %+v, want one value 73 (Append must sync before Close)", pts)
	}
}

// TestMetricsReturnsDistinctSortedNames covers Metrics(): it must return each
// recorded metric name once, sorted, regardless of how many samples each has.
func TestMetricsReturnsDistinctSortedNames(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.veclite")
	s, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeStoreOnCleanup(t, s)

	now := time.Now()
	// Two metrics, the first recorded several times.
	for i := 0; i < 3; i++ {
		if err := s.Append(Sample{Timestamp: now.Add(time.Duration(i) * time.Second), Metric: "mem.usage", Value: float64(i)}); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	if err := s.Append(Sample{Timestamp: now, Metric: "cpu.usage", Value: 1}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	got, err := s.Metrics()
	if err != nil {
		t.Fatalf("Metrics: %v", err)
	}
	want := []string{"cpu.usage", "mem.usage"} // distinct + sorted
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Metrics() = %v, want %v", got, want)
	}
}

// TestMetricsEmptyStore: Metrics on a store with no collection is nil, not a panic.
func TestMetricsEmptyStore(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.veclite")
	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	closeStoreOnCleanup(t, r)
	if names, err := r.Metrics(); err != nil || len(names) != 0 {
		t.Errorf("Metrics() on empty store = %v, %v; want empty, nil", names, err)
	}
}

func TestSummarize(t *testing.T) {
	if s := Summarize(nil); s.Count != 0 {
		t.Errorf("empty summarize count = %d, want 0", s.Count)
	}
	now := time.Now()
	pts := []Point{
		{now, 10}, {now.Add(time.Second), 30}, {now.Add(2 * time.Second), 20}, {now.Add(3 * time.Second), 40},
	}
	s := Summarize(pts)
	if s.Count != 4 || s.Min != 10 || s.Max != 40 || s.First != 10 || s.Last != 40 || s.Trend != 30 {
		t.Errorf("summary = %+v", s)
	}
	if s.Avg != 25 {
		t.Errorf("avg = %v, want 25", s.Avg)
	}
}

func closeStoreOnCleanup(t *testing.T, store *Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close history store: %v", err)
		}
	})
}
