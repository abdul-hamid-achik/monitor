package issues

import (
	"errors"
	"path/filepath"
	"testing"
)

// seedIssueWithFingerprint writes one occurrence whose issue ID is fully
// controlled via OccurrenceInput.Fingerprint (newIssue truncates/uppercases
// it into "ISS-" + fingerprint[:16]) -- letting ResolveID tests construct
// exact, deterministic short_id collisions instead of hoping a real SHA-256
// hash happens to collide.
func seedIssueWithFingerprint(t *testing.T, store *Store, fingerprint string) Issue {
	t.Helper()
	issue, _, err := store.UpsertOccurrence(OccurrenceInput{
		Project: "polyglot", Kind: KindException, Message: "boom " + fingerprint,
		Fingerprint: fingerprint, FingerprintVersion: FingerprintVersionV2,
	})
	if err != nil {
		t.Fatalf("seed issue %s: %v", fingerprint, err)
	}
	return issue
}

func TestResolveIDExactFullIDMatch(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	issue := seedIssueWithFingerprint(t, store, "aaaa000000000000")

	got, err := store.ResolveID(issue.ID)
	if err != nil || got != issue.ID {
		t.Fatalf("ResolveID(%q) = (%q, %v), want (%q, nil)", issue.ID, got, err, issue.ID)
	}
	// Case-insensitive on the literal too.
	got, err = store.ResolveID("iss-aaaa000000000000")
	if err != nil || got != issue.ID {
		t.Fatalf("ResolveID(lowercase) = (%q, %v), want (%q, nil)", got, err, issue.ID)
	}
}

func TestResolveIDUnambiguousPrefix(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	issue := seedIssueWithFingerprint(t, store, "b0b1000000000000")

	for _, prefix := range []string{"b0b1", "B0B1", "iss-b0b1", "b0b10000"} {
		got, err := store.ResolveID(prefix)
		if err != nil || got != issue.ID {
			t.Fatalf("ResolveID(%q) = (%q, %v), want (%q, nil)", prefix, got, err, issue.ID)
		}
	}
}

func TestResolveIDAmbiguousPrefixListsEveryMatch(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	a := seedIssueWithFingerprint(t, store, "aaaa000000000000")
	b := seedIssueWithFingerprint(t, store, "aaaa111111111111")

	_, err := store.ResolveID("aaaa")
	var ambiguous *AmbiguousIDError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("err = %v, want *AmbiguousIDError", err)
	}
	if len(ambiguous.Matches) != 2 {
		t.Fatalf("matches = %v, want both %s and %s", ambiguous.Matches, a.ID, b.ID)
	}
	seen := map[string]bool{}
	for _, m := range ambiguous.Matches {
		seen[m] = true
	}
	if !seen[a.ID] || !seen[b.ID] {
		t.Fatalf("matches = %v, want %s and %s", ambiguous.Matches, a.ID, b.ID)
	}
}

func TestResolveIDExactMatchWinsOverAmbiguousPrefix(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	full := seedIssueWithFingerprint(t, store, "cccc000000000000")
	seedIssueWithFingerprint(t, store, "cccc111111111111") // shares the "CCCC" short_id prefix

	got, err := store.ResolveID(full.ID)
	if err != nil {
		t.Fatalf("exact full ID should win outright even with a colliding prefix elsewhere: %v", err)
	}
	if got != full.ID {
		t.Fatalf("got %q, want %q", got, full.ID)
	}
}

func TestResolveIDNotFound(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	seedIssueWithFingerprint(t, store, "dddd000000000000")

	if _, err := store.ResolveID("zzzz"); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("err = %v, want ErrIssueNotFound", err)
	}
}

func TestResolveIDEmptyInput(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	if _, err := store.ResolveID("   "); err == nil {
		t.Fatal("expected an error for an empty id")
	}
}

func TestResolveIDOnEmptyStore(t *testing.T) {
	store := openTestStore(t, filepath.Join(t.TempDir(), "issues.veclite"))
	if _, err := store.ResolveID("a07e"); !errors.Is(err, ErrIssueNotFound) {
		t.Fatalf("err = %v, want ErrIssueNotFound", err)
	}
}

func TestAmbiguousIDErrorMessage(t *testing.T) {
	err := &AmbiguousIDError{Prefix: "a07e", Matches: []string{"ISS-A07E1111", "ISS-A07E2222"}}
	msg := err.Error()
	if msg == "" {
		t.Fatal("empty error message")
	}
}
