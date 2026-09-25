package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	gopsutilProcess "github.com/shirou/gopsutil/v4/process"

	"github.com/abdul-hamid-achik/monitor/internal/devrun"
	"github.com/abdul-hamid-achik/monitor/internal/procbind"
	"github.com/abdul-hamid-achik/monitor/internal/profiler"
	"github.com/abdul-hamid-achik/monitor/internal/project"
)

// runHotService is `monitor hot <service>` (E3.2): a symbolic name is
// looked up in the launch registry a `monitor run --name <service> --
// <cmd>` invocation wrote (the naming ADR §8),
// resolved to its real runtime leaf process (procbind.ResolveLeaf, from
// the registered LAUNCHED pid), and captured live through the exact same
// captureRuntimeAwareProfile + BuildHeatmap + renderHotLiveHuman pipeline
// runHotPID (hot.go) uses for a numeric pid target -- this file adds only
// the service-name resolution step in front of it, never touching that
// shared rendering.
//
// The one behavior a numeric pid target cannot offer: when the resolved
// leaf is a registered --inspect inspector (a Node/Deno child), its
// already-known inspector address is passed straight through as
// captureRuntimeAwareProfile's inspectAddr, skipping argv-based
// auto-detection entirely -- which would find nothing anyway, since
// --inspect is injected via NODE_OPTIONS (an environment variable), never
// as an argv flag procbind.Inspect's extractInspectAddr could parse.
func runHotService(cmd *cobra.Command, name, projectFlag, funcName, ptypeFlag, export, pprofAddr string, top int, duration time.Duration) error {
	heatType, err := parseHotType(ptypeFlag)
	if err != nil {
		return err
	}
	if duration <= 0 || duration > 2*time.Minute {
		return fmt.Errorf("--duration must be greater than zero and at most 2m")
	}

	projectSlug := resolveHotServiceProject(projectFlag, name)

	entry, err := devrun.ReadRegistryEntry(projectSlug, name)
	if err != nil || !pidIsAlive(entry.PID) {
		printUnknownService(cmd, projectSlug, name)
		os.Exit(2)
		return nil
	}

	ctx, cancel := Context()
	defer cancel()

	binding, candidates, err := procbind.ResolveLeaf(ctx, int32(entry.PID), procbind.LeafOptions{})
	if err != nil {
		var ambiguous *procbind.AmbiguousLeafError
		if errors.As(err, &ambiguous) {
			printAmbiguousLeaf(cmd, int32(entry.PID), candidates)
			os.Exit(2)
			return nil
		}
		return fmt.Errorf("monitor hot %s: %w", name, err)
	}

	// Same heap/goroutine-needs-a-Go-pprof-target guard runHotPID applies
	// (hot.go) -- see its own doc comment for why this must be refused
	// BEFORE any capture is attempted.
	if heatType != profiler.HeatCPU && isJSRuntime(binding.Runtime) {
		return fmt.Errorf("monitor hot %s: --type %s needs a Go pprof target; %s has no per-line heap/goroutine detail here — "+
			"use `monitor profile %d -t heap` (function-level only) instead", name, ptypeFlag, binding.Runtime, binding.PID)
	}

	inspectAddr, _ := registeredInspectAddr(entry, binding.PID)

	addrExplicit := cmd.Flags().Changed("pprof-addr")
	prof, method, step := captureRuntimeAwareProfile(ctx, binding.PID, &binding, heatTypeToProfileType(heatType), pprofAddr, inspectAddr, addrExplicit, duration, false)
	sampleFallback := false
	if step.Status != stepOK {
		if heatType == profiler.HeatCPU && runtime.GOOS == "darwin" && !isJSRuntime(binding.Runtime) {
			if sampleProf, _, sampleStep := captureRuntimeAwareProfile(ctx, binding.PID, &binding, profiler.ProfileSample, pprofAddr, "", addrExplicit, duration, false); sampleStep.Status == stepOK {
				prof, method, step = sampleProf, "sample", sampleStep
				sampleFallback = true
			}
		}
		if step.Status != stepOK {
			msg := step.Limitation
			if recovery := serviceRecoveryHint(name, step.Recovery); recovery != "" {
				msg = fmt.Sprintf("%s (%s)", msg, recovery)
			}
			return fmt.Errorf("%s", msg)
		}
	}
	defer discardTempProfilePath(&prof)

	var hm *profiler.Heatmap
	var src *profiler.Source
	if sampleFallback {
		hm, err = profiler.BuildHeatmapFromSample(ctx, prof, profiler.HeatOptions{
			Func: funcName, Top: top, Runtime: string(binding.Runtime),
			CodeRoots: []string{binding.CodebaseRoot}, // SEC-8: the leaf's own codebase stays readable when monitor runs elsewhere
		})
		if err != nil {
			return fmt.Errorf("monitor hot %s: %w", name, err)
		}
	} else {
		var cleanup func()
		src, cleanup, err = profilerSourceFromCapture(prof)
		if err != nil {
			return fmt.Errorf("monitor hot %s: %w", name, err)
		}
		defer cleanup()

		hm, err = profiler.BuildHeatmap(ctx, src, profiler.HeatOptions{
			Func: funcName, Top: top, ProfileType: heatType, Runtime: string(binding.Runtime),
			CodeRoots: []string{binding.CodebaseRoot}, // SEC-8: the leaf's own codebase stays readable when monitor runs elsewhere
		})
		if err != nil {
			return err
		}
	}
	if funcName != "" && !hasHeatFunction(hm, funcName) {
		return fmt.Errorf("function %q not found in service %q", funcName, name)
	}
	overlaySlug := resolveHeatProjectSlug(firstNonEmpty(binding.CodebaseRoot, binding.Cwd), binding.Name)
	if warn := applyIssueOverlay(hm, overlaySlug); warn != "" {
		hm.Warnings = append(hm.Warnings, warn)
	}

	if export != "" {
		if src == nil && !strings.HasSuffix(strings.ToLower(export), ".json") {
			return fmt.Errorf("--export %s is not available for a macOS sample capture (function-level only; no raw profile bytes to save) — export the JSON heatmap instead (--export out.json)", export)
		}
		if err := exportHot(src, hm, export); err != nil {
			return err
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "saved: %s\n", export)
	}

	if JSONOutput(cmd) {
		return WriteJSON(hm)
	}

	header := hotServiceHeaderLine(ctx, name, int32(entry.PID), binding, method, pprofAddr, inspectAddr, heatType, duration)
	next := hotServiceNextHint(name, ptypeFlag, duration, cmd.Flags().Changed("duration"))
	return renderHotLiveHuman(cmd.OutOrStdout(), header, hm, funcName, next)
}

