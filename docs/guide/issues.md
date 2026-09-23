# Local Issues

Monitor provides a Sentry-like local workflow for observations it creates. It
groups recurring events into durable issues, keeps each occurrence with its run
context and evidence references, and lets you triage the result without a
hosted backend.

This is not a Sentry SDK or protocol implementation: there is no client
library to install, no envelope wire format, and issue data never leaves your
machine. Monitor does ingest application exceptions, though — without an SDK.
Its stack-trace parser turns a crash or a caught-and-printed error straight
out of a process's stderr/stdout or an existing log file (Node, Deno, Bun,
Python, Ruby, and Go are all covered) into a structured exception, and
`issues.RecordException` turns that into a durable issue with a culprit
`file:line`, its in-app frames, and its causal chain — see
[Exception issues](#exception-issues) below. What monitor still does not do
is symbolicate source maps for that exception path, accept a Sentry SDK
envelope over the wire, or send issue data to a cloud service.

## The data model

```text
Run context ──→ Event (occurrence) ── fingerprint v1 ──→ Issue
                       │                              │
                       └── Evidence refs               └── lifecycle state
                           fcheap://stash/...              open/resolved/ignored
                           monitor://incidents/...
```

- **Run** is optional correlation metadata: run, environment, deployment,
  step, suite, attempt, release, and Git SHA. Run fields never affect grouping.
- **Event** is one persisted occurrence produced by `monitor investigate` or
  by an alert captured with `monitor watch --stash`.
- **Issue** groups events with the same stable fingerprint and tracks first
  seen, last seen, occurrence count, and lifecycle state.
- **Evidence** is a credential-free reference. A successful file.cheap archive
  uses `fcheap://stash/<id>`; a locally retained bundle uses
  `monitor://incidents/<registry-id>`.

Fingerprint V1 normalizes project, service, kind, exception type, message, and
symbols. Dynamic numbers, UUIDs, and long hexadecimal values in messages are
normalized, and symbol order does not matter. PID, timestamp, run, release,
tree hash, and artifact identity deliberately remain occurrence context, so a
restart or CI retry does not split the same problem into a new issue. This is
the scheme `monitor investigate` and `monitor watch`'s alert rules still use.

A parsed application exception instead groups under fingerprint V2 (see
[Exception issues](#exception-issues)): a different, narrower scheme built
around the exception's type and in-app call shape rather than a normalized
free-text message. `fingerprint_version` on the stored issue says which
scheme produced it; nothing about reading or triaging an issue depends on
that distinction.

## Exception issues

`issues.RecordException` is the single place a parsed
`stacktrace.Exception` becomes a durable issue occurrence. Two later
pieces of local Sentry share it: `monitor run -- <cmd>` (which detects a
crash or a printed error live, from a launched process's stderr/stdout) and
`monitor stacktrace parse --record` (which replays an existing log file
idempotently). Both call the same function so the fingerprint and culprit
rule below has exactly one implementation, not one per entry point.

**FingerprintV2Exception** hashes:

- the project (from `internal/project.Resolve`),
- the outer exception's type (the one that actually propagated: an
  `uncaughtException`, a Go panic that reached `main`, the traceback the
  interpreter itself reports),
- the outer exception's top-5 **in-app** frames, closest to the crash,
  rendered `func@relfile` with **no line or column numbers** — so a one-line
  diff elsewhere in the file does not fragment the same crash into a new
  issue — or, when the outer exception has no in-app frame at all (a
  message-only event such as `logging.error(...)` called without
  `exc_info`), the outer exception's normalized message instead, and
- the innermost cause's type, from the end of the exception's chain
  (`Chained`) when there is one.

Sampled/profiler symbols, codemap FQNs, vecgrep scores, PIDs, releases, and
the service name never enter this hash.

**Culprit** is the single frame monitor blames as the most actionable line:
the innermost cause's own **last in-app frame** when it has one, otherwise
the outer exception's own last in-app frame, otherwise no culprit at all. A
cause's "last in-app frame" does not have to be its literal crash frame —
walking backward from the crash frame to the nearest in-app one still counts:
an exception raised inside a runtime, stdlib, or dependency function that
in-app code called directly (Node's `fs.readFileSync` raising `ENOENT`,
Python's `json.loads` raising `JSONDecodeError`) still blames that in-app
caller, not nothing. Culprit only falls through to nothing when there is no
in-app frame anywhere in the chain — a crash that bottoms out entirely inside
`node_modules`, `site-packages`, `vendor`, or a language's own standard
library, with no in-app caller above it either. The rationale: the innermost
cause is usually the one line an in-app developer can actually fix, but when
even that bottoms out inside a dependency, the outer exception's own in-app
frame is a more actionable pointer than a third-party line.

**Chain ordering.** Runtimes disagree on which order they print a chained
exception in — Node's `[cause]` and Go's `pkg/errors`-style `%+v` both print
the outer exception first and the cause after; Python prints the innermost
cause's traceback first, then "The above exception was the direct cause of
the following exception:" (or "During handling of the above exception..."),
then the outer exception last. The parser normalizes both into the same
`Chained` order (outer's immediate cause down to the innermost/root cause)
before fingerprinting ever runs, so the identity rule above never has to
special-case a runtime's print order.

Each stored issue additionally carries `culprit`, `latest_exception` (type,
value, runtime, handled, up to 12 in-app frames, and up to 3 causes — always
including the innermost one — with their own culprits; capped around 2 KB,
with the exception `value` truncated first when a message runs long, so a
pathologically long message never crowds out every frame), `first_git_sha`
(from the run context's Git SHA — `MONITOR_GIT_SHA`, `GIT_SHA`, or
`GITHUB_SHA`, whichever is set; **not** resolved from the local git HEAD, so
an ordinary local dev session with none of those set leaves it empty),
`level` (`fatal`, `error`, or `warning`), and `handled`. A stored occurrence
keeps its own `exception` detail only on an issue's *first* occurrence, to
bound store size; every later occurrence relies on the issue's
`latest_exception` instead, which always tracks the occurrence with the
*newest* `observed_at` seen so far — replaying an older log line after a
newer one was already recorded never overwrites it with stale detail. All of
this is additive JSON: an issue or occurrence written before this existed
still decodes with these fields simply absent.

An event with in-app frames but no exception type or message at all — for
example a Ruby `rescue`-printed backtrace (`warn e.backtrace`) — still
records: its fingerprint and culprit come from its frames exactly as usual,
and its title/message fall back to the culprit-facing frame ("`func
(file:line)`") since there is no type/value text to show instead.

`issues.RecordException` does not scrub the exception text itself — the
caller (`monitor run --`, `monitor stacktrace parse --record`) is expected
to run it through `internal/scrub` first, the same "error text is untrusted
data" rule every other local-Sentry entry point follows. It also never calls
codemap: a culprit's fully-qualified name is filled in later, on read, by a
codemap-aware reader.

## Create occurrences

An investigation always attempts to persist an occurrence as its seventh
pipeline step:

```bash
monitor investigate 1234 --codebase "$PWD" --json
```

The steps are `identify`, `snapshot`, `profile`, `correlate`, `semantic`,
`stash`, and `issue`. `--no-save` skips file.cheap archival but still records
the issue occurrence; it simply has no stash evidence reference.

Alert-driven issue creation is opt-in through `--stash`:

```bash
monitor watch --json --stash
```

Each captured alert produces a `stash` NDJSON event. When issue persistence
succeeds, that event also contains `issue` and `occurrence`; an independent
`issue_error` reports a local-store failure without hiding the stash result.
Plain `monitor watch` does not persist issues.

## Triage from the CLI

The store defaults to `~/.local/share/monitor/issues.veclite`. Its parent
directory is mode `0700` and the database is mode `0600`. Use the persistent
`--store` flag on the `issues` command tree to inspect a different store.

```bash
monitor issues list
monitor issues list --status open --project checkout --service api --json
monitor issues list --since 24h --run-id ci-4821 --release v1.16.0 --kind exception --json
monitor issues show ISS-0123456789ABCDEF --occurrences 50 --json
monitor issues resolve ISS-0123456789ABCDEF
monitor issues ignore ISS-0123456789ABCDEF
monitor issues reopen ISS-0123456789ABCDEF
```

`issue` is an alias for `issues`. Lists are newest-first. `--status` is
repeatable and accepts `open`, `resolved`, or `ignored`; list and occurrence
limits default to 50 and 20 respectively and must be between 1 and 200.

`list` also takes window filters:

| Flag | Matches |
|------|---------|
| `--since` / `--until` | an issue whose activity window (`first_seen`..`last_seen`) overlaps the given bound. Accepts an RFC3339 timestamp or a duration (`10m`, `24h`) meaning "that long ago". |
| `--run-id` | an issue that has this run id among its 25 most recently seen (see below). |
| `--release` | an issue that has this release among its 25 most recently seen. |
| `--kind` | `exception`, `alert` (any `monitor.alert.<rule>` kind), `investigation`, or `any` (the default). |

`--since`/`--until` test whether the issue's *whole activity window*
overlaps the bound, not whether an occurrence actually landed inside it: an
issue first seen 10 hours ago and last seen 1 hour ago still matches a
`--since 6h --until 4h` window even though nothing happened in that
particular 2-hour slice. This keeps a window query fast (`list` only reads
the issues collection, never occurrences) at the cost of an occasional false
positive; confirm against `monitor issues show <id>`'s own occurrence
timestamps when precision matters.

`--run-id`/`--release` match against a small, bounded, deduplicated set of
every distinct value an issue has seen, capped at its 25 *most recently
seen* — not a scan of every retained occurrence, which is what keeps a
window query fast even over a store holding tens of thousands of
occurrences. The tradeoff: a run id or release seen only on an issue's
*older* occurrences (evicted from that set by 25 newer ones) will not match,
and an issue written before this aggregate existed (a v1.15-era issue) never
matches a `--run-id`/`--release` filter at all. The run id itself comes from
`MONITOR_RUN_ID` / `CHALUPA_CI_RUN_ID` / `CHALUPA_RUN_ID` (see
[Chalupa correlation](#chalupa-correlation) below) — not `MONITOR_LAUNCH_ID`,
which is a separate, later-epic identifier for `monitor run -- <cmd>`'s own
launch, not for this run-correlation field.

Lifecycle behavior is intentionally small:

- A new issue starts `open`.
- `resolve` records the resolution time. A later occurrence automatically
  reopens it and increments `reopened_count`; an older out-of-order occurrence
  does not.
- `ignore` keeps recording occurrences without reopening the issue.
- `reopen` explicitly returns either a resolved or ignored issue to `open`.

The store is globally bounded to 10,000 issue groups and 100,000 retained
occurrence bodies. It evicts the oldest issue groups when the issue cap is
exceeded and deletes their retained occurrences. Occurrence-only eviction is
FIFO, but an issue's `occurrence_count` remains cumulative, so it can be larger
than the number of occurrence bodies returned by `show` or `monitor_issue`.

`list`'s rows carry a trimmed `latest_exception` (type, value, runtime,
handled — no frames, no causes); `culprit` stays untouched, so a row still
names a `file:line`. `show` (and MCP's `monitor_issue`) return the full,
untrimmed detail for that one issue instead — listing dozens of issues at
once should not cost every row its full ~2 KB exception budget.

## Read issues through MCP

The two MCP tools are read-only and do not take `confirm`:

| Tool | Input | Result |
|------|-------|--------|
| `monitor_issues` | `statuses`, `project`, `service`, `since`, `until`, `run_id`, `release`, `kind`, `limit` | `{issues, total, truncated}`; default 50, max 200 |
| `monitor_issue` | required `id`, optional `occurrence_limit` | `{issue, occurrences, occurrences_truncated}`; default 20, max 200 |

`monitor_issues`'s `since`/`until`/`run_id`/`release`/`kind` are the exact
same filters the CLI's `--since`/`--until`/`--run-id`/`--release`/`--kind`
flags apply, parsed and matched by the same store code — an agent and a
human asking "which issues touched release v1.16.0 in the last 24 hours"
get the same answer either way.

Issue state changes remain explicit CLI actions. MCP agents can inspect and
recommend a transition, but this release does not expose an MCP mutation for
resolve, ignore, or reopen.

## Chalupa correlation

Monitor reads optional context from explicit investigate flags, `MONITOR_*`,
and Chalupa CI variables. Explicit values win. The principal CI variables are:

| Field | Chalupa variable |
|-------|------------------|
| environment | `CHALUPA_CI_ENVIRONMENT` |
| run | `CHALUPA_CI_RUN_ID` |
| step | `CHALUPA_CI_STEP_ID` |
| suite | `CHALUPA_CI_SUITE` |
| attempt | `CHALUPA_CI_ATTEMPT` |

Legacy `CHALUPA_ENV`, `CHALUPA_ENVIRONMENT`, and `CHALUPA_RUN_ID` remain
fallbacks. Deployment, release, service, and Git SHA also accept their
documented `MONITOR_*` / `CHALUPA_*` aliases. Context is stored on the
occurrence and bundle manifest, but never added to
`monitor.telemetry_window` V1.

## Evidence recovery

If file.cheap is unavailable, Monitor retains the incident in its private
local registry and stores a `monitor://incidents/<id>` evidence URI. Inspect
and retry it with:

```bash
monitor incidents pending --json
monitor incidents resume-stash <id> --json
```

After a successful save, file.cheap owns the bytes and Monitor can return a
validated, credential-free ArtifactRefV1. The failed-archive registry keeps at
most the 20 newest bundles, so it is a recovery queue rather than a permanent
archive.
