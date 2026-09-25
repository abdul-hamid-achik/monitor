package ecosystem

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// Health states for ProbeCodemap / ProbeVecgrep. These are the stable,
// machine-readable "code intelligence" health taxonomy consumers (monitor
// doctor, explain.Build, monitor hot) switch on. Never invent a new state
// string ad hoc — degrade to Unavailable and explain why in Detail instead.
const (
	HealthOK           = "ok"            // tool is present, the project is indexed, and results can be trusted
	HealthSchemaSkew   = "schema_skew"   // the on-disk index was written by a newer binary than the one on PATH
	HealthIndexCorrupt = "index_corrupt" // the index exists but genuinely won't open (not a schema skew)
	HealthNotIndexed   = "not_indexed"   // the tool is present and healthy, but this project/branch has no index yet
	HealthUnavailable  = "unavailable"   // the binary is missing, timed out, or failed in a way we can't classify
)

// Health is the bounded, honest-degradation health report for one code
// intelligence tool (codemap or vecgrep) against one project directory.
// Every field is safe to print or return over MCP/--json as-is; Detail and
// Recovery are plain English, never raw stack traces or secrets.
type Health struct {
	Tool     string `json:"tool"`
	State    string `json:"state"`
	Detail   string `json:"detail,omitempty"`
	Recovery string `json:"recovery,omitempty"`
	Version  string `json:"version,omitempty"`
	Path     string `json:"path,omitempty"`
}

// CodeIntelHealth bundles both code-intelligence probes for one directory —
// the shape `monitor doctor` embeds and wave-2 consumers (explain.Build,
// monitor hot, monitor issue's correlate step) will call directly.
type CodeIntelHealth struct {
	Codemap Health `json:"codemap"`
	Vecgrep Health `json:"vecgrep"`
}

// ProbeCodeIntel runs both ProbeCodemap and ProbeVecgrep against dir.
func ProbeCodeIntel(ctx context.Context, dir string) CodeIntelHealth {
	return CodeIntelHealth{
		Codemap: ProbeCodemap(ctx, dir),
		Vecgrep: ProbeVecgrep(ctx, dir),
	}
}

// codemapSchemaSkewRecovery is the one honest recovery for a codemap index
// written by a newer binary: upgrading is safe, reindexing is not (it would
// rewrite the shared global index with an older schema for every other
// consumer). See ~/notes/projects/monitor/2026-09-22-local-sentry-roadmap.md
// (E1.3/E1.3b) for why `codemap index --reindex` must never be suggested here.
const codemapSchemaSkewRecovery = "upgrade the codemap binary (cd ~/projects/codemap && go install ./cmd/codemap); do NOT run codemap index --reindex"

// codemapSchemaSkewPattern matches store.go's schema-newer error text on a
// codemap build that doesn't ship the dedicated `schema_newer` code yet
// (E1.3b, a separate codemap-repo change). It also matches after that lands,
// since the same wording is preserved. Matched against the failure envelope's
// `error` field, never against user-controlled data.
var codemapSchemaSkewPattern = regexp.MustCompile(`schema v\d+ is newer than this codemap \(supports v\d+\)`)

// healthCacheTTL bounds how long a ProbeCodemap/ProbeVecgrep result is
// reused. `codemap status --json` opens a graph DB and `vecgrep status
// --lightweight` reads index health metadata from disk; neither is free, and
// callers like `monitor issue`/`monitor hot` may probe the same project many
// times in one process. 60s is short enough that a reindex or binary
// reinstall (which busts the cache key below anyway) is noticed promptly.
const healthCacheTTL = 60 * time.Second

// healthCacheKey scopes a cached Health to exactly the inputs that could
// change its answer: which tool, which project directory, and which binary
// (by path + mtime, so `go install`-ing a newer codemap invalidates the
// cache immediately instead of waiting out the TTL).
type healthCacheKey struct {
	tool  string
	dir   string
	path  string
	mtime int64
}

type healthCacheEntry struct {
	health   Health
	computed time.Time
}

var (
	healthCacheMu sync.Mutex
	healthCache   = map[healthCacheKey]healthCacheEntry{}
)

