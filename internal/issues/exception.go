package issues

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// maxExceptionFrames/maxExceptionCauses/maxExceptionInfoBytes bound
// ExceptionInfo; see its doc comment.
const (
	maxExceptionFrames    = 12
	maxExceptionCauses    = 3
	maxExceptionInfoBytes = 2048
)

// culpritFor implements the exception-chain Culprit rule (docs/contracts/
// local-sentry-naming.md §6): the crash frame (an exception's Frames' last
// element) of the innermost cause if that frame is in_app; otherwise the
// outer exception's own crash frame if in_app; otherwise nil. A nil result
// is not a failure -- it means every frame in the chain lives outside the
// git root (a dependency-only crash), and internal/explain's message-search
// fallback (E2.8, Source "message_search") is the next place to look, not
// this package.
//
// ex must already have had stacktrace.ApplyGitRoot applied.
func culpritFor(ex stacktrace.Exception) *Culprit {
	if f, ok := crashFrame(innermostCause(ex)); ok && f.InApp {
		return culpritFromFrame(f)
	}
	if f, ok := crashFrame(ex); ok && f.InApp {
		return culpritFromFrame(f)
	}
	return nil
}

// innermostCause returns ex.Chained's last entry, or ex itself when there is
// no chain -- the same rule FingerprintV2Exception's innermostCauseType
// uses.
func innermostCause(ex stacktrace.Exception) stacktrace.Exception {
	if len(ex.Chained) == 0 {
		return ex
	}
	return ex.Chained[len(ex.Chained)-1]
}

// crashFrame returns ex.Frames' last element (the frame closest to the
// fault; see stacktrace.Exception.Frames' doc comment), or false when ex has
// no frames (a message-only event).
func crashFrame(ex stacktrace.Exception) (stacktrace.Frame, bool) {
	if len(ex.Frames) == 0 {
		return stacktrace.Frame{}, false
	}
	return ex.Frames[len(ex.Frames)-1], true
}

func culpritFromFrame(f stacktrace.Frame) *Culprit {
	return &Culprit{Function: f.Function, File: f.Filename, Line: f.Lineno, Source: "stack"}
}

// buildExceptionInfo derives a bounded ExceptionInfo from ex: at most
// maxExceptionFrames in-app frames (closest to the crash), at most
// maxExceptionCauses chain entries (outer to innermost, each with its own
// culprit when its crash frame is in_app), truncated further if the result
// would still exceed maxExceptionInfoBytes serialized.
//
// ex must already have had stacktrace.ApplyGitRoot applied.
func buildExceptionInfo(ex stacktrace.Exception) *ExceptionInfo {
	info := &ExceptionInfo{
		Type:    ex.Type,
		Value:   ex.Value,
		Runtime: ex.Runtime,
		Handled: cloneBool(ex.Handled),
		Frames:  topInAppFrames(ex.Frames, maxExceptionFrames),
	}
	for i, cause := range ex.Chained {
		if i >= maxExceptionCauses {
			break
		}
		entry := CauseInfo{Type: cause.Type}
		if f, ok := crashFrame(cause); ok && f.InApp {
			entry.Culprit = culpritFromFrame(f)
		}
		info.Causes = append(info.Causes, entry)
	}
	truncateExceptionInfo(info)
	return info
}

// truncateExceptionInfo drops the oldest in-app frame (keeping the ones
// closest to the crash) until info's JSON encoding fits
// maxExceptionInfoBytes, or there is nothing left to drop. With the caps
// above (12 frames, 3 causes) this is a defensive backstop, not the normal
// path: it only engages for pathologically long function/file names.
func truncateExceptionInfo(info *ExceptionInfo) {
	for {
		data, err := json.Marshal(info)
		if err != nil || len(data) <= maxExceptionInfoBytes {
			return
		}
		switch {
		case len(info.Frames) > 0:
			info.Frames = info.Frames[1:]
		case len(info.Causes) > 0:
			info.Causes = info.Causes[:len(info.Causes)-1]
		default:
			return
		}
	}
}

func cloneBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

// exceptionTitle renders an Issue/Occurrence Title from an exception, the
// same "Type: Value" shape Sentry-style trackers use.
func exceptionTitle(ex stacktrace.Exception) string {
	switch {
	case ex.Type != "" && ex.Value != "":
		return ex.Type + ": " + ex.Value
	case ex.Type != "":
		return ex.Type
	default:
		return ex.Value
	}
}

