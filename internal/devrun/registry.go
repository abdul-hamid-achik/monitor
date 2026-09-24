// registry.go implements E3.2's launch service registry
// (`monitor.run-service.v1`, docs/contracts/local-sentry-naming.md §8):
// `monitor run --name <n> -- <cmd>` writes one entry so a later `monitor
// hot <service>` can find the launch's real pid (and, under --inspect, its
// already-discovered inspector port) without the caller re-deriving
// either.
package devrun

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"
)

// RegistrySchema is the launch registry's contract tag (docs/contracts/
// local-sentry-naming.md §1's "Registro de servicios" row).
const RegistrySchema = "monitor.run-service.v1"

// RegistryInspector is one recorded "Debugger listening on ws://..."
// banner (E3.3b). WS -- the full ws:// URL, whose UUID path segment is the
// inspector protocol's only bearer-token-shaped secret -- is written to
// this registry file and this file ONLY: it must never be copied into any
// `monitor run`/`monitor hot` banner, --json output, or MCP payload, all
// of which report Port alone (docs/contracts/local-sentry-naming.md §8).
type RegistryInspector struct {
	// PID is the pid FindListenerPID resolved as Port's owner; 0 when no
	// live listener could be attributed to it (see InspectorBanner.PIDKnown).
	PID  int32  `json:"pid,omitempty"`
	Port int    `json:"port"`
	WS   string `json:"ws,omitempty"`
}

// RegistryEntry is one launch's `monitor.run-service.v1` document.
type RegistryEntry struct {
	Schema     string              `json:"schema"`
	LaunchID   string              `json:"launch_id"`
	PID        int                 `json:"pid"`
	Name       string              `json:"name"`
	Project    string              `json:"project"`
	StartedAt  time.Time           `json:"started_at"`
	Scan       string              `json:"scan"`
	Inspectors []RegistryInspector `json:"inspectors,omitempty"`
}

// monitorStateRoot returns $XDG_STATE_HOME/monitor, falling back to
// ~/.local/state/monitor when XDG_STATE_HOME is unset -- the same
// convention internal/stacktrace's checkpointDir and internal/incidents'
// registryDir already use. Pure path computation, no filesystem side
// effect: see monitorStateSubdir (which DOES create its result) and
// registryRootPath/registryProjectDirPath below (which deliberately do
// not, for a read-only lookup).
func monitorStateRoot() (string, error) {
	root := strings.TrimSpace(os.Getenv("XDG_STATE_HOME"))
	if root == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		root = filepath.Join(home, ".local", "state")
	} else if !filepath.IsAbs(root) {
		return "", fmt.Errorf("XDG_STATE_HOME must be an absolute path: %q", root)
	}
	return filepath.Join(root, "monitor"), nil
}

// monitorStateSubdir returns (creating it, mode 0700)
// $XDG_STATE_HOME/monitor/<sub>. Shared by the launch registry
// ("services"), --profile's exit shim ("shims") and per-launch profile
// output ("profiles/<launch-id>") in profile.go -- every one of those
// callers is about to WRITE into the directory it asks for, so creating it
// is the right default there.
func monitorStateSubdir(sub string) (string, error) {
	base, err := monitorStateRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s directory: %w", sub, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure %s directory: %w", sub, err)
	}
	return dir, nil
}

// registryRoot returns (creating it, mode 0700) $XDG_STATE_HOME/monitor/
// services (see monitorStateSubdir). Used only by a caller that is about
// to write; a read-only lookup uses registryRootPath instead.
func registryRoot() (string, error) {
	return monitorStateSubdir("services")
}

// registryRootPath returns $XDG_STATE_HOME/monitor/services WITHOUT
// creating it: a read-only lookup (ReadRegistryEntry, listing every
// registered service for an "unknown service" error) must never have the
// side effect of creating a services/ directory tree for a project that
// has never registered anything -- verified live: `monitor hot <anything>`
// in a project that had never run `monitor run --name ...` used to create
// an empty services/<project>/ directory just by looking.
func registryRootPath() (string, error) {
	base, err := monitorStateRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "services"), nil
}

