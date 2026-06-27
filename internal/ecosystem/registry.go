// Package ecosystem provides thin wrappers around local CLI tools
// (codemap, fcheap, vecgrep, vidtrace, glyphrun, cairntrace, tinyvault,
// veclite, tmux). Each wrapper exposes Available() bool and typed methods
// for the tool's --json output.
//
// Pattern: follow the vidtrace/fcheap wrappers (run(ctx, args) + decodeJSON[T]).
package ecosystem

import (
	"context"
	"encoding/json"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// ToolStatus is the per-tool health entry returned by Status().
type ToolStatus struct {
	Available bool   `json:"available"`
	Version   string `json:"version,omitempty"`
	Path      string `json:"path,omitempty"`
	Note      string `json:"note,omitempty"`
}

// Status is the aggregate report of every tool monitor knows about.
type Status struct {
	Codemap    ToolStatus `json:"codemap"`
	Fcheap     ToolStatus `json:"fcheap"`
	Vecgrep    ToolStatus `json:"vecgrep"`
	Tinyvault  ToolStatus `json:"tinyvault"`
	Vidtrace   ToolStatus `json:"vidtrace"`
	Glyphrun   ToolStatus `json:"glyphrun"`
	Cairntrace ToolStatus `json:"cairntrace"`
	Veclite    ToolStatus `json:"veclite"`
	Tmux       ToolStatus `json:"tmux"`
}

var allTools = []struct {
	bin string
}{
	{"codemap"},
	{"fcheap"},
	{"vecgrep"},
	{"tvault"},
	{"vidtrace"},
	{"glyph"},
	{"cairn"},
	{"veclite"},
	{"tmux"},
}

var (
	statusOnce sync.Once
	statusCache Status
)

// Probe returns the health of every ecosystem tool.
func Probe(ctx context.Context) Status {
	_ = ctx
	return probeAll()
}

func probeAll() Status {
	var s Status
	s.Codemap = probe("codemap")
	s.Fcheap = probe("fcheap")
	s.Vecgrep = probe("vecgrep")
	s.Tinyvault = probe("tvault")
	s.Vidtrace = probe("vidtrace")
	s.Glyphrun = probe("glyph")
	s.Cairntrace = probe("cairn")
	s.Veclite = probe("veclite")
	s.Tmux = probe("tmux")
	return s
}

func probe(bin string) ToolStatus {
	path, err := exec.LookPath(bin)
	if err != nil {
		return ToolStatus{Note: bin + " not on PATH"}
	}
	st := ToolStatus{Available: true, Path: path}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput(); err == nil {
		st.Version = strings.TrimSpace(firstLine(string(out)))
	}
	return st
}

// RunGlyphrun shells out to `glyph run <spec>`. The run directory will be
// MONITOR_RUN_DIR for any child processes the spec spawns.
func RunGlyphrun(ctx context.Context, spec string) ([]byte, error) {
	return run(ctx, "glyph", "run", "--format", "json", spec)
}

// TinyvaultRun wraps a command with tvault run so secrets land in env without
// appearing in the agent context. Returns the merged env as JSON for tests.
func TinyvaultRun(ctx context.Context, project string, args ...string) ([]byte, error) {
	full := append([]string{"run", "--project", project, "--", "env"}, args...)
	return run(ctx, "tvault", full...)
}

// -- fcheap wrappers -------------------------------------------------------
//
// monitor uses fcheap as its incident-stash vault. Each alert (or manual
// `monitor stash`) bundles the current system snapshot + relevant profile
// into a temp dir and shells out to `fcheap save`. The tree-hash tag
// (sha256 of the bundle's serialized contents) provides content-addressed
// dedup: the same incident captured twice produces the same stash ID and
// doesn't double-fill the vault, matching the codemap cache pattern.

// StashSaveResult mirrors the JSON `fcheap save` payload.
type StashSaveResult struct {
	SchemaVersion string `json:"schema_version"`
	ID            string `json:"id"`
	Name          string `json:"name"`
	CreatedAt     string `json:"created_at"`
	Path          string `json:"path"`
	SizeBytes     int64  `json:"size_bytes,omitempty"`
}

// StashSave shells out to `fcheap save <path> --json --tag ... --tool
// monitor` and returns the parsed result. Tags encode the incident
// fingerprint (alert rule, severity, snapshot hash) for later search.
func StashSave(ctx context.Context, path, name string, tags []string, ttl string) (StashSaveResult, error) {
	args := []string{"save", path, "--json", "--name", name, "--tool", "monitor"}
	for _, t := range tags {
		args = append(args, "--tag", t)
	}
	if ttl != "" {
		args = append(args, "--ttl", ttl)
	}
	out, err := run(ctx, "fcheap", args...)
	if err != nil {
		return StashSaveResult{}, &Wrap{Cmd: "fcheap save", Err: err, Output: string(out)}
	}
	return decodeJSON[StashSaveResult](out, "fcheap save")
}

// StashListEntry mirrors the JSON row of `fcheap list --json`.
type StashListEntry struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Tags      []string `json:"tags,omitempty"`
	Tool      string   `json:"tool,omitempty"`
	CreatedAt string   `json:"created_at"`
	SizeBytes int64    `json:"size_bytes,omitempty"`
}

// StashList shells out to `fcheap list --json` filtered by tags.
func StashList(ctx context.Context, tags []string) ([]StashListEntry, error) {
	args := []string{"list", "--json"}
	for _, t := range tags {
		args = append(args, "--tag", t)
	}
	out, err := run(ctx, "fcheap", args...)
	if err != nil {
		return nil, &Wrap{Cmd: "fcheap list", Err: err, Output: string(out)}
	}
	return decodeJSON[[]StashListEntry](out, "fcheap list")
}

// StashInfoEntry shells out to `fcheap info <id> --json` for metadata.
type StashInfoEntry struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Tags      []string `json:"tags,omitempty"`
	Tool      string   `json:"tool,omitempty"`
	CreatedAt string   `json:"created_at"`
	Path      string   `json:"path,omitempty"`
	SizeBytes int64    `json:"size_bytes,omitempty"`
	Manifest  []map[string]any `json:"manifest,omitempty"`
}

func StashInfo(ctx context.Context, id string) (StashInfoEntry, error) {
	out, err := run(ctx, "fcheap", "info", id, "--json")
	if err != nil {
		return StashInfoEntry{}, &Wrap{Cmd: "fcheap info", Err: err, Output: string(out)}
	}
	return decodeJSON[StashInfoEntry](out, "fcheap info")
}

func run(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	out, err := cmd.CombinedOutput()
	return out, err
}

// decodeJSON unmarshals out, wrapping parse errors with the command name.
func decodeJSON[T any](out []byte, cmd string) (T, error) {
	var v T
	if err := json.Unmarshal(out, &v); err != nil {
		return v, &Wrap{Cmd: cmd, Err: err, Output: string(out)}
	}
	return v, nil
}

// Wrap is a JSON-decode error carrying the command and raw output.
type Wrap struct {
	Cmd    string
	Err    error
	Output string
}

func (w *Wrap) Error() string { return w.Cmd + ": " + w.Err.Error() + ": " + w.Output }
func (w *Wrap) Unwrap() error { return w.Err }

func firstLine(s string) string {
	if i := strings.IndexAny(s, "\r\n"); i >= 0 {
		return s[:i]
	}
	return s
}