package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/reload"
	"github.com/abdul-hamid-achik/monitor/internal/ui/studio"
)

// newStudioCmd returns the `monitor studio` subcommand, which launches the
// interactive TUI (formerly the default of bare `monitor`, and formerly the
// `v2` subcommand). Bare `monitor` now prints help instead.
func newStudioCmd() *cobra.Command {
	var (
		reloadServer bool
		reloadAddr   string
	)
	cmd := &cobra.Command{
		Use:     "studio",
		Aliases: []string{"tui"},
		Short:   "Launch the interactive TUI (Studio)",
		Long: `studio launches Monitor's interactive terminal UI (charm.land/bubbletea/v2
+ charm.land/lipgloss/v2). All 9 tabs — Overview, CPU, Memory, Temperature,
Disk, Network, Processes, Settings, and Trends — are rendered with full
keyboard + mouse interactivity, editable settings, and a Trends tab over the
persistent metric history.

  monitor studio                 # launch the TUI
  monitor studio --reload-server # also expose POST /reload for agents/CI

Use 1-9 to jump between tabs, / to search processes, and q to quit.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			// Optionally expose POST /reload so external processes (CI / agents)
			// can trigger an in-process refresh via `monitor reload`.
			if reloadServer {
				srv := reload.NewServer(reloadAddr, reload.NoopReloader{})
				if err := srv.Start(); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: --reload-server failed to start on %s: %v\n", reloadAddr, err)
				} else {
					fmt.Fprintf(os.Stderr, "monitor: /reload endpoint listening on http://%s\n", srv.Addr())
				}
			}
			if err := studio.Run(); err != nil {
				// In headless environments the TUI can't claim a TTY; if the
				// reload server is up, keep serving until a signal so CI can
				// still exercise it. studio.Run already logged the error.
				if reloadServer {
					blockOnSignal()
				}
				return nil
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&reloadServer, "reload-server", false,
		"start a localhost HTTP /reload endpoint alongside the TUI")
	cmd.Flags().StringVar(&reloadAddr, "reload-addr", reload.DefaultAddr,
		"address for the /reload endpoint when --reload-server is set")
	return cmd
}

// blockOnSignal blocks until SIGINT/SIGTERM, so a started reload server keeps
// serving in headless environments where the TUI can't claim a TTY.
func blockOnSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}
