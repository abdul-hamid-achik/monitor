package cli

import (
	"encoding/json"
	"io"
	"os"
	"testing"
)

// TestStashCmdSurfacesRegistryIDOnFailure verifies that when fcheap archival
// fails, `monitor stash --json` surfaces the registry_id the caller needs
// for `monitor incidents resume-stash`. fcheap is hidden from PATH (rather
// than stubbed) since newStashCmd calls incidents.Capture directly.
func TestStashCmdSurfacesRegistryIDOnFailure(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// A PATH with no fcheap on it makes incidents.hasFcheap() (exec.LookPath)
	// report unavailable, forcing the registerFailedCapture path.
	t.Setenv("PATH", t.TempDir())

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := newStashCmd()
	cmd.SetArgs([]string{"--json", "--note", "test"})
	err := cmd.Execute()

	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	// newStashCmd swallows the Capture error into the report (RunE returns
	// nil so the JSON is always emitted); it must never propagate here.
	if err != nil {
		t.Fatalf("stash --json: %v", err)
	}

	var report map[string]any
	if uerr := json.Unmarshal(out, &report); uerr != nil {
		t.Fatalf("output not valid JSON: %v (%s)", uerr, out)
	}
	if _, ok := report["stash_error"]; !ok {
		t.Fatalf("expected stash_error with fcheap hidden from PATH; report = %v", report)
	}
	regID, ok := report["registry_id"].(string)
	if !ok || regID == "" {
		t.Errorf("expected a non-empty registry_id; report = %v", report)
	}
}
