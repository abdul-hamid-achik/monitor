package explain

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
	"github.com/abdul-hamid-achik/monitor/internal/stacktrace"
)

// enrichmentDeadline bounds the WHOLE codemap/git/vecgrep enrichment
// pipeline (culprit range + impact + last_touched + message search), not
// just each individual subprocess's own timeout: ProbeCodemap/ProbeVecgrep
// each carry their own ~3s timeout, but Build calls several of them in
// sequence, so a hung (not merely absent) codemap or vecgrep could otherwise
// stack those into several seconds even though every individual call is
// "bounded". context.WithTimeout keeps whichever deadline is sooner, so this
// caps the worst case without changing behavior when everything answers
// quickly (the common, healthy-tools case).
const enrichmentDeadline = 4 * time.Second

// ErrLatestNotSupported is returned by Build when id is the sentinel
// "latest": Build always reads one CONCRETE issue. Resolving "latest"
// (optionally narrowed by project/service/kind) is ResolveLatest's job --
// callers (the CLI's `monitor issue latest`, MCP's monitor_issue) run that
// first and pass its result's ID here, along with Options.ResolvedFrom so
// the contract's resolved_from block is echoed onto the Context.
var ErrLatestNotSupported = errors.New("explain: Build takes a concrete issue id; call ResolveLatest first for \"latest\"")

// Options configures Build.
type Options struct {
	// Budget controls section detail (see Budget's doc comment). ""
	// defaults to BudgetStandard.
	Budget Budget
	// Redact, when true, runs a defense-in-depth scrub.Scrubber pass over
	// every free-text field this read assembles (title, snippet lines,
	// last_touched.subject) before returning -- on top of, never instead
	// of, the scrubbing E2.2 already applied before this text was
	// persisted. This catches text that could never have been scrubbed at
	// ingest: the culprit snippet is read LIVE from disk, not from anything
	// monitor ever wrote to the store. Defaults to false (normalizeOptions
	// does NOT set it) because the human-terminal read needs no second
	// pass: that output never leaves the user's own machine, and ingest
	// already scrubbed the persisted exception. Every surface where the
	// text DOES leave that machine -- an MCP response, an --md page an
	// agent will paste elsewhere (AGENTS.md's golden rule: scrub error
	// text before returning it through MCP or --md) -- must opt in, and
	// every production caller (the CLI's `monitor issue`, MCP's
	// monitor_issue) passes Redact: true explicitly; a caller that
	// forgets to must not silently get scrubbing it didn't ask for, and
	// nothing here infers "true" from context the way Budget/Now do.
	Redact bool
	// Now overrides GeneratedAt / "current time" for deterministic tests.
	// Zero uses time.Now().UTC().
	Now time.Time
	// Root overrides the git root Build resolves culprit ranges, snippets,
	// impact, and last_touched against. Empty resolves it from the STORED
	// issue's own recorded data first (the in-app frame that produced its
	// culprit carries the absolute path it was recorded under -- see
	// resolveRoot), since a reader (notably MCP: mcphub launches `monitor
	// mcp serve` from the gateway's own cwd, not the project's checkout --
	// E2.7's whole reason for existing) commonly runs from a directory that
	// has nothing to do with the issue it is asking about. Only when the
	// issue carries no usable recorded path does this fall back to the
	// same implicit-codebase-root resolution the rest of monitor uses: via
	// project.Resolve(Hints{UseWorkingDir: true}) -- i.e. "run `monitor
	// issue <id>` from inside the project's checkout".
	Root string
	// ResolvedFrom, set by a caller that already ran ResolveLatest, is
	// echoed onto Context.ResolvedFrom.
	ResolvedFrom *ResolvedFrom
}

func normalizeOptions(opts Options) Options {
	if opts.Budget == "" {
		opts.Budget = BudgetStandard
	}
	if opts.Now.IsZero() {
		opts.Now = time.Now().UTC()
	} else {
		opts.Now = opts.Now.UTC()
	}
	return opts
}

