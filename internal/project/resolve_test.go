package project

import (
	"os"
	"path/filepath"
	"testing"
)

// mkGitRoot creates dir and a .git directory inside it, simulating a real
// repository root without needing an actual git checkout.
func mkGitRoot(t *testing.T, dir string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Join(dir, ".git"), 0o755); err != nil {
		t.Fatalf("create .git in %s: %v", dir, err)
	}
}

// mkMarker creates dir and a marker file inside it (e.g. package.json).
func mkMarker(t *testing.T, dir, marker string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", dir, err)
	}
	if err := os.WriteFile(filepath.Join(dir, marker), []byte("{}"), 0o644); err != nil {
		t.Fatalf("write %s/%s: %v", dir, marker, err)
	}
}

func TestResolveMonorepoGitRootVsNearestMarker(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "acme")
	serviceDir := filepath.Join(repoRoot, "web-api")
	mkGitRoot(t, repoRoot)
	mkMarker(t, serviceDir, "package.json")

	got := Resolve(Hints{Dir: serviceDir, PID: 4242})
	if got.Slug != "acme" {
		t.Errorf("Slug = %q, want %q", got.Slug, "acme")
	}
	if got.Service != "web-api" {
		t.Errorf("Service = %q, want %q", got.Service, "web-api")
	}
	if got.GitRoot != repoRoot {
		t.Errorf("GitRoot = %q, want %q", got.GitRoot, repoRoot)
	}
	if got.Root != got.GitRoot {
		t.Errorf("Root = %q, want it to equal GitRoot %q", got.Root, got.GitRoot)
	}
	if got.Source != "git_root" {
		t.Errorf("Source = %q, want %q", got.Source, "git_root")
	}
}

func TestResolveExplicitFlagBeatsEverything(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "acme")
	serviceDir := filepath.Join(repoRoot, "web-api")
	mkGitRoot(t, repoRoot)
	mkMarker(t, serviceDir, "package.json")
	t.Setenv(EnvProject, "from-env")

	got := Resolve(Hints{ExplicitProject: "from-flag", ExplicitService: "from-flag-service", Dir: serviceDir, PID: 1})
	if got.Slug != "from-flag" || got.Source != "flag" {
		t.Fatalf("got = %+v, want Slug=from-flag Source=flag", got)
	}
	if got.Service != "from-flag-service" {
		t.Fatalf("Service = %q, want explicit service to win", got.Service)
	}
}

func TestResolveEnvBeatsGitRootAndMarker(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "acme")
	serviceDir := filepath.Join(repoRoot, "web-api")
	mkGitRoot(t, repoRoot)
	mkMarker(t, serviceDir, "package.json")
	t.Setenv(EnvProject, "from-env")

	got := Resolve(Hints{Dir: serviceDir, PID: 1})
	if got.Slug != "from-env" || got.Source != "env" {
		t.Fatalf("got = %+v, want Slug=from-env Source=env", got)
	}
	// Service derivation is untouched by the project-level env override.
	if got.Service != "web-api" {
		t.Fatalf("Service = %q, want web-api", got.Service)
	}
}

func TestResolveHostForPIDLessAlert(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "acme")
	mkGitRoot(t, repoRoot)

	// Even sitting inside a real git repo, a PID-less (host-wide) event
	// resolves to "host", not the repo's name: a swap/disk-fill alert
	// isn't about whichever project monitor happened to be launched from.
	got := Resolve(Hints{Dir: repoRoot, PID: 0})
	if got.Slug != "host" || got.Source != "host" {
		t.Fatalf("got = %+v, want Slug=host Source=host", got)
	}

	// Explicit signals still win over the PID-less special case.
	got = Resolve(Hints{Dir: repoRoot, PID: 0, ExplicitProject: "pinned"})
	if got.Slug != "pinned" || got.Source != "flag" {
		t.Fatalf("explicit flag did not win over host fallback: %+v", got)
	}
	t.Setenv(EnvProject, "from-env")
	got = Resolve(Hints{Dir: repoRoot, PID: 0})
	if got.Slug != "from-env" || got.Source != "env" {
		t.Fatalf("env did not win over host fallback: %+v", got)
	}
}

func TestResolveMarkerEqualsGitRootDoesNotDoubleAsService(t *testing.T) {
	root := t.TempDir()
	repoRoot := filepath.Join(root, "solo-repo")
	mkGitRoot(t, repoRoot)
	mkMarker(t, repoRoot, "go.mod")

	got := Resolve(Hints{Dir: repoRoot, ProcessName: "solo-repo-bin", PID: 7})
	if got.Slug != "solo-repo" || got.Source != "git_root" {
		t.Fatalf("got = %+v, want Slug=solo-repo Source=git_root", got)
	}
	// The marker root equals the git root here, so the "marker differs from
	// git root" service rule does not fire; service falls through to the
	// process name instead of duplicating the project slug.
	if got.Service != "solo-repo-bin" {
		t.Fatalf("Service = %q, want solo-repo-bin (process fallback)", got.Service)
	}
}

