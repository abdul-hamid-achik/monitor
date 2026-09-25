package explain

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
	veclite "github.com/abdul-hamid-achik/veclite"
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

// TestBuildFramesOrderedClosestToCrashFirst pins the contract's "frames[]
// ... closest to the crash first" ordering (docs/contracts/issue-context-v1.
// md). issues.ExceptionInfo.Frames is stored oldest-to-newest with the
// crash frame LAST; Build must reverse that for Context.Frames.
func TestBuildFramesOrderedClosestToCrashFirst(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	setToolPATH(t, t.TempDir())
	storePath := newTestStore(t)
	abs := filepath.Join(root, "a.go")
	ex := stacktrace.Exception{
		Runtime: "go", Type: "panic", Value: "boom", Parser: "gopanic", Level: "fatal",
		Frames: []stacktrace.Frame{
			{Function: "outer", Filename: abs, AbsPath: abs, Lineno: 1, InApp: true},
			{Function: "middle", Filename: abs, AbsPath: abs, Lineno: 1, InApp: true},
			{Function: "crashSite", Filename: abs, AbsPath: abs, Lineno: 1, InApp: true},
		},
	}
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

	c, err := Build(context.Background(), store, res.Issue.ID, Options{Root: root, Budget: BudgetFull})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if len(c.Frames) != 3 {
		t.Fatalf("frames = %+v, want 3", c.Frames)
	}
	if c.Frames[0].Function != "crashSite" {
		t.Fatalf("frames[0] = %q, want crashSite (closest to the crash) first", c.Frames[0].Function)
	}
	if c.Frames[2].Function != "outer" {
		t.Fatalf("frames[2] = %q, want outer (the oldest call) last", c.Frames[2].Function)
	}
}

