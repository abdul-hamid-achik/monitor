package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/charmbracelet/x/term"
	"github.com/spf13/cobra"

	"github.com/abdul-hamid-achik/monitor/internal/collector"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

// newHotCmd is `monitor hot`: AC-5's "which LINE" view, built on
// profiler.BuildHeatmap (monitor.line_heatmap.v1). E3.1 wires only the
// --file mode (a saved .cpuprofile or pprof proto); resolving a live
// <pid|service> is E3.2, a later wave, so a positional target argument
// gets a clear "not yet" error instead of silently doing nothing.
func newHotCmd() *cobra.Command {
	var file, funcName, ptype, export string
	var top int
	cmd := &cobra.Command{
		Use:   "hot",
		Short: "Show the hot line inside a function (CPU, heap, or goroutine)",
		Long: `monitor hot answers "which LINE inside this function", not just
"which function" — combining stack samples (V8 positionTicks or a pprof
proto) with a per-function CodeFrame.

Only --file is wired yet: point it at a V8/Bun .cpuprofile or a
github.com/google/pprof proto (.pb / .pb.gz, gzip auto-detected).
Resolving a live <pid|service> target is a later wave.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf(
					"monitor hot <pid|service> is not implemented yet; pass --file <path> instead "+
						"(a V8/Bun .cpuprofile, or a pprof .pb/.pb.gz — got positional arg %q)", args[0])
			}
			if strings.TrimSpace(file) == "" {
				return fmt.Errorf("--file is required (pid/service modes are not implemented yet): " +
					"monitor hot --file <path.cpuprofile|path.pb.gz>")
			}

			heatType, err := parseHotType(ptype)
			if err != nil {
				return err
			}

			src, err := profiler.LoadFile(file)
			if err != nil {
				return err
			}
			if src.Kind == profiler.SourceCDP && heatType != profiler.HeatCPU && cmd.Flags().Changed("type") {
				return fmt.Errorf("--type %s only applies to a pprof profile; %s is a V8/Bun .cpuprofile (always cpu)", ptype, file)
			}

			ctx, cancel := Context()
			defer cancel()
			hm, err := profiler.BuildHeatmap(ctx, src, profiler.HeatOptions{
				Func:        funcName,
				Top:         top,
				ProfileType: heatType,
			})
			if err != nil {
				return err
			}

			if export != "" {
				if err := exportHot(src, hm, export); err != nil {
					return err
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "saved: %s\n", export)
			}

			if JSONOutput(cmd) {
				return WriteJSON(hm)
			}
			if funcName != "" && !hasHeatFunction(hm, funcName) {
				return fmt.Errorf("function %q not found in %s", funcName, file)
			}
			return renderHotHuman(cmd.OutOrStdout(), file, hm, funcName)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to a .cpuprofile (V8/Bun) or a pprof .pb/.pb.gz")
	cmd.Flags().StringVar(&funcName, "func", "", "show this function's CodeFrame instead of the hottest one")
	cmd.Flags().IntVar(&top, "top", 0, fmt.Sprintf("functions to keep in the table/JSON (default %d)", profiler.DefaultTopFunctions))
	cmd.Flags().StringVar(&ptype, "type", "cpu", "profile type: cpu, heap, goroutine (pprof sources only)")
	cmd.Flags().Bool("json", false, "emit the monitor.line_heatmap.v1 JSON document")
	cmd.Flags().StringVar(&export, "export", "",
		"also save the loaded profile (out.cpuprofile / out.pb.gz / out.pb) or the heatmap document (out.json)")
	return cmd
}

func parseHotType(s string) (profiler.HeatProfileType, error) {
	switch s {
	case "", "cpu":
		return profiler.HeatCPU, nil
	case "heap":
		return profiler.HeatHeapInuse, nil
	case "goroutine":
		return profiler.HeatGoroutine, nil
	default:
		return "", fmt.Errorf("--type must be one of cpu, heap, goroutine (got %q)", s)
	}
}

func hasHeatFunction(hm *profiler.Heatmap, name string) bool {
	for _, f := range hm.Functions {
		if f.Name == name {
			return true
		}
	}
	return false
}

// exportHot saves either the ORIGINAL loaded profile bytes (a .cpuprofile
// or pprof proto, openable in Chrome DevTools / `go tool pprof`) or the
// built heatmap document, chosen by out's extension. It refuses a
// format/source mismatch (e.g. --export out.pb.gz against a .cpuprofile
// source) rather than silently writing the wrong bytes under a misleading
// name.
func exportHot(src *profiler.Source, hm *profiler.Heatmap, out string) error {
	lower := strings.ToLower(out)
	switch {
	case strings.HasSuffix(lower, ".json"):
		data, err := json.MarshalIndent(hm, "", "  ")
		if err != nil {
			return fmt.Errorf("export: encode heatmap: %w", err)
		}
		return os.WriteFile(out, data, 0o600)
	case strings.HasSuffix(lower, ".cpuprofile"):
		if src.Kind != profiler.SourceCDP {
			return fmt.Errorf("--export %s: the loaded profile is a pprof proto, not a .cpuprofile; export to .pb.gz, .pb, or .json instead", out)
		}
		return copyFileBytes(src.Path, out)
	case strings.HasSuffix(lower, ".pb.gz"), strings.HasSuffix(lower, ".pb"):
		if src.Kind != profiler.SourcePprof {
			return fmt.Errorf("--export %s: the loaded profile is a V8/Bun .cpuprofile, not a pprof proto; export to .cpuprofile or .json instead", out)
		}
		return copyFileBytes(src.Path, out)
	default:
		return fmt.Errorf("--export %s: unsupported extension (want .cpuprofile, .pb.gz, .pb, or .json)", out)
	}
}

func copyFileBytes(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return fmt.Errorf("export: read %s: %w", src, err)
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return fmt.Errorf("export: create directory for %s: %w", dst, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return fmt.Errorf("export: write %s: %w", dst, err)
	}
	return nil
}

// renderHotHuman prints the header line (samples/active/idle/method), the
// TOTAL/SELF/FUNCTION/LOCATION table, the CodeFrame of the hottest (or
// --func) function, and a "next" line — the shape
// ~/notes/projects/monitor/2026-09-22-local-sentry-roadmap.md's "5. Mapa de
// calor por línea" mockup specifies, minus the pid/service-only banner line
// (no live process in --file mode) and the CALLERS/TESTS/ISSUES columns
// (codemap impact and the issues-store overlay are later slices, E3.4).
func renderHotHuman(w io.Writer, path string, hm *profiler.Heatmap, wantFunc string) error {
	activePct := 0.0
	if hm.Samples > 0 {
		activePct = float64(hm.ActiveSamples) / float64(hm.Samples) * 100
	}
	fmt.Fprintf(w, "loaded %s · %s · %s · active %.0f%% (idle %.0f%% excluded)      method: %s\n",
		path, hm.ProfileType, formatHeatQuantity(hm.Samples, hm.Unit), activePct, hm.IdlePct, hm.Method)
	for _, warn := range hm.Warnings {
		fmt.Fprintf(w, "! %s\n", warn)
	}

	if len(hm.Functions) == 0 {
		fmt.Fprintln(w, "(no functions found in this profile)")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TOTAL\tSELF\tFUNCTION\tLOCATION")
	for _, f := range hm.Functions {
		fmt.Fprintf(tw, "%.1f%%\t%.1f%%\t%s\t%s\n", f.CumPct, f.SelfPct, f.Name, heatLocation(f))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	var target profiler.HeatFunction
	if wantFunc != "" {
		for _, f := range hm.Functions {
			if f.Name == wantFunc {
				target = f
				break
			}
		}
	} else {
		target = hottestBySelf(hm.Functions)
	}

	cf := codeFrameForFunction(hm, target)
	fmt.Fprintln(w, cf.Render())

	next := fmt.Sprintf("monitor hot --file %s --func %s", path, nextFuncSuggestion(hm.Functions, target.Name))
	fmt.Fprintf(w, "next  %s · --export hot%s (Chrome DevTools / VS Code)\n", next, hotExportExt(hm.Method))
	return nil
}

// formatHeatQuantity renders Heatmap.Samples in a unit-appropriate,
// human-readable form: a duration for a pprof CPU profile's nanosecond
// column (raw "1750000000 nanoseconds" is not something anyone reads at a
// glance), a byte size for a heap profile, and a plain sample count
// otherwise (the CDP path, and any pprof column this package doesn't
// specifically recognize).
func formatHeatQuantity(n int, unit string) string {
	switch unit {
	case "nanoseconds":
		return time.Duration(n).String()
	case "bytes":
		return collector.FormatBytes(uint64(n))
	case "samples", "":
		return fmt.Sprintf("%d samples", n)
	default:
		return fmt.Sprintf("%d %s", n, unit)
	}
}

func heatLocation(f profiler.HeatFunction) string {
	if f.StartLine <= 0 {
		return f.File
	}
	if f.EndLine > 0 && f.EndLine != f.StartLine {
		return fmt.Sprintf("%s:%d-%d", f.File, f.StartLine, f.EndLine)
	}
	return fmt.Sprintf("%s:%d", f.File, f.StartLine)
}

// hottestBySelf returns the function with the highest SelfPct — "which
// function is actually expensive", not "which function has the highest
// cumulative total" (a thin wrapper around a hot callee always tops that
// ranking and would make a poor default CodeFrame target; see
// TestBuildHeatmapCDPWrapperGetsCalleesNotLines). Falls back to functions[0]
// (already Cum-sorted by BuildHeatmap) when every function's self share is
// zero — an all-wrapper profile with no leaf data at all.
func hottestBySelf(fns []profiler.HeatFunction) profiler.HeatFunction {
	best := fns[0]
	for _, f := range fns[1:] {
		if f.SelfPct > best.SelfPct {
			best = f
		}
	}
	return best
}

// nextFuncSuggestion names the highest-ranked function other than the one
// just shown, for the "next" line's --func suggestion — falling back to the
// shown function's own name when it's the only one in the profile.
// nextFuncSuggestion names another function worth a follow-up look, for the
// "next" line's --func suggestion: it prefers a named function over one of
// V8's anonymous-closure/module-scope placeholders ("(anonymous)", which
// every profile with any top-level or closure code carries at least one of
// and is rarely what a human wants to inspect next), falling back to
// whatever else is available, and finally to the shown function's own name
// when it is the only one in the profile.
func nextFuncSuggestion(fns []profiler.HeatFunction, shown string) string {
	fallback := shown
	for _, f := range fns {
		if f.Name == shown {
			continue
		}
		if f.Name != "(anonymous)" {
			return f.Name
		}
		fallback = f.Name
	}
	return fallback
}

func hotExportExt(method profiler.HeatMethod) string {
	if method == profiler.MethodPprofProto {
		return ".pb.gz"
	}
	return ".cpuprofile"
}

// codeFrameForFunction adapts one profiler.HeatFunction into a rendered
// widgets.CodeFrame. The dual-column (pprof) view's SELF/CUM percentages
// are each line's share of the WHOLE profile's active samples — matching
// the roadmap's FLAT/CUM mockup, where a function's own line percentages
// sum back to its function-level headline — while the self-only (CDP) view
// uses PctOfFunction, each line's share of its OWN function's self time
// (V8 positionTicks have no whole-profile-relative cum to show at all; see
// HeatLine's doc comment). Color is decided from whether stdout is a TTY
// and NO_COLOR, never by the widget itself.
func codeFrameForFunction(hm *profiler.Heatmap, f profiler.HeatFunction) widgets.CodeFrame {
	showCum := hm.Method == profiler.MethodPprofProto
	cf := widgets.CodeFrame{
		FuncName: f.Name, File: f.File, StartLine: f.StartLine, EndLine: f.EndLine,
		SelfPct: f.SelfPct, CumPct: f.CumPct, ShowCum: showCum,
		Color: wantColor(os.Stdout),
		Width: widgets.DefaultCodeFrameWidth,
	}
	total := float64(hm.ActiveSamples)
	for _, l := range f.Lines {
		cl := widgets.CodeFrameLine{Line: l.Line, Code: l.Code}
		if showCum && total > 0 {
			cl.Percent = float64(l.Self) / total * 100
			cl.CumPercent = float64(l.Cum) / total * 100
		} else {
			cl.Percent = l.PctOfFunction
		}
		cf.Lines = append(cf.Lines, cl)
	}
	if showCum {
		cf.Footer = "method: pprof proto (inlining-aware; no go toolchain needed)"
	} else {
		cf.Footer = "% = share of this function's self samples"
	}
	return cf
}

// wantColor reports whether w is a real terminal and NO_COLOR isn't set —
// the two conditions FILE OWNERSHIP's design note ("no color when not a
// TTY / NO_COLOR") requires. w is checked directly (not the global
// os.Stdout) so a future caller that redirects output for a test or a
// pipe gets the right answer without this function changing.
func wantColor(w *os.File) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if w == nil {
		return false
	}
	return term.IsTerminal(w.Fd())
}
