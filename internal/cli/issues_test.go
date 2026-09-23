package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/explain"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

func TestIssuesCommandTree(t *testing.T) {
	root := Root()
	var issuesCmdFound bool
	for _, cmd := range root.Commands() {
		if cmd.Name() != "issues" {
			continue
		}
		issuesCmdFound = true
		if cmd.PersistentFlags().Lookup("store") == nil {
			t.Fatal("issues command is missing persistent --store")
		}
		want := map[string]bool{"list": false, "show": false, "resolve": false, "reopen": false, "ignore": false}
		for _, sub := range cmd.Commands() {
			if _, ok := want[sub.Name()]; ok {
				want[sub.Name()] = true
				if sub.Flags().Lookup("json") == nil {
					t.Errorf("issues %s is missing --json", sub.Name())
				}
			}
		}
		for name, found := range want {
			if !found {
				t.Errorf("issues command is missing %s", name)
			}
		}
	}
	if !issuesCmdFound {
		t.Fatal("root command is missing issues")
	}
}

func TestIssuesListMissingStoreJSONIsEmptyArray(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.veclite")
	output, err := executeIssuesCommand(t, path, "list", "--json")
	if err != nil {
		t.Fatalf("issues list --json: %v", err)
	}
	if strings.TrimSpace(output) != "[]" {
		t.Fatalf("output = %q, want []", output)
	}
}

func TestIssuesListFiltersLimitsAndRendersHumanOutput(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	store := openIssueCLIStore(t, path)
	base := time.Date(2026, 7, 27, 12, 0, 0, 0, time.UTC)
	old, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		ObservedAt: base, Project: "monitor", Service: "api", Message: "old failure", Title: "Old",
	})
	if err != nil {
		t.Fatalf("seed old issue: %v", err)
	}
	newest, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		ObservedAt: base.Add(time.Minute), Project: "monitor", Service: "worker", Message: "new failure", Title: "Newest",
	})
	if err != nil {
		t.Fatalf("seed newest issue: %v", err)
	}
	if _, err := store.Resolve(old.ID); err != nil {
		t.Fatalf("resolve old: %v", err)
	}
	closeIssueCLIStore(t, store)

	output, err := executeIssuesCommand(t, path, "list", "--status", "open", "--project", "MONITOR", "--limit", "1", "--json")
	if err != nil {
		t.Fatalf("filtered list: %v", err)
	}
	var got []issues.Issue
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("decode list: %v (%s)", err, output)
	}
	if len(got) != 1 || got[0].ID != newest.ID {
		t.Fatalf("filtered issues = %+v, want %s", got, newest.ID)
	}

	// --all: this test's own ambient working directory (wherever `go test`
	// happens to run from) must never affect which of these seeded issues
	// show up -- see TestIssuesListDefaultsToCurrentProject for the
	// dedicated coverage of the default-to-current-project behavior itself.
	human, err := executeIssuesCommand(t, path, "list", "--all")
	if err != nil {
		t.Fatalf("human list: %v", err)
	}
	// The human table shows short ids (see docs/contracts/issue-context-v1.md),
	// not the full "ISS-..." id, and a WHERE/24H/EVENTS-shaped header --
	// see writeIssuesListHuman.
	for _, value := range []string{"EVENTS", "24H", "WHERE", shortIssueID(newest.ID), shortIssueID(old.ID), "Newest"} {
		if !strings.Contains(human, value) {
			t.Errorf("human list missing %q:\n%s", value, human)
		}
	}
	// Bare `monitor issues` (no "list") renders identically.
	bare, err := executeIssuesCommand(t, path, "--all")
	if err != nil {
		t.Fatalf("bare issues: %v", err)
	}
	if bare != human {
		t.Fatalf("bare `issues` output differs from `issues list`:\nbare:\n%s\nlist:\n%s", bare, human)
	}

	if _, err := executeIssuesCommand(t, path, "list", "--status", "broken"); err == nil || !strings.Contains(err.Error(), "invalid issue status") {
		t.Fatalf("invalid status error = %v", err)
	}
	for _, limit := range []string{"0", "201"} {
		if _, err := executeIssuesCommand(t, path, "list", "--limit", limit); err == nil || !strings.Contains(err.Error(), "between 1 and 200") {
			t.Fatalf("limit %s error = %v", limit, err)
		}
	}
}

