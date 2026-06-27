package cli

import (
	"github.com/spf13/cobra"

	uiv2 "github.com/abdul-hamid-achik/monitor/internal/ui/v2"
)

// newV2Cmd returns the `monitor v2` subcommand. Now that v2 is the
// default TUI (bare `monitor` launches v2), this subcommand is kept
// for explicit invocation. `monitor v1` launches the legacy TUI.
func newV2Cmd() *cobra.Command {
	return &cobra.Command{
		Use:    "v2",
		Short:  "Launch the Bubble Tea v2 TUI (default)",
		Long:   "Launches the v2 TUI (charm.land/bubbletea/v2 + charm.land/lipgloss/v2). This is the default when running bare `monitor`. All 8 tabs are rendered with full interactivity. Use `monitor v1` for the legacy TUI.",
		Hidden: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return uiv2.Run()
		},
	}
}