package procbind

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
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
// themselves: shells and env. ResolveLeaf skips these as leaf candidates
// and looks at their children instead.
//
// npm, yarn, npx, pnpm, tsx and ts-node are deliberately NOT in this map,
// even though E3.2 names all of them as wrappers to skip: each ships as a
// `#!/usr/bin/env node` script, so the live process's kernel-reported
// short name (what gopsutil's Name/PPID enumeration reads, and what "go"
// would collide with too if it were name-based) is "node", not "npm" or
// "yarn" -- verified live on this project's own dev box (`npm start` and
// real `yarn start` both report Name=="node"). A name-based map entry for
// them would therefore never fire on the exact platform E3.2 exists for.
// isNodeHostedWrapper below detects them by argv instead, the only signal
// that actually distinguishes them from an ordinary `node app.js`. These
// entries stay in the map anyway as a defensive/documentation fallback for
// the rare case a shim genuinely execs as a binary literally named that
// (e.g. some corepack/nvm shim shapes).
var wrapperNames = map[string]bool{
	"sh": true, "bash": true, "zsh": true, "dash": true, "env": true,
	"yarn": true, "npm": true, "npx": true, "pnpm": true,
	"tsx": true, "ts-node": true,
}

func isWrapperName(name string) bool {
	return wrapperNames[strings.ToLower(filepath.Base(name))]
}

// nodeWrapperTitles are the argv[0] values npm and classic yarn (v1) set
// via process.title once running, either bare ("npm", "yarn") or -- since a
// title rewrite on POSIX systems overwrites the process's own argv memory
// region, which is exactly what gopsutil's KERN_PROCARGS2-backed Cmdline()
// read then sees, the identical phenomenon bind.go's
// extractBundlerProctitleScript already handles for Ruby's Bundler --
// collapsed into a single whitespace-joined element together with the
// invoked script/args (e.g. "npm start"). Only the first whitespace field
// of cmdline[0] is checked for exactly this reason.
var nodeWrapperTitles = map[string]bool{
	"npm": true, "yarn": true, "npx": true, "pnpm": true,
	"tsx": true, "ts-node": true,
}

// nodeWrapperScriptBasenames are entry-script basenames npm/npx/pnpm and
// classic yarn's JS launcher ship as, for when no title rewrite happened
// (or gopsutil could not read it) and the wrapper is still visible as an
// ordinary "node /path/to/X ..." argv.
var nodeWrapperScriptBasenames = map[string]bool{
	"npm-cli.js": true, "npx-cli.js": true, "yarn.js": true,
	"pnpm.cjs": true, "pnpm.js": true,
}

// nodeWrapperPathSegments matches entry-script paths whose basename alone
// is not distinctive enough (tsx and ts-node's CLI entry files) or that
// classic yarn also ships as a bin shim rather than a bare *.js file.
var nodeWrapperPathSegments = []string{
	"/yarn/bin/yarn", "tsx/dist/cli.mjs", "tsx/dist/cli.cjs",
	"ts-node/dist/bin.js", "ts-node/dist/bin-esm.js", "ts-node/dist/bin-transpile.js",
}

// isNodeHostedWrapper reports whether a process classifyRuntime already
// sees as plain RuntimeNode is actually npm, yarn, npx, pnpm, tsx or
// ts-node running as a `#!/usr/bin/env node` script -- see the doc comment
// on wrapperNames for why the process's *Name* can never distinguish this
// case. The only source of truth left is argv: either a rewritten process
// title or the path to the wrapper's own entry script.
func isNodeHostedWrapper(cmdline []string) bool {
	if len(cmdline) == 0 {
		return false
	}
	if fields := strings.Fields(cmdline[0]); len(fields) > 0 {
		if nodeWrapperTitles[strings.ToLower(filepath.Base(fields[0]))] {
			return true
		}
	}
	for _, arg := range cmdline {
		if nodeWrapperScriptBasenames[strings.ToLower(filepath.Base(arg))] {
			return true
		}
		lower := strings.ToLower(filepath.ToSlash(arg))
		for _, seg := range nodeWrapperPathSegments {
			if strings.Contains(lower, seg) {
				return true
			}
		}
	}
	return false
}

// isGoToolchainWrapper reports whether binding is the `go` toolchain itself
// fronting a `go run` invocation, as opposed to a compiled binary that
// happens to be named "go". classifyRuntime (bind.go) already classifies a
// bare "go" process name as RuntimeGo, which is correct for a
// statically-compiled service literally named "go" but wrong here: under
// `go run`, the live "go" process is the build/launch tool, and the actual
// application runs as its child (see looksLikeGoRunBinary). Gating on the
// "run" subcommand (past a leading "-C dir"/"-C=dir" -- the only flag `go`
// allows before its subcommand) keeps this from misfiring on `go build`,
// `go test`, or a real Go binary that happens to be named "go".
func isGoToolchainWrapper(info ProcInfo, binding Binding) bool {
	if strings.ToLower(filepath.Base(info.Name)) != "go" {
		return false
	}
	args := binding.Cmdline
	if len(args) < 2 {
		return false
	}
	i := 1
	switch {
	case args[i] == "-C":
		i += 2 // "-C" and its separate directory argument
	case strings.HasPrefix(args[i], "-C="):
		i++
	}
	return i < len(args) && args[i] == "run"
}

