package devrun

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

// Launch environment variable names (the naming ADR
// §2): the ONLY variables `monitor run -- <cmd>` may export for run
// correlation. MONITOR, MONITOR_RUN_DIR, MONITOR_RUN_ID and MONITOR_SERVICE
// are owned by the legacy `monitor run <spec>` / internal/contextids and
// must never be set here -- see legacyRunEnvNames below.
const (
	EnvLaunchID      = "MONITOR_LAUNCH_ID"
	EnvLaunchService = "MONITOR_LAUNCH_SERVICE"
	EnvLaunchRoot    = "MONITOR_LAUNCH_ROOT"
)

// legacyRunEnvNames are variables owned by `monitor run <spec>` (glyphrun)
// and internal/cli/watch.go's package init(), never by `run -- <cmd>`.
// watch.go's init() unconditionally calls os.Setenv("MONITOR", "1") in
// monitor's OWN process whenever MONITOR_RUN_DIR is empty -- since Go runs
// every package init() before main() runs for every monitor invocation, not
// just `watch`, os.Environ() already carries MONITOR=1 by the time `run --`
// builds the child's environment, unless it is explicitly stripped here
// (the naming ADR §2's "known gotcha"). Passing
// os.Environ() straight through as cmd.Env would silently violate the "run
// -- only exports MONITOR_LAUNCH_*" rule.
var legacyRunEnvNames = map[string]bool{
	"MONITOR":         true,
	"MONITOR_RUN_DIR": true,
}

// LaunchIDs is the resolved MONITOR_LAUNCH_* triple for one `run --`
// invocation.
type LaunchIDs struct {
	ID      string
	Service string
	Root    string
}

// ResolveLaunchIDs computes this launch's identity, honoring nesting the
// way the roadmap's own worked example resolves it (~/notes/projects/
// monitor/2026-09-22-local-sentry-roadmap.md, "Conflictos resueltos":
// "Anidamiento: `monitor run -- task dev` que lanza `monitor run --name
// web-api -- node …`. → MONITOR_LAUNCH_ROOT se hereda, y la DedupeKey en
// vivo es sha256(ROOT + hash del bloque)..."): when environ already carries
// a non-empty MONITOR_LAUNCH_ROOT -- this monitor process is itself running
// as the child of another `monitor run -- <cmd>` -- ONLY that root carries
// through, unchanged, so the whole chain shares one identity for the live
// DedupeKey's sake (the naming ADR §5: without this,
// parent and child would see the same underlying text and double-count the
// occurrence). ID and Service are always computed fresh for THIS launch:
// an inner `--name web-api` must take effect as MONITOR_LAUNCH_SERVICE
// exactly like the roadmap's own example names it, not be silently
// replaced by whatever the OUTER launch happened to be named -- E3.2's
// per-service launch registry depends on that too.
//
// ROOT semantics (the naming ADR §2, the
// dogfooding fix formerly tracked as "FIX 1"): when there is nothing to
// inherit -- environ carries no non-empty MONITOR_LAUNCH_ROOT, i.e. this is
// an OUTERMOST launch, not a nested one -- ROOT is this launch's own
// freshly minted ID, never a directory. A directory-shaped root used to
// mean "two independent sibling `monitor run --` launches in the same repo
// whose crash text happens to be textually identical within the dedupe
// window" collapsed into one occurrence, because they all shared the same
// git-root/cwd-derived MONITOR_LAUNCH_ROOT -- the live DedupeKey
// (detector.go's liveDedupeKey) is sha256(ROOT + hash(exception block)),
// so an identical ROOT for two unrelated launches folds their otherwise-
// distinct occurrences together. Seeding ROOT from ID instead means ROOT
// answers "which outermost launch produced this text", not "which
// directory it ran in": two sibling launches now mint two different IDs
// and therefore two different roots (no collapsing), while a genuinely
// nested launch (a task runner's `run --` spawning a real service's own
// `run --`) still shares one root end to end, because only the INNER
// launch's inheritance branch above ever fires -- the outer launch, being
// itself outermost, seeded that shared root from its own ID the same way.
func ResolveLaunchIDs(environ []string, name string) LaunchIDs {
	id := newLaunchID()
	root := id
	if inherited, ok := launchRootFromEnviron(environ); ok {
		root = inherited
	}
	return LaunchIDs{ID: id, Service: strings.TrimSpace(name), Root: root}
}

