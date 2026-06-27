# Architecture

Monitor is one binary with three faces — an interactive TUI, a JSON CLI, and an
MCP stdio server — all fed by a single metric collector. This page maps the
package layout and the data flow that ties them together.

## Mode dispatch

`cmd/monitor/main.go` is the entry point. It extracts the global `--pprof`
flag, then hands everything to cobra (`internal/cli`):

- **`monitor studio`** (alias `tui`) → launch the Bubble Tea v2 TUI
  (`internal/ui/studio`)
- **A subcommand** (`snapshot`, `watch`, `mcp serve`, ...) → run the cobra CLI
- **No args / unknown flag** → print cobra help

So the TUI is an explicit subcommand and a bare `monitor` surfaces the help —
clearer DX than auto-launching a full-screen UI.

## Package layout

```
monitor/
├── cmd/monitor/main.go        # entry point; dispatches CLI vs TUI
├── internal/
│   ├── collector/             # pub/sub metric collector (canonical pattern)
│   ├── cli/                   # cobra subcommands (snapshot, watch, kill, ...)
│   ├── mcp/                   # MCP stdio server (7 tools, confirm-gated)
│   ├── analyzer/              # anomaly rules (CPU spike, RSS growth)
│   ├── capture/               # process stdout/stderr → veclite log store
│   ├── logger/                # veclite-backed log store + keyword search
│   ├── profiler/              # pprof scrape + macOS `sample`
│   ├── incidents/             # fcheap content-addressed incident stash
│   ├── reload/                # localhost HTTP /reload endpoint
│   ├── temperature/           # real SMC temperature via powermetrics (+fallback)
│   ├── ecosystem/             # CLI wrappers for codemap/fcheap/tvault/glyphrun
│   ├── kill/                  # safe process termination
│   ├── config/                # JSON settings (~/.config/monitor/config.json)
│   ├── ui/studio/             # the TUI (Bubble Tea v2 / charm.land)
│   └── widgets/               # sparklines, gauges (lipgloss v2)
├── specs/                     # glyphrun behavioral specs
├── Taskfile.yml
└── README.md
```

### Package roles

| Package | Role |
|---------|------|
| `internal/collector` | Pub/sub metric collector — publishes an `Event` on every tick; the canonical pattern other packages follow. Holds the metric types (`CPUInfo`, `MemoryInfo`, `ProcessInfo`, ...) and a generic ring buffer. |
| `internal/cli` | Cobra subcommands (`snapshot`, `watch`, `process`, `kill`, `profile`, `logs`, `investigate`, `doctor`, `mcp`, `studio`) plus the `--json` output helpers. |
| `internal/mcp` | MCP stdio server: 7 tools (3 read-only, 4 mutating) over the standard Model Context Protocol transport, with confirm-gated mutation. |
| `internal/analyzer` | Pluggable anomaly rules — `CPUSpikeRule` (CPU% over a baseline factor) and `RSSGrowthRule` (linear regression on the RSS ring buffer). |
| `internal/capture` | Log capture pipeline — pumps a child process's stdout/stderr into the veclite log store. |
| `internal/logger` | veclite-backed log store with keyword search; the TUI holds the writer, CLI search opens read-only with shared-read. |
| `internal/profiler` | Process profiling — scrapes `net/http/pprof` over HTTP for Go processes, and runs macOS `sample` for any process. |
| `internal/incidents` | Content-addressed incident stash — bundles a snapshot, tree-hashes it, and saves it to fcheap (with a no-fcheap fallback). |
| `internal/reload` | Localhost HTTP `/reload` endpoint (POST on `127.0.0.1:7351`) signalling external processes that data changed. |
| `internal/temperature` | Real SMC temperature via `sudo powermetrics`, with a transparent CPU-load estimate fallback and a `real`/`est` source badge. |
| `internal/ecosystem` | CLI wrappers for the surrounding tools (codemap, fcheap, tinyvault, glyphrun, ...); `Status(ctx)` returns aggregate health for `doctor`. |
| `internal/kill` | Safe process termination — the shared safety check used by the TUI, CLI, and MCP alike. |
| `internal/config` | JSON settings read from and written atomically to `~/.config/monitor/config.json`. |
| `internal/ui/studio` | The TUI (`monitor studio`), on Bubble Tea v2 (`charm.land/bubbletea/v2`) — all 9 tabs with full keyboard and mouse interactivity. |
| `internal/widgets` | Reusable rendering widgets (sparklines, gauges) on lipgloss v2. |

## Data flow

The collector is the single source of truth. It runs on a tick, gathers metrics
once, and fans them out to every subscriber via non-blocking callbacks. This
decouples collection from presentation, so the same `Event` drives the live TUI,
the streaming CLI, the analyzer, the MCP surface, and log capture:

```
                         ┌─────────────────┐
                         │   collector     │  (tick → Event)
                         └───────┬─────────┘
                                 │ pub/sub
            ┌────────────────────┼────────────────────┐
            ▼                    ▼                    ▼
     ┌─────────────┐     ┌──────────────┐     ┌──────────────┐
     │ studio TUI  │     │  cli (JSON)  │     │  mcp server  │
     └─────────────┘     └──────────────┘     └──────────────┘
            │                    │                    │
            └─────── analyzer, capture, incidents ────┘
```

- **TUI** (`internal/ui/studio`) subscribes and re-renders each tab as events arrive.
- **CLI** (`internal/cli`) reads a single `Event` for `snapshot`, or streams
  NDJSON for `watch`.
- **MCP** (`internal/mcp`) answers `monitor_snapshot` / `monitor_processes`
  from the same data, and routes mutating tools through `internal/kill` and
  `internal/profiler`.
- **Analyzer, capture, incidents** observe events to flag anomalies, ingest
  logs, and assemble incident bundles.

Process termination, wherever it is requested, funnels through the shared safety
check in `internal/kill`: protected and system processes are refused, and an
explicit confirmation is required (the TUI prompts, the CLI needs `--yes`, the
MCP tools need `confirm: true`).

## Notes on the current tree

- **v1 was removed.** Earlier builds shipped a Bubble Tea v1 TUI under
  `internal/ui` (reachable via `monitor v1`) alongside a legacy
  `internal/system` collector. Both are gone — `internal/ui/studio` is the only
  TUI and `internal/collector` is the only collector.
- **A single lipgloss.** The binary links exactly one lipgloss,
  `charm.land/lipgloss/v2`. Both the TUI and `internal/widgets` are on v2, so
  there is no v1/v2 styling split to reconcile.
