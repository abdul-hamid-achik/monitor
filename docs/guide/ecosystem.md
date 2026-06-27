# Ecosystem Integration

Monitor lives in a family of small, local-first tools. Rather than reimplement
secret storage, content-addressed stashing, or behavioral specs, it shells out
to sibling binaries when they happen to be on your `$PATH` and adds value on top
of them.

Every one of these integrations is **optional**. Monitor probes each tool by
name and degrades gracefully when it is missing: the core TUI, CLI, and MCP
surfaces never depend on any of them. When a tool is absent, the wrapping
command tells you so (or, for incident capture, keeps the bundle locally) — it
never crashes.

The tools Monitor knows about are wrapped in
[`internal/ecosystem/registry.go`](https://github.com/abdul-hamid-achik/monitor),
which exposes a typed `Probe()` health check and thin `run`/`runJSON` helpers
around each binary's `--json` output.

| Tool | Binary on `$PATH` | What Monitor uses it for |
|------|-------------------|--------------------------|
| fcheap | `fcheap` | Content-addressed incident stashes (`stash`, `investigate`, `watch --stash`, `incidents`) |
| tinyvault | `tvault` | Secret injection into a child process (`vault`) |
| glyphrun | `glyph` | Running behavioral specs (`run`) |
| codemap | `codemap` | Process→code correlation, surfaced as availability in `doctor` |
| vecgrep | `vecgrep` | Reported by `doctor` |
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
./bin/monitor doctor          # human-readable health table
./bin/monitor doctor --json   # machine-readable, for agents
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

Because the stash is content-addressed and tagged, you can search incidents
later by trigger, alert rule, or PID. Every monitor stash carries the
`monitor-incident` tag plus tags like `trigger:investigate`,
`snapshot:<hash12>`, `alert:<rule>`, and `pid:<n>`.

### `monitor stash` — capture a snapshot now

```bash
./bin/monitor stash --json
./bin/monitor stash --note "before risky deploy" --ttl 30d
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
./bin/monitor investigate 1234 --json
./bin/monitor investigate 1234 --no-save   # bundle locally, skip fcheap
```

`investigate` runs a snapshot → profile → stash pipeline against one process: it
captures a heap profile of the PID, takes a full system snapshot, records the
process name, and stashes the bundle. The output reports the `steps` taken and
the resulting stash. Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--ttl` | `7d` | Stash lifetime, passed through to `fcheap --ttl` |
| `--no-save` | `false` | Capture the bundle to a temp dir but skip the `fcheap save` step (useful in sandboxed environments — the profile is returned inline in the JSON) |
| `--json` | `false` | Emit JSON |

### `monitor incidents` — list what you've captured

```bash
./bin/monitor incidents
./bin/monitor incidents --json
```

This lists recent monitor stashes, with the `monitor-incident` tag pre-applied
so you only see Monitor's own captures, not every fcheap stash on the system.

### Stash-on-alert during `watch`

[`monitor watch`](./cli.md) can capture an incident automatically every time the
analyzer raises an alert:

```bash
./bin/monitor watch --json --stash
./bin/monitor watch --stash --stash-ttl 24h
```

With `--stash`, each alert fires a capture in the background and emits the
outcome as a separate NDJSON line (`{"type":"stash",...}`). `--stash-ttl`
(default `7d`) controls how long those automatic stashes live.

### Graceful degradation

If `fcheap` is not on `$PATH`, capture does **not** fail hard. Monitor still
writes the complete bundle to a temp directory and returns it with a note like
`fcheap not on PATH; bundle saved locally only`, leaving the directory in place
so you can recover the bytes. If `fcheap save` itself fails, the bundle is again
kept locally and the error surfaces in the `stash_error` field rather than
aborting the command.

## Secret injection with tinyvault

`monitor vault` wraps a command with **tinyvault** (`tvault run`) so secrets
from a named vault project land in the child process's environment — without the
agent that invoked Monitor ever seeing the secret values.

```bash
# Launch a service with its secrets injected
./bin/monitor vault --project myapp -- /usr/local/bin/myapp --port 8080

# Debug: see exactly which env vars would be injected
./bin/monitor vault --project myapp -- env
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
./bin/monitor run specs/version.yml
```

Monitor invokes `glyph run --format json <spec>`. When Monitor launches a child
process or spec it sets `MONITOR=1` in the child's environment, so the spec (or
any process it spawns) can detect that it is being observed.

## Code correlation with codemap

Monitor reports **codemap** availability through `monitor doctor`, and the
incident bundle is built to support code correlation: when you
`monitor investigate <pid>`, the stash records the process name and PID
alongside the snapshot and profile. That preserved identity is what lets you
hand an incident off to codemap to trace a misbehaving process back to the code
that owns it.

Monitor does not call codemap automatically — the correlation is a workflow you
drive yourself once `doctor` confirms codemap is installed. As with every other
integration, codemap being absent changes nothing about Monitor's core
behavior; `doctor` simply reports it as not on `$PATH`.

## Summary

- Run [`monitor doctor`](./cli.md) first to see what is installed.
- fcheap powers `stash`, `investigate`, `incidents`, and `watch --stash`; all
  of them keep a local bundle when fcheap is missing.
- tinyvault powers `vault` for leak-free secret injection.
- glyphrun powers `run` for behavioral specs.
- codemap availability is surfaced for process→code correlation workflows.

None of these are required to use Monitor — they are bonuses that light up when
the matching tool is on your `$PATH`.