// resolveHotServiceProject resolves the project a <service> name is
// looked up under: --project when given, else project.Resolve applied to
// the current working directory -- the SAME resolution devrun.go's Run
// performs when it WRITES the registry entry in the first place, down to
// two hints that both matter here:
//
//   - PID: 1. project.Hints.PID<=0 means "a host-wide event with no
//     process attached", which would otherwise skip the git-root/marker
//     walk entirely and always resolve to project "host" regardless of
//     Dir/UseWorkingDir (verified live: without this, `monitor hot
//     <service>` run from the exact same directory a service was
//     registered from still failed to find it, because Resolve
//     short-circuited to "host" before ever looking at the working
//     directory).
//   - ExplicitService: name. Inside a directory with no git root and no
//     package manifest (project.Resolve's rules 3/4 both find nothing),
//     rule 5 falls back to project.Hints.ExplicitService VERBATIM --
//     project.Resolve's own doc comment calls this out explicitly. Since
//     `monitor run --name w -- <cmd>` passes its OWN --name as
//     ExplicitService when it resolves the SAME identity to write the
//     registry entry, project there resolves to "w" too in that exact
//     shape (no git/marker root). Leaving this hint out here (as an
//     earlier version of this function did) reproduces a DIFFERENT bug
//     than the PID one above -- not "always host", but "always local":
//     `monitor run --name w -- <cmd>` from a plain, non-git scratch
//     directory registers project "w", while `monitor hot w` from that
//     same directory (with no --name of its own to pass) resolved
//     project "local" instead and never found it (verified live). Passing
//     the SAME name being looked up as ExplicitService here closes that
//     gap exactly: it only ever takes effect once rules 1-4 (explicit
//     --project, $MONITOR_PROJECT, git root, marker root) have all
//     already found nothing, so a git-rooted or manifest-rooted project
//     (the common case) is completely unaffected.
func resolveHotServiceProject(explicit, name string) string {
	if p := strings.TrimSpace(explicit); p != "" {
		return p
	}
	return project.Resolve(project.Hints{UseWorkingDir: true, PID: 1, ExplicitService: name}).Slug
}

