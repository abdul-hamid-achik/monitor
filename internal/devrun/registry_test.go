package devrun

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func withIsolatedRegistry(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_STATE_HOME", dir)
	return dir
}

func TestWriteRegistryEntryRoundTrips(t *testing.T) {
	withIsolatedRegistry(t)
	entry := RegistryEntry{
		LaunchID:  "launch-1",
		PID:       4242,
		Name:      "web-api",
		Project:   "acme",
		StartedAt: time.Unix(1700000000, 0).UTC(),
		Scan:      ScanStderr,
	}
	path, err := WriteRegistryEntry(entry)
	if err != nil {
		t.Fatalf("WriteRegistryEntry: %v", err)
	}
	got, err := ReadRegistryEntry("acme", "web-api")
	if err != nil {
		t.Fatalf("ReadRegistryEntry: %v", err)
	}
	if got.Schema != RegistrySchema {
		t.Errorf("Schema = %q, want %q", got.Schema, RegistrySchema)
	}
	if got.LaunchID != "launch-1" || got.PID != 4242 || got.Name != "web-api" || got.Project != "acme" {
		t.Errorf("round-tripped entry = %+v, want the written fields back", got)
	}
	if !got.StartedAt.Equal(entry.StartedAt) {
		t.Errorf("StartedAt = %v, want %v", got.StartedAt, entry.StartedAt)
	}

	if runtime.GOOS != "windows" {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Errorf("entry file mode = %o, want 0600", perm)
		}
		dirInfo, err := os.Stat(filepath.Dir(path))
		if err != nil {
			t.Fatalf("stat registry dir: %v", err)
		}
		if perm := dirInfo.Mode().Perm(); perm != 0o700 {
			t.Errorf("registry directory mode = %o, want 0700", perm)
		}
	}
}

func TestWriteRegistryEntryReplacesPrevious(t *testing.T) {
	withIsolatedRegistry(t)
	if _, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: 1}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: 2}); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, err := ReadRegistryEntry("acme", "w")
	if err != nil {
		t.Fatalf("ReadRegistryEntry: %v", err)
	}
	if got.PID != 2 {
		t.Errorf("PID = %d, want 2 (the second write must replace the first)", got.PID)
	}
}

func TestRemoveRegistryEntryDeletesFileAndToleratesMissing(t *testing.T) {
	withIsolatedRegistry(t)
	path, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: 1})
	if err != nil {
		t.Fatalf("WriteRegistryEntry: %v", err)
	}
	RemoveRegistryEntry(path)
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("entry file still exists after RemoveRegistryEntry: err=%v", err)
	}
	// Removing again (already gone) must not panic or need special-casing
	// by the caller.
	RemoveRegistryEntry(path)
	RemoveRegistryEntry("")
}

func TestReadRegistryEntryMissingReturnsError(t *testing.T) {
	withIsolatedRegistry(t)
	if _, err := ReadRegistryEntry("acme", "does-not-exist"); err == nil {
		t.Error("ReadRegistryEntry for an unregistered service should return an error")
	}
}

func TestListRegisteredServiceNamesSortedAndScopedPerProject(t *testing.T) {
	withIsolatedRegistry(t)
	for _, e := range []RegistryEntry{
		{Name: "web-api", Project: "acme", PID: 1},
		{Name: "worker", Project: "acme", PID: 2},
		{Name: "other-svc", Project: "other-project", PID: 3},
	} {
		if _, err := WriteRegistryEntry(e); err != nil {
			t.Fatalf("WriteRegistryEntry(%+v): %v", e, err)
		}
	}
	got := ListRegisteredServiceNames("acme")
	want := []string{"web-api", "worker"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("ListRegisteredServiceNames(acme) = %v, want %v", got, want)
	}
	if got := ListRegisteredServiceNames("no-such-project"); len(got) != 0 {
		t.Errorf("ListRegisteredServiceNames(unregistered project) = %v, want empty", got)
	}
}

// TestSanitizeRegistryComponentBlocksPathEscape guards the registry's own
// path-safety rule: --project/--name values reach the filesystem as a
// single path component, never a traversal out of registryRoot().
func TestSanitizeRegistryComponentBlocksPathEscape(t *testing.T) {
	dir := withIsolatedRegistry(t)
	path, err := RegistryPath("../../etc", "../../passwd")
	if err != nil {
		t.Fatalf("RegistryPath: %v", err)
	}
	root, err := registryRoot()
	if err != nil {
		t.Fatalf("registryRoot: %v", err)
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("filepath.Rel: %v", err)
	}
	// A real escape would produce a ".." PATH SEGMENT (e.g. "../passwd.json"),
	// not merely a component whose sanitized NAME happens to start with two
	// literal dots (".._.._etc", which sanitizeRegistryComponent produces
	// from "../../etc" by replacing "/" with "_" -- a real, if oddly named,
	// child directory, not a traversal).
	if filepath.IsAbs(rel) {
		t.Errorf("RegistryPath escaped its root (absolute): root=%s path=%s (dir=%s)", root, path, dir)
	}
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		if seg == ".." {
			t.Errorf("RegistryPath escaped its root (%q segment): root=%s path=%s (dir=%s)", seg, root, path, dir)
		}
	}
}