// sanitizeRegistryComponent turns an arbitrary --project/--name value into
// a safe single path component: project and name both end up as directory/
// file names under registryRoot(), so neither may be allowed to escape it
// via "/", "\", or a bare "..".
func sanitizeRegistryComponent(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	switch s {
	case "", ".", "..":
		return "_"
	}
	return s
}

// registryProjectDirPath returns the directory holding project's registered
// services, WITHOUT creating it (see registryRootPath's own doc comment
// for why a read-only lookup must not have that side effect).
func registryProjectDirPath(project string) (string, error) {
	root, err := registryRootPath()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, sanitizeRegistryComponent(project)), nil
}

// registryProjectDir returns (creating it, mode 0700) the directory
// holding project's registered services. Used only by WriteRegistryEntry,
// the one caller that is actually about to write into it.
func registryProjectDir(project string) (string, error) {
	dir, err := registryProjectDirPath(project)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create launch registry directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure launch registry directory: %w", err)
	}
	return dir, nil
}

// RegistryPath returns the entry file path for (project, name) WITHOUT
// creating anything: safe for a read-only lookup (ReadRegistryEntry,
// ListRegisteredServiceNames). A caller that is about to WRITE derives its
// own path from registryProjectDir instead (see WriteRegistryEntry), which
// creates the directory first.
func RegistryPath(project, name string) (string, error) {
	dir, err := registryProjectDirPath(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sanitizeRegistryComponent(name)+".json"), nil
}

// ErrServiceNameInUse is WriteRegistryEntry's collision guard: another
// launch's entry for the same (project, name) is already registered AND
// still alive. Returned instead of silently overwriting it -- a caller
// (devrun.go's Run) treats this exactly like any other best-effort
// registration failure (the launch itself is never aborted over it), but
// can tell the difference from an ordinary I/O error to print an honest
// note instead of registering nothing with no explanation at all.
var ErrServiceNameInUse = errors.New("devrun: service name already registered by a running launch")

// WriteRegistryEntry writes entry to its (Project, Name)-derived path,
// atomically (os.CreateTemp in the same directory -- never a single fixed
// "<name>.json.tmp" name, which two concurrent writers for the same
// project/name would race on and could corrupt -- verified live: 7/300
// concurrent-write rounds left an unparseable entry file with the old
// fixed-name approach -- then os.Rename into place, the same pattern
// stacktrace.SaveCheckpoint uses) and mode 0600 (os.CreateTemp's own
// default on Unix).
//
// Refuses (ErrServiceNameInUse) to replace an existing entry for the same
// (project, name) when that entry's own launch_id differs from entry's
// AND its recorded pid is still alive: two DIFFERENT launches must never
// silently collide on the same service name, one overwriting the other's
// registration out from under it (verified live: launch A with `--name w`,
// then launch B with `--name w` while A was still running, left the
// registry pointing at B; `monitor hot w` then failed to find A even
// though A was still alive and running). A stale (dead-pid) or missing
// existing entry, or one this SAME launch itself already wrote (adding
// --inspect's discovered inspectors, say), is not a collision and is
// replaced exactly as before.
//
// Returns the path written, so the caller can remove exactly that file on
// exit (RemoveRegistryEntry) without re-deriving it.
func WriteRegistryEntry(entry RegistryEntry) (string, error) {
	dir, err := registryProjectDir(entry.Project)
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, sanitizeRegistryComponent(entry.Name)+".json")
	if existing, rerr := readRegistryEntryFile(path); rerr == nil {
		if existing.LaunchID != "" && existing.LaunchID != entry.LaunchID && processAlive(existing.PID) {
			return "", ErrServiceNameInUse
		}
	}
	entry.Schema = RegistrySchema
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode launch registry entry: %w", err)
	}
	tmp, err := os.CreateTemp(dir, sanitizeRegistryComponent(entry.Name)+".*.tmp")
	if err != nil {
		return "", fmt.Errorf("create launch registry temp file: %w", err)
	}
	tmpPath := tmp.Name()
	if _, werr := tmp.Write(data); werr != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write launch registry entry: %w", werr)
	}
	if cerr := tmp.Close(); cerr != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("write launch registry entry: %w", cerr)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		_ = os.Remove(tmpPath)
		return "", fmt.Errorf("commit launch registry entry: %w", err)
	}
	return path, nil
}

