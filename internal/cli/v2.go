package cli

import (
	"github.com/spf13/cobra"

	uiv2 "github.com/abdul-hamid-achik/monitor/internal/ui/v2"
)

// newV2Cmd returns the `monitor v2` subcommand. v2 is the only TUI and
// the default (bare `monitor` launches it); this subcommand is kept as
// an explicit alias.
func newV2Cmd() *cobra.Command {
	return &cobra.Command{
		Use:    "v2",
		Short:  "Launch the Bubble Tea v2 TUI (default)",
		Long:   "Launches the TUI (charm.land/bubbletea/v2 + charm.land/lipgloss/v2). This is the default when running bare `monitor`. All 9 tabs (Overview, CPU, Memory, Temperature, Disk, Network, Processes, Settings, Trends) are rendered with full interactivity — keyboard + mouse, editable settings, and a Trends tab over the persistent metric history.",
		Hidden: false,
		RunE: func(cmd *cobra.Command, args []string) error {
			return uiv2.Run()
		},
	}
}
