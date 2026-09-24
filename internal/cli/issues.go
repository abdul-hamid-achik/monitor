package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/explain"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/project"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

const (
	defaultIssueListLimit      = 50
	defaultOccurrenceListLimit = 20
	maxIssueQueryLimit         = 200
)

type issueDetailOutput struct {
	Issue                issues.Issue        `json:"issue"`
	Occurrences          []issues.Occurrence `json:"occurrences"`
	OccurrencesTruncated bool                `json:"occurrences_truncated"`
}

type issueMutationOutput struct {
	Updated bool         `json:"updated"`
	Issue   issues.Issue `json:"issue"`
}

type issueErrorOutput struct {
	Error    string `json:"error"`
	ID       string `json:"id,omitempty"`
	NotFound bool   `json:"not_found,omitempty"`
}

type issueCommandError struct {
	action string
	id     string
	err    error
}

func (e *issueCommandError) Error() string {
	if errors.Is(e.err, issues.ErrIssueNotFound) {
		return fmt.Sprintf("issue %s not found", e.id)
	}
	return fmt.Sprintf("issues %s: %v", e.action, e.err)
}

func (e *issueCommandError) Unwrap() error { return e.err }

// silenceOwnErrors sets SilenceErrors on cmd only (root.go's own
// SilenceErrors stays false — every other command keeps cobra's default
// printing) and returns cmd, so every issue/issues subcommand can opt out
// of the double-print in one line at its own construction site. Without
// it, a RunE error printed TWICE: once from cobra's own ExecuteC
// (SilenceErrors false on both the resolved subcommand and root) and once
// more from cli.Execute()'s own "Error: %v" — see internal/cli/hot.go's
// newHotCmd, which established this exact one-command-at-a-time pattern
// for the same finding (the polish review's "errors are printed twice"
// finding named `monitor issue zzzzzz` printing twice as one of its own
// pieces of evidence). Relies on cobra's own rule that an error is only
// printed when NEITHER the resolved command NOR root has SilenceErrors
// set, so this is a per-command opt-out, never a global behavior change.
func silenceOwnErrors(cmd *cobra.Command) *cobra.Command {
	cmd.SilenceErrors = true
	return cmd
}

func newIssuesCmd() *cobra.Command {
	var storePath string
	cmd := &cobra.Command{
		// "issue" (singular) used to be a cobra Aliases entry of this
		// command; E2.5 gives it its own, different meaning (newIssueCmd,
		// registered in root.go) -- the naming ADR's `issue`/`issues`
		// collision row -- so it is deliberately NOT an alias here anymore.
		Use:          "issues [flags]",
		Short:        "List and manage durable grouped issues",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
	}
	silenceOwnErrors(cmd)
	cmd.PersistentFlags().StringVar(&storePath, "store", "", "issue store path (default: $MONITOR_ISSUES_STORE or XDG data dir)")
	listCmd := newIssuesListCmd(&storePath)
	// Bare `monitor issues [flags]` behaves exactly like
	// `monitor issues list [flags]` (see the local-sentry roadmap's UX
	// mockup, which calls it this way, e.g. `monitor issues --since 24h`);
	// `issues list` keeps working unchanged for scripts that already spell
	// it out. Both share the exact same flag variables and RunE, so a flag
	// parsed through either command line means the same thing.
	cmd.RunE = listCmd.RunE
	cmd.Flags().AddFlagSet(listCmd.Flags())
	cmd.AddCommand(
		listCmd,
		newIssuesShowCmd(&storePath),
		newIssueStatusCmd(&storePath, "resolve", issues.StatusResolved),
		newIssueStatusCmd(&storePath, "reopen", issues.StatusOpen),
		newIssueStatusCmd(&storePath, "ignore", issues.StatusIgnored),
	)
	return cmd
}

func newIssuesListCmd(storePath *string) *cobra.Command {
	var (
		statuses    []string
		projectFlag string
		service     string
		since       string
		until       string
		runID       string
		release     string
		kind        string
		limit       int
		at          string
		root        string
		all         bool
	)
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List grouped issues newest-first",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := validateIssueLimit("limit", limit); err != nil {
				return err
			}
			parsedStatuses, err := parseIssueStatuses(statuses)
			if err != nil {
				return err
			}
			now := time.Now()
			sinceBound, err := issues.ParseWindowBound(since, now)
			if err != nil {
				return &issueCommandError{action: "list", err: err}
			}
			untilBound, err := issues.ParseWindowBound(until, now)
			if err != nil {
				return &issueCommandError{action: "list", err: err}
			}
			path, err := resolveIssueStorePath(*storePath)
			if err != nil {
				return &issueCommandError{action: "list", err: err}
			}
			store, err := issues.OpenReadOnly(path)
			if err != nil {
				return &issueCommandError{action: "list", err: err}
			}
			// Bare `monitor issues` (no --project, no --all) lists the
			// CURRENT project only (roadmap: "issues sin argumentos lista
			// el proyecto actual") -- without this, a store holding several
			// projects' issues silently mixed all of them into one list
			// regardless of which checkout the command ran from. Scoped to
			// the HUMAN view only: --json is the machine/scripting path
			// (specs, MCP-adjacent tooling, an agent's own `issues --json`
			// call) that has always defaulted to "every project matching
			// every OTHER explicit filter" -- silently narrowing THAT by
			// cwd would break any script or spec invoking it from a fixed
			// checkout without expecting a directory-dependent answer.
			effectiveProject := strings.TrimSpace(projectFlag)
			if effectiveProject == "" && !all && !JSONOutput(cmd) {
				// PID: 1 is a positive, non-real PID purely to steer
				// project.Resolve's OWN "PID<=0 means a host-wide,
				// no-process event" special case (Hints.PID's doc comment)
				// away from returning the literal slug "host" here -- this
				// call is about resolving the CURRENT directory's project
				// identity, not describing a specific process.
				effectiveProject = project.Resolve(project.Hints{UseWorkingDir: true, PID: 1}).Slug
			}
			// --at (E3.4/E3's "monitor issues --at <file:line>") narrows to
			// issues whose culprit lands inside the target function/line, a
			// query that can legitimately match an issue outside the
			// default page size -- use every matching issue, not just the
			// first --limit of them, in that mode.
			effectiveLimit := limit
			if strings.TrimSpace(at) != "" {
				effectiveLimit = 0
			}
			entries, listErr := store.List(issues.ListOptions{
				Statuses: parsedStatuses,
				Project:  effectiveProject,
				Service:  strings.TrimSpace(service),
				Since:    sinceBound,
				Until:    untilBound,
				RunID:    strings.TrimSpace(runID),
				Release:  strings.TrimSpace(release),
				Kind:     strings.TrimSpace(kind),
				Limit:    effectiveLimit,
			})
			closeErr := store.Close()
			if listErr != nil {
				return &issueCommandError{action: "list", err: listErr}
			}
			if closeErr != nil {
				return &issueCommandError{action: "list", err: closeErr}
			}
			if entries == nil {
				entries = []issues.Issue{}
			}
			if strings.TrimSpace(at) != "" {
				return runIssuesAt(cmd, entries, at, root)
			}
			// Payload diet (AC-6): a list row carries only a trimmed
			// LatestException summary, never the full frame/cause detail --
			// see issues.SummarizeForList. `issues show` (Store.Get, one
			// issue) keeps the untrimmed detail.
			for i := range entries {
				entries[i] = issues.SummarizeForList(entries[i])
			}
			if JSONOutput(cmd) {
				return writeIssueJSON(cmd.OutOrStdout(), entries)
			}
			return writeIssuesListHuman(cmd.OutOrStdout(), path, entries, effectiveProject)
		},
	}
	cmd.Flags().StringSliceVar(&statuses, "status", nil, "filter by status (open, resolved, ignored; repeatable)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "filter by project (default: the current project, unless --all)")
	cmd.Flags().BoolVar(&all, "all", false, "list issues across every project instead of defaulting to the current one")
	cmd.Flags().StringVar(&service, "service", "", "filter by service")
	cmd.Flags().StringVar(&since, "since", "", "only issues active at/after this time (RFC3339 or a duration like 10m, 24h ago)")
	cmd.Flags().StringVar(&until, "until", "", "only issues active at/before this time (RFC3339 or a duration like 10m, 24h ago)")
	cmd.Flags().StringVar(&runID, "run-id", "", "filter by a run id the issue has seen")
	cmd.Flags().StringVar(&release, "release", "", "filter by a release the issue has seen")
	cmd.Flags().StringVar(&kind, "kind", "", "filter by kind: exception, alert, investigation, or any (default: any)")
	cmd.Flags().IntVar(&limit, "limit", defaultIssueListLimit, "maximum issues to return (1-200)")
	cmd.Flags().StringVar(&at, "at", "", "list issues whose culprit is inside the function containing, or exactly at, this file:line")
	cmd.Flags().StringVar(&root, "root", "", "git/codebase root for --at's codemap lookup (default: discovered from the working directory)")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return silenceOwnErrors(cmd)
}