// RemoveRegistryEntry deletes path (a value WriteRegistryEntry returned)
// IF the file's own recorded launch_id still matches launchID. A missing
// file, or one that already changed hands, is not an error: `monitor run
// --` calls this on its own exit path, which must never fail or delete
// SOMEONE ELSE's entry just because this launch's own file is already
// gone (or was never successfully created) -- best-effort by design, per
// docs/contracts/local-sentry-naming.md §8's "removed on exit" rule.
//
// The launch_id re-check closes a narrow window WriteRegistryEntry's own
// collision guard cannot: two launches racing to register the exact same
// (project, name) at nearly the same instant could both read "no existing
// entry" before either writes; without this check, the loser of that race
// exiting later would unconditionally delete the WINNER's still-live
// entry. launchID == "" (a caller that does not know/track it) skips the
// check and always removes path, matching this function's old behavior.
func RemoveRegistryEntry(path, launchID string) {
	if path == "" {
		return
	}
	if launchID != "" {
		if existing, err := readRegistryEntryFile(path); err == nil && existing.LaunchID != launchID {
			return
		}
	}
	_ = os.Remove(path)
}

// readRegistryEntryFile reads and decodes path directly (no project/name ->
// path derivation, and no directory creation as a side effect -- unlike
// ReadRegistryEntry/RegistryPath, this must work for a plain, read-only
// lookup too; see ReadRegistryEntry's own doc comment).
func readRegistryEntryFile(path string) (RegistryEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return RegistryEntry{}, err
	}
	var entry RegistryEntry
	if err := json.Unmarshal(data, &entry); err != nil {
		return RegistryEntry{}, fmt.Errorf("decode launch registry entry %s: %w", path, err)
	}
	return entry, nil
}

// processAlive reports whether pid names a currently-running process, by
// sending it signal 0 (no-op: delivers nothing, but fails with ESRCH if the
// process does not exist, or EPERM if it exists but is owned by another
// user -- either way "alive" for this check's purpose) rather than
// depending on a process-listing library: WriteRegistryEntry's collision
// guard needs only this one yes/no answer, so a raw syscall keeps this
// package's own dependency footprint unchanged.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	err = proc.Signal(syscall.Signal(0))
	return err == nil || errors.Is(err, os.ErrPermission)
}

// ReadRegistryEntry reads project/name's registered entry, if any.
func ReadRegistryEntry(project, name string) (RegistryEntry, error) {
	path, err := RegistryPath(project, name)
	if err != nil {
		return RegistryEntry{}, err
	}
	return readRegistryEntryFile(path)
}

// ListRegisteredServiceNames lists every service name currently registered
// for project (sorted), for `monitor hot <unknown-service>`'s "list every
// registered service" error -- never guessing, per procbind.
// AmbiguousLeafError's same posture. A read/list failure (the directory
// does not exist yet, most commonly: no service has ever been registered
// for this project) returns nil rather than an error: an empty list is
// itself the honest answer a caller needs to print "no services
// registered for project X".
func ListRegisteredServiceNames(project string) []string {
	dir, err := registryProjectDirPath(project)
	if err != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if !strings.HasSuffix(name, ".json") {
			continue
		}
		names = append(names, strings.TrimSuffix(name, ".json"))
	}
	sort.Strings(names)
	return names
}
