package devrun

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/contextids"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// coalesceWindow batches repeats of the identical fingerprint into a single
// store write with an aggregated Count (docs/contracts/
// local-sentry-naming.md, "coalescing de 2 s de fingerprints idénticos"),
// so a tight error loop in the monitored process costs at most one store
// write every coalesceWindow instead of one per raw event.
const coalesceWindow = 2 * time.Second

// detectorTick drives the Joiner's idle-based block closing for the live
// line stream, matching `stacktrace parse`'s own live tick interval.
const detectorTick = 100 * time.Millisecond

// dedupeBucketSeconds is the live DedupeKey's time-bucket width (see
// liveDedupeKey). It MUST be strictly smaller than coalesceWindow: a
// detector can only ever open a SECOND coalescing window for the same
// fingerprint after the first one's timer has fired (coalesceWindow later,
// at minimum -- time.AfterFunc never fires early), so as long as
// dedupeBucketSeconds < coalesceWindow, two sequential windows from the
// SAME detector are providably bucketed apart and can never collide with
// each other. That was the bug a flat 3s bucket had (wider than the 2s
// coalesce window itself): two back-to-back windows of the same handled
// error could land in the same bucket and the second's whole Count was
// silently folded away as a dedupe. See
// TestLiveDedupeKeyNeverFoldsSequentialWindowsFromTheSameDetector. A nested
// `monitor run --` reading the same underlying text observes it within
// milliseconds of the outer launch in practice, well inside a 1s bucket, so
// the roadmap's stated "±3s window" is a ceiling this stays well under, not
// a target to hit exactly.
const dedupeBucketSeconds = 1

// flushShutdownBudget bounds the TOTAL wall time flushAllPending may spend
// racing the issues store's writer lock at shutdown. Every window still
// pending when the stream ends is flushed CONCURRENTLY, all racing this ONE
// shared deadline via a derived context -- never sequentially, each eating
// its own issues.DefaultWriterWait (5s), which could otherwise pin devrun's
// exit (and the already-reaped child) behind N*5s when the store is
// contended.
const flushShutdownBudget = 2 * time.Second

// newIssueEvent is what the detector reports to Options' banner callback
// the first time a fingerprint is recorded during this run.
type newIssueEvent struct {
	ShortID string
	Level   string
	Title   string
	File    string
	Line    int
	Func    string
}

type detectorOptions struct {
	storePath      string
	launch         LaunchIDs
	id             project.Identity
	run            contextids.IDs
	redactEnvNames []string
	onNewIssue     func(newIssueEvent)
	onRepeat       func(shortID string, count int64)
}

// detector runs the Joiner -> Parse -> scrub -> coalesce -> RecordException
// pipeline over the scanned stream(s), one stacktrace.Joiner per stream
// (docs/contracts/local-sentry-naming.md's "Joiner por stream" rule -- see
// streamLine.stream) so a line interleaved from one stream can never split
// a block being accumulated on another. It never writes to logs.veclite,
// and every store write goes through issues.WithWriter's own bounded wait
// (issues.DefaultWriterWait) -- which runs on a goroutine independent of
// the line-consuming loop below (see observe/flush), so a slow or
// contended store write never delays reading the next line off the
// channel, let alone the copy goroutines feeding it.
type detector struct {
	opts     detectorOptions
	scrubber *scrub.Scrubber

	mu      sync.Mutex
	pending map[string]*coalesceEntry
	// seen maps a fingerprint to its display short ID once this SESSION
	// has recorded it at least once, deciding NEW vs "again (xN)" banners
	// for the lifetime of this one `monitor run --` invocation (not
	// whether the issue already existed in the store from an earlier run).
	seen map[string]string

	newIssueIDs []string
	// firstNewIssueFullID is the first NEW issue's full "ISS-..." id
	// recorded this run, set once (see record). Kept for a caller that
	// needs the unambiguous full id (e.g. a future --json summary); the
	// exit summary's own "next:" hint (banner.go's ExitSummary, FIX 2) no
	// longer needs it, now that `monitor issue <id|short-prefix|latest>`
	// (E2.5) resolves the short, lowercase display id in newIssueIDs
	// directly -- see shortIssueID's own doc comment.
	firstNewIssueFullID string
	occurrences         int64
	// failedWrites counts every issues.RecordException call that returned
	// an error (a contended or unreachable store, most commonly at
	// shutdown once flushAllPending's budget is exhausted): the crash text
	// itself is never fabricated or retried past that point, but it must
	// never be silently swallowed either -- see ExitSummaryInfo.FailedWrites.
	failedWrites int64
	// failedWritesCorrupted is the subset of failedWrites whose error
	// matched issues.IsCorruptedError (a damaged store, e.g. a checksum
	// mismatch) rather than ordinary lock contention (CC-6's secondary
	// fix: "store busy" is misleading -- and tells a user to just retry --
	// for a failure a retry can never fix). NOT currently surfaced in
	// ExitSummary: Result and ExitSummaryInfo are defined in devrun.go,
	// outside this package's file ownership for this change; wiring a
	// FailedWritesCorrupted field through Run (around its `result :=
	// Result{...}` and ExitSummaryInfo{...} construction) so ExitSummary
	// can print "store corrupted" instead of "store busy" for these is a
	// one-line follow-up left to whoever owns devrun.go.
	failedWritesCorrupted int64

	// flushWG tracks every coalesceWindow timer's flush goroutine
	// (started in observe) so run's final flushAllPending can wait out one
	// that is already executing concurrently with shutdown -- see
	// flushAllPending's doc comment for the exact race this closes.
	flushWG sync.WaitGroup

	// bannerMu serializes onNewIssue/onRepeat callback invocations.
	// Independent fingerprints' coalesceWindow timers (and, since
	// flushAllPending, the shutdown flush of every still-pending
	// fingerprint) can fire concurrently; Options.Banner is typically
	// os.Stderr or, in tests, a plain *bytes.Buffer -- neither promises
	// concurrent-write safety, so every banner-producing callback goes
	// through this mutex rather than racing directly on the writer.
	bannerMu sync.Mutex
}

