package procbind

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeEnumerator returns an Enumerator over a fixed, injected process
// table, so tree-building and BFS bounds can be pinned down without
// depending on (or being slowed down by) the real host's process table.
func fakeEnumerator(table []ProcInfo) Enumerator {
	return func(_ context.Context) ([]ProcInfo, error) {
		return table, nil
	}
}

func TestBuildTreeIndexesPpidToChildrenBFSOrder(t *testing.T) {
	table := []ProcInfo{
		{PID: 1, PPID: 0, Name: "init"},
		{PID: 2, PPID: 1, Name: "sh"},
		{PID: 3, PPID: 1, Name: "sh"},
		{PID: 4, PPID: 2, Name: "node"},
		{PID: 5, PPID: 2, Name: "node"},
		{PID: 6, PPID: 3, Name: "python3"},
	}
	tree, err := BuildTree(context.Background(), fakeEnumerator(table))
	if err != nil {
		t.Fatal(err)
	}
	got := tree.Descendants(1)
	wantOrder := []int32{2, 3, 4, 5, 6} // depth-1 (2, 3) before depth-2 (4, 5, 6)
	if len(got) != len(wantOrder) {
		t.Fatalf("Descendants(1) = %d entries, want %d: %+v", len(got), len(wantOrder), got)
	}
	for i, info := range got {
		if info.PID != wantOrder[i] {
			t.Fatalf("Descendants(1)[%d].PID = %d, want %d (full order = %+v)", i, info.PID, wantOrder[i], got)
		}
	}
}

func TestDescendantsExcludesRootAndUnknownPID(t *testing.T) {
	table := []ProcInfo{{PID: 1, PPID: 0, Name: "init"}, {PID: 2, PPID: 1, Name: "sh"}}
	tree, err := BuildTree(context.Background(), fakeEnumerator(table))
	if err != nil {
		t.Fatal(err)
	}
	for _, info := range tree.Descendants(1) {
		if info.PID == 1 {
			t.Fatal("Descendants(1) included the root itself")
		}
	}
	if got := tree.Descendants(999); len(got) != 0 {
		t.Fatalf("Descendants(999) (unknown pid) = %+v, want empty", got)
	}
}

// TestDescendantsBoundedByDepth builds a 19-deep chain (pid N's parent is
// pid N-1, root is pid 1) and asserts the walk stops at maxDescendantDepth
// instead of continuing indefinitely down a pathological process tree.
func TestDescendantsBoundedByDepth(t *testing.T) {
	var table []ProcInfo
	table = append(table, ProcInfo{PID: 1, PPID: 0, Name: "init"})
	for pid := int32(2); pid <= 20; pid++ {
		table = append(table, ProcInfo{PID: pid, PPID: pid - 1, Name: "sh"})
	}
	tree, err := BuildTree(context.Background(), fakeEnumerator(table))
	if err != nil {
		t.Fatal(err)
	}
	got := tree.Descendants(1)
	if len(got) != maxDescendantDepth {
		t.Fatalf("Descendants(1) on a 19-deep chain returned %d entries, want the depth bound %d", len(got), maxDescendantDepth)
	}
	for _, info := range got {
		if info.PID > maxDescendantDepth+1 {
			t.Fatalf("Descendants(1) returned pid %d beyond the depth bound (max expected pid %d)", info.PID, maxDescendantDepth+1)
		}
	}
}

// TestDescendantsBoundedByCount builds a wide fan-out under one root and
// asserts the walk stops at maxDescendantCount instead of collecting every
// one of a fork bomb's children.
func TestDescendantsBoundedByCount(t *testing.T) {
	var table []ProcInfo
	table = append(table, ProcInfo{PID: 1, PPID: 0, Name: "init"})
	const fanout = maxDescendantCount + 500
	for i := int32(0); i < fanout; i++ {
		table = append(table, ProcInfo{PID: 1000 + i, PPID: 1, Name: "worker"})
	}
	tree, err := BuildTree(context.Background(), fakeEnumerator(table))
	if err != nil {
		t.Fatal(err)
	}
	got := tree.Descendants(1)
	if len(got) != maxDescendantCount {
		t.Fatalf("Descendants(1) on a %d-wide fanout returned %d entries, want the count bound %d", fanout, len(got), maxDescendantCount)
	}
}

// TestDescendantsSurvivesPpidCycle guards against a malformed/racy ppid
// chain (2's parent is 3 and 3's parent is 2) hanging BuildTree/Descendants.
// This can never happen from one consistent enumeration, but a PID-reuse
// race mid-scan could in principle produce it, and the seen-set that guards
// BFS must make it a non-issue either way.
func TestDescendantsSurvivesPpidCycle(t *testing.T) {
	table := []ProcInfo{
		{PID: 2, PPID: 3, Name: "a"},
		{PID: 3, PPID: 2, Name: "b"},
	}
	tree, err := BuildTree(context.Background(), fakeEnumerator(table))
	if err != nil {
		t.Fatal(err)
	}
	got := tree.Descendants(2)
	if len(got) != 1 || got[0].PID != 3 {
		t.Fatalf("Descendants(2) on a 2-cycle = %+v, want exactly [pid 3]", got)
	}
}

