package cli

import (
	"bytes"
	"io"
	"os"
	"testing"
)

// --yes is retained for CLI compatibility, but safety is a hard invariant:
// it cannot make PID 1 (launchd/init) killable.
func TestKillYesCannotOverrideProtection(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := newKillCmd()
	cmd.SetArgs([]string{"1", "--yes", "--json"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("structured refusal should be emitted without a Cobra error: %v", err)
	}
	_ = w.Close()
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(out, []byte(`"refused": true`)) {
		t.Fatalf("expected protected-process refusal, got: %s", out)
	}
	if bytes.Contains(out, []byte(`"killed": true`)) {
		t.Fatalf("--yes bypassed protected-process safety: %s", out)
	}
}
