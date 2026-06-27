# AGENTS.md - Monitor CLI Development Guide

Local-first observability hub for macOS. Built with Go, Bubble Tea, and the Charm
ecosystem. Designed for both human use (TUI) and agent use (JSON CLI + MCP
server).

## Project Overview

**Monitor** is an agent-harnessable local observability tool for macOS (optimized
for Apple Silicon). It features:

- Interactive Bubble Tea TUI (Nord theme)
- JSON CLI commands (`snapshot`, `watch`, `process`, `kill`, `profile`,
  `investigate`, `logs`, `doctor`, `mcp`)
- MCP stdio server (read-only + mutating tools; mutating require
  `confirm: true` in the typed input)
- Process profiling (pprof HTTP scraping + macOS `sample`)
- Anomaly detection (CPU spike, RSS growth with linear regression)
- Ecosystem integrations: codemap, fcheap, vecgrep, vidtrace, glyphrun,
  cairntrace, tinyvault, veclite, tmux
- veclite-backed log store with shared-read for CLI search
- Glyphrun behavioral specs

**Platform**: macOS Apple Silicon (M1/M2/M3/M5)
**Language**: Go 1.25+
**Module**: `github.com/abdul-hamid-achik/monitor`

---

## Quick Reference

### Single-word Taskfile commands

```bash
task build    # Build to bin/monitor
task run      # Build and run
task dev      # Auto-reload on file changes
task test     # Run all unit tests
task cover    # Generate HTML coverage
task bench    # Run benchmarks
task lint     # go vet ./...
task fmt      # gofmt -w .
task tidy     # go mod tidy
task specs    # Run glyphrun behavioral specs
task snapshot # Print JSON system snapshot (shortcut)
task doctor   # Print ecosystem health
task release  # Build optimized release binary
task install  # Install to /usr/local/bin
task remove   # Remove from /usr/local/bin
task version  # Print Go and module versions
task info     # Show project info
task help     # Show all available tasks
task clean    # Remove build artifacts
task check    # Full CI pipeline (tidy + lint + test + release)
task all      # Alias for check
```

### Direct Go commands

```bash
go build -o bin/monitor ./cmd/monitor
go test -v ./...
go vet ./...
go mod tidy
```

---

## File Layout

