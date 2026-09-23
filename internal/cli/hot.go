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
			// Checked once, before --export or --json do anything
			// observable: both modes must refuse a nonexistent --func the
			// same way, and --export must never write a file for a
			// request that's about to fail anyway.
			if funcName != "" && !hasHeatFunction(hm, funcName) {
				return fmt.Errorf("function %q not found in %s", funcName, file)
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
			return renderHotHuman(cmd.OutOrStdout(), file, hm, funcName)
		},
	}
	cmd.Flags().StringVar(&file, "file", "", "path to a .cpuprofile (V8/Bun) or a pprof .pb/.pb.gz")
	cmd.Flags().StringVar(&funcName, "func", "", "show this function's CodeFrame instead of the hottest one")
	cmd.Flags().IntVar(&top, "top", 0, fmt.Sprintf("functions to keep in the table/JSON (default %d)", profiler.DefaultTopFunctions))
	cmd.Flags().StringVar(&ptype, "type", "cpu", "profile type: cpu, heap (inuse_space), heap-alloc (alloc_space), goroutine (pprof sources only)")
	cmd.Flags().Bool("json", false, "emit the monitor.line_heatmap.v1 JSON document")
	cmd.Flags().StringVar(&export, "export", "",
		"also save the loaded profile (out.cpuprofile / out.pb.gz / out.pb) or the heatmap document (out.json)")
	return cmd
}