func TestResolveFallsThroughServiceProcessLocal(t *testing.T) {
	empty := t.TempDir() // no .git, no marker anywhere up to filesystem root

	got := Resolve(Hints{Dir: empty, ExplicitService: "svc-only", PID: 3})
	if got.Slug != "svc-only" || got.Source != "service" {
		t.Fatalf("got = %+v, want Slug=svc-only Source=service (falls through to service)", got)
	}

	got = Resolve(Hints{Dir: empty, ProcessName: "worker", PID: 3})
	if got.Slug != "worker" || got.Source != "process" || got.Service != "worker" {
		t.Fatalf("got = %+v, want Slug=worker Source=process Service=worker", got)
	}

	got = Resolve(Hints{Dir: empty, PID: 3})
	if got.Slug != "local" || got.Source != "local" {
		t.Fatalf("got = %+v, want Slug=local Source=local", got)
	}
	if got.Service != "" {
		t.Fatalf("Service = %q, want empty when nothing resolves it", got.Service)
	}
}

func TestFindGitRootHandlesWorktreeFileAndMissingRepo(t *testing.T) {
	root := t.TempDir()
	worktree := filepath.Join(root, "worktree")
	nested := filepath.Join(worktree, "internal", "cli")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}
	// A linked worktree or submodule records .git as a FILE, not a
	// directory; findGitRoot must accept either.
	if err := os.WriteFile(filepath.Join(worktree, ".git"), []byte("gitdir: /elsewhere\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := findGitRoot(nested); got != worktree {
		t.Fatalf("findGitRoot(nested) = %q, want %q", got, worktree)
	}

	empty := t.TempDir()
	if got := findGitRoot(empty); got != "" {
		t.Fatalf("findGitRoot(empty tree) = %q, want \"\"", got)
	}
}

func TestResolveDefaultsDirToWorkingDirectory(t *testing.T) {
	dir := t.TempDir()
	mkGitRoot(t, dir)
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })
	// Compare against a fresh os.Getwd() rather than the original dir
	// string: on macOS $TMPDIR may itself be a symlink, and Resolve's
	// internal fallback and this comparison must observe it identically.
	wantRoot, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}

	// UseWorkingDir opts a SELF-describing caller (monitor describing its
	// own working directory as the subject) into the os.Getwd() fallback.
	got := Resolve(Hints{PID: 1, UseWorkingDir: true})
	if got.GitRoot != wantRoot {
		t.Fatalf("GitRoot = %q, want cwd-derived %q", got.GitRoot, wantRoot)
	}
}

// TestResolveNeverDefaultsToWorkingDirectoryUnlessOptedIn is the fix for the
// misattribution bug: a caller describing ANOTHER process -- the default,
// UseWorkingDir unset -- must never fall back to monitor's own os.Getwd(),
// even when that cwd happens to sit inside a git repo. `watch --stash` and
// investigate both describe another process and must never set
// UseWorkingDir; an exited PID or an unreadable process cwd must resolve to
// "local", not to whichever repo monitor happens to be running from.
func TestResolveNeverDefaultsToWorkingDirectoryUnlessOptedIn(t *testing.T) {
	dir := t.TempDir()
	mkGitRoot(t, dir)
	origCwd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origCwd) })

	got := Resolve(Hints{PID: 999999})
	if got.Slug != "local" || got.GitRoot != "" || got.Source != "local" {
		t.Fatalf("got = %+v, want Slug=local GitRoot=\"\" Source=local (must not default to monitor's own cwd)", got)
	}
}

// TestResolveMarkerAboveGitRootIsIgnored is the fix for a stray manifest
// above the repository (e.g. a common ~/package.json) leaking into every
// nested repo's service name. findMarkerRoot must stop at the already
// resolved git root instead of continuing to walk above it.
func TestResolveMarkerAboveGitRootIsIgnored(t *testing.T) {
	root := t.TempDir()
	mkMarker(t, root, "package.json")
	repoRoot := filepath.Join(root, "projects", "foo")
	mkGitRoot(t, repoRoot)

	got := Resolve(Hints{Dir: repoRoot, ProcessName: "foo-bin", PID: 7})
	if got.Slug != "foo" || got.Source != "git_root" {
		t.Fatalf("got = %+v, want Slug=foo Source=git_root", got)
	}
	// The marker above the git root must be ignored; service falls through
	// to the process name instead of picking up the outer directory's name.
	if got.Service != "foo-bin" {
		t.Fatalf("Service = %q, want foo-bin (marker above the git root must be ignored)", got.Service)
	}
}