// cachedHealth returns compute()'s result, reusing a cached one from the last
// healthCacheTTL for the same (tool, dir, binary path + mtime).
//
// The result is stored ONLY when ctx.Err() == nil after compute() returns.
// ProbeCodemap/ProbeVecgrep are exported for long-lived callers (a future MCP
// server, investigate's correlate step) that may pass a ctx that's already
// canceled or carries a short deadline. Without this check, one such caller
// would poison the shared 60s cache with an "unavailable" result computed
// under a dead context, and every OTHER caller probing the same dir — even
// one with a perfectly healthy, uncancelled ctx — would get that poisoned
// result for up to 60s. `monitor doctor` runs once per process so it never
// observes this, but it's real for any longer-lived consumer.
func cachedHealth(ctx context.Context, tool, dir, path string, mtime time.Time, compute func() Health) Health {
	key := healthCacheKey{tool: tool, dir: dir, path: path, mtime: mtime.UnixNano()}
	healthCacheMu.Lock()
	if e, ok := healthCache[key]; ok && time.Since(e.computed) < healthCacheTTL {
		healthCacheMu.Unlock()
		return e.health
	}
	healthCacheMu.Unlock()

	h := compute()

	if ctx.Err() != nil {
		// The caller's ctx died (was already canceled/expired, or expired
		// mid-probe) during compute(): h reflects that dead ctx, not this
		// tool/dir's real health, so it must never be reused by a later
		// caller with a fresh ctx.
		return h
	}

	healthCacheMu.Lock()
	healthCache[key] = healthCacheEntry{health: h, computed: time.Now()}
	healthCacheMu.Unlock()
	return h
}

