# Ecosystem Integration

Monitor lives in a family of small, local-first tools. Rather than reimplement
secret storage, artifact archival, or behavioral specs, it shells out
to sibling binaries when they happen to be on your `$PATH` and adds value on top
of them.

Every one of these integrations is **optional**. Monitor probes each tool by
name and degrades gracefully when it is missing: the core TUI, CLI, and MCP
surfaces never depend on any of them. When a tool is absent, the wrapping
command tells you so (or, for incident capture, keeps the bundle locally) — it
never crashes.

The tools Monitor knows about are wrapped in `internal/ecosystem`. The registry
provides typed health probes, while the code-intelligence adapter owns the
codemap, vecgrep, and ArtifactRefV1 contracts.

| Tool | Binary on `$PATH` | What Monitor uses it for |
|------|-------------------|--------------------------|
| file.cheap | `fcheap` | Incident evidence (`stash`, `investigate`, `watch --stash`, `incidents`) plus validated `artifact-ref` handoff |
| tinyvault | `tvault` | Secret injection into a child process (`vault`) |
| glyphrun | `glyph` | Running behavioral specs (`run`) |
| codemap | `codemap` | Auto-correlate profile frames and entry scripts during `investigate` (`symbol-at` + `impact`, bound with `-C <codebase>`) |
| vecgrep | `vecgrep` | Semantic/similar code search during `investigate` against the bound codebase |
| vidtrace | `vidtrace` | Reported by `doctor` |
| cairntrace | `cairn` | Reported by `doctor` |
| veclite | `veclite` | Reported by `doctor` (also the embedded log store) |
| tmux | `tmux` | Reported by `doctor` |

Note the binary names do not always match the project names: tinyvault's binary
is `tvault`, glyphrun's is `glyph`, and cairntrace's is `cairn`.

## Checking availability: `monitor doctor`

`monitor doctor` probes every tool above and reports whether it is installed,
its resolved path, and its version. It is the first thing to run when an
integration "isn't working" — usually the answer is that the binary isn't on
`$PATH`.

```bash
monitor doctor          # human-readable health table
monitor doctor --json   # machine-readable, for agents
```

Each probe runs `<binary> --version` with a 2-second timeout. A tool that is
not found is reported with a note like `fcheap not on PATH` rather than an
error — a missing tool is a normal, expected state.

The JSON form returns one object per tool, each shaped like:

```json
{
  "available": true,
  "version": "fcheap 1.2.0",
  "path": "/usr/local/bin/fcheap"
}
```

## Incident stashes with fcheap

