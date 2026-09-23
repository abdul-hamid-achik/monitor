// Package issues groups durable error occurrences into actionable issues.
package issues

import (
	"errors"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// Status is the lifecycle state of an issue.
type Status string

const (
	// StatusOpen means the issue needs attention.
	StatusOpen Status = "open"
	// StatusResolved means the issue was fixed or otherwise completed.
	StatusResolved Status = "resolved"
	// StatusIgnored means new occurrences are retained without reopening the issue.
	StatusIgnored Status = "ignored"
)

const FingerprintVersionV1 = "v1"

// FingerprintVersionV2 marks issues grouped by FingerprintV2Exception (the
// exception-chain rule; see fingerprint.go and docs/contracts/
// local-sentry-naming.md §6). Existing FingerprintVersionV1 issues
// (investigate's dominant in-app function, watch's alert rules) are
// untouched by this scheme and keep hashing under v1.
const FingerprintVersionV2 = "v2"

// KindException is the Issue/Occurrence Kind literal RecordException
// writes; see the naming ADR's Issue.Kind row ("exception", "investigation",
// "monitor.alert.<rule>").
const KindException = "exception"

var (
	// ErrIssueNotFound is returned when an issue ID does not exist.
	ErrIssueNotFound = errors.New("issue not found")
	// ErrReadOnly is returned when a mutating method is used on a read-only store.
	ErrReadOnly = errors.New("issue store is read-only")
)

// Issue is a group of occurrences with the same stable fingerprint.
type Issue struct {
	ID                 string     `json:"id"`
	Fingerprint        string     `json:"fingerprint"`
	FingerprintVersion string     `json:"fingerprint_version"`
	Project            string     `json:"project"`
	Service            string     `json:"service"`
	Kind               string     `json:"kind"`
	Title              string     `json:"title"`
	Message            string     `json:"message"`
	ExceptionType      string     `json:"exception_type"`
	Symbols            []string   `json:"symbols"`
	Severity           string     `json:"severity"`
	Status             Status     `json:"status"`
	FirstSeen          time.Time  `json:"first_seen"`
	LastSeen           time.Time  `json:"last_seen"`
	OccurrenceCount    int64      `json:"occurrence_count"`
	ReopenedCount      int64      `json:"reopened_count"`
	ResolvedAt         *time.Time `json:"resolved_at,omitempty"`

	// The fields below are additive (E2.3/E2.6): a v1.15 issue JSON decodes
	// unchanged into a zero-valued set of these (see fingerprint_test.go's
	// TestIssueOccurrenceDecodeV1_15Unchanged).

	// Culprit is the exception-chain rule's pick (docs/contracts/
	// local-sentry-naming.md §6): the innermost cause's crash frame when
	// it's in_app, else the outer exception's crash frame, else nil.
	// Tracks the ISSUE'S LATEST occurrence (like LatestException below),
	// not necessarily the occurrence that first opened the issue.
	Culprit *Culprit `json:"culprit,omitempty"`
	// LatestException mirrors the most recently recorded occurrence's
	// exception detail. Unlike Title/Message/ExceptionType/Severity/Level/
	// Handled (frozen at issue creation, like every other Issue field
	// before E2.3), this one field is intentionally kept current: frames
	// can shift line numbers, and a chain's shape can change slightly
	// between occurrences of the same issue.
	LatestException *ExceptionInfo `json:"latest_exception,omitempty"`
	// FirstGitSHA is the local git HEAD (contextids.IDs.GitSHA) at the time
	// this issue was FIRST observed -- set once, at creation, never touched
	// by a later occurrence.
	FirstGitSHA string `json:"first_git_sha,omitempty"`
	// Level classifies an exception in the stacktrace vocabulary ("fatal",
	// "error", "warning"; see stacktrace.Exception.Level) -- a closed set,
	// distinct from the free-form Severity field above. Set once, at
	// creation.
	Level string `json:"level,omitempty"`
	// Handled mirrors the outer exception's stacktrace.Exception.Handled at
	// creation: true for a caught-and-printed error, false for an uncaught
	// crash, nil when the parser gave no signal.
	Handled *bool `json:"handled,omitempty"`
	// Runs and Releases are small, bounded (maxIssueRunsReleases),
	// deduplicated sets of every distinct Occurrence.RunID / Occurrence.
	// Release this issue has seen, oldest evicted first. They exist so
	// ListOptions.RunID / ListOptions.Release (E2.6) can filter issues in
	// O(1) per issue without scanning the occurrences collection -- a
	// window query stays fast even against a store holding tens of
	// thousands of occurrences.
	Runs     []string `json:"runs,omitempty"`
	Releases []string `json:"releases,omitempty"`
}

// Culprit is the single frame a reader (the CLI, MCP, and later
// internal/explain) blames as the most actionable line for an issue. See
// docs/contracts/local-sentry-naming.md §6 (the selection rule) and §7
// (Source).
type Culprit struct {
	Function string `json:"function,omitempty"`
	// FQN is filled in later by a codemap-aware reader (internal/explain,
	// E2.5); RecordException and the fingerprint/culprit rule in this
	// package never call codemap, so FQN always starts empty here.
	FQN  string `json:"fqn,omitempty"`
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	// Source is "stack" (a real parsed frame; see FingerprintV2Exception's
	// culprit rule) or "message_search" (inferred from message text by
	// internal/explain's E2.8, which has vecgrep/git grep available). This
	// package only ever produces "stack" or a nil *Culprit.
	Source string `json:"source,omitempty"`
}

// CauseInfo is one entry in ExceptionInfo.Causes: a chain link's type and,
// when its own crash frame is in_app, that cause's own culprit.
type CauseInfo struct {
	Type    string   `json:"type,omitempty"`
	Culprit *Culprit `json:"culprit,omitempty"`
}

// ExceptionInfo is the bounded (~2 KB serialized) exception detail
// RecordException derives from a stacktrace.Exception: enough to render a
// culprit, causes, and an in-app frame list without retaining every
// occurrence's full (possibly hundreds-of-frames-long) stack forever. See
// Occurrence.Exception and Issue.LatestException.
type ExceptionInfo struct {
	Type    string `json:"type,omitempty"`
	Value   string `json:"value,omitempty"`
	Runtime string `json:"runtime,omitempty"`
	Handled *bool  `json:"handled,omitempty"`
	// Frames holds at most maxExceptionFrames in-app frames (oldest to
	// newest, crash frame last -- stacktrace.Exception's own convention),
	// pre-filtered so a page render never has to walk the raw stack again.
	Frames []stacktrace.Frame `json:"frames,omitempty"`
	// Causes holds at most maxExceptionCauses chain entries, outer to
	// innermost (mirrors stacktrace.Exception.Chained's ordering).
	Causes []CauseInfo `json:"causes,omitempty"`
}

// RunContext correlates a local event with an ephemeral/CI run without
// affecting issue identity. It matches Monitor and Chalupa's shared IDs.
type RunContext struct {
	ID           string `json:"id,omitempty"`
	Environment  string `json:"environment,omitempty"`
	DeploymentID string `json:"deployment_id,omitempty"`
	StepID       string `json:"step_id,omitempty"`
	Suite        string `json:"suite,omitempty"`
	Attempt      string `json:"attempt,omitempty"`
	Release      string `json:"release,omitempty"`
	GitSHA       string `json:"git_sha,omitempty"`
}

// EvidenceRef is a credential-free pointer to evidence owned by Monitor or
// file.cheap. URI is normally fcheap://stash/... or monitor://incidents/....
type EvidenceRef struct {
	Kind     string `json:"kind"`
	URI      string `json:"uri"`
	TreeHash string `json:"tree_hash,omitempty"`
}

// Occurrence is one observation of an issue. Run, release, PID, and tree hash
// are useful correlation data but deliberately do not participate in the
// issue fingerprint.
type Occurrence struct {
	ID            string            `json:"id"`
	IssueID       string            `json:"issue_id"`
	ObservedAt    time.Time         `json:"observed_at"`
	Project       string            `json:"project"`
	Service       string            `json:"service"`
	Kind          string            `json:"kind"`
	Title         string            `json:"title"`
	Message       string            `json:"message"`
	ExceptionType string            `json:"exception_type"`
	Symbols       []string          `json:"symbols"`
	Severity      string            `json:"severity"`
	RunID         string            `json:"run_id,omitempty"`
	Release       string            `json:"release,omitempty"`
	PID           int32             `json:"pid,omitempty"`
	TreeHash      string            `json:"tree_hash,omitempty"`
	EvidenceRefs  []string          `json:"evidence_refs"`
	Metadata      map[string]string `json:"metadata,omitempty"`
	Run           *RunContext       `json:"run,omitempty"`
	Evidence      []EvidenceRef     `json:"evidence"`
	// Count is how many raw events this occurrence subsumes. A coalesced
	// burst (e.g. the same stack trace repeated within a short window)
	// writes one Occurrence with Count > 1 instead of one row per event.
	Count int64 `json:"count,omitempty"`
	// Exception is the structured exception detail (E2.3). It is retained
	// ONLY on an issue's FIRST occurrence -- see Store.upsertOccurrenceLocked
	// -- to bound store size; every later occurrence leaves this nil and
	// relies on Issue.LatestException for the current shape.
	Exception *ExceptionInfo `json:"exception,omitempty"`
	// DedupeKey identifies the exact raw event this occurrence represents
	// (docs/contracts/local-sentry-naming.md §5: a live launch's ±3s window
	// key, or a replay's inode+offset key). When a later
	// OccurrenceInput.DedupeKey matches one already retained for the same
	// issue, the store folds the repeat into this existing row instead of
	// inserting a duplicate -- see UpsertResult.Deduped.
	DedupeKey string `json:"dedupe_key,omitempty"`
}

// Event is the local-Sentry event model. Each persisted event is an
// Occurrence grouped into an Issue by FingerprintV1.
type Event = Occurrence

// OccurrenceInput is the data accepted by UpsertOccurrence.
type OccurrenceInput struct {
	ObservedAt    time.Time
	Project       string
	Service       string
	Kind          string
	Title         string
	Message       string
	ExceptionType string
	Symbols       []string
	Severity      string
	RunID         string
	Release       string
	PID           int32
	TreeHash      string
	EvidenceRefs  []string
	Metadata      map[string]string
	Run           *RunContext
	Evidence      []EvidenceRef
	// Count is how many raw events this single occurrence write represents.
	// Zero or negative defaults to 1 (normalizeOccurrenceInput). A coalesced
	// burst passes the real count so the issue's cumulative
	// OccurrenceCount reflects every raw event, not just every write.
	Count int64

	// The fields below are E2.3 additions for exception occurrences
	// (RecordException sets all of them). They are all optional: a caller
	// that never sets them (investigate, watch) gets byte-for-byte the same
	// behavior as before this package added them.

	// Fingerprint, precomputed by the caller, is used as-is instead of
	// UpsertOccurrence deriving one from FingerprintV1. RecordException
	// passes FingerprintV2Exception's result here.
	Fingerprint string
	// FingerprintVersion tags which scheme produced Fingerprint (see
	// FingerprintVersionV1/V2). Only meaningful together with a non-empty
	// Fingerprint; an empty Fingerprint always hashes under V1 regardless
	// of this field, matching UpsertOccurrence's historical behavior.
	FingerprintVersion string
	// Exception becomes ExceptionInfo detail: retained on the occurrence
	// only when it is an issue's first (see Occurrence.Exception), and
	// always mirrored onto Issue.LatestException.
	Exception *ExceptionInfo
	// Culprit, when set, becomes the issue's Culprit (tracks the latest
	// occurrence, alongside Exception/LatestException above).
	Culprit *Culprit
	// Level classifies the occurrence in the stacktrace vocabulary
	// ("fatal", "error", "warning"); see Issue.Level.
	Level string
	// DedupeKey, when non-empty, is checked against the issue's retained
	// occurrences before inserting a new one -- see UpsertResult.Deduped
	// and docs/contracts/local-sentry-naming.md §5.
	DedupeKey string
}

// UpsertResult is UpsertOccurrence's richer sibling return
// (UpsertOccurrenceResult): the same Issue/Occurrence pair, plus whether the
// write was folded into an already-retained occurrence via
// OccurrenceInput.DedupeKey instead of creating a new one.
type UpsertResult struct {
	Issue      Issue
	Occurrence Occurrence
	// Deduped is true when OccurrenceInput.DedupeKey already matched one of
	// the issue's retained occurrences: that existing occurrence is
	// returned unchanged -- no new row, no OccurrenceCount increment, no
	// Runs/Releases aggregate update.
	Deduped bool
}

// ListOptions filters issues. Empty fields match all issues.
type ListOptions struct {
	Statuses []Status
	Project  string
	Service  string
	// Since and Until (E2.6) bound an issue's activity window: an issue
	// matches when [FirstSeen, LastSeen] overlaps [Since, Until]. A zero
	// time.Time on either side leaves that side unbounded.
	Since time.Time
	Until time.Time
	// RunID and Release (E2.6) match against an issue's Runs/Releases
	// aggregate sets (case-insensitive) -- see Issue.Runs's doc comment for
	// why this stays fast even over a large occurrences collection.
	RunID   string
	Release string
	// Kind filters by Issue.Kind: "" or "any" matches every kind,
	// "exception" and "investigation" match Issue.Kind exactly, "alert"
	// matches any Issue.Kind with the "monitor.alert." prefix watch.go
	// writes (see docs/contracts/local-sentry-naming.md's Issue.Kind row).
	// Any other value is rejected the same way an invalid Statuses entry
	// is.
	Kind  string
	Limit int
}