// pidIsAlive reports whether pid names a currently-running process --
// registry.go's own doc comment on "stale entries": an entry whose pid
// has exited must be treated exactly like no entry at all, never
// resolved against a reused pid that now belongs to an unrelated process.
func pidIsAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	alive, err := gopsutilProcess.PidExists(int32(pid))
	return err == nil && alive
}

// printUnknownService is `monitor hot <service>`'s "never guess" error
// path (the naming ADR §8): an unregistered or
// stale service name exits 2 (the caller does this; see runHotService)
// after listing every service CURRENTLY registered AND alive for
// projectSlug, the same posture procbind.AmbiguousLeafError's candidate
// list already uses for an ambiguous pid target.
func printUnknownService(cmd *cobra.Command, projectSlug, name string) {
	live := liveRegisteredServiceNames(projectSlug)
	if len(live) == 0 {
		fmt.Fprintf(cmd.ErrOrStderr(),
			"no service named %q is registered for project %q (nothing is currently registered; launch one with `monitor run --name %s -- <cmd>`)\n",
			name, projectSlug, name)
		return
	}
	fmt.Fprintf(cmd.ErrOrStderr(), "no service named %q is registered for project %q; registered services:\n", name, projectSlug)
	for _, n := range live {
		fmt.Fprintf(cmd.ErrOrStderr(), "  %s\n", n)
	}
}

// liveRegisteredServiceNames is ListRegisteredServiceNames filtered to
// entries whose pid is still alive -- a stale (dead-pid) entry is exactly
// as unregistered as one that was never written, so it must not appear in
// an "unknown service" error's candidate list either. It also opportunistically
// removes every stale entry it finds (the naming ADR
// §8's "ignored/cleaned by readers" rule): a launch killed by SIGKILL can
// never clean up its own registry entry on the way out, so without this a
// dead service accumulates in the registry forever, silently outliving
// every process that ever wrote one.
func liveRegisteredServiceNames(projectSlug string) []string {
	names := devrun.ListRegisteredServiceNames(projectSlug)
	live := make([]string, 0, len(names))
	for _, n := range names {
		entry, err := devrun.ReadRegistryEntry(projectSlug, n)
		if err != nil {
			continue
		}
		if pidIsAlive(entry.PID) {
			live = append(live, n)
			continue
		}
		if path, perr := devrun.RegistryPath(projectSlug, n); perr == nil {
			devrun.RemoveRegistryEntry(path, entry.LaunchID)
		}
	}
	return live
}

// registeredInspectAddr looks up entry.Inspectors for a banner whose
// resolved pid matches leafPID, returning it as a "host:port" address
// captureRuntimeAwareProfile's inspectAddr accepts directly. This is the
// ONLY way `monitor hot <service>` can find a Node/Deno target's inspector
// at all: --inspect is injected via NODE_OPTIONS (an environment
// variable), so procbind.Inspect's own argv-based extractInspectAddr never
// sees it, and the OS picked an ephemeral port (--inspect=127.0.0.1:0)
// that nothing but the recorded banner (or a fresh CDP handshake attempt)
// could name.
func registeredInspectAddr(entry devrun.RegistryEntry, leafPID int32) (string, bool) {
	for _, insp := range entry.Inspectors {
		if insp.PID != 0 && insp.PID == leafPID && insp.Port > 0 {
			return fmt.Sprintf("127.0.0.1:%d", insp.Port), true
		}
	}
	return "", false
}

