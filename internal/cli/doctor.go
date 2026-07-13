package cli

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/logger"
)

func newDoctorCmd() *cobra.Command {
	var required []string
	var strict bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Print ecosystem health and tool availability",
		Long: `Print ecosystem health and tool availability.

Use --require for CI or scripts that depend on specific integrations. Use
--strict to require every known integration. Status is still printed before a
non-zero result, so humans and agents can see exactly what is missing.`,
		RunE: func(cmd *cobra.Command, _ []string) error {
			requiredTools, err := normalizeRequiredTools(required, strict)
			if err != nil {
				return err
			}
			ctx, cancel := Context()
			defer cancel()
			status := ecosystem.Probe(ctx)
			if JSONOutput(cmd) {
				if err := WriteJSON(status); err != nil {
					return err
				}
			} else {
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
			}
			missing := missingRequiredTools(status, requiredTools)
			if len(missing) > 0 {
				return fmt.Errorf("required ecosystem tools unavailable: %s", strings.Join(missing, ", "))
			}
			return nil
		},
	}
	cmd.Flags().Bool("json", false, "emit JSON output")
	cmd.Flags().StringSliceVar(&required, "require", nil,
		"require tools to be available (repeat or comma-separate names)")
	cmd.Flags().BoolVar(&strict, "strict", false, "require every known ecosystem tool")
	return cmd
}

var doctorToolNames = []string{
	"codemap", "fcheap", "vecgrep", "tinyvault", "vidtrace",
	"glyphrun", "cairntrace", "veclite", "tmux",
}

func normalizeRequiredTools(values []string, strict bool) ([]string, error) {
	if strict {
		return append([]string(nil), doctorToolNames...), nil
	}
	valid := make(map[string]bool, len(doctorToolNames))
	for _, name := range doctorToolNames {
		valid[name] = true
	}
	seen := make(map[string]bool)
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			name := strings.ToLower(strings.TrimSpace(part))
			if name == "tvault" {
				name = "tinyvault"
			}
			if name == "glyph" {
				name = "glyphrun"
			}
			if !valid[name] {
				return nil, fmt.Errorf("unknown ecosystem tool %q (valid: %s)", part, strings.Join(doctorToolNames, ", "))
			}
			seen[name] = true
		}
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out, nil
}

func missingRequiredTools(status ecosystem.Status, required []string) []string {
	available := map[string]bool{
		"codemap": status.Codemap.Available, "fcheap": status.Fcheap.Available,
		"vecgrep": status.Vecgrep.Available, "tinyvault": status.Tinyvault.Available,
		"vidtrace": status.Vidtrace.Available, "glyphrun": status.Glyphrun.Available,
		"cairntrace": status.Cairntrace.Available, "veclite": status.Veclite.Available,
		"tmux": status.Tmux.Available,
	}
	missing := make([]string, 0)
	for _, name := range required {
		if !available[name] {
			missing = append(missing, name)
		}
	}
	sort.Strings(missing)
	return missing
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

// openLogStore opens the writer used by `logs capture`. Search must use
// logger.OpenReadOnly so it neither creates a database nor contends for the
// writer lock held by Studio or another capture process.
func openLogStore(path string) (*logger.Store, error) {
	return logger.OpenStore(path)
}