```
monitor/
├── cmd/monitor/
│   └── main.go                  # Entry: dispatches CLI vs TUI based on args
├── internal/
│   ├── analyzer/                # NEW: anomaly detection (CPU spike, RSS growth)
│   │   └── analyzer_test.go
│   ├── cli/                     # NEW: cobra subcommands
│   │   ├── root.go              # Root + subcommand registration
│   │   ├── snapshot.go          # `monitor snapshot`
│   │   ├── watch.go             # `monitor watch` (NDJSON)
│   │   ├── kill.go              # `monitor kill` + `monitor process`
│   │   ├── profile_logs.go      # `monitor profile`, `monitor logs`, `monitor investigate`
│   │   ├── doctor.go            # `monitor doctor` + `monitor run`
│   │   ├── mcp.go               # `monitor mcp serve`
│   │   ├── studio.go            # `monitor studio` (the TUI; alias `tui`)
│   │   ├── util.go              # JSON output helpers, context handling
│   │   └── cli_test.go
│   ├── collector/               # NEW: pub/sub metric collector
│   │   ├── collector.go         # Collector + Subscribe
│   │   ├── types.go             # CPUInfo, MemoryInfo, ProcessInfo, etc.
│   │   ├── ringbuffer.go        # Generic ring buffer
│   │   ├── collector_test.go
│   │   ├── ringbuffer_test.go
│   │   └── types_test.go
│   ├── config/                  # Settings (JSON at ~/.config/monitor/config.json)
│   │   ├── config.go
│   │   └── config_test.go
│   ├── ecosystem/               # NEW: CLI wrappers for codemap/fcheap/tvault/etc.
│   │   ├── registry.go          # Status + TinyvaultRun + RunGlyphrun
│   │   └── registry_test.go
│   ├── incidents/               # NEW: fcheap content-addressed incident stash
│   │   ├── incidents.go         # Capture (bundle + tree-hash + fcheap save)
│   │   └── incidents_test.go    # tree-hash stability, no-fcheap fallback, bundle round-trip
│   ├── capture/                  # NEW: log capture pipeline
│   │   ├── capture.go            # Source, Runner, parseLevel, looksLikeLogPath
│   │   └── capture_test.go       # 9 unit tests
│   ├── kill/                    # NEW: safe process termination
│   │   ├── kill.go
│   │   └── kill_test.go
│   ├── reload/                  # NEW: /reload HTTP endpoint for the TUI
│   │   ├── reload.go            # Reloader, NoopReloader, Server, DefaultAddr
│   │   └── reload_test.go       # 7 unit tests (healthz, reload, idempotence, ...)
│   ├── logger/                  # NEW: veclite-backed log store
│   │   ├── store.go
│   │   └── store_test.go
│   ├── mcp/                     # NEW: MCP stdio server (7 tools: 3 read-only + 4 mutating)
│   │   ├── server.go            # Service, Server, tool handlers, confirm gate
│   │   └── server_test.go       # handler unit tests
│   ├── temperature/             # NEW: real SMC temperature via sudo powermetrics
│   │   ├── temperature.go       # Source, Kind, Reading; streaming subprocess lifecycle
│   │   └── temperature_test.go  # parser variants, fallback, fake-binary upgrade
│   ├── profiler/                # NEW: pprof + sample profiling
│   │   ├── profiler.go
│   │   └── profiler_test.go
│   ├── ui/studio/               # The TUI (Bubble Tea v2 — charm.land/bubbletea/v2 + lipgloss/v2)
│   │   ├── model.go             # Model, tea.View, tea.KeyPressMsg, tab router, header, status bar (all 9 tabs)
│   │   ├── run.go               # entry point (`monitor studio`)
│   │   ├── cpu.go               # CPU tab
│   │   ├── memory.go            # Memory tab
│   │   ├── disk.go              # Disk tab
│   │   ├── network.go           # Network tab
│   │   ├── processes.go         # Processes tab (bubbles/v2 table)
│   │   └── model_test.go        # v2 unit tests
│   └── widgets/                 # Reusable widgets (sparklines, gauges; lipgloss v2)
│       ├── gauge.go
│       └── gauge_test.go
├── specs/                       # glyphrun behavioral specs (23 specs, all passing)
│   ├── baseline.yml             # save/list/delete + path-traversal guard
│   ├── cli_help.yml
│   ├── diff.yml                 # baseline vs live diff
│   ├── doctor_json.yml
│   ├── env_detection.yml
│   ├── history.yml              # query/list on a fresh store
│   ├── incidents.yml            # incidents --help + graceful w/o fcheap
│   ├── investigate.yml
│   ├── kill_safety.yml
│   ├── logs_capture.yml
│   ├── logs_search.yml
│   ├── mcp_handshake.yml
│   ├── process.yml              # process <pid> incl. unknown-pid error
│   ├── profile_sample.yml       # (skipped in CI — needs macOS `sample`)
│   ├── reload.yml
│   ├── run.yml                  # run --help + missing-spec error
│   ├── snapshot_json.yml
│   ├── stash.yml                # (skipped in CI — needs fcheap)
│   ├── studio_help.yml          # studio TUI help + bare-monitor-shows-help
│   ├── tree.yml                 # process hierarchy
│   ├── vault.yml                # vault --help + missing-project error
│   ├── version.yml
│   └── watch_tick.yml
├── Taskfile.yml                 # Single-word commands
├── AGENTS.md                    # This file
├── CLAUDE.md                    # Claude Code companion
├── go.mod
└── README.md
```

---

## Architecture

### Mode Dispatch (`cmd/monitor/main.go`)

- `monitor studio` (alias `tui`) → launch the TUI (`internal/ui/studio`)
- Other subcommand → cobra CLI (`internal/cli`)
- No args / unknown flag → cobra help
- main.go extracts only the global `--pprof` flag, then hands argv to cobra

