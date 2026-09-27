package cli

import (
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/reload"
	"github.com/abdul-hamid-achik/monitor/internal/ui/studio"
	"github.com/abdul-hamid-achik/monitor/internal/ui/tuistudio"
)

// newStudioCmd returns the `monitor studio` subcommand, which launches the
// interactive TUI (formerly the default of bare `monitor`, and formerly the
// `v2` subcommand). Bare `monitor` now prints help instead.
func newStudioCmd() *cobra.Command {
	var (
		reloadServer bool
		reloadAddr   string
		tuimarkFront bool
		fixtureMode  bool
		dumpSize     string
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

		Use 1-9 to jump between tabs, / to search processes, and q to quit.

EXPERIMENTAL: --tuimark renders the same 9 tabs over the same data sources
(internal/collector, internal/kill, internal/config, internal/history,
internal/temperature) through Tuimark (github.com/abdul-hamid-achik/tuimark)
instead of Bubble Tea. It is additive and off by default.

  monitor studio --tuimark                  # the Tuimark front-end
  monitor studio --tuimark --dump 120x30    # print one frame as JSON and exit
  monitor studio --tuimark --fixture        # deterministic offline data, for testing`,
		RunE: func(cmd *cobra.Command, args []string) error {
			if !tuimarkFront && (fixtureMode || dumpSize != "") {
				return fmt.Errorf("--fixture and --dump only apply together with --tuimark")
			}
			if tuimarkFront {
				return runTuimarkStudio(tuimarkOptions{
					reloadServer: reloadServer, reloadAddr: reloadAddr,
					fixture: fixtureMode, dumpSize: dumpSize,
				})
			}

			programReloader := studio.NewProgramReloader()
			// Optionally expose POST /reload so external processes (CI / agents)
			// can trigger an in-process refresh via `monitor reload`.
			if reloadServer {
				srv := reload.NewServer(reloadAddr, programReloader)
				if err := srv.Start(); err != nil {
					fmt.Fprintf(os.Stderr, "Warning: --reload-server failed to start on %s: %v\n", reloadAddr, err)
				} else {
					fmt.Fprintf(os.Stderr, "monitor: /reload endpoint listening on http://%s\n", srv.Addr())
				}
			}
			if err := studio.RunWithOptions(studio.Options{
				DisableTemperatureSource: disableTemperatureSource,
				Reloader:                 programReloader,
			}); err != nil {
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
	// See internal/cli/hot.go's TestHotCommandSilencesOwnErrors: cobra's own
	// ExecuteC prints "Error: ..." unless this specific command silences it,
	// and cli.Execute() (root.go) always prints its own on top of that.
	// studio's RunE can now fail validation (--fixture/--dump without
	// --tuimark), so it needs the same convention every other command with a
	// failing RunE follows.
	cmd.SilenceErrors = true
	cmd.Flags().BoolVar(&reloadServer, "reload-server", false,
		"start a localhost HTTP /reload endpoint alongside the TUI")
	cmd.Flags().StringVar(&reloadAddr, "reload-addr", reload.DefaultAddr,
		"address for the /reload endpoint when --reload-server is set")
	cmd.Flags().BoolVar(&tuimarkFront, "tuimark", false,
		"EXPERIMENTAL: render Studio with Tuimark instead of Bubble Tea")
	cmd.Flags().BoolVar(&fixtureMode, "fixture", false,
		"with --tuimark, use deterministic offline data instead of the live host (never touches ~/.config/monitor/config.json, the history store, or a real process)")
	cmd.Flags().StringVar(&dumpSize, "dump", "",
		"with --tuimark, print Dump() JSON of the first frame at COLSxROWS and exit")
	return cmd
}

type tuimarkOptions struct {
	reloadServer bool
	reloadAddr   string
	fixture      bool
	dumpSize     string
}

// runTuimarkStudio launches the experimental Tuimark front-end, or, with
// dumpSize set, prints one frame as JSON and exits — the deterministic test
// hook `monitor studio --tuimark --dump COLSxROWS` (pair with --fixture for
// a result that never depends on the host it runs on).
func runTuimarkStudio(opts tuimarkOptions) error {
	if opts.dumpSize != "" {
		var cols, rows int
		if _, err := fmt.Sscanf(opts.dumpSize, "%dx%d", &cols, &rows); err != nil {
			return fmt.Errorf("--dump wants COLSxROWS, got %q", opts.dumpSize)
		}
		d, err := tuistudio.Dump(tuistudio.Options{Fixture: opts.fixture}, cols, rows)
		if err != nil {
			return err
		}
		return WriteJSON(d)
	}

	reloader := tuistudio.NewReloader()
	if opts.reloadServer {
		srv := reload.NewServer(opts.reloadAddr, reloader)
		if err := srv.Start(); err != nil {
			fmt.Fprintf(os.Stderr, "Warning: --reload-server failed to start on %s: %v\n", opts.reloadAddr, err)
		} else {
			fmt.Fprintf(os.Stderr, "monitor: /reload endpoint listening on http://%s\n", srv.Addr())
		}
	}
	if err := tuistudio.RunWithOptions(tuistudio.Options{
		DisableTemperatureSource: disableTemperatureSource,
		Reloader:                 reloader,
		Fixture:                  opts.fixture,
	}); err != nil {
		if opts.reloadServer {
			blockOnSignal()
		}
		return nil
	}
	return nil
}

// blockOnSignal blocks until SIGINT/SIGTERM, so a started reload server keeps
// serving in headless environments where the TUI can't claim a TTY.
func blockOnSignal() {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	<-sigCh
}
