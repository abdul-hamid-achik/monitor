package cli

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
	if got := correlateProfile(context.Background(), syms, ""); len(got) != 0 {
		t.Errorf("frames without file:line should be skipped; got %v", got)
	}
}

// TestCorrelateProfileDedupesByFileFuncAndCarriesLineRange: flattenCDPProfile
// now emits one row per hot *line* of a function, so the same (file,func)
// can appear many times (e.g. heavyStringify's lines 3/4/5/6). correlate must
// call codemap symbol-at/impact only once per distinct (file,func) pair and
// still carry codemap's StartLine/EndLine through into every row that shares
// that function.
func TestCorrelateProfileDedupesByFileFuncAndCarriesLineRange(t *testing.T) {
	binDir := t.TempDir()
	callLog := filepath.Join(binDir, "calls.log")
	script := `#!/bin/sh
echo "$@" >> "$CODEMAP_CALL_LOG"
case " $* " in
  *" symbol-at "*) printf '%s' '{"file":"hot.js","line":5,"fqn":"heavyStringify","kind":"function","resolution":"enclosing","indexed":true,"start_line":1,"end_line":9}' ;;
  *" impact "*) printf '%s' '{"symbol":"heavyStringify","found":true,"call_graph":"resolved","direct_callers":["a"],"blast_radius":["a","b"],"tests":[],"untested":true}' ;;
  *) exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "codemap"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEMAP_CALL_LOG", callLog)

	// Three rows, two distinct (file,func) pairs: heavyStringify appears at
	// lines 5 and 4 (its top two hot lines), other appears once.
	syms := []profiler.Symbol{
		{Func: "heavyStringify", File: "hot.js", Line: 5, Weight: 60},
		{Func: "heavyStringify", File: "hot.js", Line: 4, Weight: 30},
		{Func: "other", File: "hot.js", Line: 20, Weight: 10},
	}
	got := correlateProfile(context.Background(), syms, "")
	if len(got) != 3 {
		t.Fatalf("expected 3 correlated rows (one per symbol), got %d: %+v", len(got), got)
	}
	for _, row := range got {
		if row["func"] != "heavyStringify" {
			continue
		}
		if row["start_line"] != 1 || row["end_line"] != 9 {
			t.Errorf("row %+v missing carried start_line/end_line", row)
		}
	}
	raw, err := os.ReadFile(callLog)
	if err != nil {
		t.Fatalf("codemap was never invoked: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	symbolAtCalls := 0
	for _, l := range lines {
		if strings.Contains(l, "symbol-at") {
			symbolAtCalls++
		}
	}
	// Exactly 2 unique (file,func) pairs (hot.js/heavyStringify, hot.js/other)
	// → exactly 2 symbol-at calls, not 3.
	if symbolAtCalls != 2 {
		t.Errorf("symbol-at called %d times, want 2 (deduped by file,func); calls:\n%s", symbolAtCalls, raw)
	}
}

// TestCorrelateProfileScoresByCumWhenPresent: correlate's ranking score used
// to be Weight (flat/self) x blast radius. A pprof-proto symbol whose Cum
// (cumulative) is high but Weight (flat) is near-zero — exactly the
// "wrapper whose hot line is a call site" shape symbolsFromPprof now
// surfaces — must still score by its real cumulative cost, not its flat
// time, or it would rank behind a low-blast, high-flat leaf despite being
// the actual bottleneck.
func TestCorrelateProfileScoresByCumWhenPresent(t *testing.T) {
	binDir := t.TempDir()
	script := `#!/bin/sh
case " $* " in
  *" symbol-at "*) printf '%s' '{"fqn":"main.wrapper","kind":"function","resolution":"enclosing","indexed":true}' ;;
  *" impact "*) printf '%s' '{"symbol":"main.wrapper","found":true,"call_graph":"resolved","direct_callers":[],"blast_radius":["a","b","c","d"],"tests":[]}' ;;
  *) exit 9 ;;
esac
`
	if err := os.WriteFile(filepath.Join(binDir, "codemap"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	syms := []profiler.Symbol{
		{Func: "main.wrapper", File: "main.go", Line: 19, Weight: 0.5, Cum: 95},
	}
	got := correlateProfile(context.Background(), syms, "")
	if len(got) != 1 {
		t.Fatalf("expected 1 correlated row, got %d: %+v", len(got), got)
	}
	row := got[0]
	if row["cum_pct"] != 95.0 {
		t.Errorf("row[cum_pct] = %v, want 95", row["cum_pct"])
	}
	score, ok := row["score"].(float64)
	if !ok {
		t.Fatalf("row[score] = %v (%T), want float64", row["score"], row["score"])
	}
	// blast=4; Cum(95) x 4 = 380, not Weight(0.5) x 4 = 2.
	if score < 379 || score > 381 {
		t.Errorf("score = %v, want ~380 (Cum x blast, not Weight x blast)", score)
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

func TestWriteNDJSONConcurrentLinesRemainValid(t *testing.T) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	defer func() { os.Stdout = old }()

	const count = 32
	var wg sync.WaitGroup
	for i := 0; i < count; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			if err := WriteNDJSON(map[string]int{"n": n}); err != nil {
				t.Errorf("WriteNDJSON: %v", err)
			}
		}(i)
	}
	wg.Wait()
	_ = w.Close()
	os.Stdout = old

	lines := 0
	scanner := bufio.NewScanner(r)
	for scanner.Scan() {
		var value map[string]int
		if err := json.Unmarshal(scanner.Bytes(), &value); err != nil {
			t.Fatalf("interleaved NDJSON line %q: %v", scanner.Text(), err)
		}
		lines++
	}
	if err := scanner.Err(); err != nil {
		t.Fatal(err)
	}
	if lines != count {
		t.Fatalf("lines = %d, want %d", lines, count)
	}
}

func TestRootCommandHasSubcommands(t *testing.T) {
	root := Root()
	want := []string{"snapshot", "watch", "kill", "process", "processes", "config", "doctor", "mcp", "logs", "profile", "investigate", "run"}
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