func TestBuildTreeDefaultEnumeratorSeesThisTestsOwnPID(t *testing.T) {
	tree, err := BuildTree(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := tree.Info(int32(os.Getpid())); !ok {
		t.Fatal("DefaultEnumerator did not see this test's own pid in the live process table")
	}
}

func TestIsWrapperName(t *testing.T) {
	for _, name := range []string{"sh", "bash", "zsh", "dash", "env", "yarn", "npm", "npx", "pnpm", "tsx", "ts-node"} {
		if !isWrapperName(name) {
			t.Errorf("isWrapperName(%q) = false, want true", name)
		}
	}
	for _, name := range []string{"node", "bun", "deno", "python3", "ruby", "go", "myservice"} {
		if isWrapperName(name) {
			t.Errorf("isWrapperName(%q) = true, want false", name)
		}
	}
}

func TestIsGoToolchainWrapper(t *testing.T) {
	toolchain := ProcInfo{Name: "go"}
	if !isGoToolchainWrapper(toolchain, Binding{Cmdline: []string{"go", "run", "."}}) {
		t.Fatal("`go run .` should be classified as the toolchain wrapper")
	}
	if isGoToolchainWrapper(toolchain, Binding{Cmdline: []string{"go", "build", "./..."}}) {
		t.Fatal("`go build` must not be treated as a `go run` wrapper")
	}
	if isGoToolchainWrapper(toolchain, Binding{Cmdline: []string{"go", "test", "./..."}}) {
		t.Fatal("`go test` must not be treated as a `go run` wrapper")
	}
	realBinary := ProcInfo{Name: "myservice"}
	if isGoToolchainWrapper(realBinary, Binding{Cmdline: []string{"myservice", "run"}}) {
		t.Fatal("a real binary named myservice must never be treated as the go toolchain, even if its own argv happens to contain \"run\"")
	}
	// Regression: "-C dir" (and "-C=dir") is the only flag `go` allows
	// before its subcommand, and must be scanned past rather than making
	// isGoToolchainWrapper require cmdline[1] == "run" literally.
	if !isGoToolchainWrapper(toolchain, Binding{Cmdline: []string{"go", "-C", "go-plain", "run", "."}}) {
		t.Fatal("`go -C dir run .` should be classified as the toolchain wrapper")
	}
	if !isGoToolchainWrapper(toolchain, Binding{Cmdline: []string{"go", "-C=go-plain", "run", "."}}) {
		t.Fatal("`go -C=dir run .` should be classified as the toolchain wrapper")
	}
	if isGoToolchainWrapper(toolchain, Binding{Cmdline: []string{"go", "-C", "go-plain", "build", "./..."}}) {
		t.Fatal("`go -C dir build` must not be treated as a `go run` wrapper")
	}
	if isGoToolchainWrapper(toolchain, Binding{Cmdline: []string{"go", "-C"}}) {
		t.Fatal("a dangling `-C` with no directory or subcommand must not be treated as a `go run` wrapper")
	}
}

func TestLooksLikeGoRunBinary(t *testing.T) {
	tests := []struct {
		exe  string
		want bool
	}{
		// The build-cache path this project's own `go run` actually used,
		// verified live: Go 1.26.6/darwin, GOCACHE under ~/Library/Caches/go-build.
		{"/Users/dev/Library/Caches/go-build/f3/f37af6352cf2766186d23dd786dca-d/go-plain", true},
		// The classic per-invocation temp-dir shape from older Go versions/platforms.
		{"/tmp/go-build123456789/b001/exe/go-plain", true},
		// Regression: a CUSTOM, already-warm GOCACHE need not contain
		// "/go-build" anywhere in its path at all -- only the bare
		// substring check used to require that. The structural
		// "<xx>/<hash>-d/<name>" shape must still match regardless of
		// where GOCACHE itself lives.
		{"/tmp/scratch-gocache/83/8352048abcdef0123456789abcdef01-d/go-plain", true},
		{"/usr/local/bin/go-plain", false},
		// Regression: an unrelated binary living under a directory whose
		// NAME merely contains "go-build" (as opposed to the real
		// go-build*/bNNN/exe/ or <xx>/<hash>-d/ shapes) must not be
		// misclassified as a `go run` artifact.
		{"/Users/dev/src/go-builder/bin/server", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := looksLikeGoRunBinary(tt.exe); got != tt.want {
			t.Errorf("looksLikeGoRunBinary(%q) = %v, want %v", tt.exe, got, tt.want)
		}
	}
}

func TestIsSupportedLeafRuntime(t *testing.T) {
	for _, rt := range []Runtime{RuntimeNode, RuntimeBun, RuntimeDeno, RuntimePython, RuntimeRuby, RuntimeGo} {
		if !isSupportedLeafRuntime(rt) {
			t.Errorf("isSupportedLeafRuntime(%q) = false, want true", rt)
		}
	}
	if isSupportedLeafRuntime(RuntimeUnknown) {
		t.Fatal("RuntimeUnknown must never be a supported leaf runtime")
	}
}

// -- Real-process integration tests -----------------------------------

// workloadJSPath returns the absolute path to the shared polyglot fixture
// used across the dogfood suite (also the E3.2/specs/resolve_descendant.yml
// spec's own target). go test always runs with cwd set to the package
// directory, so the relative walk up to examples/polyglot/ is stable
// regardless of the directory `go test ./...` itself was invoked from.
func workloadJSPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "examples", "polyglot", "js", "workload.js"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(path); statErr != nil {
		t.Skipf("workload.js fixture not found: %v", statErr)
	}
	return path
}

// goPlainDirPath returns the absolute path to the standalone go-plain
// fixture module (its own go.mod keeps it out of monitor's own build).
func goPlainDirPath(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "examples", "polyglot", "go-plain"))
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(filepath.Join(path, "main.go")); statErr != nil {
		t.Skipf("go-plain fixture not found: %v", statErr)
	}
	return path
}