// parseHotType maps --type to a HeatProfileType. "heap" (the common case:
// "what's using memory right now") reaches HeatHeapInuse; "heap-alloc"
// reaches HeatHeapAlloc ("what has ever been allocated") — heat.Build's own
// HeatOptions.ProfileType has always supported both (see
// heatFindValueIndex/detectPprofProfileType), but until now the CLI only
// ever exposed the inuse column, leaving alloc_space unreachable from
// `monitor hot --type`.
func parseHotType(s string) (profiler.HeatProfileType, error) {
	switch s {
	case "", "cpu":
		return profiler.HeatCPU, nil
	case "heap":
		return profiler.HeatHeapInuse, nil
	case "heap-alloc":
		return profiler.HeatHeapAlloc, nil
	case "goroutine":
		return profiler.HeatGoroutine, nil
	default:
		return "", fmt.Errorf("--type must be one of cpu, heap, heap-alloc, goroutine (got %q)", s)
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

// renderHotHuman prints the header line (samples/duration/active/idle/
// method), the TOTAL/SELF/FUNCTION/LOCATION table, the CodeFrame of the
// hottest (or --func) function plus its callees and limitations, and a
// "next" line — the shape
// ~/notes/projects/monitor/2026-09-22-local-sentry-roadmap.md's "5. Mapa de
// calor por línea" mockup specifies, minus the pid/service-only banner line
// (no live process in --file mode) and the CALLERS/TESTS/ISSUES columns
// (codemap impact and the issues-store overlay are later slices, E3.4). Per
// AC-5, a mostly-idle profile (hm.MostlyIdle) still gets its table — those
// percentages are real — but never a CodeFrame: with the sampler mostly
// catching (idle), no single line has earned a confident-looking marker.
func renderHotHuman(w io.Writer, path string, hm *profiler.Heatmap, wantFunc string) error {
	fmt.Fprintf(w, "%s      method: %s\n", hotHeaderLine(path, hm), humanMethodLabel(hm.Method))
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

	if hm.MostlyIdle() {
		fmt.Fprintf(w, "(codeframe skipped: %.0f%% idle — CPU heat can't explain this target's slowness; "+
			"see the warning above instead of a guessed hot line)\n", hm.IdlePct)
		return nil
	}

	var target profiler.HeatFunction
	switch {
	case wantFunc != "":
		for _, f := range hm.Functions {
			if f.Name == wantFunc {
				target = f
				break
			}
		}
	case hm.DefaultTarget != nil:
		// The real hottest-by-self function across the WHOLE profile (see
		// Heatmap.DefaultTarget's doc), not re-derived from hm.Functions:
		// that list may already be filtered/capped by --top, which must
		// never silently swap in some other, cooler function's CodeFrame.
		target = *hm.DefaultTarget
	default:
		// Defensive fallback only: BuildHeatmap always populates
		// DefaultTarget whenever it produced at least one function (see
		// addWarnings), and hm.Functions is already known non-empty here.
		target = hottestBySelf(hm.Functions)
	}

	cf := codeFrameForFunction(hm, target)
	fmt.Fprintln(w, cf.Render())
	if len(target.Callees) > 0 {
		fmt.Fprintln(w, formatCallees(hm, target))
	}
	for _, lim := range hm.Limitations {
		fmt.Fprintf(w, "  %s\n", lim)
	}

	next := "monitor hot --file " + shellQuote(path)
	if suggestion := nextFuncSuggestion(hm.Functions, target.Name); suggestion != "" && suggestion != target.Name {
		next += " --func " + shellQuote(suggestion)
	}
	fmt.Fprintf(w, "next  %s · --export hot%s (%s)\n", next, hotExportExt(hm.Method), exportViewerHint(hm.Method))
	return nil
}

// hotHeaderLine renders the "loaded ..." summary line's content (everything
// before "      method: ..."): the loaded path, profile type, capture
// duration when known, quantity, and — for a CPU profile type, where
// "idle" is a meaningful concept — the active/idle/gc/program breakdown.
func hotHeaderLine(path string, hm *profiler.Heatmap) string {
	parts := []string{"loaded " + path, string(hm.ProfileType)}
	// Only a CDP source benefits from a separate duration clause: its
	// Samples is a sample COUNT, unrelated to wall-clock time. A pprof CPU
	// source's own Samples/unit IS already a nanosecond (duration-shaped)
	// quantity — formatHeatQuantity renders it as one below — so adding
	// CaptureDurationNanos too would just print the same duration twice.
	if hm.CaptureDurationNanos > 0 && hm.Unit != "nanoseconds" {
		parts = append(parts, time.Duration(hm.CaptureDurationNanos).String())
	}
	parts = append(parts, formatHeatQuantity(hm.Samples, hm.Unit))
	if clause := hotActiveClause(hm); clause != "" {
		parts = append(parts, clause)
	}
	return strings.Join(parts, " · ")
}

// hotActiveClause renders the "active X% (idle .../gc .../program ...
// excluded)" clause, or "" to omit it entirely. "" for anything but a CPU
// profile type: idle/gc/program-time accounting is a CPU-sampling concept,
// meaningless for a heap or goroutine snapshot (ActiveSamples there always
// equals Samples by construction, so an "active 100%" clause would say
// nothing true or useful). For a CPU profile whose idle share genuinely
// wasn't measured (a pprof CPU proto with no DurationNanos — see
// Heatmap.IdleMeasured), says so explicitly rather than printing a
// misleading "idle 0%".
func hotActiveClause(hm *profiler.Heatmap) string {
	if hm.ProfileType != profiler.HeatCPU {
		return ""
	}
	if !hm.IdleMeasured {
		return "active: not measured"
	}
	activePct := 0.0
	if hm.Samples > 0 {
		activePct = float64(hm.ActiveSamples) / float64(hm.Samples) * 100
	}
	// programPct is whatever's left of the excluded share once idle and gc
	// are subtracted out (root/other negligible pseudo-frames), so the
	// three numbers shown always actually add up to the excluded share —
	// see the review evidence: a header that showed only idle silently
	// dropped real gc/program time from the accounting.
	programPct := (100 - activePct) - hm.IdlePct - hm.GCPct
	if programPct < 0.05 {
		programPct = 0 // float rounding noise, or a source with no gc/program concept (pprof): never show a fabricated negative share.
	}
	if hm.GCPct > 0 || programPct > 0 {
		return fmt.Sprintf("active %.0f%% (idle %.0f%%, gc %.0f%%, program %.0f%% excluded)", activePct, hm.IdlePct, hm.GCPct, programPct)
	}
	return fmt.Sprintf("active %.0f%% (idle %.0f%% excluded)", activePct, hm.IdlePct)
}

// humanMethodLabel renders a HeatMethod the way a person reads it — the
// roadmap mockup's own wording ("method: v8 positionTicks"), not the raw
// JSON enum value ("v8_position_ticks").
func humanMethodLabel(m profiler.HeatMethod) string {
	switch m {
	case profiler.MethodV8PositionTicks:
		return "v8 positionTicks"
	case profiler.MethodCPUProfileFile:
		return "v8 (no positionTicks; declaration-line fallback)"
	case profiler.MethodPprofProto:
		return "pprof proto (inlining-aware; no go toolchain needed)"
	default:
		return string(m)
	}
}

// formatCallees renders one function's Callees as the roadmap's "calls->"
// line — each callee's Cum as a share of the WHOLE profile's active
// samples, the same basis codeFrameForFunction's dual-column view uses —
// so a wrapper (all its cost is in what it calls, not any line of its own)
// still tells the reader where the time actually went instead of just
// "(no lines to show)".
func formatCallees(hm *profiler.Heatmap, f profiler.HeatFunction) string {
	total := float64(hm.ActiveSamples)
	parts := make([]string, 0, len(f.Callees))
	for _, c := range f.Callees {
		pct := 0.0
		if total > 0 {
			pct = float64(c.Cum) / total * 100
		}
		parts = append(parts, fmt.Sprintf("%s %.1f%%", c.Func, pct))
	}
	return "calls-> " + strings.Join(parts, "   ")
}

// shellQuote wraps s in single quotes, escaping any single quote it
// contains, so a "next" line a person copy-pastes into bash or zsh never
// breaks on a function name like "(anonymous)" (parens are shell
// metacharacters) or a path with a space.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// exportViewerHint names the tool that opens --export's output: pprof's own
// `go tool pprof` for a .pb.gz/.pb source, or Chrome DevTools / VS Code for
// a V8/Bun .cpuprofile — never the same hint for both formats, which a
// person can't actually open a pprof proto in.
func exportViewerHint(m profiler.HeatMethod) string {
	if m == profiler.MethodPprofProto {
		return "go tool pprof"
	}
	return "Chrome DevTools / VS Code"
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
