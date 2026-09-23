// Package procbind inspects a live process and binds it to a local codebase
// root for codemap/vecgrep correlation. It is deliberately separate from the
// bulk collector tick path: cmdline/cwd/exe enrichment is relatively expensive
// and only needed for single-PID diagnosis (process / investigate / profile).
package procbind

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/shirou/gopsutil/v4/process"
)

// Runtime classifies the language/runtime of a process.
type Runtime string

const (
	RuntimeUnknown Runtime = "unknown"
	RuntimeNode    Runtime = "node"
	RuntimeBun     Runtime = "bun"
	RuntimeDeno    Runtime = "deno"
	RuntimeGo      Runtime = "go"
	RuntimePython  Runtime = "python"
	// RuntimeRuby covers MRI only (ruby, ruby3.x). JRuby is intentionally out
	// of scope for now.
	RuntimeRuby Runtime = "ruby"
)

// Binding is the process→codebase attachment used by investigate and
// incident bundles. Empty fields are omitted from JSON.
type Binding struct {
	PID  int32  `json:"pid"`
	Name string `json:"name,omitempty"`
	Exe  string `json:"exe,omitempty"`
	Cwd  string `json:"cwd,omitempty"`
	// Cmdline is retained in memory only to derive runtime/script/inspector.
	// It is never serialized because argv commonly contains credentials.
	Cmdline      []string `json:"-"`
	ArgvRedacted bool     `json:"argv_redacted,omitempty"`
	Runtime      Runtime  `json:"runtime"`
	MainScript   string   `json:"main_script,omitempty"`
	CodebaseRoot string   `json:"codebase_root,omitempty"`
	// InspectAddr is a host:port for a Node/Bun/Deno inspector when detected
	// from argv (e.g. --inspect=9229). Empty when unknown.
	InspectAddr string `json:"inspect_addr,omitempty"`
	// Markers lists which root markers were found (package.json, go.mod,
	// pyproject.toml, Cargo.toml, Gemfile, .ruby-version, or .git).
	Markers []string `json:"markers,omitempty"`
	// Limitations collects non-fatal enrichment problems (permission denied, etc.).
	Limitations []string `json:"limitations,omitempty"`
}

// ResolveOptions identifies one process without relying on a mutable PID.
// All supplied fields are ANDed. Resolve refuses ambiguous matches so callers
// never profile a neighboring service by accident.
type ResolveOptions struct {
	Runtime          Runtime
	CodebaseRoot     string
	MainScriptSuffix string
	// DescendantOf, when non-zero, restricts candidate processes to
	// descendants of this pid (never the pid itself) instead of scanning
	// every live process on the host. This both bounds the (relatively
	// expensive, per-candidate) Inspect cost to one process subtree and
	// makes a selector safe against matching a same-named process outside
	// that subtree. See tree.go's Tree.Descendants for how the subtree is
	// computed (one process-table enumeration, not gopsutil's O(n^2)
	// Children()).
	DescendantOf int32
}

