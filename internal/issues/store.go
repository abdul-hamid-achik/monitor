package issues

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	veclite "github.com/abdul-hamid-achik/veclite"
)

const (
	issuesCollection      = "issues"
	occurrencesCollection = "issue_occurrences"
	// StorePathEnv overrides the default issue database for CLI/test isolation.
	StorePathEnv = "MONITOR_ISSUES_STORE"
	// DefaultMaxIssues bounds distinct issue groups in the durable store.
	DefaultMaxIssues = 10_000
	// DefaultMaxOccurrences bounds retained occurrence detail globally. Issue
	// counters remain cumulative when older occurrence bodies are evicted.
	DefaultMaxOccurrences = 100_000
	// DefaultWriterWait is how long OpenStoreWait / WithWriter retry a
	// concurrent writer's exclusive lock (e.g. a live `watch --stash`)
	// before giving up. Every short-lived writer (investigate, issues
	// resolve/reopen/ignore, the MCP investigate path, and watch --stash's
	// per-delivery write) uses this so none of them fail with
	// veclite.ErrFileLocked just because another one is mid-write.
	DefaultWriterWait = 5 * time.Second
)

// Store is a durable veclite-backed issue store.
type Store struct {
	mu       sync.Mutex
	db       *veclite.DB
	path     string
	readOnly bool
}

// DefaultPath returns the per-user issue store and ensures its private parent
// directory has mode 0700.
func DefaultPath() (string, error) {
	dataRoot := strings.TrimSpace(os.Getenv("XDG_DATA_HOME"))
	if dataRoot == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve home directory: %w", err)
		}
		dataRoot = filepath.Join(home, ".local", "share")
	} else if !filepath.IsAbs(dataRoot) {
		return "", fmt.Errorf("XDG_DATA_HOME must be an absolute path: %q", dataRoot)
	}
	dir := filepath.Join(dataRoot, "monitor")
	if err := ensurePrivateDir(dir); err != nil {
		return "", err
	}
	return filepath.Join(dir, "issues.veclite"), nil
}

// ResolvePath applies explicit override, environment override, then default.
func ResolvePath(override string) (string, error) {
	if path := strings.TrimSpace(override); path != "" {
		return path, nil
	}
	if path := strings.TrimSpace(os.Getenv(StorePathEnv)); path != "" {
		return path, nil
	}
	return DefaultPath()
}

// OpenStore opens a writable issue store, creating its parent directory and
// collections when necessary.
func OpenStore(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("open issue store: path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve issue store path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
		return nil, fmt.Errorf("create issue store directory: %w", err)
	}
	db, err := veclite.Open(abs)
	if err != nil {
		return nil, fmt.Errorf("open issue store: %w", err)
	}
	store := &Store{db: db, path: abs}
	if err := store.ensureCollections(); err != nil {
		_ = db.Close()
		return nil, err
	}
	if err := db.Sync(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("initialize issue store: %w", err)
	}
	if err := store.secureFile(); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

// OpenReadOnly opens a point-in-time shared-read view of an issue store.
func OpenReadOnly(path string) (*Store, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("open read-only issue store: path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve issue store path: %w", err)
	}
	db, err := veclite.Open(abs, veclite.WithReadOnly(true), veclite.WithSharedRead(true))
	if err != nil {
		return nil, fmt.Errorf("open read-only issue store: %w", err)
	}
	return &Store{db: db, path: abs, readOnly: true}, nil
}

func ensurePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return fmt.Errorf("create issue store directory: %w", err)
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return fmt.Errorf("secure issue store directory: %w", err)
	}
	return nil
}

func (s *Store) ensureCollections() error {
	if s.db == nil {
		return errors.New("issue store not open")
	}
	for _, name := range []string{issuesCollection, occurrencesCollection} {
		if s.db.HasCollection(name) {
			continue
		}
		if _, err := s.db.CreateCollection(name); err != nil {
			return fmt.Errorf("create %s collection: %w", name, err)
		}
	}
	return nil
}