// resolveRoot picks the git/codebase root Build resolves culprit ranges,
// snippets, impact, and last_touched against.
//
// An explicit Options.Root always wins outright (the CLI's --root flag). Its
// absence used to fall back straight to the CALLER'S working directory
// (project.Resolve(Hints{UseWorkingDir: true})) -- correct for the CLI
// (`monitor issue <id>` genuinely runs from inside the project's checkout),
// wrong for MCP: mcphub launches `monitor mcp serve` from the gateway's own
// cwd, which is exactly the "another repo's code, or none at all" case E2.7
// exists to answer. So the SECOND choice is the issue's own recorded data:
// its LatestException carries stacktrace.Frame.AbsPath (the absolute path
// the runtime printed) alongside Filename (that same path REWRITTEN
// root-relative by stacktrace.ApplyGitRoot at record time -- see inapp.go).
// Trimming Filename's suffix off AbsPath reconstructs the exact root the
// issue was recorded under, independent of the reader's own cwd. Only when
// the issue carries no usable AbsPath at all does this fall through to the
// old cwd-based guess.
//
// Whichever root was used, a mismatch against the issue's own recorded
// Project (when Resolve's Slug for that root disagrees) is reported into
// degraded rather than silently trusted: a wrong root produces a snippet,
// impact, and blame that all LOOK confident (status "ok", stale:false) while
// actually describing an unrelated checkout that happens to share a
// relative path.
func resolveRoot(opts Options, issue issues.Issue, degraded map[string]Degraded) string {
	if root := strings.TrimSpace(opts.Root); root != "" {
		return root
	}

	root := rootFromRecordedFrame(issue)
	source := "recorded frame"
	if root == "" {
		root = project.Resolve(project.Hints{UseWorkingDir: true}).GitRoot
		source = "working directory"
	}
	if root == "" {
		return ""
	}

	if issue.Project != "" {
		slug := project.Resolve(project.Hints{Dir: root, PID: 1}).Slug
		if slug != "" && !strings.EqualFold(slug, issue.Project) {
			degraded["root"] = Degraded{
				Component: "root",
				State:     "mismatch",
				Detail: fmt.Sprintf("root resolved from the %s (%s, project %q) does not match this issue's recorded project %q -- "+
					"culprit/impact/last_touched below may describe the wrong checkout", source, root, slug, issue.Project),
				Recovery: "pass --root <path to the project's checkout> (CLI) or Options.Root (callers)",
			}
		}
	}
	return root
}

// rootFromRecordedFrame reconstructs the git root an issue was recorded
// under from its own stored data: any in_app frame (they all share one root
// -- stacktrace.ApplyGitRoot applies a single gitRoot to the whole exception
// tree) whose AbsPath still ends in its own root-relative Filename. Prefers
// the frame matching the issue's own Culprit (the frame a reader most cares
// about getting right), falling back to any other usable in_app frame, and
// finally "" when nothing in the stored exception carries a usable AbsPath
// (a message-only event, or one recorded before AbsPath existed).
func rootFromRecordedFrame(issue issues.Issue) string {
	if issue.LatestException == nil {
		return ""
	}
	var fallback string
	for _, f := range issue.LatestException.Frames {
		root := rootFromFrame(f)
		if root == "" {
			continue
		}
		if issue.Culprit != nil && f.Filename == issue.Culprit.File && f.Lineno == issue.Culprit.Line {
			return root
		}
		if fallback == "" {
			fallback = root
		}
	}
	return fallback
}

