package issues

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

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

//	culpritFor implements the exception-chain Culprit rule (naming ADR §6): the innermost
//
// cause's own LAST IN-APP frame if
// it has one; otherwise the outer exception's own last in-app frame;
// otherwise nil. "Last in-app frame" is the crash frame itself when that
// frame happens to be in-app, but it is not required to be: an exception
// that bottoms out inside a runtime, stdlib, or dependency function
// in-app code called (Node's fs.readFileSync raising ENOENT, Python's
// json.loads raising JSONDecodeError, ...) still has a real, actionable
// in-app CALLER a little further up that same exception's own Frames, and
// the naming ADR's rule is explicit that the culprit only falls through to
// nil "when there is no in_app frame anywhere in the chain" -- not merely
// when the literal crash frame isn't in-app. A nil result means every frame
// in the chain lives outside the git root (a dependency-only crash with no
// in-app caller at all), and internal/explain's message-search fallback
// (E2.8, Source "message_search") is the next place to look, not this
// package.
//
// ex must already have had stacktrace.ApplyGitRoot applied.
func culpritFor(ex stacktrace.Exception) *Culprit {
	if f, ok := lastInAppFrame(innermostCause(ex)); ok {
		return culpritFromFrame(f)
	}
	if f, ok := lastInAppFrame(ex); ok {
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
// no frames (a message-only event). This is the literal crash frame,
// in-app or not; culpritFor and buildExceptionInfo's per-cause culprit use
// lastInAppFrame instead, which walks backward to find an in-app frame even
// when the crash frame itself isn't one.
func crashFrame(ex stacktrace.Exception) (stacktrace.Frame, bool) {
	if len(ex.Frames) == 0 {
		return stacktrace.Frame{}, false
	}
	return ex.Frames[len(ex.Frames)-1], true
}

// lastInAppFrame returns the in-app frame closest to the fault: ex.Frames
// walked backward from the crash frame, returning the first one with InApp
// set. Unlike crashFrame, this does not require the crash frame itself to
// be in-app -- an exception raised inside a runtime, stdlib, or dependency
// function that in-app code called (Node's fs.readFileSync ENOENT,
// Python's json.loads JSONDecodeError, ...) still has a real in-app caller
// a little further up ex's own Frames, and the naming ADR's Culprit rule
// (the naming ADR §6) wants that caller, not nil.
func lastInAppFrame(ex stacktrace.Exception) (stacktrace.Frame, bool) {
	for i := len(ex.Frames) - 1; i >= 0; i-- {
		if ex.Frames[i].InApp {
			return ex.Frames[i], true
		}
	}
	return stacktrace.Frame{}, false
}

func culpritFromFrame(f stacktrace.Frame) *Culprit {
	return &Culprit{Function: f.Function, File: f.Filename, Line: f.Lineno, Source: "stack"}
}

// buildExceptionInfo derives a bounded ExceptionInfo from ex: at most
// maxExceptionFrames in-app frames (closest to the crash), at most
// maxExceptionCauses chain entries (outer to innermost, always including the
// innermost -- see selectCauses -- each with its own culprit when it has a
// last-in-app frame; see lastInAppFrame), truncated further if the result
// would still exceed maxExceptionInfoBytes serialized (see
// truncateExceptionInfo).
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
	// (CC-4) record what the caps dropped before the size backstop runs,
	// so a reader can tell "12 frames kept" from "12 frames kept of 21":
	// maxExceptionFrames keeps only the in-app frames closest to the
	// crash, and selectCauses only the outermost-plus-innermost slice of
	// a long chain.
	inApp := 0
	for _, f := range ex.Frames {
		if f.InApp {
			inApp++
		}
	}
	info.DroppedFrames = inApp - len(info.Frames)
	kept := selectCauses(ex.Chained, maxExceptionCauses)
	info.DroppedCauses = len(ex.Chained) - len(kept)
	for _, cause := range kept {
		entry := CauseInfo{Type: cause.Type}
		if f, ok := lastInAppFrame(cause); ok {
			entry.Culprit = culpritFromFrame(f)
		}
		info.Causes = append(info.Causes, entry)
	}
	truncateExceptionInfo(info)
	return info
}

// selectCauses picks at most max entries from chained (outer to innermost)
// for ExceptionInfo.Causes, always keeping the LAST entry: chained's
// innermost cause is what Issue.Culprit and FingerprintV2Exception's
// innermost.Type both come from (the naming ADR §6),
// so it must never be the one a length cap drops. When chained is longer
// than max, this keeps its first max-1 (outermost) entries plus the very
// last (innermost) one, dropping only the middle.
func selectCauses(chained []stacktrace.Exception, max int) []stacktrace.Exception {
	if len(chained) <= max {
		return chained
	}
	if max <= 0 {
		return nil
	}
	kept := make([]stacktrace.Exception, 0, max)
	kept = append(kept, chained[:max-1]...)
	kept = append(kept, chained[len(chained)-1])
	return kept
}

// maxExceptionValueBytes bounds ExceptionInfo.Value on its own, applied
// before truncateExceptionInfo's frame/cause backstop ever runs: the
// stacktrace Joiner lets a single line run up to several KB, and Value is
// the least actionable part of the ~2 KB budget -- an in-app frame or the
// innermost cause's culprit is what actually points at a fixable line, and
// a pathologically long message must never crowd those out to zero. Matches
// stacktrace's own maxExpandedValue truncation length.
const maxExceptionValueBytes = 512

// truncateExceptionInfo first caps Value to maxExceptionValueBytes
// (UTF-8-safe), then drops the oldest in-app frame (keeping the ones
// closest to the crash), then drops causes -- preserving the outermost and,
// above all, the innermost entry as long as possible (see dropOneCause) --
// until info's JSON encoding fits maxExceptionInfoBytes, or there is
// nothing left to drop. Every frame and cause dropped here is counted in
// DroppedFrames/DroppedCauses (CC-4), on top of what the caps in
// buildExceptionInfo already recorded.
func truncateExceptionInfo(info *ExceptionInfo) {
	if len(info.Value) > maxExceptionValueBytes {
		info.Value = truncateUTF8(info.Value, maxExceptionValueBytes)
	}
	for {
		data, err := json.Marshal(info)
		if err != nil || len(data) <= maxExceptionInfoBytes {
			return
		}
		switch {
		case len(info.Frames) > 0:
			info.Frames = info.Frames[1:]
			info.DroppedFrames++
		case len(info.Causes) > 0:
			info.Causes = dropOneCause(info.Causes)
			info.DroppedCauses++
		default:
			return
		}
	}
}

// dropOneCause removes one entry from causes for truncateExceptionInfo's
// pathological-size backstop, preserving the outermost entry (causes[0])
// and, above all, the innermost one (causes[len-1] -- what Issue.Culprit and
// the fingerprint's innermost.Type both come from) as long as possible: it
// drops the entry just before the innermost first, so with 3 causes the
// middle one goes, and with 2 the outermost goes, leaving the innermost the
// very last thing this backstop ever removes.
func dropOneCause(causes []CauseInfo) []CauseInfo {
	switch len(causes) {
	case 0:
		return causes
	case 1:
		return causes[:0]
	default:
		i := len(causes) - 2
		return append(causes[:i:i], causes[i+1:]...)
	}
}

// culpritFromExceptionInfo re-derives the exception-chain Culprit from a
// stored ExceptionInfo using the same innermost-cause rule culpritFor
// applies to a raw stacktrace.Exception (the naming ADR §6): the
// innermost cause's own culprit when the chain's last kept entry has one
// (buildExceptionInfo stores each cause's lastInAppFrame pick), else the
// outer exception's last in-app frame (ExceptionInfo.Frames keeps the
// crash frame last). This is culpritFor's read-side mirror for callers
// that only hold the bounded stored detail, never the raw exception; nil
// only when the stored chain carries no in-app frame at all.
func culpritFromExceptionInfo(info *ExceptionInfo) *Culprit {
	if len(info.Causes) > 0 {
		if c := info.Causes[len(info.Causes)-1].Culprit; c != nil {
			return c
		}
	}
	if len(info.Frames) > 0 {
		return culpritFromFrame(info.Frames[len(info.Frames)-1])
	}
	return nil
}

// RebuildFromFirstOccurrence restores the exception detail an older
// monitor binary stripped from an issue record (CC-3): a mixed install
// where a v1.15 binary writes to the same store decodes the issue into
// its old struct and re-marshals it, silently dropping the additive
// Culprit/LatestException/Handled fields, while the occurrence rows keep
// their Exception detail (only an issue's FIRST occurrence retains it --
// see Occurrence.Exception). When issue is an exception-kind issue whose
// LatestException is missing, this loads its occurrences and uses the
// first one carrying Exception as LatestException, re-derives Culprit
// (see culpritFromExceptionInfo) and fills Handled from
// ExceptionInfo.Handled -- only ever filling fields that are missing,
// never overwriting data that survived the rewrite. ok reports whether a
// rebuild happened; a nil store, a store error, or no occurrence carrying
// Exception leaves issue unchanged (ok false), so the read degrades to
// the pre-rebuild behavior instead of failing.
func RebuildFromFirstOccurrence(store *Store, issue Issue) (Issue, bool) {
	if store == nil || issue.Kind != KindException || issue.LatestException != nil {
		return issue, false
	}
	occurrences, err := store.Occurrences(issue.ID, 0)
	if err != nil {
		return issue, false
	}
	// Occurrences returns newest-first and only the FIRST occurrence
	// keeps Exception detail, so the rebuild source is the last row in
	// that order that carries one -- walking from the oldest end.
	for i := len(occurrences) - 1; i >= 0; i-- {
		info := occurrences[i].Exception
		if info == nil {
			continue
		}
		rebuilt := issue
		rebuilt.LatestException = info
		if rebuilt.Culprit == nil {
			rebuilt.Culprit = culpritFromExceptionInfo(info)
		}
		if rebuilt.Handled == nil {
			rebuilt.Handled = info.Handled
		}
		return rebuilt, true
	}
	return issue, false
}

// truncateUTF8 cuts s to at most n bytes without splitting a rune in half
// (mirrors internal/stacktrace's unexported helper of the same name/shape;
// duplicated here rather than exported across the package boundary for one
// caller).
func truncateUTF8(s string, n int) string {
	if n < 0 {
		n = 0
	}
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func cloneBool(b *bool) *bool {
	if b == nil {
		return nil
	}
	v := *b
	return &v
}

// exceptionTitle renders an Issue/Occurrence Title from an exception, the
// same "Type: Value" shape Sentry-style trackers use. Returns "" when ex has
// neither -- see exceptionSymbols/crashFrameLabel for the fallback
// RecordException applies in that case (a frames-only event with no
// Type/Value, e.g. Ruby's rescue-printed `warn e.backtrace`; see ruby.go's
// rubyBacktraceGrammar).
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

// crashFrameLabel renders "func (file:line)" from the same frame culpritFor
// would blame (the innermost cause's last in-app frame, falling back to the
// outer exception's), for use as exceptionTitle's fallback when ex has no
// Type/Value at all. Returns "" when neither exception has any in-app
// frame either.
func crashFrameLabel(ex stacktrace.Exception) string {
	f, ok := lastInAppFrame(innermostCause(ex))
	if !ok {
		f, ok = lastInAppFrame(ex)
	}
	if !ok {
		return ""
	}
	return fmt.Sprintf("%s (%s:%d)", f.Function, f.Filename, f.Lineno)
}

// exceptionSymbols renders ex's outer in-app frames as "func@relfile"
// (closest to the crash first, i.e. the same order/cap as
// FingerprintV2Exception's outerFrames), for use as OccurrenceInput.Symbols
// when ex has no Type/Value: validateOccurrenceInput (store.go) requires a
// message, exception type, or symbol, and a frames-only event like a Ruby
// rescue-printed backtrace would otherwise be rejected outright even though
// it carries a perfectly good fingerprint and culprit. Returns nil when ex
// has no in-app frames either (RecordException then relies on Message/Title
// -- see the "no signal at all" case documented on RecordException).
func exceptionSymbols(ex stacktrace.Exception) []string {
	frames := topInAppFrames(ex.Frames, maxExceptionFrames)
	if len(frames) == 0 {
		return nil
	}
	symbols := make([]string, len(frames))
	for i, f := range frames {
		symbols[i] = f.Function + "@" + f.Filename
	}
	return symbols
}

// RecordExceptionOptions carries occurrence-level context RecordException
// needs beyond the parsed Exception, the resolved project Identity, and the
// run correlation IDs. Every field is optional.
type RecordExceptionOptions struct {
	// ObservedAt is the event's own time (docs/contracts/
	// the naming ADR §4): a live caller (monitor run --) passes
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
	// (the naming ADR §5); see UpsertResult.Deduped.
	DedupeKey string
	// DedupeAliases are extra keys checked against the issue's retained
	// occurrences before inserting a new one (see OccurrenceInput.
	// DedupeAliases); the primary DedupeKey is what gets retained.
	DedupeAliases []string
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
// occurrence: it applies id.GitRoot (stacktrace.ApplyGitRoot) to its own
// deep copy of ex, never the caller's (see cloneExceptionForGitRoot -- this
// makes ApplyGitRoot safe to call even when a caller already ran it, AND
// guarantees RecordException never mutates the Exception the caller handed
// it), computes FingerprintV2Exception and the exception-chain Culprit
// (the naming ADR §6), builds the bounded
// ExceptionInfo, and performs the WithWriter upsert. `monitor run --` and
// `monitor stacktrace parse --record` (both later slices) are meant to
// share this single implementation instead of each re-deriving the
// fingerprint/culprit rule.
//
// RecordException does NOT scrub ex -- callers must scrub before calling
// this (the naming ADR's "error text is untrusted
// data" rule; see internal/scrub). The same contract covers the occurrence
// context around the exception: opts.Metadata, opts.Evidence/EvidenceRefs
// and the run correlation fields (RunID, Release, and the Run block built
// from the run argument) are persisted VERBATIM, with no scrubbing of
// their own -- callers must never put argv, environment variables or
// other secret material in them, only already-scrubbed bounded labels.
// It also never calls codemap: Culprit.FQN is always left empty here,
// filled in later by a codemap-aware reader (internal/explain, E2.5).
func RecordException(ctx context.Context, storePath string, wait time.Duration, ex stacktrace.Exception, id project.Identity, run contextids.IDs, opts RecordExceptionOptions) (UpsertResult, error) {
	input := recordExceptionInput(ex, id, run, opts)

	var result UpsertResult
	err := WithWriter(ctx, storePath, wait, func(store *Store) error {
		var writeErr error
		result, writeErr = store.UpsertOccurrenceResult(input)
		return writeErr
	})
	return result, err
}

// RecordExceptionOnStore is RecordException's single-writer sibling
// (LUX-10): it derives the exact same OccurrenceInput and upserts it into
// an ALREADY-OPEN store, so a replay recording many blocks opens one
// writer for its whole batch instead of acquiring and releasing the
// cross-process lock (and re-reading the whole store) per block. The
// caller owns the WithWriter scope, and therefore the checkpoint commits
// that must follow each committed batch.
func RecordExceptionOnStore(store *Store, ex stacktrace.Exception, id project.Identity, run contextids.IDs, opts RecordExceptionOptions) (UpsertResult, error) {
	return store.UpsertOccurrenceResult(recordExceptionInput(ex, id, run, opts))
}

// recordExceptionInput applies the git root to its own deep copy of ex
// (see cloneExceptionForGitRoot) and derives everything a durable
// occurrence needs: title/message/symbols, the V2 fingerprint, the
// bounded ExceptionInfo and the chain culprit.
func recordExceptionInput(ex stacktrace.Exception, id project.Identity, run contextids.IDs, opts RecordExceptionOptions) OccurrenceInput {
	ex = cloneExceptionForGitRoot(ex)
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

	// title/message/symbols: the common case is ex.Type/ex.Value carrying
	// the display text and Symbols staying nil (validateOccurrenceInput is
	// satisfied by Message/ExceptionType alone). Some parsed shapes carry
	// frames with NEITHER a Type nor a Value, though -- a Ruby rescue-printed
	// backtrace (`warn e.backtrace`; see ruby.go's rubyBacktraceGrammar) is
	// the golden-table example -- and without a fallback here
	// validateOccurrenceInput rejects the write outright even though the
	// event has a perfectly good fingerprint and culprit. Derive Symbols
	// from the in-app frames and a Title/Message from the same crash-facing
	// frame culpritFor would blame in that case.
	title := exceptionTitle(ex)
	message := ex.Value
	var symbols []string
	if ex.Type == "" && ex.Value == "" {
		symbols = exceptionSymbols(ex)
		if label := crashFrameLabel(ex); label != "" {
			title, message = label, label
		}
	}

	return OccurrenceInput{
		ObservedAt:         observedAt,
		Project:            id.Slug,
		Service:            service,
		Kind:               kind,
		Title:              title,
		Message:            message,
		ExceptionType:      ex.Type,
		Symbols:            symbols,
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
		DedupeAliases:      opts.DedupeAliases,
	}
}

// cloneExceptionForGitRoot deep-copies ex's Frames and Chained (and every
// chained entry's own Frames/Chained, recursively) before RecordException
// applies stacktrace.ApplyGitRoot. Go passes ex "by value" only at the top
// level: Frames and Chained are slice headers that still alias the caller's
// backing arrays, and ApplyGitRoot mutates each Frame's InApp/Filename/
// AbsPath in place through that alias -- without this, a caller that keeps
// its own reference to the Exception it handed RecordException (or shares
// one across goroutines) would see it silently mutated, and an empty
// id.GitRoot would even reset every InApp to false on the caller's copy.
func cloneExceptionForGitRoot(ex stacktrace.Exception) stacktrace.Exception {
	ex.Frames = append([]stacktrace.Frame(nil), ex.Frames...)
	if ex.Chained != nil {
		chained := make([]stacktrace.Exception, len(ex.Chained))
		for i, c := range ex.Chained {
			chained[i] = cloneExceptionForGitRoot(c)
		}
		ex.Chained = chained
	}
	return ex
}

// SummarizeForList returns a shallow copy of issue with LatestException
// trimmed to a lightweight summary (Type, Value, Runtime, Handled -- no
// Frames, no Causes). Every list surface (`monitor issues list --json`,
// MCP's monitor_issues) applies this before returning its results: an
// agent or script listing dozens of issues at once should not pay
// LatestException's ~maxExceptionInfoBytes budget per row (the roadmap's
// AC-6 payload-diet goal). A single-issue surface (`monitor issues show`,
// MCP's monitor_issue -- both backed by Store.Get, never this function)
// keeps the untrimmed issues.Issue, where the full frame/cause detail is
// exactly what a reader asked for. Issue.Culprit (already compact, a single
// file:line) is left untouched, so a trimmed list row still names a culprit.
//
// The optional store (CC-3) rebuilds an exception-kind issue whose
// LatestException an older monitor binary stripped before trimming (see
// RebuildFromFirstOccurrence): a list row that can cheaply name the real
// culprit should not show `culprit: null` just because a v1.15 binary
// rewrote the store. Callers that already hold the store they listed from
// pass it; callers without one keep the historical store-less behavior
// byte for byte.
func SummarizeForList(issue Issue, store ...*Store) Issue {
	if len(store) > 0 {
		if rebuilt, ok := RebuildFromFirstOccurrence(store[0], issue); ok {
			issue = rebuilt
		}
	}
	if issue.LatestException == nil {
		return issue
	}
	summary := *issue.LatestException
	summary.Frames = nil
	summary.Causes = nil
	issue.LatestException = &summary
	return issue
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
