# CLI Reference

Monitor runs as an interactive TUI by default, but every view is also a
subcommand. Most subcommands support `--json` for machine-readable output, so
the CLI is the primary surface for scripts and agents.

```bash
./bin/monitor --help          # list every subcommand
./bin/monitor <cmd> --help    # flags for one subcommand
./bin/monitor --version       # prints: monitor <version>
```

The subcommands group into four purposes:

- **Inspect** — read current state: [`snapshot`](#snapshot),
  [`watch`](#watch), [`process`](#process).
- **Act** — change something: [`kill`](#kill).
- **Diagnose** — capture and analyze: [`profile`](#profile),
  [`investigate`](#investigate), [`stash`](#stash), [`incidents`](#incidents),
  [`logs`](#logs), [`history`](#history).
- **Ecosystem & runtime** — talk to sibling tools and the running TUI:
  [`doctor`](#doctor), [`run`](#run), [`reload`](#reload), [`mcp`](#mcp),
  [`vault`](#vault), [`v2`](#v2).

## Global flags

| Flag | Effect |
|------|--------|
| `--no-temperature-source` | Skip `powermetrics`; use the CPU-load estimation fallback (no sudo required). Persistent — available under every subcommand. |
| `--json` | Emit JSON (or NDJSON for [`watch`](#watch)) to stdout. Supported by every subcommand except [`run`](#run), [`reload`](#reload), [`mcp`](#mcp), [`vault`](#vault), and [`v2`](#v2). |
| `--version` | Print the version and exit. |

### Observed-child environment

When Monitor launches a child process or spec, it sets two environment
variables so the child can detect that it is being observed:

```
MONITOR=1
MONITOR_RUN_DIR=<dir>
```

A child that already sees `MONITOR_RUN_DIR` will not have `MONITOR` re-set, so
nested invocations don't clobber the outer run directory.

## Inspect

### `snapshot`

Print a single system snapshot — CPU, memory, network, and the process list.
Default output is human-readable; `--json` emits the full structure.

| Flag | Default | Effect |
|------|---------|--------|
| `--interval` | `1s` | Sampling interval. |
| `--json` | `false` | Emit JSON to stdout. |

```bash
./bin/monitor snapshot
./bin/monitor snapshot --json | jq '.cpu.usage_percent'
```

```json
{
  "cpu": {
    "usage_percent": 25.9,
    "per_core_usage": [52.1, 47.3, 41.9],
    "frequency_mhz": 4,
    "core_count": 10,
    "thread_count": 10,
    "load_avg_1": 5.51,
    "load_avg_5": 5.62,
    "load_avg_15": 5.07
  },
  "memory": {
    "total_bytes": 17179869184,
    "used_bytes": 11715772416,
    "usage_percent": 68.19
  }
}
```

### `watch`

Stream metrics as **NDJSON** (newline-delimited JSON) to stdout. Each line is a
self-describing object with a `type` discriminator (`tick`, `alert`, `stash`).
Pipe into `jq` or any other line-oriented tool.

| Flag | Default | Effect |
|------|---------|--------|
| `-i`, `--interval` | `1s` | Tick interval. |
| `--once` | `false` | Emit one event and exit. |
| `--stash` | `false` | On every alert, capture the incident to fcheap via `internal/incidents`. |
| `--stash-ttl` | `7d` | TTL for incident stashes (passed to fcheap `--ttl`). |
| `--json` | `false` | Emit NDJSON to stdout. |

Alerts come from the built-in analyzer (a CPU-spike rule and an RSS-growth
rule). With `--stash`, each alert captures an incident bundle in a background
goroutine and emits a separate `stash` line reporting the outcome; the watch
loop never blocks on fcheap I/O, and a stash failure shows up in a
`stash_error` field rather than stalling stdout.

```bash
./bin/monitor watch --json | jq -c 'select(.type=="tick")'
./bin/monitor watch --json --stash --stash-ttl 24h
```

```json
{"type":"tick","timestamp":"2026-06-27T00:02:38Z","cpu":{"usage_percent":25.9},"memory":{"usage_percent":68.1},"hostname":"host"}
{"type":"alert","timestamp":"2026-06-27T00:02:41Z","alert":{"rule":"cpu_spike","severity":"warning","pid":1133,"process":"node","detail":"3.1x baseline"}}
```

### `process`

Print detailed information for a single PID. Takes exactly one argument.

| Flag | Default | Effect |
|------|---------|--------|
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor process 1133
./bin/monitor process 1133 --json
```

```json
{
  "pid": 1133,
  "name": "mediaanalysisd",
  "cpu_percent": 150.4,
  "memory": 339427328,
  "memory_percent": 0,
  "threads": 24,
  "user": "abdulachik",
  "is_system": false,
  "is_protected": false
}
```

## Act

### `kill`

Safely terminate one or more processes. Accepts one or more PIDs. Termination
is gated by the same safety check used by the TUI and the MCP server:
**protected** processes (`launchd`, `kernel_task`, `WindowServer`, `Finder`,
`Dock`, …) and **system** (root-owned) processes are refused unless you pass
`--yes`.

| Flag | Default | Effect |
|------|---------|--------|
| `--force` | `false` | Send `SIGKILL` instead of `SIGTERM`. |
| `--yes` | `false` | Skip the protection checks (required to kill protected/system PIDs). |
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor kill 1234                 # SIGTERM, safety-checked
./bin/monitor kill 1234 5678 --force    # SIGKILL both
./bin/monitor kill 1 --yes --json       # override protection (use with care)
```

When a kill is refused, the command exits non-zero. With `--json` it returns a
structured refusal instead of acting:

```json
{
  "killed": false,
  "protected": true,
  "safety_warnings": ["pid 1 (launchd) is protected"],
  "note": "protected or system process; pass --yes to override"
}
```

A successful kill reports per-PID results:

```json
{
  "killed": true,
  "results": [
    {"pid": 1234, "killed": true},
    {"pid": 5678, "killed": false, "error": "process not found"}
  ]
}
```

## Diagnose

### `profile`

Capture a process profile. `heap`, `cpu`, and `goroutine` are scraped from the
target's `net/http/pprof` server; `sample` uses macOS `sample`. Heap and
goroutine profiles are symbolicated; CPU profiles are returned as raw protobuf.

| Flag | Default | Effect |
|------|---------|--------|
| `-t`, `--type` | `heap` | Profile type: `heap`, `cpu`, `goroutine`, `sample`. |
| `--pprof-addr` | `localhost:6060` | `host:port` of the target's pprof server (heap/cpu/goroutine only). |
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor profile 1234 --type heap --json
./bin/monitor profile 1234 -t goroutine --pprof-addr localhost:7070 --json
```

```json
{
  "pid": 1234,
  "type": "heap",
  "taken": "2026-06-27T00:02:38Z",
  "symbols": [
    {"func": "main.handler", "file": "main.go", "line": 42}
  ],
  "path": "/tmp/monitor-profile-1234-heap.pb.gz"
}
```

### `investigate`

Run the diagnostic pipeline for a process: capture the current system snapshot
plus a heap profile, bundle them into a content-addressed fcheap stash, and
print the stash ID so the bundle can be searched or restored later.

| Flag | Default | Effect |
|------|---------|--------|
| `--ttl` | `7d` | TTL for the stash (fcheap `--ttl`). |
| `--no-save` | `false` | Capture the bundle but skip the fcheap stash step (useful in sandboxes). |
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor investigate 1234 --json
./bin/monitor investigate 1234 --no-save --json   # profile in JSON, nothing stashed
```

```json
{
  "pid": 1234,
  "started_at": "2026-06-27T00:02:38Z",
  "steps": ["snapshot", "profile", "stash"],
  "stash": {"id": "fcheap-abc123", "path": "/tmp/monitor-incident-..."},
  "note": "investigation pipeline (capture + stash)"
}
```

### `stash`

Manually capture the current system snapshot into a content-addressed fcheap
stash and return the stash ID. Useful for "before" states ahead of risky
operations, or manual incident triage. The trigger tag is `manual`.

| Flag | Default | Effect |
|------|---------|--------|
| `--note` | `""` | Free-form note for downstream search. |
| `--ttl` | `7d` | TTL for the stash (fcheap `--ttl`). |
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor stash --note "before deploy" --json
```

```json
{
  "started_at": "2026-06-27T00:02:38Z",
  "stash": {"id": "fcheap-def456", "path": "/tmp/monitor-incident-..."}
}
```

### `incidents`

List recent monitor incident stashes (the bundles produced by [`watch
--stash`](#watch), [`investigate`](#investigate), and [`stash`](#stash)). Wraps
`fcheap list` with the monitor-incident tag pre-applied.

| Flag | Default | Effect |
|------|---------|--------|
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor incidents
./bin/monitor incidents --json
```

```json
[
  {"id": "fcheap-def456", "name": "manual snapshot", "created_at": "2026-06-27T00:02:38Z"}
]
```

### `logs`

Manage captured process logs in the local veclite store (at
`$TMPDIR/monitor-logs.veclite`). Two subcommands: `capture` and `search`.

#### `logs capture`

Ingest log lines into the store. Two modes:

1. **Wrap a new command** — everything after `--` is run via `sh -c`, and
   stdout+stderr are captured until the process exits.
2. **Tail a running process** — `--pid N` shells out to `lsof` to find the
   process's open log files (`.log`, `.out`, paths under `/var/log`, …) and
   tails each from EOF until `SIGINT`.

Lines are auto-tagged by level: `INFO:` / `[INFO]` / `WARN:` / `WARNING:` /
`ERROR:` / `FATAL:` / `DEBUG:` / `TRACE:` prefixes are detected; everything
else defaults to `info` (or `error` for stderr lines without a level).

| Flag | Default | Effect |
|------|---------|--------|
| `--pid` | `0` | Tail open log files for the running process (uses `lsof`). |
| `--max-lines` | `0` | Stop after N lines (0 = unlimited). |
| `--max-bytes` | `0` | Stop after N bytes (0 = unlimited). |
| `--name` | `""` | Override the captured process name in the store. |
| `--process-name` | `""` | Alias for `--name`. |
| `--level` | `""` | Default level for untagged lines; empty = `info` (or `error` for stderr). |
| `--json` | `false` | Emit JSON output (final result). |

```bash
./bin/monitor logs capture -- sh -c 'echo INFO: hello; echo WARN: bad >&2'
./bin/monitor logs capture --pid 1234 --max-lines 500
```

```json
{
  "name": "sh",
  "lines": 2,
  "bytes": 21,
  "duration": "3.2ms",
  "error": ""
}
```

#### `logs search`

Keyword-search the captured log store.

| Flag | Default | Effect |
|------|---------|--------|
| `--limit` | `50` | Max results. |
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor logs search "error" --json
./bin/monitor logs search "timeout" --limit 200
```

```json
[
  {"timestamp": "2026-06-27T00:02:38Z", "pid": 1234, "process": "sh", "message": "WARN: bad", "level": "warn"}
]
```

### `history`

Record and query persistent metric history. While [`watch`](#watch) streams
live ticks, `history` persists a small set of scalar series to a local veclite
store so you can ask "what was CPU doing an hour ago?" after the fact. Three
subcommands: `record`, `query`, and `list`. The store defaults to
`~/.local/share/monitor/history.veclite` (the directory is created on first
use); override it with `--db` on any subcommand.

Recorded metrics: `cpu.usage`, `mem.usage`, `mem.pressure`, `net.recv_bps`,
`net.sent_bps`, `disk.read_bps`, `disk.write_bps`, and `load.1`.

#### `history record`

Sample the system on an interval and append each tick to the store. Runs until
`SIGINT` (Ctrl-C), then prints the number of ticks recorded to stderr.

| Flag | Default | Effect |
|------|---------|--------|
| `-i`, `--interval` | `1s` | Sampling interval. |
| `--db` | `~/.local/share/monitor/history.veclite` | History store path. |

```bash
./bin/monitor history record                       # sample every second
./bin/monitor history record -i 5s --db /tmp/h.veclite
```

#### `history query`

Query a recorded metric over a look-back window. Takes exactly one argument —
the metric name — and reports the matching samples plus summary stats
(`min`/`avg`/`p95`/`max`, `first`/`last`, and `trend` = last − first).

| Flag | Default | Effect |
|------|---------|--------|
| `--since` | `1h` | Look back this far. |
| `--db` | `~/.local/share/monitor/history.veclite` | History store path. |
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor history query cpu.usage --since 30m
./bin/monitor history query mem.usage --since 6h --json | jq '.summary.p95'
```

```json
{
  "metric": "cpu.usage",
  "since": "1h0m0s",
  "summary": {
    "count": 3600,
    "min": 4.21,
    "max": 92.7,
    "avg": 27.43,
    "p95": 71.05,
    "first": 25.9,
    "last": 31.2,
    "trend": 5.3,
    "from": "2026-06-26T23:02:38Z",
    "to": "2026-06-27T00:02:38Z"
  },
  "points": [
    {"t": "2026-06-26T23:02:38Z", "v": 25.9},
    {"t": "2026-06-26T23:02:39Z", "v": 26.4}
  ]
}
```

#### `history list`

List the metrics that have been recorded into the store.

| Flag | Default | Effect |
|------|---------|--------|
| `--db` | `~/.local/share/monitor/history.veclite` | History store path. |
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor history list
./bin/monitor history list --json
```

```json
["cpu.usage", "disk.read_bps", "disk.write_bps", "load.1", "mem.pressure", "mem.usage", "net.recv_bps", "net.sent_bps"]
```

## Ecosystem & runtime

### `doctor`

Print the availability and version of every sibling ecosystem tool. With
`--json`, returns a structured status per tool (`available`, `version`,
`path`).

| Flag | Default | Effect |
|------|---------|--------|
| `--json` | `false` | Emit JSON output. |

Probed tools: `codemap`, `fcheap`, `vecgrep`, `tinyvault`, `vidtrace`,
`glyphrun`, `cairntrace`, `veclite`, and `tmux`.

```bash
./bin/monitor doctor
./bin/monitor doctor --json | jq '.fcheap.available'
```

```json
{
  "codemap": {
    "available": true,
    "version": "codemap version 0.17.0 (934813e) 2026-06-26T21:43:42Z",
    "path": "/opt/homebrew/bin/codemap"
  },
  "fcheap": {
    "available": true,
    "version": "fcheap version 0.26.2 ...",
    "path": "/opt/homebrew/bin/fcheap"
  }
}
```

### `run`

Run a glyphrun behavioral spec against monitored services. Takes exactly one
argument — the path to a spec — and prints glyphrun's output. The spawned spec
sees `MONITOR=1` and `MONITOR_RUN_DIR`.

```bash
./bin/monitor run specs/version.yml
```

### `reload`

POST to the `/reload` endpoint exposed by a running monitor TUI binary (the TUI
embeds `internal/reload` on `127.0.0.1:7351`). Useful after [`logs
capture`](#logs) or any workflow that changes data the TUI caches. The endpoint
is loopback-only; remote processes cannot reach it. If no TUI is running,
`reload` exits non-zero with a clear error.

| Flag | Default | Effect |
|------|---------|--------|
| `--addr` | `127.0.0.1:7351` | Address of the running TUI's `/reload` endpoint. |
| `--timeout` | `3s` | HTTP client timeout for the POST. |

```bash
./bin/monitor reload
./bin/monitor reload --addr 127.0.0.1:7351 --timeout 5s
```

### `mcp`

Run an MCP stdio server exposing Monitor's data. The single subcommand,
`mcp serve`, speaks MCP over stdio (newline-delimited JSON-RPC).

```bash
./bin/monitor mcp serve
```

The server exposes seven tools — three read-only (`monitor_snapshot`,
`monitor_processes`, `monitor_doctor`) and four mutating (`monitor_kill`,
`monitor_profile_capture`, `monitor_investigate`, `monitor_record`). Every
mutating tool requires `confirm: true` in its typed input. See the
[MCP Server](/guide/mcp) guide for the full tool surface and confirmation
model.

### `vault`

Run a command with secrets injected via tinyvault. Wraps the command with
`tvault run --project <name>` so secrets from the named project land in the
child process's environment **without** appearing in the agent's context.
Everything after `--` is the command and its arguments. Requires the `tvault`
binary on `$PATH` and a configured tinyvault project.

| Flag | Default | Effect |
|------|---------|--------|
| `--project` | `""` | Tinyvault project name (**required**). |

```bash
./bin/monitor vault --project myapp -- /usr/local/bin/myapp --port 8080
./bin/monitor vault --project myapp -- env   # debug: show injected env
```

### `v2`

Launch the Bubble Tea v2 TUI. This is the default when you run bare `monitor`;
the `v2` subcommand is kept as an explicit alias. All eight tabs are rendered
with full interactivity.

```bash
./bin/monitor            # same thing
./bin/monitor v2
```

See [The TUI](/guide/tui) for the tab and keyboard reference.
