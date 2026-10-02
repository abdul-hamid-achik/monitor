package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/sdk"
)

func newSDKCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "sdk",
		Short: "Locate monitor's own SDKs bundled in this binary",
		Long: `sdk writes the Node and Python SDKs bundled in this binary to
$XDG_STATE_HOME/monitor/sdk/<hash>/ (once per build) and prints where they
are, so an app can install them before they are published:

  npm install "$(monitor sdk path node)"
  pip install "$(monitor sdk path python)"

"monitor run --probes" loads the same copies without installing anything.
The Go SDK is a Go module: go get github.com/abdul-hamid-achik/monitor/sdk/go`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			paths, err := sdk.Materialize("")
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				return WriteJSON(paths)
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "node     %s\n", paths.Node)
			fmt.Fprintf(out, "python   %s\n", paths.Python)
			fmt.Fprintln(out, "go       github.com/abdul-hamid-achik/monitor/sdk/go")
			fmt.Fprintln(out, "auto     monitor run --probes -- <cmd>")
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "output as JSON")
	cmd.AddCommand(&cobra.Command{
		Use:       "path <node|python>",
		Short:     "Print one bundled SDK's directory (for npm install / pip install)",
		Args:      cobra.ExactArgs(1),
		ValidArgs: []string{"node", "python"},
		RunE: func(cmd *cobra.Command, args []string) error {
			paths, err := sdk.Materialize("")
			if err != nil {
				return err
			}
			switch args[0] {
			case "node":
				fmt.Fprintln(cmd.OutOrStdout(), paths.Node)
			case "python":
				fmt.Fprintln(cmd.OutOrStdout(), paths.Python)
			default:
				return fmt.Errorf("unknown SDK %q: want node or python (Go: go get github.com/abdul-hamid-achik/monitor/sdk/go)", args[0])
			}
			return nil
		},
	})
	return cmd
}