// killProcessGroup kills the whole process group rooted at pid, matching
// the Setpgid:true each spawn below sets — a shell wrapper's own forked
// children (or `go run`'s compiled child) share the group, so a plain
// cmd.Process.Kill() (which only signals the direct child) would leak them.
// Mirrors internal/capture/capture.go's identical pattern for the same
// reason.
func killProcessGroup(t *testing.T, pid int) {
	t.Helper()
	if err := syscall.Kill(-pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		t.Logf("kill process group %d: %v", pid, err)
	}
}

// waitForDescendantCount polls the REAL process table (not a fake one)
// until root has at least want descendants, or fails the test after
// timeout. This waits on cheap structural presence (ppid linkage) rather
// than on ResolveLeaf's own classification, so the two concerns —
// "has the child process actually forked yet" and "does ResolveLeaf
// classify it correctly" — are tested separately instead of conflating a
// slow-starting fixture with a resolution bug.
func waitForDescendantCount(t *testing.T, root int32, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		tree, err := BuildTree(context.Background(), nil)
		if err == nil && len(tree.Descendants(root)) >= want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for pid %d to have >= %d descendants", timeout, root, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// resolveLeafRetry retries ResolveLeaf until it succeeds, returns a
// definitive *AmbiguousLeafError, or timeout elapses. A definitive
// ambiguous result is returned immediately rather than retried away,
// because it is a real answer, not a "not ready yet" transient like a
// child process that has not finished forking.
func resolveLeafRetry(root int32, timeout time.Duration) (Binding, []Candidate, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	var lastCandidates []Candidate
	for {
		binding, candidates, err := ResolveLeaf(context.Background(), root, LeafOptions{})
		if err == nil {
			return binding, nil, nil
		}
		var ambiguous *AmbiguousLeafError
		if errors.As(err, &ambiguous) {
			return Binding{}, candidates, err
		}
		lastErr, lastCandidates = err, candidates
		if time.Now().After(deadline) {
			return Binding{}, lastCandidates, lastErr
		}
		time.Sleep(150 * time.Millisecond)
	}
}

// TestResolveLeafMatchesRootItselfWhenAlreadyARuntime pins down the common
// case: a plain `node server.js` launch, where root itself already
// classifies as a supported runtime and ResolveLeaf must return it
// immediately rather than searching for a child that doesn't exist.
func TestResolveLeafMatchesRootItselfWhenAlreadyARuntime(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	cmd := exec.Command(nodePath, workloadJSPath(t))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn node: %v", err)
	}
	t.Cleanup(func() { killProcessGroup(t, cmd.Process.Pid) })

	root := int32(cmd.Process.Pid)
	binding, candidates, err := resolveLeafRetry(root, 5*time.Second)
	if err != nil {
		t.Fatalf("ResolveLeaf: %v (candidates=%+v)", err, candidates)
	}
	if binding.PID != root || binding.Runtime != RuntimeNode {
		t.Fatalf("ResolveLeaf = %+v, want root pid %d classified as node", binding, root)
	}
}

