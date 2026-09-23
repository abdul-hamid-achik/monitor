package logger

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestStoreOpenClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.veclite")
	s, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
}

func TestStoreAppendAndSearch(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.veclite")
	w, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore writer: %v", err)
	}

	entries := []Entry{
		{Timestamp: time.Now(), PID: 1, Process: "test", Level: "INFO", Message: "hello world", Raw: "hello world"},
		{Timestamp: time.Now(), PID: 2, Process: "test", Level: "WARN", Message: "something is wrong", Raw: "something is wrong"},
		{Timestamp: time.Now(), PID: 3, Process: "other", Level: "ERROR", Message: "fatal failure", Raw: "fatal failure"},
	}
	for _, e := range entries {
		if err := w.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	// Close writer so the reader can acquire the lock.
	if err := w.Close(); err != nil {
		t.Fatalf("writer Close: %v", err)
	}

	r, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	closeStoreOnCleanup(t, r)
	got, err := r.Search("wrong", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) == 0 {
		t.Fatal("expected at least one match for 'wrong'")
	}
	if got[0].Message != "something is wrong" {
		t.Errorf("got = %q, want %q", got[0].Message, "something is wrong")
	}
}

// TestOpenReadOnlyWhileWriterOpen locks the concurrency guarantee used by
// `monitor logs search`: a shared read must not contend with the capture or
// Studio writer lock.
func TestOpenReadOnlyWhileWriterOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.veclite")
	// Bootstrap the collection so the reader's point-in-time snapshot knows
	// its schema even if it does not include the writer's newest record yet.
	bootstrap, err := OpenStore(path)
	if err != nil {
		t.Fatalf("bootstrap OpenStore: %v", err)
	}
	if err := bootstrap.Close(); err != nil {
		t.Fatalf("bootstrap Close: %v", err)
	}
	writer, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore writer: %v", err)
	}
	closeStoreOnCleanup(t, writer)
	if err := writer.Append(Entry{Timestamp: time.Now(), Message: "concurrent needle", Raw: "concurrent needle"}); err != nil {
		t.Fatalf("Append: %v", err)
	}

	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly while writer is open: %v", err)
	}
	closeStoreOnCleanup(t, reader)
	results, err := reader.Search("needle", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	// Readers are documented as point-in-time snapshots, so the newest entry
	// may or may not be visible. Opening and searching without lock failure is
	// the contract this test protects.
	if len(results) > 1 {
		t.Fatalf("results = %+v, want at most the one matching entry", results)
	}
}

func TestSearchMissingCollectionReturnsEmpty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.veclite")
	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	closeStoreOnCleanup(t, reader)
	results, err := reader.Search("anything", 10)
	if err != nil {
		t.Fatalf("Search missing collection: %v", err)
	}
	if results == nil || len(results) != 0 {
		t.Fatalf("results = %#v, want non-nil empty slice", results)
	}
}

// TestSearchReturnsMostRecentWithinLimit is a regression for the bug where
// Search broke out of a map-order scan at `limit`, returning a
// non-deterministic subset instead of the newest matches. Searches the
// same (writer) store so timestamps stay exact in memory.
func TestSearchReturnsMostRecentWithinLimit(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "logs.veclite")
	w, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	closeStoreOnCleanup(t, w)

	base := time.Now()
	for i := 0; i < 6; i++ {
		e := Entry{
			Timestamp: base.Add(time.Duration(i) * time.Minute),
			PID:       int32(i),
			Message:   "match needle",
			Raw:       "match needle",
		}
		if err := w.Append(e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := w.Search("needle", 3)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("Search returned %d, want 3 (the limit)", len(got))
	}
	for i := 0; i < len(got)-1; i++ {
		if got[i].Timestamp.Before(got[i+1].Timestamp) {
			t.Errorf("results not newest-first at %d: %v before %v", i, got[i].Timestamp, got[i+1].Timestamp)
		}
	}
	if !got[0].Timestamp.Equal(base.Add(5 * time.Minute)) {
		t.Errorf("newest result = %v, want the most recent entry %v", got[0].Timestamp, base.Add(5*time.Minute))
	}
}

func TestStoreInvalidPath(t *testing.T) {
	if _, err := OpenStore("/no/such/dir/logs.veclite"); err == nil {
		t.Error("expected error for invalid path")
	}
}

