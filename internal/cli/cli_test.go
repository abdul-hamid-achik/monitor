package cli

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"testing"
)

func TestWriteJSON(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()

	if err := WriteJSON(map[string]string{"hello": "world"}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}
	_ = w.Close()
	out, _ := io.ReadAll(r)
	if !bytes.Contains(out, []byte(`"hello"`)) {
		t.Errorf("output missing key: %s", out)
	}
	var m map[string]string
	if err := json.Unmarshal(out, &m); err != nil {
		t.Errorf("output not valid JSON: %v", err)
	}
	if m["hello"] != "world" {
		t.Errorf("got %v, want {world}", m)
	}
}

func TestWriteNDJSON(t *testing.T) {
	old := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w
	defer func() { os.Stdout = old }()

	if err := WriteNDJSON(map[string]int{"n": 7}); err != nil {
		t.Fatalf("WriteNDJSON: %v", err)
	}
	_ = w.Close()
	out, _ := io.ReadAll(r)
	var v map[string]int
	if err := json.Unmarshal(out, &v); err != nil {
		t.Errorf("output not valid JSON: %v", err)
	}
	if v["n"] != 7 {
		t.Errorf("got %v, want n=7", v)
	}
}

func TestRootCommandHasSubcommands(t *testing.T) {
	root := Root()
	want := []string{"snapshot", "watch", "kill", "process", "doctor", "mcp", "logs", "profile", "investigate", "run"}
	have := map[string]bool{}
	for _, c := range root.Commands() {
		have[c.Name()] = true
	}
	for _, name := range want {
		if !have[name] {
			t.Errorf("missing subcommand %q", name)
		}
	}
}

func TestRootHasVersion(t *testing.T) {
	root := Root()
	// cobra only renders version via --version flag which calls os.Exit; verify
	// the template is wired up by checking Version() on the root.
	if root.Version == "" {
		t.Error("root Version should not be empty")
	}
}