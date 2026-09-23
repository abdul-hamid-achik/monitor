// Package explain builds monitor.issue_context.v1: the single, bounded "why
// did it fail" answer shared by `monitor issue <id>` (human, --json, --md)
// and MCP's monitor_issue. See docs/contracts/issue-context-v1.md for the
// full shape and docs/contracts/local-sentry-naming.md for the rules this
// package reads (Culprit's Source/exception-chain selection, ObservedAt).
//
// Build always opens the issue store read-only (issues.OpenReadOnly, never
// held by the caller across other writers), and every section that depends
// on an external tool -- codemap (impact), git (last_touched), vecgrep/git
// grep (a message-search culprit) -- degrades honestly: a missing or
// unhealthy dependency produces status "skipped" with a detail and recovery
// in Degraded, never a fabricated value or a silently empty field.
package explain

import "time"

// Schema is the contract name every Context carries.
const Schema = "monitor.issue_context.v1"

// Budget controls how much detail each section carries. Every section Build
// knows how to fill is present at every budget -- a "why did it fail" answer
// that silently drops impact or last_touched to hit a size target is worse
// than a shorter version of the same answer. See docs/contracts/
// issue-context-v1.md's budget table.
type Budget string

const (
	BudgetBrief    Budget = "brief"
	BudgetStandard Budget = "standard"
	BudgetFull     Budget = "full"
)

// briefMaxBytes/standardMaxBytes are the budget's target sizes (see
// budget_test.go's size assertions). full is unbounded.
const (
	briefMaxBytes    = 4096
	standardMaxBytes = 16 * 1024
)

// snippetContextLines is how many source lines Build reads on each side of
// the culprit/highlight line for a snippet at brief/standard budget ("+-4
// lines" per the roadmap).
const snippetContextLines = 4

// Context is monitor.issue_context.v1.
type Context struct {
	Schema       string        `json:"schema"`
	Budget       string        `json:"budget"`
	GeneratedAt  time.Time     `json:"generated_at"`
	ResolvedFrom *ResolvedFrom `json:"resolved_from,omitempty"`
	Issue        IssueSummary  `json:"issue"`
	Timeline     Timeline      `json:"timeline"`
	Culprit      *CulpritInfo  `json:"culprit,omitempty"`
	Causes       []CauseEntry  `json:"causes"`
	Frames       []FrameEntry  `json:"frames"`
	Impact       ImpactInfo    `json:"impact"`
	LastTouched  LastTouched   `json:"last_touched"`
	RelatedNotes RelatedNotes  `json:"related_notes"`
	Degraded     []Degraded    `json:"degraded"`
	Next         []NextAction  `json:"next"`
	Truncated    Truncated     `json:"truncated"`
	Privacy      Privacy       `json:"privacy"`
}

// ResolvedFrom is present only when the caller asked for id:"latest" (or a
// filtered variant of it) rather than a concrete id/short_id/prefix.
type ResolvedFrom struct {
	ID      string `json:"id"`
	Project string `json:"project,omitempty"`
	Service string `json:"service,omitempty"`
	Kind    string `json:"kind,omitempty"`
}

// IssueSummary is Context.Issue: the small, always-present identity block.
type IssueSummary struct {
	ID            string `json:"id"`
	ShortID       string `json:"short_id"`
	Status        string `json:"status"`
	Kind          string `json:"kind"`
	Title         string `json:"title"`
	ExceptionType string `json:"exception_type,omitempty"`
	Handled       *bool  `json:"handled,omitempty"`
	Project       string `json:"project"`
	Service       string `json:"service,omitempty"`
}