func TestResolvePathPrecedenceAndDurableDefault(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv(StorePathEnv, "")

	got, err := ResolvePath("")
	if err != nil {
		t.Fatalf("ResolvePath default: %v", err)
	}
	want := filepath.Join(home, ".local", "share", "monitor", "logs.veclite")
	if got != want {
		t.Fatalf("default path = %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Dir(got)); err != nil {
		t.Fatalf("default parent was not created: %v", err)
	}
	info, err := os.Stat(filepath.Dir(got))
	if err != nil {
		t.Fatalf("stat default parent: %v", err)
	}
	if gotMode := info.Mode().Perm(); gotMode != 0o700 {
		t.Fatalf("default parent mode = %o, want 700", gotMode)
	}

	envPath := filepath.Join(home, "from-env.veclite")
	t.Setenv(StorePathEnv, envPath)
	if got, err := ResolvePath(""); err != nil || got != envPath {
		t.Fatalf("environment path = (%q, %v), want %q", got, err, envPath)
	}
	override := filepath.Join(home, "explicit.veclite")
	if got, err := ResolvePath(override); err != nil || got != override {
		t.Fatalf("explicit path = (%q, %v), want %q", got, err, override)
	}
}

func TestStoreRetentionEvictsOldestAndExpired(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.veclite")
	store, err := OpenStoreWithRetention(path, RetentionPolicy{
		MaxAge:        time.Hour,
		MaxRecords:    3,
		SweepInterval: time.Nanosecond,
	})
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	closeStoreOnCleanup(t, store)

	now := time.Now()
	entries := []Entry{
		{Timestamp: now.Add(-2 * time.Hour), Message: "expired", Raw: "expired"},
		{Timestamp: now.Add(-4 * time.Minute), Message: "first", Raw: "first"},
		{Timestamp: now.Add(-3 * time.Minute), Message: "second", Raw: "second"},
		{Timestamp: now.Add(-2 * time.Minute), Message: "third", Raw: "third"},
		{Timestamp: now.Add(-time.Minute), Message: "fourth", Raw: "fourth"},
	}
	for _, entry := range entries {
		if err := store.Append(entry); err != nil {
			t.Fatalf("Append %q: %v", entry.Message, err)
		}
	}

	got, err := store.Search("", 100)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("retained entries = %d, want 3: %+v", len(got), got)
	}
	if got[0].Message != "fourth" || got[2].Message != "second" {
		t.Fatalf("retained entries = %+v, want fourth through second", got)
	}
}

