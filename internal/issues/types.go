// Package issues groups durable error occurrences into actionable issues.
package issues

import (
	"errors"
	"time"
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
}

// ListOptions filters issues. Empty fields match all issues.
type ListOptions struct {
	Statuses []Status
	Project  string
	Service  string
	Limit    int
}
