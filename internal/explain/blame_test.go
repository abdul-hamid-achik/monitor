package explain

import (
	"context"
	"os/exec"
	"strings"
	"testing"
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
