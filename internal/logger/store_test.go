package logger

import (
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
	defer r.Close()
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
	defer w.Close()

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
