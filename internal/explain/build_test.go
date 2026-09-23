package explain

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// newTestStore opens a fresh, isolated issue store in t.TempDir().
func newTestStore(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "issues.veclite")
}

func boolPtr(b bool) *bool { return &b }

func TestBuildStackCulprit(t *testing.T) {
	root := newGitRepo(t, map[string]string{
		"src/users.ts": "function loadUser(id) {\n  return db.users.find(id).name;\n}\n",
	})
	binDir := t.TempDir() // no codemap, no vecgrep on PATH -- forces honest degradation
	setToolPATH(t, binDir)

	storePath := newTestStore(t)
	ex := stacktrace.Exception{
		Runtime: "node", Type: "TypeError", Value: "Cannot read properties of undefined (reading 'id')",
		Parser: "js", Level: "error", Handled: boolPtr(true),
		Frames: []stacktrace.Frame{
			{Function: "loadUser", AbsPath: filepath.Join(root, "src/users.ts"), Filename: filepath.Join(root, "src/users.ts"), Lineno: 2},
		},
	}
	id := project.Identity{Slug: "polyglot", Service: "workload", Root: root, GitRoot: root}
	observed := time.Date(2026, 9, 22, 18, 3, 50, 0, time.UTC)
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: observed})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	c, err := Build(context.Background(), store, res.Issue.ID, Options{
		Budget: BudgetStandard, Root: root, Now: observed.Add(2 * time.Minute), Redact: true,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	if c.Schema != Schema {
		t.Errorf("schema = %q", c.Schema)
	}
	if c.Budget != "standard" {
		t.Errorf("budget = %q", c.Budget)
	}
	if c.Issue.Project != "polyglot" || c.Issue.Service != "workload" {
		t.Errorf("issue = %+v", c.Issue)
	}
	if c.Issue.ShortID == "" || len(c.Issue.ShortID) > 4 {
		t.Errorf("short_id = %q", c.Issue.ShortID)
	}
	if c.Culprit == nil || c.Culprit.Source != "stack" || c.Culprit.Confidence != "high" {
		t.Fatalf("culprit = %+v", c.Culprit)
	}
	if c.Culprit.File != "src/users.ts" || c.Culprit.Line != 2 || c.Culprit.Function != "loadUser" {
		t.Errorf("culprit location = %+v", c.Culprit)
	}
	if c.Culprit.Snippet == nil || len(c.Culprit.Snippet.Lines) == 0 {
		t.Fatalf("snippet = %+v", c.Culprit.Snippet)
	}
	if c.Culprit.Range == nil || c.Culprit.Range.Source != "frame" {
		t.Errorf("range = %+v, want a frame-window fallback (codemap unavailable)", c.Culprit.Range)
	}

	if c.Impact.Status != SectionSkipped {
		t.Errorf("impact = %+v, want skipped (codemap unavailable)", c.Impact)
	}
	if c.LastTouched.Status != SectionOK || c.LastTouched.Subject == "" {
		t.Errorf("last_touched = %+v", c.LastTouched)
	}

	foundCodemapDegraded := false
	for _, d := range c.Degraded {
		if d.Component == "codemap" {
			foundCodemapDegraded = true
		}
	}
	if !foundCodemapDegraded {
		t.Errorf("degraded = %+v, want a codemap entry", c.Degraded)
	}

	if len(c.Next) == 0 {
		t.Error("next is empty")
	}
	if !c.Privacy.TextIsUntrusted {
		t.Error("privacy.text_is_untrusted must always be true")
	}
}

func TestBuildMessageSearchCulprit(t *testing.T) {
	root := newGitRepo(t, map[string]string{
		"src/reconcile.py": "def reconcile():\n    if not rows:\n        log.error(\"reconcile failed: batch has no rows\")\n",
	})
	binDir := t.TempDir() // no vecgrep -- forces the git_grep fallback deterministically
	setToolPATH(t, binDir)

	storePath := newTestStore(t)
	ex := stacktrace.Exception{
		Runtime: "python", Value: "reconcile failed: batch 42 has no rows",
		Parser: "message", Level: "error", Handled: boolPtr(true),
	}
	id := project.Identity{Slug: "polyglot", Service: "workload", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}
	if res.Issue.Culprit != nil {
		t.Fatalf("precondition failed: issue already has a stack culprit: %+v", res.Issue.Culprit)
	}

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	c, err := Build(context.Background(), store, res.Issue.ID, Options{Budget: BudgetStandard, Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if c.Culprit == nil || c.Culprit.Source != "message_search" || c.Culprit.Via != "git_grep" || c.Culprit.Confidence != "low" {
		t.Fatalf("culprit = %+v", c.Culprit)
	}
	if c.Culprit.File != "src/reconcile.py" || c.Culprit.Line != 3 {
		t.Errorf("culprit location = %+v", c.Culprit)
	}
}

func TestBuildNoSignalAtAllLeavesCulpritNil(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	binDir := t.TempDir()
	setToolPATH(t, binDir)

	storePath := newTestStore(t)
	ex := stacktrace.Exception{Runtime: "python", Value: "xk9", Parser: "message", Level: "error"}
	id := project.Identity{Slug: "polyglot", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	c, err := Build(context.Background(), store, res.Issue.ID, Options{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if c.Culprit != nil {
		t.Fatalf("culprit = %+v, want nil (message too short/generic to search)", c.Culprit)
	}
	if c.Impact.Status != SectionSkipped || c.Impact.Detail != "no culprit resolved" {
		t.Errorf("impact = %+v", c.Impact)
	}
	if c.LastTouched.Status != SectionSkipped {
		t.Errorf("last_touched = %+v", c.LastTouched)
	}
}

func TestBuildRejectsLatestSentinel(t *testing.T) {
	storePath := newTestStore(t)
	store, err := issues.OpenStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := Build(context.Background(), store, "latest", Options{}); err != ErrLatestNotSupported {
		t.Fatalf("err = %v, want ErrLatestNotSupported", err)
	}
	if _, err := Build(context.Background(), store, "LATEST", Options{}); err != ErrLatestNotSupported {
		t.Fatalf("err = %v, want ErrLatestNotSupported (case-insensitive)", err)
	}
}

func TestBuildPropagatesNotFound(t *testing.T) {
	storePath := newTestStore(t)
	store, err := issues.OpenStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	if _, err := Build(context.Background(), store, "ISS-MISSING", Options{}); err == nil {
		t.Fatal("expected an error for a missing issue")
	}
}

func TestBuildResolvesShortIDPrefix(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	setToolPATH(t, t.TempDir())
	storePath := newTestStore(t)
	ex := stacktrace.Exception{Runtime: "go", Type: "panic", Value: "boom", Parser: "gopanic", Level: "fatal"}
	id := project.Identity{Slug: "polyglot", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}
	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	prefix := shortID(res.Issue.ID)
	c, err := Build(context.Background(), store, prefix, Options{Root: root})
	if err != nil {
		t.Fatalf("Build(%q): %v", prefix, err)
	}
	if c.Issue.ID != res.Issue.ID {
		t.Fatalf("resolved id = %q, want %q", c.Issue.ID, res.Issue.ID)
	}
}

func TestBuildResolvedFromEchoed(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	setToolPATH(t, t.TempDir())
	storePath := newTestStore(t)
	ex := stacktrace.Exception{Runtime: "go", Type: "panic", Value: "boom", Parser: "gopanic", Level: "fatal"}
	id := project.Identity{Slug: "polyglot", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}
	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	resolved := ResolvedFrom{ID: "latest", Project: "polyglot", Kind: "exception"}
	c, err := Build(context.Background(), store, res.Issue.ID, Options{Root: root, ResolvedFrom: &resolved})
	if err != nil {
		t.Fatal(err)
	}
	if c.ResolvedFrom == nil || c.ResolvedFrom.Project != "polyglot" {
		t.Fatalf("resolved_from = %+v", c.ResolvedFrom)
	}
}

func TestBuildRequiresStore(t *testing.T) {
	if _, err := Build(context.Background(), nil, "ISS-X", Options{}); err == nil {
		t.Fatal("expected an error for a nil store")
	}
}

// TestBuildEveryTopLevelSectionAlwaysPresent guards the contract's "every
// section Build knows how to fill is present at every budget" rule: even
// with everything unavailable (no codemap/vecgrep/git-worthy content), the
// JSON round-trip must still carry impact/last_touched/related_notes/
// degraded/next/truncated/privacy as real (possibly empty/skipped) values,
// never an omitted field.
func TestBuildEveryTopLevelSectionAlwaysPresent(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	setToolPATH(t, t.TempDir())
	storePath := newTestStore(t)
	ex := stacktrace.Exception{Runtime: "go", Type: "panic", Value: "boom", Parser: "gopanic", Level: "fatal"}
	id := project.Identity{Slug: "polyglot", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatal(err)
	}
	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	c, err := Build(context.Background(), store, res.Issue.ID, Options{Root: root})
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"impact"`, `"last_touched"`, `"related_notes"`, `"degraded"`, `"next"`, `"truncated"`, `"privacy"`, `"causes"`, `"frames"`} {
		if !bytes.Contains(data, []byte(key)) {
			t.Errorf("output missing top-level key %s:\n%s", key, data)
		}
	}
}
