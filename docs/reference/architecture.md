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
│   ├── telemetry/             # bounded, identity-free NDJSON metric windows
│   ├── cli/                   # cobra subcommands (snapshot, watch, kill, ...)
│   ├── mcp/                   # MCP stdio server (10 tools, mutations confirm-gated)
│   ├── analyzer/              # anomaly rules (CPU spike, RSS growth)
│   ├── capture/               # process stdout/stderr → veclite log store
│   ├── logger/                # bounded veclite log store + keyword search
│   ├── profiler/              # pprof scrape + macOS `sample`
│   ├── procbind/              # process runtime/codebase binding
│   ├── contextids/            # Monitor/Chalupa run correlation
│   ├── issues/                # Run/Event/Issue/Evidence persistence
│   ├── incidents/             # integrity-hashed file.cheap evidence
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
| `internal/telemetry` | Fixed, bounded `monitor.telemetry_window` V1 aggregation and NDJSON serialization. This package is the privacy boundary for external adapters and contains no transport or persistence. |
| `internal/cli` | Cobra subcommands (`snapshot`, `watch`, `telemetry`, `analyze`, `process`, `processes`, `resolve`, `tree`, `kill`, `profile`, `investigate`, `issues`, `stash`, `incidents`, `logs`, `history`, `baseline`, `diff`, `config`, `doctor`, `run`, `reload`, `mcp`, `vault`, `studio`) plus the JSON/NDJSON output helpers. |
| `internal/mcp` | MCP stdio server: 10 tools (6 read-only, 4 mutating) over the standard Model Context Protocol transport, with confirm-gated mutation. |
| `internal/analyzer` | Pluggable anomaly rules — `CPUSpikeRule` (current CPU versus a rolling per-PID median plus an absolute floor), `RSSGrowthRule` (wall-clock-normalized RSS regression), `ZombieRule`, `DiskFillRule`, `SwapPressureRule`, and a config-driven `ThresholdRule`. Per-process alerts attach a `Diagnosis` when their cross-signal history can interpret the signal; `internal/analyzer/diagnosis.go` classifies memory-leak / hot-loop / load / GC-pressure patterns for CLI, MCP, and watch consumers. |
| `internal/capture` | Log capture pipeline — pumps a child process's stdout/stderr into the veclite log store. |
| `internal/logger` | Durable veclite-backed log store with metadata/time filtering and export; default retention is 7 days / 100,000 FIFO records, and searches are capped at 1,000. `logs capture` holds the writer while search opens read-only with shared-read. |
| `internal/profiler` | Runtime-aware process profiling — Node/Bun/Deno CPU and bounded heap capture over a loopback CDP inspector, Go `net/http/pprof`, and macOS `sample` fallback. |
| `internal/procbind` | Inspects or safely resolves one process, classifies its runtime, detects Node inspector metadata, and resolves the nearest codebase root. Argv is used in memory and redacted from serialized bindings. |
| `internal/contextids` | Resolves explicit, `MONITOR_*`, and `CHALUPA_CI_*` identifiers into run context and searchable incident tags without changing telemetry V1. |
| `internal/issues` | Groups occurrences into issues using Fingerprint V1 and stores Run/Event/Issue/Evidence data in a private veclite database. Resolved issues reopen on a later occurrence; ignored issues continue accumulating without reopening. |
| `internal/incidents` | Integrity-hashed incident evidence — bundles a snapshot and optional profile/code context, validates regular-file layout, and saves it to fcheap. Failed archival uses a private, 20-entry recovery registry; successful saves may yield validated ArtifactRefV1. |
| `internal/reload` | Localhost HTTP `/reload` endpoint (POST on `127.0.0.1:7351`) signalling external processes that data changed. |
| `internal/temperature` | Real SMC temperature via `sudo powermetrics`, with a transparent CPU-load estimate fallback and a `real`/`est` source badge. |
| `internal/ecosystem` | CLI wrappers for the surrounding tools (codemap, fcheap, vecgrep, tinyvault, glyphrun, ...); `Probe(ctx)` returns aggregate health for `doctor`, while typed adapters preserve code-index readiness and ArtifactRefV1 validation. |
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
- **CLI** (`internal/cli`) reads a single `Event` for `snapshot`, projects it
  through `collector.BuildCompactSnapshot` when `--compact` is requested, or
  streams NDJSON for `watch`. `telemetry` selects a closed scalar-only
  collection profile that never invokes identity, process, filesystem,
  per-core/topology, temperature, cgroup, or history collectors, then projects
  the fixed host metrics through `internal/telemetry` into bounded,
  identity-free windows.
- **MCP** (`internal/mcp`) answers `monitor_snapshot` / `monitor_processes`
  from the same data (including the same bounded compact projection), samples
  a short window through `internal/analyzer` for
  `monitor_analyze`, and routes mutating tools through `internal/kill` and
  `internal/profiler`.
- **Analyzer, capture, incidents** observe events to flag anomalies, ingest
  logs, and assemble incident bundles.

## Run, Event, Issue, and Evidence

The local issue plane sits beside metric collection; it is not part of the
telemetry export schema:

```text
collector/analyzer ──→ Event (occurrence) ── fingerprint v1 ──→ Issue
                              │                              │
Chalupa/Monitor Run context ─┘                              └── lifecycle
                              └── EvidenceRef ──→ file.cheap or local registry
```

`monitor investigate` creates an occurrence in its seventh `issue` step.
`monitor watch --stash` creates one after capturing an alert bundle. Stable
identity uses project, service, kind, normalized message/exception type, and
symbols. Run, release, PID, tree hash, timestamp, and artifact references stay
on the occurrence so retries and restarts group correctly.

The MCP issue tools are read-only. CLI commands own lifecycle mutations
(`resolve`, `ignore`, `reopen`), while evidence bytes remain in file.cheap or
the bounded local recovery registry.

The telemetry command is intentionally a producer, not an exporter SDK:

```
collector → telemetry window → NDJSON stdout → caller-owned adapter
```

Monitor owns the closed collection plan, monotonic scheduling, schema
validation, privacy projection, aggregation, and a bounded partial flush. The
adapter owns deployment labels, authentication, delivery, bounded retry
buffering, and durable storage. Keeping those responsibilities outside Monitor
prevents transport credentials and vendor-specific concepts from entering the
local observability core.

Process termination, wherever it is requested, funnels through the shared safety
check in `internal/kill`: protected and system processes are refused, and an
explicit action is required (the TUI prompts, the CLI command states the target,
and MCP tools need `confirm: true`). CLI `--yes` never bypasses protection.

## Notes on the current tree

- **v1 was removed.** Earlier builds shipped a Bubble Tea v1 TUI under
  `internal/ui` (reachable via `monitor v1`) alongside a legacy
  `internal/system` collector. Both are gone — `internal/ui/studio` is the only
  TUI and `internal/collector` is the only collector.
- **A single lipgloss.** The binary links exactly one lipgloss,
  `charm.land/lipgloss/v2`. Both the TUI and `internal/widgets` are on v2, so
  there is no v1/v2 styling split to reconcile.
