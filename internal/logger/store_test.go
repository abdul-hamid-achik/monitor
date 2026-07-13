package logger

import (
	"os"
	"path/filepath"
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
