package explain

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/abdul-hamid-achik/monitor/internal/issues"
)

func TestLastTouchedForReturnsBlameHeader(t *testing.T) {
	root := newGitRepo(t, map[string]string{"src/users.ts": "function loadUser(id) {\n  return db.users.find(id).name;\n}\n"})
	want := headSHA(t, root)

	lt := lastTouchedFor(context.Background(), root, "src/users.ts", 2)
	if lt.Status != SectionOK {
		t.Fatalf("status = %q, detail = %q", lt.Status, lt.Detail)
	}
	if !strings.HasPrefix(want, lt.SHA) {
		t.Errorf("sha = %q, want a prefix of %q", lt.SHA, want)
	}
	if lt.Subject != "fix: guard against missing user record" {
		t.Errorf("subject = %q", lt.Subject)
	}
	if lt.AuthorTime == nil || lt.AuthorTime.Year() != 2026 {
		t.Errorf("author time = %v", lt.AuthorTime)
	}
	if lt.AuthorEmail != "test@example.com" {
		t.Errorf("author email = %q", lt.AuthorEmail)
	}
}

func TestLastTouchedForSkipsWithoutGitRoot(t *testing.T) {
	lt := lastTouchedFor(context.Background(), "", "src/users.ts", 2)
	if lt.Status != SectionSkipped || lt.Detail == "" {
		t.Fatalf("lt = %+v", lt)
	}
}

func TestLastTouchedForSkipsWithoutCulpritLine(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	lt := lastTouchedFor(context.Background(), root, "", 0)
	if lt.Status != SectionSkipped {
		t.Fatalf("lt = %+v", lt)
	}
}

func TestLastTouchedForSkipsWhenGitMissing(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	t.Setenv("PATH", t.TempDir()) // no git anywhere
	lt := lastTouchedFor(context.Background(), root, "a.go", 1)
	if lt.Status != SectionSkipped || !strings.Contains(lt.Detail, "git") {
		t.Fatalf("lt = %+v", lt)
	}
}

func TestLastTouchedForSkipsOnShallowClone(t *testing.T) {
	origin := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	commitGitFiles(t, origin, map[string]string{"a.go": "package a\n\nfunc F() {}\n"}, "add F")

	work := t.TempDir() + "/work"
	cmd := exec.Command("git", "clone", "-q", "--depth", "1", "file://"+origin, work)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git clone --depth 1: %v\n%s", err, out)
	}

	lt := lastTouchedFor(context.Background(), work, "a.go", 3)
	if lt.Status != SectionSkipped || !strings.Contains(lt.Detail, "shallow") {
		t.Fatalf("lt = %+v, want a shallow-clone skip", lt)
	}
}

func TestParseBlamePorcelainHandlesMissingHeader(t *testing.T) {
	if _, ok := parseBlamePorcelain(""); ok {
		t.Fatal("expected ok=false for empty input")
	}
	if _, ok := parseBlamePorcelain("not a valid header\n"); ok {
		t.Fatal("expected ok=false for a header shorter than a sha")
	}
}

func TestStripAuthorEmailClearsField(t *testing.T) {
	lt := LastTouched{Status: SectionOK, AuthorEmail: "dev@example.com"}
	if got := stripAuthorEmail(lt); got.AuthorEmail != "" {
		t.Fatalf("author email = %q, want stripped", got.AuthorEmail)
	}
}