// rootFromFrame recovers the absolute root a single frame was recorded
// under, or "" when the frame carries no usable AbsPath/Filename pair (a
// runtime frame outside any git root, or one stacktrace.ApplyGitRoot never
// saw -- both leave Filename un-rewritten, so the suffix check below fails
// harmlessly instead of producing a bogus path).
func rootFromFrame(f stacktrace.Frame) string {
	if f.AbsPath == "" || f.Filename == "" {
		return ""
	}
	abs := filepath.ToSlash(f.AbsPath)
	rel := filepath.ToSlash(f.Filename)
	if !strings.HasSuffix(abs, rel) {
		return ""
	}
	root := strings.TrimSuffix(abs, rel)
	root = strings.TrimSuffix(root, "/")
	if root == "" {
		return ""
	}
	return filepath.FromSlash(root)
}

// Build reads id from store (read-only; store.OpenReadOnly is the caller's
// job, so a live `watch --stash` writer never blocks or is blocked by this)
// and assembles monitor.issue_context.v1. id must be a concrete issue ID, a
// short_id, or an unambiguous prefix (see issues.Store.ResolveID) -- never
// the "latest" sentinel (see ErrLatestNotSupported).
func Build(ctx context.Context, store *issues.Store, id string, opts Options) (*Context, error) {
	if store == nil {
		return nil, errors.New("explain.Build: store is required")
	}
	trimmed := strings.TrimSpace(id)
	if strings.EqualFold(trimmed, "latest") {
		return nil, ErrLatestNotSupported
	}
	resolvedID, err := store.ResolveID(trimmed)
	if err != nil {
		return nil, err
	}
	issue, err := store.Get(resolvedID)
	if err != nil {
		return nil, err
	}

	opts = normalizeOptions(opts)

	degraded := map[string]Degraded{} // component -> entry, deduplicated
	// (CC-3) an older monitor binary rewriting this store strips the
	// additive issue fields (Culprit, LatestException, Handled) while the
	// first occurrence keeps its Exception detail -- rebuild from it
	// BEFORE anything below reads those fields (root resolution included).
	if rebuilt, ok := issues.RebuildFromFirstOccurrence(store, issue); ok {
		issue = rebuilt
		degraded["issue"] = Degraded{
			Component: "issue",
			State:     "rebuilt",
			Detail:    "culprit rebuilt from first occurrence; issue record was rewritten by an older monitor",
		}
	}
	root := resolveRoot(opts, issue, degraded)

	c := &Context{
		Schema:       Schema,
		Budget:       string(opts.Budget),
		GeneratedAt:  opts.Now,
		ResolvedFrom: opts.ResolvedFrom,
		Issue:        issueSummary(issue),
		Timeline:     timelineFor(issue),
		Causes:       []CauseEntry{},
		Frames:       []FrameEntry{},
		Degraded:     []Degraded{},
		Next:         []NextAction{},
		RelatedNotes: RelatedNotes{Status: SectionSkipped, Items: []RelatedNoteItem{}},
		Privacy:      Privacy{TextIsUntrusted: true},
	}

	// Every codemap/git/vecgrep call below shares one overall deadline (see
	// enrichmentDeadline) instead of each subprocess's own multi-second
	// timeout stacking on top of the last one when a tool is hung rather
	// than simply absent.
	ectx, cancel := context.WithTimeout(ctx, enrichmentDeadline)
	defer cancel()

	culprit := culpritInfoFor(ectx, root, issue, degraded)
	c.Culprit = culprit

	if issue.LatestException != nil {
		// (CC-4) frames and causes cut at INGEST time -- the 12-frame
		// cap, the 3-cause cap, and the ~2 KB backstop -- were counted
		// into ExceptionInfo.DroppedFrames/DroppedCauses when the record
		// was written; seed Truncated with them here, before applyBudget
		// adds this read's own budget cuts on top, so a reader can tell
		// "12 frames kept" from "12 frames kept of 21".
		c.Truncated.Frames = issue.LatestException.DroppedFrames
		c.Truncated.Causes = issue.LatestException.DroppedCauses
		for _, cause := range issue.LatestException.Causes {
			c.Causes = append(c.Causes, causeEntryFrom(cause))
		}
		// in_app frames only, closest-to-the-crash first (docs/contracts/
		// issue-context-v1.md's `frames` doc comment): issues.ExceptionInfo.
		// Frames is stored oldest-to-newest with the crash frame LAST (the
		// same convention stacktrace.Exception.Frames and the fingerprint's
		// own outerFrames rule use), already filtered to in-app entries at
		// ingest (topInAppFrames), so this only reverses the order. The
		// former !f.InApp counting loop here was dead code: no stored
		// frame is ever non-in-app, and ingest-time drops are pre-counted
		// above.
		for i := len(issue.LatestException.Frames) - 1; i >= 0; i-- {
			f := issue.LatestException.Frames[i]
			c.Frames = append(c.Frames, FrameEntry{Function: f.Function, File: f.Filename, Line: f.Lineno, InApp: f.InApp})
		}
	}

	c.Impact = impactFor(ectx, root, culprit, degraded)
	c.LastTouched = lastTouchedSection(ectx, root, culprit, issue, degraded)
	if culprit != nil && culprit.Snippet != nil {
		culprit.Snippet.Stale = isStale(ectx, root, c.LastTouched, issue, culprit.File)
		// (LUX-5) a stale snippet means the highlighted line may be the
		// wrong line: a stack culprit's location confidence follows it
		// down from "high" to "medium" rather than vouching for a line
		// git just said the file moved under.
		if culprit.Snippet.Stale && culprit.Confidence == "high" {
			culprit.Confidence = "medium"
		}
	}

	for _, d := range degraded {
		c.Degraded = append(c.Degraded, d)
	}
	sortDegraded(c.Degraded)

	c.Next = nextActionsFor(issue, culprit)

	applyBudget(c, opts.Budget)

	if opts.Redact {
		c.Privacy.Scrubbed = redactContext(c)
	}

	return c, nil
}