func newIssuesShowCmd(storePath *string) *cobra.Command {
	var occurrenceLimit int
	cmd := &cobra.Command{
		Use:   "show <id>",
		Short: "Show an issue and its recent occurrences",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := validateIssueLimit("occurrences", occurrenceLimit); err != nil {
				return err
			}
			id := strings.TrimSpace(args[0])
			path, err := resolveIssueStorePath(*storePath)
			if err != nil {
				return writeIssueError(cmd, "show", id, err)
			}
			store, err := issues.OpenReadOnly(path)
			if err != nil {
				return writeIssueError(cmd, "show", id, err)
			}
			issue, getErr := store.Get(id)
			if getErr != nil {
				_ = store.Close()
				return writeIssueError(cmd, "show", id, getErr)
			}
			occurrences, occurrenceErr := store.Occurrences(id, occurrenceLimit)
			closeErr := store.Close()
			if occurrenceErr != nil {
				return writeIssueError(cmd, "show", id, occurrenceErr)
			}
			if closeErr != nil {
				return writeIssueError(cmd, "show", id, closeErr)
			}
			if occurrences == nil {
				occurrences = []issues.Occurrence{}
			}
			out := issueDetailOutput{
				Issue:       issue,
				Occurrences: occurrences,
				// A coalesced burst (issues.OccurrenceInput.Count) makes one
				// retained row stand for several raw events, so comparing
				// against len(occurrences) reports "truncated" for any
				// coalesced occurrence even when every row was retained.
				// Compare against the sum of the retained rows' Count
				// instead.
				OccurrencesTruncated: issue.OccurrenceCount > sumOccurrenceCounts(occurrences),
			}
			if JSONOutput(cmd) {
				return writeIssueJSON(cmd.OutOrStdout(), out)
			}
			return writeIssueDetail(cmd.OutOrStdout(), out)
		},
	}
	cmd.Flags().IntVar(&occurrenceLimit, "occurrences", defaultOccurrenceListLimit, "maximum occurrences to return (1-200)")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return silenceOwnErrors(cmd)
}