// TestResolveLeafSkipsShWrapperAndFindsNodeChild is the E3.2 "npm/yarn"
// style wrapper test, simulated with sh -> node (specs/resolve_descendant.yml
// covers the same shape end-to-end through the CLI). The "& wait" forces sh
// to fork a genuine child instead of exec-optimizing into node with the
// SAME pid: verified live on this project's own dev box that a plain
// `sh -c 'node ...'` (no backgrounding) has dash/bash replace their own
// process image via execve() for a single simple command with no further
// shell work left afterward, which would make this test pass trivially at
// depth 0 without ever exercising the wrapper-skip / BFS-descent logic it
// exists to pin down.
func TestResolveLeafSkipsShWrapperAndFindsNodeChild(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	workload := workloadJSPath(t)
	cmd := exec.Command(shPath, "-c", nodePath+" "+workload+" & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sh: %v", err)
	}
	t.Cleanup(func() { killProcessGroup(t, cmd.Process.Pid) })

	root := int32(cmd.Process.Pid)
	waitForDescendantCount(t, root, 1, 5*time.Second)

	binding, candidates, err := resolveLeafRetry(root, 5*time.Second)
	if err != nil {
		t.Fatalf("ResolveLeaf: %v (candidates=%+v)", err, candidates)
	}
	if binding.Runtime != RuntimeNode {
		t.Fatalf("ResolveLeaf runtime = %q, want node (binding=%+v)", binding.Runtime, binding)
	}
	if binding.PID == root {
		t.Fatal("ResolveLeaf resolved the sh wrapper itself instead of its node child")
	}
}

// TestResolveLeafReturnsAmbiguousForTwoRuntimeChildren pins down the "if
// several distinct runtime candidates exist at the same depth, return them
// as ambiguous" requirement: two node children under the same sh wrapper
// must come back as an *AmbiguousLeafError listing both, never a guess.
func TestResolveLeafReturnsAmbiguousForTwoRuntimeChildren(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	workload := workloadJSPath(t)
	script := nodePath + " " + workload + " & " + nodePath + " " + workload + " & wait"
	cmd := exec.Command(shPath, "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sh: %v", err)
	}
	t.Cleanup(func() { killProcessGroup(t, cmd.Process.Pid) })

	root := int32(cmd.Process.Pid)
	waitForDescendantCount(t, root, 2, 5*time.Second)

	_, candidates, err := resolveLeafRetry(root, 5*time.Second)
	var ambiguous *AmbiguousLeafError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("ResolveLeaf error = %v, want *AmbiguousLeafError", err)
	}
	if len(candidates) != 2 {
		t.Fatalf("ambiguous candidates = %+v, want exactly 2", candidates)
	}
	for _, c := range candidates {
		if c.Runtime != RuntimeNode {
			t.Fatalf("candidate %+v runtime != node", c)
		}
	}
}

// TestResolveLeafResolvesCompiledGoRunBinary pins down "a compiled `go run`
// child lives under the go toolchain's temp build dir ... classify it as
// go": `go run .` on examples/polyglot/go-plain must resolve to the
// compiled child, not the `go` toolchain process itself.
func TestResolveLeafResolvesCompiledGoRunBinary(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	goPlainDir := goPlainDirPath(t)
	cmd := exec.Command(goPath, "run", ".")
	cmd.Dir = goPlainDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn go run: %v", err)
	}
	t.Cleanup(func() { killProcessGroup(t, cmd.Process.Pid) })

	root := int32(cmd.Process.Pid)
	// The build (cache-warmed by earlier verification runs, but not
	// guaranteed cold-cache-free in CI) plus process start can take a few
	// seconds, longer than the other fixtures here.
	waitForDescendantCount(t, root, 1, 20*time.Second)

	binding, candidates, err := resolveLeafRetry(root, 15*time.Second)
	if err != nil {
		t.Fatalf("ResolveLeaf: %v (candidates=%+v)", err, candidates)
	}
	if binding.Runtime != RuntimeGo {
		t.Fatalf("ResolveLeaf runtime = %q, want go (binding=%+v)", binding.Runtime, binding)
	}
	if binding.PID == root {
		t.Fatal("ResolveLeaf resolved the go toolchain itself instead of the compiled binary")
	}
	if !looksLikeGoRunBinary(binding.Exe) {
		t.Fatalf("resolved binary's exe %q does not look like a `go run` build artifact", binding.Exe)
	}
}

// -- npm/yarn-family wrapper detection (argv, not process Name) --------