func TestLastTouchedForUncommittedLineReportsHonestSubject(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n\nfunc F() {}\n"})
	// An uncommitted edit to a line ALREADY in the repo -- git blame's
	// porcelain output for this line carries the well-known all-zero SHA.
	if err := os.WriteFile(filepath.Join(root, "a.go"), []byte("package a\n\nfunc F() { println(\"x\") }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	lt := lastTouchedFor(context.Background(), root, "a.go", 3)
	if lt.Status != SectionOK {
		t.Fatalf("status = %q, detail = %q", lt.Status, lt.Detail)
	}
	if !isZeroGitSHA(lt.SHA) {
		t.Fatalf("sha = %q, want git's all-zero uncommitted-change placeholder", lt.SHA)
	}
	if lt.Subject == "" || strings.Contains(lt.Subject, "Version of") {
		t.Fatalf("subject = %q, want an honest 'uncommitted' label, not git's raw porcelain placeholder", lt.Subject)
	}
	if lt.AuthorEmail != "" {
		t.Fatalf("author email = %q, want empty for an uncommitted line", lt.AuthorEmail)
	}
}

func TestIsStaleUncommittedLineIsAlwaysStale(t *testing.T) {
	lt := LastTouched{Status: SectionOK, SHA: zeroGitSHA}
	if !isStale(context.Background(), "", lt, issues.Issue{FirstGitSHA: "deadbeef"}, "a.go") {
		t.Fatal("want stale=true for an uncommitted (all-zero SHA) blame result")
	}
}

func TestIsStaleFalseWithoutFirstGitSHA(t *testing.T) {
	lt := LastTouched{Status: SectionOK, SHA: "abc1234"}
	// FirstGitSHA unset is the common case (an ordinary local dev session
	// with no MONITOR_GIT_SHA/GIT_SHA/GITHUB_SHA) -- there is nothing to
	// compare against, so this must never guess true.
	if isStale(context.Background(), "", lt, issues.Issue{}, "a.go") {
		t.Fatal("want stale=false when FirstGitSHA is unknown")
	}
}

func TestIsStaleFalseWhenBlameSHAIsAncestorOfFirstGitSHAAndFileUnchanged(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n\nfunc F() {}\n"})
	first := headSHA(t, root)
	lt := lastTouchedFor(context.Background(), root, "a.go", 3)
	if lt.Status != SectionOK {
		t.Fatalf("precondition: lt = %+v", lt)
	}
	if isStale(context.Background(), root, lt, issues.Issue{FirstGitSHA: first}, "a.go") {
		t.Fatal("want stale=false: unchanged file, blame SHA is FirstGitSHA itself")
	}
}

func TestIsStaleTrueWhenBlameSHAIsNotAnAncestorOfFirstGitSHA(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n\nfunc F() {}\n"})
	// FirstGitSHA claims the issue was first seen at a commit that does not
	// exist -- the blamed commit can never be its ancestor.
	lt := lastTouchedFor(context.Background(), root, "a.go", 3)
	if lt.Status != SectionOK {
		t.Fatalf("precondition: lt = %+v", lt)
	}
	if !isStale(context.Background(), root, lt, issues.Issue{FirstGitSHA: "0123456789abcdef0123456789abcdef01234567"}, "a.go") {
		t.Fatal("want stale=true: blame SHA is not an ancestor of an unrelated FirstGitSHA")
	}
}

func TestIsStaleTrueWhenFileChangedSinceFirstGitSHAEvenIfBlameLineDidNot(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n\nfunc F() {}\n"})
	first := headSHA(t, root)
	// A second commit touches a DIFFERENT line, so line 3's own blame SHA
	// stays the first commit (still an ancestor of itself) -- but the file
	// as a whole now differs from FirstGitSHA.
	commitGitFiles(t, root, map[string]string{"a.go": "package a\n\nfunc F() {}\n\nfunc G() {}\n"}, "add G")
	lt := lastTouchedFor(context.Background(), root, "a.go", 3)
	if lt.Status != SectionOK || lt.SHA != first {
		t.Fatalf("precondition: lt = %+v, want sha %q (line 3 itself untouched)", lt, first)
	}
	if !isStale(context.Background(), root, lt, issues.Issue{FirstGitSHA: first}, "a.go") {
		t.Fatal("want stale=true: the file changed since FirstGitSHA even though line 3's own blame did not")
	}
}
