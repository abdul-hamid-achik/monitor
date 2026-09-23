package procbind

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// maxDescendantDepth bounds how far BFS descends from a root pid, so a
// pathological process tree (or a PID-reuse race that briefly makes the
// parent graph look cyclic) can never make a descendant walk run forever.
const maxDescendantDepth = 16

// maxDescendantCount bounds the total number of descendants a walk collects,
// for the same reason as maxDescendantDepth: a runaway fork bomb under the
// resolved root must not make `monitor resolve`/`monitor hot` hang or spend
// unbounded memory.
const maxDescendantCount = 4096

// ProcInfo is the cheap identity gopsutil can report for a process without
// the fuller, more expensive enrichment Inspect performs (cmdline, cwd,
// exe, runtime classification). Building a ppid->children map needs only
// PID, PPID and Name; a caller that needs to classify a specific candidate's
// runtime calls Inspect on that one pid, not on every process in the table.
type ProcInfo struct {
	PID  int32
	PPID int32
	Name string
}

// Enumerator lists the live process table. DefaultEnumerator wraps gopsutil;
// tests inject a fake table so tree-building and BFS logic can be pinned
// down without depending on (or being slowed down by) the real host's
// process table.
type Enumerator func(ctx context.Context) ([]ProcInfo, error)

// DefaultEnumerator lists every live process with a SINGLE call to
// gopsutil's process.ProcessesWithContext, reading only Pid/Ppid/Name per
// process. This deliberately avoids gopsutil's process.Process.Children():
// on darwin, Children() re-lists and re-filters the ENTIRE process table on
// every single call, so calling it once per node while walking a tree is
// O(n^2) in the number of live processes. Enumerating once here and letting
// BuildTree index the result into a ppid->children map keeps the whole walk
// O(n).
func DefaultEnumerator(ctx context.Context) ([]ProcInfo, error) {
	procs, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("list processes: %w", err)
	}
	out := make([]ProcInfo, 0, len(procs))
	for _, p := range procs {
		ppid, ppidErr := p.PpidWithContext(ctx)
		if ppidErr != nil {
			continue // exited mid-scan, or permission denied; not usable either way
		}
		name, nameErr := p.NameWithContext(ctx)
		if nameErr != nil {
			name = ""
		}
		out = append(out, ProcInfo{PID: p.Pid, PPID: ppid, Name: name})
	}
	return out, nil
}

// Tree is a process forest built from ONE process-table enumeration and
// indexed ppid->children, for cheap repeated descendant lookups (BuildTree
// does the one expensive-ish enumeration; Descendants/descendantLevels are
// then pure map lookups).
type Tree struct {
	byPID    map[int32]ProcInfo
	children map[int32][]int32
}

// BuildTree enumerates the process table exactly once via list (nil uses
// DefaultEnumerator) and indexes it into a ppid->children map.
func BuildTree(ctx context.Context, list Enumerator) (*Tree, error) {
	if list == nil {
		list = DefaultEnumerator
	}
	procs, err := list(ctx)
	if err != nil {
		return nil, err
	}
	t := &Tree{
		byPID:    make(map[int32]ProcInfo, len(procs)),
		children: make(map[int32][]int32, len(procs)),
	}
	for _, p := range procs {
		t.byPID[p.PID] = p
	}
	for _, p := range procs {
		if p.PPID == 0 || p.PPID == p.PID {
			continue // no parent (a root), or a self-referential artifact
		}
		t.children[p.PPID] = append(t.children[p.PPID], p.PID)
	}
	return t, nil
}