func TestIsNodeHostedWrapper(t *testing.T) {
	tests := []struct {
		name    string
		cmdline []string
		want    bool
	}{
		// Rewritten process titles (process.title = "npm"/"yarn"), which --
		// like Ruby Bundler's kernel_load rewrite handled elsewhere in this
		// package -- collapse the whole visible cmdline into cmdline[0].
		{"npm title with args", []string{"npm start"}, true},
		{"bare npm title", []string{"npm"}, true},
		{"bare yarn title", []string{"yarn"}, true},
		{"yarn title with args", []string{"yarn start"}, true},
		{"npx title", []string{"npx cowsay hi"}, true},
		{"pnpm title", []string{"pnpm run dev"}, true},
		// No title rewrite: the wrapper's own entry script is still
		// visible as an ordinary node argv.
		{"npm-cli.js argv", []string{"node", "/usr/local/lib/node_modules/npm/bin/npm-cli.js", "start"}, true},
		{"npx-cli.js argv", []string{"node", "/opt/npm/bin/npx-cli.js", "cowsay"}, true},
		{"classic yarn.js argv", []string{"node", "/opt/yarn/bin/yarn.js", "start"}, true},
		{"yarn bin shim path", []string{"node", "/usr/local/Cellar/yarn/1.22.22/libexec/bin/yarn.js", "start"}, true},
		{"pnpm.cjs argv", []string{"node", "/opt/pnpm/bin/pnpm.cjs", "run", "dev"}, true},
		{"tsx entry", []string{"node", "/app/node_modules/tsx/dist/cli.mjs", "src/index.ts"}, true},
		{"ts-node entry", []string{"node", "/app/node_modules/.bin/../ts-node/dist/bin.js", "src/index.ts"}, true},
		// Must NOT fire on an ordinary application process.
		{"plain node script", []string{"node", "server.js"}, false},
		{"plain node absolute path", []string{"node", "/app/dist/server.js"}, false},
		{"empty cmdline", nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isNodeHostedWrapper(tt.cmdline); got != tt.want {
				t.Errorf("isNodeHostedWrapper(%v) = %v, want %v", tt.cmdline, got, tt.want)
			}
		})
	}
}

// TestResolveLeafSkipsNodeHostedNpmWrapperViaFakeTable pins the E3.2
// blocker down at the unit level: a fake table whose npm-family wrapper
// process has Name=="node" (the real macOS/Linux shape -- see the doc
// comment on wrapperNames) must still be skipped in favor of its node
// child, using only argv to tell them apart. This is the fake-table
// coverage the wrapper-skip logic previously had none of, now possible
// because LeafOptions.Inspector is injectable.
func TestResolveLeafSkipsNodeHostedNpmWrapperViaFakeTable(t *testing.T) {
	table := []ProcInfo{
		{PID: 1, PPID: 0, Name: "sh"},
		{PID: 2, PPID: 1, Name: "node"}, // npm, title-rewritten to "npm start"
		{PID: 3, PPID: 2, Name: "node"}, // the actual script npm launched
	}
	fakeInspect := func(_ context.Context, pid int32, _ string) (Binding, error) {
		switch pid {
		case 2:
			return Binding{PID: 2, Name: "node", Runtime: RuntimeNode, Cmdline: []string{"npm start"}}, nil
		case 3:
			return Binding{PID: 3, Name: "node", Runtime: RuntimeNode, Cmdline: []string{"node", "/app/server.js"}, MainScript: "/app/server.js"}, nil
		default:
			return Binding{}, fmt.Errorf("unexpected pid %d", pid)
		}
	}
	binding, candidates, err := ResolveLeaf(context.Background(), 1, LeafOptions{
		Enumerator: fakeEnumerator(table),
		Inspector:  fakeInspect,
	})
	if err != nil {
		t.Fatalf("ResolveLeaf: %v (candidates=%+v)", err, candidates)
	}
	if binding.PID != 3 {
		t.Fatalf("ResolveLeaf = %+v, want pid 3 (the node script), not the npm wrapper pid 2", binding)
	}
}

// TestResolveLeafReturnsInProcessWrapperWhenItHasNoChildren pins down "skip
// it only when it has a runtime descendant, so a ts-node that runs the
// script in-process is still returned": a node-hosted wrapper with NO
// children in the tree must be returned as-is rather than excluded into a
// dead end.
func TestResolveLeafReturnsInProcessWrapperWhenItHasNoChildren(t *testing.T) {
	table := []ProcInfo{
		{PID: 1, PPID: 0, Name: "sh"},
		{PID: 2, PPID: 1, Name: "node"}, // ts-node, running the script in-process
	}
	fakeInspect := func(_ context.Context, pid int32, _ string) (Binding, error) {
		if pid == 2 {
			return Binding{PID: 2, Name: "node", Runtime: RuntimeNode, Cmdline: []string{"node", "/app/node_modules/ts-node/dist/bin.js", "app.ts"}}, nil
		}
		return Binding{}, fmt.Errorf("unexpected pid %d", pid)
	}
	binding, candidates, err := ResolveLeaf(context.Background(), 1, LeafOptions{
		Enumerator: fakeEnumerator(table),
		Inspector:  fakeInspect,
	})
	if err != nil {
		t.Fatalf("ResolveLeaf: %v (candidates=%+v)", err, candidates)
	}
	if binding.PID != 2 {
		t.Fatalf("ResolveLeaf = %+v, want pid 2 (the in-process ts-node returned as its own leaf, since it has no children)", binding)
	}
}