// UpsertOccurrence records one occurrence, creates its issue when necessary,
// and automatically reopens a resolved issue as a regression. It is the
// original, narrower sibling of UpsertOccurrenceResult -- kept unchanged so
// every existing caller (investigate, watch) that never sets
// OccurrenceInput's E2.3 fields (Fingerprint, DedupeKey, Exception, Culprit)
// keeps its exact historical behavior.
func (s *Store) UpsertOccurrence(input OccurrenceInput) (Issue, Occurrence, error) {
	result, err := s.UpsertOccurrenceResult(input)
	return result.Issue, result.Occurrence, err
}

// UpsertOccurrenceResult is UpsertOccurrence's richer sibling (E2.3): same
// semantics, plus honoring OccurrenceInput.Fingerprint/DedupeKey/Exception/
// Culprit and reporting whether the write deduped against an existing
// occurrence. RecordException is the usual caller; UpsertOccurrence itself
// is a thin wrapper around this for callers that don't need UpsertResult.
func (s *Store) UpsertOccurrenceResult(input OccurrenceInput) (UpsertResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.upsertOccurrenceLocked(input)
}

// upsertOccurrenceLocked does the real work; s.mu must already be held.
func (s *Store) upsertOccurrenceLocked(input OccurrenceInput) (UpsertResult, error) {
	if err := s.requireWritable(); err != nil {
		return UpsertResult{}, err
	}
	input = normalizeOccurrenceInput(input)
	if err := validateOccurrenceInput(input); err != nil {
		return UpsertResult{}, err
	}

	fingerprint := strings.TrimSpace(input.Fingerprint)
	if fingerprint == "" {
		fingerprint = FingerprintV1(FingerprintInput{
			Project: input.Project, Service: input.Service, Kind: input.Kind,
			ExceptionType: input.ExceptionType, Message: input.Message, Symbols: input.Symbols,
		})
	}
	issueRecord, err := s.db.Collection(issuesCollection).FindOne(veclite.Equal("fingerprint", fingerprint))
	if err != nil && !errors.Is(err, veclite.ErrNotFound) {
		return UpsertResult{}, fmt.Errorf("find issue by fingerprint: %w", err)
	}

	now := input.ObservedAt
	var issue Issue
	isNewIssue := issueRecord == nil
	if isNewIssue {
		issue = newIssue(input, fingerprint)
	} else {
		if err := json.Unmarshal([]byte(issueRecord.Content), &issue); err != nil {
			return UpsertResult{}, fmt.Errorf("decode issue %d: %w", issueRecord.ID, err)
		}
		normalizeIssueSlices(&issue)
		if deduped, ok, err := s.findDedupedOccurrence(issue.ID, input.DedupeKey); err != nil {
			return UpsertResult{}, err
		} else if ok {
			return UpsertResult{Issue: issue, Occurrence: deduped, Deduped: true}, nil
		}
		issue.LastSeen = laterTime(issue.LastSeen, now)
		issue.FirstSeen = earlierTime(issue.FirstSeen, now)
		issue.OccurrenceCount += input.Count
		if issue.Status == StatusResolved &&
			(issue.ResolvedAt == nil || !now.Before(*issue.ResolvedAt)) {
			issue.Status = StatusOpen
			issue.ResolvedAt = nil
			issue.ReopenedCount++
		}
		// Culprit/LatestException track the issue's LATEST occurrence (see
		// their doc comments), unlike Title/Message/ExceptionType/Severity
		// which stay frozen at creation. Only overwrite when this
		// occurrence actually carries exception detail, so a differently
		// kinded write sharing the fingerprint space (should not normally
		// happen, but defensively) never blanks out a real one.
		if input.Culprit != nil {
			issue.Culprit = input.Culprit
		}
		if input.Exception != nil {
			issue.LatestException = input.Exception
		}
	}
	applyRunReleaseAggregate(&issue, input)

	occurrence := occurrenceFromInput(input, issue.ID)
	if !isNewIssue {
		// Exception detail is retained only on an issue's FIRST occurrence
		// to bound store size (see Occurrence.Exception); newIssue already
		// copied it onto the issue as LatestException/Culprit above.
		occurrence.Exception = nil
	}
	occurrenceContent, err := json.Marshal(occurrence)
	if err != nil {
		return UpsertResult{}, fmt.Errorf("encode occurrence: %w", err)
	}
	occurrenceRecordID, err := s.db.Collection(occurrencesCollection).InsertTextDocument(string(occurrenceContent), map[string]any{
		"id": occurrence.ID, "issue_id": issue.ID, "observed_at": occurrence.ObservedAt,
		"dedupe_key": occurrence.DedupeKey,
	})
	if err != nil {
		return UpsertResult{}, fmt.Errorf("insert occurrence: %w", err)
	}
	if err := s.saveIssue(issueRecord, issue); err != nil {
		_ = s.db.Collection(occurrencesCollection).Delete(occurrenceRecordID)
		return UpsertResult{}, err
	}
	if err := s.enforceRecordBounds(DefaultMaxIssues, DefaultMaxOccurrences); err != nil {
		return UpsertResult{}, err
	}
	if err := s.syncAndSecure(); err != nil {
		return UpsertResult{}, err
	}
	return UpsertResult{Issue: issue, Occurrence: occurrence}, nil
}

