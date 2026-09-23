package ecosystem

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// writeFakeBinary drops an executable shell script named name in dir,
// following the fake-PATH harness pattern used throughout registry_test.go.
func writeFakeBinary(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0o700); err != nil {
		t.Fatalf("write fake %s: %v", name, err)
	}
	return path
}

func setFakePATH(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("PATH", dir)
}

func TestProbeCodemapUnavailableWhenNotOnPATH(t *testing.T) {
	setFakePATH(t, t.TempDir()) // empty: no codemap anywhere on PATH
	h := ProbeCodemap(context.Background(), t.TempDir())
	if h.Tool != "codemap" || h.State != HealthUnavailable {
		t.Fatalf("health = %+v, want state %q", h, HealthUnavailable)
	}
}

// TestProbeCodemapSchemaSkew reproduces the exact live bug from
// ~/notes/projects/monitor/2026-09-22-local-sentry-findings.md (bug 16): a
// codemap build older than the graph DB's schema. codemap's own
// session.go:86-89 currently maps this to code index_corrupt with a
// reindex hint; ProbeCodemap must recognize the wrapped schema-skew error
// text and report schema_skew (never suggesting --reindex) regardless.
func TestProbeCodemapSchemaSkew(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "codemap", `#!/bin/sh
printf '%s' '{"ok":false,"error":"graph db schema v9 is newer than this codemap (supports v7); upgrade codemap","code":"index_corrupt","hint":"back it up, then run: codemap index --reindex"}'
exit 4
`)
	setFakePATH(t, dir)
	h := ProbeCodemap(context.Background(), t.TempDir())
	if h.State != HealthSchemaSkew {
		t.Fatalf("state = %q, want %q; health = %+v", h.State, HealthSchemaSkew, h)
	}
	if !strings.Contains(h.Detail, "schema v9 is newer") {
		t.Errorf("detail = %q, want it to carry the schema-skew message", h.Detail)
	}
	if h.Recovery != codemapSchemaSkewRecovery {
		t.Errorf("recovery = %q, want %q", h.Recovery, codemapSchemaSkewRecovery)
	}
	if !strings.Contains(h.Recovery, "do NOT run codemap index --reindex") {
		t.Errorf("recovery must explicitly warn against --reindex for a schema skew: %q", h.Recovery)
	}
}

// TestProbeCodemapSchemaNewerCode covers E1.3b's forward-looking dedicated
// code, in case codemap upstream ships it before monitor's next release.
func TestProbeCodemapSchemaNewerCode(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "codemap", `#!/bin/sh
printf '%s' '{"ok":false,"error":"index schema v9 is newer than this codemap binary","code":"schema_newer","hint":"upgrade codemap"}'
exit 4
`)
	setFakePATH(t, dir)
	h := ProbeCodemap(context.Background(), t.TempDir())
	if h.State != HealthSchemaSkew {
		t.Fatalf("state = %q, want %q; health = %+v", h.State, HealthSchemaSkew, h)
	}
}

// TestProbeCodemapRealCorruption asserts an index_corrupt whose error text
// does NOT match the schema-skew wording stays index_corrupt — the
// distinction that keeps monitor from either crying wolf on a fine index or
// staying silent about a genuinely broken one.
func TestProbeCodemapRealCorruption(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "codemap", `#!/bin/sh
printf '%s' '{"ok":false,"error":"read schema version: unexpected EOF","code":"index_corrupt","hint":"back it up, then run: codemap index --reindex"}'
exit 4
`)
	setFakePATH(t, dir)
	h := ProbeCodemap(context.Background(), t.TempDir())
	if h.State != HealthIndexCorrupt {
		t.Fatalf("state = %q, want %q; health = %+v", h.State, HealthIndexCorrupt, h)
	}
	if !strings.Contains(h.Recovery, "--reindex") {
		t.Errorf("recovery for real corruption should still allow --reindex: %q", h.Recovery)
	}
}

// TestProbeCodemapRegisteredButEmptyIsNotIndexed reproduces the live shape
// left by `codemap init` without a following `codemap index` (verified
// against a real codemap v0.66 in a scratch project, 2026-09-22):
// registered:true, nodes:0. HealthOK's contract is "the project is indexed
// and results can be trusted"; a registered-but-empty project doesn't meet
// that, and codemapStatusEnvelope didn't even decode `nodes` before this
// fix, so every consumer trusted an empty blast-radius/impact result.
func TestProbeCodemapRegisteredButEmptyIsNotIndexed(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "codemap", `#!/bin/sh
printf '%s' '{"project":"demo","root":"/tmp/demo","registered":true,"nodes":0,"edges":0,"files":0}'
exit 0
`)
	setFakePATH(t, dir)
	h := ProbeCodemap(context.Background(), t.TempDir())
	if h.State != HealthNotIndexed {
		t.Fatalf("state = %q, want %q for registered:true,nodes:0; health = %+v", h.State, HealthNotIndexed, h)
	}
	if h.Recovery == "" {
		t.Errorf("expected a non-empty recovery for a registered-but-empty project: %+v", h)
	}
}