// TestIssuesListWindowFlags exercises --since/--until/--run-id/--release/
// --kind end to end through the CLI command (E2.6), against the same
// issues.Store.List filters the MCP monitor_issues tool maps.
func TestIssuesListWindowFlags(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	store := openIssueCLIStore(t, path)
	base := time.Now().UTC().Add(-time.Hour)
	exIssue, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		ObservedAt: base, Project: "monitor", Kind: issues.KindException, Message: "boom",
		RunID: "run-a", Release: "rel-a",
	})
	if err != nil {
		t.Fatalf("seed exception issue: %v", err)
	}
	if _, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		ObservedAt: base, Project: "monitor", Kind: "monitor.alert.cpu_spike", Message: "cpu spike", RunID: "run-a",
	}); err != nil {
		t.Fatalf("seed alert issue: %v", err)
	}
	closeIssueCLIStore(t, store)

	output, err := executeIssuesCommand(t, path, "list", "--all", "--kind", "exception", "--json")
	if err != nil {
		t.Fatalf("list --kind exception: %v", err)
	}
	var got []issues.Issue
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, output)
	}
	if len(got) != 1 || got[0].ID != exIssue.ID {
		t.Fatalf("--kind exception = %+v, want only %s", got, exIssue.ID)
	}

	output, err = executeIssuesCommand(t, path, "list", "--all", "--run-id", "run-a", "--json")
	if err != nil {
		t.Fatalf("list --run-id: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("--run-id run-a = %+v, want both issues", got)
	}

	output, err = executeIssuesCommand(t, path, "list", "--all", "--release", "rel-a", "--json")
	if err != nil {
		t.Fatalf("list --release: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != exIssue.ID {
		t.Fatalf("--release rel-a = %+v, want only %s", got, exIssue.ID)
	}

	output, err = executeIssuesCommand(t, path, "list", "--all", "--since", "24h", "--json")
	if err != nil {
		t.Fatalf("list --since 24h: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("--since 24h = %+v, want both issues (seeded 1h ago)", got)
	}

	output, err = executeIssuesCommand(t, path, "list", "--all", "--since", "1m", "--json")
	if err != nil {
		t.Fatalf("list --since 1m: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("--since 1m = %+v, want none (seeded 1h ago)", got)
	}

	// --until N means "active at/before N ago": a bound OLDER than the
	// issues' first_seen (1h ago) excludes them (their whole window starts
	// after the bound); a bound more RECENT than first_seen includes them.
	output, err = executeIssuesCommand(t, path, "list", "--all", "--until", "2h", "--json")
	if err != nil {
		t.Fatalf("list --until 2h: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("--until 2h = %+v, want none (both issues' first_seen is 1h ago, after the 2h-ago bound)", got)
	}

	output, err = executeIssuesCommand(t, path, "list", "--all", "--until", "30m", "--json")
	if err != nil {
		t.Fatalf("list --until 30m: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("--until 30m = %+v, want both issues (first_seen 1h ago is at/before the 30m-ago bound)", got)
	}

	if _, err := executeIssuesCommand(t, path, "list", "--kind", "bogus"); err == nil {
		t.Fatal("--kind bogus succeeded")
	}
	if _, err := executeIssuesCommand(t, path, "list", "--since", "not-a-time"); err == nil {
		t.Fatal("--since not-a-time succeeded")
	}
	if _, err := executeIssuesCommand(t, path, "list", "--until", "not-a-time"); err == nil {
		t.Fatal("--until not-a-time succeeded")
	}
}

func TestIssuesShowJSONBoundsOccurrences(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	store := openIssueCLIStore(t, path)
	issue, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		Project: "monitor", Service: "cli", Message: "failure 1", RunID: "run-1",
		Run:      &issues.RunContext{ID: "run-1", Environment: "preview", StepID: "test"},
		Evidence: []issues.EvidenceRef{{Kind: "monitor.incident", URI: "fcheap://stash/stash-1"}},
	})
	if err != nil {
		t.Fatalf("seed first occurrence: %v", err)
	}
	if _, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		Project: "monitor", Service: "cli", Message: "failure 2", RunID: "run-2",
	}); err != nil {
		t.Fatalf("seed second occurrence: %v", err)
	}
	closeIssueCLIStore(t, store)

	output, err := executeIssuesCommand(t, path, "show", issue.ID, "--occurrences", "1", "--json")
	if err != nil {
		t.Fatalf("issues show: %v", err)
	}
	var got issueDetailOutput
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("decode show: %v (%s)", err, output)
	}
	if got.Issue.ID != issue.ID || len(got.Occurrences) != 1 || !got.OccurrencesTruncated {
		t.Fatalf("show output = %+v", got)
	}
	if got.Occurrences[0].RunID != "run-2" {
		t.Fatalf("latest occurrence = %+v, want run-2", got.Occurrences[0])
	}
	if got.OccurrencesTruncated != true {
		t.Fatal("show must disclose that typed evidence from older occurrences was truncated")
	}

	output, err = executeIssuesCommand(t, path, "show", issue.ID, "--occurrences", "2", "--json")
	if err != nil {
		t.Fatalf("issues show full detail: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("decode full show: %v", err)
	}
	if got.Occurrences[1].Run == nil || got.Occurrences[1].Run.StepID != "test" ||
		len(got.Occurrences[1].Evidence) != 1 || got.Occurrences[1].Evidence[0].URI != "fcheap://stash/stash-1" {
		t.Fatalf("typed Run/Evidence missing from CLI detail: %+v", got.Occurrences[1])
	}

	for _, limit := range []string{"0", "201"} {
		if _, err := executeIssuesCommand(t, path, "show", issue.ID, "--occurrences", limit); err == nil {
			t.Fatalf("show --occurrences %s succeeded", limit)
		}
	}
}

// TestIssuesShowTruncationAccountsForCoalescedCount verifies that a
// coalesced occurrence (issues.OccurrenceInput.Count > 1) is not reported
// as truncated just because one retained row stands for several raw
// events: OccurrencesTruncated must compare issue.OccurrenceCount against
// the SUM of the retained rows' Count, not len(occurrences).
func TestIssuesShowTruncationAccountsForCoalescedCount(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	store := openIssueCLIStore(t, path)
	issue, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		Project: "monitor", Service: "cli", Message: "burst", Count: 5,
	})
	if err != nil {
		t.Fatalf("seed coalesced occurrence: %v", err)
	}
	closeIssueCLIStore(t, store)

	output, err := executeIssuesCommand(t, path, "show", issue.ID, "--occurrences", "1", "--json")
	if err != nil {
		t.Fatalf("issues show: %v", err)
	}
	var got issueDetailOutput
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("decode show: %v (%s)", err, output)
	}
	if len(got.Occurrences) != 1 || got.Occurrences[0].Count != 5 {
		t.Fatalf("occurrences = %+v, want one row with Count 5", got.Occurrences)
	}
	if got.Issue.OccurrenceCount != 5 {
		t.Fatalf("issue.OccurrenceCount = %d, want 5", got.Issue.OccurrenceCount)
	}
	if got.OccurrencesTruncated {
		t.Fatal("a single coalesced row (Count=5) covering the issue's whole occurrence_count must not be reported as truncated")
	}
}

func TestIssueLifecycleCommandsAreIdempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "issues.veclite")
	store := openIssueCLIStore(t, path)
	issue, _, err := store.UpsertOccurrence(issues.OccurrenceInput{Project: "monitor", Message: "boom"})
	if err != nil {
		t.Fatalf("seed issue: %v", err)
	}
	closeIssueCLIStore(t, store)

	steps := []struct {
		action string
		want   issues.Status
	}{
		{action: "resolve", want: issues.StatusResolved},
		{action: "resolve", want: issues.StatusResolved},
		{action: "reopen", want: issues.StatusOpen},
		{action: "ignore", want: issues.StatusIgnored},
		{action: "ignore", want: issues.StatusIgnored},
	}
	for _, step := range steps {
		output, err := executeIssuesCommand(t, path, step.action, issue.ID, "--json")
		if err != nil {
			t.Fatalf("issues %s: %v", step.action, err)
		}
		var got issueMutationOutput
		if err := json.Unmarshal([]byte(output), &got); err != nil {
			t.Fatalf("decode %s: %v (%s)", step.action, err, output)
		}
		if !got.Updated || got.Issue.Status != step.want {
			t.Fatalf("issues %s = %+v, want %s", step.action, got, step.want)
		}
	}
}

func TestIssueNotFoundIsTypedAndJSONIsStructured(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.veclite")
	output, err := executeIssuesCommand(t, path, "show", "ISS-MISSING", "--json")
	if err == nil {
		t.Fatal("show missing issue succeeded")
	}
	if !errors.Is(err, issues.ErrIssueNotFound) {
		t.Fatalf("show error = %v, want ErrIssueNotFound", err)
	}
	if err.Error() != "issue ISS-MISSING not found" {
		t.Fatalf("show error = %q", err)
	}
	var payload issueErrorOutput
	if jsonErr := json.Unmarshal([]byte(output), &payload); jsonErr != nil {
		t.Fatalf("decode error output: %v (%s)", jsonErr, output)
	}
	if !payload.NotFound || payload.ID != "ISS-MISSING" || payload.Error != "issue ISS-MISSING not found" {
		t.Fatalf("error payload = %+v", payload)
	}

	output, err = executeIssuesCommand(t, path, "resolve", "ISS-MISSING", "--json")
	if err == nil || !errors.Is(err, issues.ErrIssueNotFound) {
		t.Fatalf("resolve missing error = %v", err)
	}
	if jsonErr := json.Unmarshal([]byte(output), &payload); jsonErr != nil || !payload.NotFound {
		t.Fatalf("resolve error payload = %+v, decode err=%v", payload, jsonErr)
	}
}