// findDedupedOccurrence looks up dedupeKey among issueID's retained
// occurrences (docs/contracts/local-sentry-naming.md §5). An empty
// dedupeKey always misses -- most callers never set OccurrenceInput.
// DedupeKey, and an empty key must never accidentally match another empty-
// keyed occurrence.
func (s *Store) findDedupedOccurrence(issueID, dedupeKey string) (Occurrence, bool, error) {
	key := strings.TrimSpace(dedupeKey)
	if key == "" {
		return Occurrence{}, false, nil
	}
	record, err := s.db.Collection(occurrencesCollection).FindOne(
		veclite.Equal("issue_id", issueID), veclite.Equal("dedupe_key", key))
	if errors.Is(err, veclite.ErrNotFound) {
		return Occurrence{}, false, nil
	}
	if err != nil {
		return Occurrence{}, false, fmt.Errorf("find occurrence by dedupe key: %w", err)
	}
	var occurrence Occurrence
	if err := json.Unmarshal([]byte(record.Content), &occurrence); err != nil {
		return Occurrence{}, false, fmt.Errorf("decode occurrence %d: %w", record.ID, err)
	}
	normalizeOccurrenceSlices(&occurrence)
	return occurrence, true, nil
}

// maxIssueRunsReleases bounds Issue.Runs and Issue.Releases; see their doc
// comment on Issue.
const maxIssueRunsReleases = 25

// applyRunReleaseAggregate folds input's RunID/Release into issue's
// deduplicated, size-bounded Runs/Releases sets.
func applyRunReleaseAggregate(issue *Issue, input OccurrenceInput) {
	issue.Runs = appendBoundedUnique(issue.Runs, input.RunID, maxIssueRunsReleases)
	issue.Releases = appendBoundedUnique(issue.Releases, input.Release, maxIssueRunsReleases)
}

func appendBoundedUnique(values []string, value string, max int) []string {
	value = strings.TrimSpace(value)
	if value == "" {
		return values
	}
	for _, v := range values {
		if v == value {
			return values
		}
	}
	values = append(values, value)
	if len(values) > max {
		values = values[len(values)-max:]
	}
	return values
}

// enforceRecordBounds caps both collections. When an old issue group is
// evicted, its retained occurrences are deleted with it; occurrence-only FIFO
// eviction keeps the issue's cumulative OccurrenceCount intact.
func (s *Store) enforceRecordBounds(maxIssues, maxOccurrences int) error {
	if maxIssues < 1 || maxOccurrences < 1 {
		return errors.New("issue retention limits must be positive")
	}
	issueColl := s.db.Collection(issuesCollection)
	if count := issueColl.Count(); count > maxIssues {
		records, err := issueColl.Find()
		if err != nil {
			return fmt.Errorf("list issues for retention: %w", err)
		}
		sort.Slice(records, func(i, j int) bool {
			if records[i].CreatedAt.Equal(records[j].CreatedAt) {
				return records[i].ID < records[j].ID
			}
			return records[i].CreatedAt.Before(records[j].CreatedAt)
		})
		for _, record := range records[:count-maxIssues] {
			issue, err := decodeIssue(record)
			if err != nil {
				return err
			}
			if _, err := s.db.Collection(occurrencesCollection).DeleteWhere(veclite.Equal("issue_id", issue.ID)); err != nil {
				return fmt.Errorf("delete occurrences for evicted issue %s: %w", issue.ID, err)
			}
			if err := issueColl.Delete(record.ID); err != nil {
				return fmt.Errorf("evict issue %s: %w", issue.ID, err)
			}
		}
	}

	occurrenceColl := s.db.Collection(occurrencesCollection)
	if count := occurrenceColl.Count(); count > maxOccurrences {
		evicted := occurrenceColl.EnforceMemoryLimit(veclite.MemoryConfig{
			MaxRecords:        maxOccurrences,
			EvictionPolicy:    "fifo",
			EvictionBatchSize: count,
		})
		if evicted != count-maxOccurrences {
			return fmt.Errorf("evict occurrences: removed %d, want %d", evicted, count-maxOccurrences)
		}
	}
	return nil
}