func TestProbeCodemapNotIndexed(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "codemap", `#!/bin/sh
printf '%s' '{"project":"demo","root":"/tmp/demo","registered":false}'
exit 0
`)
	setFakePATH(t, dir)
	h := ProbeCodemap(context.Background(), t.TempDir())
	if h.State != HealthNotIndexed {
		t.Fatalf("state = %q, want %q; health = %+v", h.State, HealthNotIndexed, h)
	}
}

func TestProbeCodemapOK(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "codemap", `#!/bin/sh
printf '%s' '{"project":"demo","root":"/tmp/demo","registered":true,"nodes":10,"edges":5}'
exit 0
`)
	setFakePATH(t, dir)
	h := ProbeCodemap(context.Background(), t.TempDir())
	if h.State != HealthOK {
		t.Fatalf("state = %q, want %q; health = %+v", h.State, HealthOK, h)
	}
	if h.Detail != "" || h.Recovery != "" {
		t.Errorf("an ok health should carry no detail/recovery noise: %+v", h)
	}
}

// TestProbeCodemapCachesWithinTTL asserts a second ProbeCodemap call for the
// same (dir, binary) inside the 60s TTL never re-invokes the subprocess.
func TestProbeCodemapCachesWithinTTL(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "calls")
	// Only count invocations of the `status` subcommand — computeCodemapHealth
	// also runs `--version` once per compute(), which is irrelevant to what
	// this test asserts (that a second ProbeCodemap call hits the cache
	// instead of re-running `codemap status`).
	writeFakeBinary(t, dir, "codemap", `#!/bin/sh
case " $* " in
  *" status "*) printf 'x' >> "$CODEMAP_HEALTH_CALLS" ;;
esac
printf '%s' '{"project":"demo","root":"/tmp/demo","registered":true}'
exit 0
`)
	setFakePATH(t, dir)
	t.Setenv("CODEMAP_HEALTH_CALLS", counter)

	target := t.TempDir()
	first := ProbeCodemap(context.Background(), target)
	second := ProbeCodemap(context.Background(), target)
	if first != second {
		t.Fatalf("cached result changed: %+v != %+v", first, second)
	}
	data, err := os.ReadFile(counter)
	if err != nil {
		t.Fatalf("read call counter: %v", err)
	}
	if len(data) != 1 {
		t.Fatalf("codemap status invoked %d times, want 1 (second ProbeCodemap call should hit the cache)", len(data))
	}
}

// TestProbeCodemapCanceledContextIsNotCached reproduces the cache-poisoning
// bug: a caller whose ctx is already canceled (or has a deadline that's
// already passed) got a cached "unavailable" result under the SAME (tool,
// dir, binary) key that a later, perfectly healthy caller with a fresh ctx
// then received too, for up to 60s. `monitor doctor` never hits this (one
// process, one call), but the probes are exported for reuse by longer-lived
// callers (a future MCP server, investigate's correlate step) where a
// canceled request must never contaminate every other in-flight probe of
// the same directory.
func TestProbeCodemapCanceledContextIsNotCached(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "codemap", `#!/bin/sh
printf '%s' '{"project":"demo","root":"/tmp/demo","registered":true,"nodes":10}'
exit 0
`)
	setFakePATH(t, dir)

	target := t.TempDir()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()

	first := ProbeCodemap(canceled, target)
	if first.State != HealthUnavailable {
		t.Fatalf("state = %q, want %q for an already-canceled ctx; health = %+v", first.State, HealthUnavailable, first)
	}
	if !strings.Contains(first.Detail, "canceled by caller") {
		t.Errorf("detail = %q, want it to name the CALLER's cancellation", first.Detail)
	}
	if strings.Contains(first.Detail, "timed out after 3s") {
		t.Errorf("detail = %q, must not claim a 3s tool timeout for a ctx canceled before the probe ever ran", first.Detail)
	}

	second := ProbeCodemap(context.Background(), target)
	if second.State != HealthOK {
		t.Fatalf("state = %q, want %q: the canceled-ctx result must not have poisoned the cache for this fresh, uncanceled call; health = %+v", second.State, HealthOK, second)
	}
}

