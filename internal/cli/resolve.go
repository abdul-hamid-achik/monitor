package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/procbind"
)

func newResolveCmd() *cobra.Command {
	var runtimeName, codebaseRoot, mainScriptSuffix string
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve one live process from safe identity selectors",
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtime := procbind.Runtime(runtimeName)
			switch runtime {
			case procbind.RuntimeUnknown, procbind.RuntimeNode, procbind.RuntimeBun,
				procbind.RuntimeDeno, procbind.RuntimeGo, procbind.RuntimePython:
			default:
				return fmt.Errorf("unsupported runtime %q", runtimeName)
			}
			ctx, cancel := Context()
			defer cancel()
			binding, err := procbind.Resolve(ctx, procbind.ResolveOptions{
				Runtime:          runtime,
				CodebaseRoot:     codebaseRoot,
				MainScriptSuffix: mainScriptSuffix,
			})
			if err != nil {
				return err
			}
			if JSONOutput(cmd) {
				return WriteJSON(binding)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "pid %d (%s) runtime=%s codebase=%s main=%s\n", binding.PID, binding.Name, binding.Runtime, binding.CodebaseRoot, binding.MainScript)
			return nil
		},
	}
	cmd.Flags().StringVar(&runtimeName, "runtime", "unknown", "runtime selector: node, bun, deno, go, python")
	cmd.Flags().StringVar(&codebaseRoot, "codebase-root", "", "exact detected codebase root")
	cmd.Flags().StringVar(&mainScriptSuffix, "main-script-suffix", "", "required suffix of the runtime entry script")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}