// coalesceEntry is one fingerprint's open coalescing window: the FIRST raw
// occurrence's Exception/Block/timestamp (kept as the representative detail
// for the eventual single write) plus a running count of how many raw
// events landed in the window.
type coalesceEntry struct {
	ex         stacktrace.Exception
	block      stacktrace.Block
	count      int64
	observedAt time.Time
	timer      *time.Timer
}

func newDetector(opts detectorOptions) *detector {
	return &detector{
		opts:     opts,
		scrubber: scrub.New(scrub.WithValues(scrub.SecretEnvValues(os.Environ(), opts.redactEnvNames))),
		pending:  make(map[string]*coalesceEntry),
		seen:     make(map[string]string),
	}
}

// run consumes lines until the channel is closed (the copy goroutine(s)
// have finished draining the child's pipe(s)), then closes out whatever
// each stream's Joiner still has open and flushes every still-pending
// coalescing window before returning, so a crash right at the end of a run
// is never silently dropped by an un-flushed window.
func (d *detector) run(ctx context.Context, lines <-chan streamLine) {
	joiners := map[streamKind]*stacktrace.Joiner{
		streamStderr: stacktrace.NewJoiner(),
		streamStdout: stacktrace.NewJoiner(),
	}
	ticker := time.NewTicker(detectorTick)
	defer ticker.Stop()
	for {
		select {
		case sl, ok := <-lines:
			if !ok {
				for _, j := range joiners {
					d.process(ctx, j.Flush())
				}
				d.flushAllPending(ctx)
				return
			}
			j := joiners[sl.stream]
			if sl.gap {
				// One or more lines were dropped for this stream just
				// before this one: whatever block this stream's Joiner
				// had open is now missing lines. Flush it and discard the
				// result (never process/record it) instead of letting the
				// next Feed silently complete a truncated fragment as a
				// wrongly-typed, wrong-culprit "exception" -- a drop must
				// lose an event, never fabricate one.
				j.Flush()
			}
			d.process(ctx, j.Feed(sl.line, time.Now()))
		case now := <-ticker.C:
			for _, j := range joiners {
				d.process(ctx, j.Tick(now))
			}
		}
	}
}

func (d *detector) process(ctx context.Context, blocks []stacktrace.Block) {
	for _, b := range blocks {
		ex := stacktrace.Parse(b)
		if ex == nil {
			continue
		}
		d.observe(ctx, b, *ex)
	}
}

// observe opens or extends fingerprint's coalescing window. The FIRST raw
// occurrence in a window schedules its flush coalesceWindow later and is
// otherwise untouched by later repeats within the same window: only the
// count advances, so the eventual write's Exception/Culprit detail is
// stable and reproducible across repeats, and so a nested `monitor run --`
// parsing the same underlying text is more likely to land on the same
// representative block for the live DedupeKey (see liveDedupeKey).
func (d *detector) observe(ctx context.Context, block stacktrace.Block, ex stacktrace.Exception) {
	stacktrace.ApplyGitRoot(&ex, d.opts.id.GitRoot)
	scrubException(d.scrubber, &ex)

	fingerprint := issues.FingerprintV2Exception(ex, d.opts.id.Slug)

	d.mu.Lock()
	if entry, exists := d.pending[fingerprint]; exists {
		entry.count++
		d.mu.Unlock()
		return
	}
	entry := &coalesceEntry{ex: ex, block: block, count: 1, observedAt: time.Now()}
	d.pending[fingerprint] = entry
	d.flushWG.Add(1)
	entry.timer = time.AfterFunc(coalesceWindow, func() {
		defer d.flushWG.Done()
		d.flush(ctx, fingerprint)
	})
	d.mu.Unlock()
}

