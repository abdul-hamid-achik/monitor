package history

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	veclite "github.com/abdul-hamid-achik/veclite"
)

// openWithMaxRecords opens a fresh history store whose Go-level maxRecords
// cap is set BEFORE the collection is created (Open always uses
// defaultMaxRecords for that). Tests that need a small cap in effect at
// creation time — to exercise veclite's own per-insert eviction behavior
// rather than only enforceRecordLimitLocked's after-the-fact batching — use
// this instead of Open + a post-hoc `w.maxRecords = n` override, which only
// ever changes the Go-level cap and leaves the (much larger) cap baked into
// the collection at creation time alone.
func openWithMaxRecords(t *testing.T, path string, maxRecords int) *Store {
	t.Helper()
	db, err := veclite.Open(path)
	if err != nil {
		t.Fatalf("veclite.Open: %v", err)
	}
	s := &Store{
		db:           db,
		maxRecords:   maxRecords,
		syncEvery:    defaultSyncEvery,
		syncInterval: defaultSyncInterval,
		lastSync:     time.Now(),
	}
	if err := s.ensureCollection(); err != nil {
		_ = db.Close()
		t.Fatalf("ensureCollection: %v", err)
	}
	return s
}

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

// TestMemoryLimitsSurviveReopen is the regression for bug 13 (veclite side):
// veclite v0.22.1 does not persist a collection's MemoryConfig across a
// reopen (loadFromSnapshot rebuilds the collection without it), so relying
// solely on WithMemoryLimits at CreateCollection time silently disabled the
// cap forever the first time the store was closed and reopened — reproduced
// upstream with MaxRecords=5 growing to 15 records after one reopen.
// enforceRecordLimitLocked re-applies the cap from Go-level Store state on
// every Append instead, so it must stay capped across Close+Open.
func TestMemoryLimitsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.veclite")

	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	w.maxRecords = 5
	for i := 0; i < 10; i++ {
		if err := w.Append(Sample{Metric: "cpu.usage", Value: float64(i)}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	w2, err := Open(path)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	closeStoreOnCleanup(t, w2)
	w2.maxRecords = 5
	for i := 0; i < 10; i++ {
		if err := w2.Append(Sample{Metric: "cpu.usage", Value: float64(100 + i)}); err != nil {
			t.Fatalf("post-reopen Append %d: %v", i, err)
		}
	}

	pts, err := w2.Query("cpu.usage", time.Time{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(pts) != 5 {
		t.Fatalf("record count after reopen = %d, want capped at 5 (MaxRecords lost on reopen was bug 13)", len(pts))
	}
}

// TestFreshStoreDoesNotEvictOnEveryInsert is history's counterpart to the
// same regression covered in internal/logger: veclite@v0.22.1's own
// per-insert enforcement (only triggered by a MemoryConfig passed to
// CreateCollection) evicted one record via a full sorted scan on EVERY
// insert once the collection reached its cap, in the very same session that
// created the store — pinning Count() at maxRecords and defeating
// enforceRecordLimitLocked's batching below, which used to only take effect
// after a reopen. ensureCollection no longer passes WithMemoryLimits, so a
// fresh store (never reopened) must let the collection grow past maxRecords
// by up to ~10% before evicting, then evict the whole accumulated batch.
func TestFreshStoreDoesNotEvictOnEveryInsert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.veclite")
	w := openWithMaxRecords(t, path, 100)
	closeStoreOnCleanup(t, w)

	for i := 0; i < 105; i++ {
		if err := w.Append(Sample{Metric: "cpu.usage", Value: float64(i)}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	pts, err := w.Query("cpu.usage", time.Time{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(pts) != 105 {
		t.Fatalf("record count after 105 appends = %d, want 105 (no per-insert eviction on a fresh store)", len(pts))
	}

	for i := 0; i < 5; i++ {
		if err := w.Append(Sample{Metric: "cpu.usage", Value: float64(200 + i)}); err != nil {
			t.Fatalf("Append (batch trigger) %d: %v", i, err)
		}
	}
	pts, err = w.Query("cpu.usage", time.Time{})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(pts) != 100 {
		t.Fatalf("record count after 110 appends = %d, want capped back to 100 by the batched eviction", len(pts))
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

// TestAppendIsDurableWithinSyncBounds is the regression for the durability
// finding: Append must flush within a bounded number of batches or a bounded
// time window, so a SIGKILL/crash loses at most that window rather than the
// whole recording session. veclite's default syncOnWrite is off, so before
// Append called db.Sync() at all the on-disk file stayed frozen at the last
// clean Close; Append no longer syncs on EVERY batch (that re-gob-encoded and
// fsynced the whole database every tick — see maybeSyncLocked), so this
// appends syncEvery batches to cross the count-based bound instead of one.
//
// We assert the invariant by snapshotting the live (still-open) DB file to a
// second path and opening that copy read-only — it must already contain the
// just-appended samples. (veclite holds an exclusive lock on the writer's path,
// so a second handle on the *same* path is intentionally not possible; the copy
// stands in for "what a post-crash reader would recover from disk".)
func TestAppendIsDurableWithinSyncBounds(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "h.veclite")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open writer: %v", err)
	}
	closeStoreOnCleanup(t, w)

	ts := time.Now().Add(-time.Minute).Truncate(time.Second)
	for i := 0; i < w.syncEvery; i++ {
		if err := w.Append(Sample{Timestamp: ts.Add(time.Duration(i) * time.Second), Metric: "cpu.usage", Value: 73}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if got := w.SyncCount(); got != 1 {
		t.Fatalf("SyncCount = %d after crossing syncEvery, want exactly 1", got)
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
	if len(pts) != w.syncEvery {
		t.Fatalf("on-disk snapshot = %+v, want %d values (Append must sync within syncEvery batches)", pts, w.syncEvery)
	}
}

// TestAppendDoesNotSyncEveryBatch is the efficiency regression: with fewer
// than syncEvery batches and less than syncInterval elapsed, Append must NOT
// have triggered a full rewrite yet.
func TestAppendDoesNotSyncEveryBatch(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.veclite")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeStoreOnCleanup(t, w)

	for i := 0; i < w.syncEvery-1; i++ {
		if err := w.Append(Sample{Metric: "cpu.usage", Value: float64(i)}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if got := w.SyncCount(); got != 0 {
		t.Fatalf("SyncCount = %d before crossing syncEvery or syncInterval, want 0", got)
	}
}

// TestAppendBatchesSyncsAcrossManyTicks is the write-amplification regression
// for bug 13/E1.11: history.Append used to call db.Sync() (a full gob-encode +
// fsync of the whole database) on every single tick. With the default
// syncEvery, a long recording session of many ticks must produce far fewer
// full rewrites than ticks.
func TestAppendBatchesSyncsAcrossManyTicks(t *testing.T) {
	path := filepath.Join(t.TempDir(), "h.veclite")
	w, err := Open(path)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	closeStoreOnCleanup(t, w)

	const ticks = 500
	for i := 0; i < ticks; i++ {
		if err := w.Append(Sample{Metric: "cpu.usage", Value: float64(i)}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	want := ticks / w.syncEvery
	if got := w.SyncCount(); got != want {
		t.Fatalf("SyncCount = %d for %d ticks at syncEvery=%d, want exactly %d", got, ticks, w.syncEvery, want)
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
