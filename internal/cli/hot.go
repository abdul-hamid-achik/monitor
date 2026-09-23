package cli

import (
	"context"
	"encoding/json"
	"errors"
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
	"github.com/abdul-hamid-achik/monitor/internal/issues"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
	"github.com/abdul-hamid-achik/monitor/internal/widgets"
)

// newHotCmd is `monitor hot`: AC-5's "which LINE" view, built on
// profiler.BuildHeatmap (monitor.line_heatmap.v1). E3.1 wired --file mode (a
// saved .cpuprofile or pprof proto); E3.2 adds a numeric <pid> target,
// resolved to its real runtime leaf process (skipping a shell/yarn/npm/
// `go run` wrapper) and captured live via the same runtime-aware dispatch
// `monitor profile` uses. A symbolic <service> name needs the launch
// registry (a later wave), so it still gets a clear "not yet" error instead
// of silently doing nothing.
func newHotCmd() *cobra.Command {
	var file, funcName, ptype, export, pprofAddr string
	var top int
	var duration time.Duration
	cmd := &cobra.Command{
		Use:   "hot [pid]",
		Short: "Show the hot line inside a function (CPU, heap, or goroutine)",
		Long: `monitor hot answers "which LINE inside this function", not just
"which function" — combining stack samples (V8 positionTicks or a pprof
proto) with a per-function CodeFrame.

--file loads a saved V8/Bun .cpuprofile or a github.com/google/pprof proto
(.pb / .pb.gz, gzip auto-detected). A numeric pid instead captures live,
resolving the real runtime leaf process under it (skipping a shell/yarn/npm/
` + "`go run`" + ` wrapper — ambiguous, exits 2 listing candidates). A symbolic
<service> name needs the launch registry, a later wave.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 {
				pid, perr := parsePID(args[0])
				if perr != nil {
					return fmt.Errorf(
						"monitor hot <service> is not implemented yet (needs the launch registry); "+
							"pass a numeric pid or --file <path> instead (got %q)", args[0])
				}
				return runHotPID(cmd, pid, funcName, ptype, export, pprofAddr, top, duration)
			}
			if strings.TrimSpace(file) == "" {
				return fmt.Errorf("--file is required when no pid is given: " +
					"monitor hot --file <path.cpuprofile|path.pb.gz>, or monitor hot <pid>")
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
			if warn := applyIssueOverlay(hm); warn != "" {
				hm.Warnings = append(hm.Warnings, warn)
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
	cmd.Flags().DurationVar(&duration, "duration", 5*time.Second, "CPU sampling duration for a live <pid> capture (maximum 2m); ignored for --file and for --type heap/goroutine (instant snapshots)")
	cmd.Flags().StringVar(&pprofAddr, "pprof-addr", "localhost:6060",
		"host:port of the target's net/http/pprof server, for a live <pid> Go target (ignored for --file and for a Node/Bun/Deno target's own inspector); "+
			"passing this flag explicitly asserts the endpoint belongs to the target pid and skips the ownership check")
	return cmd
}

// runHotPID is `monitor hot <pid>` (E3.2): resolve pid's real runtime leaf
// process, capture a profile live via the same runtime-aware dispatch
// `monitor profile`/MCP's monitor_profile_capture use
// (captureRuntimeAwareProfile), then hand it to the exact same
// BuildHeatmap+render pipeline --file uses, so a live capture and a saved
// one produce byte-for-byte the same kind of output.
func runHotPID(cmd *cobra.Command, pid int32, funcName, ptypeFlag, export, pprofAddr string, top int, duration time.Duration) error {
	heatType, err := parseHotType(ptypeFlag)
	if err != nil {
		return err
	}
	if duration <= 0 || duration > 2*time.Minute {
		return fmt.Errorf("--duration must be greater than zero and at most 2m")
	}

	ctx, cancel := Context()
	defer cancel()

	binding, candidates, err := procbind.ResolveLeaf(ctx, pid, procbind.LeafOptions{})
	if err != nil {
		var ambiguous *procbind.AmbiguousLeafError
		if errors.As(err, &ambiguous) {
			printAmbiguousLeaf(cmd, pid, candidates)
			os.Exit(2)
			return nil
		}
		return fmt.Errorf("monitor hot %d: %w", pid, err)
	}

	addrExplicit := cmd.Flags().Changed("pprof-addr")
	prof, method, step := captureRuntimeAwareProfile(ctx, binding.PID, &binding, heatTypeToProfileType(heatType), pprofAddr, "", addrExplicit, duration, true)
	if step.Status != stepOK {
		msg := step.Limitation
		if step.Recovery != "" {
			msg = fmt.Sprintf("%s (%s)", msg, step.Recovery)
		}
		return fmt.Errorf("%s", msg)
	}
	defer discardTempProfilePath(&prof)

	src, err := profilerSourceFromCapture(prof)
	if err != nil {
		return fmt.Errorf("monitor hot %d: %w", pid, err)
	}

	hm, err := profiler.BuildHeatmap(ctx, src, profiler.HeatOptions{
		Func: funcName, Top: top, ProfileType: heatType, Runtime: string(binding.Runtime),
	})
	if err != nil {
		return err
	}
	if funcName != "" && !hasHeatFunction(hm, funcName) {
		return fmt.Errorf("function %q not found in pid %d", funcName, pid)
	}
	if warn := applyIssueOverlay(hm); warn != "" {
		hm.Warnings = append(hm.Warnings, warn)
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

	header := hotLiveHeaderLine(ctx, pid, binding, method, pprofAddr, heatType, duration)
	next := fmt.Sprintf("monitor hot %d", pid)
	return renderHotLiveHuman(cmd.OutOrStdout(), header, hm, funcName, next)
}

// heatTypeToProfileType maps monitor hot's own --type (HeatProfileType,
// which distinguishes heap_inuse from heap_alloc — two different pprof
// sample-value COLUMNS of the very same captured proto) down to
// captureRuntimeAwareProfile's coarser ProfileType (which only distinguishes
// WHICH ENDPOINT/MECHANISM to scrape): a single heap capture already carries
// every value column BuildHeatmap might later pick from, so both HeatOptions
// heap variants capture identically.
func heatTypeToProfileType(t profiler.HeatProfileType) profiler.ProfileType {
	switch t {
	case profiler.HeatHeapInuse, profiler.HeatHeapAlloc:
		return profiler.ProfileHeap
	case profiler.HeatGoroutine:
		return profiler.ProfileGoroutine
	default:
		return profiler.ProfileCPU
	}
}

// profilerSourceFromCapture turns a just-captured profiler.Profile into a
// profiler.Source BuildHeatmap can consume, without adding a live-capture
// code path to internal/profiler's own loadfile.go: a pprof capture (heap/
// cpu/goroutine) already wrote its raw proto to Profile.Path
// (captureProfilePprof's writeTempProfile), so that file is loaded directly;
// a CDP CPU capture carries its raw .cpuprofile JSON in Profile.Text instead
// (ProfileInspector never writes one to disk), so it is spooled to a private
// temp file first, loaded, and immediately removed. A capture with neither —
// macOS `sample` (no file:line at all), or a CDP HEAP snapshot (a completely
// different wire shape LoadFile was never built to parse) — has no
// line-level detail to build a heatmap from, and this returns an error
// naming why rather than fabricating an empty heatmap.
func profilerSourceFromCapture(prof profiler.Profile) (*profiler.Source, error) {
	if prof.Path != "" {
		src, err := profiler.LoadFile(prof.Path)
		if err != nil {
			return nil, fmt.Errorf("this %s capture (method %s) has no line-level detail to build a heatmap from: %w", prof.Type, prof.Method, err)
		}
		return src, nil
	}
	if prof.Text != "" {
		f, err := os.CreateTemp("", "monitor-hot-live-*.cpuprofile")
		if err != nil {
			return nil, fmt.Errorf("stage captured profile: %w", err)
		}
		path := f.Name()
		defer os.Remove(path)
		if _, werr := f.WriteString(prof.Text); werr != nil {
			_ = f.Close()
			return nil, fmt.Errorf("stage captured profile: %w", werr)
		}
		if cerr := f.Close(); cerr != nil {
			return nil, fmt.Errorf("stage captured profile: %w", cerr)
		}
		src, err := profiler.LoadFile(path)
		if err != nil {
			return nil, fmt.Errorf("this %s capture (method %s) has no line-level detail to build a heatmap from: %w", prof.Type, prof.Method, err)
		}
		return src, nil
	}
	return nil, fmt.Errorf("this %s capture (method %s) carries no file:line detail to build a heatmap from", prof.Type, prof.Method)
}

// hotLiveHeaderLine renders monitor hot's live-capture banner — the roadmap
// mockup's "monitor > <name> = <runtime> pid <N> (child of <wrapper> <M>) ·
// inspector <addr> · sampling <duration>" (section 5), minus the launch
// registry's own --name (service mode, a later wave): the leaf's own
// runtime+pid stands in for <name>. When the resolved leaf differs from the
// requested pid (a wrapper was skipped), the original process is looked up
// ONCE more (a single Inspect call, not a full process-table walk) purely
// for its name; any failure there just omits the name rather than the whole
// clause, since ResolveLeaf itself already proved a real wrapper was
// skipped.
func hotLiveHeaderLine(ctx context.Context, requestedPID int32, binding procbind.Binding, method, pprofAddr string, heatType profiler.HeatProfileType, duration time.Duration) string {
	who := fmt.Sprintf("%s pid %d", binding.Runtime, binding.PID)
	if requestedPID != binding.PID {
		label := "wrapper"
		if root, err := procbind.Inspect(ctx, requestedPID, ""); err == nil && root.Name != "" {
			label = root.Name
		}
		who += fmt.Sprintf(" (child of %s %d)", label, requestedPID)
	}
	parts := []string{"monitor > " + who}
	switch {
	case strings.HasPrefix(method, "inspector"):
		if binding.InspectAddr != "" {
			parts = append(parts, "inspector "+binding.InspectAddr)
		}
	case strings.HasPrefix(method, "pprof"):
		addr := pprofAddr
		if addr == "" {
			addr = profiler.DefaultPprofAddr
		}
		parts = append(parts, "pprof "+addr)
	}
	if heatType == profiler.HeatCPU {
		parts = append(parts, "sampling "+duration.String())
	} else {
		parts = append(parts, "instant snapshot")
	}
	return strings.Join(parts, " · ")
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

// renderHotHuman prints the --file header line (samples/duration/active/
// idle/method) then delegates the rest — table, CodeFrame, callees,
// limitations, next line — to renderHeatBody, shared with the live <pid>
// renderer (renderHotLiveHuman) so both modes render the exact same body
// shape.
func renderHotHuman(w io.Writer, path string, hm *profiler.Heatmap, wantFunc string) error {
	fmt.Fprintf(w, "%s      method: %s\n", hotHeaderLine(path, hm), humanMethodLabel(hm.Method))
	return renderHeatBody(w, hm, wantFunc, "monitor hot --file "+shellQuote(path))
}

// renderHotLiveHuman is runHotPID's (E3.2) renderer: header already carries
// the roadmap's "monitor > <name> = ..." live-capture banner (see
// hotLiveHeaderLine), everything after it is the same renderHeatBody
// --file mode uses.
func renderHotLiveHuman(w io.Writer, header string, hm *profiler.Heatmap, wantFunc, next string) error {
	fmt.Fprintln(w, header)
	return renderHeatBody(w, hm, wantFunc, next)
}

// renderHeatBody prints the shared part of `monitor hot`'s human output —
// warnings, the TOTAL/SELF/FUNCTION/LOCATION/ISSUES table (codemap's own
// CALLERS/TESTS columns are a later slice; ISSUES is E3.4's errors × heat
// overlay, applyIssueOverlay), the CodeFrame of the hottest (or --func)
// function plus its callees and limitations, and a "next" line — the shape
// ~/notes/projects/monitor/2026-09-22-local-sentry-roadmap.md's "5. Mapa de
// calor por línea" mockup specifies. Per AC-5, a mostly-idle profile
// (hm.MostlyIdle) still gets its table — those percentages are real — but
// never a CodeFrame: with the sampler mostly catching (idle), no single
// line has earned a confident-looking marker; E3.5 adds the idle case's own
// follow-up suggestions (--type goroutine for Go, `monitor issues --since`
// for any runtime) right after the warning that explains why.
func renderHeatBody(w io.Writer, hm *profiler.Heatmap, wantFunc, next string) error {
	for _, warn := range hm.Warnings {
		fmt.Fprintf(w, "! %s\n", warn)
		if warn == "mostly idle: slowness is off-CPU" {
			fmt.Fprintln(w, "  this process is mostly idle, so its slowness is off-CPU (I/O, locks, awaits).")
			fmt.Fprintln(w, "  Go: monitor hot <pid> --type goroutine   ·   any runtime: monitor issues --since 10m (timeouts?)")
		}
	}

	if len(hm.Functions) == 0 {
		fmt.Fprintln(w, "(no functions found in this profile)")
		return nil
	}

	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TOTAL\tSELF\tFUNCTION\tLOCATION\tISSUES")
	for _, f := range hm.Functions {
		fmt.Fprintf(tw, "%.1f%%\t%.1f%%\t%s\t%s\t%s\n", f.CumPct, f.SelfPct, f.Name, heatLocation(f), issuesColumnForFunction(f))
	}
	if err := tw.Flush(); err != nil {
		return err
	}

	if hm.MostlyIdle() {
		fmt.Fprintf(w, "(codeframe skipped: %.0f%% idle)\n", hm.IdlePct)
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
		if len(l.Issues) > 0 {
			cl.Issues = make([]string, 0, len(l.Issues))
			for _, iss := range l.Issues {
				cl.Issues = append(cl.Issues, fmt.Sprintf("%s x%d %s", iss.ShortID, iss.Count, iss.Status))
			}
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

// applyIssueOverlay is E3.4 (errors × heat, the hot side): it reads the
// local issues store read-only (issues.OpenReadOnly — never blocking, and
// never blocked by, a writer such as `watch --stash`) and fills each
// function's HeatLine.Issues with every exception issue whose Culprit lands
// on that exact line, matched by file (root-relative or absolute; see
// heatFileMatchesCulprit) and by line falling inside the function's own
// [StartLine,EndLine] range. heat.Build itself never computes this (see
// HeatLine.Issues' doc comment in internal/profiler/heat.go) — it lives
// here, applied once after BuildHeatmap returns, so a heatmap and the
// issues store never disagree about an issue's current status.
//
// Returns a Warnings-shaped string describing a REAL degradation (the store
// exists but couldn't be opened/read); a store that simply doesn't exist
// yet — the common case before any issue has ever been recorded — is not a
// degradation and returns "".
func applyIssueOverlay(hm *profiler.Heatmap) string {
	if hm == nil || len(hm.Functions) == 0 {
		return ""
	}
	path, err := issues.ResolvePath("")
	if err != nil {
		return ""
	}
	if _, statErr := os.Stat(path); statErr != nil {
		return "" // no store yet -- not a degradation
	}
	store, err := issues.OpenReadOnly(path)
	if err != nil {
		return fmt.Sprintf("issues overlay skipped: %s", err.Error())
	}
	defer func() { _ = store.Close() }()

	list, err := store.List(issues.ListOptions{Kind: issues.KindException})
	if err != nil {
		return fmt.Sprintf("issues overlay skipped: %s", err.Error())
	}

	for i := range hm.Functions {
		f := &hm.Functions[i]
		if f.File == "" {
			continue
		}
		for _, iss := range list {
			if iss.Culprit == nil || iss.Culprit.Line <= 0 || iss.Culprit.File == "" {
				continue
			}
			if !heatFileMatchesCulprit(f.File, iss.Culprit.File) {
				continue
			}
			if f.StartLine > 0 && (iss.Culprit.Line < f.StartLine || iss.Culprit.Line > f.EndLine) {
				continue
			}
			for li := range f.Lines {
				if f.Lines[li].Line != iss.Culprit.Line {
					continue
				}
				f.Lines[li].Issues = append(f.Lines[li].Issues, profiler.HeatLineIssue{
					ShortID: issueShortID(iss.ID),
					Count:   int(iss.OccurrenceCount),
					Status:  string(iss.Status),
				})
			}
		}
	}
	return ""
}

// heatFileMatchesCulprit reports whether a heatmap function's own File and
// an issue's Culprit.File name the same source file, tolerating a
// root-relative form on one side and an absolute one on the other (a
// heatmap function's File can be either, depending on how it was resolved —
// see heat.go's range-resolution comments — while stacktrace.Frame.Filename
// is always git-root-relative). Exact match first; otherwise one is
// accepted as a path SUFFIX of the other, split only on "/" boundaries
// (never a bare substring match, which could otherwise conflate
// "workload.js" with "other_workload.js").
func heatFileMatchesCulprit(heatFile, culpritFile string) bool {
	a := filepath.ToSlash(strings.TrimSpace(heatFile))
	b := filepath.ToSlash(strings.TrimSpace(culpritFile))
	if a == "" || b == "" {
		return false
	}
	if a == b {
		return true
	}
	return strings.HasSuffix(a, "/"+b) || strings.HasSuffix(b, "/"+a)
}

// issueShortID derives an issue's display short_id from its full store ID —
// docs/contracts/issue-context-v1.md's "uppercase first 4 hex chars of the
// id's hex portion" (store.go builds ID as "ISS-" + strings.ToUpper(
// fingerprint[:16]), so the hex portion is already uppercase and stripping
// the prefix is all that's needed).
func issueShortID(id string) string {
	id = strings.TrimPrefix(id, "ISS-")
	if len(id) > 4 {
		return id[:4]
	}
	return id
}

// issuesColumnForFunction renders the TOTAL/SELF/FUNCTION/LOCATION/ISSUES
// table's ISSUES cell for one function: "-" when no line in the function
// carries an overlay, else every distinct short_id across its lines
// (deduplicated, first-seen order), comma-joined and capped at 2 with a
// "+N more" tail so one function with many issues can't blow out the
// table's column alignment.
func issuesColumnForFunction(f profiler.HeatFunction) string {
	var ids []string
	seen := map[string]bool{}
	for _, l := range f.Lines {
		for _, iss := range l.Issues {
			if seen[iss.ShortID] {
				continue
			}
			seen[iss.ShortID] = true
			ids = append(ids, iss.ShortID)
		}
	}
	if len(ids) == 0 {
		return "-"
	}
	const maxShown = 2
	if len(ids) <= maxShown {
		return strings.Join(ids, ",")
	}
	return fmt.Sprintf("%s,+%d more", strings.Join(ids[:maxShown], ","), len(ids)-maxShown)
}
