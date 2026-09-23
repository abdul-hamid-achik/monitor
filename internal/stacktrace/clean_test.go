package stacktrace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestCleanLogsProduceNoEvents is the "clean logs -> 0 events" requirement:
// ordinary application chatter that merely contains the word "error" (or
// "warn"/"info" markers) in a non-exception context must never be detected.
func TestCleanLogsProduceNoEvents(t *testing.T) {
	dir := filepath.Join("testdata", "clean")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read clean fixtures dir: %v", err)
	}
	if len(entries) < 5 {
		t.Fatalf("only %d clean fixtures, want at least 5", len(entries))
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		t.Run(e.Name(), func(t *testing.T) {
			b, err := os.ReadFile(filepath.Join(dir, e.Name()))
			if err != nil {
				t.Fatalf("read %s: %v", e.Name(), err)
			}
			exs := Detect(string(b))
			if len(exs) != 0 {
				t.Errorf("got %d exceptions from a clean fixture, want 0: %+v", len(exs), exs)
			}
		})
	}
}

func TestPythonMessageOnly(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "synthetic", "python-logging.stderr.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	exs := Detect(string(b))
	if len(exs) != 2 {
		t.Fatalf("got %d exceptions, want 2 (1 logged traceback + 1 message-only): %+v", len(exs), exs)
	}
	traceback, msgOnly := exs[0], exs[1]
	if traceback.Parser != "python" || traceback.Type != "ValueError" {
		t.Errorf("first exception = %+v, want a python ValueError traceback", traceback)
	}
	if traceback.Handled == nil || !*traceback.Handled {
		t.Errorf("logging.exception traceback should be Handled=true, got %+v", traceback)
	}
	if msgOnly.Parser != "message" {
		t.Fatalf("second exception parser = %q, want %q: %+v", msgOnly.Parser, "message", msgOnly)
	}
	if len(msgOnly.Frames) != 0 {
		t.Errorf("message-only exception has frames: %+v", msgOnly.Frames)
	}
	if msgOnly.Level != LevelError {
		t.Errorf("message-only level = %q, want error", msgOnly.Level)
	}
	if msgOnly.Value != "connection pool exhausted: 0 of 10 connections available" {
		t.Errorf("message-only value = %q", msgOnly.Value)
	}
}