// TestResolveLeafNeverReturnsItsOwnPID guards against monitor resolving
// itself as the runtime leaf: when monitor runs under `go run`, its OWN
// compiled binary lives under the go build cache, the exact shape
// looksLikeGoRunBinary exists to recognize. The fake table plants this
// test binary's REAL os.Getpid() as a descendant candidate that would
// otherwise classify as a perfectly valid go leaf, so removing the
// self-pid exclusion makes this test fail rather than vacuously pass.
func TestResolveLeafNeverReturnsItsOwnPID(t *testing.T) {
	self := int32(os.Getpid())
	table := []ProcInfo{
		{PID: 1, PPID: 0, Name: "go"},
		{PID: self, PPID: 1, Name: "monitor"},
	}
	fakeInspect := func(_ context.Context, pid int32, _ string) (Binding, error) {
		if pid == self {
			return Binding{PID: self, Name: "monitor", Runtime: RuntimeGo}, nil
		}
		return Binding{}, fmt.Errorf("unexpected pid %d", pid)
	}
	_, candidates, err := ResolveLeaf(context.Background(), 1, LeafOptions{
		Enumerator: fakeEnumerator(table),
		Inspector:  fakeInspect,
	})
	if err == nil {
		t.Fatalf("ResolveLeaf must never return the calling process's own pid, want an error since no other candidate exists (candidates=%+v)", candidates)
	}
}

// -- Real npm integration (the exact E3.2 done-when scenario) ----------

// resolveRealNodeAndNpmCli locates node's REAL executable path (via
// process.execPath, NOT the "node" PATH entry's own location) and its
// bundled npm-cli.js, skipping the test if either cannot be found. On a
// dev machine where node is managed by a version manager (asdf, nvm, ...),
// the "node"/"npm" PATH entries are commonly resolver shim scripts (which
// fork their own transient subprocesses -- verified live on this
// project's own dev box with asdf -- to pick the active node version
// before exec'ing into the real binaries) rather than the real
// interpreter, and that resolution's own forked helper processes would
// otherwise race with (and can transiently masquerade as) the real
// npm/node process tree the tests below exist to pin down. Running node
// directly on npm's own npm-cli.js, once fully resolved, has exactly the
// process shape a plain `npm start` settles into.
func resolveRealNodeAndNpmCli(t *testing.T) (nodePath, npmCliPath string) {
	t.Helper()
	shimNodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	out, err := exec.Command(shimNodePath, "-e", "process.stdout.write(process.execPath)").Output()
	if err != nil {
		t.Skipf("cannot resolve node's real exec path: %v", err)
	}
	nodePath = strings.TrimSpace(string(out))
	npmCliPath = filepath.Join(filepath.Dir(filepath.Dir(nodePath)), "lib", "node_modules", "npm", "bin", "npm-cli.js")
	if _, statErr := os.Stat(npmCliPath); statErr != nil {
		t.Skipf("npm-cli.js not found next to node (%s): %v", npmCliPath, statErr)
	}
	return nodePath, npmCliPath
}

// startNpmStart spawns `npm start` (specifically, node running npm's own
// npm-cli.js entry script -- see resolveRealNodeAndNpmCli) in a fresh temp
// directory whose package.json's "start" script directly invokes node on
// workload.js, returning the pid (already Setpgid'd, cleaned up via
// t.Cleanup). Used by the ResolveLeaf-alone npm-wrapper test: this is the
// exact live process shape E3.2's done-when names explicitly -- `npm
// start` must resolve to its node child, not npm itself.
func startNpmStart(t *testing.T) int32 {
	t.Helper()
	nodePath, npmCliPath := resolveRealNodeAndNpmCli(t)
	workload := workloadJSPath(t)
	dir := t.TempDir()
	pkg := fmt.Sprintf(`{"name":"resolve-leaf-fixture","version":"1.0.0","private":true,"scripts":{"start":%q}}`, nodePath+" "+workload)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(nodePath, npmCliPath, "start")
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn npm: %v", err)
	}
	t.Cleanup(func() { killProcessGroup(t, cmd.Process.Pid) })
	root := int32(cmd.Process.Pid)
	waitForDescendantCount(t, root, 1, 15*time.Second)
	return root
}

// TestResolveLeafSkipsNpmWrapperAndFindsNodeChild is the real, non-simulated
// E3.2 done-when: "`yarn start` / `npm run dev` resolve to the node child,
// not the wrapper" (roadmap, E3.2). Unlike
// TestResolveLeafSkipsShWrapperAndFindsNodeChild (which simulates the shape
// with sh -> node, a process whose Name already classifies as Unknown and
// so never exercised the argv-based npm detection), this runs a real npm.
func TestResolveLeafSkipsNpmWrapperAndFindsNodeChild(t *testing.T) {
	root := startNpmStart(t)
	binding, candidates, err := resolveLeafRetry(root, 10*time.Second)
	if err != nil {
		t.Fatalf("ResolveLeaf: %v (candidates=%+v)", err, candidates)
	}
	if binding.Runtime != RuntimeNode {
		t.Fatalf("ResolveLeaf runtime = %q, want node (binding=%+v)", binding.Runtime, binding)
	}
	if binding.PID == root {
		t.Fatal("ResolveLeaf resolved the npm wrapper itself instead of its node child")
	}
}