func newIssue(input OccurrenceInput, fingerprint string) Issue {
	version := strings.TrimSpace(input.FingerprintVersion)
	if version == "" || strings.TrimSpace(input.Fingerprint) == "" {
		version = FingerprintVersionV1
	}
	// A FingerprintV1/V2 hash is always a 64-char sha256 hex digest, well
	// over 16 chars. OccurrenceInput.Fingerprint (E2.3) lets a caller supply
	// its own precomputed value, though, so guard the slice instead of
	// trusting it: a short custom fingerprint gets the whole thing as its
	// ID suffix rather than panicking.
	idSuffix := fingerprint
	if len(idSuffix) > 16 {
		idSuffix = idSuffix[:16]
	}
	issue := Issue{
		ID:                 "ISS-" + strings.ToUpper(idSuffix),
		Fingerprint:        fingerprint,
		FingerprintVersion: version,
		Project:            input.Project,
		Service:            input.Service,
		Kind:               input.Kind,
		Title:              input.Title,
		Message:            input.Message,
		ExceptionType:      input.ExceptionType,
		Symbols:            cloneStrings(input.Symbols),
		Severity:           input.Severity,
		Level:              input.Level,
		Handled:            exceptionHandled(input.Exception),
		Status:             StatusOpen,
		FirstSeen:          input.ObservedAt,
		LastSeen:           input.ObservedAt,
		OccurrenceCount:    input.Count,
		Culprit:            input.Culprit,
		LatestException:    input.Exception,
		FirstGitSHA:        runGitSHA(input.Run),
	}
	return issue
}

// exceptionHandled clones ExceptionInfo.Handled so the stored Issue never
// aliases a caller-owned pointer.
func exceptionHandled(info *ExceptionInfo) *bool {
	if info == nil || info.Handled == nil {
		return nil
	}
	v := *info.Handled
	return &v
}

func runGitSHA(run *RunContext) string {
	if run == nil {
		return ""
	}
	return run.GitSHA
}

func occurrenceFromInput(input OccurrenceInput, issueID string) Occurrence {
	return Occurrence{
		ID: randomID("OCC-"), IssueID: issueID, ObservedAt: input.ObservedAt,
		Project: input.Project, Service: input.Service, Kind: input.Kind,
		Title: input.Title, Message: input.Message, ExceptionType: input.ExceptionType,
		Symbols: cloneStrings(input.Symbols), Severity: input.Severity,
		RunID: input.RunID, Release: input.Release, PID: input.PID, TreeHash: input.TreeHash,
		EvidenceRefs: cloneStrings(input.EvidenceRefs), Metadata: cloneMap(input.Metadata),
		Run: cloneRun(input.Run), Evidence: cloneEvidence(input.Evidence), Count: input.Count,
		Exception: input.Exception, DedupeKey: strings.TrimSpace(input.DedupeKey),
	}
}

func (s *Store) saveIssue(record *veclite.Record, issue Issue) error {
	content, err := json.Marshal(issue)
	if err != nil {
		return fmt.Errorf("encode issue: %w", err)
	}
	payload := map[string]any{
		"id": issue.ID, "fingerprint": issue.Fingerprint, "status": string(issue.Status),
		"project": issue.Project, "service": issue.Service, "last_seen": issue.LastSeen,
	}
	coll := s.db.Collection(issuesCollection)
	if record == nil {
		if _, err := coll.InsertTextDocument(string(content), payload); err != nil {
			return fmt.Errorf("insert issue: %w", err)
		}
		return nil
	}
	if err := coll.UpdateDocument(record.ID, string(content), payload); err != nil {
		return fmt.Errorf("update issue %s: %w", issue.ID, err)
	}
	return nil
}