Monitor uses **fcheap** as its incident vault. When something misbehaves,
Monitor bundles the current system snapshot (and, where relevant, a process
profile and the alert that triggered the capture) into a temp directory,
computes a `sha256` tree-hash of the bundle's contents, and shells out to
`fcheap save` with that hash as a tag. The implementation lives in
[`internal/incidents/incidents.go`](https://github.com/abdul-hamid-achik/monitor).

The tree hash is an integrity and correlation key; it does not promise that
two captures receive the same file.cheap stash ID. Snapshots and profiles
normally contain timestamps, so repeated real captures generally differ. Tags
still let you search incidents by trigger, alert rule, or PID. Every Monitor stash carries the
`monitor-incident` tag plus tags like `trigger:investigate`,
`snapshot:<hash12>`, `alert:<rule>`, and `pid:<n>`.

### `monitor stash` — capture a snapshot now

```bash
monitor stash --json
monitor stash --note "before risky deploy" --ttl 30d
```

`stash` bundles the current system snapshot and returns the stash ID. Use it to
capture a "before" state ahead of a risky operation, or for manual triage when
the analyzer didn't fire. Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--note` | `""` | Free-form note recorded with the stash for downstream search |
| `--ttl` | `7d` | Stash lifetime, passed through to `fcheap --ttl` |
| `--json` | `false` | Emit JSON |

### `monitor investigate <pid>` — the diagnostic pipeline

```bash
monitor investigate 1234 --json
monitor investigate 1234 --no-save   # bundle locally, skip fcheap
```

`investigate` runs `identify` → `snapshot` → `profile` → `correlate` →
`semantic` → `stash` → `issue`. It binds the target to a runtime/codebase,
prefers an ownership-verified Node inspector profile when available, adds
bounded codemap and vecgrep context, archives the evidence, and groups the
occurrence. The output reports every step and its limitations.

| Flag | Default | Meaning |
|------|---------|---------|
| `--ttl` | `7d` | Stash lifetime, passed through to `fcheap --ttl` |
| `--no-save` | `false` | Skip file.cheap archival; keep the profile in JSON and still record the issue occurrence |
| `--codebase` | auto-detect | Project root used by codemap and vecgrep |
| correlation flags | environment | `--environment`, `--deployment-id`, `--run-id`, `--release`, `--service`, `--git-sha` |
| `--json` | `false` | Emit JSON |

### `monitor incidents` — list what you've captured

```bash
monitor incidents
monitor incidents --json
```

This lists recent monitor stashes, with the `monitor-incident` tag pre-applied
so you only see Monitor's own captures, not every fcheap stash on the system.

If fcheap was missing or the archive failed when an incident was captured,
the bundle is retained locally (in a durable registry, not the ephemeral
temp dir the capture started in) and the failed `stash`/`investigate` result
carries a `registry_id`. `monitor incidents pending` lists what's still
waiting, and `monitor incidents resume-stash <id-or-path>` retries the
archive once fcheap is back — evidence survives a restart instead of being
lost with the temp dir. See the [CLI Reference](./cli#incidents).

### Stash-on-alert during `watch`

[`monitor watch`](./cli.md) can capture an incident automatically every time the
analyzer raises an alert:

```bash
monitor watch --json --stash
monitor watch --stash --stash-ttl 24h
```

With `--stash`, each alert fires a capture in the background and emits the
outcome as a separate NDJSON line (`{"type":"stash",...}`). `--stash-ttl`
(default `7d`) controls how long those automatic stashes live.

### Graceful degradation

If `fcheap` is not on `$PATH`, capture does **not** discard the evidence.
Monitor moves the complete bundle into its durable recovery registry under
`$XDG_STATE_HOME/monitor/incidents` (or
`~/.local/state/monitor/incidents`) and returns a registry ID. If registration
itself fails, the original temporary bundle remains as a last-resort recovery
path. The registry keeps the 20 newest entries.

### ArtifactRefV1 handoff

After a successful save, Monitor asks fcheap for a credential-free reference.
It only surfaces the result when the strict local contract validates:

- `$schema` is `urn:filecheap.dev:artifact-ref:v1` and `version` is `1`;
- `provider` is `fcheap-local`;
- `artifact_id` is portable and `uri` exactly equals
  `fcheap://stash/<artifact_id>`;
- `kind` is present and local references do not include `web_url`.

Chalupa can store this reference beside a run without copying profile or
incident bytes into telemetry. file.cheap remains the evidence owner.

## Secret injection with tinyvault

`monitor vault` wraps a command with **tinyvault** (`tvault run`) so secrets
from a named vault project land in the child process's environment — without the
agent that invoked Monitor ever seeing the secret values.

```bash
# Launch a service with its secrets injected
monitor vault --project myapp -- /usr/local/bin/myapp --port 8080

# Debug: see exactly which env vars would be injected
monitor vault --project myapp -- env
```

Everything after the `--` separator is the command and its arguments. Under the
hood Monitor runs `tvault run --project <name> -- env <your command>`, so
tinyvault resolves the project's secrets, injects them as environment variables,
and `env` passes them through to your command. With no command, `env` simply
prints the injected environment so you can verify a project's secrets.

`--project` is required. If `tvault` is not on `$PATH`, the command exits with
`tvault not found on $PATH; install tinyvault first` rather than running your
command without its secrets.

## Behavioral specs with glyphrun

`monitor run` executes a **glyphrun** spec via the `glyph` binary, returning its
JSON result. This lets you drive behavioral specs against services while Monitor
is observing them.

```bash
monitor run specs/version.yml
```

Monitor invokes `glyph run --format json <spec>`. When Monitor launches a child
process or spec it sets `MONITOR=1` in the child's environment, so the spec (or
any process it spawns) can detect that it is being observed.

## Code correlation with codemap + vecgrep

`monitor investigate <pid>` automatically binds the process to a codebase
(auto-detected from cwd/`package.json`/`go.mod`, or `--codebase`) and then:

1. **codemap** — bounded `symbol-at` + `impact` calls with `-C <codebase>` on
   profile frames and the process main script. Only resolvable file/line
   positions become correlations.
2. **vecgrep** — a bounded `json-envelope` readiness query first verifies that
   the index exists and is fresh. Similar/search hits are capped; a stale or
   missing index remains visible as a limitation instead of being hidden.

Results land in the investigate JSON (`correlations`, `semantic_hits`) and,
when stashed, in `correlations.json` / `semantic.json` inside the fcheap
bundle. Missing binaries degrade to step status `skipped` with a recovery
hint — core Monitor behavior never hard-fails on them.

```bash
monitor doctor --require codemap,vecgrep,fcheap
monitor process 1234 --codebase ~/projects/my-app --json   # binding preview
monitor investigate 1234 --codebase ~/projects/my-app --json
```

Cross-repo handoff (Chalupa CI + file.cheap ArtifactRefV1) is specified in
the [Monitor incident v1 contract](../contracts/monitor-incident-v1.md).

## Summary

- Run [`monitor doctor`](./cli.md) first to see what is installed.
- fcheap powers `stash`, `investigate`, `incidents`, and `watch --stash`; all
  keep recoverable local evidence when archival fails, and successful saves
  may emit a validated ArtifactRefV1.
- tinyvault powers `vault` for leak-free secret injection.
- glyphrun powers `run` for behavioral specs.
- codemap + vecgrep power automatic process→code correlation in `investigate`.

None of these are required to use Monitor — they are bonuses that light up when
the matching tool is on your `$PATH`.
