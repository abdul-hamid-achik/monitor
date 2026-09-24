// profile.go implements E3.3b's `monitor run --profile`: node and bun both
// write a V8-format .cpuprofile at exit when launched with
// --cpu-prof --cpu-prof-dir=<dir> (NODE_OPTIONS for node, BUN_OPTIONS for
// bun -- both verified live), but neither one actually reaches that exit
// path on a bare Ctrl-C unless something in the process registers a
// signal handler of its own (verified live: without the shim below,
// SIGINT kills node with no profile at all, and bun does not even react
// to SIGINT -- it hangs until SIGKILL). exitShimScript is the fix: loaded
// via NODE_OPTIONS' --require / BUN_OPTIONS' --preload, it calls
// process.exit() on SIGINT/SIGTERM, but ONLY when it is still the sole
// listener for that signal -- an application that registers its own
// handler is left completely alone (see exitShimScript's own comment).
//
// Deno has no env-injectable equivalent (verified live: NODE_OPTIONS'
// --cpu-prof has no effect at all under deno, silently), so --profile is
// skipped for it with a one-line, honest note; live profiling for Deno
// still works via --inspect + `monitor hot <service>`.
package devrun

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/abdul-hamid-achik/monitor/internal/profiler"
)

// exitShimScript is loaded via --require (node) or --preload (bun) under
// --profile. It must stay a plain CommonJS module (no import/export
// syntax, no dependencies -- `os` is a builtin, always available even this
// early) since both --require and --preload load it before any of the
// target's own module resolution/transpilation is set up.
//
// Why "only when no REAL application handler is present": merely
// registering ANY listener for SIGINT already changes Node's default
// disposition from "terminate immediately" to "run every registered
// listener and then do nothing further automatically" -- so a shim that
// always called process.exit() unconditionally would override an
// application's OWN SIGINT/SIGTERM handler (e.g. one that drains
// in-flight requests before exiting), which is exactly the kind of
// unrequested behavior change docs/contracts/local-sentry-naming.md's "no
// se inyecta ... salvo por entorno y en modo append" golden rule forbids.
//
// A naive "am I the ONLY listener" check (process.listeners(sig).length
// === 1) deadlocks against signal-exit (npm's most common process-exit
// hygiene library -- pulled in transitively by execa, ora/restore-cursor,
// foreground-child, write-file-atomic, ...; verified live against the real
// signal-exit@4.1.0 from npm, both under node and bun): signal-exit
// registers its own single, shared listener and, on signal, ONLY re-raises
// it (so the process actually terminates) when `process.listeners(sig)`
// reports EXACTLY as many listeners as it itself tracks having installed
// (globalThis[Symbol.for('signal-exit emitter')].count) -- our shim's own
// listener makes that count one too many, so signal-exit backs off and
// waits for "whoever else is listening" (us) to decide, while our OLD,
// naive check saw 2 listeners (itself + signal-exit's) and, symmetrically,
// assumed some OTHER real handler must be present and backed off too.
// Both sides deferring to each other is the deadlock: neither SIGINT nor
// SIGTERM ever terminates the process, and no profile is ever written
// (confirmed live: only SIGKILL got the process to exit, well after
// --profile's whole reason for existing -- letting a bare Ctrl-C still
// flush a profile -- had already failed).
//
// The fix does not special-case any one signal-exit version's internals
// beyond the one thing it deliberately exposes for exactly this kind of
// interop: the shared Emitter singleton it stores on
// globalThis[Symbol.for('signal-exit emitter')], whose `count` is how many
// independent signal-exit instances (across however many copies of the
// package are nested in node_modules) are currently loaded -- every one of
// which registers exactly one process-level listener. Treating "our own
// listener, plus every listener signal-exit itself accounts for" as the
// non-application baseline (rather than requiring we be totally alone)
// correctly recognizes there is still no REAL application handler once
// that baseline is all that is left, so the shim can safely finish what
// signal-exit itself declined to: call process.exit() so --cpu-prof's exit
// hook actually fires. When an application-level handler DOES exist (the
// listener count exceeds that baseline), this still resolves to a no-op,
// exactly as before -- verified live with an app that registers its own
// SIGINT handler (in addition to signal-exit) and drains before calling
// process.exit(7) itself: the shim never fires, and 7 is preserved.
//
// The exit code itself is 128 + the signal's number (matching the
// process's own conventional "killed by signal" exit status, e.g. 130 for
// SIGINT), not a bare 0: a run --profile terminated by Ctrl-C is not a
// clean exit, and reporting one would make monitor's own exit summary lie
// about it (see run_profile.yml).
const exitShimScript = `'use strict';
var os = require('os');
function monitorNonAppListenerBaseline() {
  // Our own listener (1) plus every signal-exit@4.x instance's own shared
  // listener, if any is loaded -- see this file's own doc comment for why
  // this is the correct "nobody is really watching this signal" baseline
  // instead of a naive "am I totally alone" check.
  var se = globalThis[Symbol.for('signal-exit emitter')];
  var seCount = (se && typeof se.count === 'number') ? se.count : 0;
  return 1 + seCount;
}
function monitorExitOnSignal(sig) {
  return function () {
    if (process.listeners(sig).length <= monitorNonAppListenerBaseline()) {
      var num = (os.constants && os.constants.signals && os.constants.signals[sig]) || 0;
      process.exit(128 + num);
    }
  };
}
process.on('SIGINT', monitorExitOnSignal('SIGINT'));
process.on('SIGTERM', monitorExitOnSignal('SIGTERM'));
`