// Get returns an issue by ID.
func (s *Store) Get(id string) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return Issue{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Issue{}, errors.New("get issue: id is required")
	}
	if !s.db.HasCollection(issuesCollection) {
		return Issue{}, fmt.Errorf("%w: %s", ErrIssueNotFound, id)
	}
	record, err := s.db.Collection(issuesCollection).FindOne(veclite.Equal("id", id))
	if errors.Is(err, veclite.ErrNotFound) {
		return Issue{}, fmt.Errorf("%w: %s", ErrIssueNotFound, id)
	}
	if err != nil {
		return Issue{}, fmt.Errorf("get issue %s: %w", id, err)
	}
	return decodeIssue(record)
}

// List returns matching issues newest-first.
func (s *Store) List(opts ListOptions) ([]Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	// Validate filters before the HasCollection short-circuit below: an
	// invalid --status or --kind must be rejected the same way against a
	// brand-new, still-empty store as against a populated one, not silently
	// accepted just because there is nothing yet to filter.
	statuses := make(map[Status]struct{}, len(opts.Statuses))
	for _, status := range opts.Statuses {
		if !validStatus(status) {
			return nil, fmt.Errorf("list issues: invalid status %q", status)
		}
		statuses[status] = struct{}{}
	}
	kind := strings.ToLower(strings.TrimSpace(opts.Kind))
	if !validKindFilter(kind) {
		return nil, fmt.Errorf("list issues: invalid kind %q", opts.Kind)
	}
	if !s.db.HasCollection(issuesCollection) {
		return []Issue{}, nil
	}
	records, err := s.db.Collection(issuesCollection).Find()
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}
	result := make([]Issue, 0, len(records))
	for _, record := range records {
		issue, err := decodeIssue(record)
		if err != nil {
			return nil, err
		}
		if len(statuses) > 0 {
			if _, ok := statuses[issue.Status]; !ok {
				continue
			}
		}
		if opts.Project != "" && !strings.EqualFold(opts.Project, issue.Project) {
			continue
		}
		if opts.Service != "" && !strings.EqualFold(opts.Service, issue.Service) {
			continue
		}
		// Since/Until (E2.6): an issue matches when its activity window
		// [FirstSeen, LastSeen] overlaps [Since, Until]. A zero bound on
		// either side leaves that side unbounded.
		if !opts.Since.IsZero() && issue.LastSeen.Before(opts.Since) {
			continue
		}
		if !opts.Until.IsZero() && issue.FirstSeen.After(opts.Until) {
			continue
		}
		if opts.RunID != "" && !containsFold(issue.Runs, opts.RunID) {
			continue
		}
		if opts.Release != "" && !containsFold(issue.Releases, opts.Release) {
			continue
		}
		if !matchesKind(issue.Kind, kind) {
			continue
		}
		result = append(result, issue)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].LastSeen.Equal(result[j].LastSeen) {
			return result[i].ID < result[j].ID
		}
		return result[i].LastSeen.After(result[j].LastSeen)
	})
	if opts.Limit > 0 && len(result) > opts.Limit {
		result = result[:opts.Limit]
	}
	return result, nil
}

// Occurrences returns an issue's occurrences newest-first.
func (s *Store) Occurrences(issueID string, limit int) ([]Occurrence, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireOpen(); err != nil {
		return nil, err
	}
	issueID = strings.TrimSpace(issueID)
	if issueID == "" {
		return nil, errors.New("list occurrences: issue id is required")
	}
	if !s.db.HasCollection(occurrencesCollection) {
		return []Occurrence{}, nil
	}
	records, err := s.db.Collection(occurrencesCollection).Find(veclite.Equal("issue_id", issueID))
	if err != nil {
		return nil, fmt.Errorf("list occurrences for %s: %w", issueID, err)
	}
	result := make([]Occurrence, 0, len(records))
	for _, record := range records {
		var occurrence Occurrence
		if err := json.Unmarshal([]byte(record.Content), &occurrence); err != nil {
			return nil, fmt.Errorf("decode occurrence %d: %w", record.ID, err)
		}
		normalizeOccurrenceSlices(&occurrence)
		result = append(result, occurrence)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ObservedAt.Equal(result[j].ObservedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].ObservedAt.After(result[j].ObservedAt)
	})
	if limit > 0 && len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