// TestMemoryLimitsSurviveReopen is the regression for bug 13 (veclite side):
// veclite v0.22.1 does not persist a collection's MemoryConfig across a
// reopen (loadFromSnapshot rebuilds the collection without it), so a store
// that relied solely on the WithMemoryLimits option passed to
// CreateCollection would silently lose its cap the first time it was closed
// and reopened. enforceRecordLimit re-applies RetentionPolicy.MaxRecords from
// Go-level Store state on every Append instead, independent of veclite's
// internal config, so the cap must survive Close+Open.
func TestMemoryLimitsSurviveReopen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.veclite")
	policy := RetentionPolicy{MaxAge: time.Hour, MaxRecords: 5}

	w, err := OpenStoreWithRetention(path, policy)
	if err != nil {
		t.Fatalf("OpenStoreWithRetention: %v", err)
	}
	for i := 0; i < 10; i++ {
		if err := w.Append(Entry{Message: "pre-reopen", Raw: "pre-reopen"}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	w2, err := OpenStoreWithRetention(path, policy)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	closeStoreOnCleanup(t, w2)
	for i := 0; i < 10; i++ {
		if err := w2.Append(Entry{Message: "post-reopen", Raw: "post-reopen"}); err != nil {
			t.Fatalf("post-reopen Append %d: %v", i, err)
		}
	}

	got, err := w2.Search("", 100)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 5 {
		t.Fatalf("record count after reopen = %d, want capped at 5 (MaxRecords lost on reopen was bug 13)", len(got))
	}
}

// TestAppendBatchesSyncsAcrossManyLines is the write-amplification regression
// for E1.11: once a store is at its record cap, Append used to call
// db.Sync() — a full gob-encode + fsync of the whole database — on every
// single captured line. With MaxRecords=100 and the default SyncEvery, 10k
// appends must cause at most 10 full rewrites.
func TestAppendBatchesSyncsAcrossManyLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.veclite")
	store, err := OpenStoreWithRetention(path, RetentionPolicy{MaxAge: time.Hour, MaxRecords: 100})
	if err != nil {
		t.Fatalf("OpenStoreWithRetention: %v", err)
	}
	closeStoreOnCleanup(t, store)

	const lines = 10_000
	for i := 0; i < lines; i++ {
		if err := store.Append(Entry{Message: "line", Raw: "line"}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	if got := store.SyncCount(); got > 10 {
		t.Fatalf("SyncCount = %d for %d appends at MaxRecords=100, want <= 10", got, lines)
	}
}

// TestReaderSeesLinesWithinSyncInterval verifies the freshness half of
// E1.11: a concurrent OpenReadOnly reader must see a captured line within
// SyncInterval, not only at Close. Deliberately makes exactly ONE Append and
// nothing else: maybeSyncLocked's time bound is only ever evaluated from
// inside Append, so a prior version of this test forced the flush with a
// second "trigger" Append instead of exercising the real requirement —
// startFlusher's background ticker, which must flush on wall-clock time
// alone even when nothing calls Append again.
func TestReaderSeesLinesWithinSyncInterval(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.veclite")
	store, err := OpenStoreWithRetention(path, RetentionPolicy{
		MaxAge:       time.Hour,
		MaxRecords:   DefaultMaxRecords,
		SyncEvery:    DefaultSyncEvery, // far more than the one line below
		SyncInterval: 30 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("OpenStoreWithRetention: %v", err)
	}
	closeStoreOnCleanup(t, store)

	if err := store.Append(Entry{Message: "fresh needle", Raw: "fresh needle"}); err != nil {
		t.Fatalf("Append: %v", err)
	}
	// No further Append. Only the background flusher goroutine (startFlusher)
	// can notice that SyncInterval has elapsed and flush this line to disk.
	time.Sleep(150 * time.Millisecond)

	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly: %v", err)
	}
	closeStoreOnCleanup(t, reader)
	got, err := reader.Search("needle", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("reader saw %d matches for the needle within SyncInterval, want 1 (background flusher did not flush a quiet writer)", len(got))
	}
}

// TestFreshStoreDoesNotEvictOnEveryInsert is the eviction-batching
// regression: veclite@v0.22.1's own per-insert enforcement (only triggered
// by a MemoryConfig passed to CreateCollection) evicted one record via a
// full sorted scan on EVERY insert once the collection reached its cap, in
// the very same session that created the store — pinning Count() at
// MaxRecords and defeating enforceRecordLimit's batching below, which used
// to only take effect after a reopen (bug 13 dropping veclite's
// MemoryConfig incidentally "fixed" this on the second session only).
// ensureCollection no longer passes WithMemoryLimits, so a fresh store
// (never reopened) must let the collection grow past MaxRecords by up to
// ~10% before evicting, then evict the whole accumulated batch at once.
func TestFreshStoreDoesNotEvictOnEveryInsert(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.veclite")
	store, err := OpenStoreWithRetention(path, RetentionPolicy{MaxAge: time.Hour, MaxRecords: 100})
	if err != nil {
		t.Fatalf("OpenStoreWithRetention: %v", err)
	}
	closeStoreOnCleanup(t, store)

	for i := 0; i < 105; i++ {
		if err := store.Append(Entry{Message: "line", Raw: "line"}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got, err := store.Search("", 200)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 105 {
		t.Fatalf("record count after 105 appends = %d, want 105 (no per-insert eviction on a fresh store)", len(got))
	}

	for i := 0; i < 5; i++ {
		if err := store.Append(Entry{Message: "line", Raw: "line"}); err != nil {
			t.Fatalf("Append (batch trigger) %d: %v", i, err)
		}
	}
	got, err = store.Search("", 200)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != 100 {
		t.Fatalf("record count after 110 appends = %d, want capped back to 100 by the batched eviction", len(got))
	}
}

// TestHelperFlushBurst is not a real test. TestFlusherSurvivesKillNineAfterBurst
// re-executes the test binary with -test.run limited to this one function, in
// a subprocess it then SIGKILLs — simulating a captured process's own
// supervisor sending kill -9 well after a burst of output but with no clean
// shutdown (no Close, no Sync). Run directly (e.g. by `go test`'s normal
// matching), it is a no-op because the env var below is unset.
func TestHelperFlushBurst(t *testing.T) {
	path := os.Getenv("MONITOR_LOGGER_KILLTEST_PATH")
	if path == "" {
		return
	}
	store, err := OpenStoreWithRetention(path, RetentionPolicy{
		MaxAge:       time.Hour,
		MaxRecords:   DefaultMaxRecords,
		SyncEvery:    DefaultSyncEvery,
		SyncInterval: 200 * time.Millisecond,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "helper open:", err)
		os.Exit(1)
	}
	for i := 0; i < 1500; i++ {
		msg := fmt.Sprintf("burst %d", i)
		if err := store.Append(Entry{Message: msg, Raw: msg}); err != nil {
			fmt.Fprintln(os.Stderr, "helper append:", err)
			os.Exit(1)
		}
	}
	// Go idle and wait to be SIGKILLed by the parent test — no Close, no
	// Sync, so only the background flusher (startFlusher) can have gotten
	// any of the 1500 lines above onto disk.
	select {}
}

// TestFlusherSurvivesKillNineAfterBurst is the kill -9 regression for
// E1.11's freshness requirement: lines written more than SyncInterval before
// an abrupt SIGKILL (no clean Close, unlike every other test in this file)
// must already be on disk, because the periodic background flusher — not a
// process exiting cleanly — is what got them there.
func TestFlusherSurvivesKillNineAfterBurst(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("SIGKILL/process-group semantics differ on windows")
	}
	path := filepath.Join(t.TempDir(), "logs.veclite")
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperFlushBurst$", "-test.v")
	cmd.Env = append(os.Environ(), "MONITOR_LOGGER_KILLTEST_PATH="+path)
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper subprocess: %v", err)
	}
	// Let the helper write its 1500-line burst and let the flusher's
	// 200ms ticker fire multiple times, then SIGKILL well past that —
	// nothing here gives the helper a chance at a clean shutdown.
	time.Sleep(800 * time.Millisecond)
	if err := cmd.Process.Kill(); err != nil {
		t.Fatalf("kill -9 helper subprocess: %v", err)
	}
	_ = cmd.Wait()

	reader, err := OpenReadOnly(path)
	if err != nil {
		t.Fatalf("OpenReadOnly after kill -9: %v", err)
	}
	closeStoreOnCleanup(t, reader)
	got, err := reader.Search("burst", 2000)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) < 1000 {
		t.Fatalf("lines visible after kill -9 = %d of 1500, want at least 1000 (periodic flusher should have persisted most of the burst well before the kill)", len(got))
	}
}

func TestSearchCapsExcessiveLimit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.veclite")
	store, err := OpenStoreWithRetention(path, RetentionPolicy{MaxRecords: MaxSearchLimit + 10})
	if err != nil {
		t.Fatalf("openStore: %v", err)
	}
	closeStoreOnCleanup(t, store)

	for i := 0; i < MaxSearchLimit+1; i++ {
		if err := store.Append(Entry{Message: "match", Raw: "match"}); err != nil {
			t.Fatalf("Append %d: %v", i, err)
		}
	}
	got, err := store.Search("match", MaxSearchLimit+100)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if len(got) != MaxSearchLimit {
		t.Fatalf("Search returned %d, want hard cap %d", len(got), MaxSearchLimit)
	}
}