// binaryModTime returns path's mtime, or the zero time when it can't be
// stat'd (a cache key collision on the zero time is harmless: it just means
// entries for an unstat-able binary share one slot).
func binaryModTime(path string) time.Time {
	st, err := os.Stat(path)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

// ---------------------------------------------------------------------------
// codemap
// ---------------------------------------------------------------------------

// ProbeCodemap runs `codemap status --json` against dir (3s timeout, 60s
// cache keyed by dir + binary mtime) and classifies the result into the
// Health taxonomy. It never runs `codemap index --reindex` and never
// recommends it: a codemap index is a single global store shared by every
// project, and reindexing it because THIS binary can't read it would corrupt
// it for every other consumer (see codemapSchemaSkewRecovery).
func ProbeCodemap(ctx context.Context, dir string) Health {
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := exec.LookPath("codemap")
	if err != nil {
		return Health{
			Tool:     "codemap",
			State:    HealthUnavailable,
			Detail:   "codemap not on PATH",
			Recovery: "install codemap: cd ~/projects/codemap && go install ./cmd/codemap",
		}
	}
	return cachedHealth(ctx, "codemap", dir, path, binaryModTime(path), func() Health {
		return computeCodemapHealth(ctx, path, dir)
	})
}

// codemapStatusEnvelope covers both shapes `codemap status --json` can print:
// the failure envelope {ok:false, error, code, hint} on any CodedError, and
// the success StatusReport ({registered, project, ...}) when the graph DB
// opened and the query resolved (even for a project that isn't indexed yet —
// that's registered:false with ok/error entirely absent, not a failure).
type codemapStatusEnvelope struct {
	OK         bool   `json:"ok"`
	Error      string `json:"error"`
	Code       string `json:"code"`
	Hint       string `json:"hint"`
	Registered bool   `json:"registered"`
	Project    string `json:"project"`
	// Nodes distinguishes a project codemap has actually indexed from one
	// that's merely registered (`codemap init` without `codemap index`
	// leaves registered:true, nodes:0). See the HealthOK doc comment above:
	// "the project is indexed and results can be trusted" doesn't hold for a
	// registered-but-empty project.
	Nodes int64 `json:"nodes"`
}

// canceledByCallerHealth reports that ctx (the caller's context, not the
// probe's own internal timeout) is why this probe couldn't complete. It is
// deliberately worded differently from a real "timed out after 3s" (a
// property of the tool being probed) so a caller never confuses its own
// cancellation with the tool actually hanging — and cachedHealth never
// caches this result (see its doc comment).
func canceledByCallerHealth(tool, detailPrefix string, ctxErr error) Health {
	return Health{
		Tool:   tool,
		State:  HealthUnavailable,
		Detail: detailPrefix + ": canceled by caller: " + ctxErr.Error(),
	}
}

func computeCodemapHealth(ctx context.Context, path, dir string) Health {
	if err := ctx.Err(); err != nil {
		return canceledByCallerHealth("codemap", "codemap status --json", err)
	}

	h := Health{Tool: "codemap", Path: path, Version: shortBinaryVersion(ctx, path)}

	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	args := []string{}
	if dir != "" {
		args = append(args, "-C", dir)
	}
	args = append(args, "status", "--json", "--skip-stale")
	cmd := exec.CommandContext(cctx, path, args...)
	// WaitDelay bounds cleanup after the context is canceled: without it, a
	// child that forked its own descendants (inheriting our stdout/stderr
	// pipes) could keep Wait() blocked well past the 3s deadline above, since
	// killing the direct child alone doesn't close pipes a grandchild still
	// holds open.
	cmd.WaitDelay = time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// Check the CALLER's ctx first: context.DeadlineExceeded on cctx is
	// ambiguous between "our own 3s timer fired" and "ctx's own, possibly
	// much shorter, deadline expired first" (context.WithTimeout's child
	// reports whichever error caused it to finish, and both cases report the
	// identical DeadlineExceeded sentinel). Only when ctx itself is still
	// live do we know the 3s figure below is actually true.
	if err := ctx.Err(); err != nil {
		return canceledByCallerHealth("codemap", "codemap status --json", err)
	}

	if cctx.Err() == context.DeadlineExceeded {
		h.State = HealthUnavailable
		h.Detail = "codemap status --json timed out after 3s"
		h.Recovery = "retry, or check for a stuck codemap/codemap-daemon process"
		return h
	}

	out := bytes.TrimSpace(stdout.Bytes())
	var env codemapStatusEnvelope
	if len(out) > 0 {
		_ = json.Unmarshal(out, &env) // best-effort; malformed output falls through below
	}

	if runErr != nil {
		switch {
		case env.Code == "schema_newer":
			// E1.3b's forward-looking code: codemap upstream hasn't shipped it at
			// the time of E1.3, but honoring it now costs nothing and needs no
			// text matching once it lands.
			h.State = HealthSchemaSkew
			h.Detail = env.Error
			h.Recovery = codemapSchemaSkewRecovery
		case env.Code == "index_corrupt" && codemapSchemaSkewPattern.MatchString(env.Error):
			// Today's codemap (session.go:86-89) maps a schema-newer DB to the
			// same index_corrupt code as real corruption. Distinguish by the
			// wrapped store.go error text so monitor never tells the user to
			// reindex a perfectly good, newer-schema index.
			h.State = HealthSchemaSkew
			h.Detail = env.Error
			h.Recovery = codemapSchemaSkewRecovery
		case env.Code == "index_corrupt":
			h.State = HealthIndexCorrupt
			h.Detail = env.Error
			h.Recovery = firstNonEmpty(env.Hint, "back up the codemap data dir, then run: codemap index --reindex")
		case env.Code == "index_missing" || env.Code == "not_found" || env.Code == "not_indexed":
			h.State = HealthNotIndexed
			h.Detail = firstNonEmpty(env.Error, "project is not indexed by codemap")
			h.Recovery = firstNonEmpty(env.Hint, "run: codemap index")
		case env.Code != "":
			h.State = HealthUnavailable
			h.Detail = firstNonEmpty(env.Error, runErr.Error())
			h.Recovery = env.Hint
		default:
			h.State = HealthUnavailable
			h.Detail = "codemap status --json: " + runErr.Error()
			if se := strings.TrimSpace(stderr.String()); se != "" {
				h.Detail += " (stderr: " + se + ")"
			}
		}
		return h
	}

	if len(out) == 0 {
		h.State = HealthUnavailable
		h.Detail = "codemap status --json produced no output"
		return h
	}
	if err := json.Unmarshal(out, &env); err != nil {
		h.State = HealthUnavailable
		h.Detail = "codemap status --json: malformed output"
		return h
	}
	if !env.Registered {
		h.State = HealthNotIndexed
		h.Detail = "project is not indexed by codemap"
		h.Recovery = "run: codemap index"
		return h
	}
	if env.Nodes == 0 {
		// registered:true, nodes:0 is what `codemap init` without `codemap
		// index` leaves (verified live against codemap v0.66,
		// StatusWithOptions in ~/projects/codemap/internal/app/service_init.go
		// sets Registered=true as soon as the project row exists, before any
		// indexing happens). Reporting HealthOK here would tell every
		// consumer (explain.Build, hot, issues --at) that impact/blast-radius
		// results can be trusted, when there is nothing indexed to query yet.
		h.State = HealthNotIndexed
		h.Detail = "project is registered with codemap but has no indexed nodes yet"
		h.Recovery = "run: codemap index"
		return h
	}
	h.State = HealthOK
	return h
}

// ---------------------------------------------------------------------------
// vecgrep
// ---------------------------------------------------------------------------

// ProbeVecgrep runs `vecgrep status --format json --lightweight` against dir
// (3s timeout, 60s cache keyed by dir + binary mtime) and reports whether the
// CURRENT branch's index is searchable. vecgrep keeps one index per git
// branch, so "ready" on main tells you nothing about a feature branch: this
// always re-probes dir's checked-out branch.
func ProbeVecgrep(ctx context.Context, dir string) Health {
	if ctx == nil {
		ctx = context.Background()
	}
	path, err := exec.LookPath("vecgrep")
	if err != nil {
		return Health{
			Tool:     "vecgrep",
			State:    HealthUnavailable,
			Detail:   "vecgrep not on PATH",
			Recovery: "install vecgrep: cd ~/projects/vecgrep && go install ./cmd/vecgrep",
		}
	}
	return cachedHealth(ctx, "vecgrep", dir, path, binaryModTime(path), func() Health {
		return computeVecgrepHealth(ctx, path, dir)
	})
}

// vecgrepLightweightStatus is the subset of `vecgrep status --format json
// --lightweight`'s real output (verified live against ~/projects/monitor and
// ~/projects/teak, 2026-09-22) that Health needs. vecgrep has no -C/--path
// flag; the project is resolved from cwd, so callers set cmd.Dir instead.
type vecgrepLightweightStatus struct {
	Stats     map[string]int64 `json:"stats"`
	Freshness struct {
		State  string `json:"state"`
		Reason string `json:"reason"`
	} `json:"freshness"`
}

// vecgrepNotIndexedRecovery covers both real causes of a missing vecgrep
// index for the current branch/project: a fresh git worktree/branch whose
// per-branch index was never built (this project may already be `vecgrep
// init`-ed elsewhere), and a project that was never `vecgrep init`-ed at all.
// `vecgrep branch switch` is a cheap first try — it just activates whatever
// snapshot already exists for this branch, if any — before paying for a full
// `vecgrep index`.
const vecgrepNotIndexedRecovery = "vecgrep branch switch (restore an existing snapshot for this branch), or vecgrep index (build one)"

// vecgrepUninitializedRecovery covers a directory that was never `vecgrep
// init`-ed at all (or sits inside a different project's boundary). Unlike
// vecgrepNotIndexedRecovery's case, NEITHER `vecgrep branch switch` nor
// `vecgrep index` works here — both fail with "not in a vecgrep project"
// until `vecgrep init` has run — so recommending them (as vecgrep's own
// error text does point to `vecgrep init`, verified live against an
// uninitialized scratch repo and ~/projects/file.cheap) would send an agent
// down two dead ends before the one command that actually works.
const vecgrepUninitializedRecovery = "vecgrep init, then vecgrep index"

// vecgrepUninitializedPattern matches vecgrep's plain-text (non-JSON) error
// when the directory was never `vecgrep init`-ed, or sits inside a different
// registered project's boundary — both distinct from "initialized but empty".
var vecgrepUninitializedPattern = regexp.MustCompile(`not in a vecgrep project|nested project boundary`)

func computeVecgrepHealth(ctx context.Context, path, dir string) Health {
	if err := ctx.Err(); err != nil {
		return canceledByCallerHealth("vecgrep", "vecgrep status", err)
	}

	h := Health{Tool: "vecgrep", Path: path, Version: shortBinaryVersion(ctx, path)}

	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, path, "status", "--format", "json", "--lightweight")
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.WaitDelay = time.Second // see the comment on the codemap probe's WaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()

	// See the matching check in computeCodemapHealth: cctx's DeadlineExceeded
	// is ambiguous between our own 3s timer and ctx's own (possibly shorter)
	// deadline, so ctx must be checked first.
	if err := ctx.Err(); err != nil {
		return canceledByCallerHealth("vecgrep", "vecgrep status", err)
	}

	if cctx.Err() == context.DeadlineExceeded {
		h.State = HealthUnavailable
		h.Detail = "vecgrep status timed out after 3s"
		h.Recovery = "retry, or check for a stuck vecgrep daemon process"
		return h
	}

	if runErr != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		// vecgrep's cobra CLI prints its own "Error: <message>" line on
		// failure; strip that framing so Detail carries just the message.
		msg = strings.TrimPrefix(msg, "Error: ")
		if vecgrepUninitializedPattern.MatchString(msg) {
			h.State = HealthNotIndexed
			h.Detail = firstNonEmpty(msg, "project is not indexed by vecgrep")
			h.Recovery = vecgrepUninitializedRecovery
			return h
		}
		h.State = HealthUnavailable
		h.Detail = firstNonEmpty(msg, "vecgrep status: "+runErr.Error())
		return h
	}

	out := bytes.TrimSpace(stdout.Bytes())
	var status vecgrepLightweightStatus
	if err := json.Unmarshal(out, &status); err != nil {
		h.State = HealthUnavailable
		h.Detail = "vecgrep status --lightweight: malformed output"
		return h
	}
	if status.Stats["chunks"] <= 0 {
		h.State = HealthNotIndexed
		h.Detail = firstNonEmpty(status.Freshness.Reason, "current branch has no vecgrep index yet")
		h.Recovery = vecgrepNotIndexedRecovery
		return h
	}
	h.State = HealthOK
	return h
}

// ---------------------------------------------------------------------------
// shared helpers
// ---------------------------------------------------------------------------

// shortBinaryVersion best-effort runs `<path> --version` with a short
// timeout, returning its trimmed first line. Failure just leaves Version
// empty; it's diagnostic, never load-bearing.
func shortBinaryVersion(ctx context.Context, path string) string {
	cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	cmd := exec.CommandContext(cctx, path, "--version")
	cmd.WaitDelay = 500 * time.Millisecond
	out, err := cmd.CombinedOutput()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(firstLine(string(out)))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