// TestBuildResolvesRootFromRecordedFrameNotWorkingDirectory is the review's
// exact repro: MCP (mcphub) launches `monitor mcp serve` from an unrelated
// cwd. With no Options.Root, Build must derive the root the issue was
// actually RECORDED under from its own stored frame AbsPath, never from an
// unrelated caller cwd that happens to have a file at the same relative
// path.
func TestBuildResolvesRootFromRecordedFrameNotWorkingDirectory(t *testing.T) {
	root := newGitRepo(t, map[string]string{
		"src/users.ts": "function loadUser(id) {\n  return db.users.find(id).name;\n}\n",
	})
	setToolPATH(t, t.TempDir())

	storePath := newTestStore(t)
	abs := filepath.Join(root, "src/users.ts")
	ex := stacktrace.Exception{
		Runtime: "node", Type: "TypeError", Value: "Cannot read properties of undefined (reading 'id')",
		Parser: "js", Level: "error", Handled: boolPtr(true),
		Frames: []stacktrace.Frame{
			{Function: "loadUser", AbsPath: abs, Filename: abs, Lineno: 2, InApp: true},
		},
	}
	id := project.Identity{Slug: "polyglot", Service: "workload", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// A completely unrelated repo, with a file at the SAME relative path,
	// deliberately different content -- if root resolution fell back to
	// cwd, the snippet/blame below would silently come from HERE instead.
	elsewhere := newGitRepo(t, map[string]string{"src/users.ts": "// unrelated file, wrong repo\n"})
	t.Chdir(elsewhere)

	c, err := Build(context.Background(), store, res.Issue.ID, Options{})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if c.Culprit == nil || c.Culprit.Snippet == nil {
		t.Fatalf("culprit/snippet = %+v, want the recording repo's real snippet", c.Culprit)
	}
	joined := strings.Join(c.Culprit.Snippet.Lines, "\n")
	if strings.Contains(joined, "unrelated") {
		t.Fatalf("snippet = %q, want the RECORDING repo's content, not the unrelated cwd's", joined)
	}
	if c.LastTouched.Status != SectionOK {
		t.Fatalf("last_touched = %+v, want ok from the recording repo (the unrelated cwd's repo has too few lines to blame line 2 the same way)", c.LastTouched)
	}
}

// TestResolveRootReportsMismatchAgainstIssueProject covers resolveRoot's own
// honest-degradation rule directly: when an auto-derived root's own project
// identity disagrees with the issue's recorded Project, that is reported
// into degraded rather than silently trusted.
func TestResolveRootReportsMismatchAgainstIssueProject(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	t.Chdir(root)
	issue := issues.Issue{Project: "totally-different-project"}
	degraded := map[string]Degraded{}
	got := resolveRoot(Options{}, issue, degraded)
	if got != root {
		t.Fatalf("root = %q, want %q", got, root)
	}
	d, ok := degraded["root"]
	if !ok || d.State != "mismatch" {
		t.Fatalf("degraded = %+v, want a root mismatch entry", degraded)
	}
}

// TestResolveRootExplicitOverrideSkipsMismatchCheck: an explicit Options.
// Root (the CLI's --root) is trusted outright, never second-guessed against
// the issue's recorded Project.
func TestResolveRootExplicitOverrideSkipsMismatchCheck(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	issue := issues.Issue{Project: "totally-different-project"}
	degraded := map[string]Degraded{}
	got := resolveRoot(Options{Root: root}, issue, degraded)
	if got != root {
		t.Fatalf("root = %q, want %q", got, root)
	}
	if _, ok := degraded["root"]; ok {
		t.Fatalf("degraded = %+v, an explicit --root must never be second-guessed", degraded)
	}
}

func TestRootFromRecordedFramePrefersCulpritMatchingFrame(t *testing.T) {
	issue := issues.Issue{
		Culprit: &issues.Culprit{File: "src/b.go", Line: 5},
		LatestException: &issues.ExceptionInfo{
			Frames: []stacktrace.Frame{
				{Filename: "src/a.go", AbsPath: "/repo-a/src/a.go", Lineno: 1},
				{Filename: "src/b.go", AbsPath: "/repo-b/src/b.go", Lineno: 5},
			},
		},
	}
	if got := rootFromRecordedFrame(issue); got != "/repo-b" {
		t.Fatalf("root = %q, want /repo-b (the culprit-matching frame's root)", got)
	}
}

func TestRootFromRecordedFrameFallsBackToAnyUsableFrame(t *testing.T) {
	issue := issues.Issue{
		LatestException: &issues.ExceptionInfo{
			Frames: []stacktrace.Frame{
				{Filename: "src/a.go", AbsPath: "/repo/src/a.go", Lineno: 1},
			},
		},
	}
	if got := rootFromRecordedFrame(issue); got != "/repo" {
		t.Fatalf("root = %q, want /repo", got)
	}
}

func TestRootFromRecordedFrameEmptyWithoutException(t *testing.T) {
	if got := rootFromRecordedFrame(issues.Issue{}); got != "" {
		t.Fatalf("root = %q, want empty", got)
	}
}

// TestBuildFitsBriefBudgetWithOversizedMessage is the Build-level size test
// the contract's budget table promises ("brief no pasa de 4096 bytes ...
// con test") -- budget_test.go's own tests only ever exercised applyBudget
// against a synthetic Context; a real Build() with a pathologically long
// exception message must ALSO fit, since Issue.Title is never bounded at
// ingest.
func TestBuildFitsBriefBudgetWithOversizedMessage(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	setToolPATH(t, t.TempDir())
	storePath := newTestStore(t)
	longMessage := "panic: " + strings.Repeat("a very long diagnostic message segment ", 400)
	ex := stacktrace.Exception{Runtime: "go", Type: "panic", Value: longMessage, Parser: "gopanic", Level: "fatal"}
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

	c, err := Build(context.Background(), store, res.Issue.ID, Options{Root: root, Budget: BudgetBrief})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	data, err := json.Marshal(c)
	if err != nil {
		t.Fatal(err)
	}
	if len(data) > briefMaxBytes {
		t.Fatalf("brief size = %d bytes, want <= %d (title = %d runes)", len(data), briefMaxBytes, len([]rune(c.Issue.Title)))
	}

	c2, err := Build(context.Background(), store, res.Issue.ID, Options{Root: root, Budget: BudgetStandard})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	data2, err := json.Marshal(c2)
	if err != nil {
		t.Fatal(err)
	}
	if len(data2) > standardMaxBytes {
		t.Fatalf("standard size = %d bytes, want <= %d", len(data2), standardMaxBytes)
	}
}

// TestBuildRedactScrubsEnvSecretInLiveSnippet guards the read-side
// defense-in-depth pass (Options.Redact) against secret ENV VALUES, not
// just secret shapes: the culprit snippet is read LIVE from disk, so a
// secret that leaked into the source file was never seen by the
// ingest-time scrubber, and redactContext must carry the current
// process's secret env values (scrub.WithValues) for exactly this case.
func TestBuildRedactScrubsEnvSecretInLiveSnippet(t *testing.T) {
	// Deliberately shapeless (no Bearer/JWT/AKIA/sk_live/... shape): a
	// value a shape detector already recognizes would be redacted even
	// without the WithValues wiring under test. Built by concatenation
	// per AGENTS.md so no provider-shaped literal is pushed.
	secret := "env_" + "plainopaque-4a7f19c3"
	t.Setenv("FAKE_API_TOKEN", secret)

	root := newGitRepo(t, map[string]string{
		"src/app.go": "package app\n\nfunc doWork() {\n\tpanic(\"boom\") // token=" + secret + "\n}\n",
	})
	setToolPATH(t, t.TempDir()) // no codemap/vecgrep -- honest degradation; git stays real

	storePath := newTestStore(t)
	abs := filepath.Join(root, "src/app.go")
	ex := stacktrace.Exception{
		Runtime: "go", Type: "panic", Value: "boom", Parser: "gopanic", Level: "fatal",
		Frames: []stacktrace.Frame{{Function: "doWork", AbsPath: abs, Filename: abs, Lineno: 4}},
	}
	id := project.Identity{Slug: "polyglot", Service: "workload", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	// Redact:false (the human-terminal default): no second pass runs, so
	// the live snippet keeps the secret verbatim -- that output stays on
	// the user's own machine.
	plain, err := Build(context.Background(), store, res.Issue.ID, Options{Budget: BudgetStandard, Root: root})
	if err != nil {
		t.Fatalf("Build (Redact:false): %v", err)
	}
	if plain.Culprit == nil || plain.Culprit.Snippet == nil || len(plain.Culprit.Snippet.Lines) == 0 {
		t.Fatalf("precondition failed: no snippet to redact: %+v", plain.Culprit)
	}
	plainJoined := strings.Join(plain.Culprit.Snippet.Lines, "\n")
	if !strings.Contains(plainJoined, secret) {
		t.Errorf("Redact:false must keep the env secret visible, snippet = %q", plainJoined)
	}
	if plain.Privacy.Scrubbed != 0 {
		t.Errorf("Privacy.Scrubbed = %d, want 0 when Redact is false", plain.Privacy.Scrubbed)
	}

	// Redact:true (MCP / --md): the exact env value is replaced even
	// though no shape detector could ever recognize it.
	redacted, err := Build(context.Background(), store, res.Issue.ID, Options{Budget: BudgetStandard, Root: root, Redact: true})
	if err != nil {
		t.Fatalf("Build (Redact:true): %v", err)
	}
	if redacted.Culprit == nil || redacted.Culprit.Snippet == nil || len(redacted.Culprit.Snippet.Lines) == 0 {
		t.Fatalf("precondition failed: no snippet rendered: %+v", redacted.Culprit)
	}
	redactedJoined := strings.Join(redacted.Culprit.Snippet.Lines, "\n")
	if strings.Contains(redactedJoined, secret) {
		t.Errorf("Redact:true leaked the env secret into the snippet: %q", redactedJoined)
	}
	if !strings.Contains(redactedJoined, "[redacted]") {
		t.Errorf("Redact:true snippet does not carry the [redacted] token: %q", redactedJoined)
	}
	if redacted.Privacy.Scrubbed < 1 {
		t.Errorf("Privacy.Scrubbed = %d, want >= 1 (the snippet line)", redacted.Privacy.Scrubbed)
	}
}

func TestShellQuote(t *testing.T) {
	cases := []struct{ in, want string }{
		{"plain.js", "'plain.js'"},
		{"it's.js", `'it'\''s.js'`},
		{"evil$(touch pwned).js", `'evil$(touch pwned).js'`},
	}
	for _, c := range cases {
		if got := shellQuote(c.in); got != c.want {
			t.Errorf("shellQuote(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

// TestBuildEditorHintQuotesCulpritPath (SEC-4): the culprit path is
// untrusted stderr text; the $EDITOR hint monitor proposes must be a
// command `sh -c` cannot be broken out of, even when the forged frame's
// file name embeds command substitution AND a single quote.
func TestBuildEditorHintQuotesCulpritPath(t *testing.T) {
	markerDir := t.TempDir()
	marker := filepath.Join(markerDir, "PWNED_BY_NEXT_HINT")
	evil := "evil$(touch " + marker + ")'quote.js"
	root := newGitRepo(t, map[string]string{
		evil: "function boom() {\n  throw new Error('pwn');\n}\n",
	})
	setToolPATH(t, t.TempDir())

	storePath := newTestStore(t)
	abs := filepath.Join(root, evil)
	ex := stacktrace.Exception{
		Runtime: "node", Type: "Error", Value: "pwn", Parser: "js", Level: "error",
		Frames: []stacktrace.Frame{{Function: "boom", AbsPath: abs, Filename: abs, Lineno: 2, InApp: true}},
	}
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
	hint := ""
	for _, n := range c.Next {
		if strings.HasPrefix(n.CLI, "$EDITOR ") {
			hint = n.CLI
		}
	}
	if hint == "" {
		t.Fatalf("next = %+v, want an $EDITOR hint (the snippet read succeeded)", c.Next)
	}
	if want := "$EDITOR +2 " + shellQuote(evil); hint != want {
		t.Fatalf("hint = %q, want %q", hint, want)
	}
	// The proof the quoting holds: run the hint exactly as a user would
	// (EDITOR=true sh -c "$CLI") and assert the embedded command
	// substitution never executes. /bin/sh is used by absolute path
	// because setToolPATH narrowed this test's PATH to git alone.
	cmd := exec.Command("/bin/sh", "-c", hint)
	cmd.Env = append(os.Environ(), "EDITOR=true")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("sh -c %q: %v\n%s", hint, err, out)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatalf("the hint's command substitution executed (marker %s exists): %q", marker, hint)
	}
}

// TestBuildOmitsEditorHintWhenSnippetUnreadable (SEC-4): the $EDITOR hint
// promises a jump to the line the page just showed; when the snippet read
// failed (the culprit file is not readable under the root), the hint must
// be omitted entirely.
func TestBuildOmitsEditorHintWhenSnippetUnreadable(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	setToolPATH(t, t.TempDir())

	storePath := newTestStore(t)
	abs := filepath.Join(root, "ghost.js") // never created on disk
	ex := stacktrace.Exception{
		Runtime: "node", Type: "Error", Value: "boom", Parser: "js", Level: "error",
		Frames: []stacktrace.Frame{{Function: "boom", AbsPath: abs, Filename: abs, Lineno: 2, InApp: true}},
	}
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
	if c.Culprit == nil || c.Culprit.File != "ghost.js" {
		t.Fatalf("culprit = %+v, want the recorded stack culprit", c.Culprit)
	}
	if c.Culprit.Snippet != nil {
		t.Fatalf("snippet = %+v, want nil (the file does not exist under the root)", c.Culprit.Snippet)
	}
	for _, n := range c.Next {
		if strings.HasPrefix(n.CLI, "$EDITOR") {
			t.Fatalf("next offered an $EDITOR hint for an unreadable culprit: %q", n.CLI)
		}
	}
	foundSnippetDegraded := false
	for _, d := range c.Degraded {
		if d.Component == "snippet" {
			foundSnippetDegraded = true
		}
	}
	if !foundSnippetDegraded {
		t.Errorf("degraded = %+v, want a snippet entry", c.Degraded)
	}
}

// TestBuildMessageSearchUsesExceptionValueNotTitle (LUX-2): the search
// text is the exception's own rendered value (issue.Message), not the
// Title. The Title prefixes the exception type ("TypeError: ..."), which
// never appears in source; searching it found nothing in a clean repo.
func TestBuildMessageSearchUsesExceptionValueNotTitle(t *testing.T) {
	root := newGitRepo(t, map[string]string{
		"src/users.js": "function loadUser(id) {\n  throw new TypeError(`loadUser: missing user record for id ${id}`);\n}\n",
	})
	setToolPATH(t, t.TempDir()) // no vecgrep -- deterministic git_grep fallback

	storePath := newTestStore(t)
	ex := stacktrace.Exception{
		Runtime: "node", Type: "TypeError", Value: "loadUser: missing user record for id 42",
		Parser: "message", Level: "error", Handled: boolPtr(true),
	}
	id := project.Identity{Slug: "polyglot", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}
	if res.Issue.Title == res.Issue.Message {
		t.Fatalf("precondition failed: Title %q must carry the Type prefix the fix avoids searching", res.Issue.Title)
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
	if c.Culprit == nil || c.Culprit.Source != "message_search" || c.Culprit.Via != "git_grep" {
		t.Fatalf("culprit = %+v, want a message_search culprit via git_grep", c.Culprit)
	}
	if c.Culprit.File != "src/users.js" || c.Culprit.Line != 2 {
		t.Fatalf("culprit location = %+v, want src/users.js:2 (the throw that renders this message)", c.Culprit)
	}
}

// TestBuildSkipsMessageSearchForNonExceptionKind (LUX-12): a watch alert
// ("monitor.alert.<rule>", project "host") must not get an inferred
// culprit in whatever repo the reader stands in, even when its title
// would match source text under the resolved root.
func TestBuildSkipsMessageSearchForNonExceptionKind(t *testing.T) {
	root := newGitRepo(t, map[string]string{
		"rules.go": "package rules\n\nfunc (r *DiskFillRule) Name() string { return \"disk_fill\" }\n\nfunc message() string { return \"disk usage above threshold on /data\" }\n",
	})
	setToolPATH(t, t.TempDir())

	storePath := newTestStore(t)
	wstore, err := issues.OpenStore(storePath)
	if err != nil {
		t.Fatal(err)
	}
	alert, _, err := wstore.UpsertOccurrence(issues.OccurrenceInput{
		Project: "host", Kind: "monitor.alert.disk_fill",
		Title: "disk_fill", Message: "disk usage above threshold on /data",
		Severity: "warning", ObservedAt: time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("seed alert issue: %v", err)
	}
	if err := wstore.Close(); err != nil {
		t.Fatal(err)
	}

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	c, err := Build(context.Background(), store, alert.ID, Options{Root: root})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if c.Culprit != nil {
		t.Fatalf("culprit = %+v, want nil: the message-search fallback must not run for a watch alert (LUX-12)", c.Culprit)
	}
}

// TestBuildSnippetStaleAfterUncommittedShiftWithoutFirstGitSHA (LUX-5):
// the plain local journey never records FirstGitSHA, so staleness used to
// be unreachable and `monitor issue` kept highlighting the wrong line
// with confidence high. An uncommitted edit that shifts lines -- where
// blame still attributes the shifted culprit line to its original commit
// -- must now report stale:true and lower confidence.
func TestBuildSnippetStaleAfterUncommittedShiftWithoutFirstGitSHA(t *testing.T) {
	original := "function work(i) {\n  let s = 0;\n  s += i;\n  s *= 2;\n  throw new Error('work: unlucky draw');\n}\n"
	root := newGitRepo(t, map[string]string{"app.js": original})
	setToolPATH(t, t.TempDir())

	storePath := newTestStore(t)
	abs := filepath.Join(root, "app.js")
	ex := stacktrace.Exception{
		Runtime: "node", Type: "Error", Value: "work: unlucky draw", Parser: "js", Level: "fatal",
		Frames: []stacktrace.Frame{{Function: "work", AbsPath: abs, Filename: abs, Lineno: 5, InApp: true}},
	}
	id := project.Identity{Slug: "polyglot", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}
	if res.Issue.FirstGitSHA != "" {
		t.Fatalf("precondition failed: FirstGitSHA = %q, want empty for the plain local journey", res.Issue.FirstGitSHA)
	}

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()

	clean, err := Build(context.Background(), store, res.Issue.ID, Options{Root: root})
	if err != nil {
		t.Fatalf("Build (clean): %v", err)
	}
	if clean.Culprit == nil || clean.Culprit.Snippet == nil {
		t.Fatalf("culprit/snippet = %+v, want a rendered snippet", clean.Culprit)
	}
	if clean.Culprit.Snippet.Stale {
		t.Fatal("stale = true on an untouched worktree, want false")
	}
	if clean.Culprit.Confidence != "high" {
		t.Fatalf("confidence = %q, want high on a fresh read", clean.Culprit.Confidence)
	}

	// Prepend comment lines WITHOUT committing: the culprit line number now
	// holds a DIFFERENT committed line (blame stays non-zero), which is
	// exactly the case only the worktree-vs-HEAD check can catch.
	if err := os.WriteFile(abs, []byte("// shift\n// shift\n// shift\n"+original), 0o644); err != nil {
		t.Fatal(err)
	}

	edited, err := Build(context.Background(), store, res.Issue.ID, Options{Root: root})
	if err != nil {
		t.Fatalf("Build (edited): %v", err)
	}
	if edited.Culprit == nil || edited.Culprit.Snippet == nil {
		t.Fatalf("culprit/snippet = %+v, want a rendered snippet", edited.Culprit)
	}
	if !edited.Culprit.Snippet.Stale {
		t.Fatal("stale = false after an uncommitted edit shifted the culprit line, want true (LUX-5)")
	}
	if edited.Culprit.Confidence != "medium" {
		t.Fatalf("confidence = %q, want medium on a stale snippet (LUX-5)", edited.Culprit.Confidence)
	}
}

// TestBuildSeedsTruncatedFromIngestDrops (CC-4): frames/causes cut at
// ingest (the 12-frame cap, the 3-cause cap) must reach
// truncated.frames/causes, and a brief-budget read must ADD its own cuts
// on top of -- not instead of -- those ingest counts.
func TestBuildSeedsTruncatedFromIngestDrops(t *testing.T) {
	root := newGitRepo(t, map[string]string{"a.go": "package a\n"})
	setToolPATH(t, t.TempDir())
	storePath := newTestStore(t)
	abs := filepath.Join(root, "a.go")
	frames := make([]stacktrace.Frame, 0, 20)
	for i := range 20 {
		frames = append(frames, stacktrace.Frame{Function: fmt.Sprintf("fn%d", i), AbsPath: abs, Filename: abs, Lineno: 1, InApp: true})
	}
	chained := make([]stacktrace.Exception, 0, 5)
	for i := range 5 {
		chained = append(chained, stacktrace.Exception{Type: fmt.Sprintf("Cause%d", i)})
	}
	ex := stacktrace.Exception{Type: "Error", Value: "boom", Runtime: "go", Level: "fatal", Frames: frames, Chained: chained}
	id := project.Identity{Slug: "polyglot", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}

	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		t.Fatal(err)
	}
	full, err := Build(context.Background(), store, res.Issue.ID, Options{Root: root, Budget: BudgetFull})
	if err != nil {
		t.Fatalf("Build (full): %v", err)
	}
	// Every recorded in-app frame must be accounted for exactly once:
	// listed, or counted in truncated.frames (the 12-frame cap plus any
	// size-backstop cut at ingest -- the long temp-dir AbsPath makes the
	// backstop drop a couple more here, which is fine; the accounting is
	// what must stay exact).
	if len(full.Frames) > 12 || len(full.Frames)+full.Truncated.Frames != 20 {
		t.Fatalf("frames = %d truncated.frames = %d, want <=12 listed and every one of the 20 recorded frames accounted for", len(full.Frames), full.Truncated.Frames)
	}
	if len(full.Causes) != 3 || full.Truncated.Causes != 2 {
		t.Fatalf("causes = %d truncated.causes = %d, want 3/2 (5 chained causes, 3 kept)", len(full.Causes), full.Truncated.Causes)
	}

	brief, err := Build(context.Background(), store, res.Issue.ID, Options{Root: root, Budget: BudgetBrief})
	if err != nil {
		t.Fatalf("Build (brief): %v", err)
	}
	if len(brief.Frames) > briefFrameLimit {
		t.Fatalf("brief frames = %d, want at most briefFrameLimit (%d)", len(brief.Frames), briefFrameLimit)
	}
	if want := full.Truncated.Frames + (len(full.Frames) - len(brief.Frames)); brief.Truncated.Frames != want {
		t.Fatalf("brief truncated.frames = %d, want %d (ingest %d + brief's own %d)", brief.Truncated.Frames, want, full.Truncated.Frames, len(full.Frames)-len(brief.Frames))
	}
}

// TestBuildRebuildsCulpritAfterOldBinaryRewrite (CC-3): a v1.15 binary
// writing to the same store re-marshals the issue through its old struct
// and strips culprit/latest_exception/handled, while the first occurrence
// keeps its exception detail. Build must rebuild the culprit from that
// occurrence, render the snippet, and say so in degraded.
func TestBuildRebuildsCulpritAfterOldBinaryRewrite(t *testing.T) {
	root := newGitRepo(t, map[string]string{
		"src/users.ts": "function loadUser(id) {\n  return db.users.find(id).name;\n}\n",
	})
	setToolPATH(t, t.TempDir())
	storePath := newTestStore(t)
	abs := filepath.Join(root, "src/users.ts")
	handled := true
	ex := stacktrace.Exception{
		Runtime: "node", Type: "TypeError", Value: "Cannot read properties of undefined (reading 'id')",
		Parser: "js", Level: "error", Handled: &handled,
		Frames: []stacktrace.Frame{{Function: "loadUser", AbsPath: abs, Filename: abs, Lineno: 2, InApp: true}},
	}
	id := project.Identity{Slug: "polyglot", Root: root, GitRoot: root}
	res, err := issues.RecordException(context.Background(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: time.Now().UTC()})
	if err != nil {
		t.Fatalf("RecordException: %v", err)
	}

	// Simulate the older binary's rewrite the way it actually happens:
	// decode the issue, drop the fields the old struct never had, and
	// update the stored document through veclite directly.
	db, err := veclite.Open(storePath)
	if err != nil {
		t.Fatal(err)
	}
	coll := db.Collection("issues")
	record, err := coll.FindOne(veclite.Equal("id", res.Issue.ID))
	if err != nil {
		t.Fatal(err)
	}
	var issue issues.Issue
	if err := json.Unmarshal([]byte(record.Content), &issue); err != nil {
		t.Fatal(err)
	}
	issue.Culprit = nil
	issue.LatestException = nil
	issue.Handled = nil
	content, err := json.Marshal(issue)
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{
		"id": issue.ID, "fingerprint": issue.Fingerprint, "status": string(issue.Status),
		"project": issue.Project, "service": issue.Service, "last_seen": issue.LastSeen,
	}
	if err := coll.UpdateDocument(record.ID, string(content), payload); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
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
	if c.Culprit == nil || c.Culprit.Source != "stack" || c.Culprit.File != "src/users.ts" || c.Culprit.Line != 2 {
		t.Fatalf("culprit = %+v, want the rebuilt stack culprit src/users.ts:2", c.Culprit)
	}
	if c.Culprit.Snippet == nil || len(c.Culprit.Snippet.Lines) == 0 {
		t.Fatalf("snippet = %+v, want the rebuilt culprit's snippet", c.Culprit.Snippet)
	}
	if c.Issue.Handled == nil || !*c.Issue.Handled {
		t.Fatalf("issue.handled = %v, want true rebuilt from the occurrence", c.Issue.Handled)
	}
	found := false
	for _, d := range c.Degraded {
		if d.Component == "issue" && strings.Contains(d.Detail, "rebuilt from first occurrence") {
			found = true
		}
	}
	if !found {
		t.Fatalf("degraded = %+v, want the CC-3 rebuild note", c.Degraded)
	}
}