// exitShimFileName is exitShimScript's file name under the shared shims
// directory (monitorStateSubdir("shims")). ".cjs" forces CommonJS
// interpretation regardless of any "type": "module" in a nearby
// package.json node's own module resolution might otherwise consult.
const exitShimFileName = "exit-on-signal.cjs"

// exitShimPath returns exitShimScript's on-disk path, writing it (mode
// 0600, in the private 0700 "shims" state directory) only if it is
// missing or stale -- this file's content is fixed per monitor build, so
// a fresh launch normally just reuses the one already on disk rather than
// rewriting it every time.
func exitShimPath() (string, error) {
	dir, err := monitorStateSubdir("shims")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, exitShimFileName)
	if data, err := os.ReadFile(path); err == nil && string(data) == exitShimScript {
		return path, nil
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(exitShimScript), 0o600); err != nil {
		return "", fmt.Errorf("write exit shim: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return "", fmt.Errorf("commit exit shim: %w", err)
	}
	return path, nil
}

// profileOutputDir returns (creating it, mode 0700) this launch's private
// --profile output directory: $XDG_STATE_HOME/monitor/profiles/<launchID>.
// Unlike the exit shim (shared, content-addressed by staying constant)
// each launch gets its OWN directory, so two concurrent `monitor run
// --profile` launches can never race on -- or worse, one silently
// overwrite -- the other's .cpuprofile.
func profileOutputDir(launchID string) (string, error) {
	return monitorStateSubdir(filepath.Join("profiles", launchID))
}

// appendRuntimeOption finds env's SINGLE "name=value" entry and appends
// opt to its value (space-separated, matching NODE_OPTIONS/BUN_OPTIONS'
// own space-separated flag syntax -- see env.go's BuildEnv/appendOption,
// which this mirrors exactly), or adds a new "name=opt" entry when none
// exists. Applied as an independent second pass over BuildEnv's own
// output (rather than folded into BuildEnv itself) so --inspect and
// --profile can each contribute without BuildEnv needing to know about
// either -- both may fire in the same launch, and calling this twice in a
// row (once per feature) correctly finds and extends the entry the first
// call already added.
func appendRuntimeOption(env []string, name, opt string) []string {
	for i, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || k != name {
			continue
		}
		if v == "" {
			env[i] = name + "=" + opt
		} else {
			env[i] = name + "=" + v + " " + opt
		}
		return env
	}
	return append(env, name+"="+opt)
}

// runtimeAugmentation is applyInspectAndProfile's result: the (possibly
// further-modified) env slice, --profile's private output directory (""
// when --profile was not requested or was skipped for this runtime), and
// any one-line, honest notes to print (Bun+--inspect, Deno+--profile).
type runtimeAugmentation struct {
	env        []string
	profileDir string
	notes      []string
}

// bunInspectNote and denoProfileNote are the two documented, verified
// limitations E3.3b calls for instead of silently doing nothing.
const (
	bunInspectNote  = "Bun's inspector speaks JSC, not V8 CDP -- --inspect has no effect for monitor's profiler here; use --profile instead"
	denoProfileNote = "Deno has no env-injectable --cpu-prof yet -- monitor cannot write a profile at exit for it; use --inspect + `monitor hot <service>` for live CPU profiling instead"
	// bunProfileSpacePathNote fires when $XDG_STATE_HOME (or wherever the
	// per-launch profile dir / shared exit shim ends up) contains a space:
	// verified live that BUN_OPTIONS cannot carry such a path at all --
	// unlike NODE_OPTIONS (which parses a double-quoted value correctly,
	// with or without a space), Bun does not strip quote characters from a
	// BUN_OPTIONS token, so even a QUOTED path with no space in it fails to
	// resolve ("error: Module not found" for the literal, quote-including
	// string); an unquoted path that actually contains a space is worse
	// still -- Bun splits it into two argv-shaped tokens at the space and
	// exits 1 before the target script ever runs at all, which is exactly
	// the "monitoring must never be able to break the monitored process"
	// golden rule this note exists to avoid violating. There is no known
	// quoting scheme that works, so BUN_OPTIONS' own --cpu-prof/--preload
	// are simply left unset in this case (NODE_OPTIONS is unaffected and
	// still gets a working, quoted profile) and this note explains why.
	bunProfileSpacePathNote = "monitor's state directory contains a space, which Bun's BUN_OPTIONS cannot carry as a path at all (verified live, even quoted) -- --profile is skipped for Bun this launch; move XDG_STATE_HOME to a path with no spaces to use --profile with Bun"
)

