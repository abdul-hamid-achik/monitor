package explain

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/scrub"
)

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
	// Redact, when true (the default: see normalizeOptions), runs a
	// defense-in-depth scrub.Scrubber pass over every free-text field this
	// read assembles (title, snippet lines, last_touched.subject) before
	// returning -- on top of, never instead of, the scrubbing E2.2 already
	// applied before this text was persisted. This catches text that could
	// never have been scrubbed at ingest: the culprit snippet is read LIVE
	// from disk, not from anything monitor ever wrote to the store.
	Redact bool
	// Now overrides GeneratedAt / "current time" for deterministic tests.
	// Zero uses time.Now().UTC().
	Now time.Time
	// Root overrides the git root Build resolves culprit ranges, snippets,
	// impact, and last_touched against. Empty resolves it the same way the
	// rest of monitor resolves an implicit codebase root: via
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
	root := strings.TrimSpace(opts.Root)
	if root == "" {
		root = project.Resolve(project.Hints{UseWorkingDir: true}).GitRoot
	}

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

	degraded := map[string]Degraded{} // component -> entry, deduplicated

	culprit := culpritInfoFor(ctx, root, issue, degraded)
	c.Culprit = culprit

	if issue.LatestException != nil {
		for _, cause := range issue.LatestException.Causes {
			c.Causes = append(c.Causes, causeEntryFrom(cause))
		}
		for _, f := range issue.LatestException.Frames {
			c.Frames = append(c.Frames, FrameEntry{Function: f.Function, File: f.Filename, Line: f.Lineno, InApp: f.InApp})
		}
	}

	c.Impact = impactFor(ctx, root, culprit, degraded)
	c.LastTouched = lastTouchedSection(ctx, root, culprit, issue, degraded)
	if culprit != nil && culprit.Snippet != nil {
		culprit.Snippet.Stale = isStale(c.LastTouched, issue)
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
		Title: title, ExceptionType: issue.ExceptionType, Handled: issue.Handled,
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
		TimeSource: "live",
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
// there is one, else E2.8's message-search fallback. It also resolves the
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
	} else {
		message := issue.Title
		if message == "" {
			message = issue.Message
		}
		result, d := culpritForMessage(ctx, root, message)
		if d != nil {
			degraded[d.Component] = *d
		}
		if result != nil {
			info = &CulpritInfo{
				File: result.File, Line: result.Line,
				Source: "message_search", Via: result.Via, Confidence: "low",
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

// isStale reports whether the culprit's last-touching commit differs from
// the issue's FirstGitSHA -- see Snippet.Stale's doc comment for why an
// unknown FirstGitSHA never produces a guessed true.
func isStale(lt LastTouched, issue issues.Issue) bool {
	return lt.Status == SectionOK && lt.SHA != "" && issue.FirstGitSHA != "" && !strings.HasPrefix(lt.SHA, issue.FirstGitSHA) && !strings.HasPrefix(issue.FirstGitSHA, lt.SHA)
}

func nextActionsFor(issue issues.Issue, culprit *CulpritInfo) []NextAction {
	short := strings.ToLower(shortID(issue.ID))
	next := []NextAction{
		{CLI: fmt.Sprintf("monitor issue %s --md", short), Why: "paste-ready fix context for your agent"},
	}
	if culprit != nil && culprit.File != "" && culprit.Line > 0 {
		next = append(next, NextAction{CLI: fmt.Sprintf("$EDITOR +%d %s", culprit.Line, culprit.File), Why: "jump straight to the culprit line"})
	}
	if issue.Status == issues.StatusOpen {
		next = append(next, NextAction{CLI: fmt.Sprintf("monitor issues resolve %s", strings.ToUpper(short)), Why: "marks it resolved; reopens automatically if it recurs"})
	}
	return next
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
// doc comment), returning how many values it redacted.
func redactContext(c *Context) int {
	s := scrub.New()
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