func newIssueStatusCmd(storePath *string, action string, status issues.Status) *cobra.Command {
	cmd := &cobra.Command{
		Use:   action + " <id>",
		Short: issueStatusShort(action),
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			id := strings.TrimSpace(args[0])
			path, err := resolveIssueStorePath(*storePath)
			if err != nil {
				return writeIssueError(cmd, action, id, err)
			}
			// Waits up to issues.DefaultWriterWait for a concurrent writer
			// (e.g. a live `watch --stash`) to release its brief exclusive
			// lock instead of failing outright with ErrFileLocked (bug 12).
			var updated issues.Issue
			err = issues.WithWriter(cmd.Context(), path, issues.DefaultWriterWait, func(store *issues.Store) error {
				// Accept a short_id/prefix (as `monitor issues`/`monitor
				// issue` display them and as explain.Build's own NEXT
				// actions suggest), not just the literal full ID.
				resolvedID, resolveErr := store.ResolveID(id)
				if resolveErr != nil {
					return resolveErr
				}
				id := resolvedID
				var statusErr error
				switch status {
				case issues.StatusResolved:
					updated, statusErr = store.Resolve(id)
				case issues.StatusOpen:
					updated, statusErr = store.Reopen(id)
				case issues.StatusIgnored:
					updated, statusErr = store.Ignore(id)
				default:
					statusErr = fmt.Errorf("unsupported target status %q", status)
				}
				return statusErr
			})
			if err != nil {
				return writeIssueError(cmd, action, id, err)
			}
			if JSONOutput(cmd) {
				return writeIssueJSON(cmd.OutOrStdout(), issueMutationOutput{Updated: true, Issue: updated})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s is %s.\n", updated.ID, updated.Status)
			return err
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return silenceOwnErrors(cmd)
}

func issueStatusShort(action string) string {
	switch action {
	case "resolve":
		return "Mark an issue resolved"
	case "reopen":
		return "Reopen a resolved or ignored issue"
	case "ignore":
		return "Ignore an issue without dropping future occurrences"
	default:
		return "Update an issue"
	}
}

// sumOccurrenceCounts totals the Count of every retained occurrence, so a
// coalesced burst (one row, Count > 1) is weighed the same as the raw
// events it subsumes when deciding whether older occurrences were dropped.
func sumOccurrenceCounts(occurrences []issues.Occurrence) int64 {
	var total int64
	for _, occurrence := range occurrences {
		if occurrence.Count > 0 {
			total += occurrence.Count
		} else {
			total++
		}
	}
	return total
}

func resolveIssueStorePath(explicit string) (string, error) {
	return issues.ResolvePath(explicit)
}

func parseIssueStatuses(values []string) ([]issues.Status, error) {
	result := make([]issues.Status, 0, len(values))
	seen := make(map[issues.Status]struct{}, len(values))
	for _, value := range values {
		status := issues.Status(strings.ToLower(strings.TrimSpace(value)))
		switch status {
		case issues.StatusOpen, issues.StatusResolved, issues.StatusIgnored:
		default:
			return nil, fmt.Errorf("invalid issue status %q (use open, resolved, or ignored)", value)
		}
		if _, ok := seen[status]; ok {
			continue
		}
		seen[status] = struct{}{}
		result = append(result, status)
	}
	return result, nil
}

func validateIssueLimit(name string, value int) error {
	if value < 1 || value > maxIssueQueryLimit {
		return fmt.Errorf("--%s must be between 1 and %d", name, maxIssueQueryLimit)
	}
	return nil
}

func writeIssueError(cmd *cobra.Command, action, id string, err error) error {
	wrapped := &issueCommandError{action: action, id: id, err: err}
	if JSONOutput(cmd) {
		_ = writeIssueJSON(cmd.OutOrStdout(), issueErrorOutput{
			Error:    wrapped.Error(),
			ID:       id,
			NotFound: errors.Is(err, issues.ErrIssueNotFound),
		})
	}
	return wrapped
}

func writeIssueJSON(w io.Writer, value any) error {
	encoder := json.NewEncoder(w)
	encoder.SetIndent("", "  ")
	return encoder.Encode(value)
}

// activityWindow/activityBuckets size the human list's 24h sparkline column
// (the naming ADR's UX mockup section 3: a 10-char
// ".........#"-style column).
const (
	activityWindow  = 24 * time.Hour
	activityBuckets = 10
	// newIssueWindow bounds the "NEW" label (issueLabel): an issue whose
	// FIRST occurrence landed within this long ago is still "new" for
	// display purposes, distinct from "REGRESSED" (ReopenedCount > 0,
	// which applies regardless of age). This is a monitor-CLI display
	// convention, not part of any persisted field.
	newIssueWindow = 24 * time.Hour
	// maxWhereLen/maxTitleLen truncate the human table's widest free-text
	// columns so one long title or a deeply nested path never blows out
	// every other column's alignment in a real terminal width.
	//
	// maxTitleLen is narrowed from 55 by the polish review's "issues list
	// at 137 columns" finding: with the NEW/REGRESSED + severity prefix
	// folded into the TITLE cell (statusCell, up to ~11 runes: "NEW
	// warning"/"REGRESSED"), the old 55 let one real row reach 137
	// columns; 20 brings that same shape down to ~103.
	//
	// maxWhereLen stays at its original 40, NOT narrowed to fit 100 the
	// way the review's own text suggests ("narrow the WHERE column, or
	// make it project-relative"): specs/issues_context.yml's
	// issues_list_shows_short_id_activity_and_where outcome hard-asserts
	// the FULL, untruncated "examples/polyglot/js/workload.js:31" (35
	// runes) appears in this exact column for a real seeded issue -- a
	// cap below ~35, or a project-relative rewrite that drops the
	// "examples/polyglot/" prefix, both make that committed, must-pass
	// spec fail. 40 keeps that contract intact while
	// truncatePathDisplay's own tail-preserving cut (see below) still
	// improves the genuinely pathological case (a monorepo path deeper
	// than 40 runes) that used to render in full, unbounded.
	maxTitleLen = 20
	maxWhereLen = 40
)

// writeIssuesListHuman renders entries as `monitor issues`' human table:
// short ids, a 24h activity sparkline, a NEW/REGRESSED label folded into the
// status cell, and a WHERE column naming the culprit file:line -- see the
// local-sentry roadmap's UX mockup section 3. storePath is reopened
// read-only (best-effort: a failure here degrades to an all-empty
// sparkline column rather than failing the whole list) to read each
// entry's recent occurrence timestamps, since issues.Issue itself does not
// carry a time series.
func writeIssuesListHuman(w io.Writer, storePath string, entries []issues.Issue, projectFilter string) error {
	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, "No issues found.")
		return err
	}
	now := time.Now()
	activity := activityBucketsFor(storePath, entries, now)
	// Widened per THIS listing when 4 hex characters collide among the rows
	// actually being printed (see uniqueDisplayIDs) -- a 4-char short id
	// that isn't actually unique among what's on screen would make the
	// footer's own suggested `monitor issue <id>` immediately ambiguous.
	displayIDs := uniqueDisplayIDs(entries)

	if err := writeIssuesListHeader(w, entries, projectFilter, now); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tEVENTS\t24H\tLAST\tTITLE\tWHERE"); err != nil {
		return err
	}
	for _, issue := range entries {
		statusCell := strings.TrimSpace(issueLabel(issue, now) + " " + severityWord(issue))
		title := truncateDisplay(displayIssueValue(firstNonEmpty(issue.Title, issue.Message)), maxTitleLen)
		if statusCell != "" {
			title = statusCell + "  " + title
		}
		line := widgets.RenderActivityLine(activity[issue.ID])
		if _, err := fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%s\n",
			strings.ToUpper(displayIDs[issue.ID]), issue.OccurrenceCount, line,
			humanDuration(now.Sub(issue.LastSeen)), title,
			truncatePathDisplay(culpritLocation(issue), maxWhereLen)); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	return writeIssuesListFooter(w, entries, displayIDs)
}

// uniqueDisplayIDs returns, for each of entries, the shortest ID-suffix
// length (starting at the usual 4 hex characters -- shortIssueID) that is
// unique among entries ITSELF, widening only the colliding groups rather
// than every row. Collisions here are rare (a 4-hex-char short id has a
// large space) but not impossible in a large list, and the mockup's own
// `next monitor issue <id>` hint is worthless if that id then resolves
// ambiguously.
func uniqueDisplayIDs(entries []issues.Issue) map[string]string {
	out := make(map[string]string, len(entries))
	remaining := make([]string, 0, len(entries))
	for _, e := range entries {
		remaining = append(remaining, e.ID)
	}
	for length := 4; len(remaining) > 0 && length <= 32; length++ {
		groups := make(map[string][]string, len(remaining))
		for _, id := range remaining {
			suffix := strings.TrimPrefix(id, "ISS-")
			key := suffix
			if len(key) > length {
				key = key[:length]
			}
			groups[key] = append(groups[key], id)
		}
		var next []string
		for key, ids := range groups {
			if len(ids) == 1 {
				out[ids[0]] = key
				continue
			}
			next = append(next, ids...)
		}
		remaining = next
	}
	// Exhausted the full hex length and something STILL collides (only
	// possible for literal duplicate IDs, which should not exist) -- fall
	// back to the plain 4-char id rather than leaving it unset.
	for _, id := range remaining {
		out[id] = shortIssueID(id)
	}
	return out
}

// writeIssuesListHeader prints "<project> · N open · M new in the last
// hour" (or a project-less variant when the results span more than one
// project and none was requested via --project).
func writeIssuesListHeader(w io.Writer, entries []issues.Issue, projectFilter string, now time.Time) error {
	label := strings.TrimSpace(projectFilter)
	if label == "" {
		label = commonProject(entries)
	}
	var open, newInLastHour int
	for _, issue := range entries {
		if issue.Status == issues.StatusOpen {
			open++
		}
		if !issue.FirstSeen.IsZero() && now.Sub(issue.FirstSeen) < time.Hour {
			newInLastHour++
		}
	}
	parts := []string{}
	if label != "" {
		parts = append(parts, label)
	}
	parts = append(parts, fmt.Sprintf("%d open", open))
	if newInLastHour > 0 {
		parts = append(parts, fmt.Sprintf("%d new in the last hour", newInLastHour))
	}
	_, err := fmt.Fprintln(w, strings.Join(parts, " · "))
	return err
}

// writeIssuesListFooter prints the mockup's "next" hint line, pointing at
// the top (most recently active) row's short id.
func writeIssuesListFooter(w io.Writer, entries []issues.Issue, displayIDs map[string]string) error {
	if len(entries) == 0 {
		return nil
	}
	id := strings.ToLower(displayIDs[entries[0].ID])
	_, err := fmt.Fprintf(w, "next  monitor issue %s  ·  monitor issue %s --md | pbcopy  ·  monitor issues --status resolved\n", id, id)
	return err
}

