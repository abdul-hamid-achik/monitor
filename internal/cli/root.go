package cli

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

// Version is set by goreleaser at build time.
var Version = "dev"

// Root is the top-level cobra command for `monitor`.
func Root() *cobra.Command {
	root := &cobra.Command{
		Use:   "monitor",
		Short: "Local-first observability tool for macOS",
		Long: `Monitor is a terminal-based, agent-harnessable system monitor for macOS.

It runs as an interactive TUI by default, and exposes its data to scripts,
agents, and other tools via JSON CLI commands and an MCP stdio server.

When the monitor launches a child process or spec, it sets MONITOR=1 and
MONITOR_RUN_DIR=<dir> so the child can detect it is being observed.`,
		SilenceUsage:  true,
		SilenceErrors: false,
		Version:       Version,
	}

	root.SetVersionTemplate("monitor {{.Version}}\n")
	root.SetIn(os.Stdin)
	root.SetOut(os.Stdout)
	root.SetErr(os.Stderr)

	root.PersistentFlags().BoolVar(&disableTemperatureSource, "no-temperature-source", false,
		"skip powermetrics; use the CPU-load estimation fallback (no sudo required)")

	root.AddCommand(
		newSnapshotCmd(),
		newWatchCmd(),
		newProcessCmd(),
		newKillCmd(),
		newProfileCmd(),
		newInvestigateCmd(),
		newStashCmd(),
		newIncidentsCmd(),
		newLogsCmd(),
		newDoctorCmd(),
		newRunCmd(),
		newReloadCmd(),
		newMCPCmd(),
		newV2Cmd(),
		newVaultCmd(),
	)

	return root
}

// Execute runs the root command and exits with a non-zero status on failure.
func Execute() {
	if err := Root().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "Error:", err)
		os.Exit(1)
	}
}