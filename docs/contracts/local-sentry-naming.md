# Error tracking naming ADR

- **Status**: Accepted
- **Context**: the error-tracking epic (see the `feat/local-sentry` roadmap)
  adds a launch verb, a stack-trace parser, an exception model, a heatmap,
  and an issue-detail view, each proposed independently by several
  contributors during design. Several names collided outright (two proposed
  launch verbs, two proposed environment variables for the same run
  correlation) and the exception-chain rule (which frame is the culprit, what
  goes into the fingerprint) was decided ad hoc per parser. This ADR fixes one
  name and one rule per concept so parallel PRs do not diverge. Every PR that
  touches a name or a rule below must cite this document instead of
  re-deciding it.

This is the naming half of the day-0 ADR. The two contract schemas it
introduces are drafted separately: [`issue-context-v1`](./issue-context-v1)
and [`line-heatmap-v1`](./line-heatmap-v1).

## 1. Naming map

| Concept | Chosen | Rejected |
|---|---|---|
| Launch verb | `monitor run -- <cmd>` (dual-mode). `monitor run <spec.yml>` keeps its current, unrelated meaning: it shells out to `glyph run` (`internal/ecosystem/registry.go`'s `RunGlyphrun`) and exports `MONITOR=1` / `MONITOR_RUN_DIR` | `monitor exec`, `monitor launch` |
| `run --` environment | `MONITOR_LAUNCH_ID`, `MONITOR_LAUNCH_SERVICE`, `MONITOR_LAUNCH_ROOT` (inherited as-is when a `run --` nests inside another) | `MONITOR=1`, `MONITOR_SERVICE`, `MONITOR_RUN_ID` — those three are already owned (see §2) |
| Scanned streams | `--scan stderr` (default), `stdout`, or `both` | always scanning stdout |
| Parse verb (hidden) | `monitor stacktrace parse [--file F] [--record] [--from-start]`; `--follow` is a later epic | `ingest parse`, `traces parse` |
| Reprocess checkpoints | `$XDG_STATE_HOME/monitor/parse/<sha256(abspath)>.json` holding `{inode, size, offset}` | using wall-clock "now" as `ObservedAt` |
| Heatmap | `monitor hot <pid\|service\|file> [--type cpu\|heap\|goroutine]` producing `monitor.line_heatmap.v1` | `hotspots`, `profile analyze`, `profile --lines` |
| Issue detail | `monitor issue <id>` producing `monitor.issue_context.v1`; `monitor issues --at <file:line>` | `issue-explain.v1`, `issues context` |
| `issue`/`issues` collision | `internal/cli/issues.go` already registers `issue` as a cobra **alias** of `issues` (`Aliases: []string{"issue"}`), so today `monitor issue <anything>` just prints the `issues` command's help rather than an issue page. E2.5's new, one-argument `monitor issue <id>` must **take over** that alias rather than add a second meaning for it; `monitor issues list\|show\|resolve\|reopen\|ignore` keep dispatching exactly as they do today, so no existing script or muscle memory breaks. This is a user-visible CLI change and belongs in the CHANGELOG. | leaving the alias as-is and hoping `<id>` never collides with a subcommand name |
| Exception chain | see §6 (fingerprint/culprit rule) | picking the outermost or innermost exception ad hoc per parser |
| New packages (MVP) | `internal/stacktrace`, `internal/scrub`, `internal/project`, `internal/devrun`, `internal/explain`, `internal/sourcemap` | `internal/events` for the MVP; a second, duplicated joiner under `internal/capture` |
| Later packages | `internal/event` (a future `monitor.event/v1`), `internal/probe`, `internal/ingest/sentry`, `internal/notes` | `internal/probes`, `devrun/hooks` |
| Probe environment (later epic) | `MONITOR_PROBE_DIR` | `MONITOR_SPOOL_DIR` |
| `Culprit` type | `issues.Culprit{Function, FQN, File, Line, Source}`, `Source` is `stack` or `message_search` | a bare `string`, or a `*HotspotRef` |
| `Frame.Mapping` enum | `exact \| ambiguous \| transpiled \| inferred \| ""` — one shared source-map confidence enum, set on a stack-trace `Frame` wherever a location was resolved through a source map | a second, incompatible enum per contract. `monitor.line_heatmap.v1`'s per-line `mapping` reuses these same four confidence values for the same reason, plus its own separate `stale` signal — a profiled *line* can go stale after profiling in a way a per-crash stack `Frame` has no equivalent for; see [`line-heatmap-v1`](./line-heatmap-v1) |
| Ingest port (later epic) | `127.0.0.1:8969`, explicit failure on a port conflict, project key required | `7352`; accepting an unknown key |
| Launch registry (v1.17) | `$XDG_STATE_HOME/monitor/services/<project>/<name>.json` (`monitor.run-service.v1`; no argv; directory `0700`, file `0600`) | pinning `MONITOR_RUN_DIR` at launch time |

## 2. Environment variable ownership

Four environment variables name the same idea — "what run is this" — for four
different consumers. None of them may be repurposed:

| Variable | Owner | Set by |
|---|---|---|
| `MONITOR=1` | Legacy `monitor run <spec>` (glyphrun) and `monitor watch`. glyphrun's runner and cairntrace both branch on its presence. | `internal/ecosystem/registry.go`'s `RunGlyphrun` (`cmd.Env = append(os.Environ(), "MONITOR=1", "MONITOR_RUN_DIR="+runDir)`), and unconditionally by `internal/cli/watch.go`'s package `init()` whenever `MONITOR_RUN_DIR` is unset in the current process — see the gotcha below. |
| `MONITOR_RUN_DIR` | Legacy `monitor run <spec>` (glyphrun). Consumed by `contextids` as a last-resort run-id source (`runDirBasename(...)`) when nothing more specific is set. | Same call site as `MONITOR=1` above. |
| `MONITOR_RUN_ID` | `internal/contextids`, which reads it **before** `CHALUPA_CI_RUN_ID` / `CHALUPA_RUN_ID`. | Whatever launched the process (CI, a wrapper script); monitor itself never sets it. |
| `MONITOR_SERVICE` | `internal/contextids`, which reads it **before** `CHALUPA_SERVICE`. | Same — monitor never sets it. |
| `MONITOR_LAUNCH_ID` / `MONITOR_LAUNCH_SERVICE` / `MONITOR_LAUNCH_ROOT` | The new `monitor run -- <cmd>` (E2.4). Computed once per launch and exported to the child. | `monitor run --` only. |

Rules that follow from the table:

- `monitor run -- <cmd>` **only** exports the three `MONITOR_LAUNCH_*`
  variables. It must never set `MONITOR`, `MONITOR_RUN_DIR`,
  `MONITOR_RUN_ID`, or `MONITOR_SERVICE` — those belong to the legacy spec
  runner and to `contextids` respectively, and glyphrun / cairntrace / the CI
  wrapper already branch on them.
- `MONITOR_LAUNCH_ID` and `MONITOR_LAUNCH_SERVICE` are **always freshly
  computed for THIS launch**, nested or not: an inner `--name web-api` takes
  effect as this launch's own `MONITOR_LAUNCH_SERVICE` exactly as named, it
  is never silently replaced by whatever the OUTER launch happened to be
  called, and every launch gets its own fresh, unique `MONITOR_LAUNCH_ID`
  (E3.2's per-service launch registry, below, depends on both of these:
  `monitor run --name w -- yarn start` must register under `w`, not under
  whatever a task-runner parent named itself).
- `MONITOR_LAUNCH_ROOT`'s value depends on whether this launch is nested,
  and this is the one exception to "always freshly computed": when `run --`
  nests inside another `run --` (a spawned dev server that itself runs
  under `monitor run --`), the child inherits the **parent's**
  `MONITOR_LAUNCH_ROOT` unchanged, so a whole chain of nested launches
  shares one root end to end — this is what lets a nested launch's live
  DedupeKey (§5) fold together with its parent's, instead of the same
  underlying crash text being recorded twice (once by each launch's own
  detector). When `run --` is **not** nested — no non-empty
  `MONITOR_LAUNCH_ROOT` was inherited, i.e. this is the OUTERMOST launch —
  `MONITOR_LAUNCH_ROOT` is set to **this launch's own,
  just-generated `MONITOR_LAUNCH_ID`**, never a directory. This was a
  verified dogfooding bug (fixed after the original day-0 ADR shipped):
  `MONITOR_LAUNCH_ROOT` used to default to the launch's git root (or cwd, if
  no git root was found) whenever it was not inherited. A directory is
  shared by every process that happens to launch from that same repo, so
  two completely independent, non-nested `monitor run --` invocations
  against the same repo — two sibling terminal tabs each running `monitor
  run -- node worker.js`, say — got the *same* `MONITOR_LAUNCH_ROOT` merely
  by being co-located, and §5's live DedupeKey is seeded from
  `MONITOR_LAUNCH_ROOT` plus a short (±3s-class) time bucket: if both
  workers happened to crash with textually identical output within that
  window (a real scenario for two replicas of the same worker, or two
  people hitting the same bug at the same time), their occurrences silently
  collapsed into one, undercounting `occurrence_count` for a real,
  independent second crash. Seeding a fresh launch's root from its own ID
  instead means `MONITOR_LAUNCH_ROOT` answers "which **outermost launch**
  produced this text", not "which directory did this run in": two sibling,
  non-nested launches now always mint two different IDs, and therefore two
  different roots, so they can never collapse into each other — while a
  genuinely nested launch still shares one root, because only the
  inheritance branch above ever overrides it, and only the innermost
  launch's own detector actually watches its output for the crash.