// commonProject returns entries' shared Project when every entry has the
// same one, else "".
func commonProject(entries []issues.Issue) string {
	if len(entries) == 0 {
		return ""
	}
	first := entries[0].Project
	for _, e := range entries[1:] {
		if e.Project != first {
			return ""
		}
	}
	return first
}

// activityBucketsFor reads up to a bounded number of recent occurrences per
// entry and buckets their timestamps into the last activityWindow. A
// failure to open storePath degrades to every issue reporting an
// all-empty (all-'.') sparkline rather than failing the list -- the
// sparkline is a nice-to-have annotation, never load-bearing.
func activityBucketsFor(storePath string, entries []issues.Issue, now time.Time) map[string][]int64 {
	result := make(map[string][]int64, len(entries))
	store, err := issues.OpenReadOnly(storePath)
	if err != nil {
		return result
	}
	defer store.Close()
	since := now.Add(-activityWindow)
	for _, issue := range entries {
		occurrences, err := store.Occurrences(issue.ID, 500)
		if err != nil {
			continue
		}
		times := make([]time.Time, 0, len(occurrences))
		for _, occ := range occurrences {
			times = append(times, occ.ObservedAt)
		}
		result[issue.ID] = widgets.BucketCounts(times, since, now, activityBuckets)
	}
	return result
}

// issueLabel returns "REGRESSED" (reopened at least once, regardless of
// age), "NEW" (first seen within newIssueWindow and never reopened), or ""
// (an ordinary, already-known open issue) -- monitor issues' display-only
// convention, not a persisted field.
func issueLabel(issue issues.Issue, now time.Time) string {
	if issue.ReopenedCount > 0 {
		return "REGRESSED"
	}
	if !issue.FirstSeen.IsZero() && now.Sub(issue.FirstSeen) < newIssueWindow {
		return "NEW"
	}
	return ""
}

// severityWord renders the human list's short severity word: "fatal" for
// an uncaught crash, "handled" for a caught-and-printed error, else the
// stacktrace Level ("error"/"warning") or, lacking that, the legacy free-
// form Severity field.
func severityWord(issue issues.Issue) string {
	switch {
	case issue.Level == "fatal":
		return "fatal"
	case issue.Handled != nil && *issue.Handled:
		return "handled"
	case issue.Level != "":
		return issue.Level
	default:
		return issue.Severity
	}
}

// culpritLocation renders "file:line" from issue.Culprit, or "-" when the
// issue has none (a message-only event whose message-search fallback
// hasn't been resolved, or a non-exception issue kind).
func culpritLocation(issue issues.Issue) string {
	if issue.Culprit == nil || issue.Culprit.File == "" {
		return "-"
	}
	if issue.Culprit.Line > 0 {
		return fmt.Sprintf("%s:%d", issue.Culprit.File, issue.Culprit.Line)
	}
	return issue.Culprit.File
}

// shortIssueID is issues.Issue.ID's short_id (see docs/contracts/
// issue-context-v1.md): the first 4 hex characters after "ISS-". Mirrors
// internal/explain's unexported shortID -- duplicated rather than exported
// across the package boundary for this one display use.
func shortIssueID(id string) string {
	suffix := strings.TrimPrefix(id, "ISS-")
	if len(suffix) > 4 {
		return suffix[:4]
	}
	return suffix
}

// humanDuration renders a coarse, single-unit relative age ("2m", "9h",
// "3d") for the LAST column -- deliberately coarser than time.Duration's
// own String(), which would print "2m3.412s" and blow out the column.
func humanDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	switch {
	case d < time.Minute:
		return "now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d/time.Minute))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d/time.Hour))
	default:
		return fmt.Sprintf("%dd", int(d/(24*time.Hour)))
	}
}

// truncateDisplay shortens s to at most n runes, marking the cut with a
// trailing "..." (three ASCII dots, matching the mockup's own "...") when it
// actually had to cut anything.
func truncateDisplay(s string, n int) string {
	r := []rune(s)
	if len(r) <= n || n <= 3 {
		return s
	}
	return string(r[:n-3]) + "..."
}

// truncatePathDisplay shortens a "file:line"-shaped path to at most n
// runes, keeping its TAIL — the filename and line number, the part someone
// actually needs to find the culprit — rather than truncateDisplay's own
// right-truncation, which would keep a long leading directory (e.g.
// "examples/polyglot/") and cut off exactly the useful part. A leading "…"
// marks the cut. See the polish review's "narrow the WHERE column ...
// sensibly" finding.
func truncatePathDisplay(s string, n int) string {
	r := []rune(s)
	if len(r) <= n || n <= 1 {
		return s
	}
	return "…" + string(r[len(r)-(n-1):])
}

func writeIssueDetail(w io.Writer, out issueDetailOutput) error {
	issue := out.Issue
	if _, err := fmt.Fprintf(w, "Issue %s\nStatus: %s\nProject: %s\nService: %s\nSeverity: %s\nTitle: %s\nFirst seen: %s\nLast seen: %s\nOccurrences: %d\n",
		issue.ID, issue.Status, displayIssueValue(issue.Project), displayIssueValue(issue.Service),
		displayIssueValue(issue.Severity), displayIssueValue(firstNonEmpty(issue.Title, issue.Message)),
		issue.FirstSeen.Format(time.RFC3339), issue.LastSeen.Format(time.RFC3339), issue.OccurrenceCount); err != nil {
		return err
	}
	if len(out.Occurrences) == 0 {
		_, err := fmt.Fprintln(w, "\nNo occurrences found.")
		return err
	}
	if _, err := fmt.Fprintln(w, "\nRecent occurrences:"); err != nil {
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tOBSERVED AT\tRUN\tPID\tEVIDENCE"); err != nil {
		return err
	}
	for _, occurrence := range out.Occurrences {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%d\n",
			occurrence.ID, occurrence.ObservedAt.Format(time.RFC3339),
			displayIssueValue(occurrence.RunID), occurrence.PID, len(occurrence.EvidenceRefs)); err != nil {
			return err
		}
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	if out.OccurrencesTruncated {
		_, err := fmt.Fprintln(w, "(occurrences truncated; increase --occurrences up to 200)")
		return err
	}
	return nil
}

func displayIssueValue(value string) string {
	if strings.TrimSpace(value) == "" {
		return "-"
	}
	return value
}

// ---------------------------------------------------------------------------
// `monitor issues --at <file:line>` (E3.4 / roadmap "errores x calor")
// ---------------------------------------------------------------------------

// issuesAtOutput is `issues --at`'s --json shape: additive to the plain
// list's []issues.Issue (it's the same slice, just pre-filtered), plus the
// resolved target so a caller can tell a codemap-backed range match from a
// bare exact-line one.
type issuesAtOutput struct {
	File    string         `json:"file"`
	Line    int            `json:"line"`
	Symbol  string         `json:"symbol,omitempty"`
	RangeOK bool           `json:"range_resolved"`
	Start   int            `json:"start,omitempty"`
	End     int            `json:"end,omitempty"`
	Issues  []issues.Issue `json:"issues"`
}