func executeIssuesCommand(t *testing.T, storePath string, args ...string) (string, error) {
	t.Helper()
	cmd := newIssuesCmd()
	var output, stderr bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"--store", storePath}, args...))
	err := cmd.Execute()
	return output.String(), err
}

func openIssueCLIStore(t *testing.T, path string) *issues.Store {
	t.Helper()
	store, err := issues.OpenStore(path)
	if err != nil {
		t.Fatalf("open issue store: %v", err)
	}
	return store
}

func closeIssueCLIStore(t *testing.T, store *issues.Store) {
	t.Helper()
	if err := store.Close(); err != nil {
		t.Fatalf("close issue store: %v", err)
	}
}

// ---------------------------------------------------------------------------
// `monitor issue <id|short-prefix|latest>` (E2.5)
// ---------------------------------------------------------------------------

// noExternalToolsPATH points PATH at an empty directory so codemap/vecgrep/
// git are all reported unavailable quickly and deterministically -- these
// CLI-level tests care about monitor's own wiring (flags, JSON shape,
// exit-2 candidate reporting), not the real dev machine's tool versions;
// internal/explain's own tests already cover the healthy-tool paths with
// fake binaries on PATH.
func noExternalToolsPATH(t *testing.T) {
	t.Helper()
	t.Setenv("PATH", t.TempDir())
}

// seedCLIException records one exception issue straight through
// issues.RecordException (the same call `monitor run --`/`stacktrace parse
// --record` will make once E2.4/E2.1 land) with a real in-app culprit frame
// under root, for the `monitor issue`/`--at` CLI tests below.
func seedCLIException(t *testing.T, storePath, root, relFile string, line int, function string, observedAt time.Time) issues.Issue {
	t.Helper()
	abs := filepath.Join(root, relFile)
	ex := stacktrace.Exception{
		Runtime: "go", Type: "panic", Value: "boom at " + relFile, Parser: "gopanic", Level: "fatal",
		Frames: []stacktrace.Frame{{Function: function, AbsPath: abs, Filename: abs, Lineno: line}},
	}
	id := project.Identity{Slug: "polyglot", Service: "workload", Root: root, GitRoot: root}
	res, err := issues.RecordException(t.Context(), storePath, issues.DefaultWriterWait, ex, id, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: observedAt})
	if err != nil {
		t.Fatalf("seedCLIException: %v", err)
	}
	return res.Issue
}

// newCLIRoot creates a temp "git root" (just enough for project.Resolve's
// findGitRoot / internal/explain's snippet EvalSymlinks confinement -- a
// bare .git directory marker, not a real repository) with one source file.
func newCLIRoot(t *testing.T, relFile, content string) string {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(root, relFile)
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	return root
}

