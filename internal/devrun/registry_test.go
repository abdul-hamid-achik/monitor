package devrun

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// deadPID starts and reaps a real, trivial child process and returns its
// (now guaranteed-dead) pid -- a safe stand-in for "a pid that used to be
// registered but is no longer alive" without depending on any assumption
// about how high an OS's pid numbers go.
func deadPID(t *testing.T) int {
	t.Helper()
	cmd := exec.Command("true")
	if err := cmd.Run(); err != nil {
		t.Fatalf("run throwaway process: %v", err)
	}
	return cmd.Process.Pid
}

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
	path, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: 1, LaunchID: "launch-1"})
	if err != nil {
		t.Fatalf("WriteRegistryEntry: %v", err)
	}
	RemoveRegistryEntry(path, "launch-1")
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("entry file still exists after RemoveRegistryEntry: err=%v", err)
	}
	// Removing again (already gone) must not panic or need special-casing
	// by the caller.
	RemoveRegistryEntry(path, "launch-1")
	RemoveRegistryEntry("", "launch-1")
}

// TestRemoveRegistryEntryRefusesWhenLaunchIDChanged is the residual-race
// regression test (WriteRegistryEntry's own collision guard's doc
// comment): if the file at path now belongs to a DIFFERENT launch_id than
// the one this caller minted it under, RemoveRegistryEntry must leave it
// alone rather than deleting whichever launch currently owns the name.
func TestRemoveRegistryEntryRefusesWhenLaunchIDChanged(t *testing.T) {
	withIsolatedRegistry(t)
	path, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: 1, LaunchID: "launch-A"})
	if err != nil {
		t.Fatalf("WriteRegistryEntry: %v", err)
	}
	// Simulate another launch taking over the same file out from under the
	// first (e.g. after A's entry went stale and was cleaned up, or a
	// residual TOCTOU race -- see WriteRegistryEntry's own doc comment).
	if err := os.WriteFile(path, []byte(`{"schema":"monitor.run-service.v1","launch_id":"launch-B","pid":2,"name":"w","project":"acme"}`), 0o600); err != nil {
		t.Fatalf("simulate takeover: %v", err)
	}
	RemoveRegistryEntry(path, "launch-A")
	if _, err := os.Stat(path); err != nil {
		t.Errorf("entry file removed even though launch_id no longer matched: %v", err)
	}
	got, err := ReadRegistryEntry("acme", "w")
	if err != nil || got.LaunchID != "launch-B" {
		t.Errorf("ReadRegistryEntry after refused removal = %+v, %v, want launch-B's entry intact", got, err)
	}
}

// TestWriteRegistryEntryRefusesToOverwriteALiveDifferentLaunch is the
// collision-guard regression test: launch A registers "w" and is still
// alive (this TEST process's own pid, guaranteed alive); a second launch B
// with a different launch_id trying to register the exact same
// (project, name) must be refused with ErrServiceNameInUse, not silently
// overwrite A's entry.
func TestWriteRegistryEntryRefusesToOverwriteALiveDifferentLaunch(t *testing.T) {
	withIsolatedRegistry(t)
	if _, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: os.Getpid(), LaunchID: "launch-A"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	_, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: os.Getpid(), LaunchID: "launch-B"})
	if !errors.Is(err, ErrServiceNameInUse) {
		t.Fatalf("second write error = %v, want ErrServiceNameInUse", err)
	}
	got, err := ReadRegistryEntry("acme", "w")
	if err != nil || got.LaunchID != "launch-A" {
		t.Errorf("ReadRegistryEntry after refused write = %+v, %v, want launch-A's entry untouched", got, err)
	}
}

// TestWriteRegistryEntrySameLaunchUpdatesFreely is the non-collision case:
// the SAME launch_id updating its own entry (e.g. adding a freshly
// discovered --inspect inspector) must never be refused.
func TestWriteRegistryEntrySameLaunchUpdatesFreely(t *testing.T) {
	withIsolatedRegistry(t)
	if _, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: os.Getpid(), LaunchID: "launch-A"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: os.Getpid(), LaunchID: "launch-A", Inspectors: []RegistryInspector{{Port: 9229}}}); err != nil {
		t.Fatalf("same-launch update: %v", err)
	}
	got, err := ReadRegistryEntry("acme", "w")
	if err != nil || len(got.Inspectors) != 1 {
		t.Errorf("ReadRegistryEntry after same-launch update = %+v, %v, want the inspector to have been saved", got, err)
	}
}

// TestWriteRegistryEntryReplacesAStaleDeadPidEntry: a previous launch's
// entry whose pid is no longer alive must never block a new registration
// -- it is exactly as unregistered as a missing entry (naming ADR §8's "stale entries" rule).
func TestWriteRegistryEntryReplacesAStaleDeadPidEntry(t *testing.T) {
	withIsolatedRegistry(t)
	if _, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: deadPID(t), LaunchID: "launch-dead"}); err != nil {
		t.Fatalf("first write: %v", err)
	}
	if _, err := WriteRegistryEntry(RegistryEntry{Name: "w", Project: "acme", PID: os.Getpid(), LaunchID: "launch-new"}); err != nil {
		t.Fatalf("write over a stale dead-pid entry: %v", err)
	}
	got, err := ReadRegistryEntry("acme", "w")
	if err != nil || got.LaunchID != "launch-new" {
		t.Errorf("ReadRegistryEntry = %+v, %v, want launch-new's entry", got, err)
	}
}

// TestReadRegistryEntryAndListDoNotCreateDirectories: a pure read-only
// lookup for a project that has never registered anything must not create
// services/<project>/ (or even services/ itself) as a side effect.
func TestReadRegistryEntryAndListDoNotCreateDirectories(t *testing.T) {
	dir := withIsolatedRegistry(t)
	if _, err := ReadRegistryEntry("never-registered", "w"); err == nil {
		t.Error("ReadRegistryEntry for an unregistered project should return an error")
	}
	if got := ListRegisteredServiceNames("never-registered"); len(got) != 0 {
		t.Errorf("ListRegisteredServiceNames = %v, want empty", got)
	}
	if _, err := os.Stat(filepath.Join(dir, "monitor", "services")); !os.IsNotExist(err) {
		t.Errorf("services/ directory was created by a read-only lookup: stat err=%v", err)
	}
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