// goRunTempDirPattern matches the classic per-invocation build temp
// directory shape `go run` has used across Go versions/platforms:
// "$TMPDIR/go-buildNNN/b001/exe/<name>".
var goRunTempDirPattern = regexp.MustCompile(`/go-build[^/]*/b\d+/exe/[^/]+$`)

// goRunCacheDirPattern matches the build-cache path Go now reuses directly
// on some platforms/versions instead of a fresh temp dir:
// "$GOCACHE/<xx>/<hash>-d/<name>" (verified live on this machine, Go
// 1.26.6/darwin, default GOCACHE under ~/Library/Caches/go-build, and with
// a custom GOCACHE elsewhere). This is matched structurally by the
// two-level "<2 hex chars>/<hex string>-d/<name>" shape rather than by
// hardcoding a GOCACHE location, so it survives GOCACHE being redirected.
var goRunCacheDirPattern = regexp.MustCompile(`/[0-9a-f]{2}/[0-9a-f]{8,64}-d/[^/]+$`)

// looksLikeGoRunBinary reports whether exe sits inside one of the two known
// `go run` build-output locations above. This is a structural path-shape
// match rather than the previous bare `strings.Contains(exe, "/go-build")`
// substring check, for two reasons: a bare substring check missed the
// build-cache shape whenever a *custom* GOCACHE was warm already (its path
// need not contain "/go-build" at all), and it could misclassify an
// unrelated binary that merely happens to live under a directory whose
// *name* contains "go-build" (e.g. "~/src/go-builder/bin/server").
// classifyRuntime never sees this process as Go on its own, because its
// live process name is the compiled program's own name (e.g. "go-plain"),
// not "go" or "*.test".
func looksLikeGoRunBinary(exe string) bool {
	if exe == "" {
		return false
	}
	slash := filepath.ToSlash(exe)
	return goRunTempDirPattern.MatchString(slash) || goRunCacheDirPattern.MatchString(slash)
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

// InspectFunc matches Inspect's signature. LeafOptions.Inspector lets tests
// substitute a fake one alongside a fake Enumerator.
type InspectFunc func(ctx context.Context, pid int32, codebaseOverride string) (Binding, error)

// LeafOptions configures ResolveLeaf. The zero value is the common case:
// resolve against the live process table via DefaultEnumerator, inspecting
// each candidate with the real Inspect.
type LeafOptions struct {
	// Enumerator overrides the process-table source; nil uses DefaultEnumerator.
	// Tests inject a fake table here.
	Enumerator Enumerator
	// Inspector overrides per-candidate enrichment; nil uses Inspect. Tests
	// inject a fake one, together with Enumerator, so the wrapper-skip
	// logic (npm/yarn/npx/pnpm/tsx/ts-node, the go run toolchain) can be
	// pinned down without touching the real process table.
	Inspector InspectFunc
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
	inspector := opts.Inspector
	if inspector == nil {
		inspector = Inspect
	}

	for _, level := range tree.descendantLevels(root) {
		var matched []Binding
		for _, pid := range level {
			binding, ok := classifyLeafCandidate(ctx, tree, inspector, pid)
			if !ok || !isSupportedLeafRuntime(binding.Runtime) {
				continue
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

// classifyLeafCandidate applies E3.2's leaf-resolution rules to one
// candidate pid: it is excluded (ok=false) when it is the calling process
// itself, a name-known wrapper (shell/env), the `go` toolchain fronting
// `go run`, or an npm-family/TS-execution wrapper hosted by node that still
// has at least one child of its own (so its real runtime is expected to
// show up as that child instead -- see isNodeHostedWrapper's doc comment).
// A `go run` compiled child is reclassified to RuntimeGo here, before the
// caller applies any runtime filter, so every caller sees the same
// classification. binding is only meaningful when ok is true.
//
// Shared by ResolveLeaf's BFS and Resolve's DescendantOf candidate
// filtering (bind.go), so both modes agree on what counts as a real
// runtime leaf under a given pid.
func classifyLeafCandidate(ctx context.Context, t *Tree, inspector InspectFunc, pid int32) (Binding, bool) {
	if pid == int32(os.Getpid()) {
		// Never resolve to the calling `monitor` process itself: when
		// monitor runs under `go run`, its own compiled binary lives
		// under the go build cache -- exactly the shape looksLikeGoRunBinary
		// exists to recognize -- so resolving a `go run` ancestor's
		// descendants could otherwise return monitor's OWN pid as the "go"
		// leaf it just so happens to be. Ancestors of this process are not
		// separately special-cased: in every realistic shape (a shell, the
		// go toolchain itself) they are already excluded by the wrapper
		// and go-toolchain checks below.
		return Binding{}, false
	}
	info, known := t.byPID[pid]
	if known && isWrapperName(info.Name) {
		return Binding{}, false
	}
	binding, inspectErr := inspector(ctx, pid, "")
	if inspectErr != nil {
		return Binding{}, false // exited between enumeration and inspection
	}
	if isGoToolchainWrapper(info, binding) {
		return Binding{}, false
	}
	if binding.Runtime == RuntimeNode && len(t.children[pid]) > 0 && isNodeHostedWrapper(binding.Cmdline) {
		return Binding{}, false
	}
	if !isSupportedLeafRuntime(binding.Runtime) && looksLikeGoRunBinary(binding.Exe) {
		binding.Runtime = RuntimeGo
	}
	return binding, true
}