// launchRootFromEnviron reports environ's MONITOR_LAUNCH_ROOT, when
// non-empty -- the sole nesting signal (see ResolveLaunchIDs).
func launchRootFromEnviron(environ []string) (string, bool) {
	vals := envMap(environ)
	root := strings.TrimSpace(vals[EnvLaunchRoot])
	if root == "" {
		return "", false
	}
	return root, true
}

func envMap(environ []string) map[string]string {
	m := make(map[string]string, len(environ))
	for _, kv := range environ {
		name, value, ok := strings.Cut(kv, "=")
		if ok {
			m[name] = value
		}
	}
	return m
}

// newLaunchID returns a fresh random launch id: 12 random bytes, hex
// encoded -- the same shape internal/issues' randomID uses for issue/
// occurrence IDs. crypto/rand failing is exceptionally rare (no kernel
// entropy source); the fallback trades cryptographic randomness for "still
// unique enough for one process's lifetime" rather than leaving the launch
// completely unidentified.
func newLaunchID() string {
	var b [12]byte
	if _, err := rand.Read(b[:]); err == nil {
		return hex.EncodeToString(b[:])
	}
	return fmt.Sprintf("%024x", time.Now().UnixNano())
}

// BuildEnv derives the child's environment from environ (normally
// os.Environ()):
//
//   - strips the legacy MONITOR/MONITOR_RUN_DIR variables (legacyRunEnvNames)
//     and any pre-existing MONITOR_LAUNCH_* (replaced below), so the result
//     never carries a stale or foreign value for either;
//   - sets MONITOR_LAUNCH_ID/SERVICE/ROOT from launch;
//   - when appendSourceMaps is true, appends " --enable-source-maps" to
//     NODE_OPTIONS -- creating it if unset, but always APPENDING to an
//     existing value, never overwriting it (naming ADR's "never overwrite
//
// NODE_OPTIONS/BUN_OPTIONS/.../
//
//	  always append, or set only if unset" rule). appendSourceMaps false
//	  (--no-source-maps) leaves any existing NODE_OPTIONS completely alone;
//	- when scanStdout is true and PYTHONUNBUFFERED is not already set in
//	  environ, sets it to "1" -- a scanned stdout becomes a pipe, and CPython
//	  fully block-buffers stdout (rather than line-buffering it) once it is
//	  not a tty, which would otherwise delay the detector seeing anything
//	  until the pipe's buffer fills or the process exits.
func BuildEnv(environ []string, launch LaunchIDs, appendSourceMaps, scanStdout bool) []string {
	out := make([]string, 0, len(environ)+4)
	haveNodeOptions := false
	havePythonUnbuffered := false
	for _, kv := range environ {
		name, _, _ := strings.Cut(kv, "=")
		switch {
		case legacyRunEnvNames[name]:
			continue
		case name == EnvLaunchID, name == EnvLaunchService, name == EnvLaunchRoot:
			continue // replaced below, whatever this process inherited
		case name == "NODE_OPTIONS":
			haveNodeOptions = true
			if appendSourceMaps {
				kv = appendOption(kv, "--enable-source-maps")
			}
		case name == "PYTHONUNBUFFERED":
			havePythonUnbuffered = true
		}
		out = append(out, kv)
	}
	if appendSourceMaps && !haveNodeOptions {
		out = append(out, "NODE_OPTIONS=--enable-source-maps")
	}
	if scanStdout && !havePythonUnbuffered {
		out = append(out, "PYTHONUNBUFFERED=1")
	}
	out = append(out,
		EnvLaunchID+"="+launch.ID,
		EnvLaunchService+"="+launch.Service,
		EnvLaunchRoot+"="+launch.Root,
	)
	return out
}

// appendOption appends opt to kv's ("NAME=value") value, space-separated,
// or sets it bare when the value was empty.
func appendOption(kv, opt string) string {
	name, value, _ := strings.Cut(kv, "=")
	if value == "" {
		return name + "=" + opt
	}
	return name + "=" + value + " " + opt
}
