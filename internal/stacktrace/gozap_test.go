package stacktrace

import (
	"os"
	"path/filepath"
	"testing"
)

// TestGoZapStdoutGoldenEvent parses the real, captured stdout of
// examples/polyglot/go-zap-stdout: a zap console error line immediately
// followed by a pkg/errors-style "%+v" dump.
func TestGoZapStdoutGoldenEvent(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("testdata", "dogfood", "go-zap-stdout.stdout.txt"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	exs := Detect(string(b))
	if len(exs) != 1 {
		t.Fatalf("got %d exceptions, want 1: %+v", len(exs), exs)
	}
	ex := exs[0]
	if ex.Parser != "zap" {
		t.Fatalf("parser = %q, want zap", ex.Parser)
	}
	if ex.Value != "request failed" {
		t.Errorf("value = %q, want the zap msg field", ex.Value)
	}
	if ex.Level != LevelError || ex.Handled == nil || !*ex.Handled {
		t.Errorf("level/handled = %s/%v, want error/true", ex.Level, ex.Handled)
	}
	if len(ex.Frames) == 0 {
		t.Fatal("no frames parsed from the pkg/errors trace")
	}
	top := ex.Frames[len(ex.Frames)-1]
	if top.Function != "main.doWork" || top.Lineno != 61 {
		t.Errorf("top frame = %+v, want main.doWork:61", top)
	}
	if top.Filename != "/repo/examples/polyglot/go-zap-stdout/main.go" {
		t.Errorf("top frame filename = %q", top.Filename)
	}
}
