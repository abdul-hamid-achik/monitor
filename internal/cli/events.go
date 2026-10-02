package cli

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/events"
	"github.com/abdul-hamid-achik/monitor/internal/issues"
)

// inboxDrainWait bounds how long an automatic inbox drain (monitor issues,
// monitor issue, monitor serve) waits for the issues writer lock: a read
// command must never stall behind a busy store; the events simply wait
// for the next drain.
const inboxDrainWait = time.Second

func newEventsCmd() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "events",
		Short: "Show the inbox monitor's own SDKs deliver events to",
		Long: `events shows the inbox monitor's own SDKs (sdk/node, sdk/python,
sdk/go) write monitor.event.v1 files to when no "monitor run --" launch
gave them a directory: $XDG_STATE_HOME/monitor/events/inbox.

"monitor issues" and "monitor issue" drain it into the default issue store
before they read, and "monitor serve" does while it runs (not with
--read-only). "monitor events drain" does it explicitly, for any store.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inbox, err := inboxOrFlag(dir)
			if err != nil {
				return err
			}
			pending, err := events.Pending(inbox)
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				return WriteJSON(map[string]any{"inbox": inbox, "pending": len(pending)})
			}
			fmt.Fprintf(cmd.OutOrStdout(), "inbox    %s\npending  %d event(s)\n", inbox, len(pending))
			if len(pending) > 0 {
				fmt.Fprintln(cmd.OutOrStdout(), "next     monitor events drain")
			}
			return nil
		},
	}
	cmd.PersistentFlags().StringVar(&dir, "dir", "", "events directory (default: the global inbox)")
	cmd.Flags().Bool("json", false, "output as JSON")
	cmd.AddCommand(newEventsDrainCmd(&dir))
	return cmd
}

func newEventsDrainCmd(dir *string) *cobra.Command {
	var (
		store string
		limit int
	)
	cmd := &cobra.Command{
		Use:   "drain",
		Short: "Record every pending SDK event into the issue store",
		Long: `drain records the pending monitor.event.v1 files in the inbox (or --dir)
into the issue store, oldest first, under one writer lock. Each event is
scrubbed, attributed to the project its working directory resolves to, and
grouped exactly like a crash parsed from process output; its file is
removed once the store has it. A file that can never be recorded is moved
to the directory's .rejected folder. Re-delivering an event folds into its
existing occurrence.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			inbox, err := inboxOrFlag(*dir)
			if err != nil {
				return err
			}
			path, err := issues.ResolvePath(store)
			if err != nil {
				return err
			}
			res, err := events.Ingest(context.Background(), events.IngestOptions{Dir: inbox, StorePath: path, Limit: limit})
			if err != nil {
				return fmt.Errorf("drain %s: %w", inbox, err)
			}
			if JSONOutput(cmd) {
				return WriteJSON(res)
			}
			fmt.Fprintln(cmd.OutOrStdout(), drainSummary(res))
			return nil
		},
	}
	cmd.Flags().StringVar(&store, "store", "", "issue store path (default: $MONITOR_ISSUES_STORE or XDG data dir)")
	cmd.Flags().IntVar(&limit, "limit", events.DefaultLimit, "most events to record in one drain")
	cmd.Flags().Bool("json", false, "output as JSON")
	return cmd
}

func inboxOrFlag(dir string) (string, error) {
	if d := strings.TrimSpace(dir); d != "" {
		return d, nil
	}
	return events.InboxDir()
}

func drainSummary(res events.IngestResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "recorded %d event(s) into %d issue(s)", res.Recorded, len(res.IssueIDs))
	if res.Deduped > 0 {
		fmt.Fprintf(&b, " · %d already recorded", res.Deduped)
	}
	if res.Rejected > 0 {
		fmt.Fprintf(&b, " · %d rejected (see .rejected/)", res.Rejected)
	}
	if res.Remaining > 0 {
		fmt.Fprintf(&b, " · %d still pending (run again)", res.Remaining)
	}
	return b.String()
}

// drainInboxIntoDefaultStore records pending SDK events from the global
// inbox before a read, best-effort and quietly. It only ever targets the
// DEFAULT store: the inbox's events belong to no particular store, and a
// command pointed at another one (--store, MONITOR_ISSUES_STORE: a test,
// a spec, a scratch store) must never pull the user's real events into it.
func drainInboxIntoDefaultStore(ctx context.Context, storeFlag string) {
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(storeFlag) != "" || strings.TrimSpace(os.Getenv(issues.StorePathEnv)) != "" {
		return
	}
	inbox, err := events.InboxDir()
	if err != nil {
		return
	}
	if pending, err := events.Pending(inbox); err != nil || len(pending) == 0 {
		return
	}
	path, err := issues.ResolvePath("")
	if err != nil {
		return
	}
	_, _ = events.Ingest(ctx, events.IngestOptions{Dir: inbox, StorePath: path, Wait: inboxDrainWait})
}