// TestProbeVecgrepParentDeadlineNotMisreportedAsToolTimeout reproduces the
// second half of the same finding: a parent ctx whose deadline is SHORTER
// than the probe's own 3s timeout expires mid-probe. cctx.Err() ==
// context.DeadlineExceeded either way (the sentinel doesn't distinguish
// "our own 3s timer fired" from "the caller's much shorter deadline fired
// first"), so before this fix the probe blamed a nonexistent 3s tool
// timeout instead of the caller's own ~100ms deadline — and cached that
// misleading result besides.
func TestProbeVecgrepParentDeadlineNotMisreportedAsToolTimeout(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "vecgrep", `#!/bin/sh
sleep 1
printf '%s' '{"stats":{"chunks":5},"freshness":{"state":"fresh"}}'
`)
	// As in TestProbeCodemapTimeout: the fake binary's own `sleep` needs a
	// real sleep on PATH too.
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/bin:/usr/bin")

	target := t.TempDir()
	shortCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	first := ProbeVecgrep(shortCtx, target)
	if first.State != HealthUnavailable {
		t.Fatalf("state = %q, want %q; health = %+v", first.State, HealthUnavailable, first)
	}
	if strings.Contains(first.Detail, "timed out after 3s") {
		t.Errorf("detail = %q, must not blame the tool's 3s timeout for the CALLER's 100ms deadline expiring", first.Detail)
	}
	if !strings.Contains(first.Detail, "canceled by caller") {
		t.Errorf("detail = %q, want it to name the caller's short deadline", first.Detail)
	}

	second := ProbeVecgrep(context.Background(), target)
	if second.State != HealthOK {
		t.Fatalf("state = %q, want %q: the short-deadline result must not have poisoned the cache for this fresh call; health = %+v", second.State, HealthOK, second)
	}
}

func TestProbeCodemapTimeout(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "codemap", `#!/bin/sh
sleep 5
printf '%s' '{"registered":true}'
`)
	// The fake codemap must win the PATH lookup, but the script's own `sleep`
	// call still needs a real `sleep` binary to resolve — append the
	// standard POSIX bin dirs (never the test process's real PATH, which
	// could contain a real "codemap" ahead of ours on some machines).
	t.Setenv("PATH", dir+string(os.PathListSeparator)+"/bin:/usr/bin")
	start := time.Now()
	h := ProbeCodemap(context.Background(), t.TempDir())
	// Worst case is the version probe's ~2s timeout (+0.5s WaitDelay) plus
	// the status probe's 3s timeout (+1s WaitDelay), run in sequence.
	if elapsed := time.Since(start); elapsed > 8*time.Second {
		t.Errorf("ProbeCodemap took %s, want it capped near the ~6.5s worst case", elapsed)
	}
	if h.State != HealthUnavailable {
		t.Fatalf("state = %q, want %q on timeout; health = %+v", h.State, HealthUnavailable, h)
	}
}

func TestProbeVecgrepUnavailableWhenNotOnPATH(t *testing.T) {
	setFakePATH(t, t.TempDir())
	h := ProbeVecgrep(context.Background(), t.TempDir())
	if h.Tool != "vecgrep" || h.State != HealthUnavailable {
		t.Fatalf("health = %+v, want state %q", h, HealthUnavailable)
	}
}

// TestProbeVecgrepNotIndexedEmptyProject reproduces the live shape observed
// against an already-`vecgrep init`-ed project with zero chunks (verified
// against ~/projects/monitor, 2026-09-22): exit 0, valid JSON, no chunks yet.
func TestProbeVecgrepNotIndexedEmptyProject(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "vecgrep", `#!/bin/sh
printf '%s' '{"veclite_bytes":0,"indexed_bytes":0,"index_fresh":false,"stats":{},"freshness":{"state":"unknown","reason":"index_health_manifest_missing"},"lightweight":true}'
exit 0
`)
	setFakePATH(t, dir)
	h := ProbeVecgrep(context.Background(), t.TempDir())
	if h.State != HealthNotIndexed {
		t.Fatalf("state = %q, want %q; health = %+v", h.State, HealthNotIndexed, h)
	}
	if h.Recovery != vecgrepNotIndexedRecovery {
		t.Errorf("recovery = %q, want %q", h.Recovery, vecgrepNotIndexedRecovery)
	}
}

// TestProbeVecgrepNotIndexedUninitialized reproduces vecgrep's plain-text
// (non-JSON) failure for a directory that was never `vecgrep init`-ed
// (verified live against ~/projects/file.cheap, 2026-09-22).
func TestProbeVecgrepNotIndexedUninitialized(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "vecgrep", `#!/bin/sh
printf '%s' "Error: not in a vecgrep project (no config file or .vecgrep directory found). Run 'vecgrep init' to initialize: run 'vecgrep init' first" >&2
exit 1
`)
	setFakePATH(t, dir)
	h := ProbeVecgrep(context.Background(), t.TempDir())
	if h.State != HealthNotIndexed {
		t.Fatalf("state = %q, want %q; health = %+v", h.State, HealthNotIndexed, h)
	}
	if strings.HasPrefix(h.Detail, "Error: ") {
		t.Errorf("detail should have cobra's Error: framing stripped: %q", h.Detail)
	}
}