// Resolve inspects live processes and returns the one exact match. Command
// lines remain memory-only through Binding.Cmdline and are never included in
// errors or JSON output.
func Resolve(ctx context.Context, opts ResolveOptions) (Binding, error) {
	if opts.Runtime == RuntimeUnknown && opts.CodebaseRoot == "" && opts.MainScriptSuffix == "" && opts.DescendantOf == 0 {
		return Binding{}, fmt.Errorf("at least one process selector is required")
	}
	wantRoot := canonicalPath(opts.CodebaseRoot)
	wantSuffix := filepath.Clean(opts.MainScriptSuffix)
	matches := make([]Binding, 0, 2)

	if opts.DescendantOf != 0 {
		// Apply the SAME leaf-resolution rules ResolveLeaf's BFS uses
		// (tree.go's classifyLeafCandidate) to every descendant before
		// matching it against the caller's selector: without this, a
		// `--descendant-of <pid> --runtime go` still matched the `go run`
		// toolchain process itself (never reclassified to its compiled
		// child), and an npm/yarn wrapper (which classifyRuntime sees as
		// plain "node") collided with its own node child under
		// `--runtime node`, producing a spurious ambiguous match. Filtering
		// here keeps "alone" (ResolveLeaf) and "combined with another
		// selector" (this branch) agreeing on what counts as a real
		// runtime leaf under a given pid.
		tree, err := BuildTree(ctx, nil)
		if err != nil {
			return Binding{}, err
		}
		for _, info := range tree.Descendants(opts.DescendantOf) {
			binding, ok := classifyLeafCandidate(ctx, tree, Inspect, info.PID)
			if !ok {
				continue
			}
			if !matchesBinding(binding, opts, wantRoot, wantSuffix) {
				continue
			}
			matches = append(matches, binding)
		}
	} else {
		processes, err := process.ProcessesWithContext(ctx)
		if err != nil {
			return Binding{}, fmt.Errorf("list processes: %w", err)
		}
		for _, p := range processes {
			binding, inspectErr := Inspect(ctx, p.Pid, "")
			if inspectErr != nil {
				continue
			}
			if !matchesBinding(binding, opts, wantRoot, wantSuffix) {
				continue
			}
			matches = append(matches, binding)
		}
	}

	sort.Slice(matches, func(i, j int) bool { return matches[i].PID < matches[j].PID })
	if len(matches) == 0 {
		if opts.DescendantOf != 0 {
			return Binding{}, fmt.Errorf("no process matched runtime=%q codebase_root=%q main_script_suffix=%q descendant_of=%d", opts.Runtime, opts.CodebaseRoot, opts.MainScriptSuffix, opts.DescendantOf)
		}
		return Binding{}, fmt.Errorf("no process matched runtime=%q codebase_root=%q main_script_suffix=%q", opts.Runtime, opts.CodebaseRoot, opts.MainScriptSuffix)
	}
	if len(matches) > 1 {
		if opts.DescendantOf != 0 {
			// Route through the same typed error ResolveLeaf uses, so the
			// CLI can report exit code 2 with the full candidate list for
			// EITHER shape of `resolve --descendant-of` -- alone or
			// combined with another selector -- rather than only the
			// "alone" leaf-resolution path.
			candidates := make([]Candidate, 0, len(matches))
			for _, match := range matches {
				candidates = append(candidates, Candidate{PID: match.PID, Name: match.Name, Runtime: match.Runtime, MainScript: match.MainScript})
			}
			return Binding{}, &AmbiguousLeafError{Candidates: candidates}
		}
		identities := make([]string, 0, len(matches))
		for _, match := range matches {
			identities = append(identities, fmt.Sprintf("pid=%d name=%q main_script=%q", match.PID, match.Name, match.MainScript))
		}
		return Binding{}, fmt.Errorf("process selector is ambiguous (%d matches): %s", len(matches), strings.Join(identities, "; "))
	}
	return matches[0], nil
}

func matchesBinding(binding Binding, opts ResolveOptions, wantRoot, wantSuffix string) bool {
	if opts.Runtime != RuntimeUnknown && binding.Runtime != opts.Runtime {
		return false
	}
	if wantRoot != "" && canonicalPath(binding.CodebaseRoot) != wantRoot {
		return false
	}
	if wantSuffix != "." && wantSuffix != "" {
		main := filepath.Clean(binding.MainScript)
		if main == "." || (!strings.HasSuffix(main, wantSuffix) && filepath.Base(main) != wantSuffix) {
			return false
		}
	}
	return true
}

func canonicalPath(path string) string {
	if path == "" {
		return ""
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return filepath.Clean(resolved)
	}
	return filepath.Clean(abs)
}