// Resolve marks an issue resolved. Calling it repeatedly is idempotent.
func (s *Store) Resolve(id string) (Issue, error) {
	return s.setStatus(id, StatusResolved)
}

// Reopen marks a resolved or ignored issue open. Calling it repeatedly is idempotent.
func (s *Store) Reopen(id string) (Issue, error) {
	return s.setStatus(id, StatusOpen)
}

// Ignore marks an issue ignored. New occurrences do not automatically reopen it.
func (s *Store) Ignore(id string) (Issue, error) {
	return s.setStatus(id, StatusIgnored)
}

func (s *Store) setStatus(id string, status Status) (Issue, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.requireWritable(); err != nil {
		return Issue{}, err
	}
	id = strings.TrimSpace(id)
	if id == "" {
		return Issue{}, errors.New("update issue status: id is required")
	}
	record, err := s.db.Collection(issuesCollection).FindOne(veclite.Equal("id", id))
	if errors.Is(err, veclite.ErrNotFound) {
		return Issue{}, fmt.Errorf("%w: %s", ErrIssueNotFound, id)
	}
	if err != nil {
		return Issue{}, fmt.Errorf("find issue %s: %w", id, err)
	}
	issue, err := decodeIssue(record)
	if err != nil {
		return Issue{}, err
	}
	if issue.Status == status {
		return issue, nil
	}
	issue.Status = status
	if status == StatusResolved {
		now := time.Now().UTC()
		issue.ResolvedAt = &now
	} else {
		issue.ResolvedAt = nil
	}
	if err := s.saveIssue(record, issue); err != nil {
		return Issue{}, err
	}
	if err := s.syncAndSecure(); err != nil {
		return Issue{}, err
	}
	return issue, nil
}

// Close releases the underlying veclite handle.
func (s *Store) Close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.db == nil {
		return nil
	}
	readOnly := s.readOnly
	err := s.db.Close()
	s.db = nil
	if err != nil {
		return fmt.Errorf("close issue store: %w", err)
	}
	if !readOnly {
		if err := s.secureFile(); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) syncAndSecure() error {
	if err := s.db.Sync(); err != nil {
		return fmt.Errorf("sync issue store: %w", err)
	}
	return s.secureFile()
}

func (s *Store) secureFile() error {
	if err := os.Chmod(s.path, 0o600); err != nil {
		return fmt.Errorf("secure issue store: %w", err)
	}
	return nil
}

func (s *Store) requireOpen() error {
	if s.db == nil {
		return errors.New("issue store not open")
	}
	return nil
}

func (s *Store) requireWritable() error {
	if err := s.requireOpen(); err != nil {
		return err
	}
	if s.readOnly {
		return ErrReadOnly
	}
	return nil
}

func decodeIssue(record *veclite.Record) (Issue, error) {
	var issue Issue
	if err := json.Unmarshal([]byte(record.Content), &issue); err != nil {
		return Issue{}, fmt.Errorf("decode issue %d: %w", record.ID, err)
	}
	normalizeIssueSlices(&issue)
	return issue, nil
}

func normalizeOccurrenceInput(input OccurrenceInput) OccurrenceInput {
	if input.ObservedAt.IsZero() {
		input.ObservedAt = time.Now().UTC()
	} else {
		input.ObservedAt = input.ObservedAt.UTC()
	}
	input.Project = strings.TrimSpace(input.Project)
	input.Service = strings.TrimSpace(input.Service)
	input.Kind = strings.TrimSpace(input.Kind)
	input.Title = strings.TrimSpace(input.Title)
	input.Message = strings.TrimSpace(input.Message)
	input.ExceptionType = strings.TrimSpace(input.ExceptionType)
	input.Severity = strings.ToLower(strings.TrimSpace(input.Severity))
	input.Symbols = cleanStrings(input.Symbols)
	input.EvidenceRefs = cleanStrings(input.EvidenceRefs)
	input.Evidence = cleanEvidence(input.Evidence)
	if input.Count <= 0 {
		input.Count = 1
	}
	return input
}