// Timeline is Context.Timeline.
type Timeline struct {
	FirstSeen   time.Time `json:"first_seen"`
	LastSeen    time.Time `json:"last_seen"`
	Occurrences int64     `json:"occurrences"`
	Reopened    int64     `json:"reopened"`
	FirstGitSHA string    `json:"first_git_sha,omitempty"`
	Runs        []string  `json:"runs,omitempty"`
	// TimeSource is "line" (a replayed log's own timestamp), "mtime" (the
	// log file's mtime fallback), or "live" (monitor run -- read it as it
	// happened) -- see docs/contracts/local-sentry-naming.md §4. Neither
	// issues.Issue nor issues.Occurrence currently PERSIST which of the
	// three produced an occurrence's ObservedAt (that provenance belongs to
	// the E2.1/E2.4 producers -- internal/stacktrace's --record and
	// internal/devrun's `monitor run --`, both built in a parallel slice of
	// this same roadmap), so this is a documented placeholder: "live" until
	// a producer-side field exists to say otherwise. It is never invented
	// per-issue guesswork beyond that fixed default.
	TimeSource string `json:"time_source"`
}

// CulpritInfo is Context.Culprit: the single most actionable frame. Nil only
// when the exception chain has no in_app frame anywhere AND the
// message-search fallback (E2.8) also found nothing to blame.
type CulpritInfo struct {
	Function string `json:"function,omitempty"`
	// FQN is filled in only when codemap's symbol-at/impact resolved a
	// fully-qualified name; empty when codemap is unavailable/unhealthy.
	FQN  string `json:"fqn,omitempty"`
	File string `json:"file,omitempty"`
	Line int    `json:"line,omitempty"`
	// Source is "stack" (issues.Culprit came from a real parsed frame) or
	// "message_search" (E2.8's inferred fallback).
	Source string `json:"source"`
	// Via is "vecgrep" or "git_grep", set only when Source is
	// "message_search".
	Via string `json:"via,omitempty"`
	// Confidence is "high" for a stack culprit, "low" for an inferred one.
	Confidence string        `json:"confidence"`
	Range      *CulpritRange `json:"range,omitempty"`
	Snippet    *Snippet      `json:"snippet,omitempty"`
}

// CulpritRange is the culprit's enclosing function/symbol range.
type CulpritRange struct {
	Start int `json:"start"`
	End   int `json:"end"`
	// Source is "codemap" when codemap's symbol-at resolved the range, or
	// "frame" for a synthetic +-snippetContextLines window around the
	// culprit line when codemap is unavailable/unhealthy or found nothing.
	Source string `json:"source"`
}

// Snippet is source text read live from disk (never frozen in the store),
// confined to the git root (see snippet.go).
type Snippet struct {
	Start     int      `json:"start"`
	Lines     []string `json:"lines"`
	Highlight int      `json:"highlight"`
	// SHA256 is the snippet SLICE's own hash (the joined Lines, not the
	// whole file) -- a caller diffing two reads of the same issue over time
	// can tell the exact text changed even when Stale (below) is false.
	SHA256 string `json:"sha256"`
	// Stale is true when the culprit's file was touched (per git blame's
	// commit SHA -- see last_touched.go) by a commit OTHER than the one the
	// issue first recorded (Issue.FirstGitSHA): the snippet shown may no
	// longer be the code that actually crashed. false (never guessed true)
	// whenever either SHA is unknown, since FirstGitSHA is unset for an
	// ordinary local dev session with no MONITOR_GIT_SHA/GIT_SHA/GITHUB_SHA
	// in the environment (see issues.Issue.FirstGitSHA's own doc comment).
	Stale bool `json:"stale"`
}