func executeIssueCommand(t *testing.T, storePath, root string, args ...string) (string, error) {
	t.Helper()
	cmd := newIssueCmd()
	var output, stderr bytes.Buffer
	cmd.SetOut(&output)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"--store", storePath, "--root", root}, args...))
	err := cmd.Execute()
	return output.String(), err
}

func TestIssueCommandRegisteredAtTopLevel(t *testing.T) {
	root := Root()
	for _, cmd := range root.Commands() {
		if cmd.Name() == "issue" {
			if cmd.Flags().Lookup("json") == nil || cmd.Flags().Lookup("md") == nil {
				t.Fatalf("issue command missing --json/--md")
			}
			return
		}
	}
	t.Fatal("root command is missing the top-level `issue` command")
}

func TestIssuesCommandNoLongerAliasesIssue(t *testing.T) {
	root := Root()
	for _, cmd := range root.Commands() {
		if cmd.Name() != "issues" {
			continue
		}
		for _, alias := range cmd.Aliases {
			if alias == "issue" {
				t.Fatal("`issues` still aliases `issue`; E2.5 gives `issue` its own top-level command (naming ADR's issue/issues collision row)")
			}
		}
	}
}

func TestIssueCommandHumanJSONAndMarkdown(t *testing.T) {
	noExternalToolsPATH(t)
	root := newCLIRoot(t, "src/app.go", "package app\n\nfunc doWork() {\n\tpanic(\"boom\")\n}\n")
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	issue := seedCLIException(t, storePath, root, "src/app.go", 4, "doWork", time.Now().UTC())

	human, err := executeIssueCommand(t, storePath, root, shortIssueID(issue.ID))
	if err != nil {
		t.Fatalf("issue (human): %v\n%s", err, human)
	}
	for _, want := range []string{"CULPRIT", "src/app.go:4", "doWork", "NEXT"} {
		if !strings.Contains(human, want) {
			t.Errorf("human page missing %q:\n%s", want, human)
		}
	}

	jsonOut, err := executeIssueCommand(t, storePath, root, shortIssueID(issue.ID), "--json")
	if err != nil {
		t.Fatalf("issue --json: %v\n%s", err, jsonOut)
	}
	var got explain.Context
	if err := json.Unmarshal([]byte(jsonOut), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, jsonOut)
	}
	if got.Schema != explain.Schema || got.Budget != string(explain.BudgetStandard) {
		t.Fatalf("schema/budget = %q/%q", got.Schema, got.Budget)
	}
	if got.Culprit == nil || got.Culprit.File != "src/app.go" || got.Culprit.Line != 4 {
		t.Fatalf("culprit = %+v", got.Culprit)
	}

	md, err := executeIssueCommand(t, storePath, root, shortIssueID(issue.ID), "--md")
	if err != nil {
		t.Fatalf("issue --md: %v", err)
	}
	if !strings.Contains(md, "## Culprit") || !strings.Contains(md, "src/app.go:4") {
		t.Errorf("markdown missing culprit section:\n%s", md)
	}
}

func TestIssueCommandLatestRespectsProjectFilter(t *testing.T) {
	noExternalToolsPATH(t)
	root := newCLIRoot(t, "src/app.go", "package app\n")
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	now := time.Now().UTC()
	want := seedCLIException(t, storePath, root, "src/app.go", 1, "f", now.Add(-time.Minute))
	// A more recent issue in a DIFFERENT project must not win under
	// --project polyglot.
	other := project.Identity{Slug: "other", Root: root, GitRoot: root}
	if _, err := issues.RecordException(t.Context(), storePath, issues.DefaultWriterWait,
		stacktrace.Exception{Runtime: "go", Type: "panic", Value: "elsewhere", Level: "fatal"},
		other, contextids.IDs{}, issues.RecordExceptionOptions{ObservedAt: now}); err != nil {
		t.Fatal(err)
	}

	out, err := executeIssueCommand(t, storePath, root, "latest", "--project", "polyglot", "--json")
	if err != nil {
		t.Fatalf("issue latest: %v\n%s", err, out)
	}
	var got explain.Context
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	if got.Issue.ID != want.ID {
		t.Fatalf("resolved = %s, want %s", got.Issue.ID, want.ID)
	}
	if got.ResolvedFrom == nil || got.ResolvedFrom.Project != "polyglot" {
		t.Fatalf("resolved_from = %+v", got.ResolvedFrom)
	}
}