// runIssuesAt filters entries (already matching every other --status/
// --project/... flag) to those whose culprit lands inside the function
// containing target, or -- when codemap can't resolve that range (honest
// degradation, never a fabricated range) -- exactly on target's line.
func runIssuesAt(cmd *cobra.Command, entries []issues.Issue, at, rootFlag string) error {
	file, line, err := parseFileLine(at)
	if err != nil {
		return &issueCommandError{action: "list --at", err: err}
	}
	root := resolveExplainRoot(rootFlag)
	// Stored culprits are root-relative (stacktrace.ApplyGitRoot rewrites
	// Filename that way at record time); an absolute --at path, or one
	// relative to a subdirectory the command happens to run from, matches
	// nothing until it is normalized the same way.
	file = normalizeAtFile(file, root)
	out := issuesAtOutput{File: file, Line: line}

	if root != "" {
		if health := ecosystem.ProbeCodemap(cmd.Context(), root); health.State == ecosystem.HealthOK {
			if sym, err := ecosystem.CodemapSymbolAtPath(cmd.Context(), file, line, ecosystem.CodemapOpts{Path: root}); err == nil && sym.Resolution != "none" && sym.StartLine > 0 {
				out.RangeOK = true
				out.Symbol = sym.Symbol
				out.Start, out.End = sym.StartLine, sym.EndLine
			}
		}
	}

	matched := make([]issues.Issue, 0, len(entries))
	for _, issue := range entries {
		if !culpritMatchesAt(issue, out) {
			continue
		}
		matched = append(matched, issue)
	}
	// Payload diet (AC-6), same as the plain list path just above: a row
	// here carries only a trimmed LatestException summary.
	for i := range matched {
		matched[i] = issues.SummarizeForList(matched[i])
	}
	out.Issues = matched

	if JSONOutput(cmd) {
		return writeIssueJSON(cmd.OutOrStdout(), out)
	}
	return writeIssuesAtHuman(cmd.OutOrStdout(), out)
}

// normalizeAtFile rewrites file (as typed on --at) to be relative to root,
// the same way every stored Culprit.File already is. An absolute path is
// made relative to root directly; a relative one is first resolved against
// the current working directory (the same "relative to wherever you
// happened to run the command from" a human typing --at means) and THEN
// made relative to root. Any step that fails, or resolves outside root
// entirely, returns file unchanged -- best-effort, never fatal: the caller
// falls back to matching on file exactly as given.
func normalizeAtFile(file, root string) string {
	if root == "" {
		return file
	}
	abs := file
	if !filepath.IsAbs(abs) {
		cwd, err := os.Getwd()
		if err != nil {
			return file
		}
		abs = filepath.Join(cwd, file)
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return file
	}
	return filepath.ToSlash(rel)
}

// culpritMatchesAt reports whether issue's culprit falls inside out's
// resolved symbol range, or -- when no range was resolved -- exactly on
// out's target line. Both branches require the same file.
func culpritMatchesAt(issue issues.Issue, out issuesAtOutput) bool {
	if issue.Culprit == nil || issue.Culprit.File == "" {
		return false
	}
	if filepath.Clean(issue.Culprit.File) != filepath.Clean(out.File) {
		return false
	}
	if out.RangeOK {
		return issue.Culprit.Line >= out.Start && issue.Culprit.Line <= out.End
	}
	return issue.Culprit.Line == out.Line
}

func writeIssuesAtHuman(w io.Writer, out issuesAtOutput) error {
	switch {
	case out.RangeOK:
		symbol := out.Symbol
		if symbol == "" {
			symbol = "?"
		}
		if _, err := fmt.Fprintf(w, "%s() · %s:%d-%d (codemap) · %d issue(s) in this function\n",
			symbol, out.File, out.Start, out.End, len(out.Issues)); err != nil {
			return err
		}
	default:
		if _, err := fmt.Fprintf(w, "%s:%d · %d issue(s) at this line (no range resolved: exact-line match only)\n",
			out.File, out.Line, len(out.Issues)); err != nil {
			return err
		}
	}
	if len(out.Issues) == 0 {
		return nil
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "LINE\tID\tEVENTS\tSTATUS\tTITLE"); err != nil {
		return err
	}
	for _, issue := range out.Issues {
		line := 0
		if issue.Culprit != nil {
			line = issue.Culprit.Line
		}
		if _, err := fmt.Fprintf(tw, "%d\t%s\t%d\t%s\t%s\n",
			line, strings.ToUpper(shortIssueID(issue.ID)), issue.OccurrenceCount, issue.Status,
			truncateDisplay(displayIssueValue(firstNonEmpty(issue.Title, issue.Message)), maxTitleLen)); err != nil {
			return err
		}
	}
	return tw.Flush()
}

// parseFileLine parses "path/to/file:123" -- the LAST ':' separates the
// line number, so a path containing ':' elsewhere (rare, but legal on
// POSIX filesystems) still parses correctly.
func parseFileLine(at string) (file string, line int, err error) {
	at = strings.TrimSpace(at)
	idx := strings.LastIndex(at, ":")
	if idx <= 0 || idx == len(at)-1 {
		return "", 0, fmt.Errorf("invalid --at %q: want file:line", at)
	}
	line, err = strconv.Atoi(at[idx+1:])
	if err != nil || line <= 0 {
		return "", 0, fmt.Errorf("invalid --at %q: line must be a positive integer", at)
	}
	return at[:idx], line, nil
}

// resolveExplainRoot picks the git/codebase root `issues --at` and
// `monitor issue` resolve codemap/git/snippet lookups against: an explicit
// override when given, else the same working-directory-based resolution
// project.Resolve uses everywhere else in monitor (see internal/explain.
// Options.Root's doc comment).
func resolveExplainRoot(explicit string) string {
	if root := strings.TrimSpace(explicit); root != "" {
		return root
	}
	return project.Resolve(project.Hints{UseWorkingDir: true}).GitRoot
}

// ---------------------------------------------------------------------------
// `monitor issue <id|short-prefix|latest>` (E2.5)
// ---------------------------------------------------------------------------

// deprecatedIssuesAlias lists `monitor issues`' own subcommand names: before
// E2.5, `issue` (singular) was a plain cobra Aliases entry of `issues`
// (plural), so `monitor issue list`/`show <id>`/`resolve <id>`/etc. all
// worked by literally being `monitor issues <same args>`. E2.5 gave `issue`
// its own, different meaning (the page above) and dropped that alias
// outright, which broke every script still spelling it the old way with no
// warning ("issue list not found" / "accepts 1 arg(s), received 2"). See
// runDeprecatedIssueAlias.
var deprecatedIssuesAlias = map[string]bool{
	"list": true, "show": true, "resolve": true, "reopen": true, "ignore": true,
}