- **Known gotcha**: `internal/cli/watch.go` has a package-level `init()` that
  unconditionally calls `os.Setenv("MONITOR", "1")` in the *current process*
  whenever `MONITOR_RUN_DIR` is empty — this runs for every `monitor`
  invocation, not just `watch`, because Go executes all package `init()`
  functions before `main`. Since `os.Setenv` mutates the process environment
  that `os/exec` inherits by default, `monitor run -- <cmd>` must explicitly
  strip `MONITOR` (and `MONITOR_RUN_DIR`) from the child's `cmd.Env` rather
  than passing `os.Environ()` through untouched, or the "only
  `MONITOR_LAUNCH_*`" rule above is silently violated.

## 3. `--scan` semantics

`monitor run -- <cmd>` inherits the child's stdin. `--scan` selects which
output stream(s) feed the stack-trace detector:

- `stderr` (default): the language runtimes in the golden table (Node, Deno,
  Bun, Python, Ruby, Go) print uncaught exceptions and panics here.
- `stdout`: needed for loggers that write structured output to stdout by
  convention — some `zap` setups point `OutputPaths`/`ErrorOutputPaths` at
  stdout instead of stderr, and `pino`/many Rails loggers default there too.
- `both`: scans both streams; a line is attributed to the stream it was read
  from.