### Event Bus (`internal/collector`)

The collector publishes `Event` on every tick. Subscribers receive non-blocking
callbacks. This decouples collection from presentation:

```
Collector → [subs] → TUI renderer, analyzer, MCP stream, log capture
```

### Ecosystem Layer (`internal/ecosystem`)

Each tool has an `Available() bool` and typed methods. The `Status(ctx)` function
returns JSON-ready aggregate health. Wrappers follow the vidtrace pattern:
`run(ctx, bin, args)` + `decodeJSON[T]`.

### MCP Server (`internal/mcp`)

Standard Model Context Protocol stdio transport. Tools return JSON via the
shared `result()` helper. Pattern matches codemap's server (one Server struct,
one Service, NL-JSON-RPC framing).

Read-only tools shipped:

- `monitor_snapshot` — full SystemInfo
- `monitor_processes` — top processes
- `monitor_doctor` — ecosystem health

Mutating tools (all require `confirm: true` in the typed input):

- `monitor_kill` — terminate a process; safety-checked, refuses protected
- `monitor_profile_capture` — heap/cpu/goroutine/sample profile
- `monitor_investigate` — diagnostic pipeline (stub when no svc wired)
- `monitor_record` — vidtrace recording (stub; returns structured refusal)

Two-layer safety: the MCP SDK validates the typed input schema (rejecting
calls that omit `confirm` outright), and the handlers re-check `confirm`
so agents that hand-build a request still get a structured refusal payload
with `refused: true` and a `reason`.

### Logger (`internal/logger`)

veclite-backed log store. TUI holds the writer lock; CLI search uses
`OpenReadOnly` with `WithSharedRead(true)` + `WithReadOnly(true)` so concurrent
queries don't block collection. The `/reload` HTTP endpoint in `internal/reload` (POST /reload on 127.0.0.1:7351) signals external processes that data has changed; `monitor --reload-server` starts it and `monitor reload` posts to it.

### Profiler (`internal/profiler`)

- Go processes: scrape `net/http/pprof` over HTTP
- Any process: macOS `sample <pid> 1 -mayDie`
- Parses pprof text into `Symbol{Func, File, Line}`

### Analyzer (`internal/analyzer`)

Pluggable rules:

- `CPUSpikeRule` — flags CPU% > factor × baseline
- `RSSGrowthRule` — linear regression on RSS ring buffer; slope + R²
- `ZombieRule` — processes in state Z (planned)

Engine observes every `collector.Event` and returns fired alerts.

### Environment Detection

When monitor launches a child process or spec, it sets:

- `MONITOR=1`
- `MONITOR_RUN_DIR=<run-dir>`

So child processes can detect they are being observed. Mirrors glyphrun's
`GLYPHRUN=1` / `GLYPHRUN_RUN_DIR` pattern.

---

## Coding Conventions

### Naming

- Files: lowercase, snake_case (`collector.go`, `app.go`)
- Types: PascalCase, no domain prefix (`CPUInfo`, `ProcessInfo`)
- Functions: PascalCase exported, camelCase private
- Constants: PascalCase (`TabOverview`)
- Variables: camelCase, descriptive

### Style

- Errors returned immediately, early returns
- Composite struct literals with field names
- Package-level doc comments on every package
- Imports: stdlib first, third-party second, local last

### JSON Tags

All metric structs include `json:"snake_case"` tags for stable CLI output.

---

## Testing

### Unit tests

Every package has a `_test.go`. Run with:

```bash
task test          # all
go test ./internal/collector/...
```

Test patterns:

- Table-driven tests for pure functions
- `t.TempDir()` for filesystem
- `context.Background()` for collector; `context.WithCancel` for goroutines

### Glyphrun behavioral specs

```bash
task specs                                  # all specs
~/projects/glyphrun/bin/glyph run specs/version.yml
```

Each spec has:

- `intent:` — one-line purpose
- `target:` — command under test
- `terminal:` — PTY dimensions
- `preconditions:` — setup commands
- `steps:` — interaction sequence
- `outcomes:` — verifiable assertions (screen / process / command)