// quoteForNodeOptions double-quotes val for a NODE_OPTIONS entry. Verified
// live that Node parses a double-quoted NODE_OPTIONS value correctly
// whether or not it actually contains a space, so every path this package
// injects into NODE_OPTIONS is quoted unconditionally -- the alternative,
// leaving a path unquoted, is what let a space in $XDG_STATE_HOME send
// Node's --cpu-prof-dir a truncated, wrong path (Node treats the text after
// the first space as a SEPARATE option) and silently write the profile
// into a newly created, wrongly-permissioned directory instead of the
// private one this package created. This must never be used for
// BUN_OPTIONS -- see bunProfileSpacePathNote's own doc comment for why
// quoting breaks Bun instead of fixing it.
func quoteForNodeOptions(val string) string {
	return `"` + val + `"`
}

// hasWhitespace reports whether s contains a character BUN_OPTIONS' own
// (undocumented, verified-live) tokenizer would split on.
func hasWhitespace(s string) bool {
	return strings.ContainsAny(s, " \t\n")
}

// applyInspectAndProfile is `monitor run --inspect`/`--profile`'s env
// pipeline, applied to BuildEnv's own output. argv0Base is the launched
// command's base name (e.g. "node", "bun", "deno", or a wrapper like
// "yarn") -- checked only to special-case the two DOCUMENTED, verified
// limitations above; every other case (including every wrapper, whose
// eventual real leaf is unknown at launch time) gets both options applied
// unconditionally, because NODE_OPTIONS/BUN_OPTIONS are inherited down the
// whole process tree and are a harmless no-op for whichever runtime never
// reads them (verified live: bun ignores NODE_OPTIONS outright, even a
// broken one; node and deno have never heard of BUN_OPTIONS at all).
func applyInspectAndProfile(env []string, opts Options, argv0Base, launchID string) (runtimeAugmentation, error) {
	out := runtimeAugmentation{env: env}
	runtime := strings.ToLower(strings.TrimSpace(argv0Base))

	if opts.Inspect {
		if runtime == "bun" {
			out.notes = append(out.notes, bunInspectNote)
		} else {
			out.env = appendRuntimeOption(out.env, "NODE_OPTIONS", "--inspect=127.0.0.1:0")
		}
	}

	if opts.Profile {
		if runtime == "deno" {
			out.notes = append(out.notes, denoProfileNote)
		} else {
			shim, err := exitShimPath()
			if err != nil {
				return out, fmt.Errorf("--profile: %w", err)
			}
			dir, err := profileOutputDir(launchID)
			if err != nil {
				return out, fmt.Errorf("--profile: %w", err)
			}
			out.profileDir = dir

			// NODE_OPTIONS: always quoted (see quoteForNodeOptions) --
			// correct with or without a space in dir/shim.
			nodeCPUProf := fmt.Sprintf("--cpu-prof --cpu-prof-dir=%s", quoteForNodeOptions(dir))
			out.env = appendRuntimeOption(out.env, "NODE_OPTIONS", nodeCPUProf+" --require "+quoteForNodeOptions(shim))

			// BUN_OPTIONS: never quoted (quoting itself breaks Bun's own
			// parsing -- see bunProfileSpacePathNote) and skipped entirely,
			// with an honest note, when either path actually contains
			// whitespace Bun cannot carry any other way.
			if hasWhitespace(dir) || hasWhitespace(shim) {
				out.notes = append(out.notes, bunProfileSpacePathNote)
			} else {
				bunCPUProf := fmt.Sprintf("--cpu-prof --cpu-prof-dir=%s", dir)
				out.env = appendRuntimeOption(out.env, "BUN_OPTIONS", bunCPUProf+" --preload "+shim)
			}
		}
	}

	return out, nil
}

// profileCandidate is one *.cpuprofile file found directly under a
// --cpu-prof-dir, before it is picked between (see bestProfileFile).
type profileCandidate struct {
	path string
	mod  time.Time
}

// listProfileFiles lists every *.cpuprofile file directly under dir.
func listProfileFiles(dir string) ([]profileCandidate, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]profileCandidate, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".cpuprofile") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, profileCandidate{path: filepath.Join(dir, e.Name()), mod: info.ModTime()})
	}
	return out, nil
}

