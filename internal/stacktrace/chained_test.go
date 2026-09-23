package stacktrace

import (
	"os"
	"path/filepath"
	"testing"
)

func readChained(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", "chained", name))
	if err != nil {
		t.Fatalf("read chained fixture %s: %v", name, err)
	}
	return string(b)
}

// TestChainedNodeCause verifies Node's `{ cause }` chain: printed
// outer-first (unlike Python), so Chained needs no reversal, but the
// physical text is what's actually normalized here.
func TestChainedNodeCause(t *testing.T) {
	exs := Detect(readChained(t, "node-cause.stderr.txt"))
	if len(exs) != 1 {
		t.Fatalf("got %d exceptions, want 1: %+v", len(exs), exs)
	}
	ex := exs[0]
	if ex.Type != "Error" || ex.Value != "work failed" {
		t.Fatalf("outer exception = %+v, want Error: work failed", ex)
	}
	if ex.Level != LevelFatal {
		t.Errorf("outer level = %q, want fatal", ex.Level)
	}
	if len(ex.Frames) == 0 || ex.Frames[len(ex.Frames)-1].Function != "wrapIt" {
		t.Errorf("outer crash frame = %+v, want wrapIt last", ex.Frames)
	}
	if len(ex.Chained) != 1 {
		t.Fatalf("got %d chained causes, want 1: %+v", len(ex.Chained), ex.Chained)
	}
	cause := ex.Chained[0]
	if cause.Type != "Error" || cause.Value != "root cause boom" {
		t.Errorf("cause = %+v, want Error: root cause boom", cause)
	}
	if len(cause.Frames) == 0 || cause.Frames[len(cause.Frames)-1].Function != "rootCause" {
		t.Errorf("cause crash frame = %+v, want rootCause last", cause.Frames)
	}
}

func testPythonChain(t *testing.T, fixture string) {
	t.Helper()
	exs := Detect(readChained(t, fixture))
	if len(exs) != 1 {
		t.Fatalf("got %d exceptions, want 1: %+v", len(exs), exs)
	}
	ex := exs[0]
	if ex.Type != "RuntimeError" {
		t.Fatalf("outer exception type = %q, want RuntimeError (the final, most-recently-raised one)", ex.Type)
	}
	if ex.Level != LevelFatal || ex.Handled == nil || *ex.Handled {
		t.Errorf("outer exception = %+v, want fatal/unhandled (reaches <module>)", ex)
	}
	if len(ex.Frames) == 0 || ex.Frames[len(ex.Frames)-1].Function != "wrap_it" {
		t.Errorf("outer crash frame = %+v, want wrap_it last", ex.Frames)
	}
	if len(ex.Chained) != 1 {
		t.Fatalf("got %d chained causes, want 1 (the root ValueError, listed even though it printed FIRST): %+v", len(ex.Chained), ex.Chained)
	}
	cause := ex.Chained[0]
	if cause.Type != "ValueError" {
		t.Errorf("cause type = %q, want ValueError", cause.Type)
	}
	if len(cause.Frames) == 0 || cause.Frames[len(cause.Frames)-1].Function != "root_cause" {
		t.Errorf("cause crash frame = %+v, want root_cause last", cause.Frames)
	}
}

func TestChainedPythonDirectCause(t *testing.T) {
	testPythonChain(t, "py-direct-cause.stderr.txt")
}

func TestChainedPythonDuringHandling(t *testing.T) {
	testPythonChain(t, "py-during-handling.stderr.txt")
}
