# Local Sentry naming ADR

- **Status**: Accepted
- **Context**: the "local Sentry" epic (see the `feat/local-sentry` roadmap)
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
| Exception chain | see §3 (fingerprint/culprit rule) | picking the outermost or innermost exception ad hoc per parser |
| New packages (MVP) | `internal/stacktrace`, `internal/scrub`, `internal/project`, `internal/devrun`, `internal/explain`, `internal/sourcemap` | `internal/events` for the MVP; a second, duplicated joiner under `internal/capture` |
| Later packages | `internal/event` (a future `monitor.event/v1`), `internal/probe`, `internal/ingest/sentry`, `internal/notes` | `internal/probes`, `devrun/hooks` |
| Probe environment (later epic) | `MONITOR_PROBE_DIR` | `MONITOR_SPOOL_DIR` |
| `Culprit` type | `issues.Culprit{Function, FQN, File, Line, Source}`, `Source` is `stack` or `message_search` | a bare `string`, or a `*HotspotRef` |
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
- When `run --` nests inside another `run --` (a spawned dev server that
  itself runs under `monitor run --`), the child inherits the parent's
  `MONITOR_LAUNCH_*` values unchanged rather than computing new ones, so a
  chain of launches still resolves to one launch identity.
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
  convention — Graphite's zap config sets `OutputPaths`/`ErrorOutputPaths` to
  stdout, and `pino`/many Rails loggers default there too.
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

Both dedupe keys are occurrence-only context: neither one enters
`FingerprintV2Exception` (§6), so a live occurrence and a later replay of the
same log line still group into the same issue.

## 6. Exception-chain rule (fingerprint and culprit)

An `Exception` carries `Chained []Exception`, ordered from the **outer**
exception (what the runtime raised or logged first) to the **innermost
cause** (what "During handling of the above exception..." / "Caused by:" /
Go's `%+v` wrapped-error chain ultimately blames), regardless of the order
the runtime printed them in.

- **`FingerprintV2Exception`** = `outer.Type` + the outer exception's top-5
  `in_app` frames, function and file only, **with line numbers stripped** +
  the innermost cause's `Type`. Line numbers are excluded so an unrelated
  one-line diff above the crash does not fragment the same issue into a new
  one; the outer type anchors the "shape" of the failure while the innermost
  cause's type anchors its root. Sampled/profiler symbols, codemap FQNs,
  vecgrep scores, PIDs, releases, and (for exceptions) the service name never
  enter the fingerprint.
- **`Culprit`** = the crash frame of the **innermost cause** if that frame is
  `in_app`; otherwise it falls back to the crash frame of the **outer**
  exception. Rationale: the innermost cause is usually the one line an
  in-app developer can actually fix, but when even that cause bottoms out
  entirely inside a dependency (`node_modules`, `site-packages`, `vendor`,
  `GOROOT`, gems), the outer exception's own in-app frame is the more
  actionable pointer than a third-party line.
- A message-only event (no frames — `logging.error("...")` without
  `exc_info`, a bare `zap.Error` without `%+v`) never reaches this rule; see
  §7.

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