func TestIssueCommandLatestNoMatchIsAnErrorWithRecoveryHint(t *testing.T) {
	noExternalToolsPATH(t)
	root := newCLIRoot(t, "src/app.go", "package app\n")
	storePath := filepath.Join(t.TempDir(), "issues.veclite")

	_, err := executeIssueCommand(t, storePath, root, "latest", "--project", "nothing-here")
	if err == nil || !errors.Is(err, issues.ErrIssueNotFound) {
		t.Fatalf("err = %v, want ErrIssueNotFound", err)
	}

	out, err := executeIssueCommand(t, storePath, root, "latest", "--project", "nothing-here", "--json")
	if err == nil {
		t.Fatal("expected a non-nil error even with --json")
	}
	var payload map[string]any
	if jsonErr := json.Unmarshal([]byte(out), &payload); jsonErr != nil {
		t.Fatalf("decode: %v (%s)", jsonErr, out)
	}
	if payload["recovery"] == nil || payload["not_found"] != true {
		t.Fatalf("payload = %+v, want a recovery hint and not_found:true", payload)
	}
}

func TestIssueCommandNotFound(t *testing.T) {
	noExternalToolsPATH(t)
	root := newCLIRoot(t, "src/app.go", "package app\n")
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	if _, err := issues.OpenStore(storePath); err != nil {
		t.Fatal(err)
	}

	_, err := executeIssueCommand(t, storePath, root, "ISS-MISSING")
	if err == nil || !errors.Is(err, issues.ErrIssueNotFound) {
		t.Fatalf("err = %v, want ErrIssueNotFound", err)
	}
}

// TestPrintAmbiguousIssueErrorHumanAndJSON exercises printAmbiguousIssueError
// directly (see its own doc comment for why the os.Exit(2) call is kept out
// of the tested function): specs/issues_context.yml covers the real
// process-level exit code.
func TestPrintAmbiguousIssueErrorHumanAndJSON(t *testing.T) {
	ambiguous := &issues.AmbiguousIDError{Prefix: "a0", Matches: []string{"ISS-A0AA000000000000", "ISS-A0BB000000000000"}}

	cmd := newIssueCmd()
	var stderr bytes.Buffer
	cmd.SetErr(&stderr)
	printAmbiguousIssueError(cmd, "a0", ambiguous)
	human := stderr.String()
	if !strings.Contains(human, "ambiguous") || !strings.Contains(human, "ISS-A0AA000000000000") || !strings.Contains(human, "ISS-A0BB000000000000") {
		t.Fatalf("human ambiguous output = %q", human)
	}

	cmd = newIssueCmd()
	if err := cmd.Flags().Set("json", "true"); err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	printAmbiguousIssueError(cmd, "a0", ambiguous)
	var payload map[string]any
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		t.Fatalf("decode: %v (%s)", err, stdout.String())
	}
	if payload["error"] != "ambiguous" {
		t.Fatalf("payload = %+v", payload)
	}
}

// ---------------------------------------------------------------------------
// `monitor issues --at <file:line>` (E3.4 / roadmap "errores x calor")
// ---------------------------------------------------------------------------

func TestIssuesAtExactLineFallbackWhenCodemapUnavailable(t *testing.T) {
	noExternalToolsPATH(t)
	root := newCLIRoot(t, "src/app.go", "package app\n\nfunc a() {\n\tpanic(\"a\")\n}\n\nfunc b() {\n\tpanic(\"b\")\n}\n")
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	now := time.Now().UTC()
	wantMatch := seedCLIException(t, storePath, root, "src/app.go", 4, "a", now)
	seedCLIException(t, storePath, root, "src/app.go", 8, "b", now) // different line, must not match

	out, err := executeIssuesCommand(t, storePath, "--at", "src/app.go:4", "--root", root, "--all", "--json")
	if err != nil {
		t.Fatalf("issues --at: %v\n%s", err, out)
	}
	var got struct {
		RangeResolved bool           `json:"range_resolved"`
		Issues        []issues.Issue `json:"issues"`
	}
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, out)
	}
	if got.RangeResolved {
		t.Error("range_resolved = true, want false (codemap unavailable)")
	}
	if len(got.Issues) != 1 || got.Issues[0].ID != wantMatch.ID {
		t.Fatalf("issues = %+v, want only %s", got.Issues, wantMatch.ID)
	}
}

