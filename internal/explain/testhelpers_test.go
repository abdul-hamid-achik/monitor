package explain

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// newGitRepo creates an isolated git repository in a fresh temp dir, writes
// files, and commits them with a deterministic author/committer identity and
// a fixed commit message -- so blame/build tests can assert on an exact SHA
// prefix, subject, and author time without depending on this worktree's own
// (shared, mutating) git history.
func newGitRepo(t *testing.T, files map[string]string) (root string) {
	t.Helper()
	root = t.TempDir()
	runGit(t, root, "init", "-q", "-b", "main")
	runGit(t, root, "config", "user.email", "test@example.com")
	runGit(t, root, "config", "user.name", "Test Author")
	writeGitFiles(t, root, files)
	runGit(t, root, "add", "-A")
	runGitEnv(t, root, []string{"GIT_AUTHOR_DATE=2026-09-18T14:02:00+00:00", "GIT_COMMITTER_DATE=2026-09-18T14:02:00+00:00"},
		"commit", "-q", "-m", "fix: guard against missing user record")
	return root
}

// writeGitFiles writes files (relative path -> content) under root, creating
// parent directories as needed.
func writeGitFiles(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for rel, content := range files {
		abs := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", rel, err)
		}
		if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", rel, err)
		}
	}
}

// commitGitFiles updates/adds files and commits again, for tests that need
// a SECOND commit (e.g. isStale's "touched after FirstGitSHA" case).
func commitGitFiles(t *testing.T, root string, files map[string]string, message string) {
	t.Helper()
	writeGitFiles(t, root, files)
	runGit(t, root, "add", "-A")
	runGitEnv(t, root, []string{"GIT_AUTHOR_DATE=2026-09-20T09:00:00+00:00", "GIT_COMMITTER_DATE=2026-09-20T09:00:00+00:00"},
		"commit", "-q", "-m", message)
}

func headSHA(t *testing.T, root string) string {
	t.Helper()
	return strings.TrimSpace(runGitOutput(t, root, "rev-parse", "HEAD"))
}

func runGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	runGitEnv(t, dir, nil, args...)
}

func runGitEnv(t *testing.T, dir string, extraEnv []string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append([]string{"GIT_CONFIG_NOSYSTEM=1", "HOME=" + dir}, extraEnv...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Run(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out.String())
	}
}

func runGitOutput(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// realGitDir returns the directory containing the real `git` binary on
// PATH, so a test that replaces PATH to control OTHER tools (vecgrep,
// codemap) can still keep git working.
func realGitDir(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git not on PATH")
	}
	return filepath.Dir(path)
}

// setToolPATH replaces PATH with fakeDir followed by the real git's
// directory (see realGitDir), so a fake/absent tool in fakeDir is what a
// test observes while `git` itself keeps working underneath it.
func setToolPATH(t *testing.T, fakeDir string) {
	t.Helper()
	t.Setenv("PATH", fakeDir+string(os.PathListSeparator)+realGitDir(t))
}
