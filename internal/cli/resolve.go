package cli

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/procbind"
)

func newResolveCmd() *cobra.Command {
	var runtimeName, codebaseRoot, mainScriptSuffix string
	var descendantOf int32
	cmd := &cobra.Command{
		Use:   "resolve",
		Short: "Resolve one live process from safe identity selectors",
		RunE: func(cmd *cobra.Command, _ []string) error {
			runtime := procbind.Runtime(runtimeName)
			switch runtime {
			case procbind.RuntimeUnknown, procbind.RuntimeNode, procbind.RuntimeBun,
				procbind.RuntimeDeno, procbind.RuntimeGo, procbind.RuntimePython,
				procbind.RuntimeRuby:
			default:
				return fmt.Errorf("unsupported runtime %q", runtimeName)
			}
			ctx, cancel := Context()
			defer cancel()

			// --descendant-of with no OTHER selector runs leaf resolution:
			// walk descendants of the given pid (root itself first) and
			// return the first one that classifies as an application
			// runtime, skipping wrapper processes (a shell, yarn/npm/pnpm,
			// the `go run` toolchain, ...). --descendant-of combined with
			// another selector instead just restricts the ordinary
			// selector match to that pid's descendants — see
			// procbind.ResolveOptions.DescendantOf.
			if descendantOf != 0 && runtime == procbind.RuntimeUnknown && codebaseRoot == "" && mainScriptSuffix == "" {
				binding, candidates, err := procbind.ResolveLeaf(ctx, descendantOf, procbind.LeafOptions{})
				if err != nil {
					var ambiguous *procbind.AmbiguousLeafError
					if errors.As(err, &ambiguous) {
						printAmbiguousLeaf(cmd, descendantOf, candidates)
						os.Exit(2)
					}
					return err
				}
				return writeResolveResult(cmd, binding)
			}

			binding, err := procbind.Resolve(ctx, procbind.ResolveOptions{
				Runtime:          runtime,
				CodebaseRoot:     codebaseRoot,
				MainScriptSuffix: mainScriptSuffix,
				DescendantOf:     descendantOf,
			})
			if err != nil {
				return err
			}
			return writeResolveResult(cmd, binding)
		},
	}
	cmd.Flags().StringVar(&runtimeName, "runtime", "unknown", "runtime selector: node, bun, deno, go, python, ruby")
	cmd.Flags().StringVar(&codebaseRoot, "codebase-root", "", "exact detected codebase root")
	cmd.Flags().StringVar(&mainScriptSuffix, "main-script-suffix", "", "required suffix of the runtime entry script")
	cmd.Flags().Int32Var(&descendantOf, "descendant-of", 0, "restrict matching to descendants of this pid; alone, resolves the leaf runtime process under it")
	cmd.Flags().Bool("json", false, "emit JSON output")
	return cmd
}

// writeResolveResult prints binding in the flag-selected format. The JSON
// shape is exactly Binding's existing JSON tags, unchanged by the
// --descendant-of leaf path above: both paths resolve to the same Binding.
func writeResolveResult(cmd *cobra.Command, binding procbind.Binding) error {
	if JSONOutput(cmd) {
		return WriteJSON(binding)
	}
	fmt.Fprintf(cmd.OutOrStdout(), "pid %d (%s) runtime=%s codebase=%s main=%s\n", binding.PID, binding.Name, binding.Runtime, binding.CodebaseRoot, binding.MainScript)
	return nil
}

// printAmbiguousLeaf reports every tied leaf candidate under root, in either
// format, before the caller exits 2. Candidates come from
// procbind.ResolveLeaf and never carry raw argv (procbind.Binding /
// procbind.Candidate never serialize Cmdline).
func printAmbiguousLeaf(cmd *cobra.Command, root int32, candidates []procbind.Candidate) {
	if JSONOutput(cmd) {
		_ = WriteJSON(map[string]any{
			"error":         "ambiguous",
			"descendant_of": root,
			"candidates":    candidates,
		})
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "ambiguous leaf process under pid %d (%d candidates):\n", root, len(candidates))
	for _, c := range candidates {
		fmt.Fprintf(cmd.ErrOrStderr(), "  pid=%d name=%s runtime=%s main_script=%s\n", c.PID, c.Name, c.Runtime, c.MainScript)
	}
}