func TestSearchWithOptionsFiltersMetadataAndTime(t *testing.T) {
	path := filepath.Join(t.TempDir(), "logs.veclite")
	store, err := OpenStore(path)
	if err != nil {
		t.Fatalf("OpenStore: %v", err)
	}
	closeStoreOnCleanup(t, store)

	now := time.Now().Truncate(time.Second)
	entries := []Entry{
		{Timestamp: now.Add(-2 * time.Hour), PID: 10, Process: "api-old", Level: "error", Message: "timeout old", Raw: "ERROR timeout old"},
		{Timestamp: now.Add(-2 * time.Minute), PID: 10, Process: "api-server", Level: "error", Message: "timeout recent", Raw: "ERROR timeout recent"},
		{Timestamp: now.Add(-time.Minute), PID: 11, Process: "worker", Level: "info", Message: "timeout worker", Raw: "INFO timeout worker"},
	}
	for _, entry := range entries {
		if err := store.Append(entry); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}

	got, err := store.SearchWithOptions(SearchOptions{
		Query:   "TIMEOUT",
		Limit:   10,
		Levels:  []string{"ERROR"},
		Process: "API",
		PID:     10,
		Since:   now.Add(-10 * time.Minute),
	})
	if err != nil {
		t.Fatalf("SearchWithOptions: %v", err)
	}
	if len(got) != 1 || got[0].Message != "timeout recent" {
		t.Fatalf("filtered results = %+v, want only recent api error", got)
	}
}

func closeStoreOnCleanup(t *testing.T, store *Store) {
	t.Helper()
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close log store: %v", err)
		}
	})
}
