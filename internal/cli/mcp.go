package cli

import (
	"context"
	"fmt"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/kill"
	"github.com/abdul-hamid-achik/monitor/internal/mcp"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

func newMCPCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "mcp <subcommand>",
		Short: "Run an MCP stdio server exposing monitor data",
	}
	cmd.AddCommand(newMCPServeCmd())
	return cmd
}

func newMCPServeCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the MCP stdio server (newline-delimited JSON-RPC)",
		RunE: func(cmd *cobra.Command, _ []string) error {
			c := NewCollector(0)
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			svc := &mcp.Service{
				// Read tool: latest snapshot.
				Snapshots: func() collector.SystemInfo {
					return c.Collect(ctx)
				},
				// Mutating tools: thin wrappers over the CLI's existing logic.
				Kill: func(pid int32, force bool) error {
					return kill.Kill(pid, force)
				},
				Profile: func(ctx context.Context, pid int32, ptype profiler.ProfileType) (profiler.Profile, error) {
					// MCP profiling uses the default pprof address; the CLI
					// `--pprof-addr` flag covers non-default ports.
					return profiler.Capture(ctx, pid, ptype, "")
				},
				// Investigate runs the same real pipeline the CLI does
				// (snapshot + profile + correlate + stash).
				Investigate: func(ctx context.Context, pid int32) map[string]any {
					return investigatePipeline(ctx, pid, "7d", false)
				},
				// Record captures a short screen recording via the platform
				// recorder (screencapture / ffmpeg). Returns an error — turned
				// into a structured refusal by the handler — when no recorder
				// or display is available (headless agents).
				Record: func(ctx context.Context, pid int32, durationSeconds int) (string, error) {
					return ecosystem.RecordScreen(ctx, durationSeconds)
				},
			}
			s := mcp.NewServer(svc)
			if err := s.Run(ctx); err != nil {
				return fmt.Errorf("mcp serve: %w", err)
			}
			return nil
		},
	}
	return cmd
}