// runDeprecatedIssueAlias delegates `monitor issue <one of
// deprecatedIssuesAlias> ...` to a freshly built `monitor issues` command
// tree, passing every raw arg through UNCHANGED (newIssueCmd runs with
// DisableFlagParsing so this branch is reached before anything has tried to
// interpret them) -- reusing `issues`' own real flag parsing and RunE
// rather than duplicating any of it, so behavior (--json, --status, --since,
// --occurrences, whatever the target subcommand accepts) matches `monitor
// issues <args>` exactly, not just approximately. It also means `--store`
// works exactly like it always did on this alias, as long as it comes after
// the subcommand name (`monitor issue list --store X`), the position every
// pre-E2.5 example used.
func runDeprecatedIssueAlias(cmd *cobra.Command, args []string) error {
	fmt.Fprintf(cmd.ErrOrStderr(), "`monitor issue %s` is deprecated; use `monitor issues %s` instead.\n", args[0], args[0])
	issuesCmd := newIssuesCmd()
	issuesCmd.SetArgs(args)
	issuesCmd.SetOut(cmd.OutOrStdout())
	issuesCmd.SetErr(cmd.ErrOrStderr())
	issuesCmd.SetContext(cmd.Context())
	return issuesCmd.Execute()
}

func newIssueCmd() *cobra.Command {
	var (
		storePath, projectFlag, service, kind, root string
		md                                          bool
	)
	cmd := &cobra.Command{
		Use:   "issue <id|short-prefix|latest>",
		Short: "Show one issue's full context: culprit, causes, impact, and next steps",
		Long: `issue prints the monitor.issue_context.v1 page for one issue: the
culprit line and snippet, the exception chain's causes, the in-app
stack, codemap's blast radius (impact), the last commit that touched
the culprit line, and proposed next steps.

<id> may be a full issue ID, an unambiguous short_id/prefix (as shown
by 'monitor issues'), or the literal "latest" -- optionally narrowed
by --project/--service/--kind -- to mean "the most recently active
issue". An ambiguous prefix exits 2 and lists every match.

--json emits the full monitor.issue_context.v1 contract at the
"standard" budget. --md emits a paste-ready markdown page for an
agent.

'monitor issue list|show|resolve|reopen|ignore ...' (the pre-E2.5
alias of 'monitor issues') keeps working during the deprecation: it
prints a note to stderr and delegates to 'monitor issues <same
args>'.`,
		// DisableFlagParsing: the deprecated-alias check below has to run
		// BEFORE anything tries to interpret args as THIS command's own
		// flags -- `issues list`/`show`/etc. accept flags (--status,
		// --since, --occurrences, ...) this command does not itself
		// register, and cobra would otherwise reject the whole invocation
		// with "unknown flag" before RunE ever ran, never reaching the
		// delegation. The non-deprecated path below parses this command's
		// OWN flags manually instead, once it knows it needs to.
		DisableFlagParsing: true,
		SilenceUsage:       true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			if len(args) > 0 && deprecatedIssuesAlias[strings.ToLower(args[0])] {
				return runDeprecatedIssueAlias(cmd, args)
			}
			if err := cmd.Flags().Parse(args); err != nil {
				return err
			}
			positional := cmd.Flags().Args()
			if len(positional) == 0 {
				return &issueCommandError{action: "issue", err: fmt.Errorf("id is required")}
			}
			if len(positional) > 1 {
				return &issueCommandError{action: "issue", err: fmt.Errorf(
					"accepts 1 arg (an id/prefix/\"latest\"), received %d -- did you mean `monitor issues %s`?", len(positional), positional[0])}
			}
			rawID := strings.TrimSpace(positional[0])
			if rawID == "" {
				return &issueCommandError{action: "issue", err: fmt.Errorf("id is required")}
			}
			path, err := resolveIssueStorePath(storePath)
			if err != nil {
				return &issueCommandError{action: "issue", err: err}
			}
			store, err := issues.OpenReadOnly(path)
			if err != nil {
				return &issueCommandError{action: "issue", id: rawID, err: err}
			}
			defer store.Close()

			opts := explain.Options{Budget: explain.BudgetStandard, Root: resolveExplainRoot(root), Redact: true}
			targetID := rawID
			if strings.EqualFold(rawID, "latest") {
				latest, resolved, ok, latestErr := explain.ResolveLatest(store, explain.LatestFilter{
					Project: projectFlag, Service: service, Kind: kind,
				})
				if latestErr != nil {
					return &issueCommandError{action: "issue", id: rawID, err: latestErr}
				}
				if !ok {
					return writeLatestNotFoundError(cmd, resolved)
				}
				opts.ResolvedFrom = &resolved
				targetID = latest.ID
			}

			built, buildErr := explain.Build(cmd.Context(), store, targetID, opts)
			if buildErr != nil {
				var ambiguous *issues.AmbiguousIDError
				if errors.As(buildErr, &ambiguous) {
					printAmbiguousIssueError(cmd, rawID, ambiguous)
					os.Exit(2)
				}
				return writeIssueError(cmd, "issue", rawID, buildErr)
			}

			switch {
			case md:
				_, err := fmt.Fprint(cmd.OutOrStdout(), built.RenderMarkdown())
				return err
			case JSONOutput(cmd):
				return writeIssueJSON(cmd.OutOrStdout(), built)
			default:
				return writeIssuePageHuman(cmd.OutOrStdout(), built)
			}
		},
	}
	cmd.Flags().StringVar(&storePath, "store", "", "issue store path (default: $MONITOR_ISSUES_STORE or XDG data dir)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "restrict \"latest\" to this project")
	cmd.Flags().StringVar(&service, "service", "", "restrict \"latest\" to this service")
	cmd.Flags().StringVar(&kind, "kind", "", "restrict \"latest\" to this kind: exception (default), alert, investigation, or any")
	cmd.Flags().StringVar(&root, "root", "", "git/codebase root for culprit/impact/blame lookups (default: discovered from the working directory)")
	cmd.Flags().Bool("json", false, "emit the full monitor.issue_context.v1 JSON contract")
	cmd.Flags().BoolVar(&md, "md", false, "emit a paste-ready markdown page for an agent")
	return silenceOwnErrors(cmd)
}

// printAmbiguousIssueError reports every candidate a short prefix matched.
// The caller is responsible for os.Exit(2) right after calling this
// (mirrors internal/cli/resolve.go's printAmbiguousLeaf/os.Exit(2) split
// for procbind.AmbiguousLeafError, including that split's own reasoning:
// keeping the exit call OUT of this function is what lets
// TestPrintAmbiguousIssueErrorHumanAndJSON exercise the printed output
// in-process without terminating the test binary). store.ResolveID's own
// ambiguity detection (which frame is ambiguous) is covered deterministically
// in internal/issues/resolve_id_test.go via a crafted fingerprint collision.
// The literal process-level exit(2) path IS also covered black-box, by
// specs/issues_context.yml's ambiguous_short_prefix_exits_2: a 1-char
// prefix is easy to collide deterministically without any crafted hash --
// seed enough (>16, the hex alphabet's size) distinct message-only issues
// and, by the pigeonhole principle, at least two of their ids are
// guaranteed to share a first hex character.
func printAmbiguousIssueError(cmd *cobra.Command, id string, ambiguous *issues.AmbiguousIDError) {
	if JSONOutput(cmd) {
		_ = writeIssueJSON(cmd.OutOrStdout(), map[string]any{
			"error": "ambiguous", "id": id, "candidates": ambiguous.Matches,
		})
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "issue id %q is ambiguous (%d matches):\n", id, len(ambiguous.Matches))
	for _, m := range ambiguous.Matches {
		fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", m)
	}
}