In every mode, the copy to the terminal happens first and never blocks on the
detector: it is a non-blocking send to a bounded channel (1024 lines), and a
full channel drops the line and increments a counter rather than applying
back-pressure to the child.

## 4. `ObservedAt`

An event's `ObservedAt` is the time of the event, not the time monitor
happened to read it:

- **Live** (`monitor run --`): `ObservedAt` is wall-clock "now" at the moment
  the line was read, since the source is live.
- **Reprocessed** (`monitor stacktrace parse [--record]`): `ObservedAt` comes
  from the timestamp printed on the log line itself when the parser can find
  one; otherwise it falls back to the log file's mtime. It is never the
  current time. This makes re-running `stacktrace parse` over an old log
  idempotent instead of bumping every issue's `last_seen` to "now", and keeps
  the store's auto-reopen rule (comparing `ObservedAt` to `resolved_at`)
  correct for an old replay.

## 5. Dedupe keys and parse checkpoints

Two independent idempotency mechanisms cover the two entry points:

- **Live occurrences** (`monitor run --`) use
  `DedupeKey = sha256(MONITOR_LAUNCH_ROOT + hash(exception block))`, matched
  within a ±3s window. This keeps a `monitor run --` that wraps another
  `monitor run --` (a nested launch, e.g. a supervisor script) from recording
  the same crash twice — once from each launch's detector.
