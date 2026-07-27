package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/issues"
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

func newIssuesCmd() *cobra.Command {
	var storePath string
	cmd := &cobra.Command{
		Use:          "issues <subcommand>",
		Aliases:      []string{"issue"},
		Short:        "List and manage durable grouped issues",
		SilenceUsage: true,
	}
	cmd.PersistentFlags().StringVar(&storePath, "store", "", "issue store path (default: $MONITOR_ISSUES_STORE or XDG data dir)")
	cmd.AddCommand(
		newIssuesListCmd(&storePath),
		newIssuesShowCmd(&storePath),
		newIssueStatusCmd(&storePath, "resolve", issues.StatusResolved),
		newIssueStatusCmd(&storePath, "reopen", issues.StatusOpen),
		newIssueStatusCmd(&storePath, "ignore", issues.StatusIgnored),
	)
	return cmd
}

func newIssuesListCmd(storePath *string) *cobra.Command {
	var (
		statuses []string
		project  string
		service  string
		limit    int
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
			path, err := resolveIssueStorePath(*storePath)
			if err != nil {
				return &issueCommandError{action: "list", err: err}
			}
			store, err := issues.OpenReadOnly(path)
			if err != nil {
				return &issueCommandError{action: "list", err: err}
			}
			entries, listErr := store.List(issues.ListOptions{
				Statuses: parsedStatuses,
				Project:  strings.TrimSpace(project),
				Service:  strings.TrimSpace(service),
				Limit:    limit,
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
			if JSONOutput(cmd) {
				return writeIssueJSON(cmd.OutOrStdout(), entries)
			}
			return writeIssueList(cmd.OutOrStdout(), entries)
		},
	}
	cmd.Flags().StringSliceVar(&statuses, "status", nil, "filter by status (open, resolved, ignored; repeatable)")
	cmd.Flags().StringVar(&project, "project", "", "filter by project")
	cmd.Flags().StringVar(&service, "service", "", "filter by service")
	cmd.Flags().IntVar(&limit, "limit", defaultIssueListLimit, "maximum issues to return (1-200)")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
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
				Issue:                issue,
				Occurrences:          occurrences,
				OccurrencesTruncated: issue.OccurrenceCount > int64(len(occurrences)),
			}
			if JSONOutput(cmd) {
				return writeIssueJSON(cmd.OutOrStdout(), out)
			}
			return writeIssueDetail(cmd.OutOrStdout(), out)
		},
	}
	cmd.Flags().IntVar(&occurrenceLimit, "occurrences", defaultOccurrenceListLimit, "maximum occurrences to return (1-200)")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
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
			store, err := issues.OpenStore(path)
			if err != nil {
				return writeIssueError(cmd, action, id, err)
			}
			var updated issues.Issue
			switch status {
			case issues.StatusResolved:
				updated, err = store.Resolve(id)
			case issues.StatusOpen:
				updated, err = store.Reopen(id)
			case issues.StatusIgnored:
				updated, err = store.Ignore(id)
			default:
				err = fmt.Errorf("unsupported target status %q", status)
			}
			closeErr := store.Close()
			if err != nil {
				return writeIssueError(cmd, action, id, err)
			}
			if closeErr != nil {
				return writeIssueError(cmd, action, id, closeErr)
			}
			if JSONOutput(cmd) {
				return writeIssueJSON(cmd.OutOrStdout(), issueMutationOutput{Updated: true, Issue: updated})
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s is %s.\n", updated.ID, updated.Status)
			return err
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
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

func writeIssueList(w io.Writer, entries []issues.Issue) error {
	if len(entries) == 0 {
		_, err := fmt.Fprintln(w, "No issues found.")
		return err
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ID\tSTATUS\tSEVERITY\tLAST SEEN\tCOUNT\tSERVICE\tTITLE"); err != nil {
		return err
	}
	for _, issue := range entries {
		if _, err := fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\t%s\n",
			issue.ID, issue.Status, displayIssueValue(issue.Severity),
			issue.LastSeen.Format(time.RFC3339), issue.OccurrenceCount,
			displayIssueValue(issue.Service), displayIssueValue(firstNonEmpty(issue.Title, issue.Message))); err != nil {
			return err
		}
	}
	return tw.Flush()
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
