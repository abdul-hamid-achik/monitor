# MCP Server

Monitor exposes the same data it shows in the TUI to AI agents through a
[Model Context Protocol](https://modelcontextprotocol.io/) (MCP) stdio server.
An agent can orient itself with a system snapshot, drill into processes, check
ecosystem health, and — with explicit confirmation — take action.

## Starting the server

```bash
./bin/monitor mcp serve
```

The server speaks MCP over **stdio** using newline-delimited JSON-RPC. It does
not open a port; the agent harness launches `monitor mcp serve` as a child
process and talks to it over stdin/stdout. The serve subcommand takes no flags
of its own — just run it and connect. (The global `--no-temperature-source`
flag still applies and forces the CPU-load temperature fallback.)

## Tools

The server registers **8 tools**: 4 read-only and 4 mutating. Every tool
returns JSON.

### Read-only tools

| Tool | Description |
|------|-------------|
| `monitor_snapshot` | Return the latest system view with an interpreted `summary` and optional `next` suggestions. Pass `compact:true` for the bounded, history-free `monitor.compact_snapshot` v1 payload recommended for agent context; omit it for the backward-compatible full `SystemInfo`. |
| `monitor_processes` | Return the top processes. Typed input: `limit` (default 15, max 200), `sort_by` (`cpu` default or `rss`), `filter` (case-insensitive substring on the process name). Output: `{processes, total, truncated, reason}`. |
| `monitor_doctor` | Report ecosystem tool availability (codemap, fcheap, tinyvault, glyphrun, etc.). |
| `monitor_analyze` | Sample metrics for `window_seconds` (default 10, min 4, max 60) and return `{window_seconds, samples, diagnoses, healthy}` where `diagnoses` is `[]Diagnosis` (`summary`, `evidence`, `confidence`, `next_actions`) — always present, `[]` when nothing looks wrong (`healthy:true`). Optional `pid` focuses the diagnosis on one process and is echoed back. The tool to call when something is slow. Read-only — no confirm. |

These take no required input (`monitor_analyze` and `monitor_processes` take
optional fields) and never change anything on the host. The server
instructions tell the agent to call `monitor_snapshot` first to orient, then
drill down with `monitor_processes` or `monitor_doctor`, or reach for
`monitor_analyze` directly when the user reports slowness or a suspected
leak.

For a small local model, prefer:

```json
{
  "name": "monitor_snapshot",
  "arguments": {
    "compact": true,
    "process_limit": 5,
    "process_filter": "ollama",
    "filesystem_limit": 10,
    "filesystem_filter": "apfs"
  }
}
```

The filters are case-insensitive. Limits are clamped (`process_limit` to 25,
`filesystem_limit` to 50), histories are omitted, and truncation/total fields
tell the caller when to drill down with `monitor_processes`. The full response
remains the default so existing clients are not broken.

### Mutating tools

| Tool | Description |
|------|-------------|
| `monitor_kill` | Safely terminate a process. `force=true` sends SIGKILL instead of SIGTERM. The signal is verified, not just dispatched: the response carries `killed` (true only once the process is confirmed gone), `outcome` (`terminated`\|`still_running`\|`unknown`), `signal`, `waited_ms`, and — when the process survives — a `next_action` suggesting `force:true`. Kill never escalates to SIGKILL on its own. |
| `monitor_profile_capture` | Capture a profile for a process. `type` is one of `heap`, `cpu`, `goroutine`, `sample` (default `heap`). Refuses to scrape `heap`/`cpu`/`goroutine` unless the pprof listener at `localhost:6060` is proven to belong to the target `pid` (use `type:sample` instead when it isn't). A successful capture must also produce a non-empty artifact — `captured:false` with `limitation` and `next_actions` otherwise; on success the response includes an `artifact` receipt (`{verified, size_bytes}`). |
| `monitor_investigate` | Run the full diagnostic pipeline for a process: capture a snapshot, capture a profile (ownership-gated pprof heap, falling back to macOS `sample`), correlate hot frames to codemap symbols (with blast radius + test coverage), and stash the bundle with fcheap. Returns typed `steps: [{step, status, limitation, recovery}]` and an overall `verdict` (`complete`\|`partial`); `investigated` reflects `verdict=="complete"`, never a blind true. |
| `monitor_record` | Capture a real screen recording via the platform recorder (`screencapture` on macOS, `ffmpeg` x11grab on Linux) for `duration` seconds (default 30), returning a video path that vidtrace can analyze. The response verifies the recording file exists and is non-empty (`artifact_verified`/`artifact_bytes`), or reports `recording:false` with a `limitation` when it doesn't; a non-path `bundle_id` (e.g. an opaque vidtrace id) is `artifact_verified:false` since existence can't be checked. |

Each mutating tool takes a `pid` and a required `confirm` field (see below).

## The confirm gate

Every mutating tool requires `confirm: true` in its typed input before it will
act. This mirrors the CLI's `--yes` convention and keeps the MCP surface safe
for agents: the agent must explicitly assert intent before anything changes on
the host.

The gate is enforced at two layers:

1. **Schema.** `confirm` is a required field on each mutating tool's typed
   input, so the MCP SDK rejects calls that omit it outright.
2. **Handler re-check.** Even for hand-built requests that reach a handler, the
   handler re-checks the flag. When `confirm` is missing or false, the tool
   does **not** act — it returns a structured refusal payload instead of an
   opaque error, so the harness can inspect the reason and retry:

```json
{
  "killed": false,
  "refused": true,
  "reason": "refused: confirm=true required",
  "pid": 1234
}
```

A correct, confirmed call looks like this:

```json
{
  "name": "monitor_kill",
  "arguments": {
    "pid": 1234,
    "force": false,
    "confirm": true
  }
}
```

### Kill safety still applies

`confirm: true` is necessary but not sufficient. `monitor_kill` runs the same
shared safety check used by the TUI and CLI. Protected processes (`launchd`,
`kernel_task`, `WindowServer`, `Finder`, `Dock`, …) and system-owned processes
are refused even with `confirm: true`, and there is no override path through
the tool:

```json
{
  "killed": false,
  "refused": true,
  "reason": "refused: target is a protected or system-owned process; this tool cannot terminate it",
  "pid": 1
}
```

### Graceful degradation

`monitor_investigate` and `monitor_record` are fully wired to real
implementations. They still degrade gracefully when the host can't satisfy the
request, returning the same structured shape rather than failing.

`monitor_investigate` runs the real pipeline: snapshot -> profile -> correlate
-> stash, with a typed receipt per step. The profile step is ownership-gated —
it only trusts a `localhost:6060` pprof heap scrape when the LISTEN socket is
proven to belong to the target `pid` (via `/proc`/`lsof`-backed connection
enumeration), and falls back to macOS `sample` otherwise. Frames are
correlated to codemap symbols (resolving each file:line to its enclosing
function, then enriching resolved frames with blast radius and test coverage)
best-effort — skipped when `codemap` isn't on `PATH` or the profile's frames
carry no file:line (true of `sample` output). An empty or unverified profile
is never stashed: `steps[].limitation`/`recovery` explain what happened, and
the top-level `verdict` is `"complete"` only when every step succeeded.

`monitor_record` invokes the platform recorder directly — `screencapture -V`
on macOS or `ffmpeg -f x11grab` on Linux — and returns the path to the captured
video, which can then be analyzed with vidtrace (`vidtrace index` /
`vidtrace analyze`). The handler stats the returned path before reporting
success: a missing or zero-byte file comes back as `recording:false` with a
`limitation` rather than a silent lie. On a headless host (no recorder binary,
no X11 `DISPLAY`, or denied screen-capture permission) it refuses gracefully
with a structured payload instead of erroring:

```json
{
  "recording": false,
  "refused": true,
  "reason": "no screen recorder available (record service not configured)",
  "pid": 1234
}
```

## Client configuration

Register Monitor as an MCP server in your agent client. The exact file depends
on the client, but the shape is the same — a command and its arguments:

```json
{
  "mcpServers": {
    "monitor": {
      "command": "monitor",
      "args": ["mcp", "serve"]
    }
  }
}
```

If `monitor` is not on your `PATH`, use an absolute path to the binary
(for example `/usr/local/bin/monitor`, or `./bin/monitor` from a source
build).

## See also

- [CLI Reference](/guide/cli) — the same data over `--json` commands.
- The mutating tools mirror the CLI subcommands `kill`, `profile`,
  `investigate`, and `record`; the CLI gates them with `--yes` where the MCP
  tools gate with `confirm: true`.
