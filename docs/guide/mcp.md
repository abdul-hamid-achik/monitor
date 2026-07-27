# MCP Server

Monitor exposes the same data it shows in the TUI to AI agents through a
[Model Context Protocol](https://modelcontextprotocol.io/) (MCP) stdio server.
An agent can orient itself with a system snapshot, drill into processes, check
ecosystem health, and — with explicit confirmation — take action.

## Starting the server

```bash
monitor mcp serve
```

The server speaks MCP over **stdio** using newline-delimited JSON-RPC. It does
not open a port; the agent harness launches `monitor mcp serve` as a child
process and talks to it over stdin/stdout. The serve subcommand takes no flags
of its own — just run it and connect. (The global `--no-temperature-source`
flag still applies and forces the CPU-load temperature fallback.)

## Tools

The server registers **10 tools**: 6 read-only and 4 mutating. Every tool
returns JSON.

Each tool also publishes standard MCP safety annotations. Read tools advertise
`readOnlyHint:true`; all tools advertise `openWorldHint:false` because Monitor
only inspects the local host and its configured local integrations. The kill
tool advertises `destructiveHint:true` and remains non-idempotent because a PID
can be reused between calls. Profile, investigate, and record advertise
`destructiveHint:false`: they create local evidence but do not overwrite or
delete the target process. These annotations help compatible clients render
approval UI, but they are hints—the typed `confirm` field and handler checks
remain the enforcement boundary.

### Read-only tools

| Tool | Description |
|------|-------------|
| `monitor_snapshot` | Return the latest system view with an interpreted `summary` and optional `next` suggestions. Pass `compact:true` for the bounded, history-free `monitor.compact_snapshot` v1 payload recommended for agent context; omit it for the backward-compatible full `SystemInfo`. |
| `monitor_processes` | Return the top processes. Typed input: `limit` (default 15, max 200), `sort_by` (`cpu` default or `rss`), `filter` (case-insensitive substring on the process name). Output: `{processes, total, truncated, reason}`. |
| `monitor_doctor` | Report ecosystem tool availability (codemap, fcheap, tinyvault, glyphrun, etc.). |
| `monitor_analyze` | Sample metrics for `window_seconds` (default 10, min 4, max 60) and return `{window_seconds, samples, diagnoses, healthy}` where `diagnoses` is `[]Diagnosis` (`summary`, `evidence`, `confidence`, `next_actions`) — always present, `[]` when nothing looks wrong (`healthy:true`). Optional `pid` focuses the diagnosis on one process and is echoed back. The tool to call when something is slow. Read-only — no confirm. |
| `monitor_issues` | List recurring local issues newest-first. Optional `statuses` (`open`, `resolved`, `ignored`), `project`, `service`, and `limit` (default 50, max 200). Returns `{issues, total, truncated}`. |
| `monitor_issue` | Get one issue and its recent occurrences. Requires `id`; `occurrence_limit` defaults to 20 and is capped at 200. Returns `{issue, occurrences, occurrences_truncated}` or a structured `not_found` error payload. |

All read-only tools except `monitor_issue` have no required input;
`monitor_issue` requires `id`. Optional filters never change anything on the
host. The server instructions tell the agent to call `monitor_snapshot` first
to orient, then
drill down with `monitor_processes` or `monitor_doctor`, or reach for
`monitor_analyze` directly when the user reports slowness or a suspected
leak. For recurring failures, call `monitor_issues` to triage and
`monitor_issue` to inspect Run/Event/Evidence context before capturing more
evidence.

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

### Recommended agent workflow

Use the smallest tool that answers the question, and only create evidence when
the read-only pass justifies it:

1. Call `monitor_snapshot` with `{"compact":true}` to orient.
2. Use `monitor_processes` to find a candidate PID, or `monitor_issues` to
   start from a recurring failure.
3. Call `monitor_analyze` for a short read-only observation window; inspect an
   existing group with `monitor_issue` before collecting duplicate evidence.
4. With explicit user intent, call `monitor_investigate` with `confirm:true`.
   Pass `codebase` when process binding cannot infer the project; pass Chalupa
   IDs when the incident belongs to an ephemeral CI environment.
5. Use `monitor_profile_capture`, `monitor_record`, or `monitor_kill` only when
   the previous result recommends that narrower action.

This ordering keeps model context bounded and separates observation from host
mutation. A `partial` investigation is still useful: read its per-step
limitations and follow the suggested recovery action instead of retrying every
tool blindly.

### Mutating tools

| Tool | Description |
|------|-------------|
| `monitor_kill` | Safely terminate a process. `force=true` sends SIGKILL instead of SIGTERM. The signal is verified, not just dispatched: the response carries `killed` (true only once the process is confirmed gone), `outcome` (`terminated`\|`still_running`\|`unknown`), `signal`, `waited_ms`, and — when the process survives — a `next_action` suggesting `force:true`. Kill never escalates to SIGKILL on its own. |
| `monitor_profile_capture` | Capture a profile for a process. `type` is one of `heap`, `cpu`, `goroutine`, `sample` (default `heap`). Refuses to scrape `heap`/`cpu`/`goroutine` unless the pprof listener at `localhost:6060` is proven to belong to the target `pid` (use `type:sample` instead when it isn't). A successful capture must also produce a non-empty artifact — `captured:false` with `limitation` and `next_actions` otherwise; on success the response includes an `artifact` receipt (`{verified, size_bytes}`). |
| `monitor_investigate` | Run `identify` → `snapshot` → `profile` → `correlate` → `semantic` → `stash` → `issue`. It prefers an ownership-verified Node inspector CPU profile, otherwise ownership-gated pprof or macOS `sample`; enriches usable frames with codemap and a fresh vecgrep index; archives evidence with file.cheap; and records the occurrence. Returns typed `steps` and `verdict` (`complete`\|`partial`). |
| `monitor_record` | Capture a real screen recording via the platform recorder (`screencapture` on macOS, `ffmpeg` x11grab on Linux) for `duration` seconds (default 30), returning a video path that vidtrace can analyze. The response verifies the recording file exists and is non-empty (`artifact_verified`/`artifact_bytes`), or reports `recording:false` with a `limitation` when it doesn't; a non-path `bundle_id` (e.g. an opaque vidtrace id) is `artifact_verified:false` since existence can't be checked. |

Each mutating tool takes a `pid` and a required `confirm` field (see below).

## The confirm gate

Every mutating tool requires `confirm: true` in its typed input before it will
act. This makes an agent explicitly assert intent before anything changes on
the host. Process protection remains a separate invariant after confirmation.

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

`monitor_investigate` runs the same seven steps as the CLI, with a typed receipt
per step. Node/Bun/Deno processes with a detected inspector first prove that
the port belongs to the target PID, then capture a bounded CDP CPU profile.
Go pprof has the same ownership requirement, and macOS `sample` remains a
fallback. Codemap receives the bound codebase through `-C`; vecgrep is trusted
only when its bounded JSON envelope says the index exists and is fresh.
Missing tools, stale indexes, and frames without file/line are reported as
limitations rather than invented correlations. Empty or unverified profile
data is never put in the evidence bundle.

The `issue` step still runs when `no_save:true`; in that case it records an
occurrence without a file.cheap evidence URI. A successful stash may include a
validated, credential-free ArtifactRefV1, while a failed archive can produce a
`monitor://incidents/<id>` reference to the private recovery registry.

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

If the agent client does not inherit your shell `PATH`, run
`command -v monitor` and use the returned absolute path for `command`. A source
build that has not been installed can use `./bin/monitor` instead.

## See also

- [CLI Reference](/guide/cli) — the same data over `--json` commands.
- [Local Issues](/guide/issues) — issue grouping, lifecycle, and evidence references.
- The mutating tools mirror the CLI subcommands `kill`, `profile`,
  `investigate`, and `record`; MCP adds a required `confirm: true` gate, while
  both surfaces retain the same protected-process refusal.