// serviceRecoveryHint adapts captureRuntimeAwareProfile's shared
// step.Recovery text (investigate.go) for the service-name path: that
// text is written for `monitor profile`/`monitor investigate`, both of
// which have their own `--inspect-addr` flag to override auto-detection --
// `monitor hot <service>` has no such flag (it resolves the inspector from
// the launch registry instead, see registeredInspectAddr), so surfacing
// that text verbatim here points the caller at a flag that does not exist
// on this command (verified live: `monitor hot x` on an un-inspected node
// service printed "...the CLI also accepts --inspect-addr..." twice, and
// following it was a dead end). name is the registered leaf's kind of
// thing to relaunch: --inspect only helps a Node/Deno target, so the
// rewritten hint always names the ACTUAL fix for a service target --
// relaunching registered with --inspect -- rather than a CLI flag that
// belongs to a different command.
func serviceRecoveryHint(name, recovery string) string {
	if strings.Contains(recovery, "--inspect-addr") || strings.Contains(recovery, "--inspect=") {
		return fmt.Sprintf("relaunch it with `monitor run --name %s --inspect -- <cmd>` so `monitor hot %s` can find its inspector", name, name)
	}
	return recovery
}

// hotServiceHeaderLine renders the roadmap mockup's own `monitor hot
// <service>` banner: "monitor > <name> = <runtime> pid <N> (child of
// <wrapper> <M>; monitor run --inspect) · inspector <addr> · sampling
// <duration>". Deliberately its own function rather than a reuse of
// hot.go's hotLiveHeaderLine: <name> here is always the REGISTERED
// service name (never omitted the way hotLiveHeaderLine's MainScript-
// derived label is when unknown), and the "; monitor run --inspect"
// clause only makes sense for a registry-sourced inspector, which a plain
// numeric-pid target never has.
func hotServiceHeaderLine(ctx context.Context, name string, launchedPID int32, binding procbind.Binding, method, pprofAddr, inspectAddr string, heatType profiler.HeatProfileType, duration time.Duration) string {
	usedRegisteredInspector := inspectAddr != ""
	who := fmt.Sprintf("%s pid %d", binding.Runtime, binding.PID)
	if launchedPID != binding.PID {
		label := "wrapper"
		if root, err := procbind.Inspect(ctx, launchedPID, ""); err == nil && root.Name != "" {
			label = root.Name
		}
		if usedRegisteredInspector {
			who += fmt.Sprintf(" (child of %s %d; monitor run --inspect)", label, launchedPID)
		} else {
			who += fmt.Sprintf(" (child of %s %d)", label, launchedPID)
		}
	}
	who = name + " = " + who

	parts := []string{"monitor > " + who}
	switch {
	case strings.HasPrefix(method, "inspector"):
		switch {
		case binding.InspectAddr != "":
			parts = append(parts, "inspector "+binding.InspectAddr)
		case usedRegisteredInspector:
			// binding.InspectAddr is only ever populated by argv-based
			// auto-detection (procbind.Inspect), which --inspect via
			// NODE_OPTIONS never reaches -- see registeredInspectAddr's
			// doc comment. The address actually used (passed to
			// captureRuntimeAwareProfile as inspectAddr, not through
			// binding) still belongs in the header.
			parts = append(parts, "inspector "+inspectAddr)
		}
	case strings.HasPrefix(method, "pprof"):
		addr := pprofAddr
		if addr == "" {
			addr = profiler.DefaultPprofAddr
		}
		parts = append(parts, "pprof "+addr)
	case method == "sample":
		parts = append(parts, "macOS sample")
	}
	switch {
	case method == "sample":
		// LUX-15: captureSample runs `sample <pid> <secs>` with the
		// clamped request, so the banner prints that same clamped
		// value -- what a person reads is what actually ran.
		parts = append(parts, fmt.Sprintf("sampling %ds", profiler.SampleSeconds(duration)))
	case heatType == profiler.HeatCPU:
		parts = append(parts, "sampling "+duration.String())
	default:
		parts = append(parts, "instant snapshot")
	}
	return strings.Join(parts, " · ")
}

// hotServiceNextHint mirrors hot.go's hotLiveNextHint for a service
// target: the base command is `monitor hot <service>` (never a bare pid,
// which the caller would have to re-look-up in the registry every time),
// plus whichever non-default flags this exact invocation used.
func hotServiceNextHint(name, ptypeFlag string, duration time.Duration, durationExplicit bool) string {
	next := "monitor hot " + name
	if ptypeFlag != "" && ptypeFlag != "cpu" {
		next += " --type " + ptypeFlag
	}
	if durationExplicit {
		next += " --duration " + duration.String()
	}
	return next
}
