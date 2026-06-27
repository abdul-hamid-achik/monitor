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
	got, err := r.Search("wrong", "keyword", 10)
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

func TestStoreInvalidPath(t *testing.T) {
	if _, err := OpenStore("/no/such/dir/logs.veclite"); err == nil {
		t.Error("expected error for invalid path")
	}
}