// flush is the coalesceWindow timer's callback: it removes fingerprint's
// entry (a no-op if flushAllPending already claimed it -- see its doc
// comment for why that race is safe) and records it.
func (d *detector) flush(ctx context.Context, fingerprint string) {
	d.mu.Lock()
	entry, ok := d.pending[fingerprint]
	if ok {
		delete(d.pending, fingerprint)
	}
	d.mu.Unlock()
	if !ok {
		return
	}
	d.record(ctx, fingerprint, entry)
}

// flushAllPending is run's shutdown path: every window still open when the
// stream ended is recorded immediately instead of waiting out its timer --
// all of them CONCURRENTLY, racing one shared flushShutdownBudget deadline
// (via a context derived from ctx) rather than sequentially each eating its
// own issues.DefaultWriterWait. A store write that does not finish before
// the deadline fails with a context error, same as any other write failure
// (see record): it is counted in failedWrites and reported in the exit
// summary rather than silently pinning devrun's shutdown behind a store a
// concurrent writer is holding open.
//
// Race with an in-flight timer callback: entry.timer.Stop() only prevents a
// timer that has not fired YET; one that already fired and is between
// acquiring d.mu (in flush, above) races this function for the same
// fingerprint. Both sides delete-then-record under d.mu, so whichever wins
// the lock records it and the other finds it already gone and does
// nothing -- never a double-write, never a lost one. The residual case is
// flush's goroutine winning that race and still being mid-write (inside
// issues.RecordException's own bounded wait) at the moment this function
// would otherwise return: flushWG.Wait() below blocks until every such
// goroutine's write has actually completed, so devrun.Run (and therefore
// the process) never exits out from under a write still in flight.
func (d *detector) flushAllPending(ctx context.Context) {
	d.mu.Lock()
	pending := d.pending
	d.pending = make(map[string]*coalesceEntry)
	d.mu.Unlock()

	if len(pending) > 0 {
		shutdownCtx, cancel := context.WithTimeout(ctx, flushShutdownBudget)
		var wg sync.WaitGroup
		for fp, entry := range pending {
			if entry.timer.Stop() {
				// Stop returned true: we successfully cancelled the timer
				// before its callback ran, so the deferred flushWG.Done()
				// inside that callback (see observe) will never execute.
				// Balance this entry's earlier flushWG.Add(1) ourselves --
				// otherwise Wait() below hangs forever on every ordinary
				// (non-racing) shutdown, not just the rare race it exists
				// for. Stop returning false means the callback already
				// started (or this timer already fired/was already
				// stopped); in that case IT owns the Done() call, and
				// calling Done() here too would double-decrement the
				// WaitGroup.
				d.flushWG.Done()
			}
			wg.Add(1)
			go func(fp string, entry *coalesceEntry) {
				defer wg.Done()
				d.record(shutdownCtx, fp, entry)
			}(fp, entry)
		}
		wg.Wait()
		cancel()
	}
	d.flushWG.Wait()
}