func validateOccurrenceInput(input OccurrenceInput) error {
	if input.Project == "" {
		return errors.New("upsert occurrence: project is required")
	}
	if input.Message == "" && input.ExceptionType == "" && len(input.Symbols) == 0 {
		return errors.New("upsert occurrence: message, exception type, or symbol is required")
	}
	return nil
}

func validStatus(status Status) bool {
	return status == StatusOpen || status == StatusResolved || status == StatusIgnored
}

// alertKindPrefix is the literal watch.go writes for every alert-derived
// Issue.Kind ("monitor.alert.<rule>"); see docs/contracts/
// local-sentry-naming.md's Issue.Kind row.
const alertKindPrefix = "monitor.alert."

// validKindFilter reports whether kind (already lower-cased and trimmed) is
// one of ListOptions.Kind's accepted sentinel values, including "" (no
// filter).
func validKindFilter(kind string) bool {
	switch kind {
	case "", "any", KindException, "investigation", "alert":
		return true
	default:
		return false
	}
}

// matchesKind reports whether issueKind satisfies a (lower-cased, trimmed)
// ListOptions.Kind filter; see ListOptions.Kind's doc comment.
func matchesKind(issueKind, kind string) bool {
	switch kind {
	case "", "any":
		return true
	case "alert":
		return strings.HasPrefix(issueKind, alertKindPrefix)
	default:
		return strings.EqualFold(issueKind, kind)
	}
}

// containsFold reports whether values contains target, case-insensitively.
func containsFold(values []string, target string) bool {
	for _, v := range values {
		if strings.EqualFold(v, target) {
			return true
		}
	}
	return false
}

func earlierTime(a, b time.Time) time.Time {
	if a.IsZero() || b.Before(a) {
		return b
	}
	return a
}

func laterTime(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

func cleanStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			result = append(result, value)
		}
	}
	return result
}

func cloneStrings(values []string) []string {
	result := make([]string, len(values))
	copy(result, values)
	return result
}

func cloneMap(values map[string]string) map[string]string {
	if values == nil {
		return map[string]string{}
	}
	result := make(map[string]string, len(values))
	for key, value := range values {
		result[key] = value
	}
	return result
}

func cloneRun(value *RunContext) *RunContext {
	if value == nil {
		return nil
	}
	copy := *value
	return &copy
}

func cleanEvidence(values []EvidenceRef) []EvidenceRef {
	result := make([]EvidenceRef, 0, len(values))
	for _, value := range values {
		value.Kind = strings.TrimSpace(value.Kind)
		value.URI = strings.TrimSpace(value.URI)
		value.TreeHash = strings.TrimSpace(value.TreeHash)
		if value.URI != "" {
			result = append(result, value)
		}
	}
	return result
}

func cloneEvidence(values []EvidenceRef) []EvidenceRef {
	result := make([]EvidenceRef, len(values))
	copy(result, values)
	return result
}

func normalizeIssueSlices(issue *Issue) {
	if issue.Symbols == nil {
		issue.Symbols = []string{}
	}
}

func normalizeOccurrenceSlices(occurrence *Occurrence) {
	if occurrence.Symbols == nil {
		occurrence.Symbols = []string{}
	}
	if occurrence.EvidenceRefs == nil {
		occurrence.EvidenceRefs = []string{}
	}
	if occurrence.Evidence == nil {
		occurrence.Evidence = []EvidenceRef{}
	}
	if occurrence.Metadata == nil {
		occurrence.Metadata = map[string]string{}
	}
	if occurrence.Count <= 0 {
		// Records written before Count existed decode with the zero value;
		// every occurrence represents at least one raw event.
		occurrence.Count = 1
	}
}

func randomID(prefix string) string {
	var value [12]byte
	if _, err := rand.Read(value[:]); err == nil {
		return prefix + strings.ToUpper(hex.EncodeToString(value[:]))
	}
	sum := FingerprintV1(FingerprintInput{Message: fmt.Sprintf("%d", time.Now().UnixNano())})
	return prefix + strings.ToUpper(sum[:24])
}
