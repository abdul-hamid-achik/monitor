package cli

import (
	"context"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
)

// newVaultCmd returns the `monitor vault` subcommand which wraps a
// command with `tvault run` so secrets land in the child process's
// env without appearing in the agent context. The command and its
// args are passed through; tvault injects the secrets transparently.
//
// Usage:
//
//	monitor vault --project myapp -- /usr/local/bin/myapp --port 8080
//	monitor vault --project myapp -- env  # debug: see what env vars are injected
//
// The vault subcommand is a thin wrapper around ecosystem.TinyvaultRun
// which already shells out to `tvault run --project <p> -- <cmd>`.
func newVaultCmd() *cobra.Command {
	var project string
	cmd := &cobra.Command{
		Use:   "vault --project <name> -- command [args...]",
		Short: "Run a command with secrets injected via tinyvault",
		Long: `vault wraps a command with 'tvault run --project <p>' so
secrets from the named tinyvault project land in the child process's
environment without appearing in the agent context.

Usage:
  monitor vault --project myapp -- /usr/local/bin/myapp --port 8080
  monitor vault --project myapp -- env   # debug: show injected env

The command and its args (after the -- separator) are run under ` + "`env`" + `
via tvault run, so tvault resolves the project's secrets and injects
them as environment variables for the command; the agent calling this
subcommand never sees the secret values.

Requires: tvault binary on $PATH and a tinyvault project with secrets
configured for the given project name.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if project == "" {
				return fmt.Errorf("--project is required")
			}

			// Check tvault is available.
			if !tvaultAvailable() {
				return fmt.Errorf("tvault not found on $PATH; install tinyvault first")
			}

			ctx, cancel := Context()
			defer cancel()

			// The args after -- are the command + its args.
			// Cobra strips the -- so args[0] is the command, args[1:] are its args.
			output, err := ecosystem.TinyvaultRun(ctx, project, args...)
			if err != nil {
				return fmt.Errorf("tvault run: %w", err)
			}

			// Print the merged env (tvault run -- env prints the env).
			// For real service launches, the user passes the service binary
			// and tvault handles the exec; the output is the service's stdout.
			os.Stdout.Write(output)
			return nil
		},
	}
	cmd.Flags().StringVar(&project, "project", "", "tinyvault project name (required)")
	return cmd
}

// tvaultAvailable checks if the tvault binary is on $PATH.
func tvaultAvailable() bool {
	return ecosystem.Probe(context.Background()).Tinyvault.Available
}
