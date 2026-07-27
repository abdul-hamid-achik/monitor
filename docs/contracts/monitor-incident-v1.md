# monitor.incident v1 contract

This contract defines how Monitor produces local diagnostic evidence, how
file.cheap owns archived bytes, and how Chalupa correlates that evidence with
an ephemeral or CI run. Consumers should store the credential-free reference;
they should not re-derive process identity or copy raw profile bytes into
telemetry.

## Boundaries

- `monitor.telemetry_window` V1 remains a host-metric privacy lane. It never
  contains process identity, stack frames, argv, run IDs, incident context, or
  issue data.
- Incident and issue data remains local unless an operator explicitly passes
  its ArtifactRefV1 to another system.
- Monitor inspects process argv in memory to infer runtime, entrypoint, and an
  inspector address. It never serializes argv into bindings, issues, bundles,
  or telemetry.
- Local file.cheap is optional. When archival fails, Monitor retains evidence
  in a bounded private recovery registry.

## 1. Process binding

```bash
monitor process <pid> --json
monitor process <pid> --codebase /path/to/app --json
```

The additive JSON envelope is:

```json
{
  "pid": 123,
  "name": "node",
  "cpu_percent": 98.4,
  "memory": 339427328,
  "memory_percent": 1.98,
  "threads": 8,
  "user": "developer",
  "status": "S",
  "parent": 1,
  "is_system": false,
  "is_protected": false,
  "metric_states": {"cpu": {"state": "observed"}},
  "binding": {
    "pid": 123,
    "name": "node",
    "exe": "/usr/local/bin/node",
    "cwd": "/app",
    "argv_redacted": true,
    "runtime": "node",
    "main_script": "/app/dist/server.js",
    "codebase_root": "/app",
    "inspect_addr": "127.0.0.1:9230",
    "markers": ["package.json", ".git"],
    "limitations": []
  }
}
```

The existing `ProcessInfo` fields remain at the top level. `binding` (or
`binding_error` when inspection fails) is the only additive field; there is no
nested `process` object.

`runtime` is `node`, `bun`, `deno`, `go`, `python`, or `unknown`. Auto-detect
walks upward from the process cwd/main script for `package.json`, `go.mod`,
`pyproject.toml`, `Cargo.toml`, or `.git`. `--codebase` overrides detection.

## 2. Investigation pipeline

```bash
monitor investigate <pid> --json \
  --codebase /path/to/app \
  --environment "$CHALUPA_CI_ENVIRONMENT" \
  --run-id "$CHALUPA_CI_RUN_ID" \
  --release "$MONITOR_RELEASE" \
  --git-sha "$GITHUB_SHA"
```

The seven steps are:

| Step | Contract |
|------|----------|
| `identify` | Resolve the redacted runtime/codebase binding. |
| `snapshot` | Take a warmed full snapshot so per-process CPU is based on a real counter delta. |
| `profile` | Prefer an ownership-verified Node/Bun/Deno inspector CPU profile; otherwise use owned Go pprof or macOS `sample`. |
| `correlate` | Run bounded codemap `symbol-at` / `impact` calls with `-C <codebase>` on usable file/line positions. |
| `semantic` | Read vecgrep index readiness from its bounded JSON envelope, then keep capped similar/search hits only when the index is present and fresh. |
| `stash` | Build and integrity-hash the bundle; save it with fcheap or retain it locally. `--no-save` skips this step. |
| `issue` | Persist the event as an occurrence grouped into a durable local issue. This still runs with `--no-save`. |

Each receipt has `step`, `status` (`ok`, `failed`, or `skipped`), and optional
`limitation` / `recovery`. Optional tools may produce `skipped`; the overall
verdict is `partial` only when a step failed.

### Context precedence

Explicit investigate fields win, then Monitor variables, then Chalupa CI and
legacy fallbacks:

| Field | Sources in precedence order |
|-------|-----------------------------|
| environment | `--environment`, `MONITOR_ENVIRONMENT`, `CHALUPA_CI_ENVIRONMENT`, `CHALUPA_ENV`, `CHALUPA_ENVIRONMENT` |
| deployment_id | `--deployment-id`, `MONITOR_DEPLOYMENT_ID`, `CHALUPA_DEPLOYMENT_ID` |
| run_id | `--run-id`, `MONITOR_RUN_ID`, `CHALUPA_CI_RUN_ID`, `CHALUPA_RUN_ID`, basename(`MONITOR_RUN_DIR`) |
| step_id | `MONITOR_STEP_ID`, `CHALUPA_CI_STEP_ID` |
| suite | `MONITOR_SUITE`, `CHALUPA_CI_SUITE` |
| attempt | `MONITOR_ATTEMPT`, `CHALUPA_CI_ATTEMPT` |
| release | `--release`, `MONITOR_RELEASE`, `CHALUPA_RELEASE` |
| service | `--service`, `MONITOR_SERVICE`, `CHALUPA_SERVICE`, then process name |
| git_sha | `--git-sha`, `MONITOR_GIT_SHA`, `GIT_SHA`, `GITHUB_SHA` |