// record performs the actual issues.RecordException write for one
// coalesced window and, unless the write deduped against an existing
// occurrence, reports it to the banner callbacks. NEW is derived from the
// store's own answer, not merely "this session has not seen the
// fingerprint before": result.Issue.OccurrenceCount equals entry.count
// (this write's own count) only when this write created the issue's VERY
// FIRST occurrence ever (upsertOccurrenceLocked seeds a fresh issue's
// OccurrenceCount from input.Count; every later write only ADDS to a
// strictly larger running total) -- so a pre-existing issue from an
// earlier `monitor run --` invocation against the same store is correctly
// announced as a repeat, never as NEW, even though this session is seeing
// its fingerprint for the first time.
func (d *detector) record(ctx context.Context, fingerprint string, entry *coalesceEntry) {
	result, err := issues.RecordException(ctx, d.opts.storePath, issues.DefaultWriterWait, entry.ex, d.opts.id, d.opts.run, issues.RecordExceptionOptions{
		ObservedAt: entry.observedAt,
		Count:      entry.count,
		DedupeKey:  liveDedupeKey(d.opts.launch.Root, entry.block, entry.observedAt),
	})
	if err != nil {
		// A store write failure must never bring down the detector, let
		// alone the monitored child (there is nowhere else to report it
		// synchronously -- the detector never writes to logs.veclite by
		// design); it is counted here so the exit summary can say so
		// honestly instead of silently losing the crash. issues.
		// IsCorruptedError separately flags a damaged store (see
		// failedWritesCorrupted's doc comment) so a future exit-summary
		// wiring can tell a user "store corrupted" apart from "store
		// busy, try again".
		d.mu.Lock()
		d.failedWrites++
		if issues.IsCorruptedError(err) {
			d.failedWritesCorrupted++
		}
		d.mu.Unlock()
		return
	}
	if result.Deduped {
		return
	}

	isNewIssue := result.Issue.OccurrenceCount == entry.count

	d.mu.Lock()
	shortID, seenBefore := d.seen[fingerprint]
	if !seenBefore {
		shortID = shortIssueID(result.Issue.ID)
		d.seen[fingerprint] = shortID
	}
	if isNewIssue {
		d.newIssueIDs = append(d.newIssueIDs, shortID)
		if d.firstNewIssueFullID == "" {
			d.firstNewIssueFullID = result.Issue.ID
		}
	}
	d.occurrences += entry.count
	d.mu.Unlock()

	switch {
	case isNewIssue && d.opts.onNewIssue != nil:
		culprit := result.Issue.Culprit
		d.bannerMu.Lock()
		d.opts.onNewIssue(newIssueEvent{
			ShortID: shortID,
			Level:   result.Occurrence.Severity,
			Title:   result.Issue.Title,
			File:    culpritFile(culprit),
			Line:    culpritLine(culprit),
			Func:    culpritFunc(culprit),
		})
		d.bannerMu.Unlock()
	case !isNewIssue && d.opts.onRepeat != nil:
		d.bannerMu.Lock()
		d.opts.onRepeat(shortID, entry.count)
		d.bannerMu.Unlock()
	}
}

// liveDedupeKey implements the live DedupeKey rule (docs/contracts/
// local-sentry-naming.md §5): sha256(MONITOR_LAUNCH_ROOT + hash(exception
// block)), bucketed by dedupeBucketSeconds so two detectors independently
// parsing the identical raw text at nearly the same wall-clock instant (a
// `monitor run --` nested inside another one) land on the same key and the
// store's dedupe lookup (an exact string match against the issue's
// retained occurrences) folds the second write into the first's Deduped
// case instead of double-counting -- while two SEQUENTIAL windows from the
// SAME detector (see dedupeBucketSeconds' doc comment for the proof) never
// collide with each other.
func liveDedupeKey(launchRoot string, block stacktrace.Block, observedAt time.Time) string {
	bucket := observedAt.Unix() / dedupeBucketSeconds
	seed := strings.Join([]string{
		launchRoot,
		stacktrace.HashBlock(block.Text()),
		strconv.FormatInt(bucket, 10),
	}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// shortIssueID derives a display-only short id from an Issue.ID
// ("ISS-<16 hex chars>", per internal/issues' newIssue): the FIRST 4 hex
// characters after "ISS-" (already lowercase, since issues.newIssue's hex
// encoding is), matching docs/contracts/issue-context-v1.md's short_id
// field -- which is also exactly what `monitor issue <id|short-prefix|
// latest>` (E2.5, internal/cli/issues.go's newIssueCmd) accepts, so a
// banner printed today, and the exit summary's "next:" hint (banner.go's
// ExitSummary, FIX 2), already resolve with no further epic needed. This
// is a devrun-local, cosmetic derivation -- internal/issues has no
// ShortID/ResolveID concept of its own -- so it must never be persisted or
// treated as a stable identifier beyond this run's own banners.
func shortIssueID(id string) string {
	id = strings.TrimPrefix(id, "ISS-")
	if len(id) <= 4 {
		return id
	}
	return id[:4]
}

func culpritFile(c *issues.Culprit) string {
	if c == nil {
		return ""
	}
	return c.File
}

func culpritLine(c *issues.Culprit) int {
	if c == nil {
		return 0
	}
	return c.Line
}

func culpritFunc(c *issues.Culprit) string {
	if c == nil {
		return ""
	}
	return c.Function
}

// scrubException redacts ex's Type/Value and every frame's Function text,
// recursively through Chained, using scrubber -- the golden rule that error
// text is untrusted data and must be redacted before it is persisted or
// printed (docs/contracts/local-sentry-naming.md's "Scrub por defecto").
// Filename/AbsPath are left alone: they are resolved, checked paths (see
// stacktrace.ApplyGitRoot), not attacker- or user-controlled message text.
func scrubException(scrubber *scrub.Scrubber, ex *stacktrace.Exception) {
	if ex == nil {
		return
	}
	ex.Type = scrubber.String(ex.Type)
	ex.Value = scrubber.String(ex.Value)
	for i := range ex.Frames {
		ex.Frames[i].Function = scrubber.String(ex.Frames[i].Function)
	}
	for i := range ex.Chained {
		scrubException(scrubber, &ex.Chained[i])
	}
}
