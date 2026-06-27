package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/logger"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

func newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Print ecosystem health and tool availability",
		RunE: func(cmd *cobra.Command, _ []string) error {
			ctx, cancel := Context()
			defer cancel()
			status := ecosystem.Probe(ctx)
			if JSONOutput(cmd) {
				return WriteJSON(status)
			}
			fmt.Println("Ecosystem tools:")
			fmt.Println("  codemap     :", status.Codemap)
			fmt.Println("  fcheap      :", status.Fcheap)
			fmt.Println("  vecgrep     :", status.Vecgrep)
			fmt.Println("  tinyvault   :", status.Tinyvault)
			fmt.Println("  vidtrace    :", status.Vidtrace)
			fmt.Println("  glyphrun    :", status.Glyphrun)
			fmt.Println("  cairntrace  :", status.Cairntrace)
			fmt.Println("  veclite     :", status.Veclite)
			fmt.Println("  tmux        :", status.Tmux)
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

func newRunCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run <glyphrun-spec>",
		Short: "Run a glyphrun behavioral spec against monitored services",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			out, err := ecosystem.RunGlyphrun(context.Background(), args[0])
			if err != nil {
				return err
			}
			fmt.Println(string(out))
			return nil
		},
	}
	return cmd
}

// openLogStore wires up a veclite-backed log store with shared-read so CLI
// tools can search while the TUI holds the writer lock.
func openLogStore(path string) (*logger.Store, error) {
	_ = profiler.Capture // ensure import kept when profiler file is empty
	return logger.OpenStore(path)
}