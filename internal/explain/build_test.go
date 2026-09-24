package explain

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
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