// newestProfileFile returns the most recently modified *.cpuprofile file
// directly under dir -- bestProfileFile's fallback when every candidate
// fails to parse as an actual profile (see its own doc comment), and this
// package's own tests' baseline.
func newestProfileFile(dir string) (string, error) {
	candidates, err := listProfileFiles(dir)
	if err != nil {
		return "", err
	}
	var best string
	var bestMod time.Time
	for _, c := range candidates {
		if best == "" || c.mod.After(bestMod) {
			best, bestMod = c.path, c.mod
		}
	}
	if best == "" {
		return "", os.ErrNotExist
	}
	return best, nil
}

// bestProfileFile picks which *.cpuprofile under dir actually belongs to
// the launched APPLICATION rather than an intermediate wrapper that
// happens to inherit the very same NODE_OPTIONS/BUN_OPTIONS -- npm, or
// yarn re-execing into its own node instance, both of which are
// themselves node/bun processes and therefore ALSO reach --cpu-prof's own
// exit hook and write their OWN, separate profile into this SAME
// per-launch directory. Picking "whichever file is newest" (this
// package's original approach) is wrong exactly when the wrapper happens
// to write its own file LAST: verified live with `monitor run --profile
// -- npm start` (a script that runs `node workload.js`), where npm's own
// profile (nearly idle -- it spent almost its entire short life blocked
// waiting on its child) landed after the child's and was the one
// summarized, while the actual workload's profile sat unused in the same
// directory.
//
// When more than one candidate exists, this loads each
// (profiler.LoadFile + BuildHeatmap) and picks the one with the most
// Heatmap.ActiveSamples: a wrapper that merely waited on its child spends
// nearly all of its (short) life idle/blocked in a syscall, so it can
// never accumulate meaningful CPU samples the way the actual workload
// does, regardless of which one happened to flush its file last. A file
// that fails to load (corrupt, or simply not a profile -- as every real
// .cpuprofile file WILL be here, but this package's own tests write
// placeholder "{}" files) is skipped rather than erroring the whole pick
// out; if every candidate fails to load, this falls back to
// newestProfileFile's plain mtime pick rather than reporting no profile at
// all when --cpu-prof-dir clearly did write something.
func bestProfileFile(ctx context.Context, dir string) (string, error) {
	candidates, err := listProfileFiles(dir)
	if err != nil {
		return "", err
	}
	if len(candidates) == 0 {
		return "", os.ErrNotExist
	}
	if len(candidates) == 1 {
		return candidates[0].path, nil
	}

	var best string
	var bestActive int
	var bestMod time.Time
	for _, c := range candidates {
		active, ok := activeSampleCount(ctx, c.path)
		if !ok {
			continue
		}
		if best == "" || active > bestActive || (active == bestActive && c.mod.After(bestMod)) {
			best, bestActive, bestMod = c.path, active, c.mod
		}
	}
	if best != "" {
		return best, nil
	}
	// Every candidate failed to load: fall back to the plain mtime pick
	// rather than claiming no profile was written at all.
	return newestProfileFile(dir)
}

// activeSampleCount loads path and reports its Heatmap.ActiveSamples; ok is
// false when path could not be loaded as a profile at all (see
// bestProfileFile's own doc comment for why that is a "skip", not a fatal
// error, at this call site).
func activeSampleCount(ctx context.Context, path string) (int, bool) {
	src, err := profiler.LoadFile(path)
	if err != nil {
		return 0, false
	}
	hm, err := profiler.BuildHeatmap(ctx, src, profiler.HeatOptions{})
	if err != nil {
		return 0, false
	}
	return hm.ActiveSamples, true
}

// summarizeProfile loads path (a just-captured .cpuprofile) via
// profiler.LoadFile + profiler.BuildHeatmap and renders the roadmap's
// one-line exit summary: "cpu profile: <path> · hottest: <func>
// <file:line> (<pct>)". Degrades to naming the profile with no hot
// function named, rather than failing outright, when the profile carries
// no functions at all (e.g. the process exited within the first sampling
// tick) -- see Heatmap.DefaultTarget's own doc comment.
func summarizeProfile(ctx context.Context, path string) (string, error) {
	src, err := profiler.LoadFile(path)
	if err != nil {
		return "", fmt.Errorf("load %s: %w", path, err)
	}
	hm, err := profiler.BuildHeatmap(ctx, src, profiler.HeatOptions{})
	if err != nil {
		return "", fmt.Errorf("build heatmap for %s: %w", path, err)
	}
	if hm.DefaultTarget == nil {
		return fmt.Sprintf("cpu profile: %s (no hot function found)", path), nil
	}
	f := *hm.DefaultTarget
	loc := f.File
	if f.StartLine > 0 {
		loc = fmt.Sprintf("%s:%d", f.File, f.StartLine)
	}
	return fmt.Sprintf("cpu profile: %s · hottest: %s %s (%.0f%%)", path, f.Name, loc, f.SelfPct), nil
}
