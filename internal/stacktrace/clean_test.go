package stacktrace

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestCleanLogsProduceNoEvents is the "clean logs -> 0 events" requirement.
// The fixtures are adversarial on purpose: "Error:"/"TypeError:" lines with
// no stack, Bun-like "N |" gutters (rustc, psql, markdown), JSON error lines
// from non-Go loggers (winston, pino, bunyan, structlog, ECS), info-level
// zap/tslog/Ruby Logger/Python logging records, Python warnings, Go test
// failures, "panic" in the middle of a line, and prose "panic ...:" lines
// (e.g. "panic button pressed: deployment 42") right before a SIGQUIT-style
// goroutine dump: the word alone is not a panic report, so the dump is not
// an event.
func TestCleanLogsProduceNoEvents(t *testing.T) {
	dir := filepath.Join("testdata", "clean")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read clean fixtures dir: %v", err)
	}
	if len(entries) < 10 {
		t.Fatalf("only %d clean fixtures, want at least 10", len(entries))
	}
	for _, e := range entries {
		t.Run(e.Name(), func(t *testing.T) {
			text := readFixture(t, "clean/"+e.Name())
			for name, in := range map[string]string{"lf": text, "crlf": strings.ReplaceAll(text, "\n", "\r\n")} {
				if exs := Detect(in); len(exs) != 0 {
					t.Errorf("%s: got %d events from a clean fixture, want 0: %s", name, len(exs), strings.Join(summarizeAll(exs), " | "))
				}
			}
		})
	}
}
