package procbind

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
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
		{"/usr/local/bin/go-plain", false},
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
