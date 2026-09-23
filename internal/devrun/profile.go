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
// syntax, no dependencies) since both --require and --preload load it
// before any of the target's own module resolution/transpilation is set
// up.
//
// Why "only when we are the sole listener": merely registering ANY
// listener for SIGINT already changes Node's default disposition from
// "terminate immediately, running exit hooks" to "run every registered
// listener and then do nothing further automatically" -- so a shim that
// always called process.exit() unconditionally would override an
// application's OWN SIGINT/SIGTERM handler (e.g. one that drains
// in-flight requests before exiting), which is exactly the kind of
// unrequested behavior change docs/contracts/local-sentry-naming.md's "no
// se inyecta ... salvo por entorno y en modo append" golden rule forbids.
// Checking process.listeners(sig).length === 1 at the moment the signal
// actually fires (by which point every listener -- ours, loaded first via
// --require/--preload, and any the application added later at module load
// time -- is already registered) tells the shim whether it is truly alone;
// if so, nothing else was ever going to make the process exit gracefully,
// so it does the one thing --cpu-prof's exit hook needs: call
// process.exit(0). When the application has its own handler(s), this
// resolves to a no-op and lets them run exactly as if the shim were not
// loaded at all.
const exitShimScript = `'use strict';
function monitorExitOnSignal(sig) {
  return function () {
    if (process.listeners(sig).length === 1) {
      process.exit(0);
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
)

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
			cpuProf := fmt.Sprintf("--cpu-prof --cpu-prof-dir=%s", dir)
			out.env = appendRuntimeOption(out.env, "NODE_OPTIONS", cpuProf+" --require "+shim)
			out.env = appendRuntimeOption(out.env, "BUN_OPTIONS", cpuProf+" --preload "+shim)
		}
	}

	return out, nil
}

// newestProfileFile returns the most recently modified *.cpuprofile file
// directly under dir -- --cpu-prof-dir writes exactly one file per
// process that actually reached its exit hook, named with a timestamp and
// pid monitor does not need to parse itself.
func newestProfileFile(dir string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}
	var best string
	var bestMod time.Time
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".cpuprofile") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if best == "" || info.ModTime().After(bestMod) {
			best = filepath.Join(dir, e.Name())
			bestMod = info.ModTime()
		}
	}
	if best == "" {
		return "", os.ErrNotExist
	}
	return best, nil
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