// -- Resolve(..., DescendantOf: ...) tests (moved from bind_test.go; see --
// -- its comment for why E3.2's own tests live here, not there)         --

// TestResolveDescendantOfRestrictsToPidSubtree pins down the E3.2 addition
// to ResolveOptions: DescendantOf must restrict candidate processes to a
// live pid's descendants instead of scanning every process on the host.
// This starts a real "sh -c 'sleep 30 & wait'" (the "& wait" forces sh to
// fork a genuine child instead of exec-optimizing into "sleep" with the
// SAME pid -- the behavior many sh implementations use for a single simple
// command with no further shell work left, verified live on this project's
// own dev box), then resolves with DescendantOf=<sh pid> and no OTHER
// selector. With no runtime, codebase-root or main-script-suffix filter,
// matchesBinding accepts ANY process, so the single result must be the
// "sleep" child -- proving the DescendantOf restriction, not some other
// selector, is what narrowed the match down from every live process to
// exactly one.
func TestResolveDescendantOfRestrictsToPidSubtree(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	sleepPath, err := exec.LookPath("sleep")
	if err != nil {
		t.Skip("sleep not on PATH")
	}
	cmd := exec.Command(shPath, "-c", sleepPath+" 30 & wait")
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sh: %v", err)
	}
	t.Cleanup(func() { killProcessGroup(t, cmd.Process.Pid) })

	root := int32(cmd.Process.Pid)
	ctx := context.Background()
	deadline := time.Now().Add(5 * time.Second)
	var binding Binding
	for {
		// Runtime must be set explicitly to RuntimeUnknown ("unknown"), the
		// documented "no runtime filter" sentinel: the zero value of the
		// Runtime field is the empty string, which matchesBinding treats as
		// a (never-matching) filter for a runtime literally named "", not
		// as "no filter". The CLI's own --runtime flag defaults to the
		// string "unknown" for exactly this reason.
		binding, err = Resolve(ctx, ResolveOptions{Runtime: RuntimeUnknown, DescendantOf: root})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Resolve(DescendantOf=%d): %v", root, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if binding.PID == root {
		t.Fatal("Resolve(DescendantOf) matched the sh wrapper itself, not its sleep child")
	}
	if !strings.Contains(binding.Name, "sleep") {
		t.Fatalf("Resolve(DescendantOf) matched pid %d name=%q, want the sleep child", binding.PID, binding.Name)
	}
}

// TestResolveDescendantOfCountsAsASelector pins down that DescendantOf alone
// (no runtime/codebase-root/main-script-suffix) satisfies the "at least one
// process selector is required" guard, matching --descendant-of being a
// valid `monitor resolve` invocation on its own. Runtime is set explicitly
// to RuntimeUnknown (the zero value is the empty string "", a DIFFERENT,
// always-failing sentinel -- see matchesBinding), matching the CLI's own
// --runtime flag default, and DescendantOf targets a pid vanishingly
// unlikely to exist (so the subtree is empty and this cannot accidentally
// pass by matching a real, unrelated process): this test's only job is to
// prove the guard itself does not fire, not to check any match outcome.
func TestResolveDescendantOfCountsAsASelector(t *testing.T) {
	_, err := Resolve(context.Background(), ResolveOptions{Runtime: RuntimeUnknown, DescendantOf: 999999})
	if err != nil && strings.Contains(err.Error(), "at least one process selector is required") {
		t.Fatalf("DescendantOf alone (Runtime=RuntimeUnknown) should count as a selector, got %v", err)
	}
}

// TestResolveDescendantOfSkipsNpmWrapperForRuntimeFilter pins down the
// "combined mode" half of the E3.2 blocker, reproducing finding 2's exact
// live repro shape: `sh -c 'npm start & wait'` with `--descendant-of <sh>
// --runtime node` used to fail ambiguous (2 matches: npm itself AND its
// node child), because DescendantOf's candidate filtering applied no
// wrapper skip of its own. Root here is the SH pid, not npm's own pid --
// unlike a bare `startNpmStart(t)` root, which tree.Descendants always
// excludes by construction and so could never actually exercise this
// wrapper-skip path in combined mode (npm would never be a candidate
// either way). With npm as a MID-level descendant instead, npm's own
// candidacy is exactly what must be filtered out.
func TestResolveDescendantOfSkipsNpmWrapperForRuntimeFilter(t *testing.T) {
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	nodePath, npmCliPath := resolveRealNodeAndNpmCli(t)
	workload := workloadJSPath(t)
	dir := t.TempDir()
	pkg := fmt.Sprintf(`{"name":"resolve-leaf-fixture","version":"1.0.0","private":true,"scripts":{"start":%q}}`, nodePath+" "+workload)
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(pkg), 0o644); err != nil {
		t.Fatal(err)
	}
	script := nodePath + " " + npmCliPath + " start & wait"
	cmd := exec.Command(shPath, "-c", script)
	cmd.Dir = dir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sh: %v", err)
	}
	t.Cleanup(func() { killProcessGroup(t, cmd.Process.Pid) })
	root := int32(cmd.Process.Pid)
	waitForDescendantCount(t, root, 2, 15*time.Second)

	ctx := context.Background()
	deadline := time.Now().Add(10 * time.Second)
	var binding Binding
	for {
		binding, err = Resolve(ctx, ResolveOptions{Runtime: RuntimeNode, DescendantOf: root})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Resolve(Runtime=node, DescendantOf=%d): %v", root, err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	if binding.MainScript == "" || !strings.HasSuffix(filepath.ToSlash(binding.MainScript), "workload.js") {
		t.Fatalf("Resolve(DescendantOf) combined with --runtime node matched %+v, want the real workload.js child, not the npm wrapper", binding)
	}
}