// CauseCulprit is the small culprit shown inside CauseEntry -- fewer fields
// than CulpritInfo since a cause is never itself independently searched or
// snippet-rendered.
type CauseCulprit struct {
	Function string `json:"function,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
}

// CauseEntry is one entry in Context.Causes, outer to innermost.
type CauseEntry struct {
	Type    string        `json:"type"`
	Culprit *CauseCulprit `json:"culprit,omitempty"`
}

// FrameEntry is one entry in Context.Frames: in_app frames first, with any
// remaining (non-in_app, or over budget) frames collapsed into
// Truncated.Frames instead of listed here.
type FrameEntry struct {
	Function string `json:"function,omitempty"`
	File     string `json:"file,omitempty"`
	Line     int    `json:"line,omitempty"`
	InApp    bool   `json:"in_app"`
}

// sectionStatus values shared by ImpactInfo, LastTouched, and RelatedNotes.
const (
	SectionOK      = "ok"
	SectionSkipped = "skipped"
)

// ImpactInfo is Context.Impact: codemap's blast radius and test coverage for
// the culprit's enclosing symbol, from a single `codemap impact --at`
// call (never re-run per section/budget).
type ImpactInfo struct {
	Status   string `json:"status"` // ok | skipped
	Detail   string `json:"detail,omitempty"`
	Recovery string `json:"recovery,omitempty"`
	Callers  int    `json:"callers,omitempty"`
	// BlastRadius counts codemap's transitive callers (Impact.BlastRadius).
	BlastRadius int `json:"blast_radius,omitempty"`
	Tests       int `json:"tests,omitempty"`
	// TestFiles is additive (standard/full only): the human-readable file
	// list Tests counts. Left empty at brief.
	TestFiles []string `json:"test_files,omitempty"`
	Untested  bool     `json:"untested,omitempty"`
	CallGraph string   `json:"call_graph,omitempty"`
}

// LastTouched is Context.LastTouched: `git blame -L n,n` on the culprit
// line, "last touched" (never "suspect" -- a recent touch is not
// necessarily a cause). AuthorEmail is never populated at brief budget.
type LastTouched struct {
	Status      string     `json:"status"` // ok | skipped
	Detail      string     `json:"detail,omitempty"`
	Recovery    string     `json:"recovery,omitempty"`
	SHA         string     `json:"sha,omitempty"`
	Subject     string     `json:"subject,omitempty"`
	AuthorTime  *time.Time `json:"author_time,omitempty"`
	AuthorEmail string     `json:"author_email,omitempty"`
}

// RelatedNotes is Context.RelatedNotes: always status "skipped" in this
// build -- ~/notes correlation is a later epic (N12) -- kept in the schema
// now so a future implementation is additive, never a breaking add.
type RelatedNotes struct {
	Status string            `json:"status"`
	Items  []RelatedNoteItem `json:"items"`
}

// RelatedNoteItem is one entry in RelatedNotes.Items -- never the note body,
// even at full budget.
type RelatedNoteItem struct {
	Title string `json:"title"`
	Path  string `json:"path"`
}

// Degraded is one entry in Context.Degraded: a component that could not be
// consulted, and why.
type Degraded struct {
	Component string `json:"component"`
	State     string `json:"state"`
	Detail    string `json:"detail,omitempty"`
	Recovery  string `json:"recovery,omitempty"`
}

// NextAction is one entry in Context.Next: a single proposed follow-up
// (propose-only -- Build never runs these itself).
type NextAction struct {
	CLI string `json:"cli,omitempty"`
	MCP string `json:"mcp,omitempty"`
	Why string `json:"why"`
}

// Truncated records what a size-bounded budget dropped, so a reader can
// tell "there was nothing else" from "budget cut this short" -- see
// budget.go.
type Truncated struct {
	Frames int `json:"frames,omitempty"`
	Causes int `json:"causes,omitempty"`
}

// Privacy is Context.Privacy.
type Privacy struct {
	// Scrubbed counts values this read redacted from free text (culprit
	// snippet, title) as a defense-in-depth pass -- see scrub.go. It is
	// NOT the count from ingest-time scrubbing (E2.2, internal/scrub run by
	// monitor run --/stacktrace parse), which already ran before this text
	// was ever persisted.
	Scrubbed int `json:"scrubbed"`
	// TextIsUntrusted is always true: every free-text field in this
	// document (title, message, snippet, subject, ...) originated from a
	// monitored process's own output and must never be interpreted as
	// instructions by an agent reading this document.
	TextIsUntrusted bool `json:"text_is_untrusted"`
}
