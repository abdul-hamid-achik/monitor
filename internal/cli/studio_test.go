package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// TestStudioCommandSilencesOwnErrors is the same regression
// TestHotCommandSilencesOwnErrors (hot_test.go) guards: cobra's own
// ExecuteC prints "Error: ..." unless this specific command silences it,
// and cli.Execute() (root.go) always prints its own on top of that.
func TestStudioCommandSilencesOwnErrors(t *testing.T) {
	if !newStudioCmd().SilenceErrors {
		t.Fatal("newStudioCmd().SilenceErrors must be true, or a studio RunE failure prints \"Error: ...\" twice end-to-end")
	}
}

// TestStudioFixtureAndDumpRequireTuimark checks that --fixture/--dump are
// refused without --tuimark (the Bubble Tea studio has no such flags), so
// the failure is a clear, immediate error instead of an ignored flag.
func TestStudioFixtureAndDumpRequireTuimark(t *testing.T) {
	for _, args := range [][]string{
		{"--fixture"},
		{"--dump", "80x24"},
		{"--fixture", "--dump", "80x24"},
	} {
		cmd := newStudioCmd()
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs(args)
		if err := cmd.Execute(); err == nil {
			t.Errorf("args %v: expected an error without --tuimark", args)
		} else if !strings.Contains(err.Error(), "--tuimark") {
			t.Errorf("args %v: expected the error to mention --tuimark, got %q", args, err)
		}
	}
}

// TestStudioTuimarkDumpPrintsAValidFrame is the deterministic test hook the
// glyphrun specs and this test both rely on: `--tuimark --dump COLSxROWS
// --fixture` never touches the live host and prints Dump() JSON of the
// first frame, with zero layout/bind errors, then exits — it never starts
// the interactive Run() loop, so it is safe to call cmd.Execute() here.
func TestStudioTuimarkDumpPrintsAValidFrame(t *testing.T) {
	cmd := newStudioCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--tuimark", "--fixture", "--dump", "100x28"})

	// WriteJSON writes straight to os.Stdout (the repo's convention, same as
	// every other --json command; see TestWriteJSON in cli_test.go), so
	// capture the real stdout instead of the cobra buffer.
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	execErr := cmd.Execute()
	_ = w.Close()
	os.Stdout = old
	stdout, _ := io.ReadAll(r)
	if execErr != nil {
		t.Fatalf("execute: %v", execErr)
	}

	var d struct {
		Cols   int   `json:"cols"`
		Rows   int   `json:"rows"`
		OK     bool  `json:"ok"`
		Errors []any `json:"errors"`
		Grid   []string
	}
	if err := json.Unmarshal(stdout, &d); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, stdout)
	}
	if d.Cols != 100 || d.Rows != 28 {
		t.Errorf("expected a 100x28 dump, got %dx%d", d.Cols, d.Rows)
	}
	if !d.OK || len(d.Errors) != 0 {
		t.Errorf("expected an error-free dump, ok=%v errors=%v", d.OK, d.Errors)
	}
}

// TestStudioTuimarkDumpRejectsBadSize checks the --dump size parser's
// error path (no COLSxROWS match) returns a clear error instead of a
// panic or a silent zero-size dump.
func TestStudioTuimarkDumpRejectsBadSize(t *testing.T) {
	cmd := newStudioCmd()
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	cmd.SetArgs([]string{"--tuimark", "--fixture", "--dump", "not-a-size"})
	if err := cmd.Execute(); err == nil {
		t.Fatal("expected an error for a malformed --dump size")
	}
}