// writeLatestNotFoundError reports "latest" (optionally filtered) matching
// nothing: a real command failure for the CLI (exit 1, unlike MCP's
// monitor_issue, which turns the same situation into a non-error recovery
// hint -- see internal/mcp/server.go's handleIssue), but with a full,
// actionable message of its own rather than the generic issueCommandError
// wrapper's flattened "issue latest not found" (which would silently drop
// exactly which filters were applied).
func writeLatestNotFoundError(cmd *cobra.Command, resolved explain.ResolvedFrom) error {
	err := fmt.Errorf("%w: no issue matches \"latest\" (project=%q service=%q kind=%q) -- try `monitor issues` to see what is open",
		issues.ErrIssueNotFound, resolved.Project, resolved.Service, resolved.Kind)
	if JSONOutput(cmd) {
		_ = writeIssueJSON(cmd.OutOrStdout(), map[string]any{
			"error": err.Error(), "id": "latest", "not_found": true, "resolved_from": resolved,
			"recovery": "try `monitor issues` to see what is open, or widen --kind to any",
		})
	}
	return err
}

// issuePageWidth is the issue page header's own line width — the same
// 100-column budget monitor hot's CodeFrame renders at
// (widgets.DefaultCodeFrameWidth) — so the header's right-aligned badges
// (issuePageBadges) land at a consistent column across every monitor
// command's human output, not a width this file invents independently.
const issuePageWidth = widgets.DefaultCodeFrameWidth

// writeIssuePageHuman renders explain.Context as the mockup's "página del
// issue": CULPRIT (with snippet), CAUSES, STACK, IMPACT, TOUCHED, and NEXT,
// each degrading to an explicit "skipped: <detail>" line instead of a blank
// section.
func writeIssuePageHuman(w io.Writer, c *explain.Context) error {
	badges := issuePageBadges(c)
	left := issuePageHeaderLeft(c.Issue.ShortID, displayIssueValue(c.Issue.Title), badges, issuePageWidth)
	if _, err := fmt.Fprintln(w, padHeaderLine(left, badges, issuePageWidth)); err != nil {
		return err
	}
	if _, err := fmt.Fprintf(w, "%s · first seen %s · last seen %s · %d event(s)%s\n\n",
		projectServiceLabel(c.Issue), relSince(c.Timeline.FirstSeen), relSince(c.Timeline.LastSeen),
		c.Timeline.Occurrences, timeSourceSuffix(c.Timeline.TimeSource)); err != nil {
		return err
	}

	if err := writeCulpritSection(w, c.Culprit); err != nil {
		return err
	}
	if err := writeCausesSection(w, c.Causes); err != nil {
		return err
	}
	if err := writeStackSection(w, c); err != nil {
		return err
	}
	if err := writeImpactSection(w, c.Impact); err != nil {
		return err
	}
	if err := writeTouchedSection(w, c.LastTouched); err != nil {
		return err
	}
	// Only degraded entries NOT already surfaced inline above (IMPACT and
	// TOUCHED each print their own "skipped: <detail>" line when their
	// dependency is unhealthy) are worth a separate line here -- otherwise
	// a codemap/git problem would print twice, once under its own section
	// and once more under a generic "DEGRADED" heading. An entry like
	// "vecgrep" (informational alongside a successful git_grep culprit) or
	// "message_search"/"snippet" (skipped, with no other section to carry
	// it) has nowhere else to appear, so those still get printed.
	if err := writeDegradedSection(w, extraDegraded(c)); err != nil {
		return err
	}
	return writeNextSection(w, c.Next)
}

// extraDegraded returns c.Degraded minus any entry whose component is
// already fully represented by IMPACT or TOUCHED's own inline "skipped:"
// line (same detail text) -- see writeIssuePageHuman's comment above.
func extraDegraded(c *explain.Context) []explain.Degraded {
	extra := make([]explain.Degraded, 0, len(c.Degraded))
	for _, d := range c.Degraded {
		if d.Component == "codemap" && c.Impact.Status != explain.SectionOK && c.Impact.Detail == d.Detail {
			continue
		}
		if d.Component == "git" && c.LastTouched.Status != explain.SectionOK && c.LastTouched.Detail == d.Detail {
			continue
		}
		extra = append(extra, d)
	}
	return extra
}

func projectServiceLabel(issue explain.IssueSummary) string {
	if issue.Service != "" {
		return issue.Project + " / " + issue.Service
	}
	return issue.Project
}

// issuePageHeaderLeft builds "SHORTID  Title", truncating Title (with an
// ellipsis — truncateDisplay) so that left, padHeaderLine's own minimum
// 2-space gap, and badges together never exceed width — the fix for the
// polish review's "the new issue header pads badges after the title but
// never truncates it" finding: a long crash message used to push badges
// well past 100 columns (one measured 254) instead of the roadmap mockup's
// own compact, title-truncated shape ("Error: node workload: intentional
// uncaught... js/workload.js:49"-style truncation, just applied to the
// header's own title instead). A short title that already fits is
// returned unchanged.
func issuePageHeaderLeft(shortID, title, badges string, width int) string {
	prefix := shortID + "  "
	budget := width - len([]rune(prefix)) - 2 // padHeaderLine's own minimum gap.
	if badges != "" {
		budget -= len([]rune(badges))
	}
	if budget < 1 {
		budget = 1
	}
	return prefix + truncateDisplay(title, budget)
}

// padHeaderLine right-aligns right against left, padded with spaces to
// width total columns — the roadmap mockup's own header shape ("91F3
// ValueError: bad row <n>                    regressed · error ·
// handled"), mirroring widgets.CodeFrame.renderHeader's own left/pad/right
// layout (there padded with '-' instead of ' '). A left (or right) long
// enough that width would go negative just gets one space of separation
// instead of overlapping or a negative Repeat count. right == "" returns
// left unchanged (nothing to align).
func padHeaderLine(left, right string, width int) string {
	if right == "" {
		return left
	}
	gap := width - len([]rune(left)) - len([]rune(right))
	if gap < 2 {
		gap = 2
	}
	return left + strings.Repeat(" ", gap) + right
}

// issuePageStatusWord is the issue page header's own status word — lower-
// case "regressed"/"new" (issueLabel's own convention, reused here so the
// list and the page never disagree about what counts as new/regressed),
// matching the roadmap mockup's own second example ("C4E0 ... open ·
// error · handled") for an ordinary open issue.
//
// A store Status other than "open" (resolved/ignored) is ALWAYS shown —
// never silently replaced by "new"/"regressed", which used to make a
// resolved or ignored issue's page read as if it were still open (the
// polish review's "the issue page header no longer shows the issue's
// actual state" finding: `monitor issues resolve` then `monitor issue`
// showed "new" with no mention of resolved anywhere on the page). When
// BOTH apply — a resolved/ignored issue that was also first seen recently,
// or reopened — both show, status first: "resolved · new" or "ignored ·
// regressed".
func issuePageStatusWord(c *explain.Context) string {
	now := time.Now()
	var label string
	switch {
	case c.Timeline.Reopened > 0:
		label = "regressed"
	case !c.Timeline.FirstSeen.IsZero() && now.Sub(c.Timeline.FirstSeen) < newIssueWindow:
		label = "new"
	}
	status := c.Issue.Status
	switch {
	case status != "" && status != string(issues.StatusOpen) && label != "":
		return status + " · " + label
	case status != "" && status != string(issues.StatusOpen):
		return status
	case label != "":
		return label
	default:
		return status // "open" (the mockup's second example), or "" if genuinely unknown.
	}
}