Adding a new spec: copy an existing one, adjust intent + target + outcomes.

---

## Dependency Plan

| Package | Import |
|---------|--------|
| Bubble Tea v2 | `charm.land/bubbletea/v2` (the only TUI runtime) |
| Bubbles v2 | `charm.land/bubbles/v2` (table widget in the Processes tab) |
| Lipgloss v2 | `charm.land/lipgloss/v2` (TUI styling + `internal/widgets`) |
| gopsutil | `github.com/shirou/gopsutil/v4` |
| Cobra | `github.com/spf13/cobra` |
| MCP SDK | `github.com/modelcontextprotocol/go-sdk/mcp` |
| Veclite | `github.com/abdul-hamid-achik/veclite` |
| Clipboard | `github.com/atotto/clipboard` |

The binary links a single lipgloss (`charm.land/lipgloss/v2`) — both the TUI
and `internal/widgets` are on v2.

---

## Important Gotchas

1. **veclite requires read-only for shared-read** — a writer cannot enable
   shared-read; only readers do.
2. **cobra `--version` exits the process** — test via `Root().Version`, not by
   executing with `--version` (would call `os.Exit`).
3. **Bubble Tea v2 is a breaking change** — `View()` returns `tea.View`, not
   `string`; `tea.KeyMsg` becomes `tea.KeyPressMsg`. The TUI lives in
   `internal/ui/studio/` (launched via `monitor studio`); all 9 tabs are
   ported with full interactivity.
4. **MCP tool handlers** need `*mcp.CallToolRequest` as second argument.
5. **Temperature readings come from `sudo powermetrics` when available**
   (`internal/temperature`). Falls back to a CPU-load estimate when
   sudo can't be obtained; the `temperature.source` field on the
   `SystemInfo` JSON (`"estimated"` or `"powermetrics"`) and the TUI's
   `● real` / `● est` badge tell the caller which.

---

## Common Tasks for Agents

### Add a CLI subcommand

1. Create `internal/cli/<name>.go` with `newXxxCmd()` returning `*cobra.Command`
2. Register in `internal/cli/root.go`'s `AddCommand` list
3. Add `JSONOutput(cmd) bool` branch for `--json`
4. Test manually: `./bin/monitor <name> --help`
5. Add unit test in `cli_test.go`

### Add an MCP tool

1. Add typed input struct in `internal/mcp/server.go`
2. Add handler `func (s *Server) handleXxx(ctx, req, in) (*CallToolResult, any, error)`
3. Register in `register()` via `mcp.AddTool`
4. Test with an MCP client (Claude Code)

### Add a glyphrun spec

1. Copy an existing spec in `specs/`
2. Adjust intent, target, steps, outcomes
3. Validate: `glyph spec verify specs/<name>.yml`
4. Run: `glyph run specs/<name>.yml --format md`

### Update protected-process list

`internal/collector/types.go` — `ProtectedProcessNames` map.

---

## Known Limitations

1. **Load averages on macOS** — always 0 (gopsutil doesn't expose them on
   macOS); Linux reads real values from `/proc/loadavg`.
2. **Temperature** — real SMC readings need `sudo powermetrics` (macOS); else a
   CPU-load estimate, badged `est`. Linux is always the estimate.
3. **History concurrency** — the recorder holds an exclusive veclite lock, so
   `monitor history query` can't run while a recorder is active (clear error).

---

## References

- Design notes: `~/notes/projects/monitor/`
- Iteration 1 (Vision & Roadmap): `~/notes/projects/monitor/Rewrite Vision and Roadmap.md`
- Iteration 2 (Concrete Specs): `~/notes/projects/monitor/Iteration 2 - Concrete Specs and Validated Patterns.md`
- Codemap MCP pattern: `~/projects/codemap/internal/mcp/server.go`
- Vecgrep Bubble Tea v2 studio: `~/projects/vecgrep/internal/studio/`
- Glyphrun spec model: `~/projects/glyphrun/docs/verifiers.md`