- **Reprocessed occurrences** (`monitor stacktrace parse --record`) use
  `DedupeKey = sha256(inode + path + offset + hash(exception block))`, paired
  with the per-file checkpoint from §1 (`{inode, size, offset}`). Re-running
  `--record` over a log that has not grown past its checkpoint replays
  nothing; growth past the checkpoint parses only the new bytes.
- **Checkpoint reset**: the checkpoint resets to `offset: 0` whenever the
  file's current inode no longer matches the stored one, or the file's
  current size is smaller than the stored offset. Both signal rotation or
  truncation (logrotate, a restarted service overwriting its own log) rather
  than steady growth, so re-reading from the start is correct; without this a
  rotated log would either be skipped forever (the stale offset lands past
  the new file's EOF) or silently miss its first bytes.

Both dedupe keys are occurrence-only context: neither one enters
`FingerprintV2Exception` (§6), so a live occurrence and a later replay of the
same log line still group into the same issue.

## 6. Exception-chain rule (fingerprint and culprit)

An `Exception` carries `Chained []Exception`, ordered from the **outer**
exception to the **innermost cause**, regardless of the order the runtime
printed them in — and runtimes disagree on that order, which is exactly why
this needs a rule instead of "whatever the parser saw first":

- **Outer** = the exception that actually propagated to the top level: a
  Node `uncaughtException`, a Go panic that reached `main`, the traceback the
  Python interpreter itself reports for the process.
- **Innermost cause** = the end of whichever chaining idiom the runtime
  uses: Python's `__cause__`/`__context__` ("...the direct cause of the
  following exception:" / "During handling of the above exception..."),
  Node's `Error.cause` printed as `[cause]`, or Go's `%+v`-formatted
  wrapped-error chain (`pkg/errors`, `fmt.Errorf("%w")`).

Node's `[cause]` and Go's `%+v` both print the outer exception **first** and
the cause **after**, which already matches this ordering. Python is the
opposite: it prints the innermost cause's traceback first, then "During
handling of the above exception..." / "The above exception was the direct
cause of the following exception:", then the **outer** exception's traceback
**last**. A parser that assumed "printed first = outer" would invert every
Python chain — exactly the case this rule exists to normalize. For example,
a Python worker whose top-level handler wraps a parse failure —

```
Traceback (most recent call last):
  File "workload.py", line 31, in parse_row
    raise ValueError(f"bad row {n}")
ValueError: bad row 7

The above exception was the direct cause of the following exception:

Traceback (most recent call last):
  File "workload.py", line 52, in sync
    raise RuntimeError("sync aborted") from exc
RuntimeError: sync aborted
```

— prints `ValueError` first and `RuntimeError` last, but `Chained` must still
record `[RuntimeError, ValueError]` (outer to innermost): the fingerprint
anchors on `RuntimeError` as the outer type, and (per the `Culprit` rule
below) the culprit falls to `ValueError`'s in-app frame at `workload.py:31`,
not `RuntimeError`'s frame at `workload.py:52`. A Node equivalent (`throw new
RuntimeishError(..., { cause: parseErr })`, printed as the outer error
followed by `[cause]: ValueError-ish: bad row 7`) reaches the same
`[outer, innermost]` order directly, without needing to reverse anything.

**`FingerprintV2Exception`** =
`sha256("v2" + "exception" + project + outer.Type + outerFrames + innermost.Type)`.
Each component exists for a reason:

- `"v2"` salts this scheme so it can never collide with `FingerprintV1`
  (the sampled-symbol scheme `investigate` and `watch` alerts still use) or
  with a future, unrelated fingerprint kind that reuses the same hash space.
- `"exception"` is this fingerprint's **kind** tag — which rule produced the
  hash — the same way `Issue.Kind` distinguishes the literal values the store
  actually writes: `exception` (this rule), `investigation`
  (`internal/cli/investigate.go`), and `monitor.alert.<rule>`
  (`internal/cli/watch.go`).
- `project` (from `project.Resolve`) scopes the hash per project. Without it,
  two unrelated projects that happen to share a stack shape (a common
  library's `TypeError`, say) would merge into one issue.
- `outerFrames` is the outer exception's **top-5 `in_app` frames**, function
  and file only, **with line numbers stripped**, rendered `func@relfile`.
  Line numbers are excluded so an unrelated one-line diff above the crash
  does not fragment the same issue into a new one. "Top-5" counts from the
  **crash frame backward**: `Exception.Frames` is stored oldest-to-newest
  with the crash frame last, so the top 5 are the slice's last 5 elements
  (closest to where it actually broke), not its first 5 (the oldest calls on
  the stack).
- **Fallback when the outer exception has zero `in_app` frames** — a
  message-only event, e.g. `logging.error("...")` called without
  `exc_info`, or a bare `zap.Error` without a `%+v`-formatted cause —
  `outerFrames` above is replaced by the outer exception's **normalized
  message value**: the same template-normalized string that
  `culprit.source: message_search` (§7) searches on, so "connection refused:
  10.0.4.12:5432" and "connection refused: 10.0.4.19:5432" still fingerprint
  together instead of opening a new issue per IP. This value enters the hash
  **only** in this no-`in_app`-frames case; whenever the outer exception has
  at least one `in_app` frame, the raw message text never enters the
  fingerprint.
- `innermost.Type` anchors the failure's root regardless of which of the two
  rules above produced `outerFrames`.

Sampled/profiler symbols, codemap FQNs, vecgrep scores, PIDs, releases, and
(for exceptions) the service name never enter the fingerprint.

- **`Culprit`** = the crash frame of the **innermost cause** if that frame is
  `in_app`; otherwise it falls back to the crash frame of the **outer**
  exception. Rationale: the innermost cause is usually the one line an
  in-app developer can actually fix, but when even that cause bottoms out
  entirely inside a dependency (`node_modules`, `site-packages`, `vendor`,
  `GOROOT`, gems), the outer exception's own in-app frame is the more
  actionable pointer than a third-party line.
- A message-only event still reaches the fingerprint rule above, via the
  normalized-message fallback — it is not excluded from fingerprinting. What
  it skips is `Culprit`'s stack-based path: with no `in_app` frame anywhere
  in the chain, `Culprit` falls through to `message_search`; see §7.

## 7. `culprit.source`

`Culprit.Source` records how the culprit was found, since a stack-derived
culprit and an inferred one carry different confidence:

- `stack`: the culprit is a real frame from a parsed stack trace (§6).
- `message_search`: there were no `in_app` frames to blame (a message-only
  event), so the culprit was inferred by searching the message text —
  `vecgrep search --mode keyword` when the project's branch index is ready,
  falling back to `git grep -F` otherwise. A `message_search` culprit is
  always marked `mapping: inferred`, carries lower confidence, and — per the
  golden rule that error text never reaches an embedding provider — only
  ever uses vecgrep's keyword (BM25) mode, never its semantic mode.

Both variants report `via` (`vecgrep` or `git_grep`) when `source` is
`message_search`, and both degrade to `status: skipped` with an explicit
`recovery`, never a fabricated line, when neither vecgrep nor git are
available.

## 8. Launch service registry (`monitor.run-service.v1`, E3.2)

`monitor run --name <n> -- <cmd>` registers the launch so a later `monitor
hot <service>` can find its real runtime leaf process without the caller
having to know or re-derive its pid:

- **Path**: `$XDG_STATE_HOME/monitor/services/<project>/<name>.json`
  (`$XDG_STATE_HOME` defaults to `~/.local/state` when unset, matching
  `internal/stacktrace`'s parse-checkpoint convention). `<project>` is
  `project.Identity.Slug` and `<name>` is the launch's effective service
  name (`--name`, or the same fallback chain `MONITOR_LAUNCH_SERVICE`
  itself uses) — the same two identifiers `monitor issues --project/
  --service` already filters by, so a registry entry and an issue recorded
  by the same launch are always addressable by the same two names.
- **Permissions**: the `monitor/services` directory tree is created `0700`
  and every entry file `0600` — the same private-by-default posture as the
  parse checkpoints and the incident registry. Nothing about a launch's
  command line (argv) is ever written into it.
- **Written atomically**: a temp file in the same directory, then
  `os.Rename` into place — a process killed mid-write must never leave a
  half-written, corrupt registry entry for a reader to trip over, the same
  rule `stacktrace.SaveCheckpoint` already follows.
- **Removed on exit**: `monitor run --` deletes its own entry once the
  child has exited (best-effort; a killed-by-SIGKILL `monitor` process
  cannot clean up after itself). A **stale entry** — its recorded `pid` is
  no longer alive — is therefore an expected, ordinary case, not corruption:
  every reader (`monitor hot <service>`) checks liveness itself and treats a
  dead-pid entry exactly like a missing one (with a recovery naming the
  service as unregistered), rather than erroring.
- **Shape**: `{schema, launch_id, pid, name, project, started_at, scan,
  inspectors: [{pid, port, ws}]}`. `pid` is the **launched** process
  (`cmd.Process.Pid` from `devrun.Run`), not any later-resolved runtime
  leaf — leaf resolution (`procbind.ResolveLeaf`, skipping a shell/yarn/npm/
  `go run` wrapper) happens at READ time in `monitor hot <service>`, using
  this `pid` as the BFS root, exactly like `monitor hot <pid>` already
  does for a numeric target. This keeps the registry honest about what was
  actually launched and keeps leaf resolution as the one thing that decides
  which descendant is the "real" application process, rather than
  duplicating that logic into the write path too.
- **`inspectors[]`**: populated only under `--inspect` (E3.3b, below); each
  entry is one parsed "Debugger listening on ws://host:port/uuid" banner,
  with `pid` resolved from the banner's **port**, by asking which live
  process owns that listening TCP socket (never trusted from the banner
  text itself, which names no pid) — the same "prove it by the listening
  socket's owner, not by whoever claims it" posture
  `internal/profiler/ownership.go`'s `VerifyListenerOwnership` already
  applies to a pprof endpoint. `ws` (the full `ws://...` URL, whose UUID
  path segment is the inspector protocol's only bearer-token-shaped secret)
  is written to this 0600 file and this file ONLY: it is never printed by
  `monitor run`'s own banners, never returned by `monitor hot`'s `--json`,
  and never reaches MCP — every one of those surfaces reports the `port`
  alone. A wrapper that itself prints its own inspector banner (`yarn`
  re-exec'ing `node --inspect`, which duplicates Node's own banner line) is
  recorded as its own, separate `inspectors[]` entry rather than
  deduplicated — `monitor hot <service>`'s consumer, not the registry
  writer, decides which one actually owns the resolved leaf pid.
- **`monitor hot <service> [--project P]`**: resolves `<project>` from the
  current working directory by default (`project.Resolve`'s ordinary rule),
  reads that project's `<name>.json`, and:
  - prefers a registered `inspectors[]` entry whose `pid` equals (or is an
    ancestor of) the resolved leaf, for a Node/Deno target — this skips
    re-discovering the inspector port from scratch, and is what makes
    `monitor run --name w --inspect -- yarn start` then `monitor hot w`
    profile the real `node` child (via its own already-recorded inspector),
    not `yarn`;
  - otherwise resolves the leaf from the registered `pid` via
    `procbind.ResolveLeaf` exactly like `monitor hot <pid>` — this is what
    makes `monitor run --name q --scan both -- go run .` then `monitor hot
    q` sample the compiled child, never the `go run` toolchain process.
  - An unknown `<service>` (no registry entry, or a stale/dead one) exits 2
    and lists every currently-registered, still-live service for that
    project, the same "never guess, list the candidates" posture
    `procbind.AmbiguousLeafError` already uses.

Rejected: pinning `MONITOR_RUN_DIR` at launch time as the registry key
(that variable is owned by the legacy `monitor run <spec>`, §2, and is not
set by `run --` at all) or keying the registry file by pid instead of
`<project>/<name>` (a pid is reused across launches and tells a caller
nothing about which service they meant, whereas `--name` is exactly what a
person already types to identify one).
