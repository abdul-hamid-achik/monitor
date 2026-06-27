// Package ecosystem provides thin wrappers around local CLI tools
// (codemap, fcheap, vecgrep, vidtrace, glyphrun, cairntrace, tinyvault,
// veclite, tmux). Each wrapper exposes Available() bool and typed methods
// for the tool's --json output.
//
// Pattern: follow the vidtrace/fcheap wrappers (run(ctx, args) + decodeJSON[T]).
package ecosystem

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
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

// Probe returns the health of every ecosystem tool. The caller's ctx is
// honored: cancelling it interrupts the in-flight --version probes.
func Probe(ctx context.Context) Status {
	if ctx == nil {
		ctx = context.Background()
	}
	return probeAll(ctx)
}

func probeAll(ctx context.Context) Status {
	var s Status
	s.Codemap = probe(ctx, "codemap")
	s.Fcheap = probe(ctx, "fcheap")
	s.Vecgrep = probe(ctx, "vecgrep")
	s.Tinyvault = probe(ctx, "tvault")
	s.Vidtrace = probe(ctx, "vidtrace")
	s.Glyphrun = probe(ctx, "glyph")
	s.Cairntrace = probe(ctx, "cairn")
	s.Veclite = probe(ctx, "veclite")
	s.Tmux = probe(ctx, "tmux")
	return s
}

func probe(ctx context.Context, bin string) ToolStatus {
	path, err := exec.LookPath(bin)
	if err != nil {
		return ToolStatus{Note: bin + " not on PATH"}
	}
	st := ToolStatus{Available: true, Path: path}
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	if out, err := exec.CommandContext(ctx, bin, "--version").CombinedOutput(); err == nil {
		st.Version = strings.TrimSpace(firstLine(string(out)))
	}
	return st
}

// RunGlyphrun shells out to `glyph run <spec>`. The run directory will be
// MONITOR_RUN_DIR for any child processes the spec spawns.
func RunGlyphrun(ctx context.Context, spec string) ([]byte, error) {
	return runJSON(ctx, "glyph", "run", "--format", "json", spec)
}

// TinyvaultRun wraps a command with `tvault run` so secrets land in the
// child's environment without appearing in the agent context. The command
// runs under `env`: with no args it dumps the injected environment as
// KEY=value lines; with a command it execs that command (env passes the
// secrets through). Output is raw bytes (KEY=value text or the command's
// own output), not JSON.
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
	out, err := runJSON(ctx, "fcheap", args...)
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
	out, err := runJSON(ctx, "fcheap", args...)
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
	out, err := runJSON(ctx, "fcheap", "info", id, "--json")
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

// runJSON runs a command expected to emit JSON on stdout, capturing stdout
// and stderr SEPARATELY so a warning/progress line on stderr can't corrupt
// the JSON handed to decodeJSON. On failure the error carries stderr.
func runJSON(ctx context.Context, bin string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if se := strings.TrimSpace(stderr.String()); se != "" {
			return stdout.Bytes(), fmt.Errorf("%s: %w (stderr: %s)", bin, err, se)
		}
		return stdout.Bytes(), fmt.Errorf("%s: %w", bin, err)
	}
	return stdout.Bytes(), nil
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