// descendantLevels returns root's descendant PIDs grouped by BFS depth.
// levels[0] is always []int32{root} itself. Bounded by maxDescendantDepth
// and maxDescendantCount (root does not count against the bound). A cycle
// in the parent graph (possible only from a PID-reuse race mid-enumeration)
// cannot cause an infinite walk: each pid is only ever enqueued once, via
// the seen set.
func (t *Tree) descendantLevels(root int32) [][]int32 {
	if t == nil {
		return [][]int32{{root}}
	}
	levels := [][]int32{{root}}
	seen := map[int32]bool{root: true}
	total := 0
	queue := []int32{root}
	for depth := 0; depth < maxDescendantDepth && len(queue) > 0 && total < maxDescendantCount; depth++ {
		var next []int32
		for _, pid := range queue {
			for _, childPID := range t.children[pid] {
				if seen[childPID] {
					continue
				}
				seen[childPID] = true
				next = append(next, childPID)
				total++
				if total >= maxDescendantCount {
					break
				}
			}
			if total >= maxDescendantCount {
				break
			}
		}
		if len(next) == 0 {
			break
		}
		levels = append(levels, next)
		queue = next
	}
	return levels
}

// Descendants returns root's descendants in breadth-first order (nearest
// first), bounded by maxDescendantDepth and maxDescendantCount. root itself
// is never included. A pid with no known descendants (already exited, a
// leaf process, or a pid the tree never saw) returns an empty slice, not an
// error: "no descendants" is a normal, expected result, not a failure.
func (t *Tree) Descendants(root int32) []ProcInfo {
	levels := t.descendantLevels(root)
	var out []ProcInfo
	for i, level := range levels {
		if i == 0 {
			continue // levels[0] is root itself
		}
		for _, pid := range level {
			if t != nil {
				if info, ok := t.byPID[pid]; ok {
					out = append(out, info)
					continue
				}
			}
			out = append(out, ProcInfo{PID: pid})
		}
	}
	return out
}

// Info returns the tree's cheap identity for pid, if the enumeration saw it.
func (t *Tree) Info(pid int32) (ProcInfo, bool) {
	if t == nil {
		return ProcInfo{}, false
	}
	info, ok := t.byPID[pid]
	return info, ok
}

// wrapperNames are short process names that never host application code
// themselves: shells, package-manager launchers, and TS-execution shims.
// ResolveLeaf skips these as leaf candidates and looks at their children
// instead. Most of these already classify as RuntimeUnknown via
// classifyRuntime, so this list mostly documents intent; the one name that
// does NOT classify as Unknown is "go" (see isGoToolchainWrapper), which is
// deliberately handled separately since skipping it unconditionally would
// also skip a real compiled binary that happens to be named "go".
var wrapperNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "env": true,
	"yarn": true, "npm": true, "npx": true, "pnpm": true,
	"tsx": true, "ts-node": true,
}

func isWrapperName(name string) bool {
	return wrapperNames[strings.ToLower(filepath.Base(name))]
}

// isGoToolchainWrapper reports whether binding is the `go` toolchain itself
// fronting a `go run` invocation, as opposed to a compiled binary that
// happens to be named "go". classifyRuntime (bind.go) already classifies a
// bare "go" process name as RuntimeGo, which is correct for a
// statically-compiled service literally named "go" but wrong here: under
// `go run`, the live "go" process is the build/launch tool, and the actual
// application runs as its child (see looksLikeGoRunBinary). Gating on
// cmdline[1] == "run" keeps this from misfiring on `go build`, `go test`,
// or a real Go binary that happens to be named "go".
func isGoToolchainWrapper(info ProcInfo, binding Binding) bool {
	if strings.ToLower(filepath.Base(info.Name)) != "go" {
		return false
	}
	return len(binding.Cmdline) > 1 && binding.Cmdline[1] == "run"
}

// looksLikeGoRunBinary reports whether exe sits inside the Go toolchain's
// build cache or a per-invocation build temp directory -- both are known
// locations for the binary `go run` executes, across Go versions/platforms:
// the classic "$TMPDIR/go-buildNNN/b001/exe/<name>" per-invocation temp
// directory, and the build-cache path Go now reuses directly on some
// platforms/versions, "$GOCACHE/<xx>/<hash>-d/<name>" (verified live on
// this machine: Go 1.26.6/darwin, GOCACHE under ~/Library/Caches/go-build).
// classifyRuntime never sees this process as Go on its own, because its
// live process name is the compiled program's own name (e.g. "go-plain"),
// not "go" or "*.test".
func looksLikeGoRunBinary(exe string) bool {
	if exe == "" {
		return false
	}
	return strings.Contains(filepath.ToSlash(exe), "/go-build")
}