// issuePageBadges renders the issue page header's right-hand side — the
// roadmap mockup §4's "regressed · error · handled": a status word
// (issuePageStatusWord), the exception Level when known ("fatal"/"error"/
// "warning"), falling back to Kind when there is no Level (a non-exception
// issue kind such as "alert"/"investigation", or an older exception issue
// with no persisted level — never dropping the issue's kind from the page
// entirely, per the polish review's own finding), and "handled"/
// "unhandled" when Handled is known (nil — no handled/unhandled fact
// recorded at all — omits it, never guesses).
func issuePageBadges(c *explain.Context) string {
	parts := make([]string, 0, 3)
	if s := issuePageStatusWord(c); s != "" {
		parts = append(parts, s)
	}
	switch {
	case c.Issue.Level != "":
		parts = append(parts, c.Issue.Level)
	case c.Issue.Kind != "":
		parts = append(parts, c.Issue.Kind)
	}
	if c.Issue.Handled != nil {
		if *c.Issue.Handled {
			parts = append(parts, "handled")
		} else {
			parts = append(parts, "unhandled")
		}
	}
	return strings.Join(parts, " · ")
}

// relSince renders a "just now" / "<N><unit> ago" relative-time phrase —
// the fix for the review's "first now ago · last now ago" finding:
// humanDuration's own "now" bucket (anything under a minute) reads fine
// stand-alone in the issues-list LAST column, but turns into the nonsense
// "now ago" once " ago" is appended after it, as the issue page's first/
// last-seen line (and TOUCHED's own relative age) used to do.
func relSince(t time.Time) string {
	d := time.Since(t)
	if d < 0 {
		d = 0
	}
	if d < time.Minute {
		return "just now"
	}
	return humanDuration(d) + " ago"
}

// timeSourceSuffix renders Timeline.TimeSource's honest annotation for the
// issue page header — " (times from log lines)" when TimeSource is "line"
// (the roadmap mockup's own wording, §4: "3 events (times from log
// lines)"). "" for every other value: "unknown" (this build's current
// placeholder for every issue — see Timeline.TimeSource's own doc comment)
// and "mtime"/"live" have no mockup wording of their own yet, so this stays
// silent rather than inventing one.
func timeSourceSuffix(ts string) string {
	if ts == "line" {
		return " (times from log lines)"
	}
	return ""
}

func writeCulpritSection(w io.Writer, culprit *explain.CulpritInfo) error {
	if culprit == nil {
		_, err := fmt.Fprintln(w, "CULPRIT  none in this chain (see message-search fallback status below)")
		return err
	}
	loc := culprit.File
	if culprit.Line > 0 {
		loc = fmt.Sprintf("%s:%d", culprit.File, culprit.Line)
	}
	suffix := ""
	if culprit.Source == "message_search" {
		suffix = fmt.Sprintf("  inferred from message · via %s · confidence %s", culprit.Via, culprit.Confidence)
	}
	fn := ""
	if culprit.Function != "" {
		fn = fmt.Sprintf(" in %s()", culprit.Function)
	}
	if _, err := fmt.Fprintf(w, "CULPRIT  %s%s%s\n", loc, fn, suffix); err != nil {
		return err
	}
	if culprit.Snippet == nil {
		return nil
	}
	for i, line := range culprit.Snippet.Lines {
		n := culprit.Snippet.Start + i
		marker := "  "
		if n == culprit.Snippet.Highlight {
			marker = "> "
		}
		if _, err := fmt.Fprintf(w, "%s%4d | %s\n", marker, n, line); err != nil {
			return err
		}
	}
	if culprit.Snippet.Stale {
		_, err := fmt.Fprintln(w, "         (snippet may be stale: touched after this issue was first recorded)")
		return err
	}
	return nil
}

func writeCausesSection(w io.Writer, causes []explain.CauseEntry) error {
	if len(causes) == 0 {
		return nil
	}
	for i, cause := range causes {
		role := "outer"
		if i == len(causes)-1 {
			role = "innermost cause"
		}
		if cause.Culprit != nil {
			_, err := fmt.Fprintf(w, "CAUSES   %s          %s · %s:%d in %s()\n", cause.Type, role, cause.Culprit.File, cause.Culprit.Line, cause.Culprit.Function)
			if err != nil {
				return err
			}
			continue
		}
		if _, err := fmt.Fprintf(w, "CAUSES   %s          %s\n", cause.Type, role); err != nil {
			return err
		}
	}
	return nil
}

func writeStackSection(w io.Writer, c *explain.Context) error {
	_, err := fmt.Fprintf(w, "STACK    in-app %d%s\n", len(c.Frames), collapsedSuffix(c.Truncated.Frames))
	return err
}

func collapsedSuffix(n int) string {
	if n <= 0 {
		return ""
	}
	return fmt.Sprintf(" · %d more collapsed (--json for the full list)", n)
}

func writeImpactSection(w io.Writer, impact explain.ImpactInfo) error {
	if impact.Status != explain.SectionOK {
		return writeSkippedLine(w, "IMPACT", impact.Detail, impact.Recovery)
	}
	_, err := fmt.Fprintf(w, "IMPACT   %d caller(s) · blast %d · %d test(s) · call_graph=%s\n",
		impact.Callers, impact.BlastRadius, impact.Tests, displayIssueValue(impact.CallGraph))
	return err
}

func writeTouchedSection(w io.Writer, lt explain.LastTouched) error {
	if lt.Status != explain.SectionOK {
		return writeSkippedLine(w, "TOUCHED", lt.Detail, lt.Recovery)
	}
	when := ""
	if lt.AuthorTime != nil {
		when = "  " + relSince(*lt.AuthorTime)
	}
	_, err := fmt.Fprintf(w, "TOUCHED  %s \"%s\"%s   (local git blame; last touched, not suspect)\n", shortCommitSHA(lt.SHA), lt.Subject, when)
	return err
}

func writeDegradedSection(w io.Writer, degraded []explain.Degraded) error {
	for _, d := range degraded {
		label := strings.ToUpper(d.Component)
		if err := writeSkippedLine(w, label, d.Detail, d.Recovery); err != nil {
			return err
		}
	}
	return nil
}

func writeSkippedLine(w io.Writer, label, detail, recovery string) error {
	if _, err := fmt.Fprintf(w, "%-8s skipped: %s\n", label, detail); err != nil {
		return err
	}
	if recovery == "" {
		return nil
	}
	_, err := fmt.Fprintf(w, "%-8s recovery: %s\n", "", recovery)
	return err
}

func writeNextSection(w io.Writer, next []explain.NextAction) error {
	for i, n := range next {
		label := "NEXT"
		if i > 0 {
			label = ""
		}
		cmdText := n.CLI
		if cmdText == "" {
			cmdText = n.MCP
		}
		if _, err := fmt.Fprintf(w, "%-8s %-40s %s\n", label, cmdText, n.Why); err != nil {
			return err
		}
	}
	return nil
}

func shortCommitSHA(sha string) string {
	if len(sha) > 10 {
		return sha[:10]
	}
	return sha
}