func TestIssuesAtHumanOutputAndNoMatch(t *testing.T) {
	noExternalToolsPATH(t)
	root := newCLIRoot(t, "src/app.go", "package app\n")
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	seedCLIException(t, storePath, root, "src/app.go", 1, "f", time.Now().UTC())

	human, err := executeIssuesCommand(t, storePath, "--at", "src/app.go:1", "--root", root, "--all")
	if err != nil {
		t.Fatalf("issues --at (human): %v\n%s", err, human)
	}
	for _, want := range []string{"LINE", "ID", "no range resolved"} {
		if !strings.Contains(human, want) {
			t.Errorf("human --at output missing %q:\n%s", want, human)
		}
	}

	none, err := executeIssuesCommand(t, storePath, "--at", "src/other.go:1", "--root", root, "--all")
	if err != nil {
		t.Fatalf("issues --at (no match): %v", err)
	}
	if strings.Contains(none, "LINE\tID") {
		t.Errorf("expected no table for a non-matching --at target:\n%s", none)
	}
}

// TestIssueDeprecatedSubcommandsDelegateToIssues covers `monitor issue
// list|show|resolve|...` -- the pre-E2.5 alias of `monitor issues` -- still
// working during the deprecation by delegating to a real `monitor issues
// <same args>` invocation (runDeprecatedIssueAlias), instead of failing with
// "issue list not found" or a stray-args error.
func TestIssueDeprecatedSubcommandsDelegateToIssues(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	store := openIssueCLIStore(t, storePath)
	issue, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		ObservedAt: time.Now().UTC(), Project: "polyglot", Message: "boom",
	})
	if err != nil {
		t.Fatalf("seed: %v", err)
	}
	closeIssueCLIStore(t, store)

	// --store goes AFTER the deprecated subcommand name (e.g. "monitor
	// issue list --store X"), same as every pre-E2.5 example: newIssueCmd's
	// deprecation check only looks at the FIRST raw arg (see its own doc
	// comment), so a global-style flag placed before the subcommand name
	// falls through to the (non-deprecated) single-id parsing path instead.
	run := func(args ...string) (stdout, stderr string, err error) {
		t.Helper()
		cmd := newIssueCmd()
		var out, errOut bytes.Buffer
		cmd.SetOut(&out)
		cmd.SetErr(&errOut)
		cmd.SetArgs(append(append([]string{}, args...), "--store", storePath))
		err = cmd.Execute()
		return out.String(), errOut.String(), err
	}

	out, errOut, err := run("list", "--all", "--json")
	if err != nil {
		t.Fatalf("issue list: %v", err)
	}
	if !strings.Contains(out, issue.ID) {
		t.Fatalf("issue list output = %q, want it to contain %s", out, issue.ID)
	}
	if !strings.Contains(errOut, "deprecated") {
		t.Errorf("stderr = %q, want a deprecation note", errOut)
	}

	out, _, err = run("show", issue.ID, "--json")
	if err != nil {
		t.Fatalf("issue show: %v\n%s", err, out)
	}
	var detail struct {
		Issue issues.Issue `json:"issue"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &detail); jsonErr != nil {
		t.Fatalf("decode: %v (%s)", jsonErr, out)
	}
	if detail.Issue.ID != issue.ID {
		t.Fatalf("issue show = %+v, want %s", detail.Issue, issue.ID)
	}

	out, _, err = run("resolve", issue.ID, "--json")
	if err != nil {
		t.Fatalf("issue resolve: %v\n%s", err, out)
	}
	var mutated struct {
		Issue issues.Issue `json:"issue"`
	}
	if jsonErr := json.Unmarshal([]byte(out), &mutated); jsonErr != nil {
		t.Fatalf("decode: %v (%s)", jsonErr, out)
	}
	if mutated.Issue.Status != issues.StatusResolved {
		t.Fatalf("status after issue resolve = %q, want resolved", mutated.Issue.Status)
	}
}

// TestIssueTooManyPositionalArgsIsAFriendlyError: a NON-deprecated first
// argument followed by extra positional args (not a typo'd `issues`
// subcommand) still gets a clear error instead of a generic cobra one.
func TestIssueTooManyPositionalArgsIsAFriendlyError(t *testing.T) {
	cmd := newIssueCmd()
	var stdout bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetArgs([]string{"a07e", "extra"})
	err := cmd.Execute()
	if err == nil || !strings.Contains(err.Error(), "did you mean") {
		t.Fatalf("err = %v, want a friendly too-many-args error", err)
	}
}

// TestIssuesListDefaultsToCurrentProject is the dedicated coverage for
// "monitor issues sin argumentos lista el proyecto actual": bare `monitor
// issues` (and `issues list`, with neither --project nor --all) lists only
// the project resolved from the current working directory in the HUMAN
// view; --all restores the old "every project" behavior. --json is
// deliberately UNAFFECTED by this default: it is the machine/scripting path
// (specs, an agent's own tooling) that has always meant "every project
// matching every other explicit filter", and silently narrowing that by
// cwd would make an unchanged script's answer depend on which directory it
// happened to run from.
func TestIssuesListDefaultsToCurrentProject(t *testing.T) {
	storePath := filepath.Join(t.TempDir(), "issues.veclite")
	store := openIssueCLIStore(t, storePath)
	now := time.Now().UTC()
	current, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		ObservedAt: now, Project: "acme-project", Message: "here",
	})
	if err != nil {
		t.Fatalf("seed current-project issue: %v", err)
	}
	other, _, err := store.UpsertOccurrence(issues.OccurrenceInput{
		ObservedAt: now.Add(time.Minute), Project: "other-project", Message: "elsewhere",
	})
	if err != nil {
		t.Fatalf("seed other-project issue: %v", err)
	}
	closeIssueCLIStore(t, store)

	// project.Resolve derives the slug from the git root's basename -- an
	// isolated dir named exactly "acme-project" with a bare .git marker is
	// enough (findGitRoot only stats for .git; it never needs a real repo).
	cwd := filepath.Join(t.TempDir(), "acme-project")
	if err := os.MkdirAll(filepath.Join(cwd, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Chdir(cwd)

	// --json: unaffected by cwd, still lists both projects by default.
	output, err := executeIssuesCommand(t, storePath, "list", "--json")
	if err != nil {
		t.Fatalf("list --json: %v", err)
	}
	var got []issues.Issue
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatalf("decode: %v (%s)", err, output)
	}
	if len(got) != 2 {
		t.Fatalf("default --json list = %+v, want BOTH issues (unaffected by cwd)", got)
	}

	// The human view (no --json) defaults to the current project.
	human, err := executeIssuesCommand(t, storePath, "list")
	if err != nil {
		t.Fatalf("human list: %v", err)
	}
	if !strings.Contains(human, shortIssueID(current.ID)) {
		t.Errorf("human list = %q, want it to contain the current-project issue %s", human, current.ID)
	}
	if strings.Contains(human, shortIssueID(other.ID)) {
		t.Errorf("human list = %q, want it to EXCLUDE the other-project issue %s by default", human, other.ID)
	}
	// The human header names the defaulted project (writeIssuesListHeader).
	if !strings.Contains(human, "acme-project") {
		t.Errorf("human list header = %q, want it to name the defaulted current project", human)
	}

	// --all restores "every project" in the human view too.
	humanAll, err := executeIssuesCommand(t, storePath, "list", "--all")
	if err != nil {
		t.Fatalf("human list --all: %v", err)
	}
	if !strings.Contains(humanAll, shortIssueID(current.ID)) || !strings.Contains(humanAll, shortIssueID(other.ID)) {
		t.Fatalf("human list --all = %q, want both issues", humanAll)
	}

	// Bare `monitor issues` (no "list") behaves the same way.
	bare, err := executeIssuesCommand(t, storePath)
	if err != nil {
		t.Fatalf("bare issues: %v", err)
	}
	if bare != human {
		t.Fatalf("bare issues output differs from issues list:\nbare:\n%s\nlist:\n%s", bare, human)
	}
}