func shortID(id string) string {
	suffix := strings.TrimPrefix(id, "ISS-")
	if len(suffix) > 4 {
		return suffix[:4]
	}
	return suffix
}

func issueSummary(issue issues.Issue) IssueSummary {
	title := issue.Title
	if title == "" {
		title = issue.Message
	}
	return IssueSummary{
		ID: issue.ID, ShortID: shortID(issue.ID), Status: string(issue.Status), Kind: issue.Kind,
		Title: title, ExceptionType: issue.ExceptionType, Handled: issue.Handled, Level: issue.Level,
		Project: issue.Project, Service: issue.Service,
	}
}

func timelineFor(issue issues.Issue) Timeline {
	return Timeline{
		FirstSeen: issue.FirstSeen, LastSeen: issue.LastSeen,
		Occurrences: issue.OccurrenceCount, Reopened: issue.ReopenedCount,
		FirstGitSHA: issue.FirstGitSHA, Runs: issue.Runs,
		// See Timeline.TimeSource's doc comment: a documented placeholder
		// until a producer-side provenance field exists.
		TimeSource: "unknown",
	}
}

func causeEntryFrom(cause issues.CauseInfo) CauseEntry {
	entry := CauseEntry{Type: cause.Type}
	if cause.Culprit != nil {
		entry.Culprit = &CauseCulprit{Function: cause.Culprit.Function, File: cause.Culprit.File, Line: cause.Culprit.Line}
	}
	return entry
}

