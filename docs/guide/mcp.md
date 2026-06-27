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
process and talks to it over stdin/stdout. There are no flags — just run the
subcommand and connect.

## Tools

The server registers **7 tools**: 3 read-only and 4 mutating. Every tool
returns JSON.

### Read-only tools

| Tool | Description |
|------|-------------|
| `monitor_snapshot` | Return the latest `SystemInfo` (CPU, memory, temperature, network, disk, processes). |
| `monitor_processes` | Return the process list, already sorted by CPU. |
| `monitor_doctor` | Report ecosystem tool availability (codemap, fcheap, tinyvault, glyphrun, etc.). |

These take no required input and never change anything on the host. The server
instructions tell the agent to call `monitor_snapshot` first to orient, then
drill down with `monitor_processes` or `monitor_doctor`.

### Mutating tools

| Tool | Description |
|------|-------------|
| `monitor_kill` | Safely terminate a process. `force=true` sends SIGKILL instead of SIGTERM. |
| `monitor_profile_capture` | Capture a profile for a process. `type` is one of `heap`, `cpu`, `goroutine`, `sample` (default `heap`). |
| `monitor_investigate` | Run the diagnostic pipeline (snapshot + profile + search + correlate) for a process. |
| `monitor_record` | Start a vidtrace screen recording for a process (`duration` in seconds, default 30). |

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

### Unconfigured services

`monitor_investigate` and `monitor_record` may not have a backing service wired
in. When a service is unavailable the tool returns the same structured shape
rather than failing — for example `monitor_record` reports that vidtrace is not
installed, and `monitor_investigate` falls back to a stable stub payload so the
agent always sees the same fields.

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
