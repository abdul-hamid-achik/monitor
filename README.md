# Monitor

A terminal-based, **agent-harnessable** system monitor for macOS and Linux,
built in Go with the Charm ecosystem (Bubble Tea v2) and a Nord theme.

Monitor runs as an interactive TUI by default, and exposes the same data to
scripts, agents, and other tools via JSON CLI commands and an MCP stdio
server.

![License](https://img.shields.io/badge/license-MIT-blue.svg)
![Go](https://img.shields.io/badge/go-1.25+-blue.svg)
![Platform](https://img.shields.io/badge/platform-macOS%20%7C%20Linux-blue)

## Features

- 📊 **Real-time TUI** — CPU, Memory, Temperature, Network, Disk, and Processes
- 🤖 **Agent-harnessable** — every view is also a JSON CLI command + an MCP server
- 🌡️ **Real temperature** — SMC sensors via `powermetrics` on macOS, with a
  transparent CPU-load estimate fallback when sudo isn't available
- 🔍 **Process tools** — sort, multi-select, search, and safely terminate
- ⚠️ **Safe process killing** — protected/system processes refuse termination,
  consistently across the TUI, CLI, and MCP surfaces
- 🧩 **Ecosystem integration** — fcheap incident stashes, tinyvault secret
  injection, glyphrun specs, codemap, and more
- 🎨 **Nord theme** + full keyboard & mouse navigation

## Two ways to use it

### 1. Interactive TUI

```bash
./bin/monitor          # launches the Bubble Tea v2 TUI (8 tabs)
```

### 2. CLI + JSON (for scripts and agents)

Every subcommand supports `--json` for machine-readable output:

```bash
./bin/monitor snapshot --json | jq '.cpu'      # one-shot system snapshot
./bin/monitor watch --json                      # stream NDJSON metric events
./bin/monitor process 1234 --json               # detailed process info
./bin/monitor kill 1234                           # safety-checked (refuses protected/system PIDs)
./bin/monitor profile 1234 --type heap --json   # heap/cpu/goroutine/sample profile
./bin/monitor logs capture -- mycommand          # ingest a command's logs
./bin/monitor logs search "error" --json         # keyword search the log store
./bin/monitor stash --json                        # capture an incident bundle to fcheap
./bin/monitor investigate 1234 --json             # snapshot + profile + codemap-ranked stash
./bin/monitor history record                       # persist metric samples over time
./bin/monitor history query cpu.usage --since 1h --json   # time-series + trend stats
./bin/monitor baseline save pre-deploy             # capture a labeled snapshot
./bin/monitor diff pre-deploy                       # what changed since the baseline
./bin/monitor watch --webhook https://… --notify    # POST/desktop-notify on each alert
./bin/monitor doctor --json                       # ecosystem tool availability
./bin/monitor vault --project myapp -- mycommand  # run with tinyvault secrets injected
```

When Monitor launches a child process or spec it sets `MONITOR=1` and
`MONITOR_RUN_DIR=<dir>` so the child can detect it is being observed.

### 3. MCP server (for AI agents)

```bash
./bin/monitor mcp serve     # speaks MCP over stdio
```

Exposes 7 tools — 3 read-only (`monitor_snapshot`, `monitor_processes`,
`monitor_doctor`) and 4 mutating (`monitor_kill`, `monitor_profile_capture`,
`monitor_investigate`, `monitor_record`). Every mutating tool requires
`confirm: true` in its typed input; the handlers re-check it so hand-built
requests still get a structured refusal rather than acting.

## Installation

### Prerequisites

- Go 1.25 or higher
- macOS (Apple Silicon, full feature set) or Linux (core metrics; temperature
  falls back to the load estimate)
- [Task](https://taskfile.dev/) — optional task runner

### Build from source

```bash
go mod tidy

# Build (or use `task build`)
go build -o bin/monitor ./cmd/monitor

# Optionally install
sudo cp bin/monitor /usr/local/bin/
```

## TUI usage

### Keyboard shortcuts

| Key | Action |
|-----|--------|
| `q` / `Ctrl+C` | Quit |
| `→` / `Tab` / `l` | Next tab |
| `←` / `Shift+Tab` / `h` | Previous tab |
| `1`–`8` | Jump to a tab (Overview, CPU, Memory, Temperature, Disk, Network, Processes, Settings) |
| `/` | Search processes |
| `Space` | Toggle process selection |
| `Ctrl+A` / `Ctrl+D` | Select all / clear selection |
| `c` / `m` | Sort processes by CPU / memory |
| `k` / `x` | Terminate / force-kill selected (with confirmation) |

The v2 TUI is keyboard-driven; navigate tabs and the process table with the
keys above.

### Tabs

Overview, CPU, Memory, Temperature, Disk, Network, Processes, and Settings.
Settings are read from `~/.config/monitor/config.json` (written atomically).

## Safety features

Process termination is gated on a shared safety check used by the TUI, CLI,
and MCP server alike:

1. **Protected processes** — `launchd` (PID 1), `kernel_task`, `WindowServer`,
   `Finder`, `Dock`, etc. cannot be terminated.
2. **System processes** — root/`_mbsetupuser`-owned processes are refused too.
3. **Explicit confirmation** — the TUI prompts; the CLI needs `--yes`; the MCP
   tools need `confirm: true`.
4. **SIGTERM vs SIGKILL** — both modes are offered explicitly.

## Architecture

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
│   ├── ui/v2/                 # the TUI (Bubble Tea v2 / charm.land)
│   └── widgets/               # sparklines, gauges (lipgloss v2)
├── specs/                     # glyphrun behavioral specs
├── Taskfile.yml
└── README.md
```

## Technologies

- **Bubble Tea v2** (`charm.land/bubbletea/v2`) — TUI runtime
- **Lip Gloss v2** / **Bubbles v2** — styling + the process table
- **gopsutil** — cross-platform system metrics
- **Cobra** — CLI
- **MCP Go SDK** — the agent surface
- **veclite** — the embedded log store

## Limitations

- **Temperature** — real SMC readings need `powermetrics` (macOS) with cached
  sudo credentials; otherwise Monitor falls back to a CPU-load estimate and
  badges each reading `real` or `est`. On Linux, temperature is always the
  estimate.
- **CPU profiles** — the pprof scrape targets `localhost:6060`; heap/goroutine
  profiles are symbolicated, CPU profiles are returned as raw protobuf.

## Contributing

`task check` runs tidy + lint + test + a release build. Add a test alongside
every change, and a glyphrun spec in `specs/` for observable behavior. See
`AGENTS.md` and `CLAUDE.md` for the full contributor guide.

## License

MIT — see LICENSE.

## Acknowledgments

- [Charm](https://charm.sh/) for the TUI libraries
- [gopsutil](https://github.com/shirou/gopsutil) for system metrics
- [Nord Theme](https://www.nordtheme.com/) for the palette
