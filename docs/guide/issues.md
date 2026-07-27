# Local Issues

Monitor provides a Sentry-like local workflow for observations it creates. It
groups recurring events into durable issues, keeps each occurrence with its run
context and evidence references, and lets you triage the result without a
hosted backend.

This is not a Sentry SDK or protocol implementation. Monitor does not ingest
arbitrary application exceptions, symbolicate source maps, or send issue data
to a cloud service.

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
restart or CI retry does not split the same problem into a new issue.

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
monitor issues show ISS-0123456789ABCDEF --occurrences 50 --json
monitor issues resolve ISS-0123456789ABCDEF
monitor issues ignore ISS-0123456789ABCDEF
monitor issues reopen ISS-0123456789ABCDEF
```

`issue` is an alias for `issues`. Lists are newest-first. `--status` is
repeatable and accepts `open`, `resolved`, or `ignored`; list and occurrence
limits default to 50 and 20 respectively and must be between 1 and 200.

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

## Read issues through MCP

The two MCP tools are read-only and do not take `confirm`:

| Tool | Input | Result |
|------|-------|--------|
| `monitor_issues` | `statuses`, `project`, `service`, `limit` | `{issues, total, truncated}`; default 50, max 200 |
| `monitor_issue` | required `id`, optional `occurrence_limit` | `{issue, occurrences, occurrences_truncated}`; default 20, max 200 |

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