The CLI exposes flags for environment, deployment, run, release, service, and
Git SHA. Its step/suite/attempt values come from the environment. MCP
`monitor_investigate` accepts `codebase`, `environment`, `deployment_id`,
`run_id`, `step_id`, `suite`, `attempt`, `release`, `service`, `git_sha`, `ttl`,
and `no_save`, plus required `pid` and `confirm:true`. Each non-empty MCP field
is the explicit override and therefore wins over the environment.

## 3. Bundle layout

Kind: `monitor.incident`

Native schema: `urn:monitor.dev:incident:v1`

| File | Required | Content |
|------|----------|---------|
| `manifest.json` | yes | Bundle header, declared file list, run context, and optional `raw_profile`. |
| `snapshot.json` | yes | `collector.SystemInfo`. |
| `profile.json` | no | Verified profile metadata/text/symbols. Its path is bundle-relative when raw bytes exist. |
| `profile.data` | no | Copied raw profile bytes, declared by `manifest.raw_profile`; maximum 128 MiB. |
| `process.json` | no | Redacted process binding. `cmdline` is removed before persistence. |
| `correlations.json` | no | Bounded codemap rows. |
| `semantic.json` | no | Bounded vecgrep hits. |

Example manifest:

```json
{
  "schema_version": "1",
  "kind": "monitor.incident",
  "trigger": "investigate",
  "alert": {
    "rule": "investigate",
    "pid": 123,
    "process": "node",
    "detail": "manual investigate of pid 123"
  },
  "ttl": "7d",
  "context": {
    "environment": "preview-42",
    "run_id": "run-abc",
    "step_id": "test",
    "suite": "pull-request",
    "attempt": "2",
    "release": "v1.15.0",
    "service": "api",
    "git_sha": "abc123"
  },
  "profile_method": "inspector_cpu",
  "codebase_root": "/app",
  "runtime": "node",
  "main_script": "/app/dist/server.js",
  "raw_profile": "profile.data",
  "files": [
    "manifest.json",
    "snapshot.json",
    "profile.json",
    "profile.data",
    "process.json",
    "correlations.json",
    "semantic.json"
  ]
}
```

Files are present only when their corresponding data exists. The declared file
list must match the flat bundle. Directory entries, symlinks, undeclared files,
missing declared files, invalid schema/kind, and oversized raw profiles are
rejected during recovery validation. A recovery bundle is capped at 32 files,
128 MiB per file, 256 MiB total, and 1 MiB for `manifest.json`.

### Integrity and local recovery

Monitor hashes sorted length-prefixed file names and contents with SHA-256. The
tree hash identifies the exact bundle bytes and is added as a tag; it does not
guarantee that separate captures receive the same file.cheap stash ID.

Bundle/registry directories use mode `0700` and files use `0600`. Registered
paths are derived from a validated 12-hex ID rather than trusting
`entry.json`. Before resume, Monitor recomputes the tree hash and refuses
tampered evidence. The registry keeps the 20 newest failed archives.

Searchable fcheap tags include `monitor-incident`, `trigger:<trigger>`, and
`snapshot:<treeHash12>`, plus available alert, PID, confidence, runtime,
codebase basename, and run-context tags. The producer tool is `monitor`.

## 4. ArtifactRefV1

After a successful stash, Monitor invokes:

```bash
fcheap artifact-ref <stash-id> --json \
  --kind monitor.incident \
  --producer-tool monitor \
  --native-schema urn:monitor.dev:incident:v1 \
  --native-id <treeHash16> \
  --entrypoint manifest.json
```

The reference is surfaced only if all local invariants validate:

```json
{
  "$schema": "urn:filecheap.dev:artifact-ref:v1",
  "version": 1,
  "provider": "fcheap-local",
  "uri": "fcheap://stash/<id>",
  "artifact_id": "<id>",
  "kind": "monitor.incident",
  "producer": {
    "tool": "monitor",
    "native_schema": "urn:monitor.dev:incident:v1",
    "native_id": "<treeHash16>",
    "entrypoint": "manifest.json"
  }
}
```

Monitor requires schema/version 1, provider `fcheap-local`, a portable local
artifact ID, exact URI/ID correspondence, a non-empty kind, valid producer
fields, and no `web_url`. Unknown or malformed output is not forwarded.

## 5. Run, Event, Issue, and Evidence

