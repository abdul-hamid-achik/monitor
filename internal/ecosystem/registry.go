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
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

// RecordScreen records the screen for `seconds` to a temp video file and
// returns its path, using the platform's built-in recorder: `screencapture
// -V` on macOS, ffmpeg x11grab on Linux. The resulting video can be analyzed
// with vidtrace (`vidtrace index` / `vidtrace analyze`). It returns an error
// when no recorder or display is available (headless / CI / no permission),
// so callers can refuse gracefully.
func RecordScreen(ctx context.Context, seconds int) (string, error) {
	if seconds <= 0 {
		seconds = 30
	}
	switch runtime.GOOS {
	case "darwin":
		if _, err := exec.LookPath("screencapture"); err != nil {
			return "", fmt.Errorf("screencapture not available")
		}
		path, err := tempRecordingPath("mov")
		if err != nil {
			return "", err
		}
		// -V N records N seconds of video; -x suppresses the capture sound.
		if err := exec.CommandContext(ctx, "screencapture", "-x", "-V", fmt.Sprintf("%d", seconds), path).Run(); err != nil {
			return "", errors.Join(fmt.Errorf("screencapture: %w", err), os.Remove(path))
		}
		return path, nil
	case "linux":
		if _, err := exec.LookPath("ffmpeg"); err != nil {
			return "", fmt.Errorf("ffmpeg not available")
		}
		display := os.Getenv("DISPLAY")
		if display == "" {
			return "", fmt.Errorf("no X11 DISPLAY for screen recording")
		}
		path, err := tempRecordingPath("mp4")
		if err != nil {
			return "", err
		}
		if err := exec.CommandContext(ctx, "ffmpeg", "-y", "-f", "x11grab", "-t", fmt.Sprintf("%d", seconds), "-i", display, path).Run(); err != nil {
			return "", errors.Join(fmt.Errorf("ffmpeg x11grab: %w", err), os.Remove(path))
		}
		return path, nil
	default:
		return "", fmt.Errorf("screen recording not supported on %s", runtime.GOOS)
	}
}

