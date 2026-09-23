// registry.go implements E3.2's launch service registry
// (`monitor.run-service.v1`, docs/contracts/local-sentry-naming.md §8):
// `monitor run --name <n> -- <cmd>` writes one entry so a later `monitor
// hot <service>` can find the launch's real pid (and, under --inspect, its
// already-discovered inspector port) without the caller re-deriving
// either.
package devrun

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
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

// monitorStateSubdir returns (creating it, mode 0700)
// $XDG_STATE_HOME/monitor/<sub>, falling back to
// ~/.local/state/monitor/<sub> when XDG_STATE_HOME is unset -- the same
// convention internal/stacktrace's checkpointDir and internal/incidents'
// registryDir already use. Shared by the launch registry ("services"),
// --profile's exit shim ("shims") and per-launch profile output
// ("profiles/<launch-id>") in profile.go.
func monitorStateSubdir(sub string) (string, error) {
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
	dir := filepath.Join(root, "monitor", sub)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create %s directory: %w", sub, err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure %s directory: %w", sub, err)
	}
	return dir, nil
}

// registryRoot returns $XDG_STATE_HOME/monitor/services (see
// monitorStateSubdir).
func registryRoot() (string, error) {
	return monitorStateSubdir("services")
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

// registryProjectDir returns (creating it, mode 0700) the directory
// holding project's registered services.
func registryProjectDir(project string) (string, error) {
	root, err := registryRoot()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, sanitizeRegistryComponent(project))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create launch registry directory: %w", err)
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return "", fmt.Errorf("secure launch registry directory: %w", err)
	}
	return dir, nil
}

// RegistryPath returns the entry file for (project, name), creating the
// project's registry directory (0700) if it does not exist yet.
func RegistryPath(project, name string) (string, error) {
	dir, err := registryProjectDir(project)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, sanitizeRegistryComponent(name)+".json"), nil
}

// WriteRegistryEntry writes entry to its (Project, Name)-derived path,
// atomically (a temp file in the same directory, then os.Rename into
// place -- the same pattern stacktrace.SaveCheckpoint uses) and mode 0600,
// replacing any previous entry for that project/name. Returns the path
// written, so the caller can remove exactly that file on exit
// (RemoveRegistryEntry) without re-deriving it.
func WriteRegistryEntry(entry RegistryEntry) (string, error) {
	path, err := RegistryPath(entry.Project, entry.Name)
	if err != nil {
		return "", err
	}
	entry.Schema = RegistrySchema
	data, err := json.MarshalIndent(entry, "", "  ")
	if err != nil {
		return "", fmt.Errorf("encode launch registry entry: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return "", fmt.Errorf("write launch registry entry: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("commit launch registry entry: %w", err)
	}
	return path, nil
}

// RemoveRegistryEntry deletes path (a value WriteRegistryEntry returned).
// A missing file is not an error: `monitor run --` calls this on its own
// exit path, which must never fail just because the file was already
// cleaned up (or never successfully created) -- best-effort by design, per
// docs/contracts/local-sentry-naming.md §8's "removed on exit" rule.
func RemoveRegistryEntry(path string) {
	if path == "" {
		return
	}
	_ = os.Remove(path)
}

// ReadRegistryEntry reads project/name's registered entry, if any.
func ReadRegistryEntry(project, name string) (RegistryEntry, error) {
	path, err := RegistryPath(project, name)
	if err != nil {
		return RegistryEntry{}, err
	}
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

// ListRegisteredServiceNames lists every service name currently registered
// for project (sorted), for `monitor hot <unknown-service>`'s "list every
// registered service" error -- never guessing, per procbind.
// AmbiguousLeafError's same posture. A read/list failure (the directory
// does not exist yet, most commonly: no service has ever been registered
// for this project) returns nil rather than an error: an empty list is
// itself the honest answer a caller needs to print "no services
// registered for project X".
func ListRegisteredServiceNames(project string) []string {
	dir, err := registryProjectDir(project)
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