// culpritInfoFor resolves Context.Culprit: the stored stack culprit when
// there is one, else E2.8's message-search fallback (exception-kind
// issues only -- LUX-12). It also resolves the
// culprit's range (codemap symbol-at, falling back to a synthetic frame
// window) and reads its snippet -- every dependency along the way reports
// into degraded (keyed by component so two call sites reporting the same
// unhealthy codemap collapse to one entry).
func culpritInfoFor(ctx context.Context, root string, issue issues.Issue, degraded map[string]Degraded) *CulpritInfo {
	var info *CulpritInfo
	if issue.Culprit != nil && issue.Culprit.File != "" {
		info = &CulpritInfo{
			Function: issue.Culprit.Function, File: issue.Culprit.File, Line: issue.Culprit.Line,
			Source: "stack", Confidence: "high",
		}
	} else if issue.Kind == issues.KindException {
		// (LUX-12) the message-search fallback runs only for
		// exception-kind issues: a watch alert ("monitor.alert.<rule>",
		// project "host") has no source line to infer, and blaming
		// whatever repo the reader happens to stand in -- plus a
		// vecgrep/git-grep recovery for a disk alert -- is noise.
		//
		// (LUX-2) search the exception's own rendered value, NOT the
		// Title: Title prefixes the exception type ("TypeError:
		// loadUser: ..."), and that prefixed string never appears in
		// source -- in a clean repo the fallback found nothing, and in a
		// repo whose tests quote the rendered message it blamed the
		// test. issue.Message mirrors ex.Value at ingest, and the stored
		// exception's Value is the next-best spelling; the Title is the
		// last resort, for a record with neither.
		var exceptionValue string
		if issue.LatestException != nil {
			exceptionValue = issue.LatestException.Value
		}
		message := firstNonEmptyString(issue.Message, exceptionValue, issue.Title)
		result, d := culpritForMessage(ctx, root, message)
		if d != nil {
			degraded[d.Component] = *d
		}
		if result != nil {
			info = &CulpritInfo{
				File: result.File, Line: result.Line,
				// mapping: "inferred" is fixed for EVERY message_search
				// culprit (the naming ADR §7: "siempre
				// marcado mapping: inferred") -- it reuses stacktrace.Frame's
				// shared source-map confidence enum as the vocabulary for
				// "this location was not read directly off a real frame".
				Source: "message_search", Via: result.Via, Confidence: "low", Mapping: "inferred",
			}
		}
	}
	if info == nil {
		return nil
	}

	codemapHealth := ecosystem.ProbeCodemap(ctx, root)
	if codemapHealth.State == ecosystem.HealthOK {
		if sym, err := ecosystem.CodemapSymbolAtPath(ctx, info.File, info.Line, ecosystem.CodemapOpts{Path: root}); err == nil && sym.Resolution != "none" && sym.StartLine > 0 {
			info.FQN = sym.FQN
			if info.Function == "" {
				info.Function = sym.Symbol
			}
			info.Range = &CulpritRange{Start: sym.StartLine, End: sym.EndLine, Source: "codemap"}
		}
	} else {
		degraded["codemap"] = Degraded{Component: "codemap", State: codemapHealth.State, Detail: codemapHealth.Detail, Recovery: codemapHealth.Recovery}
	}
	if info.Range == nil {
		start := info.Line - snippetContextLines
		if start < 1 {
			start = 1
		}
		info.Range = &CulpritRange{Start: start, End: info.Line + snippetContextLines, Source: "frame"}
	}

	if snippet, reason := readSnippet(root, info.File, info.Line, snippetContextLines); snippet != nil {
		info.Snippet = snippet
	} else if reason != "" {
		degraded["snippet"] = Degraded{Component: "snippet", State: SectionSkipped, Detail: reason}
	}
	return info
}

func impactFor(ctx context.Context, root string, culprit *CulpritInfo, degraded map[string]Degraded) ImpactInfo {
	if culprit == nil || culprit.File == "" {
		return ImpactInfo{Status: SectionSkipped, Detail: "no culprit resolved"}
	}
	if d, ok := degraded["codemap"]; ok {
		return ImpactInfo{Status: SectionSkipped, Detail: d.Detail, Recovery: d.Recovery}
	}
	impact, err := ecosystem.CodemapImpactAtPath(ctx, culprit.File, culprit.Line, 0, ecosystem.CodemapOpts{Path: root})
	if err != nil {
		return ImpactInfo{Status: SectionSkipped, Detail: fmt.Sprintf("codemap impact: %v", err)}
	}
	if !impact.Found {
		detail := impact.Note
		if detail == "" {
			detail = "codemap could not resolve a symbol at this file:line"
		}
		return ImpactInfo{Status: SectionSkipped, Detail: detail}
	}
	return ImpactInfo{
		Status: SectionOK, Callers: len(impact.DirectCallers), BlastRadius: len(impact.BlastRadius),
		Tests: len(impact.Tests), TestFiles: testFilesFrom(impact.Tests),
		Untested: impact.Untested, CallGraph: impact.CallGraph,
	}
}