The issue step stores the diagnostic event in
`~/.local/share/monitor/issues.veclite` (or `$XDG_DATA_HOME`). The occurrence
contains optional `RunContext` and `EvidenceRef` values. Evidence normally uses
the validated `fcheap://stash/<id>` URI; failed archival uses
`monitor://incidents/<registry-id>`.

### RunContext

All fields are optional and omitted when empty:

```json
{
  "id": "run-abc",
  "environment": "preview-42",
  "deployment_id": "dep-123",
  "step_id": "test",
  "suite": "pull-request",
  "attempt": "2",
  "release": "v1.15.0",
  "git_sha": "abc123"
}
```

### EvidenceRef

`kind` and `uri` are required in the typed reference; `tree_hash` is optional:

```json
{
  "kind": "monitor.incident",
  "uri": "fcheap://stash/<id>",
  "tree_hash": "<64 lowercase hex characters>"
}
```

A pending local archive uses kind `monitor.incident.pending` and URI
`monitor://incidents/<registry-id>`.

### Event / occurrence

`Event` is an alias of the persisted occurrence. This is its JSON shape;
`run_id`, `release`, `pid`, `tree_hash`, `metadata`, and `run` are omitted when
empty. `evidence_refs` is the compatibility URI list and `evidence` is the
typed representation; both serialize as arrays.

```json
{
  "id": "OCC-0123456789ABCDEF01234567",
  "issue_id": "ISS-0123456789ABCDEF",
  "observed_at": "2026-07-27T12:00:00Z",
  "project": "chalupa",
  "service": "api",
  "kind": "investigation",
  "title": "Investigation: api",
  "message": "manual process investigation",
  "exception_type": "",
  "symbols": ["server.handleRequest"],
  "severity": "warning",
  "run_id": "run-abc",
  "release": "v1.15.0",
  "pid": 123,
  "tree_hash": "<64 lowercase hex characters>",
  "evidence_refs": ["fcheap://stash/<id>"],
  "metadata": {
    "environment": "preview-42",
    "trigger": "investigate"
  },
  "run": {
    "id": "run-abc",
    "environment": "preview-42",
    "release": "v1.15.0"
  },
  "evidence": [
    {
      "kind": "monitor.incident",
      "uri": "fcheap://stash/<id>",
      "tree_hash": "<64 lowercase hex characters>"
    }
  ]
}
```

### Issue

An issue is the stable group returned by `monitor issues` and the read-only MCP
tools:

```json
{
  "id": "ISS-0123456789ABCDEF",
  "fingerprint": "<64 lowercase hex characters>",
  "fingerprint_version": "v1",
  "project": "chalupa",
  "service": "api",
  "kind": "investigation",
  "title": "Investigation: api",
  "message": "manual process investigation",
  "exception_type": "",
  "symbols": ["server.handleRequest"],
  "severity": "warning",
  "status": "open",
  "first_seen": "2026-07-27T12:00:00Z",
  "last_seen": "2026-07-27T12:00:00Z",
  "occurrence_count": 1,
  "reopened_count": 0
}
```

`status` is `open`, `resolved`, or `ignored`. `resolved_at` is present only for
a resolved issue.

Fingerprint V1 excludes run, release, PID, timestamp, tree hash, and artifact
identity. Those values correlate a particular occurrence but must not fragment
the same recurring problem. A resolved issue reopens on a later occurrence;
an ignored issue continues accumulating without reopening.

The store retains at most 10,000 issue groups and 100,000 occurrence bodies
globally. When the issue cap is exceeded, the oldest issue group and its
retained occurrences are evicted. Occurrence-body eviction is FIFO and does
not decrement the owning issue's cumulative `occurrence_count`; consumers must
therefore treat that counter as lifetime observations retained by the issue,
not as the number of occurrence bodies currently queryable.

## 6. Consumer behavior

Chalupa should retain the ArtifactRefV1 alongside its suite/run evidence and
join it to performance telemetry using run/deployment context and time. It
must not insert profile or incident bytes into telemetry tables.

file.cheap owns the archived bytes and manifest. Monitor consumes the current
fcheap JSON contract (`source_path`, `total_size`, `file_count`, `files`, and
`content_hash`) and treats ArtifactRefV1 as the portable handoff.

Codemap and vecgrep are optional enrichers. Missing codemap, unresolved source
positions, a missing/stale vecgrep index, timeouts, or warnings remain explicit
limitations; they never fabricate code evidence or prevent the issue step.

## Conformance checklist

- Process JSON and bundles contain no argv or environment variables.
- Investigation returns all seven typed steps.
- Context accepts the `CHALUPA_CI_*` run fields without entering telemetry.
- Bundle files match `manifest.files`; raw bytes are capped and declared.
- Recovery validates paths, file types, schema, declared layout, and tree hash.
- ArtifactRefV1 is credential-free and strictly validated before handoff.
- Issue fingerprint excludes occurrence-only context and evidence identity.
