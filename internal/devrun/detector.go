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
// pipeline over one merged stream of lines from the scanned stream(s). It
// never writes to logs.veclite, and every store write goes through
// issues.WithWriter's own bounded wait (issues.DefaultWriterWait) -- which
// runs on a goroutine independent of the line-consuming loop below (see
// observe/flush), so a slow or contended store write never delays reading
// the next line off the channel, let alone the copy goroutines feeding it.
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
	occurrences int64

	// flushWG tracks every coalesceWindow timer's flush goroutine
	// (started in observe) so run's final flushAllPending can wait out one
	// that is already executing concurrently with shutdown -- see
	// flushAllPending's doc comment for the exact race this closes.
	flushWG sync.WaitGroup
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
// have finished draining the child's pipe(s)), then closes out whatever the
// Joiner still has open and flushes every still-pending coalescing window
// before returning, so a crash right at the end of a run is never silently
// dropped by an un-flushed window.
func (d *detector) run(ctx context.Context, lines <-chan string) {
	j := stacktrace.NewJoiner()
	ticker := time.NewTicker(detectorTick)
	defer ticker.Stop()
	for {
		select {
		case line, ok := <-lines:
			if !ok {
				d.process(ctx, j.Flush())
				d.flushAllPending(ctx)
				return
			}
			d.process(ctx, j.Feed(line, time.Now()))
		case now := <-ticker.C:
			d.process(ctx, j.Tick(now))
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
// stream ended is recorded immediately instead of waiting out its timer.
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
	for fp, entry := range pending {
		if entry.timer.Stop() {
			// Stop returned true: we successfully cancelled the timer
			// before its callback ran, so the deferred flushWG.Done()
			// inside that callback (see observe) will never execute.
			// Balance this entry's earlier flushWG.Add(1) ourselves --
			// otherwise Wait() below hangs forever on every ordinary
			// (non-racing) shutdown, not just the rare race it exists
			// for. Stop returning false means the callback already
			// started (or this timer already fired/was already stopped);
			// in that case IT owns the Done() call, and calling Done()
			// here too would double-decrement the WaitGroup.
			d.flushWG.Done()
		}
		d.record(ctx, fp, entry)
	}
	d.flushWG.Wait()
}

// record performs the actual issues.RecordException write for one
// coalesced window and, unless the write deduped against an existing
// occurrence, reports it to the banner callbacks (NEW the first time this
// session sees fingerprint, "again (xN)" every time after).
func (d *detector) record(ctx context.Context, fingerprint string, entry *coalesceEntry) {
	result, err := issues.RecordException(ctx, d.opts.storePath, issues.DefaultWriterWait, entry.ex, d.opts.id, d.opts.run, issues.RecordExceptionOptions{
		ObservedAt: entry.observedAt,
		Count:      entry.count,
		DedupeKey:  liveDedupeKey(d.opts.launch.Root, entry.block, entry.observedAt),
	})
	if err != nil {
		// A store write failure must never bring down the detector, let
		// alone the monitored child (there is nowhere else to report it --
		// the detector never writes to logs.veclite by design); it is
		// simply absent from this run's banners/summary.
		return
	}
	if result.Deduped {
		return
	}

	d.mu.Lock()
	shortID, seenBefore := d.seen[fingerprint]
	if !seenBefore {
		shortID = shortIssueID(result.Issue.ID)
		d.seen[fingerprint] = shortID
		d.newIssueIDs = append(d.newIssueIDs, shortID)
	}
	d.occurrences += entry.count
	d.mu.Unlock()

	switch {
	case !seenBefore && d.opts.onNewIssue != nil:
		culprit := result.Issue.Culprit
		d.opts.onNewIssue(newIssueEvent{
			ShortID: shortID,
			Level:   result.Occurrence.Severity,
			Title:   result.Issue.Title,
			File:    culpritFile(culprit),
			Line:    culpritLine(culprit),
			Func:    culpritFunc(culprit),
		})
	case seenBefore && d.opts.onRepeat != nil:
		d.opts.onRepeat(shortID, entry.count)
	}
}

// liveDedupeKey implements the live DedupeKey rule (docs/contracts/
// local-sentry-naming.md §5): sha256(MONITOR_LAUNCH_ROOT + hash(exception
// block)), matched within "a ±3s window" -- approximated here by rounding
// observedAt down to a 3-second bucket, so two detectors independently
// parsing the identical raw text at nearly the same wall-clock instant (a
// `monitor run --` nested inside another one) land on the same key and the
// store's dedupe lookup (an exact string match against the issue's
// retained occurrences) folds the second write into the first's Deduped
// case instead of double-counting.
func liveDedupeKey(launchRoot string, block stacktrace.Block, observedAt time.Time) string {
	const bucketSeconds = 3
	bucket := observedAt.Unix() / bucketSeconds
	seed := strings.Join([]string{
		launchRoot,
		stacktrace.HashBlock(block.Text()),
		strconv.FormatInt(bucket, 10),
	}, "\x00")
	sum := sha256.Sum256([]byte(seed))
	return hex.EncodeToString(sum[:])
}

// shortIssueID derives a display-only short id from an Issue.ID
// ("ISS-<16 hex chars>", per internal/issues' newIssue): its last 4 hex
// characters, matching the roadmap's UX mockups ("5C1D", "A07E", "7B21").
// This is a devrun-local, cosmetic derivation -- internal/issues has no
// ShortID/ResolveID concept of its own yet (that lands with the issue-list
// CLI/MCP work) -- so it must never be persisted or treated as a stable
// identifier beyond this run's own banners.
func shortIssueID(id string) string {
	id = strings.TrimPrefix(id, "ISS-")
	if len(id) <= 4 {
		return id
	}
	return id[len(id)-4:]
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
