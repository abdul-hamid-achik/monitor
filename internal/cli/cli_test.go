package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
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

func TestBuildForest(t *testing.T) {
	procs := []collector.ProcessInfo{
		{PID: 1, Name: "init", Parent: 0},
		{PID: 10, Name: "shell", Parent: 1},
		{PID: 20, Name: "child", Parent: 10},
		{PID: 30, Name: "orphan", Parent: 999}, // parent absent -> a root
	}
	roots := buildForest(procs, 0)
	if len(roots) != 2 {
		t.Fatalf("roots = %d, want 2 (init + orphan)", len(roots))
	}
	var init1 *treeNode
	for _, r := range roots {
		if r.PID == 1 {
			init1 = r
		}
	}
	if init1 == nil || len(init1.Children) != 1 || init1.Children[0].PID != 10 {
		t.Fatalf("init subtree = %+v", init1)
	}
	if len(init1.Children[0].Children) != 1 || init1.Children[0].Children[0].PID != 20 {
		t.Error("pid 20 should nest under shell 10")
	}
	// Subtree rooted at a specific pid.
	sub := buildForest(procs, 10)
	if len(sub) != 1 || sub[0].PID != 10 || len(sub[0].Children) != 1 {
		t.Errorf("subtree at 10 = %+v", sub)
	}
	if len(buildForest(procs, 12345)) != 0 {
		t.Error("subtree at a missing pid should be empty")
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