// Inspect reads identity fields for pid and derives runtime + codebase root.
// codebaseOverride, when non-empty, wins over auto-detection and is cleaned
// to an absolute path when possible.
func Inspect(ctx context.Context, pid int32, codebaseOverride string) (Binding, error) {
	if pid <= 0 {
		return Binding{}, fmt.Errorf("invalid pid %d", pid)
	}
	p, err := process.NewProcessWithContext(ctx, pid)
	if err != nil {
		return Binding{}, fmt.Errorf("open pid %d: %w", pid, err)
	}
	b := Binding{PID: pid, Runtime: RuntimeUnknown}

	if name, err := p.NameWithContext(ctx); err == nil {
		b.Name = name
	} else {
		b.Limitations = append(b.Limitations, "name: "+err.Error())
	}
	if exe, err := p.ExeWithContext(ctx); err == nil {
		b.Exe = exe
	} else {
		b.Limitations = append(b.Limitations, "exe: "+err.Error())
	}
	if cwd, err := p.CwdWithContext(ctx); err == nil {
		b.Cwd = cwd
	} else {
		b.Limitations = append(b.Limitations, "cwd: "+err.Error())
	}
	if args, err := p.CmdlineSliceWithContext(ctx); err == nil {
		b.Cmdline = args
		b.ArgvRedacted = len(args) > 0
	} else {
		// Fallback to a single joined string when slice fails.
		if s, err2 := p.CmdlineWithContext(ctx); err2 == nil && s != "" {
			b.Cmdline = strings.Fields(s)
			b.ArgvRedacted = true
		} else {
			b.Limitations = append(b.Limitations, "cmdline: "+err.Error())
		}
	}

	b.Runtime = classifyRuntime(b.Name, b.Exe, b.Cmdline)
	b.MainScript = extractMainScript(b.Runtime, b.Cmdline, b.Cwd)
	b.InspectAddr = extractInspectAddr(b.Runtime, b.Cmdline)

	if codebaseOverride != "" {
		if abs, err := filepath.Abs(codebaseOverride); err == nil {
			b.CodebaseRoot = abs
		} else {
			b.CodebaseRoot = codebaseOverride
		}
		if st, err := os.Stat(b.CodebaseRoot); err != nil || !st.IsDir() {
			b.Limitations = append(b.Limitations, "codebase override is not a directory: "+b.CodebaseRoot)
		}
	} else {
		start := b.Cwd
		if start == "" && b.MainScript != "" {
			start = filepath.Dir(b.MainScript)
		}
		if start != "" {
			root, markers := FindCodebaseRoot(start)
			b.CodebaseRoot = root
			b.Markers = markers
		}
	}
	return b, nil
}