// TestResolveDescendantOfReclassifiesGoRunCompiledChild pins down the other
// half of the same combined-mode gap: --descendant-of together with
// --runtime go must reclassify the compiled `go run` child the same way
// ResolveLeaf does, not match the `go` toolchain process itself (which
// classifyRuntime already, separately, classifies as RuntimeGo on its own
// process name).
func TestResolveDescendantOfReclassifiesGoRunCompiledChild(t *testing.T) {
	goPath, err := exec.LookPath("go")
	if err != nil {
		t.Skip("go not on PATH")
	}
	goPlainDir := goPlainDirPath(t)
	cmd := exec.Command(goPath, "run", ".")
	cmd.Dir = goPlainDir
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn go run: %v", err)
	}
	t.Cleanup(func() { killProcessGroup(t, cmd.Process.Pid) })

	root := int32(cmd.Process.Pid)
	waitForDescendantCount(t, root, 1, 20*time.Second)

	ctx := context.Background()
	deadline := time.Now().Add(15 * time.Second)
	var binding Binding
	for {
		binding, err = Resolve(ctx, ResolveOptions{Runtime: RuntimeGo, DescendantOf: root})
		if err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("Resolve(Runtime=go, DescendantOf=%d): %v", root, err)
		}
		time.Sleep(150 * time.Millisecond)
	}
	if binding.PID == root {
		t.Fatal("Resolve(DescendantOf) combined with --runtime go matched the go toolchain itself, not the compiled binary")
	}
	if !looksLikeGoRunBinary(binding.Exe) {
		t.Fatalf("resolved binary's exe %q does not look like a `go run` build artifact", binding.Exe)
	}
}

// TestResolveDescendantOfAmbiguousReturnsTypedErrorWithCandidates pins down
// the "exit code 2 with the candidate list" requirement for the COMBINED
// mode, not just the "alone" leaf-resolution path AmbiguousLeafError
// already covered for: two node children under the same sh wrapper, with
// --runtime node added, must come back as *AmbiguousLeafError listing both
// rather than an untyped "process selector is ambiguous" error the CLI
// could only report as a plain exit 1.
func TestResolveDescendantOfAmbiguousReturnsTypedErrorWithCandidates(t *testing.T) {
	nodePath, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not on PATH")
	}
	shPath, err := exec.LookPath("sh")
	if err != nil {
		t.Skip("sh not on PATH")
	}
	workload := workloadJSPath(t)
	script := nodePath + " " + workload + " & " + nodePath + " " + workload + " & wait"
	cmd := exec.Command(shPath, "-c", script)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Skipf("cannot spawn sh: %v", err)
	}
	t.Cleanup(func() { killProcessGroup(t, cmd.Process.Pid) })

	root := int32(cmd.Process.Pid)
	waitForDescendantCount(t, root, 2, 5*time.Second)

	// Retry until the DEFINITIVE final state (both node children fully
	// forked AND exec'd, so both classify as node) is reached, rather than
	// stopping at the first non-"no process matched" result: right after
	// sh forks each backgrounded job, there is a brief fork()-but-not-yet-
	// exec()'d window where a child can still look like its parent shell
	// image, not "node" yet. Stopping on an early, single-match SUCCESS in
	// that window (rather than retrying past it) would latch onto a
	// transient false negative for the ambiguity this test exists to pin
	// down -- verified flaky under `go test -race`, which slows and
	// reorders scheduling enough to land in that window noticeably more
	// often than an unraced run does.
	var rerr error
	deadline := time.Now().Add(8 * time.Second)
	for {
		_, rerr = Resolve(context.Background(), ResolveOptions{Runtime: RuntimeNode, DescendantOf: root})
		var ambiguous *AmbiguousLeafError
		if errors.As(rerr, &ambiguous) {
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	var ambiguous *AmbiguousLeafError
	if !errors.As(rerr, &ambiguous) {
		t.Fatalf("Resolve(DescendantOf) combined with --runtime node error = %v, want *AmbiguousLeafError", rerr)
	}
	if len(ambiguous.Candidates) != 2 {
		t.Fatalf("ambiguous candidates = %+v, want exactly 2", ambiguous.Candidates)
	}
}
