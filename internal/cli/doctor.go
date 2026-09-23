package cli

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/ecosystem"
	"github.com/abdul-hamid-achik/monitor/internal/logger"
)

// doctorBinaryNames is the "one binary per consumer" hygiene list (E0.3):
// every tool monitor shells out to, plus monitor itself, so a stale dev
// build shadowing a versioned release on PATH is visible in `monitor
// doctor` instead of silently winning.
var doctorBinaryNames = []string{"monitor", "codemap", "glyph", "cairn", "vecgrep"}

// selfBinary is monitor's own {version, path} — the binary actually running
// this process, which may differ from whatever "monitor" resolves to on
// PATH (see doctorBinaryNames / doctorReport.Binaries.Other).
type selfBinary struct {
	Version string `json:"version"`
	Path    string `json:"path,omitempty"`
}

// doctorBinaries is the `binaries` section of `monitor doctor --json`.
type doctorBinaries struct {
	Self  selfBinary             `json:"self"`
	Other []ecosystem.BinaryInfo `json:"other_binaries"`
}

// doctorReport is `monitor doctor --json`'s stable presence contract (see
// docs/contracts/doctor-v1.md). ecosystem.Status is embedded anonymously so
// every existing top-level field (codemap, fcheap, vecgrep, ...) keeps its
// exact key — additive only, since minerva/cairntrace/chalupa may already
// parse this envelope. code_intel and binaries are new, additive sections.
type doctorReport struct {
	ecosystem.Status
	CodeIntel ecosystem.CodeIntelHealth `json:"code_intel"`
	Binaries  doctorBinaries            `json:"binaries"`
}

func buildDoctorReport(ctx context.Context) doctorReport {
	wd, _ := os.Getwd()
	report := doctorReport{
		Status:    ecosystem.Probe(ctx),
		CodeIntel: ecosystem.ProbeCodeIntel(ctx, wd),
	}
	report.Binaries.Self = selfBinary{Version: Version}
	if exe, err := os.Executable(); err == nil {
		report.Binaries.Self.Path = exe
	}
	report.Binaries.Other = make([]ecosystem.BinaryInfo, 0, len(doctorBinaryNames))
	for _, name := range doctorBinaryNames {
		report.Binaries.Other = append(report.Binaries.Other, ecosystem.ScanBinary(ctx, name))
	}
	return report
}

func newDoctorCmd() *cobra.Command {
	var required []string
	var strict bool
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Print ecosystem health and tool availability",
		Long: `Print ecosystem health and tool availability.

Beyond raw tool presence, doctor also reports code-intelligence health
(whether codemap/vecgrep are actually usable against the current directory —
not just on PATH — distinguishing a stale/schema-skewed index from a project
that simply isn't indexed yet) and binary hygiene for monitor, codemap, glyph,
cairn, and vecgrep — the exact tools monitor itself shells out to, which is
a different (smaller) set than --strict checks — reporting each one's
resolved PATH, version, and whether a duplicate install on PATH shadows it.

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
			report := buildDoctorReport(ctx)
			if JSONOutput(cmd) {
				if err := WriteJSON(report); err != nil {
					return err
				}
			} else {
				printDoctorHuman(report)
			}
			missing := missingRequiredTools(report.Status, requiredTools)
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

func printDoctorHuman(report doctorReport) {
	fmt.Println("Ecosystem tools:")
	fmt.Println("  codemap     :", formatToolStatus(report.Status.Codemap))
	fmt.Println("  fcheap      :", formatToolStatus(report.Status.Fcheap))
	fmt.Println("  vecgrep     :", formatToolStatus(report.Status.Vecgrep))
	fmt.Println("  tinyvault   :", formatToolStatus(report.Status.Tinyvault))
	fmt.Println("  vidtrace    :", formatToolStatus(report.Status.Vidtrace))
	fmt.Println("  glyphrun    :", formatToolStatus(report.Status.Glyphrun))
	fmt.Println("  cairntrace  :", formatToolStatus(report.Status.Cairntrace))
	fmt.Println("  veclite     :", formatToolStatus(report.Status.Veclite))
	fmt.Println("  tmux        :", formatToolStatus(report.Status.Tmux))

	fmt.Println()
	fmt.Println("Code intelligence health:")
	fmt.Println("  codemap     :", formatHealth(report.CodeIntel.Codemap))
	fmt.Println("  vecgrep     :", formatHealth(report.CodeIntel.Vecgrep))

	fmt.Println()
	fmt.Println("Binaries:")
	self := report.Binaries.Self
	if self.Path != "" {
		fmt.Printf("  self        : monitor %s (%s)\n", self.Version, self.Path)
	} else {
		fmt.Printf("  self        : monitor %s\n", self.Version)
	}
	for _, bin := range report.Binaries.Other {
		fmt.Println("  "+padName(bin.Name)+":", formatBinary(bin))
	}
}

func formatToolStatus(st ecosystem.ToolStatus) string {
	if !st.Available {
		if st.Note != "" {
			return "unavailable — " + st.Note
		}
		return "unavailable"
	}
	if st.Version != "" {
		return fmt.Sprintf("available (%s) — %s", st.Version, st.Path)
	}
	return "available — " + st.Path
}

func formatHealth(h ecosystem.Health) string {
	line := h.State
	if h.Detail != "" {
		line += " — " + h.Detail
	}
	if h.Recovery != "" {
		line += "\n      recovery: " + h.Recovery
	}
	return line
}

func formatBinary(bin ecosystem.BinaryInfo) string {
	if !bin.Available {
		return "unavailable"
	}
	line := bin.Path
	if bin.Version != "" {
		line += " (" + bin.Version + ")"
	}
	if bin.Shadowed {
		line += "\n      warning: " + bin.Warning
	}
	return line
}

// padName right-pads a binary name to line up the ":" column across the
// doctorBinaryNames list ("monitor", "codemap", "glyph", "cairn", "vecgrep").
func padName(name string) string {
	const width = 12
	if len(name) >= width {
		return name
	}
	return name + strings.Repeat(" ", width-len(name))
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
