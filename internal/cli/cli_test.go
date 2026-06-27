package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

func TestCorrelateProfileSkipsFramesWithoutFileLine(t *testing.T) {
	// Frames lacking a file:line are skipped regardless of codemap presence,
	// so this is deterministic in CI (where codemap/the index may be absent).
	syms := []profiler.Symbol{
		{Func: "a", File: "", Line: 0},
		{Func: "b", File: "x.go", Line: 0},
	}
	if got := correlateProfile(context.Background(), syms); len(got) != 0 {
		t.Errorf("frames without file:line should be skipped; got %v", got)
	}
}

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

// TestProcessCmdFindsCurrentPID is a regression for the bug where
// `monitor process <pid>` read the empty zero-value Snapshot() instead of
// calling Collect(ctx), so it returned "pid N not found" for every PID —
// including ones that exist. The test process's own PID must be found.
func TestProcessCmdFindsCurrentPID(t *testing.T) {
	old := os.Stdout
	_, w, _ := os.Pipe()
	os.Stdout = w

	cmd := newProcessCmd()
	cmd.SetArgs([]string{strconv.Itoa(os.Getpid())})
	err := cmd.Execute()

	_ = w.Close()
	os.Stdout = old
	if err != nil {
		t.Fatalf("process <own pid> should be found, got: %v", err)
	}
}

func TestParsePID(t *testing.T) {
	good := map[string]int32{"1": 1, "123": 123, "2147483647": 2147483647}
	for in, want := range good {
		got, err := parsePID(in)
		if err != nil || got != want {
			t.Errorf("parsePID(%q) = (%d, %v), want (%d, nil)", in, got, err, want)
		}
	}
	// trailing garbage, non-positive, overflow, and whitespace must all fail
	// (fmt.Sscanf "%d" silently accepted "123abc" → wrong-process targeting).
	for _, bad := range []string{"123abc", "abc", "0", "-1", "", " 12", "12 ", "1.5", "99999999999999"} {
		if got, err := parsePID(bad); err == nil {
			t.Errorf("parsePID(%q) = (%d, nil), want an error", bad, got)
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