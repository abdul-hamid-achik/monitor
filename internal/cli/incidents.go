package cli

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/incidents"
)

// newIncidentsCmd lists recent monitor incident stashes (fcheap-backed) and
// hosts the registry subcommands (pending, resume-stash). "incident" is an
// alias so `monitor incident resume-stash <id>` works verbatim.
func newIncidentsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "incidents",
		Aliases: []string{"incident"},
		Short:   "List and manage monitor incident stashes",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := Context()
			defer cancel()
			entries, err := incidents.Search(ctx, nil)
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				// Schema stability: bare `incidents --json` stays a plain
				// []StashListEntry array; the registry has its own
				// `incidents pending --json`.
				return WriteJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Println("No monitor incident stashes found.")
			} else {
				fmt.Printf("Recent incident stashes (%d):\n", len(entries))
				for _, e := range entries {
					fmt.Printf("  %s  %s  %s\n", e.CreatedAt, e.ID, e.Name)
				}
			}
			// Best-effort hint: surface bundles stuck in the local registry.
			if pending, perr := incidents.ListRegistry(); perr == nil && len(pending) > 0 {
				fmt.Printf("\n%d pending local bundle(s) awaiting archival — see `monitor incidents pending`\n", len(pending))
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	cmd.AddCommand(newIncidentsPendingCmd(), newIncidentsResumeCmd())
	return cmd
}

// newIncidentsPendingCmd lists bundles retained in the durable local
// registry because fcheap archival failed.
func newIncidentsPendingCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "pending",
		Short: "List local incident bundles awaiting fcheap archival",
		RunE: func(cmd *cobra.Command, _ []string) error {
			entries, err := incidents.ListRegistry()
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				if entries == nil {
					entries = []incidents.RegistryEntry{}
				}
				return WriteJSON(entries)
			}
			if len(entries) == 0 {
				fmt.Println("No pending incident bundles.")
				return nil
			}
			fmt.Printf("Pending incident bundles (%d):\n", len(entries))
			for _, e := range entries {
				fmt.Printf("  %s  %s  trigger=%s  attempts=%d  last_error=%s\n",
					e.CreatedAt.Format(time.RFC3339), e.ID, e.Trigger, e.Attempts, e.LastError)
			}
			fmt.Println("\nRetry with: monitor incidents resume-stash <id>")
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

// newIncidentsResumeCmd re-attempts fcheap archival of a bundle retained
// after a failed capture — the recovery path promised by Capture's note.
func newIncidentsResumeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "resume-stash <id|path>",
		Short: "Re-attempt fcheap archival of a locally retained incident bundle",
		Long: `resume-stash re-attempts fcheap archival for a bundle that could not
be archived when it was captured (fcheap missing, disk full, ...). Accepts a
registry ID from 'monitor incidents pending', a registry entry directory,
or a path to a bare retained bundle directory. On success the local copy
is removed; on failure the bundle is kept and the attempt is recorded.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx, cancel := Context()
			defer cancel()
			res, err := incidents.Resume(ctx, args[0])
			if err != nil {
				if JSONOutput(cmd) {
					_ = WriteJSON(map[string]any{"resumed": false, "id": args[0], "error": err.Error()})
				}
				return fmt.Errorf("resume-stash %s: %w", args[0], err)
			}
			if JSONOutput(cmd) {
				return WriteJSON(map[string]any{"resumed": true, "stash": res})
			}
			fmt.Printf("Archived bundle %s as stash %s (%s)\n", args[0], res.StashID, res.Path)
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}
