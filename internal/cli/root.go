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
		Short: "Local-first, agent-harnessable system monitor for macOS & Linux",
		Long: `Monitor is a terminal-based, agent-harnessable system monitor for macOS
and Linux.

Run the interactive TUI with 'monitor studio'. Every view is also a JSON CLI
command (snapshot, watch, process, tree, history, diff, ...) and an MCP stdio
server ('monitor mcp serve'), so scripts and agents get the same data.

When the monitor launches a child process or spec, it sets MONITOR=1 so the
child can detect it is being observed.`,
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
		newTelemetryCmd(),
		newAnalyzeCmd(),
		newProcessCmd(),
		newProcessesCmd(),
		newKillCmd(),
		newProfileCmd(),
		newInvestigateCmd(),
		newIssuesCmd(),
		newStashCmd(),
		newIncidentsCmd(),
		newLogsCmd(),
		newDoctorCmd(),
		newRunCmd(),
		newReloadCmd(),
		newMCPCmd(),
		newStudioCmd(),
		newVaultCmd(),
		newHistoryCmd(),
		newBaselineCmd(),
		newDiffCmd(),
		newTreeCmd(),
		newConfigCmd(),
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