// TestProbeVecgrepReady reproduces the live shape of an actually indexed
// project (verified against ~/projects/teak, 2026-09-22): non-zero chunks,
// even while stale/pending-changes, still means the index is searchable.
func TestProbeVecgrepReady(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "vecgrep", `#!/bin/sh
printf '%s' '{"veclite_bytes":127940210,"indexed_bytes":5674450,"index_fresh":false,"stats":{"chunks":11582,"embeddings":11582,"files":688},"freshness":{"state":"stale","reason":"raw_source_drift"}}'
exit 0
`)
	setFakePATH(t, dir)
	h := ProbeVecgrep(context.Background(), t.TempDir())
	if h.State != HealthOK {
		t.Fatalf("state = %q, want %q (stale-but-indexed is still ready); health = %+v", h.State, HealthOK, h)
	}
}

func TestProbeVecgrepUnavailableOnUnrecognizedFailure(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "vecgrep", `#!/bin/sh
printf '%s' 'boom: disk read failure' >&2
exit 17
`)
	setFakePATH(t, dir)
	h := ProbeVecgrep(context.Background(), t.TempDir())
	if h.State != HealthUnavailable {
		t.Fatalf("state = %q, want %q; health = %+v", h.State, HealthUnavailable, h)
	}
}

func TestProbeVecgrepUsesDirAsWorkingDirectory(t *testing.T) {
	binDir := t.TempDir()
	project := t.TempDir()
	receipt := filepath.Join(binDir, "pwd-receipt")
	writeFakeBinary(t, binDir, "vecgrep", `#!/bin/sh
pwd > "$VECGREP_PWD_RECEIPT"
printf '%s' '{"stats":{"chunks":1},"freshness":{"state":"fresh"}}'
`)
	setFakePATH(t, binDir)
	t.Setenv("VECGREP_PWD_RECEIPT", receipt)

	if h := ProbeVecgrep(context.Background(), project); h.State != HealthOK {
		t.Fatalf("state = %q, want %q; health = %+v", h.State, HealthOK, h)
	}
	got, err := os.ReadFile(receipt)
	if err != nil {
		t.Fatalf("read pwd receipt: %v", err)
	}
	// Resolve symlinks (macOS temp dirs live under /var, itself a symlink to
	// /private/var) so this doesn't flake on the shell's already-resolved pwd.
	wantDir, _ := filepath.EvalSymlinks(project)
	gotDir, _ := filepath.EvalSymlinks(strings.TrimSpace(string(got)))
	if gotDir != wantDir {
		t.Fatalf("vecgrep ran with cwd %q, want %q", gotDir, wantDir)
	}
}

func TestScanBinaryDetectsShadowing(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	writeFakeBinary(t, first, "glyph", "#!/bin/sh\nprintf 'glyph version dev'\n")
	writeFakeBinary(t, second, "glyph", "#!/bin/sh\nprintf 'glyph version v0.20.0'\n")
	t.Setenv("PATH", first+string(os.PathListSeparator)+second)

	info := ScanBinary(context.Background(), "glyph")
	if !info.Available || info.Path != filepath.Join(first, "glyph") {
		t.Fatalf("info = %+v, want Path %q", info, filepath.Join(first, "glyph"))
	}
	if !info.Shadowed {
		t.Fatalf("info.Shadowed = false, want true: %+v", info)
	}
	if len(info.AllPaths) != 2 || info.AllPaths[0] != filepath.Join(first, "glyph") || info.AllPaths[1] != filepath.Join(second, "glyph") {
		t.Fatalf("AllPaths = %v, want both dirs in PATH order", info.AllPaths)
	}
	if info.Warning == "" {
		t.Error("expected a non-empty shadow warning")
	}
}

func TestScanBinaryNoShadowWhenSingleMatch(t *testing.T) {
	dir := t.TempDir()
	writeFakeBinary(t, dir, "cairn", "#!/bin/sh\nprintf '2.11.1'\n")
	t.Setenv("PATH", dir)

	info := ScanBinary(context.Background(), "cairn")
	if !info.Available || info.Shadowed || info.Warning != "" {
		t.Fatalf("info = %+v, want a single, unshadowed match", info)
	}
}

func TestScanBinaryUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	info := ScanBinary(context.Background(), "definitely-not-on-path-12345")
	if info.Available {
		t.Fatalf("info = %+v, want Available=false", info)
	}
}