func tempRecordingPath(ext string) (string, error) {
	f, err := os.CreateTemp("", "monitor-rec-*."+ext)
	if err != nil {
		return "", err
	}
	path := f.Name()
	if err := f.Close(); err != nil {
		return "", errors.Join(fmt.Errorf("close recording temp file: %w", err), os.Remove(path))
	}
	return path, nil
}

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
	runDir := strings.TrimSpace(os.Getenv("MONITOR_RUN_DIR"))
	cleanup := func() {}
	if runDir == "" {
		var err error
		runDir, err = os.MkdirTemp("", "monitor-run-")
		if err != nil {
			return nil, fmt.Errorf("create monitor run directory: %w", err)
		}
		cleanup = func() { _ = os.RemoveAll(runDir) }
	}
	defer cleanup()
	cmd := exec.CommandContext(ctx, "glyph", "run", "--format", "json", spec)
	cmd.Env = append(os.Environ(), "MONITOR=1", "MONITOR_RUN_DIR="+runDir)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), fmt.Errorf("glyph: %w (stderr: %s)", err, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
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

// -- codemap wrappers ------------------------------------------------------

// SymbolAt is the result of `codemap symbol-at <file>:<line> --json`. It maps
// a source position to its enclosing symbol. Resolution is "exact" (the line
// is the symbol's declaration), "enclosing" (inside the symbol's range), or
// "none" (no symbol — e.g. the file/project isn't indexed).
type SymbolAt struct {
	File       string `json:"file"`
	Line       int    `json:"line"`
	Symbol     string `json:"symbol,omitempty"`
	FQN        string `json:"fqn,omitempty"`
	Kind       string `json:"kind,omitempty"`
	StartLine  int    `json:"start_line,omitempty"`
	EndLine    int    `json:"end_line,omitempty"`
	Resolution string `json:"resolution"`
	Indexed    bool   `json:"indexed"`
}

// CodemapAvailable reports whether the codemap binary is on PATH.
func CodemapAvailable() bool {
	_, err := exec.LookPath("codemap")
	return err == nil
}

// CodemapSymbolAt resolves a file:line position to its enclosing symbol via
// `codemap symbol-at`. An unresolved position is a valid result
// (Resolution "none"), not an error; an error is returned only when the
// subprocess or JSON decode fails.
func CodemapSymbolAt(ctx context.Context, file string, line int) (SymbolAt, error) {
	out, err := runJSON(ctx, "codemap", "symbol-at", fmt.Sprintf("%s:%d", file, line), "--json")
	if err != nil {
		return SymbolAt{}, err
	}
	return decodeJSON[SymbolAt](out, "codemap symbol-at")
}

// Impact is the result of `codemap impact --at <file>:<line> --json`: the
// blast radius (transitive callers) and test coverage of the enclosing
// symbol. The caller/blast/test slices are kept raw — only their counts
// matter here.
type Impact struct {
	Symbol        string            `json:"symbol"`
	Found         bool              `json:"found"`
	DirectCallers []json.RawMessage `json:"direct_callers"`
	BlastRadius   []json.RawMessage `json:"blast_radius"`
	Tests         []json.RawMessage `json:"tests"`
	Untested      bool              `json:"untested"`
	CallGraph     string            `json:"call_graph"`
	Resolution    string            `json:"resolution,omitempty"`
	Note          string            `json:"note,omitempty"`
}

// CodemapImpactAt computes the blast radius (transitive callers) and test
// coverage for the symbol enclosing file:line, via `codemap impact --at`.
// depth bounds the blast-radius hops (0 uses codemap's default).
func CodemapImpactAt(ctx context.Context, file string, line, depth int) (Impact, error) {
	args := []string{"impact", "--at", fmt.Sprintf("%s:%d", file, line), "--json"}
	if depth > 0 {
		args = append(args, "--depth", fmt.Sprintf("%d", depth))
	}
	out, err := runJSON(ctx, "codemap", args...)
	if err != nil {
		return Impact{}, err
	}
	return decodeJSON[Impact](out, "codemap impact")
}

// -- fcheap wrappers -------------------------------------------------------
//
// monitor uses fcheap as its incident-stash vault. Each alert (or manual
// `monitor stash`) bundles the current system snapshot + relevant profile
// into a temp dir and shells out to `fcheap save`. Monitor computes a stable
// tree hash as an integrity/fingerprint tag; the fcheap stash ID remains the
// provider's opaque identifier and no deduplication behavior is assumed.

// StashSaveResult mirrors the JSON `fcheap save` payload.
type StashSaveResult struct {
	SchemaVersion string           `json:"schema_version"`
	ID            string           `json:"id"`
	Name          string           `json:"name"`
	CreatedAt     string           `json:"created_at"`
	SourcePath    string           `json:"source_path,omitempty"`
	FileCount     int              `json:"file_count"`
	TotalSize     int64            `json:"total_size"`
	ContentHash   string           `json:"content_hash,omitempty"`
	Files         []StashFileEntry `json:"files,omitempty"`
	Status        string           `json:"status,omitempty"`
	// Path and SizeBytes are compatibility aliases for older Monitor callers
	// and test doubles. file.cheap's wire contract is source_path/total_size.
	Path      string `json:"-"`
	SizeBytes int64  `json:"-"`
}

// StashFileEntry is one file in a file.cheap manifest.
type StashFileEntry struct {
	Path string `json:"path"`
	Size int64  `json:"size"`
	Hash string `json:"hash,omitempty"`
}

// StashSave shells out to `fcheap save <path> --json --tag ... --tool
// monitor` and returns the parsed result. Tags encode the incident
// fingerprint (alert rule, severity, snapshot hash) for later search.
func StashSave(ctx context.Context, path, name string, tags []string, ttl string) (StashSaveResult, error) {
	// Disable post-save auto-compression so exit status remains an atomic
	// save signal. file.cheap can otherwise persist the stash and then exit
	// non-zero because compression failed, causing Monitor to retain a false
	// duplicate in its pending registry.
	args := []string{"save", path, "--json", "--no-compress", "--name", name, "--tool", "monitor"}
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
	res, err := decodeJSON[StashSaveResult](out, "fcheap save")
	if err != nil {
		return StashSaveResult{}, err
	}
	res.Path = res.SourcePath
	res.SizeBytes = res.TotalSize
	return res, nil
}

// StashListEntry mirrors the JSON row of `fcheap list --json`.
type StashListEntry struct {
	ID        string   `json:"id"`
	Name      string   `json:"name"`
	Tags      []string `json:"tags,omitempty"`
	Tool      string   `json:"tool,omitempty"`
	CreatedAt string   `json:"created_at"`
	FileCount int      `json:"file_count"`
	TotalSize int64    `json:"total_size"`
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
	ID          string           `json:"id"`
	Name        string           `json:"name"`
	Tags        []string         `json:"tags,omitempty"`
	Tool        string           `json:"tool,omitempty"`
	CreatedAt   string           `json:"created_at"`
	SourcePath  string           `json:"source_path,omitempty"`
	FileCount   int              `json:"file_count"`
	TotalSize   int64            `json:"total_size"`
	ContentHash string           `json:"content_hash,omitempty"`
	Files       []StashFileEntry `json:"files,omitempty"`
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