// RecordExceptionOptions carries occurrence-level context RecordException
// needs beyond the parsed Exception, the resolved project Identity, and the
// run correlation IDs. Every field is optional.
type RecordExceptionOptions struct {
	// ObservedAt is the event's own time (docs/contracts/
	// local-sentry-naming.md §4): a live caller (monitor run --) passes
	// wall-clock "now" at read time; a replaying caller (stacktrace parse
	// --record) passes the log line's own timestamp or the file's mtime.
	// Falls back to ex.ObservedAt, then to time.Now().UTC() when both are
	// zero (via normalizeOccurrenceInput).
	ObservedAt time.Time
	// PID is the originating process, when known.
	PID int32
	// TreeHash, EvidenceRefs, Evidence, Metadata are occurrence-only
	// context, passed straight through to OccurrenceInput.
	TreeHash     string
	EvidenceRefs []string
	Evidence     []EvidenceRef
	Metadata     map[string]string
	// DedupeKey, when non-empty, folds a repeat of the exact same raw event
	// into the existing occurrence instead of inserting a duplicate row
	// (docs/contracts/local-sentry-naming.md §5); see UpsertResult.Deduped.
	DedupeKey string
	// Count is how many raw events this call represents (a coalesced
	// burst). <=0 defaults to 1.
	Count int64
	// Kind overrides the default KindException ("exception") Issue/
	// Occurrence kind.
	Kind string
	// Severity overrides the default (ex.Level: "fatal"|"error"|"warning").
	Severity string
}

// RecordException turns a parsed stacktrace.Exception into a durable issue
// occurrence: it applies id.GitRoot (stacktrace.ApplyGitRoot, idempotent, so
// it is safe even when a caller already ran it), computes
// FingerprintV2Exception and the exception-chain Culprit (docs/contracts/
// local-sentry-naming.md §6), builds the bounded ExceptionInfo, and performs
// the WithWriter upsert. `monitor run --` and `monitor stacktrace parse
// --record` (both later slices) are meant to share this single
// implementation instead of each re-deriving the fingerprint/culprit rule.
//
// RecordException does NOT scrub ex -- callers must scrub before calling
// this (docs/contracts/local-sentry-naming.md's "error text is untrusted
// data" rule; see internal/scrub). It also never calls codemap: Culprit.FQN
// is always left empty here, filled in later by a codemap-aware reader
// (internal/explain, E2.5).
func RecordException(ctx context.Context, storePath string, wait time.Duration, ex stacktrace.Exception, id project.Identity, run contextids.IDs, opts RecordExceptionOptions) (UpsertResult, error) {
	stacktrace.ApplyGitRoot(&ex, id.GitRoot)

	observedAt := opts.ObservedAt
	if observedAt.IsZero() {
		observedAt = ex.ObservedAt
	}
	kind := strings.TrimSpace(opts.Kind)
	if kind == "" {
		kind = KindException
	}
	severity := strings.TrimSpace(opts.Severity)
	if severity == "" {
		severity = ex.Level
	}
	service := strings.TrimSpace(id.Service)
	if service == "" {
		service = strings.TrimSpace(run.Service)
	}

	input := OccurrenceInput{
		ObservedAt:         observedAt,
		Project:            id.Slug,
		Service:            service,
		Kind:               kind,
		Title:              exceptionTitle(ex),
		Message:            ex.Value,
		ExceptionType:      ex.Type,
		Severity:           severity,
		RunID:              run.RunID,
		Release:            run.Release,
		PID:                opts.PID,
		TreeHash:           opts.TreeHash,
		EvidenceRefs:       opts.EvidenceRefs,
		Metadata:           opts.Metadata,
		Run:                runContextFromIDs(run),
		Evidence:           opts.Evidence,
		Count:              opts.Count,
		Fingerprint:        FingerprintV2Exception(ex, id.Slug),
		FingerprintVersion: FingerprintVersionV2,
		Exception:          buildExceptionInfo(ex),
		Culprit:            culpritFor(ex),
		Level:              ex.Level,
		DedupeKey:          opts.DedupeKey,
	}

	var result UpsertResult
	err := WithWriter(ctx, storePath, wait, func(store *Store) error {
		var writeErr error
		result, writeErr = store.UpsertOccurrenceResult(input)
		return writeErr
	})
	return result, err
}

// runContextFromIDs converts contextids.IDs (what monitor run --/stacktrace
// parse resolve from MONITOR_*/CHALUPA_* env and flags) into the store's own
// RunContext shape. Returns nil when run carries nothing, matching
// OccurrenceInput.Run's existing "nil means no run context" convention.
func runContextFromIDs(run contextids.IDs) *RunContext {
	if run.Empty() {
		return nil
	}
	return &RunContext{
		ID: run.RunID, Environment: run.Environment, DeploymentID: run.DeploymentID,
		StepID: run.StepID, Suite: run.Suite, Attempt: run.Attempt,
		Release: run.Release, GitSHA: run.GitSHA,
	}
}