func lastTouchedSection(ctx context.Context, root string, culprit *CulpritInfo, issue issues.Issue, degraded map[string]Degraded) LastTouched {
	if culprit == nil || culprit.File == "" {
		return LastTouched{Status: SectionSkipped, Detail: "no culprit resolved"}
	}
	lt := lastTouchedFor(ctx, root, culprit.File, culprit.Line)
	if lt.Status != SectionOK {
		degraded["git"] = Degraded{Component: "git", State: SectionSkipped, Detail: lt.Detail, Recovery: lt.Recovery}
	}
	return lt
}

// isStale reports whether the snippet shown might no longer be the code
// that actually crashed. It is true when:
//   - git blame's own answer for the culprit line is an UNCOMMITTED change
//     (git's well-known all-zero SHA for a line not yet committed) -- the
//     file on disk right now is, by definition, not what was recorded;
//   - the file differs from HEAD while FirstGitSHA is unknown (LUX-5) --
//     the ordinary `monitor run --` journey never sets FirstGitSHA (it
//     comes only from MONITOR_GIT_SHA/GIT_SHA/GITHUB_SHA env), and the
//     old behavior here (stale:false, no matter what) kept reporting a
//     confident snippet on the WRONG line right after an edit shifted
//     lines: an uncommitted difference against HEAD is the strongest
//     signal available without a persisted line hash;
//   - the blamed commit is not an ancestor of the issue's FirstGitSHA (the
//     line was touched by a commit that did not exist yet when this issue
//     was first recorded); or
//   - the file itself differs from its state at FirstGitSHA (an uncommitted
//     edit made without changing the blamed line's OWN last commit, e.g. a
//     nearby line moved this one without git attributing a new blame to it).
//
// It stays false when there is nothing to compare against (no blame
// answer, no git root, and -- for the FirstGitSHA rules -- no recorded
// SHA); guessing true from an inconclusive check would be exactly the
// fabricated-provenance mistake this field exists to avoid, and guessing
// false (the previous behavior, comparing two SHA-shaped strings that
// are almost never actually equal even on an unchanged file) reported
// stale for nearly every issue with a FirstGitSHA, which is just as
// wrong in the other direction.
func isStale(ctx context.Context, root string, lt LastTouched, issue issues.Issue, relFile string) bool {
	if lt.Status != SectionOK || lt.SHA == "" {
		return false
	}
	if isZeroGitSHA(lt.SHA) {
		return true
	}
	if issue.FirstGitSHA == "" {
		// LUX-5's interim rule until a culprit-line hash is persisted at
		// ingest: see the doc comment above.
		return gitWorktreeFileDiffers(ctx, root, relFile)
	}
	if root == "" {
		return false
	}
	if !gitIsAncestor(ctx, root, lt.SHA, issue.FirstGitSHA) {
		return true
	}
	return gitFileDiffers(ctx, root, issue.FirstGitSHA, relFile)
}

// gitWorktreeFileDiffers reports whether relFile's working-tree content in
// root differs from HEAD, via `git diff --quiet HEAD -- <file>` (LUX-5's
// staleness check for issues with no FirstGitSHA). Unlike gitFileDiffers
// (blame.go), an inconclusive result -- git missing, root not a
// repository, the file unknown to HEAD, or a timeout -- reports false:
// FirstGitSHA is already unknown here, so no signal is strong enough to
// justify guessing stale (Snippet.Stale's "never guessed true" rule).
// Only git's definite "exit 1: the file differs" counts.
func gitWorktreeFileDiffers(ctx context.Context, root, relFile string) bool {
	if root == "" || relFile == "" {
		return false
	}
	cctx, cancel := context.WithTimeout(ctx, gitTimeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, "git", "diff", "--quiet", "HEAD", "--", relFile)
	cmd.Dir = root
	cmd.WaitDelay = 500 * time.Millisecond
	err := cmd.Run()
	if err == nil {
		return false
	}
	var exitErr *exec.ExitError
	return errors.As(err, &exitErr) && exitErr.ExitCode() == 1
}

