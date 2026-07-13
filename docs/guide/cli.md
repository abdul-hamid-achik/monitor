# CLI Reference

Monitor offers an interactive TUI (`monitor studio`), but every view is also a
subcommand. Running bare `monitor` prints help. Most subcommands support
`--json` for machine-readable output, so the CLI is the primary surface for
scripts and agents.

```bash
./bin/monitor --help          # list every subcommand
./bin/monitor <cmd> --help    # flags for one subcommand
./bin/monitor --version       # prints: monitor <version>
```

The subcommands group into four purposes:

- **Inspect** — read current state: [`snapshot`](#snapshot),
  [`watch`](#watch), [`process`](#process), [`processes`](#processes),
  [`tree`](#tree).
- **Act** — change something: [`kill`](#kill).
- **Diagnose** — capture and analyze: [`analyze`](#analyze), [`profile`](#profile),
  [`investigate`](#investigate), [`stash`](#stash), [`incidents`](#incidents),
  [`logs`](#logs), [`history`](#history), [`baseline`](#baseline),
  [`diff`](#diff).
- **Ecosystem & runtime** — talk to sibling tools and the running TUI:
  [`config`](#config), [`doctor`](#doctor), [`run`](#run), [`reload`](#reload),
  [`mcp`](#mcp), [`vault`](#vault), [`studio`](#studio).

## Global flags

| Flag | Effect |
|------|--------|
| `--no-temperature-source` | Skip `powermetrics`; use the CPU-load estimation fallback (no sudo required). Persistent — available under every subcommand. |
| `--json` | Emit JSON (or NDJSON for [`watch`](#watch)) to stdout. Supported by every subcommand except [`run`](#run), [`reload`](#reload), [`mcp`](#mcp), [`vault`](#vault), and [`studio`](#studio). |
| `--pprof <addr>` | Expose Monitor's **own** `net/http/pprof` on `addr` (e.g. `localhost:6060`). Off by default. |
| `--version` | Print the version and exit. |

### Profiling Monitor itself

`--pprof <addr>` starts an HTTP server serving Monitor's own
`/debug/pprof/` endpoints on the given address, then continues with whatever
subcommand or mode you asked for. It is most useful with the long-running modes
— the TUI, [`watch`](#watch), [`history record`](#history-record), and
[`mcp serve`](#mcp) — where there's a live process to profile. Monitor logs the
listen address to stderr:

```bash
./bin/monitor --pprof localhost:6060 watch --json
# monitor: pprof listening on http://localhost:6060/debug/pprof/
```

Because the endpoint is a standard `net/http/pprof` server, you can point
Monitor's own profiler at it to capture Monitor profiling Monitor:

```bash
./bin/monitor profile <monitor-pid> --type heap --pprof-addr localhost:6060 --json
```

### Observed-child environment

When Monitor launches a child process or spec, it sets `MONITOR=1` in the
child's environment so the child can detect that it is being observed:

```
MONITOR=1
```

When a richer parent (e.g. glyphrun) already exports `MONITOR_RUN_DIR`, Monitor
leaves `MONITOR` alone, so nested invocations don't clobber the outer run.

## Inspect

### `snapshot`

Print a single system snapshot — CPU, memory, network, and the process list.
Default output is human-readable; `--json` emits the full lossless structure.
`--compact` emits JSON using the stable `monitor.compact_snapshot` schema. It
omits histories and per-core arrays, caps both top-process lists and the
filesystem list, and is the recommended form for agent context windows.

| Flag | Default | Effect |
|------|---------|--------|
| `--interval` | `1s` | Warm-up delay between counter samples; `0` skips warm-up and leaves first-sample rates unavailable. |
| `--json` | `false` | Emit JSON to stdout. |
| `--compact` | `false` | Emit bounded, schema-versioned JSON instead of human/full output. |
| `--process-limit` | `5` | Entries in each of `top_cpu` and `top_memory`; clamped to 25. |
| `--process-filter` | `""` | Case-insensitive process-name substring for the compact view. |
| `--filesystem-limit` | `10` | Filesystems in the compact view; clamped to 50. |
| `--filesystem-filter` | `""` | Case-insensitive device, mount-point, or filesystem substring. |

```bash
./bin/monitor snapshot
./bin/monitor snapshot --json | jq '.cpu.usage_percent'
./bin/monitor snapshot --compact --process-limit 3 --process-filter ollama
./bin/monitor snapshot --compact --filesystem-filter apfs | jq '.filesystems'
```

The compact payload starts with `schema_version: 1` and
`kind: "monitor.compact_snapshot"`. `processes.system_total`,
`processes.matched`, `processes.truncated`, `filesystem_system_total`,
`filesystem_total`, `filesystem_limit`, and `filesystems_truncated` make every
omission explicit. Empty lists serialize as `[]`, never `null`. The existing
`--json` shape is unchanged.

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
| `--webhook` | `""` | POST each alert as JSON to this URL. |
| `--notify` | `false` | Show a desktop notification on each alert (`osascript` / `notify-send`). |
| `--alert-cooldown` | `1m` | Suppress repeats of the same active finding across output and sinks; `0` emits every sample. |
| `--delivery-limit` | `8` | Maximum concurrent stash, webhook, and notification deliveries; excess work is dropped with a stderr warning. |
| `--json` | `false` | Emit NDJSON to stdout. |

Alerts come from the built-in analyzer. Five rules always run, and watch feeds
the analyzer the full event — including per-partition `Disk` and the
`Processes` list — so the per-process rules fire too:

- `cpu_spike` — current process CPU exceeds 3× its rolling median baseline and an absolute 50% floor.
- `rss_growth` — process RSS keeps climbing faster than 50 KB/s (wall-clock-normalized regression with R² confidence).
- `disk_fill` — any mounted partition's usage is `>= 90%`.
- `swap_pressure` — swap used is `>= 50%` of swap total (real memory pressure).
- `zombie_process` — the OS reports a process in zombie (`Z`) state.

In addition, the `cpu_alert_threshold` and `memory_alert_threshold` settings in
[`config.json`](/reference/configuration) get teeth here: when either is set
above `0`, a threshold rule fires `cpu_threshold` / `mem_threshold` alerts
whenever overall CPU or memory usage crosses the configured percentage.

With `--stash`, each alert captures an incident bundle in a background
goroutine and emits a separate `stash` line reporting the outcome; the watch
loop never blocks on fcheap I/O, and a stash failure shows up in a
`stash_error` field rather than stalling stdout.

`--webhook` and `--notify` are additional alert sinks — they fire alongside
(not instead of) the NDJSON stream and `--stash`. Deliveries run asynchronously
under the bounded `--delivery-limit`, so the watch loop never blocks or creates
unbounded goroutines when a sink is slow. `--webhook`
POSTs the raw `collector.Alert` JSON (the same object embedded in an `alert`
line) to the given URL; a non-2xx response is logged to stderr. `--notify`
shells out to `osascript` on macOS or `notify-send` on Linux; delivery failures
are logged to stderr and never stall the stream.

Repeated alerts are gated by a stable identity (rule plus PID, filesystem, or
system scope). The default one-minute cooldown prevents a full disk or sustained
threshold from creating a stash, webhook, and notification every second. The
first finding emits immediately and can emit again after the cooldown expires.

When the analyzer attaches a diagnosis to a per-process alert (see
`monitor_analyze` in the [MCP reference](/guide/mcp)), it rides along on every
sink: the webhook payload gains a `diagnosis` object, the desktop notification
body swaps the terse rule detail for the diagnosis summary and appends the
confidence level, and the fcheap bundle's `manifest.json` carries it too
(tagged `confidence:<level>` for search). All additive — consumers that
ignore the new key see no change.

```bash
./bin/monitor watch --json | jq -c 'select(.type=="tick")'
./bin/monitor watch --json --stash --stash-ttl 24h
./bin/monitor watch --json --webhook https://hooks.example.com/alerts --notify
```

```json
{"type":"tick","timestamp":"2026-06-27T00:02:38Z","cpu":{"usage_percent":25.9},"memory":{"usage_percent":68.1},"hostname":"host"}
{"type":"alert","timestamp":"2026-06-27T00:02:41Z","alert":{"rule":"cpu_spike","severity":"warning","pid":1133,"process":"node","detail":"cpu spike","diagnosis":{"summary":"node pinned a core for 45s while RSS stayed flat — consistent with a hot loop","evidence":["cpu 150% for 45s","rss flat"],"confidence":"medium","next_actions":["monitor profile 1133 --type cpu","monitor investigate 1133"]}}}
{"type":"alert","timestamp":"2026-06-27T00:02:44Z","alert":{"rule":"cpu_threshold","severity":"warning","detail":"CPU 91% >= threshold 90%"}}
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
  "status": "S",
  "parent": 1,
  "metric_states": {
    "status": {"state": "observed"},
    "io": {"state": "unsupported", "reason": "per-process I/O counters are not exposed by gopsutil on macOS"}
  },
  "is_system": false,
  "is_protected": false
}
```

### `processes`

List a bounded process inventory without requesting the full system snapshot.
`ps` is an alias. By default it uses `max_processes` and
`show_system_processes` from Monitor's effective configuration; flags override
those defaults. Human rows include OS status; JSON entries additionally expose
parent, memory share, I/O counters when supported, and per-field
`metric_states` when a value is unavailable.

| Flag | Default | Effect |
|------|---------|--------|
| `--sort` | `cpu` | Sort by `cpu`, `memory`, `pid`, or `name`. |
| `-f`, `--filter` | `""` | Case-insensitive substring across name, PID, user, and status. |
| `-n`, `--limit` | config value | Maximum rows, capped at 1000. |
| `--system`, `--all` | config value | Include system-owned processes. |
| `--json` | `false` | Emit a bounded JSON envelope with match/truncation metadata. |

```bash
./bin/monitor processes --sort memory --limit 10
./bin/monitor ps --filter node --json
```

### `tree`

Print the process forest by parent/child relationship. With no argument, every
top-level process is shown (one whose parent isn't in the captured set, e.g.
reparented to init), each followed by its descendants indented underneath. Pass
a PID to print just that process's subtree. Children are sorted by PID for
determinism.

| Flag | Default | Effect |
|------|---------|--------|
| `--json` | `false` | Emit nested JSON output. |

```bash
./bin/monitor tree              # the whole forest
./bin/monitor tree 1234         # the subtree rooted at pid 1234
./bin/monitor tree --json | jq '.[0].children'
```

The text output indents each child under its parent and annotates CPU and
memory:

```
launchd (pid 1)  cpu 0.0%  mem 12.1 MB
  loginwindow (pid 512)  cpu 0.1%  mem 48.0 MB
  node (pid 1133)  cpu 3.1%  mem 256.0 MB
    esbuild (pid 1190)  cpu 0.0%  mem 32.0 MB
```

With `--json`, each node is the full process object with its children nested
under a `children` array (omitted when a process has none):

```json
[
  {
    "pid": 1,
    "name": "launchd",
    "cpu_percent": 0.0,
    "memory": 12685312,
    "children": [
      {"pid": 1133, "name": "node", "cpu_percent": 3.1, "memory": 268435456,
       "children": [
         {"pid": 1190, "name": "esbuild", "cpu_percent": 0.0, "memory": 33554432}
       ]}
    ]
  }
]
```

## Act

### `kill`

Safely terminate one or more processes. Accepts one or more PIDs. Termination
is gated by the same safety check used by the TUI and the MCP server:
**protected** processes (`launchd`, `kernel_task`, `WindowServer`, `Finder`,
`Dock`, …) and **system** (root-owned) processes are always refused. The
compatibility `--yes` flag acknowledges intent but cannot bypass this invariant.

The kill is **verified**, not just dispatched: after sending the signal,
`kill` polls for up to ~2 seconds to observe whether the process actually
exited, and reports one of three outcomes — `terminated`, `still_running`, or
`unknown` (state couldn't be checked) — alongside `waited_ms`. A process that
survives `SIGTERM` is reported as `still_running` with a `next_action`
suggesting `--force`; `kill` never escalates to `SIGKILL` on its own.

| Flag | Default | Effect |
|------|---------|--------|
| `--force` | `false` | Send `SIGKILL` instead of `SIGTERM`. |
| `--yes` | `false` | Acknowledge intent; never overrides protected/system safety. |
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor kill 1234                 # SIGTERM, safety-checked
./bin/monitor kill 1234 5678 --force    # SIGKILL both
./bin/monitor kill 1 --yes --json       # still returns a structured refusal
```

When a human-output kill is refused, the command exits non-zero. With `--json`
it returns a structured refusal instead of acting:

```json
{
  "killed": false,
  "refused": true,
  "reason": "protected or system processes cannot be terminated by monitor",
  "protected": true,
  "safety_warnings": ["launchd (pid 1) is a protected system process"]
}
```

A successful kill reports per-PID results. `killed` at both the top level and
per-PID reflects the **verified** outcome (`terminated`), not merely that a
signal was sent:

```json
{
  "killed": true,
  "results": [
    {"pid": 1234, "killed": true, "outcome": "terminated", "signal": "SIGTERM", "waited_ms": 84},
    {"pid": 5678, "killed": false, "error": "process not found"}
  ]
}
```

A process that ignores `SIGTERM` reports `still_running` with a suggested
next action instead of a false `killed:true`:

```json
{
  "killed": false,
  "results": [
    {"pid": 9012, "killed": false, "outcome": "still_running", "signal": "SIGTERM", "waited_ms": 2003,
     "next_action": "process ignored SIGTERM; if termination is required, retry with force (CLI: --force, MCP: force:true) to send SIGKILL"}
  ]
}
```

## Diagnose

### `analyze`

Sample a bounded window and run the same cross-signal process diagnosis engine
as the read-only `monitor_analyze` MCP tool. Without `--pid`, Monitor considers
every process present in the final sample; a PID focuses the report. A healthy
JSON result always includes `"diagnoses": []` rather than `null`.

| Flag | Default | Effect |
|------|---------|--------|
| `--window` | `10s` | Total sampling window; greater than zero and at most 60 seconds. |
| `-i`, `--interval` | `1s` | Delay between samples; must not exceed the window. |
| `--pid` | `0` | Focus on one positive PID (`0` means all current processes). |
| `--json` | `false` | Emit `{window, interval, pid?, samples, healthy, diagnoses}`. |

```bash
./bin/monitor analyze --window 10s
./bin/monitor analyze --pid 1234 --window 15s --json
```

### `profile`

Capture a process profile. `heap`, `cpu`, and `goroutine` are scraped from the
target's `net/http/pprof` server; `sample` uses macOS `sample`. Heap and
goroutine profiles are symbolicated; CPU profiles are returned as raw protobuf.

Before scraping the default pprof address, `profile` checks that the LISTEN
socket at `--pprof-addr` actually belongs to the target pid (via connection
enumeration) and refuses otherwise — an unrelated process could be listening
on `localhost:6060`, and monitor won't silently profile the wrong thing.
Passing `--pprof-addr` explicitly asserts you know the endpoint is correct
and skips the check; `-t sample` needs no pprof endpoint at all. A capture
that produces no usable data (empty file, or no text/symbols) is also
refused rather than reported as a hollow success.

| Flag | Default | Effect |
|------|---------|--------|
| `-t`, `--type` | `heap` | Profile type: `heap`, `cpu`, `goroutine`, `sample`. |
| `--pprof-addr` | `localhost:6060` | `host:port` of the target's pprof server (heap/cpu/goroutine only). Passing this flag explicitly asserts the endpoint belongs to the target pid and skips the ownership check. |
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

Run the diagnostic pipeline for a process: snapshot -> profile -> correlate ->
stash, bundling the result into a content-addressed fcheap stash and printing
the stash ID so the bundle can be searched or restored later.

The profile step is ownership-gated the same way `profile` is: it only trusts
a `localhost:6060` pprof heap scrape when the LISTEN socket is proven to
belong to the target pid, falling back to macOS `sample` otherwise. Each
pipeline stage reports a typed result — `{step, status, limitation, recovery}`
with `status` one of `ok`/`failed`/`skipped` — and the top-level `verdict` is
`"complete"` only when every step succeeded, `"partial"` otherwise. An empty
or unverified profile is never stashed.

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
  "steps": [
    {"step": "snapshot", "status": "ok"},
    {"step": "profile", "status": "ok"},
    {"step": "correlate", "status": "ok"},
    {"step": "stash", "status": "ok"}
  ],
  "verdict": "complete",
  "profile_method": "pprof_heap",
  "stash": {"stash_id": "fcheap-abc123", "path": "/tmp/monitor-incident-..."},
  "note": "investigation pipeline complete (profile and stash verified)"
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
  "stash": {"stash_id": "fcheap-def456", "path": "/tmp/monitor-incident-..."}
}
```

### `incidents`

List recent monitor incident stashes (the bundles produced by [`watch
--stash`](#watch), [`investigate`](#investigate), and [`stash`](#stash)). Wraps
`fcheap list` with the monitor-incident tag pre-applied. `incident` (singular)
is an alias for the whole command tree.

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

When fcheap archival fails (fcheap missing, disk full, ...), the bundle isn't
lost: it's persisted into a durable local registry under
`$XDG_STATE_HOME/monitor/incidents` (falling back to
`~/.local/state/monitor/incidents`) instead of the ephemeral temp dir the
capture started in, and the failed `stash`/`investigate` result carries a
`registry_id` you can hand to `resume-stash`.

#### `incidents pending`

List bundles retained in the local registry — the ones still waiting to be
archived.

```bash
./bin/monitor incidents pending --json
```

#### `incidents resume-stash`

Re-attempt `fcheap save` for a bundle that failed to archive. Accepts a
registry ID from `incidents pending`, a registry entry directory, or a path to
a bare retained bundle directory. On success the local copy is removed; on
failure the bundle is kept and the attempt (and error) is recorded for the
next try.

```bash
./bin/monitor incidents resume-stash abc123def456
./bin/monitor incident resume-stash abc123def456 --json
```

### `logs`

Manage captured process logs in the durable local veclite store at
`~/.local/share/monitor/logs.veclite`. `--store` overrides the location for
either subcommand, and `MONITOR_LOG_STORE` provides a shared environment-wide
override (explicit flag wins).

#### `logs capture`

Ingest log lines into the store. Two modes:

1. **Wrap a new command** — everything after `--` is passed directly to the
   executable as exact argv. Monitor does not join arguments or invoke an
   intermediate shell. Use an explicit `sh -c '…'` when shell syntax is
   intentional. stdout+stderr are captured until the process exits.
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
| `--store` | `~/.local/share/monitor/logs.veclite` | Override the shared capture/search database. |
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
  "store": "/Users/me/.local/share/monitor/logs.veclite",
  "error": ""
}
```

#### `logs search`

Keyword-search the captured log store. The query is optional when filters are
enough on their own; results are always newest-first. Exports can preserve the
full structured entries (`json`/`ndjson`) or replay captured lines (`raw`).

| Flag | Default | Effect |
|------|---------|--------|
| `--limit` | `50` | Max results. |
| `--level` | none | Filter one or more levels; repeat or comma-separate. |
| `--process` | `""` | Filter by a case-insensitive process-name substring. |
| `--pid` | `0` | Filter by process ID. |
| `--since` | `0` | Include only entries this recent, such as `15m` or `2h`. |
| `--format` | `text` | Output `text`, `json`, `ndjson`, or `raw`. |
| `--output`, `-o` | stdout | Write an export to a file (`-` means stdout). |
| `--store` | `~/.local/share/monitor/logs.veclite` | Override the shared capture/search database. |
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor logs search "error" --json
./bin/monitor logs search "timeout" --level error,warn --process api --since 2h
./bin/monitor logs search --since 15m --format ndjson -o recent.ndjson
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

### `baseline`

Capture and manage labeled system baselines. A baseline is an inspectable JSON
snapshot — overall CPU / memory / load1, every process keyed by PID (name,
memory, CPU percent), and the TCP listening sockets — saved to
`~/.local/share/monitor/baselines/<name>.json`. Pair baselines with
[`diff`](#diff) to answer "what changed?" across a deploy, a restart, or an
incident window. Three subcommands: `save`, `list`, and `delete`.

#### `baseline save`

Capture the current system as a named baseline. Takes exactly one argument —
the baseline name. Listeners are gathered best-effort; some sockets are
invisible without elevated privileges.

| Flag | Default | Effect |
|------|---------|--------|
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor baseline save pre-deploy
./bin/monitor baseline save pre-deploy --json
```

```json
{"saved": "pre-deploy", "processes": 412, "listeners": 37}
```

#### `baseline list`

List the names of saved baselines (sorted).

| Flag | Default | Effect |
|------|---------|--------|
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor baseline list
./bin/monitor baseline list --json
```

```json
["post-deploy", "pre-deploy"]
```

#### `baseline delete`

Delete a saved baseline. Takes exactly one argument — the baseline name.

```bash
./bin/monitor baseline delete pre-deploy
```

### `diff`

Compare a saved [`baseline`](#baseline) against the live system, or against a
second baseline. Takes one required argument (the "from" baseline) and an
optional second argument (the "to" baseline); with one argument, the live
system is captured as the "to" side. Reports new / gone / changed processes,
new / gone listening ports, and the shift in CPU / memory / load1.

A process present in both sides is reported as **changed** only when its memory
moved by at least `--mem-threshold` (in KB). Changed processes are sorted by the
largest absolute memory movement first.

| Flag | Default | Effect |
|------|---------|--------|
| `--mem-threshold` | `1024` | Minimum per-process memory change to report, in KB. |
| `--json` | `false` | Emit JSON output. |

```bash
./bin/monitor diff pre-deploy                 # pre-deploy -> live
./bin/monitor diff pre-deploy post-deploy     # pre-deploy -> post-deploy
./bin/monitor diff pre-deploy --mem-threshold 8192 --json
```

The `--json` output is the `Diff` struct: `cpu_delta` / `mem_delta` are
percentage-point shifts, `load1_delta` is the load-average shift, and each
process change carries `old_mem` / `new_mem` / `mem_delta` in bytes.

Deltas that clear both an absolute floor and a relative-change threshold
(so a 10MB process doubling doesn't scream, but a 1GB process doubling does)
get a **verdict**: a human- and agent-readable interpretation with a summary,
supporting evidence, a confidence level (`medium`/`high` — a two-sample diff
never earns `low`, since below-threshold means no verdict at all), and
suggested next actions. Verdicts cover total RSS, memory, swap, CPU, load1,
disk, process count, and the biggest individual process movers (capped at 3).
Human output prints a `verdicts:` section after the process/listener changes;
JSON output carries them under `verdicts` (omitted entirely when nothing was
significant). Baselines saved before this feature carry no swap/disk data, so
diffs against them silently skip swap and disk verdicts rather than reporting
a false spike.

```json
{
  "from": "pre-deploy",
  "to": "live",
  "cpu_delta": 12.4,
  "mem_delta": 3.1,
  "load1_delta": 0.87,
  "new_procs": [
    {"pid": 4821, "name": "myapp", "new_mem": 58720256}
  ],
  "gone_procs": [
    {"pid": 4099, "name": "myapp", "old_mem": 47185920}
  ],
  "changed_procs": [
    {"pid": 1133, "name": "node", "old_mem": 268435456, "new_mem": 402653184, "mem_delta": 134217728}
  ],
  "new_listeners": [
    {"proto": "tcp", "port": 8080, "pid": 4821, "process": "myapp"}
  ],
  "gone_listeners": [
    {"proto": "tcp", "port": 8079, "pid": 4099, "process": "myapp"}
  ]
}
```

When a delta clears its threshold, each `verdicts` entry has the same shape
as an alert's `diagnosis`, plus the `metric` it interprets (and `pid` for a
per-process `proc_rss` verdict):

```json
{
  "verdicts": [
    {
      "metric": "proc_rss",
      "pid": 1133,
      "summary": "node (pid 1133) RSS +50% vs baseline (was 256.0 MB, now 384.0 MB) — investigate",
      "evidence": ["was 256.0 MB, now 384.0 MB (Δ +128.0 MB)", "significant: |Δ| >= 256.0 MB and >= 50% of this process's baseline RSS"],
      "confidence": "medium",
      "next_actions": ["monitor profile 1133 --type heap", "monitor investigate 1133"]
    }
  ]
}
```

## Ecosystem & runtime

### `config`

Inspect and update the settings shared by Studio and process inventory
commands. Writes are validated and atomic; setting keys accept hyphens or
underscores.

```bash
./bin/monitor config show --json
./bin/monitor config get update-interval
./bin/monitor config set update-interval 500ms
./bin/monitor config set cpu-alert-threshold 85
./bin/monitor config path
./bin/monitor config reset --yes
```

See [Configuration](/reference/configuration) for fields and validation rules.

### `doctor`

Print the availability and version of every sibling ecosystem tool. With
`--json`, returns a structured status per tool (`available`, `version`,
`path`).

| Flag | Default | Effect |
|------|---------|--------|
| `--json` | `false` | Emit JSON output. |
| `--require` | none | Require named integrations; repeat or comma-separate values. |
| `--strict` | `false` | Require every known integration. |

Probed tools: `codemap`, `fcheap`, `vecgrep`, `tinyvault`, `vidtrace`,
`glyphrun`, `cairntrace`, `veclite`, and `tmux`.

```bash
./bin/monitor doctor
./bin/monitor doctor --json | jq '.fcheap.available'
./bin/monitor doctor --require fcheap,codemap
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

The server exposes eight tools — four read-only (`monitor_snapshot`,
`monitor_processes`, `monitor_doctor`, `monitor_analyze`) and four mutating
(`monitor_kill`, `monitor_profile_capture`, `monitor_investigate`,
`monitor_record`). Every mutating tool requires `confirm: true` in its typed
input; `monitor_analyze` is read-only and has no confirm gate. See the
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

### `studio`

Launch the interactive TUI (Bubble Tea v2). All nine tabs are rendered with
full keyboard + mouse interactivity. `tui` is an alias.

```bash
./bin/monitor studio                  # launch the TUI
./bin/monitor tui                     # alias
./bin/monitor studio --reload-server  # also expose POST /reload for agents/CI
```

See [The TUI](/guide/tui) for the tab and keyboard reference.
