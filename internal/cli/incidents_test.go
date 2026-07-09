package cli

import (
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// TestIncidentsCommandTree verifies the incidents command is wired with its
// "incident" alias and pending/resume-stash subcommands, each documenting
// --json. Per CLAUDE.md, never call cobra.Execute() on the root in tests.
func TestIncidentsCommandTree(t *testing.T) {
	root := Root()
	for _, c := range root.Commands() {
		if c.Name() == "incidents" {
			found := false
			for _, alias := range c.Aliases {
				if alias == "incident" {
					found = true
				}
			}
			if !found {
				t.Errorf("incidents command should have the \"incident\" alias; got %v", c.Aliases)
			}
			if c.Flags().Lookup("json") == nil {
				t.Error("incidents should document --json")
			}
			subs := map[string]bool{}
			for _, sc := range c.Commands() {
				subs[sc.Name()] = true
				if sc.Flags().Lookup("json") == nil {
					t.Errorf("subcommand %q should document --json", sc.Name())
				}
			}
			if !subs["pending"] {
				t.Error("incidents is missing the \"pending\" subcommand")
			}
			if !subs["resume-stash"] {
				t.Error("incidents is missing the \"resume-stash\" subcommand")
			}
			return
		}
	}
	t.Fatal("root has no \"incidents\" command")
}

// TestIncidentsPendingEmptyJSON verifies `incidents pending --json` emits a
// bare `[]`, never `null`, when the registry is empty.
func TestIncidentsPendingEmptyJSON(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())

	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()

	cmd := newIncidentsPendingCmd()
	cmd.SetArgs([]string{"--json"})
	err := cmd.Execute()

	_ = w.Close()
	os.Stdout = old
	out, _ := io.ReadAll(r)

	if err != nil {
		t.Fatalf("incidents pending --json: %v", err)
	}
	if strings.TrimSpace(string(out)) != "[]" {
		var v any
		if jerr := json.Unmarshal(out, &v); jerr != nil {
			t.Fatalf("output not valid JSON: %v (%s)", jerr, out)
		}
		arr, ok := v.([]any)
		if !ok {
			t.Fatalf("output should decode to a JSON array; got %T (%s)", v, out)
		}
		if len(arr) != 0 {
			t.Fatalf("expected an empty array; got %d entries", len(arr))
		}
	}
}

// TestIncidentsResumeRequiresArg verifies resume-stash enforces exactly one
// positional argument and fails cleanly (no panic) for an unknown id.
func TestIncidentsResumeRequiresArg(t *testing.T) {
	cmd := newIncidentsResumeCmd()
	cmd.SetArgs([]string{})
	if err := cmd.Execute(); err == nil {
		t.Error("resume-stash with no args should fail (cobra.ExactArgs(1))")
	}

	t.Setenv("XDG_STATE_HOME", t.TempDir())
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w
	cmd2 := newIncidentsResumeCmd()
	cmd2.SetArgs([]string{"nope", "--json"})
	err := cmd2.Execute()
	_ = w.Close()
	os.Stdout = old
	if err == nil || !strings.Contains(err.Error(), "resume-stash") {
		t.Errorf("resume-stash nope --json = %v, want an error mentioning resume-stash", err)
	}
}
