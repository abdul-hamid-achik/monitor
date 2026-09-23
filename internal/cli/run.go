package cli

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/devrun"
	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
)

// newRunCmd is `monitor run`, in dual dispatch (docs/contracts/
// local-sentry-naming.md's "launch verb" row; E2.4):
//
//   - `monitor run <spec.yml>` (no `--`): the LEGACY glyphrun spec runner,
//     unchanged -- it alone shells out to `glyph run` and exports MONITOR=1
//     / MONITOR_RUN_DIR (ecosystem.RunGlyphrun), which glyphrun and
//     cairntrace both branch on.
//   - `monitor run [flags] -- <cmd> [args...]` (a `--` present, detected via
//     cmd.ArgsLenAtDash()): launches and monitors cmd via internal/devrun,
//     exporting only MONITOR_LAUNCH_ID/SERVICE/ROOT.
//
// Both modes share one command (and therefore one `run` entry in --help)
// rather than a second verb, per the naming ADR's explicit rejection of
// `monitor exec`/`monitor launch`.
func newRunCmd() *cobra.Command {
	var (
		name         string
		projectFlag  string
		scan         string
		quiet        bool
		noIssues     bool
		redactEnv    []string
		noSourceMaps bool
		store        string
	)
	cmd := &cobra.Command{
		Use:   "run <glyphrun-spec> | run [flags] -- <cmd> [args...]",
		Short: "Run a glyphrun spec, or launch and monitor a command for crashes",
		Long: `run is two commands sharing one verb, distinguished by whether a "--"
is present:

  monitor run <spec.yml>
      The legacy glyphrun behavioral-spec runner: shells out to
      "glyph run" and exports MONITOR=1 / MONITOR_RUN_DIR, which
      glyphrun and cairntrace both branch on. Unchanged by the mode
      below.

  monitor run [flags] -- <cmd> [args...]
      Launches cmd with its stdin inherited, copies its stdout/stderr
      to this terminal untouched, and -- for whichever stream --scan
      names -- also feeds a copy to the stack-trace detector: an
      uncaught exception or a caught-and-printed error becomes a
      durable, grouped issue with a file:line culprit, without any
      SDK. The copy to the terminal never waits on the detector or the
      issues store: a full internal buffer drops lines and counts them
      rather than ever slowing the monitored command down.

      Exports ONLY MONITOR_LAUNCH_ID, MONITOR_LAUNCH_SERVICE and
      MONITOR_LAUNCH_ROOT -- never MONITOR=1, MONITOR_SERVICE, or
      MONITOR_RUN_ID, which belong to the legacy spec runner and to
      internal/contextids respectively. cmd's own exit code (or
      128+signal, if it was killed by one) becomes monitor's exit
      code.`,
		Args: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() < 0 {
				return cobra.ExactArgs(1)(cmd, args)
			}
			if cmd.ArgsLenAtDash() != 0 {
				return fmt.Errorf("flags must come before -- (found %d positional argument(s) before --)", cmd.ArgsLenAtDash())
			}
			if len(args) == 0 {
				return errors.New(`"monitor run --" requires a command after --`)
			}
			return nil
		},
		RunE: func(cmd *cobra.Command, args []string) error {
			if cmd.ArgsLenAtDash() < 0 {
				// Legacy: monitor run <spec.yml>. Unchanged from before
				// this command grew a second mode.
				out, err := ecosystem.RunGlyphrun(context.Background(), args[0])
				if err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), string(out))
				return nil
			}

			// monitor run -- <cmd> [args...]. context.Background(), not
			// this package's Context() helper: Context() cancels on
			// SIGINT/SIGTERM, and exec.CommandContext kills its process on
			// ctx cancellation -- devrun.Run already owns SIGINT/SIGTERM/
			// SIGHUP handling itself (the TTY-shared-vs-Setpgid rule), so a
			// second, independent cancellation path here would race it and
			// could abruptly kill the child instead of forwarding the
			// signal the way the roadmap's process-group contract requires.
			result, err := devrun.Run(context.Background(), devrun.Options{
				Argv:           args,
				Scan:           scan,
				Name:           name,
				Project:        projectFlag,
				Quiet:          quiet,
				NoIssues:       noIssues,
				RedactEnvNames: redactEnv,
				NoSourceMaps:   noSourceMaps,
				StorePath:      store,
			})
			if err != nil {
				return err
			}
			// devrun.Run already wrote every banner/passthrough byte
			// directly to the real terminal streams; propagate the CHILD's
			// own exit code (docs/contracts/local-sentry-naming.md's "se
			// propaga el exit code" rule) rather than cobra's default
			// "any non-nil error becomes exit 1" -- matching the existing
			// os.Exit(N)-from-RunE precedent in internal/cli/resolve.go
			// for a command whose meaningful exit codes aren't 0-or-1.
			if result.ExitCode != 0 {
				os.Exit(result.ExitCode)
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "service name for MONITOR_LAUNCH_SERVICE and the launch banner (default: derived)")
	cmd.Flags().StringVar(&projectFlag, "project", "", "project override for issue grouping (default: resolved from the command's git root)")
	cmd.Flags().StringVar(&scan, "scan", devrun.ScanStderr, "which stream(s) feed the crash detector: stderr, stdout, or both")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "suppress the start/NEW/again banners (the exit summary still prints when lines were dropped)")
	cmd.Flags().BoolVar(&noIssues, "no-issues", false, "pass the command's output through untouched, but never detect or record issues")
	cmd.Flags().StringSliceVar(&redactEnv, "redact-env", nil, "additional environment variable NAMEs to redact from recorded exception text")
	cmd.Flags().BoolVar(&noSourceMaps, "no-source-maps", false, "do not append --enable-source-maps to NODE_OPTIONS")
	cmd.Flags().StringVar(&store, "store", "", "issue store path (default: $MONITOR_ISSUES_STORE or XDG data dir)")
	return cmd
}