func isSupportedLeafRuntime(rt Runtime) bool {
	switch rt {
	case RuntimeNode, RuntimeBun, RuntimeDeno, RuntimePython, RuntimeRuby, RuntimeGo:
		return true
	default:
		return false
	}
}

// Candidate is one leaf-resolution candidate, reported back on an ambiguous
// match so a caller (e.g. `monitor resolve --descendant-of`, which exits 2)
// can print exactly which processes tied instead of guessing.
type Candidate struct {
	PID        int32   `json:"pid"`
	Name       string  `json:"name,omitempty"`
	Runtime    Runtime `json:"runtime"`
	MainScript string  `json:"main_script,omitempty"`
}

// LeafOptions configures ResolveLeaf. The zero value is the common case:
// resolve against the live process table via DefaultEnumerator.
type LeafOptions struct {
	// Enumerator overrides the process-table source; nil uses DefaultEnumerator.
	// Tests inject a fake table here.
	Enumerator Enumerator
}

// AmbiguousLeafError is returned when two or more descendants at the same
// BFS depth classify as distinct runtime candidates. ResolveLeaf refuses to
// guess which one the caller meant; Candidates lists every tied process.
type AmbiguousLeafError struct {
	Candidates []Candidate
}

func (e *AmbiguousLeafError) Error() string {
	parts := make([]string, 0, len(e.Candidates))
	for _, c := range e.Candidates {
		parts = append(parts, fmt.Sprintf("pid=%d runtime=%s main_script=%q", c.PID, c.Runtime, c.MainScript))
	}
	return fmt.Sprintf("ambiguous leaf process (%d candidates): %s", len(e.Candidates), strings.Join(parts, "; "))
}

// ResolveLeaf finds the one process under root that actually runs
// application code, skipping wrapper processes such as a shell, a
// package-manager launcher (yarn/npm/npx/pnpm), a TS-execution shim
// (tsx/ts-node), or the go toolchain fronting `go run`. It enumerates the
// process table once, then walks root's descendants breadth-first (root
// itself first, at depth 0) and returns the first depth at which exactly
// one process classifies as a supported runtime (node, bun, deno, python,
// ruby, or a compiled go binary).
//
// If root itself already classifies as a supported runtime -- the common
// case for a plain `node server.js` or `python app.py` launch -- it is
// returned immediately, without inspecting any children.
//
// If a depth has more than one distinct runtime candidate, ResolveLeaf
// returns an *AmbiguousLeafError (also returned as the second value) rather
// than guessing.
func ResolveLeaf(ctx context.Context, root int32, opts LeafOptions) (Binding, []Candidate, error) {
	tree, err := BuildTree(ctx, opts.Enumerator)
	if err != nil {
		return Binding{}, nil, err
	}

	for _, level := range tree.descendantLevels(root) {
		var matched []Binding
		for _, pid := range level {
			info, known := tree.byPID[pid]
			if known && isWrapperName(info.Name) {
				continue
			}
			binding, inspectErr := Inspect(ctx, pid, "")
			if inspectErr != nil {
				continue // exited between enumeration and inspection
			}
			if isGoToolchainWrapper(info, binding) {
				continue
			}
			if !isSupportedLeafRuntime(binding.Runtime) {
				if !looksLikeGoRunBinary(binding.Exe) {
					continue
				}
				binding.Runtime = RuntimeGo
			}
			matched = append(matched, binding)
		}
		switch len(matched) {
		case 0:
			continue // nothing at this depth; BFS already queued its children
		case 1:
			return matched[0], nil, nil
		default:
			candidates := make([]Candidate, 0, len(matched))
			for _, b := range matched {
				candidates = append(candidates, Candidate{PID: b.PID, Name: b.Name, Runtime: b.Runtime, MainScript: b.MainScript})
			}
			return Binding{}, candidates, &AmbiguousLeafError{Candidates: candidates}
		}
	}
	return Binding{}, nil, fmt.Errorf("no runtime leaf process found under pid %d", root)
}
