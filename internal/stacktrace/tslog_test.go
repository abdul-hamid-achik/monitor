package stacktrace

import (
	"os"
	"path/filepath"
	"testing"
)

func TestTslogSynthetic(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "synthetic", "tslog.log"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	exs := Detect(string(b))
	if len(exs) != 1 {
		t.Fatalf("got %d exceptions, want 1 (the INFO startup line is not an exception): %+v", len(exs), exs)
	}
	ex := exs[0]
	if ex.Parser != "tslog" {
		t.Fatalf("parser = %q, want tslog", ex.Parser)
	}
	if ex.Type != "ApplicationFailure" || ex.Value != "activity task failed" {
		t.Errorf("outer = %+v", ex)
	}
	if ex.Level != LevelError {
		t.Errorf("level = %q, want error", ex.Level)
	}
	if len(ex.Frames) != 2 || ex.Frames[0].Function != "runActivity" || ex.Frames[0].Lineno != 80 {
		t.Errorf("frames = %+v", ex.Frames)
	}
	if len(ex.Chained) != 1 {
		t.Fatalf("got %d chained, want 1: %+v", len(ex.Chained), ex.Chained)
	}
	cause := ex.Chained[0]
	if cause.Type != "ApplicationFailure" || cause.Value != "root cause: boom" {
		t.Errorf("cause = %+v", cause)
	}
	if len(cause.Frames) != 1 || cause.Frames[0].Function != "doWork" || cause.Frames[0].Lineno != 90 {
		t.Errorf("cause frames = %+v", cause.Frames)
	}
}