// FindCodebaseRoot walks up from start looking for package.json, go.mod,
// pyproject.toml, Cargo.toml, Gemfile, .ruby-version, or .git. Returns the
// first directory that contains any marker (preferring the nearest), plus
// the markers found there. If nothing is found, returns ("", nil).
func FindCodebaseRoot(start string) (string, []string) {
	dir, err := filepath.Abs(start)
	if err != nil {
		dir = start
	}
	for {
		markers := markersAt(dir)
		if len(markers) > 0 {
			return dir, markers
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

func markersAt(dir string) []string {
	var out []string
	for _, name := range []string{"package.json", "go.mod", "pyproject.toml", "Cargo.toml", "Gemfile", ".ruby-version", ".git"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
			out = append(out, name)
		}
	}
	return out
}

func classifyRuntime(name, exe string, cmdline []string) Runtime {
	base := strings.ToLower(filepath.Base(name))
	if base == "" {
		base = strings.ToLower(filepath.Base(exe))
	}
	// Strip version suffixes like node-20.
	switch {
	case base == "node" || strings.HasPrefix(base, "node") && isNodeish(base):
		return RuntimeNode
	case base == "nodejs":
		return RuntimeNode
	case base == "bun":
		return RuntimeBun
	case base == "deno":
		return RuntimeDeno
	case base == "python" || base == "python3" || strings.HasPrefix(base, "python"):
		return RuntimePython
	case isRubyish(base):
		return RuntimeRuby
	case base == "bundle" && isBundleExecRuby(cmdline):
		return RuntimeRuby
	}
	// Go binaries are often the service name, not "go". Heuristic: no
	// interpreter in argv0 and exe looks like a compiled binary is weak;
	// prefer explicit go tool or known .test suffix.
	if base == "go" || strings.HasSuffix(base, ".test") {
		return RuntimeGo
	}
	// Cmdline argv0 may differ from Name (e.g. Name=node, argv0=/usr/local/bin/node).
	if len(cmdline) > 0 {
		a0 := strings.ToLower(filepath.Base(cmdline[0]))
		switch {
		case a0 == "node" || a0 == "nodejs" || isNodeish(a0):
			return RuntimeNode
		case a0 == "bun":
			return RuntimeBun
		case a0 == "deno":
			return RuntimeDeno
		case a0 == "python" || a0 == "python3" || strings.HasPrefix(a0, "python"):
			return RuntimePython
		case isRubyish(a0):
			return RuntimeRuby
		case a0 == "bundle" && isBundleExecRuby(cmdline):
			return RuntimeRuby
		case a0 == "go":
			return RuntimeGo
		}
	}
	return RuntimeUnknown
}

// isRubyish matches MRI interpreter basenames: "ruby", "ruby3.4", "ruby-3.1"
// (Homebrew/Debian-style versioned binaries). JRuby is intentionally excluded.
func isRubyish(base string) bool {
	if base == "ruby" {
		return true
	}
	if strings.HasPrefix(base, "ruby") {
		rest := strings.TrimPrefix(base, "ruby")
		rest = strings.TrimPrefix(rest, "-")
		if rest != "" {
			if _, err := strconv.Atoi(rest[:1]); err == nil {
				return true
			}
		}
	}
	return false
}

// isBundleExecRuby reports whether cmdline looks like "bundle [flags] exec
// ruby|rails|rake|puma ...". Bundler normally Kernel#execs straight into the
// resolved interpreter (so the live process already looks like plain ruby
// and is caught by isRubyish above); this only matters for the less common
// case where the bundle wrapper is still the live process image.
func isBundleExecRuby(cmdline []string) bool {
	for i := 1; i < len(cmdline); i++ {
		arg := cmdline[i]
		if strings.HasPrefix(arg, "-") {
			continue // bundler flag before "exec", e.g. --gemfile=Gemfile
		}
		if arg != "exec" {
			return false
		}
		if i+1 >= len(cmdline) {
			return false
		}
		next := strings.ToLower(filepath.Base(cmdline[i+1]))
		return isRubyish(next) || next == "rails" || next == "rake" || next == "puma"
	}
	return false
}

func isNodeish(base string) bool {
	if base == "node" || base == "nodejs" {
		return true
	}
	// node20, node-22.1.0
	if strings.HasPrefix(base, "node-") || strings.HasPrefix(base, "node") {
		rest := strings.TrimPrefix(base, "node")
		rest = strings.TrimPrefix(rest, "-")
		if rest == "" {
			return true
		}
		if _, err := strconv.Atoi(rest[:1]); err == nil {
			return true
		}
	}
	return false
}

// extractMainScript returns the first non-flag path-like argument that looks
// like a JS/TS/Python/Ruby entry for interpreter runtimes.
func extractMainScript(rt Runtime, cmdline []string, cwd string) string {
	switch rt {
	case RuntimeNode, RuntimeBun, RuntimeDeno, RuntimePython, RuntimeRuby:
	default:
		return ""
	}
	if rt == RuntimeRuby {
		// Checked before the len(cmdline)<2 guard below: Bundler's proctitle
		// rewrite commonly collapses a live process down to a SINGLE cmdline
		// element (verified live with both Ruby 2.6/Bundler 1.17 and Ruby
		// 3.4/Bundler 2.6 — see extractBundlerProctitleScript), so bailing
		// out early on a short cmdline would silently skip the one shape
		// this branch exists to handle.
		if script := extractBundlerProctitleScript(cmdline, cwd); script != "" {
			return script
		}
	}
	if len(cmdline) < 2 {
		return ""
	}
	for i := 1; i < len(cmdline); i++ {
		arg := cmdline[i]
		if arg == "" {
			continue
		}
		// Flags and their values.
		if strings.HasPrefix(arg, "-") {
			// --require <mod>, -r <mod>, --import <mod>, -e code: skip value.
			// NOTE: --inspect / --inspect-brk / --inspect-wait deliberately do
			// NOT consume the next argv element — in Node, Deno and Bun they
			// take no separate value (only --inspect=host:port carries one),
			// so treating them as value-consuming here silently ate the main
			// script argument that followed a bare --inspect.
			switch arg {
			case "-r", "--require", "--import", "-e", "--eval",
				"--inspect-port", "--cpu-prof-dir",
				"--heap-prof-dir", "--diagnostic-dir":
				// Shared across every interpreter runtime we classify here:
				// each of these always takes a separate value.
				i++
			case "-p", "--print", "-c", "--config":
				// Node's "-p/--print" (evaluate+print) and Python's "-c"
				// (eval) take a value. Ruby's "-p" (autoprint) and "-c"
				// (syntax check only) take NONE, so this flag switch is not
				// shareable as-is: consuming the next element for Ruby ate
				// its real script argument (e.g. `ruby -p script.rb`).
				if rt != RuntimeRuby {
					i++
				}
			case "-I", "-C", "-E":
				// Ruby-only: -I<dir> (load path), -C<dir> (chdir before
				// running) and -E<enc> (external/internal encoding) all
				// take a value. Nothing else in this runtime set defines
				// these flags, and Python in particular has an unrelated
				// "-I" (isolated mode) that takes NO value at all, so this
				// must never fire for a non-Ruby runtime.
				if rt == RuntimeRuby {
					i++
				}
			}
			// --inspect=host:port already consumed as single token.
			continue
		}
		// Skip bare subcommands for package managers invoked via node? rare.
		if arg == "run" || arg == "exec" {
			continue
		}
		if !looksLikeSourceFile(arg, rt) {
			continue
		}
		return resolvePath(arg, cwd)
	}
	return ""
}

// extractBundlerProctitleScript recognizes Bundler's kernel_load rewrite of
// the live process's title. A Bundler-installed bin script with a Ruby
// shebang (the common case for bin/rails, bin/rake, bin/puma) is not exec'd
// into a new process: bundler/cli/exec.rb loads it in-process via
// Kernel#load and then calls Process.setproctitle("#{file} #{args}"). That
// rewrites cmdline[0] into a single whitespace-joined string (e.g.
// "/app/bin/rails server") — the first token of it is the real,
// already-resolved script path and needs no further flag or extension
// interpretation, only cwd-relative resolution.
//
// What happens to cmdline[1:] after the rewrite is NOT reliable enough to
// gate on, and this deliberately does not try: verified live on this
// project's own darwin dev box, system Ruby 2.6.10 + Bundler 1.17.2
// collapses "bundle exec mysvc arg1 arg2" down to a cmdline of length 1
// (nothing at all follows cmdline[0]), while Ruby 3.4.8 + Bundler 2.6.9 on
// the same OS instead leaves 1-6 EXTRA elements that are not blank — they
// are leaked environment-variable strings (e.g. "PATH=...", "PWD=...")
// from past the end of the process's original argv reservation, an
// emulated-setproctitle/gopsutil-parsing artifact, not real argv. Earlier
// revisions of this function required len(cmdline)>=2 and every element
// from index 1 onward to be "", which is exactly backwards: it rejected the
// length-1 shape entirely and rejected the leaked-env-var shape too,
// meaning it never actually fired on any live Bundler-loaded process this
// was tested against. The only signal this now trusts is cmdline[0]
// containing whitespace, which a normal argv[0] (always a bare resolved
// interpreter/binary path) never does.
//
// Processes launched as "bundle exec ruby app.rb" do not hit this path:
// Bundler execs straight into the interpreter for that case (Kernel#exec,
// a real execve), which leaves argv looking exactly like a plain "ruby ..."
// invocation (cmdline[0] == "ruby", no whitespace) and is handled by the
// ordinary flag-walking loop in extractMainScript instead.
func extractBundlerProctitleScript(cmdline []string, cwd string) string {
	if len(cmdline) == 0 || !strings.ContainsAny(cmdline[0], " \t") {
		return ""
	}
	fields := strings.Fields(cmdline[0])
	if len(fields) == 0 {
		return ""
	}
	return resolvePath(fields[0], cwd)
}

func looksLikeSourceFile(arg string, rt Runtime) bool {
	lower := strings.ToLower(arg)
	switch rt {
	case RuntimeNode, RuntimeBun, RuntimeDeno:
		for _, ext := range []string{".js", ".mjs", ".cjs", ".ts", ".tsx", ".jsx", ".mts", ".cts"} {
			if strings.HasSuffix(lower, ext) {
				return true
			}
		}
		// Allow extensionless paths that exist as files later; still accept
		// common entry basenames.
		base := filepath.Base(lower)
		return base == "server" || base == "index" || base == "main" || base == "app"
	case RuntimePython:
		return strings.HasSuffix(lower, ".py")
	case RuntimeRuby:
		// Bundler-installed bin scripts (rails/rake/puma/rackup) commonly
		// have no extension, but a live "bundle exec rails server" process
		// never actually shows this argv shape: Bundler's kernel_load path
		// rewrites the process title before this loop ever runs (see
		// extractBundlerProctitleScript), so guessing a bare "rails"/"rake"/
		// "puma" basename against cwd only produced a path that does not
		// exist and is intentionally not matched here.
		return strings.HasSuffix(lower, ".rb") || strings.HasSuffix(lower, ".ru") || strings.HasSuffix(lower, ".rake")
	default:
		return false
	}
}

func resolvePath(arg, cwd string) string {
	if filepath.IsAbs(arg) {
		return arg
	}
	if cwd == "" {
		return arg
	}
	return filepath.Clean(filepath.Join(cwd, arg))
}

// extractInspectAddr parses Node/Deno/Bun-style inspect flags from argv.
// Forms: --inspect, --inspect=9229, --inspect=host:port, --inspect-brk[=...],
// --inspect-wait[=...], --inspect-port=N.
//
// The bare forms (--inspect, --inspect-brk, --inspect-wait) take NO separate
// value in Node, Deno or Bun — only the "=host:port" form carries an address.
// A following argv token (if any) is the entry script or a script argument,
// never an implicit port, so it is deliberately never consumed here.
func extractInspectAddr(rt Runtime, cmdline []string) string {
	defaultInspect := "127.0.0.1:9229"
	if rt == RuntimeBun {
		// A bare --inspect on Bun listens on ws://localhost:6499/<uuid>
		// (its own startup banner says "localhost"), and on macOS it in
		// fact binds only the IPv6 loopback [::1]:6499 -- not 127.0.0.1.
		// Using the hostname "localhost" here, rather than hardcoding an
		// IPv4 literal, lets a dialer try both address families instead of
		// getting connection-refused against an interface Bun never bound.
		defaultInspect = "localhost:6499"
	}
	for i := range cmdline {
		arg := cmdline[i]
		switch {
		case arg == "--inspect" || arg == "--inspect-brk" || arg == "--inspect-wait":
			return defaultInspect
		case strings.HasPrefix(arg, "--inspect="), strings.HasPrefix(arg, "--inspect-brk="), strings.HasPrefix(arg, "--inspect-wait="):
			val := arg[strings.IndexByte(arg, '=')+1:]
			if val == "" {
				return defaultInspect
			}
			return normalizeHostPort(val, defaultInspect)
		case arg == "--inspect-port":
			if i+1 < len(cmdline) {
				return normalizeHostPort(cmdline[i+1], defaultInspect)
			}
		case strings.HasPrefix(arg, "--inspect-port="):
			return normalizeHostPort(strings.TrimPrefix(arg, "--inspect-port="), defaultInspect)
		}
	}
	// NODE_OPTIONS may carry inspect flags; best-effort read from environ of
	// the *current* process is wrong. Callers that need child env should
	// extend Inspect later. Keep empty.
	return ""
}

func normalizeHostPort(s, defaultAddr string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return defaultAddr
	}
	defaultPort := defaultAddr[strings.LastIndexByte(defaultAddr, ':')+1:]
	if _, err := strconv.Atoi(s); err == nil {
		return "127.0.0.1:" + s
	}
	if strings.HasPrefix(s, ":") {
		return "127.0.0.1" + s
	}
	// host without port
	if !strings.Contains(s, ":") {
		return s + ":" + defaultPort
	}
	return s
}
