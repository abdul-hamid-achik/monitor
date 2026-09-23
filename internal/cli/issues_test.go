package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/issues"
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

	human, err := executeIssuesCommand(t, path, "list")
	if err != nil {
		t.Fatalf("human list: %v", err)
	}
	for _, value := range []string{"ID", "STATUS", newest.ID, old.ID, "Newest"} {
		if !strings.Contains(human, value) {
			t.Errorf("human list missing %q:\n%s", value, human)
		}
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

	output, err := executeIssuesCommand(t, path, "list", "--kind", "exception", "--json")
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

	output, err = executeIssuesCommand(t, path, "list", "--run-id", "run-a", "--json")
	if err != nil {
		t.Fatalf("list --run-id: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("--run-id run-a = %+v, want both issues", got)
	}

	output, err = executeIssuesCommand(t, path, "list", "--release", "rel-a", "--json")
	if err != nil {
		t.Fatalf("list --release: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].ID != exIssue.ID {
		t.Fatalf("--release rel-a = %+v, want only %s", got, exIssue.ID)
	}

	output, err = executeIssuesCommand(t, path, "list", "--since", "24h", "--json")
	if err != nil {
		t.Fatalf("list --since 24h: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Fatalf("--since 24h = %+v, want both issues (seeded 1h ago)", got)
	}

	output, err = executeIssuesCommand(t, path, "list", "--since", "1m", "--json")
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
	output, err = executeIssuesCommand(t, path, "list", "--until", "2h", "--json")
	if err != nil {
		t.Fatalf("list --until 2h: %v", err)
	}
	if err := json.Unmarshal([]byte(output), &got); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("--until 2h = %+v, want none (both issues' first_seen is 1h ago, after the 2h-ago bound)", got)
	}

	output, err = executeIssuesCommand(t, path, "list", "--until", "30m", "--json")
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