func nextActionsFor(issue issues.Issue, culprit *CulpritInfo) []NextAction {
	short := strings.ToLower(shortID(issue.ID))
	next := []NextAction{
		{CLI: fmt.Sprintf("monitor issue %s --md", short), Why: "paste-ready fix context for your agent"},
	}
	// (SEC-4) two guards on the $EDITOR hint:
	//
	// The culprit path is untrusted stderr text (in-app is decided by
	// prefix, with no existence check), so it is shell-quoted -- an
	// unquoted `$(...)` in a forged frame otherwise survives verbatim
	// into a command monitor itself proposes, and running it executes
	// the substitution.
	//
	// The hint is offered only when the snippet read succeeded: a culprit
	// file that could not be read under the root is exactly a path
	// monitor cannot vouch for, and "jump straight to the culprit line"
	// promises a line the page just showed.
	if culprit != nil && culprit.File != "" && culprit.Line > 0 && culprit.Snippet != nil {
		next = append(next, NextAction{CLI: fmt.Sprintf("$EDITOR +%d %s", culprit.Line, shellQuote(culprit.File)), Why: "jump straight to the culprit line"})
	}
	if issue.Status == issues.StatusOpen {
		next = append(next, NextAction{CLI: fmt.Sprintf("monitor issues resolve %s", strings.ToUpper(short)), Why: "marks it resolved; reopens automatically if it recurs"})
	}
	return next
}

// shellQuote wraps s in single quotes for a shell command line, escaping
// any embedded single quote the POSIX way (' + \' + '), so an untrusted
// culprit path cannot break out of the $EDITOR hint nextActionsFor
// proposes (SEC-4). Byte-for-byte the same helper cli/hot.go uses for its
// `monitor hot --file` hint; duplicated here rather than exported through
// a new package boundary for one call site -- the two must stay in sync.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func sortDegraded(d []Degraded) {
	for i := 1; i < len(d); i++ {
		for j := i; j > 0 && d[j-1].Component > d[j].Component; j-- {
			d[j-1], d[j] = d[j], d[j-1]
		}
	}
}

// redactContext runs a defense-in-depth scrub pass over every free-text
// field this read assembled from disk or the store (see Options.Redact's
// doc comment), returning how many values it redacted. The scrubber also
// carries the exact values of the CURRENT process's secret env vars
// (scrub.SecretEnvValues over os.Environ), mirroring the ingest-side
// scrubbers (devrun's detector, `stacktrace parse --record`): an opaque
// secrets-manager value that leaked into a live-read snippet line has no
// shape a detector can recognize, so only its literal value catches it.
// The env at explain time may differ from the env at ingest time (a
// replay read from another shell misses values, and picks up new ones) --
// this pass is defense-in-depth on top of the ingest-time gate, never a
// replacement for it.
func redactContext(c *Context) int {
	s := scrub.New(scrub.WithValues(scrub.SecretEnvValues(os.Environ(), nil)))
	c.Issue.Title = s.String(c.Issue.Title)
	if c.Culprit != nil && c.Culprit.Snippet != nil {
		for i, line := range c.Culprit.Snippet.Lines {
			c.Culprit.Snippet.Lines[i] = s.String(line)
		}
	}
	if c.LastTouched.Subject != "" {
		c.LastTouched.Subject = s.String(c.LastTouched.Subject)
	}
	return s.Count()
}
