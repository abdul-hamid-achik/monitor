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
	// Markers lists which root markers were found (package.json, go.mod, .git).
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
}

// Resolve inspects live processes and returns the one exact match. Command
// lines remain memory-only through Binding.Cmdline and are never included in
// errors or JSON output.
func Resolve(ctx context.Context, opts ResolveOptions) (Binding, error) {
	if opts.Runtime == RuntimeUnknown && opts.CodebaseRoot == "" && opts.MainScriptSuffix == "" {
		return Binding{}, fmt.Errorf("at least one process selector is required")
	}
	processes, err := process.ProcessesWithContext(ctx)
	if err != nil {
		return Binding{}, fmt.Errorf("list processes: %w", err)
	}
	wantRoot := canonicalPath(opts.CodebaseRoot)
	wantSuffix := filepath.Clean(opts.MainScriptSuffix)
	matches := make([]Binding, 0, 2)
	for _, candidate := range processes {
		binding, inspectErr := Inspect(ctx, candidate.Pid, "")
		if inspectErr != nil {
			continue
		}
		if !matchesBinding(binding, opts, wantRoot, wantSuffix) {
			continue
		}
		matches = append(matches, binding)
	}
	sort.Slice(matches, func(i, j int) bool { return matches[i].PID < matches[j].PID })
	if len(matches) == 0 {
		return Binding{}, fmt.Errorf("no process matched runtime=%q codebase_root=%q main_script_suffix=%q", opts.Runtime, opts.CodebaseRoot, opts.MainScriptSuffix)
	}
	if len(matches) > 1 {
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
	b.InspectAddr = extractInspectAddr(b.Cmdline)

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
// pyproject.toml, Cargo.toml, or .git. Returns the first directory that
// contains any marker (preferring the nearest), plus the markers found there.
// If nothing is found, returns ("", nil).
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
	for _, name := range []string{"package.json", "go.mod", "pyproject.toml", "Cargo.toml", ".git"} {
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
		case a0 == "go":
			return RuntimeGo
		}
	}
	return RuntimeUnknown
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
// like a JS/TS/Python entry for interpreter runtimes.
func extractMainScript(rt Runtime, cmdline []string, cwd string) string {
	if len(cmdline) < 2 {
		return ""
	}
	switch rt {
	case RuntimeNode, RuntimeBun, RuntimeDeno, RuntimePython:
	default:
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
			switch arg {
			case "-r", "--require", "--import", "-e", "--eval", "-p", "--print",
				"--inspect", "--inspect-brk", "--inspect-port", "--cpu-prof-dir",
				"--heap-prof-dir", "--diagnostic-dir", "-c", "--config":
				i++
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

// extractInspectAddr parses Node/Bun-style inspect flags from argv.
// Forms: --inspect, --inspect=9229, --inspect=host:port, --inspect-brk[=...],
// --inspect-port=N.
func extractInspectAddr(cmdline []string) string {
	const defaultInspect = "127.0.0.1:9229"
	for i := range cmdline {
		arg := cmdline[i]
		switch {
		case arg == "--inspect" || arg == "--inspect-brk":
			// Optional following host:port token without '='.
			if i+1 < len(cmdline) && !strings.HasPrefix(cmdline[i+1], "-") && looksLikeHostPort(cmdline[i+1]) {
				return normalizeHostPort(cmdline[i+1])
			}
			return defaultInspect
		case strings.HasPrefix(arg, "--inspect="), strings.HasPrefix(arg, "--inspect-brk="):
			val := arg[strings.IndexByte(arg, '=')+1:]
			if val == "" {
				return defaultInspect
			}
			return normalizeHostPort(val)
		case arg == "--inspect-port":
			if i+1 < len(cmdline) {
				return normalizeHostPort(cmdline[i+1])
			}
		case strings.HasPrefix(arg, "--inspect-port="):
			return normalizeHostPort(strings.TrimPrefix(arg, "--inspect-port="))
		}
	}
	// NODE_OPTIONS may carry inspect flags; best-effort read from environ of
	// the *current* process is wrong. Callers that need child env should
	// extend Inspect later. Keep empty.
	return ""
}

func looksLikeHostPort(s string) bool {
	if s == "" {
		return false
	}
	// port only
	if _, err := strconv.Atoi(s); err == nil {
		return true
	}
	// host:port
	if i := strings.LastIndexByte(s, ':'); i > 0 {
		if _, err := strconv.Atoi(s[i+1:]); err == nil {
			return true
		}
	}
	return false
}

func normalizeHostPort(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "127.0.0.1:9229"
	}
	if _, err := strconv.Atoi(s); err == nil {
		return "127.0.0.1:" + s
	}
	if strings.HasPrefix(s, ":") {
		return "127.0.0.1" + s
	}
	// host without port
	if !strings.Contains(s, ":") {
		return s + ":9229"
	}
	return s
}
